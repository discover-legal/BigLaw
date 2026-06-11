// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Top-level orchestrator — task lifecycle, phase sequencing, synthesis.

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/clientvoice"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/dytopo"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/learning"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/protocols"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Ensure interface is satisfied at compile time.
var _ agents.KnowledgeStore = (*knowledge.Adapter)(nil)

var phaseSequences = map[types.WorkflowType][]types.TaskPhase{
	types.WorkflowCounsel:       {types.PhaseIntake, types.PhaseResearch, types.PhaseDrafting, types.PhaseDelivery},
	types.WorkflowRoundtable:    {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseDrafting, types.PhaseReview, types.PhaseDelivery},
	types.WorkflowAdversarial:   {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseReview, types.PhaseVerification, types.PhaseDelivery},
	types.WorkflowReview:        {types.PhaseIntake, types.PhaseAnalysis, types.PhaseReview, types.PhaseVerification, types.PhaseDelivery},
	types.WorkflowTabulate:      {types.PhaseIntake, types.PhaseAnalysis, types.PhaseDelivery},
	types.WorkflowFullBench:     {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseDrafting, types.PhaseReview, types.PhaseVerification, types.PhaseDelivery},
	types.WorkflowLegalDesign:   {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseDrafting, types.PhaseReview, types.PhaseDelivery},
	types.WorkflowPreEngagement: {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseDelivery},
}

const (
	maxConcurrentTasks  = 10
	maxDescriptionChars = 20_000
)

// Orchestrator ties together all subsystems.
type Orchestrator struct {
	mu        sync.RWMutex
	tasks     map[string]*types.Task
	gateChans map[string]chan struct{} // taskID → signal channel for gate resolution

	cfg       *config.Config
	provReg   *providers.Registry
	costs     *cost.Store
	embedC    *embeddings.Client
	engine    *dytopo.Engine
	protocols *protocols.Runner
	registry  *agents.Registry
	memStore  *memory.InterRoundStore
	knowledge *knowledge.Store
	templates *templates.Store
	settings  *settings.SettingsStore
	profiles  *auth.ProfileStore
	clients   *clients.ClientStore
	time      *timekeeping.TimeStore
	learning  *learning.Engine
	tools     agents.ToolRegistry
	// clientVoice is optional (set via SetClientVoiceStore): the per-matter
	// advocacy brief pushed by the client-facing agent (Remy / CNTXT).
	clientVoice *clientvoice.Store

	// rootAgent is used for round goal generation and synthesis.
	rootAgentDef types.AgentDefinition
}

// ProgressEvent is emitted for SSE streams.
type ProgressEvent struct {
	TaskID string
	Type   string
	Data   interface{}
}

var progressSubs []chan ProgressEvent
var progressSubsMu sync.Mutex

func SubscribeProgress() chan ProgressEvent {
	ch := make(chan ProgressEvent, 32)
	progressSubsMu.Lock()
	progressSubs = append(progressSubs, ch)
	progressSubsMu.Unlock()
	return ch
}

func UnsubscribeProgress(ch chan ProgressEvent) {
	progressSubsMu.Lock()
	defer progressSubsMu.Unlock()
	for i, c := range progressSubs {
		if c == ch {
			progressSubs = append(progressSubs[:i], progressSubs[i+1:]...)
			close(ch)
			return
		}
	}
}

