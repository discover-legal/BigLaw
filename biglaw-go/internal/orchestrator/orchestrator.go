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

	"github.com/discover-legal/biglaw-go/internal/adapters"
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
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/discover-legal/biglaw-go/internal/writer"
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
				Description:  "Task: " + strutil.Truncate(task.Description, 200),
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
		Data:    map[string]interface{}{"description": strutil.Truncate(params.Description, 200), "workflowType": params.WorkflowType},
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

// update applies fn to a live task under the write lock. Every write to a
// task after it has been handed to runTask must go through here (or hold
// o.mu directly): GetTask/ListTasks/persistTasks hand out shallow copies
// that are marshaled outside the lock, so unsynchronized writes would race
// with those reads. Slice-typed fields that handlers rewrite (Findings,
// PendingGates) must be replaced copy-on-write, never mutated in place.
func (o *Orchestrator) update(task *types.Task, fn func(t *types.Task)) {
	o.mu.Lock()
	fn(task)
	o.mu.Unlock()
}

// snapshot returns a consistent shallow copy of a live task for use by
// long-running readers (synthesis, tabulation) that must not hold the lock.
func (o *Orchestrator) snapshot(task *types.Task) *types.Task {
	o.mu.RLock()
	cp := *task
	o.mu.RUnlock()
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
	// Copy-on-write: shallow task copies returned by GetTask/persistTasks
	// are marshaled outside the lock, so the shared backing array must not
	// be written in place.
	gates := make([]types.GateRequest, len(task.PendingGates))
	copy(gates, task.PendingGates)
	for i := range gates {
		if gates[i].ID == gateID {
			gates[i].Status = "approved"
			gates[i].ReviewerNote = note
			now := time.Now()
			gates[i].ReviewedAt = &now
			task.UpdatedAt = time.Now()
			break
		}
	}
	task.PendingGates = gates
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
	// Copy-on-write for both slices — see ApproveGate. The previous
	// in-place [:0] compaction corrupted shallow copies being marshaled
	// concurrently on other goroutines.
	var findingID string
	gates := make([]types.GateRequest, len(task.PendingGates))
	copy(gates, task.PendingGates)
	for i := range gates {
		if gates[i].ID == gateID {
			gates[i].Status = "rejected"
			gates[i].ReviewerNote = reason
			now := time.Now()
			gates[i].ReviewedAt = &now
			findingID = gates[i].FindingID
			task.UpdatedAt = time.Now()
			break
		}
	}
	task.PendingGates = gates
	if findingID != "" {
		filtered := make([]types.Finding, 0, len(task.Findings))
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
	o.update(task, func(t *types.Task) { t.Status = "running" })
	emitProgress(task.ID, "started", map[string]interface{}{"taskId": task.ID, "workflowType": task.WorkflowType})
	audit.Default.Write(audit.WriteRequest{Event: "task.started", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"workflowType": task.WorkflowType}})

	phases := phaseSequences[task.WorkflowType]
	var runErr error

	for _, phase := range phases {
		// CurrentRound is written only by this goroutine (under the lock,
		// for readers' sake), so reading it here is safe.
		if task.CurrentRound >= task.MaxRounds {
			slog.Warn("task hit maxRounds cap", "taskId", task.ID, "maxRounds", task.MaxRounds)
			break
		}
		o.update(task, func(t *types.Task) {
			t.CurrentPhase = phase
			t.UpdatedAt = time.Now()
		})
		emitProgress(task.ID, "phase", map[string]interface{}{"phase": phase})

		if err := o.runPhase(task, phase); err != nil {
			runErr = err
			break
		}

		// Wait for any pending gates. PendingGates is rewritten by the
		// gate handlers, so read it under the lock.
		hasPending := false
		o.mu.RLock()
		for _, g := range task.PendingGates {
			if g.Status == "pending" {
				hasPending = true
				break
			}
		}
		o.mu.RUnlock()
		if hasPending {
			o.update(task, func(t *types.Task) { t.Status = "awaiting_gate" })
			o.waitForGates(task)
			o.update(task, func(t *types.Task) { t.Status = "running" })
		}
	}

	if runErr != nil {
		o.update(task, func(t *types.Task) {
			t.Status = "failed"
			t.Error = runErr.Error()
		})
		if task.ActiveTimeEntryID != "" {
			o.time.Close(task.ActiveTimeEntryID)
			o.update(task, func(t *types.Task) { t.ActiveTimeEntryID = "" })
		}
		emitProgress(task.ID, "failed", map[string]interface{}{"error": runErr.Error()})
		audit.Default.Write(audit.WriteRequest{Event: "task.failed", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"error": runErr.Error()}})
		o.persistTasks()
		return
	}

	// Final synthesis. Reads Findings/PendingGates, which gate handlers
	// rewrite concurrently — work from a locked snapshot.
	output, err := o.synthesise(o.snapshot(task))
	if err != nil {
		output = fmt.Sprintf("Synthesis error: %v", err)
	}
	o.update(task, func(t *types.Task) { t.Output = output })

	// Tabulation for tabulate workflow.
	if task.WorkflowType == types.WorkflowTabulate {
		if table, err := o.tabulate(o.snapshot(task)); err == nil && table != nil {
			o.update(task, func(t *types.Task) { t.Table = table })
		}
	}

	o.update(task, func(t *types.Task) {
		t.Status = "complete"
		now := time.Now()
		t.CompletedAt = &now
		t.UpdatedAt = now
	})

	if task.ActiveTimeEntryID != "" {
		o.time.Close(task.ActiveTimeEntryID)
		o.update(task, func(t *types.Task) { t.ActiveTimeEntryID = "" })
	}

	final := o.snapshot(task)
	o.recordAgentOutcomes(final)

	emitProgress(task.ID, "complete", map[string]interface{}{"findings": len(final.Findings), "output": strutil.Truncate(final.Output, 200)})
	audit.Default.Write(audit.WriteRequest{Event: "task.complete", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"findings": len(final.Findings)}})
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
	o.update(task, func(t *types.Task) { t.CurrentRound++ })
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
	// Build source-text map for the citation gate, keyed by every identifier a
	// model might cite: the internal UUID, the document title/filename, and the
	// normalised title. Models cite by title ("sec-referral-notice.docx"), not
	// UUID, so without the title keys mechanical verification never matches and
	// every finding is falsely flagged as unverified.
	sourceTexts := map[string]string{}
	for _, docID := range task.DocumentIDs {
		text, err := o.knowledge.GetFullText(docID)
		if err != nil || text == "" {
			continue
		}
		sourceTexts[docID] = text
		if doc := o.knowledge.GetByID(docID); doc != nil && strings.TrimSpace(doc.Title) != "" {
			sourceTexts[doc.Title] = text
			if k := protocols.NormalizeSourceKey(doc.Title); k != "" {
				sourceTexts[k] = text
			}
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

	gates := o.protocols.IdentifyGates(task.ID, debated)
	o.annotateGatesWithClientVoice(task, gates)

	// Fold debate/verification outcomes back into the round record, then
	// publish everything in one locked write. The round is appended only
	// now: the citation gate mutates findings in place, which must not
	// happen on data already visible to marshaling readers.
	byID := map[string]types.Finding{}
	for _, f := range debated {
		byID[f.ID] = f
	}
	for i := range roundState.Findings {
		if f, ok := byID[roundState.Findings[i].ID]; ok {
			roundState.Findings[i] = f
		}
	}

	o.update(task, func(t *types.Task) {
		t.Rounds = append(t.Rounds, *roundState)
		t.Findings = append(t.Findings, debated...)
		t.PendingGates = append(t.PendingGates, gates...)
		t.UpdatedAt = time.Now()
	})
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
	safeDesc := adapters.SanitizePromptContent(task.Description)
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
		safeDesc, task.WorkflowType, phase,
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
			Description: fmt.Sprintf("Execute the %s phase for: %s", phase, strutil.Truncate(safeDesc, 200)),
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
	expectedOutputs := []string{}
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
	safeDesc := adapters.SanitizePromptContent(task.Description)
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

	// When the findings won't fit a single synthesis call's input budget, write the
	// deliverable via the scoped multi-pass writer (cluster → tight agentic drafters
	// that pull their own findings via search_findings → stitch) instead of dumping
	// every finding into one prompt — which truncates to the window and yields an
	// empty result on small-context local models. The monolith path below still
	// handles small tasks / large-context models in one clean call.
	estTokens := 0
	for _, f := range filteredFindings {
		estTokens += strutil.EstimateTokens(f.Content) + 40
	}
	if estTokens > synthesisWriterBudgetTokens {
		if out, err := o.writeDeliverable(task, filteredFindings); err == nil && strings.TrimSpace(out) != "" {
			return out, nil
		} else if err != nil {
			slog.Warn("multi-pass writer failed; falling back to single-call synthesis", "task", task.ID, "err", err)
		}
	}

	var lines []string
	anyFlagged := false
	for i, f := range filteredFindings {
		content := f.Content
		if len(content) > 5000 {
			content = strutil.Truncate(content, 5000)
		}
		marker := ""
		switch f.EvidenceStatus {
		case types.EvidenceUnverified, types.EvidenceUnsupported:
			anyFlagged = true
			note := f.EvidenceNote
			if note == "" {
				note = "support could not be mechanically verified"
			}
			marker = fmt.Sprintf("⚠️ UNVERIFIED — %s. Do NOT present this as established fact; if you rely on it, caveat it as unverified in the output.\n", note)
		}
		lines = append(lines, fmt.Sprintf("[%d] (%s, Round %d) %s%s", i+1, f.AgentName, f.Round, marker, content))
	}
	findingsSummary := strings.Join(lines, "\n\n")
	if len(findingsSummary) > 200_000 {
		findingsSummary = strutil.Truncate(findingsSummary, 200_000)
	}

	toneBlock := ""
	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil && p.ToneProfile != nil {
			snippet := adapters.SanitizePromptContent(p.ToneProfile.InjectionSnippet)
			if len(snippet) > 2000 {
				snippet = strutil.Truncate(snippet, 2000)
			}
			toneBlock = "\nLAWYER TONE PROFILE — write the final output in this voice:\n" + snippet + "\n"
		}
	}

	unverifiedDirective := ""
	if anyFlagged {
		unverifiedDirective = "\nSome findings are marked \"⚠️ UNVERIFIED\": their citations could not be mechanically verified against the source documents. You MUST NOT state these as established fact — either omit them or surface them with an explicit caveat (e.g. \"unverified — requires confirmation\") so the reader is warned."
	}

	prompt := fmt.Sprintf(`TASK: %s

ALL FINDINGS FROM ALL ROUNDS:
%s
%s
Produce the final legal output for this task. Structure appropriately for the workflow type: %s.
Ground every statement in the findings above — do not introduce facts, figures, or citations they do not support. Write a clean, client-ready deliverable: do NOT print internal finding numbers or IDs, bracketed references (e.g. [3] or "Finding 12"), agent names, tool names, or unfilled placeholder tokens (e.g. [Current Date], [Email Address]) — fill them in or omit them.%s`,
		safeDesc, findingsSummary, toneBlock, task.WorkflowType, unverifiedDirective)

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskSynthesis,
	})
	// Extended thinking is model-agnostic now: a larger output budget for
	// reasoning, plus an optional reasoning_effort hint for endpoints that
	// support it. Any reasoning-capable model can use it.
	useThinking := routing.ShouldUseThinking(routing.TaskSynthesis, &tier, routing.ComplexityHigh)

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
		Temperature: o.cfg.LLMTemperature,
	}
	if useThinking {
		chatParams.ReasoningEffort = o.cfg.ReasoningEffort
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

// synthesisWriterBudgetTokens is the per-call input budget for synthesis: when the
// findings exceed it, the multi-pass writer is used instead of one monolithic call.
// Sized to sit comfortably inside a small local window (e.g. 8K) alongside the
// system prompt and output budget.
const synthesisWriterBudgetTokens = 5000

// writeDeliverable produces the final output via the scoped multi-pass writer: it
// maps findings into the writer's view, builds a Writer over the synthesis model,
// and lets it cluster → draft (tight agentic sub-agents, search_findings scoped per
// section) → stitch. Used when findings overflow a single synthesis call.
func (o *Orchestrator) writeDeliverable(task *types.Task, findings []types.Finding) (string, error) {
	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return "", err
	}
	bare := routing.ResolveModelID(model)

	wf := make([]writer.Finding, 0, len(findings))
	for _, f := range findings {
		item := writer.Finding{
			ID:       f.ID,
			Content:  f.Content,
			Agent:    f.AgentName,
			Round:    f.Round,
			Grounded: f.EvidenceStatus == types.EvidenceGrounded,
			Note:     f.EvidenceNote,
		}
		if len(f.Citations) > 0 {
			item.Evidence = f.Citations[0].Quote
			item.Source = f.Citations[0].Source
		}
		wf = append(wf, item)
	}

	// Lawyer tone → writer persona (same source as the monolith path).
	persona := ""
	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil && p.ToneProfile != nil {
			snippet := adapters.SanitizePromptContent(p.ToneProfile.InjectionSnippet)
			if len(snippet) > 2000 {
				snippet = strutil.Truncate(snippet, 2000)
			}
			persona = "Write in this lawyer's voice:\n" + snippet
		}
	}

	// Deterministic synthesis: temperature 0 so the writer reliably realizes the
	// full set of sections + figures in a single run, rather than sampling a
	// different (often narrower) subset each time — the high run-to-run variance
	// that capped single runs well below the architecture's true coverage.
	synthTemp := 0.0
	w := writer.New(o.embedC, prov, bare, writer.Options{
		Temperature:       &synthTemp,
		InputBudgetTokens: synthesisWriterBudgetTokens,
		Persona:           persona,
		// Coverage spine: the matter's own enumerated topics become guaranteed
		// sections, so no required allegation category vanishes through clustering.
		RequiredSections: o.extractCoverageSpine(task, prov, bare),
		RecordCost:       func(resp *providers.ChatResponse) { o.recordCost(resp, bare, cost.ContextSynthesis, task.ID) },
		// Synthesis-time figure handling: drafters pull exact figures for their
		// section from the source exhibits on demand (document-backed
		// extract_specifics), rather than every agent pre-stuffing figures into
		// findings (which floods the writer). Backed by the tool registry's RAG.
		Specifics: func(topic string, topK int) []writer.SpecificHit {
			res, err := o.tools.Execute("extract_specifics", map[string]interface{}{"topic": topic, "top_k": topK}, agents.ToolContext{TaskID: task.ID})
			if err != nil {
				return nil
			}
			m, ok := res.(map[string]interface{})
			if !ok {
				return nil
			}
			rows, _ := m["results"].([]map[string]interface{})
			hits := make([]writer.SpecificHit, 0, len(rows))
			for _, r := range rows {
				sn, _ := r["snippet"].(string)
				if strings.TrimSpace(sn) == "" {
					continue
				}
				src, _ := r["title"].(string)
				if src == "" {
					src, _ = r["id"].(string)
				}
				ctx, _ := r["context"].(string)
				hits = append(hits, writer.SpecificHit{Text: sn, Source: src, Context: ctx})
			}
			return hits
		},
	})
	return w.Write(adapters.SanitizePromptContent(task.Description), string(task.WorkflowType), wf)
}