func emitProgress(taskID, typ string, data interface{}) {
	ev := ProgressEvent{TaskID: taskID, Type: typ, Data: data}
	progressSubsMu.Lock()
	defer progressSubsMu.Unlock()
	for _, ch := range progressSubs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// New creates an Orchestrator. Call Init() before use.
func New(
	cfg *config.Config,
	provReg *providers.Registry,
	costs *cost.Store,
	embedC *embeddings.Client,
	registry *agents.Registry,
	memStore *memory.InterRoundStore,
	knowledgeStore *knowledge.Store,
	templatesStore *templates.Store,
	settingsStore *settings.SettingsStore,
	profileStore *auth.ProfileStore,
	clientStore *clients.ClientStore,
	timeStore *timekeeping.TimeStore,
	learningEngine *learning.Engine,
	toolReg agents.ToolRegistry,
	rootDef types.AgentDefinition,
) *Orchestrator {
	o := &Orchestrator{
		tasks:        map[string]*types.Task{},
		gateChans:    map[string]chan struct{}{},
		cfg:          cfg,
		provReg:      provReg,
		costs:        costs,
		embedC:       embedC,
		registry:     registry,
		memStore:     memStore,
		knowledge:    knowledgeStore,
		templates:    templatesStore,
		settings:     settingsStore,
		profiles:     profileStore,
		clients:      clientStore,
		time:         timeStore,
		learning:     learningEngine,
		tools:        toolReg,
		rootAgentDef: rootDef,
	}
	o.protocols = protocols.New(cfg, provReg, costs)
	return o
}

func (o *Orchestrator) buildEngine() *dytopo.Engine {
	return dytopo.New(o.cfg, o.provReg, o.costs, o.embedC, dytopo.Options{
		Registry:  o.registry,
		Memory:    o.memStore,
		Knowledge: knowledge.NewAdapter(o.knowledge),
		Pinned:    []types.AgentDefinition{o.rootAgentDef},
		Tools:     o.tools,
		Learning:  o.learning,
	})
}

// Init loads persisted state and seeds the agent registry.
func (o *Orchestrator) Init(allAgents []types.AgentDefinition) error {
	if err := o.settings.Init(); err != nil {
		return fmt.Errorf("settings init: %w", err)
	}
	if err := o.profiles.Init(); err != nil {
		return fmt.Errorf("profiles init: %w", err)
	}
	if err := o.clients.Init(o.cfg.Persistence.ClientsFile); err != nil {
		return fmt.Errorf("clients init: %w", err)
	}
	o.time.Init(o.cfg.Persistence.TimeFile)

	if err := o.registry.Init(); err != nil {
		return fmt.Errorf("registry init: %w", err)
	}
	if err := o.memStore.Init(); err != nil {
		return fmt.Errorf("memory init: %w", err)
	}

	// Seed registry if empty.
	existing := o.registry.ListAll()
	if len(existing) == 0 {
		if err := o.registry.RegisterAll(allAgents); err != nil {
			return fmt.Errorf("seed agents: %w", err)
		}
	}

	if err := o.learning.Init(o.cfg.Persistence.LearningFile); err != nil {
		return fmt.Errorf("learning init: %w", err)
	}

	o.restoreTasks()

	o.engine = o.buildEngine()
	return nil
}

// ─── Task management ──────────────────────────────────────────────────────────

type SubmitParams struct {
	Description        string
	WorkflowType       types.WorkflowType
	DocumentIDs        []string
	ClientNumber       string
	MatterNumber       string
	Jurisdiction       string
	CreatedByProfileID string
}

func (o *Orchestrator) SubmitTask(params SubmitParams) (*types.Task, error) {
	if len(params.Description) > maxDescriptionChars {
		return nil, fmt.Errorf("description exceeds %d character limit", maxDescriptionChars)
	}
	o.mu.RLock()
	running := 0
	for _, t := range o.tasks {
		if t.Status == "running" {
			running++
		}
	}
	o.mu.RUnlock()
	if running >= maxConcurrentTasks {
		return nil, fmt.Errorf("server at capacity: %d tasks already running", running)
	}

	phases, ok := phaseSequences[params.WorkflowType]
	if !ok {
		return nil, fmt.Errorf("unknown workflowType %q", params.WorkflowType)
	}

	task := &types.Task{
		ID:                 uuid.New().String(),
		Description:        params.Description,
		Jurisdiction:       strings.ToUpper(strings.TrimSpace(params.Jurisdiction)),
		ClientNumber:       params.ClientNumber,
		MatterNumber:       params.MatterNumber,
		DocumentIDs:        params.DocumentIDs,
		CreatedByProfileID: params.CreatedByProfileID,
		WorkflowType:       params.WorkflowType,
		Status:             "pending",
		CurrentPhase:       phases[0],
		CurrentRound:       0,
		MaxRounds:          o.cfg.DyTopo.MaxRounds,
		ActiveAgentIDs:     []string{},
		Rounds:             []types.RoundState{},
		Findings:           []types.Finding{},
		PendingGates:       []types.GateRequest{},
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	// Open a time entry if a profile is associated.
	if params.CreatedByProfileID != "" {
		if profile := o.profiles.Get(params.CreatedByProfileID); profile != nil {
			entry := o.time.Open(types.TimeEntry{
				ProfileID:    profile.ID,
				ProfileName:  profile.Name,
				TaskID:       task.ID,
				MatterNumber: task.MatterNumber,
				ClientNumber: task.ClientNumber,
				Description:  "Task: " + task.Description[:min(200, len(task.Description))],
				Event:        "task_run",
				StartedAt:    time.Now(),
			})
			task.ActiveTimeEntryID = entry.ID
		}
	}

	o.mu.Lock()
	o.tasks[task.ID] = task
	o.gateChans[task.ID] = make(chan struct{}, 8)
	o.mu.Unlock()

	audit.Default.Write(audit.WriteRequest{
		Event:   "task.created",
		ActorID: orSystem(task.CreatedByProfileID),
		TaskID:  task.ID,
		Data:    map[string]interface{}{"description": params.Description[:min(200, len(params.Description))], "workflowType": params.WorkflowType},
	})
	o.persistTasks()

	go o.runTask(task)
	return task, nil
}

// Settings exposes the admin settings store (backing GET/PUT /settings).
func (o *Orchestrator) Settings() *settings.SettingsStore {
	return o.settings
}

// SetClientVoiceStore attaches the client-voice store. Optional: without it
// gates simply carry no client-voice note.
func (o *Orchestrator) SetClientVoiceStore(cv *clientvoice.Store) {
	o.clientVoice = cv
}

// ClientVoice exposes the client-voice store (may be nil).
func (o *Orchestrator) ClientVoice() *clientvoice.Store {
	return o.clientVoice
}

// Providers exposes the model provider registry for API-layer engines
// (redline, headnotes, precedents, reports) that make their own model calls.
func (o *Orchestrator) Providers() *providers.Registry {
	return o.provReg
}

// Costs exposes the cost store for API-layer engines.
func (o *Orchestrator) Costs() *cost.Store {
	return o.costs
}

// MemoryStore exposes the inter-round memory store (backing POST /memory/query).
func (o *Orchestrator) MemoryStore() *memory.InterRoundStore {
	return o.memStore
}

// Templates exposes the template store.
func (o *Orchestrator) Templates() *templates.Store {
	return o.templates
}

func (o *Orchestrator) GetTask(id string) *types.Task {
	o.mu.RLock()
	defer o.mu.RUnlock()
	t := o.tasks[id]
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func (o *Orchestrator) ListTasks() []*types.Task {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*types.Task, 0, len(o.tasks))
	for _, t := range o.tasks {
		cp := *t
		out = append(out, &cp)
	}
	return out
}

func (o *Orchestrator) DeleteTask(id string) bool {
	o.mu.Lock()
	_, existed := o.tasks[id]
	delete(o.tasks, id)
	delete(o.gateChans, id)
	o.mu.Unlock()
	if existed {
		o.persistTasks()
		audit.Default.Write(audit.WriteRequest{Event: "task.deleted", ActorID: audit.ActorSystem, TaskID: id, Data: map[string]interface{}{}})
	}
	return existed
}

func (o *Orchestrator) AssignLawyers(taskID string, lawyerIDs []string, actorID string) *types.Task {
	o.mu.Lock()
	task := o.tasks[taskID]
	if task == nil {
		o.mu.Unlock()
		return nil
	}
	task.AssignedLawyerIDs = lawyerIDs
	task.UpdatedAt = time.Now()
	o.mu.Unlock()
	o.persistTasks()
	audit.Default.Write(audit.WriteRequest{Event: "task.assigned", ActorID: orSystem(actorID), TaskID: taskID, Data: map[string]interface{}{"lawyerIds": lawyerIDs}})
	cp := *task
	return &cp
}

// ─── Gate management ──────────────────────────────────────────────────────────

func (o *Orchestrator) ApproveGate(taskID, gateID string, note, reviewerProfileID string) error {
	o.mu.Lock()
	task := o.tasks[taskID]
	if task == nil {
		o.mu.Unlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	for i := range task.PendingGates {
		if task.PendingGates[i].ID == gateID {
			task.PendingGates[i].Status = "approved"
			task.PendingGates[i].ReviewerNote = note
			now := time.Now()
			task.PendingGates[i].ReviewedAt = &now
			task.UpdatedAt = time.Now()
			break
		}
	}
	ch := o.gateChans[taskID]
	o.mu.Unlock()
	audit.Default.Write(audit.WriteRequest{Event: "gate.approved", ActorID: orSystem(reviewerProfileID), TaskID: taskID, Data: map[string]interface{}{"gateId": gateID, "note": note}})
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	o.persistTasks()
	return nil
}

func (o *Orchestrator) RejectGate(taskID, gateID, reason, reviewerProfileID string) error {
	o.mu.Lock()
	task := o.tasks[taskID]
	if task == nil {
		o.mu.Unlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	var findingID string
	for i := range task.PendingGates {
		if task.PendingGates[i].ID == gateID {
			task.PendingGates[i].Status = "rejected"
			task.PendingGates[i].ReviewerNote = reason
			now := time.Now()
			task.PendingGates[i].ReviewedAt = &now
			findingID = task.PendingGates[i].FindingID
			task.UpdatedAt = time.Now()
			break
		}
	}
	if findingID != "" {
		filtered := task.Findings[:0]
		for _, f := range task.Findings {
			if f.ID != findingID {
				filtered = append(filtered, f)
			}
		}
		task.Findings = filtered
	}
	ch := o.gateChans[taskID]
	o.mu.Unlock()
	audit.Default.Write(audit.WriteRequest{Event: "gate.rejected", ActorID: orSystem(reviewerProfileID), TaskID: taskID, Data: map[string]interface{}{"gateId": gateID, "reason": reason}})
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	o.persistTasks()
	return nil
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (o *Orchestrator) ListTemplates() []types.TaskTemplate {
	return o.templates.List()
}

func (o *Orchestrator) SubmitFromTemplate(templateID string, subs map[string]string, documentIDs []string, refs SubmitParams) (*types.Task, error) {
	t := o.templates.Get(templateID)
	if t == nil {
		return nil, fmt.Errorf("template not found: %s", templateID)
	}
	desc, wfType := templates.InstantiateTemplate(*t, subs)
	refs.Description = desc
	refs.WorkflowType = types.WorkflowType(wfType)
	refs.DocumentIDs = documentIDs
	return o.SubmitTask(refs)
}

// ─── Internal task runner ─────────────────────────────────────────────────────

func (o *Orchestrator) runTask(task *types.Task) {
	task.Status = "running"
	emitProgress(task.ID, "started", map[string]interface{}{"taskId": task.ID, "workflowType": task.WorkflowType})
	audit.Default.Write(audit.WriteRequest{Event: "task.started", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"workflowType": task.WorkflowType}})

	phases := phaseSequences[task.WorkflowType]
	var runErr error

	for _, phase := range phases {
		task.CurrentPhase = phase
		task.UpdatedAt = time.Now()
		emitProgress(task.ID, "phase", map[string]interface{}{"phase": phase})

		if err := o.runPhase(task, phase); err != nil {
			runErr = err
			break
		}

		// Wait for any pending gates.
		hasPending := false
		for _, g := range task.PendingGates {
			if g.Status == "pending" {
				hasPending = true
				break
			}
		}
		if hasPending {
			task.Status = "awaiting_gate"
			o.waitForGates(task)
			task.Status = "running"
		}
	}

	if runErr != nil {
		task.Status = "failed"
		task.Error = runErr.Error()
		if task.ActiveTimeEntryID != "" {
			o.time.Close(task.ActiveTimeEntryID)
			task.ActiveTimeEntryID = ""
		}
		emitProgress(task.ID, "failed", map[string]interface{}{"error": runErr.Error()})
		audit.Default.Write(audit.WriteRequest{Event: "task.failed", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"error": runErr.Error()}})
		o.persistTasks()
		return
	}

	// Final synthesis.
	output, err := o.synthesise(task)
	if err != nil {
		output = fmt.Sprintf("Synthesis error: %v", err)
	}
	task.Output = output

	// Tabulation for tabulate workflow.
	if task.WorkflowType == types.WorkflowTabulate {
		if table, err := o.tabulate(task); err == nil && table != nil {
			task.Table = table
		}
	}

	task.Status = "complete"
	now := time.Now()
	task.CompletedAt = &now
	task.UpdatedAt = now

	if task.ActiveTimeEntryID != "" {
		o.time.Close(task.ActiveTimeEntryID)
		task.ActiveTimeEntryID = ""
	}

	o.recordAgentOutcomes(task)

	emitProgress(task.ID, "complete", map[string]interface{}{"findings": len(task.Findings), "output": task.Output[:min(200, len(task.Output))]})
	audit.Default.Write(audit.WriteRequest{Event: "task.complete", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"findings": len(task.Findings)}})
	o.persistTasks()
}

// recordAgentOutcomes feeds completed-task results back into the registry
// success scores and the Q-learning table, grouped per (agent, phase).
// Challenged-but-unresolved findings earn 30% of their stated confidence.
func (o *Orchestrator) recordAgentOutcomes(task *types.Task) {
	if len(task.Findings) == 0 {
		return
	}
	phaseByFinding := map[string]types.TaskPhase{}
	for _, r := range task.Rounds {
		for _, f := range r.Findings {
			phaseByFinding[f.ID] = r.Goal.Phase
		}
	}

	type bucket struct {
		phase  types.TaskPhase
		scores []float64
	}
	buckets := map[string]*bucket{}
	for _, f := range task.Findings {
		phase, ok := phaseByFinding[f.ID]
		if !ok {
			phase = task.CurrentPhase
		}
		key := f.AgentID + "::" + string(phase)
		b := buckets[key]
		if b == nil {
			b = &bucket{phase: phase}
			buckets[key] = b
		}
		effective := f.Confidence
		if f.Challenged && !f.Resolved {
			effective *= 0.3
		}
		b.scores = append(b.scores, effective)
	}

	phases := phaseSequences[task.WorkflowType]
	for key, b := range buckets {
		agentID := strings.SplitN(key, "::", 2)[0]
		sum := 0.0
		for _, s := range b.scores {
			sum += s
		}
		avg := sum / float64(len(b.scores))

		nextPhase := b.phase
		for i, p := range phases {
			if p == b.phase && i+1 < len(phases) {
				nextPhase = phases[i+1]
				break
			}
		}

		o.registry.RecordOutcome([]string{agentID}, avg)
		if err := o.learning.RecordEpisode(learning.EpisodeOpts{
			Phase:        b.phase,
			NextPhase:    nextPhase,
			Jurisdiction: task.Jurisdiction,
			WorkflowType: task.WorkflowType,
			AgentID:      agentID,
			Reward:       avg,
			Done:         true,
		}); err != nil {
			slog.Warn("learning: record episode failed", "agent", agentID, "err", err)
		}
	}
}

func (o *Orchestrator) runPhase(task *types.Task, phase types.TaskPhase) error {
	audit.Default.Write(audit.WriteRequest{Event: "phase.start", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"phase": phase}})

	goal, err := o.generateRoundGoal(task, phase)
	if err != nil {
		return fmt.Errorf("generate round goal: %w", err)
	}
	task.CurrentRound++
	goal.Round = task.CurrentRound

	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	var lawyerTone *types.ToneProfile
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil {
			lawyerTone = p.ToneProfile
		}
	}

	var billingCtx *dytopo.AgentBillingCtx
	if o.cfg.AgentBilling.Enabled && primaryProfileID != "" {
		billingCtx = &dytopo.AgentBillingCtx{
			ResponsibleLawyerID: primaryProfileID,
			MatterNumber:        task.MatterNumber,
			ClientNumber:        task.ClientNumber,
		}
		if p := o.profiles.Get(primaryProfileID); p != nil {
			billingCtx.ResponsibleLawyerName = p.Name
		}
	}

	roundState, err := o.engine.RunRound(task, goal, lawyerTone, billingCtx)
	if err != nil {
		return fmt.Errorf("run round: %w", err)
	}
	task.Rounds = append(task.Rounds, *roundState)

	// Build source-text map for citation gate.
	sourceTexts := map[string]string{}
	for _, docID := range task.DocumentIDs {
		if text, err := o.knowledge.GetFullText(docID); err == nil && text != "" {
			sourceTexts[docID] = text
		}
	}

	passed, _ := o.protocols.ApplyCitationGate(roundState.Findings, sourceTexts)

	// Debate each finding.
	debated := make([]types.Finding, len(passed))
	for i, f := range passed {
		d, _ := o.protocols.RunDebate(f, task.ID)
		debated[i] = d
	}

	// Verification pipeline.
	for i := range debated {
		if result, err := o.protocols.RunVerification(debated[i], task.ID); err == nil {
			debated[i].VerificationResult = &result
		}
	}

	task.Findings = append(task.Findings, debated...)

	gates := o.protocols.IdentifyGates(task.ID, debated)
	o.annotateGatesWithClientVoice(task, gates)
	task.PendingGates = append(task.PendingGates, gates...)

	task.UpdatedAt = time.Now()
	emitProgress(task.ID, "round", map[string]interface{}{"round": task.CurrentRound, "phase": phase, "findings": len(debated), "gates": len(gates)})
	audit.Default.Write(audit.WriteRequest{Event: "phase.complete", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"phase": phase, "findings": len(debated), "gates": len(gates)}})
	return nil
}

// annotateGatesWithClientVoice attaches Remy's client-advocacy read to each
// gate when the matter has an advocacy brief. With a provider available the
// note is a Haiku assessment of the finding against the client's stated
// goals; if the model call fails the brief itself is attached verbatim so
// the reviewer still sees the client's voice.
func (o *Orchestrator) annotateGatesWithClientVoice(task *types.Task, gates []types.GateRequest) {
	// Admin-toggleable: some lawyers don't want client-voice hints at gates.
	if !o.cfg.ClientVoice.GateNotes {
		return
	}
	if o.clientVoice == nil || task.MatterNumber == "" || len(gates) == 0 {
		return
	}
	voice := o.clientVoice.Voice(task.MatterNumber)
	if voice == nil || len(voice.Entries) == 0 {
		return
	}
	lines := make([]string, 0, len(voice.Entries))
	for _, e := range voice.Entries {
		lines = append(lines, fmt.Sprintf("- [%s] %s", e.Category, e.Note))
	}
	brief := strings.Join(lines, "\n")

	for i := range gates {
		note := o.assessClientVoice(task, brief, gates[i].Finding)
		if note == "" {
			note = "Client's stated position (via Remy, the client advocate):\n" + brief
		}
		gates[i].ClientVoiceNote = note
	}
}