// extractCoverageSpine derives the matter's required sections from the documents'
// own enumerated structure (e.g. a referral's "six categories of potential
// violations"). It retrieves the enumerating passages and asks the model to list
// the distinct categories as section headings — document-grounded, general to legal
// docs, not rubric-derived. Returns nil when nothing enumerable is found (the writer
// then falls back to clustering).
func (o *Orchestrator) extractCoverageSpine(task *types.Task, prov providers.Provider, model string) []string {
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{
		"query": "allegation categories of potential violations enumerated; the referral identifies the following categories; counts of alleged violations; issues presented",
		"top_k": 8,
	}, agents.ToolContext{TaskID: task.ID})
	if err != nil {
		return nil
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		return nil
	}
	rows, _ := m["results"].([]map[string]interface{})
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	for _, r := range rows {
		if sn, _ := r["snippet"].(string); strings.TrimSpace(sn) != "" {
			b.WriteString("- ")
			b.WriteString(strings.Join(strings.Fields(sn), " "))
			b.WriteString("\n")
		}
	}
	passages := strutil.TruncateToTokens(b.String(), 2500)
	prompt := fmt.Sprintf("TASK: %s\n\nFrom the passages below, list the DISTINCT allegation categories / required topics this matter addresses, as short section headings (e.g. \"Cherry-Picking Trade Allocations\", \"Misleading Form ADV Disclosures\"). One heading per line, no numbering, no preamble. Only categories actually present in the passages.\n\nPASSAGES:\n%s",
		strings.Join(strings.Fields(task.Description), " "), passages)
	spineTemp := 0.0 // deterministic spine: same categories every run
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   500,
		System:      "You extract a legal document's enumerated structure as a clean list of section headings, nothing else.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: &spineTemp,
	})
	if err != nil {
		return nil
	}
	o.recordCost(resp, model, cost.ContextSynthesis, task.ID)
	var text string
	for _, bl := range resp.Content {
		if bl.Type == providers.BlockText {
			text = bl.Text
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-*•0123456789.) \t"))
		ln = strings.Trim(ln, "*_#:")
		ln = strings.TrimSpace(ln)
		if n := len(ln); n < 4 || n > 90 {
			continue
		}
		key := strings.ToLower(ln)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ln)
		if len(out) >= 12 { // safety cap
			break
		}
	}
	if len(out) < 2 {
		return nil // not a usable spine; writer falls back to clustering
	}
	slog.Info("coverage spine extracted", "task", task.ID, "sections", len(out))
	return out
}