// assessClientVoice asks Haiku, speaking as the client's advocate, whether a
// gated finding aligns with or cuts against the client's stated goals.
// Returns "" on any failure — the caller falls back to the verbatim brief.
func (o *Orchestrator) assessClientVoice(task *types.Task, brief string, f types.Finding) string {
	tier := types.TierTool
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskVerification,
	})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return ""
	}
	prompt := fmt.Sprintf(`THE CLIENT'S STATED POSITION (captured during intake, in their own words):
%s

A FINDING ON THEIR MATTER IS AWAITING HUMAN REVIEW:
%s

In 2-3 sentences, tell the reviewing lawyer how this finding sits with what
the client actually wants: flag any conflict with their goals, concerns,
constraints, or preferences, or confirm alignment. Be concrete and cite the
client's own words where useful. Do not restate the finding.`, brief, f.Content)

	resp, err := prov.Chat(providers.ChatParams{
		Model:     routing.ResolveModelID(model),
		MaxTokens: 250,
		System: "You are Remy, the client's advocate. You do not work for the firm — " +
			"you speak for the client. Your notes help the reviewing lawyer serve " +
			"the client's actual interests.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	})
	if err != nil {
		return ""
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextClientVoice, task.ID)
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return strings.TrimSpace(b.Text)
		}
	}
	return ""
}

func (o *Orchestrator) generateRoundGoal(task *types.Task, phase types.TaskPhase) (types.RoundGoal, error) {
	priorPhases := make([]string, 0, len(task.Rounds))
	for _, r := range task.Rounds {
		priorPhases = append(priorPhases, string(r.Goal.Phase))
	}
	prompt := fmt.Sprintf(`TASK: %s

WORKFLOW: %s
CURRENT PHASE: %s
PRIOR PHASES COMPLETED: %s
FINDINGS SO FAR: %d

Generate a specific, actionable round goal for the %s phase.
Format:
DESCRIPTION: <one paragraph describing what agents should do this round>
EXPECTED_OUTPUT_1: <first expected output>
EXPECTED_OUTPUT_2: <second expected output>
EXPECTED_OUTPUT_3: <third expected output>`,
		task.Description, task.WorkflowType, phase,
		strings.Join(priorPhases, ", "), len(task.Findings), phase)

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskSynthesis,
	})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return types.RoundGoal{}, err
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   600,
		System:      o.rootAgentDef.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	})
	if err != nil {
		// Fall back to a basic goal.
		return types.RoundGoal{
			ID:          uuid.New().String(),
			Round:       task.CurrentRound,
			Phase:       phase,
			Description: fmt.Sprintf("Execute the %s phase for: %s", phase, task.Description[:min(200, len(task.Description))]),
		}, nil
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextRoundGoal, task.ID)

	text := ""
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
			break
		}
	}

	description := fmt.Sprintf("Execute the %s phase", phase)
	if m := regexpFindSubmatch(`(?i)DESCRIPTION:\s*([\s\S]+?)(?:EXPECTED_OUTPUT|$)`, text); len(m) > 1 {
		description = strings.TrimSpace(m[1])
	}
	var expectedOutputs []string
	for _, m := range regexpFindAllSubmatch(`(?i)EXPECTED_OUTPUT_\d+:\s*(.+)`, text, 5) {
		if len(m) > 1 {
			expectedOutputs = append(expectedOutputs, strings.TrimSpace(m[1]))
		}
	}

	return types.RoundGoal{
		ID:              uuid.New().String(),
		Round:           task.CurrentRound,
		Phase:           phase,
		Description:     description,
		ExpectedOutputs: expectedOutputs,
	}, nil
}