func (o *Orchestrator) tabulate(task *types.Task) (*types.TaskTable, error) {
	safeDesc := adapters.SanitizePromptContent(task.Description)
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
			c = strutil.Truncate(c, 500)
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
}`, safeDesc, sb.String())

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
		// Gate handlers rewrite PendingGates under the lock.
		allResolved := true
		o.mu.RLock()
		for _, g := range task.PendingGates {
			if g.Status == "pending" {
				allResolved = false
				break
			}
		}
		o.mu.RUnlock()
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
		normalizeTask(t)
		o.tasks[t.ID] = t
		o.gateChans[t.ID] = make(chan struct{}, 8)
	}
	o.mu.Unlock()
}

// normalizeTask repairs nil slices on tasks restored from disk. Earlier
// builds persisted rounds whose Edges/Findings (and findings' Citations)
// could be nil; nil marshals to JSON null, which breaks the UI's contract
// that these fields are always arrays.
func normalizeTask(t *types.Task) {
	if t.DocumentIDs == nil {
		t.DocumentIDs = []string{}
	}
	if t.ActiveAgentIDs == nil {
		t.ActiveAgentIDs = []string{}
	}
	if t.Rounds == nil {
		t.Rounds = []types.RoundState{}
	}
	if t.Findings == nil {
		t.Findings = []types.Finding{}
	}
	if t.PendingGates == nil {
		t.PendingGates = []types.GateRequest{}
	}
	for i := range t.Findings {
		if t.Findings[i].Citations == nil {
			t.Findings[i].Citations = []types.Citation{}
		}
	}
	for i := range t.Rounds {
		r := &t.Rounds[i]
		if r.Goal.ExpectedOutputs == nil {
			r.Goal.ExpectedOutputs = []string{}
		}
		if r.ActiveAgentIDs == nil {
			r.ActiveAgentIDs = []string{}
		}
		if r.Edges == nil {
			r.Edges = []types.CommunicationEdge{}
		}
		if r.Messages == nil {
			r.Messages = []types.AgentMessage{}
		}
		if r.Findings == nil {
			r.Findings = []types.Finding{}
		}
		for j := range r.Findings {
			if r.Findings[j].Citations == nil {
				r.Findings[j].Citations = []types.Citation{}
			}
		}
	}
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