func (o *Orchestrator) synthesise(task *types.Task) (string, error) {
	var filteredFindings []types.Finding
	rejectedIDs := map[string]bool{}
	for _, g := range task.PendingGates {
		if g.Status == "rejected" {
			rejectedIDs[g.FindingID] = true
		}
	}
	for _, f := range task.Findings {
		if !rejectedIDs[f.ID] {
			filteredFindings = append(filteredFindings, f)
		}
	}

	var lines []string
	for i, f := range filteredFindings {
		content := f.Content
		if len(content) > 5000 {
			content = content[:5000]
		}
		lines = append(lines, fmt.Sprintf("[%d] (%s, Round %d) %s", i+1, f.AgentName, f.Round, content))
	}
	findingsSummary := strings.Join(lines, "\n\n")
	if len(findingsSummary) > 200_000 {
		findingsSummary = findingsSummary[:200_000]
	}

	toneBlock := ""
	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil && p.ToneProfile != nil {
			snippet := p.ToneProfile.InjectionSnippet
			if len(snippet) > 2000 {
				snippet = snippet[:2000]
			}
			toneBlock = "\nLAWYER TONE PROFILE — write the final output in this voice:\n" + snippet + "\n"
		}
	}

	prompt := fmt.Sprintf(`TASK: %s

ALL FINDINGS FROM ALL ROUNDS:
%s
%s
Produce the final legal output for this task. Structure appropriately for the workflow type: %s.
Every claim must trace to a specific finding number from the list above.`,
		task.Description, findingsSummary, toneBlock, task.WorkflowType)

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskSynthesis,
	})
	useThinking := routing.ShouldUseThinking(model, routing.TaskSynthesis, &tier, routing.ComplexityHigh)

	maxTokens := 4000
	if useThinking {
		maxTokens = 16000
	}

	prov, err := o.provReg.Get(model)
	if err != nil {
		return "", err
	}
	chatParams := providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   maxTokens,
		System:      o.rootAgentDef.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	}
	if useThinking {
		chatParams.Thinking = &providers.ThinkingConfig{BudgetTokens: o.cfg.Anthropic.ThinkingBudgetTokens}
	}
	resp, err := prov.Chat(chatParams)
	if err != nil {
		return "", err
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextSynthesis, task.ID)

	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return b.Text, nil
		}
	}
	return "", nil
}

func (o *Orchestrator) tabulate(task *types.Task) (*types.TaskTable, error) {
	filteredFindings := make([]types.Finding, 0, len(task.Findings))
	rejectedIDs := map[string]bool{}
	for _, g := range task.PendingGates {
		if g.Status == "rejected" {
			rejectedIDs[g.FindingID] = true
		}
	}
	for _, f := range task.Findings {
		if !rejectedIDs[f.ID] {
			filteredFindings = append(filteredFindings, f)
		}
	}
	if len(filteredFindings) == 0 {
		return nil, nil
	}

	var sb strings.Builder
	for _, f := range filteredFindings {
		c := f.Content
		if len(c) > 500 {
			c = c[:500]
		}
		sb.WriteString(fmt.Sprintf("id=%s | %s (R%d, conf %.2f): %s\n\n", f.ID, f.AgentName, f.Round, f.Confidence, c))
	}

	prompt := fmt.Sprintf(`TASK: %s

FINDINGS:
%s

Extract these findings into a structured table. Choose 3-6 columns appropriate for this subject matter.
Respond with ONLY valid JSON (no prose, no markdown fences):
{
  "columns": ["Column A", "Column B"],
  "rows": [
    { "Column A": "value", "Column B": "value", "_findingIds": ["<finding id>"] }
  ]
}`, task.Description, sb.String())

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return nil, err
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   4000,
		System:      o.rootAgentDef.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
	})
	if err != nil {
		return nil, err
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextTabulate, task.ID)

	text := ""
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
			break
		}
	}
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "```json", ""), "```", ""))
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start == -1 || end <= start {
		return nil, fmt.Errorf("no JSON in tabulate response")
	}
	var parsed struct {
		Columns []string                 `json:"columns"`
		Rows    []map[string]interface{} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil {
		return nil, err
	}

	rows := make([]map[string]string, 0, len(parsed.Rows))
	var sourceFindingIDs []string
	for _, row := range parsed.Rows {
		outRow := map[string]string{}
		for _, col := range parsed.Columns {
			if v, ok := row[col]; ok {
				outRow[col] = fmt.Sprintf("%v", v)
			}
		}
		if ids, ok := row["_findingIds"].([]interface{}); ok {
			for _, id := range ids {
				idStr := fmt.Sprintf("%v", id)
				outRow["_findingId"] = idStr
				sourceFindingIDs = append(sourceFindingIDs, idStr)
			}
		}
		rows = append(rows, outRow)
	}

	return &types.TaskTable{
		Columns:          parsed.Columns,
		Rows:             rows,
		SourceFindingIDs: sourceFindingIDs,
		GeneratedAt:      time.Now(),
	}, nil
}

// ─── Gate waiting ─────────────────────────────────────────────────────────────

func (o *Orchestrator) waitForGates(task *types.Task) {
	o.mu.RLock()
	ch := o.gateChans[task.ID]
	o.mu.RUnlock()
	if ch == nil {
		return
	}
	for {
		allResolved := true
		for _, g := range task.PendingGates {
			if g.Status == "pending" {
				allResolved = false
				break
			}
		}
		if allResolved {
			return
		}
		select {
		case <-ch:
		case <-time.After(30 * time.Second):
		}
	}
}

// ─── Persistence ──────────────────────────────────────────────────────────────

func (o *Orchestrator) persistTasks() {
	o.mu.RLock()
	tasksCopy := make([]*types.Task, 0, len(o.tasks))
	for _, t := range o.tasks {
		cp := *t
		tasksCopy = append(tasksCopy, &cp)
	}
	o.mu.RUnlock()

	data, err := json.MarshalIndent(tasksCopy, "", "  ")
	if err != nil {
		return
	}
	tmp := o.cfg.Persistence.TasksFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, o.cfg.Persistence.TasksFile)
}

func (o *Orchestrator) restoreTasks() {
	data, err := os.ReadFile(o.cfg.Persistence.TasksFile)
	if err != nil {
		return
	}
	var items []*types.Task
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	o.mu.Lock()
	for _, t := range items {
		if _, ok := phaseSequences[t.WorkflowType]; !ok {
			continue
		}
		o.tasks[t.ID] = t
		o.gateChans[t.ID] = make(chan struct{}, 8)
	}
	o.mu.Unlock()
}

// ─── Cost recording ───────────────────────────────────────────────────────────

func (o *Orchestrator) recordCost(resp *providers.ChatResponse, modelID string, ctx cost.CostContext, taskID string) {
	isLocal := routing.IsOllamaModel(modelID) || routing.IsLocalModel(modelID)
	var costUSD *float64
	var wh *float64
	var watts *int
	if !isLocal {
		cw, cr := 0, 0
		if resp.Usage.CacheWriteTokens != nil {
			cw = *resp.Usage.CacheWriteTokens
		}
		if resp.Usage.CacheReadTokens != nil {
			cr = *resp.Usage.CacheReadTokens
		}
		costUSD = cost.CalcCostUSD(modelID, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	} else {
		w := cost.CalcWattHours(o.cfg.Local.InferenceWatts, resp.DurationMs)
		wh = &w
		watts = &o.cfg.Local.InferenceWatts
	}
	provider := "anthropic"
	if routing.IsOllamaModel(modelID) {
		provider = "ollama"
	} else if routing.IsLocalModel(modelID) {
		provider = "local"
	}
	o.costs.Record(cost.RecordRequest{
		Model:          modelID,
		Provider:       provider,
		InputTokens:    resp.Usage.InputTokens,
		OutputTokens:   resp.Usage.OutputTokens,
		CostUSD:        costUSD,
		EstimatedWh:    wh,
		EstimatedWatts: watts,
		DurationMs:     resp.DurationMs,
		Context:        ctx,
		TaskID:         taskID,
	})
}

// ─── Utilities ────────────────────────────────────────────────────────────────

func orSystem(id string) string {
	if id == "" {
		return audit.ActorSystem
	}
	return id
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func regexpFindSubmatch(pattern, text string) []string {
	re := regexp.MustCompile(pattern)
	return re.FindStringSubmatch(text)
}

func regexpFindAllSubmatch(pattern, text string, n int) [][]string {
	re := regexp.MustCompile(pattern)
	return re.FindAllStringSubmatch(text, n)
}
