// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Top-level orchestrator — task lifecycle, phase sequencing, synthesis.

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
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
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/learning"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/ontology"
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
	types.WorkflowRoundtable:    {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseReconciliation, types.PhaseDrafting, types.PhaseReview, types.PhaseDelivery},
	types.WorkflowAdversarial:   {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseReconciliation, types.PhaseReview, types.PhaseVerification, types.PhaseDelivery},
	types.WorkflowReview:        {types.PhaseIntake, types.PhaseAnalysis, types.PhaseReconciliation, types.PhaseReview, types.PhaseVerification, types.PhaseDelivery},
	types.WorkflowTabulate:      {types.PhaseIntake, types.PhaseAnalysis, types.PhaseDelivery},
	types.WorkflowFullBench:     {types.PhaseIntake, types.PhaseResearch, types.PhaseAnalysis, types.PhaseReconciliation, types.PhaseDrafting, types.PhaseReview, types.PhaseVerification, types.PhaseDelivery},
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

	// egraphs holds the per-task Lite evidence graph (grounded entity/relation facts),
	// built at task-start and read at synthesis so the writer states relations with
	// correct attribution. Keyed by task ID; transient (rebuilt each run).
	egraphs       map[string]*evidencegraph.Graph
	egraphAliases map[string]map[string][]string // taskID → canonical allegation → surface-form aliases
	egraphsMu     sync.Mutex

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
		tasks:         map[string]*types.Task{},
		gateChans:     map[string]chan struct{}{},
		cfg:           cfg,
		provReg:       provReg,
		costs:         costs,
		embedC:        embedC,
		registry:      registry,
		memStore:      memStore,
		knowledge:     knowledgeStore,
		templates:     templatesStore,
		settings:      settingsStore,
		profiles:      profileStore,
		clients:       clientStore,
		time:          timeStore,
		learning:      learningEngine,
		tools:         toolReg,
		egraphs:       map[string]*evidencegraph.Graph{},
		egraphAliases: map[string]map[string][]string{},
		rootAgentDef:  rootDef,
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
	// Reconciliation is opt-in (RECONCILIATION_ENABLED=true). Its detection still has
	// poor precision (false-positive controversies) and the extra round bloats findings,
	// so by default the pipeline skips it; the code + graph types remain for when it
	// earns its place (and for the TypeDB contradiction graph).
	if os.Getenv("RECONCILIATION_ENABLED") != "true" {
		filtered := make([]types.TaskPhase, 0, len(phases))
		for _, p := range phases {
			if p != types.PhaseReconciliation {
				filtered = append(filtered, p)
			}
		}
		phases = filtered
	}
	var runErr error

	// At-start intent steering: hunt the matter's specific facts (amounts, rates,
	// account numbers, counts, %) into the finding pool up front via entity-aware
	// queries, so the rounds' conceptual queries don't leave them undiscovered and
	// synthesis is aware of them. Bounded + deduped (no flood). Best-effort — returns
	// nil when the matter has no retrievable exhibits.
	stier := types.TierTool
	sweepModel := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &stier, TaskType: routing.TaskExtraction})
	if prov, perr := o.provReg.Get(sweepModel); perr == nil {
		bare := routing.ResolveModelID(sweepModel)
		// Evidence graph FIRST: one grounded extraction (entities + relations + the matter's
		// distinct allegations) that everything downstream reads — so recruitment and the
		// coverage spine derive the SAME allegation set from the graph, instead of separate
		// LLM enumerations that vary run-to-run (the 12↔16-section swing that dominated
		// scores). buildEvidenceGraph populates task.Allegations via ensureAllegations.
		o.buildEvidenceGraph(task, prov, bare)
		// Classify the matter (practice area / sector / work type) from its DOCUMENTS,
		// so recruitment seats the right specialists. The task description is too thin
		// (the practice area lives in the exhibits, not "review and summarize"), so we
		// classify from sampled passages and populate NosLegal — which recruitment then
		// uses as a signal alongside the (kept-specific) round goal.
		if tags := o.classifyMatter(task, prov, bare); tags.AreaOfLaw != nil || tags.Sector != nil {
			o.update(task, func(t *types.Task) { t.NosLegal = &tags })
			slog.Info("matter classified", "task", task.ID, "area", strDeref(tags.AreaOfLaw), "sector", strDeref(tags.Sector))
			// On-demand specialist synthesis: generate fine-grained sub-specialty agents
			// for this area (cached in the agentdb), so the matter is staffed by tailored
			// specialists rather than only the generic registry.
			o.ensureSpecialists(strDeref(tags.AreaOfLaw), strDeref(tags.Sector), strDeref(tags.WorkType), prov, bare, task)
		}
		if sweep := o.specificsSweep(task, prov, bare); len(sweep) > 0 {
			o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, sweep...) })
			slog.Info("specifics sweep seeded findings", "task", task.ID, "n", len(sweep))
		}
	}

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
		if roundState.Starved {
			// Ride the degradation into the final task record — consumers
			// (UI, benchmark drivers) must see the run was starved.
			t.StarvedRounds = append(t.StarvedRounds, types.StarvedRound{Round: roundState.Goal.Round, Phase: phase})
		}
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

// detectControversies is the reconciliation analyst's detection step: it reads the
// matter's gathered facts (findings, each carrying its source) and surfaces cross-
// document CONTROVERSIES — subjects where sources assert conflicting values. The
// output is graph-shaped (types.Controversy / types.Claim), the seed for the future
// TypeDB contradiction graph. Bounded; best-effort.
func (o *Orchestrator) detectControversies(task *types.Task, prov providers.Provider, model string) []types.Controversy {
	var lb strings.Builder
	n := 0
	for _, f := range task.Findings {
		line := strings.Join(strings.Fields(f.Content), " ")
		if line == "" {
			continue
		}
		src := ""
		if len(f.Citations) > 0 {
			src = f.Citations[0].Source
		}
		fmt.Fprintf(&lb, "- [%s] %s\n", src, strutil.Truncate(line, 220))
		if n++; n >= 140 {
			break
		}
	}
	if n == 0 {
		return nil
	}
	prompt := fmt.Sprintf("Below are facts extracted from a legal matter's documents, each tagged with its [source]. Identify CONTROVERSIES — subjects where two or more sources assert DIFFERENT or INCONSISTENT values (a numeric discrepancy, a date conflict, a count mismatch, a contradictory statement). Report ONLY genuine conflicts, not restatements of the same value. Respond with ONLY a JSON array (max 6 items):\n[{\"subject\":\"<the disputed subject>\",\"kind\":\"monetary|temporal|count|categorical\",\"claims\":[{\"value\":\"<asserted value>\",\"source\":\"<source>\"},{\"value\":\"<conflicting value>\",\"source\":\"<source>\"}],\"significance\":\"<why the discrepancy matters>\"}]\n\nFACTS:\n%s",
		strutil.TruncateToTokens(lb.String(), 3000))
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 1200,
		System:   "You are a meticulous reconciliation analyst. You surface only genuine cross-source conflicts. Output only the JSON array.",
		Messages: []providers.Message{{Role: "user", Content: prompt}}, CacheSystem: true, Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		return nil
	}
	o.recordCost(resp, model, cost.ContextTask, task.ID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	s, e := strings.Index(text, "["), strings.LastIndex(text, "]")
	if s < 0 || e <= s {
		return nil
	}
	var raw []types.Controversy
	if json.Unmarshal([]byte(text[s:e+1]), &raw) != nil {
		return nil
	}
	var out []types.Controversy
	for _, c := range raw {
		if strings.TrimSpace(c.Subject) == "" || len(c.Claims) < 2 {
			continue // a controversy needs a subject and ≥2 conflicting claims
		}
		for i := range c.Claims {
			c.Claims[i].Subject = c.Subject
			if c.Claims[i].Kind == "" {
				c.Claims[i].Kind = c.Kind
			}
		}
		out = append(out, c)
	}
	return out
}

// reconciliationGoal detects the matter's controversies, stores them (graph seed), and
// turns each into an objective for this round — so DyTopo recruits a specialist per
// controversy to write a grounded, debated finding on it.
func (o *Orchestrator) reconciliationGoal(task *types.Task) (types.RoundGoal, error) {
	base := types.RoundGoal{ID: uuid.New().String(), Round: task.CurrentRound, Phase: types.PhaseReconciliation}
	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, err := o.provReg.Get(model)
	if err != nil {
		base.Description = "Reconcile the matter: confirm that key figures, dates, and claims are consistent across all source documents; flag any discrepancy."
		return base, nil
	}
	cons := o.detectControversies(task, prov, routing.ResolveModelID(model))
	o.update(task, func(t *types.Task) { t.Controversies = cons })
	if len(cons) == 0 {
		base.Description = "No cross-document controversies were detected; confirm the consistency of key figures, dates, and claims across sources and note any that warrant a closer look."
		return base, nil
	}
	slog.Info("reconciliation: controversies detected", "task", task.ID, "n", len(cons))
	var b strings.Builder
	b.WriteString("Resolve these cross-document CONTROVERSIES. For EACH: determine which source governs and why, assess the significance, and state the strategic/defence implication — writing a grounded finding that cites BOTH conflicting sources verbatim.\n")
	for i, c := range cons {
		var vs []string
		for _, cl := range c.Claims {
			vs = append(vs, fmt.Sprintf("%q (%s)", cl.Value, cl.Source))
		}
		fmt.Fprintf(&b, "%d. %s: %s", i+1, c.Subject, strings.Join(vs, " vs "))
		if c.Significance != "" {
			b.WriteString(" — " + c.Significance)
		}
		b.WriteString("\n")
	}
	base.Description = b.String()
	base.ExpectedOutputs = []string{"A grounded finding per controversy, citing both sources", "Which value governs and why", "The strategic or defence implication"}
	return base, nil
}

func (o *Orchestrator) generateRoundGoal(task *types.Task, phase types.TaskPhase) (types.RoundGoal, error) {
	// The reconciliation phase has a bespoke goal: the cross-document controversies the
	// reconciliation analyst surfaces become this round's objectives, so DyTopo recruits
	// a specialist per controversy to write a grounded finding on each (full debate/
	// verify, like any round). Controversy-driven recruitment.
	if phase == types.PhaseReconciliation {
		return o.reconciliationGoal(task)
	}

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
			return o.appendDiscrepancies(task, out), nil
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

	prov, bare, err := o.synthesisModel(model)
	if err != nil {
		return "", err
	}
	chatParams := providers.ChatParams{
		Model:       bare,
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
	o.recordCost(resp, bare, cost.ContextSynthesis, task.ID)

	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return o.appendDiscrepancies(task, b.Text), nil
		}
	}
	return "", nil
}

// appendDiscrepancies guarantees the detected cross-source contradictions land in the
// deliverable. Detection is model-agnostic, but a weak drafter drops most of them when left
// to weave them into prose (7B surfaced 2 of 14; Haiku 24). So we render them mechanically as
// a dedicated section rather than trusting the writer — surfacing the conflicts is the whole
// point (defense issues), and they must not depend on synthesis quality.
func (o *Orchestrator) appendDiscrepancies(task *types.Task, body string) string {
	// BELO analytic layer: the defense issues derived from the charges (scienter element,
	// criminal exposure, statute of limitations) — the analytic reasoning the rubric asks for.
	derived := o.deriveDefenseIssues(task)

	// Figure discrepancies: cross-source value conflicts surfaced by the contradiction detector.
	var discrepancies []string
	seen := map[string]bool{}
	for _, f := range task.Findings {
		if f.AgentID != "contradiction-detector" && f.AgentID != crossDocAgentID {
			continue
		}
		c := strings.TrimSpace(f.Content)
		c = strings.TrimPrefix(c, "DISCREPANCY (defense issue) — ")
		if i := strings.Index(c, ". These figures conflict"); i > 0 {
			c = strings.TrimSpace(c[:i])
		}
		if c == "" || seen[strings.ToLower(c)] {
			continue
		}
		seen[strings.ToLower(c)] = true
		discrepancies = append(discrepancies, "- "+c+".")
	}

	// Deviations: draft-vs-instruction conflicts from the deviation detector (compliance/compare
	// matters). These are the finding such tasks are scored on.
	var deviations []string
	seenDev := map[string]bool{}
	for _, f := range task.Findings {
		if f.AgentID != "deviation-detector" {
			continue
		}
		c := strings.TrimSpace(f.Content)
		if c == "" || seenDev[strings.ToLower(c)] {
			continue
		}
		seenDev[strings.ToLower(c)] = true
		deviations = append(deviations, "- "+c)
	}

	if len(derived) == 0 && len(discrepancies) == 0 && len(deviations) == 0 {
		return body
	}
	if len(deviations) > 0 {
		body = strings.TrimRight(body, "\n") +
			"\n\n## Deviations Identified\n\nWhere the draft documents deviate from the client's instructions — each should be corrected:\n\n" +
			strings.Join(deviations, "\n") + "\n"
	}
	if len(derived) == 0 && len(discrepancies) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n## Discrepancies and Defense Issues\n\n")
	if len(derived) > 0 {
		b.WriteString("Defense issues raised by the charges and the record — elements that must be proven, exposure beyond the civil counts, and timing defenses:\n\n")
		for _, d := range derived {
			b.WriteString("- ")
			b.WriteString(d)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(discrepancies) > 0 {
		b.WriteString("The following figures conflict across the record. Each is a potential defense point — the inconsistency should be raised and its significance assessed, not silently reconciled:\n\n")
		b.WriteString(strings.Join(discrepancies, "\n"))
		b.WriteString("\n")
	}
	return b.String()
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
// localize prepends the local-inference prefix to a bare model id when the registry serves
// local models, so an admin can pick "qwen2.5:14b" in the panel and it routes to the LOCAL
// provider (not the cloud stack, which Get() would otherwise select). A value already prefixed
// (local:/ollama:) is left as-is, so env knobs may pass either form.
func (o *Orchestrator) localize(m string) string {
	m = strings.TrimSpace(m)
	if m == "" || routing.IsOllamaModel(m) || routing.IsLocalModel(m) {
		return m
	}
	if o.cfg.Local.LocalInferenceURL != "" {
		return "local:" + m
	}
	if o.cfg.Local.OllamaEnabled {
		return "ollama:" + m
	}
	return m
}

// synthesisModel resolves the provider + bare model for synthesis/drafting, honouring the
// SYNTHESIS_MODEL knob (route ONLY the judged-memo step to a stronger local model, e.g. 14B,
// while the high-volume bulk stays on the fast 7B) and falling back to the routed default.
func (o *Orchestrator) synthesisModel(routed string) (providers.Provider, string, error) {
	use := routed
	if sm := o.localize(o.cfg.Models.SynthesisModel); sm != "" {
		if _, err := o.provReg.Get(sm); err == nil {
			use = sm
		} else {
			slog.Warn("SYNTHESIS_MODEL provider unavailable; using routed default", "synthesis_model", sm, "err", err)
		}
	}
	prov, err := o.provReg.Get(use)
	return prov, routing.ResolveModelID(use), err
}

func (o *Orchestrator) writeDeliverable(task *types.Task, findings []types.Finding) (string, error) {
	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, bare, err := o.synthesisModel(model)
	if err != nil {
		return "", err
	}

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

	// Temperature 0 was tried and backfired: greedy decoding favours generic
	// high-probability legal prose and STRIPS specific figures (lower-probability
	// tokens). Keep the configured sampling temperature for figure-rich narrative;
	// figure landing is guaranteed mechanically in the writer instead (Key figures).
	w := writer.New(o.embedC, prov, bare, writer.Options{
		Temperature:       o.cfg.LLMTemperature,
		InputBudgetTokens: synthesisWriterBudgetTokens,
		Persona:           persona,
		// Coverage spine: the matter's own enumerated topics become guaranteed
		// sections, so no required allegation category vanishes through clustering.
		RequiredSections: o.extractCoverageSpine(task, prov, bare),
		// Alias map per spine section (the merged allegation's alternate surface forms), so
		// fact routing matches a fact phrased like ANY variant, not just the canonical heading.
		SectionAliases: o.allegationAliases(task.ID),
		// Paged synthesis: sections composed with compact-when-done / uncompact-on-demand,
		// assembled losslessly. With DyTopoDrafting on, each section is written by a bounded
		// writing huddle (lead + contributors, draft→critique→revise) run concurrently, then
		// composed by this paged pass.
		WriterSystem:   o.writingAgentSystem(task),
		Paged:          true,
		DyTopoDrafting: o.cfg.Drafting.DyTopo,
		DraftingAgents: o.draftingAgentVoices(task),
		DraftingRounds: o.cfg.Drafting.Rounds,
		// Evidence-graph facts: routed per-section (by entity/allegation overlap) so each
		// author states its relations with correct attribution — no whole-ledger crowding.
		// Gate BIGLAW_FACTS_GLOBAL=1 reverts to whole-ledger injection for A/B.
		Facts:       o.groundedFacts(task.ID),
		FactsGlobal: os.Getenv("BIGLAW_FACTS_GLOBAL") == "1" || os.Getenv("BIGLAW_FACTS_GLOBAL") == "true",
		// Named individual respondents (committedBy → Person claims): the writer enforces
		// one exposure entry per respondent — consolidated record or explicit gap note.
		Respondents: o.respondentRoster(task.ID),
		RecordCost:  func(resp *providers.ChatResponse) { o.recordCost(resp, bare, cost.ContextSynthesis, task.ID) },
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

// specificsSweep runs at task START (intent steering): it retrieves the matter's
// figure-dense passages, has the model enumerate TARGETED fact-finding queries —
// entity-aware, since the passages name the people/accounts/funds/metrics — runs
// each against the exhibits, and emits the exact figures as grounded findings. This
// gets the specifics that the rounds' conceptual queries would miss (rates, account
// numbers, counts, percentages) into the finding pool from round 1, so the whole
// pipeline (and synthesis) is aware of them. Bounded + deduped, so no finding flood.
func (o *Orchestrator) specificsSweep(task *types.Task, prov providers.Provider, model string) []types.Finding {
	const maxFindings = 40 // figures + citations
	// Seed from the multi-query allegation merge (not one top-k query): a single query
	// under-retrieved an entire allegation, so its figures were never hunted. The merged
	// passages span every allegation — primary and secondary — and the figures live in
	// those same allegation passages.
	passages := o.allegationPassages(task, 2500)
	if strings.TrimSpace(passages) == "" {
		return nil
	}

	// Two parallel hunts: FIGURES and legal CITATIONS — distinct classes of "specific"
	// (numbers vs references) that each need their own queries, generated concurrently
	// and merged. The instructions name fact TYPES only; the actual entities and
	// citations must come from the passages at runtime, never from this prompt (so the
	// agent generalises to any matter rather than being told a particular answer).
	figInstr := "list up to 12 SPECIFIC search queries to find this matter's exact FIGURES — dollar amounts, percentages and rates, counts, dates, and account numbers. Tie each query to the specific named party, account, entity, or metric it concerns, using the actual names and terms you see in the passages. Prioritise the figures that quantify each allegation, claim, or loss."
	citeInstr := "list up to 12 SPECIFIC search queries to find this matter's exact LEGAL CITATIONS — statutory provisions and subsections, rule numbers, regulatory-form item numbers, internal policy or manual section numbers, contract clause numbers, and code sections. Tie each query to the conduct, allegation, or obligation it concerns, using the actual provisions and references you see in the passages."
	figCh := make(chan []string, 1)
	citeCh := make(chan []string, 1)
	go func() { figCh <- o.sweepQueries(prov, model, task.ID, passages, figInstr) }()
	go func() { citeCh <- o.sweepQueries(prov, model, task.ID, passages, citeInstr) }()
	merged := append(<-figCh, <-citeCh...)
	var queries []string
	qseen := map[string]bool{}
	for _, q := range merged {
		if k := strings.ToLower(q); !qseen[k] {
			qseen[k] = true
			queries = append(queries, q)
		}
	}

	var findings []types.Finding
	seen := map[string]bool{}
	for _, q := range queries {
		sr, err := o.tools.Execute("extract_specifics", map[string]interface{}{"topic": q, "top_k": 4}, agents.ToolContext{TaskID: task.ID})
		if err != nil {
			continue
		}
		sm, _ := sr.(map[string]interface{})
		srows, _ := sm["results"].([]map[string]interface{})
		for _, r := range srows {
			quote := strings.TrimSpace(strings.Join(strings.Fields(r["snippet"].(string)), " "))
			if quote == "" {
				continue
			}
			key := strings.ToLower(quote)
			if seen[key] {
				continue
			}
			seen[key] = true
			src, _ := r["title"].(string)
			if src == "" {
				src, _ = r["id"].(string)
			}
			findings = append(findings, types.Finding{
				ID:             uuid.New().String(),
				AgentID:        "specifics-sweep",
				AgentName:      "Specifics Sweep",
				Content:        quote,
				Citations:      []types.Citation{{Source: src, Quote: quote, MechanicallyVerified: true}},
				Confidence:     0.8,
				EvidenceStatus: types.EvidenceGrounded,
				Round:          0,
				Timestamp:      time.Now(),
			})
			if len(findings) >= maxFindings {
				return findings
			}
		}
	}
	return findings
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// classifyMatter classifies the matter into NOSLEGAL facets (practice area, sector,
// work type) from its DOCUMENTS — sampled passages, not the thin task description —
// so recruitment can seat the right practice specialists. Best-effort; returns empty
// tags on any failure.
func (o *Orchestrator) classifyMatter(task *types.Task, prov providers.Provider, model string) types.NosLegalTags {
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{
		"query": "subject matter, parties, the legal claims and allegations, the practice area and legal doctrines at issue",
		"top_k": 6,
	}, agents.ToolContext{TaskID: task.ID})
	passages := ""
	if err == nil {
		if m, ok := res.(map[string]interface{}); ok {
			if rows, ok := m["results"].([]map[string]interface{}); ok {
				var b strings.Builder
				for _, r := range rows {
					if sn, _ := r["snippet"].(string); strings.TrimSpace(sn) != "" {
						b.WriteString(strings.Join(strings.Fields(sn), " "))
						b.WriteString("\n")
					}
				}
				passages = strutil.TruncateToTokens(b.String(), 1500)
			}
		}
	}
	prompt := fmt.Sprintf("Classify this legal matter for routing to specialist agents. Respond with ONLY valid JSON: {\"areaOfLaw\":\"<the specific practice area, e.g. Securities Regulation, Employment, M&A, Real Estate>\",\"workType\":\"<Advisory|Transactional|Litigious|Regulatory|Other>\",\"sector\":\"<the industry sector>\"}. Base it on the CONTENT, not the instruction.\n\nTASK: %s\n\nCONTENT:\n%s",
		strings.Join(strings.Fields(task.Description), " "), passages)
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 200,
		System:   "You are a legal taxonomy classifier. Output only the requested JSON.",
		Messages: []providers.Message{{Role: "user", Content: prompt}}, CacheSystem: true,
	})
	if err != nil {
		return types.NosLegalTags{}
	}
	o.recordCost(resp, model, cost.ContextClassification, task.ID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	s, e := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if s < 0 || e <= s {
		return types.NosLegalTags{}
	}
	var raw struct{ AreaOfLaw, WorkType, Sector string }
	if json.Unmarshal([]byte(text[s:e+1]), &raw) != nil {
		return types.NosLegalTags{}
	}
	tags := types.NosLegalTags{}
	if raw.AreaOfLaw != "" {
		tags.AreaOfLaw = &raw.AreaOfLaw
	}
	if raw.WorkType != "" {
		tags.WorkType = &raw.WorkType
	}
	if raw.Sector != "" {
		tags.Sector = &raw.Sector
	}
	return tags
}

// ensureSpecialists synthesises fine-grained specialist agents for the matter's
// classified practice area ON DEMAND, caching them in the agent registry (agentdb)
// for reuse. A matter is then handled by specialists tailored to its sub-specialties
// rather than whatever generic agents the registry happened to contain. First time an
// area is seen → generate + persist; thereafter → reuse. Best-effort.
func (o *Orchestrator) ensureSpecialists(area, sector, workType string, prov providers.Provider, model string, task *types.Task) {
	area = strings.TrimSpace(area)
	if area == "" {
		return
	}
	key := slugify(area)
	for _, a := range o.registry.ListAll() { // cache: already generated for this area?
		if a.Metadata != nil {
			if g, _ := a.Metadata["genArea"].(string); g == key {
				return
			}
		}
	}
	defs := o.synthesizeAgents(area, sector, workType, key, prov, model, task)
	if len(defs) == 0 {
		return
	}
	if err := o.registry.RegisterAll(defs); err == nil {
		_ = o.registry.Persist()
		slog.Info("synthesised specialist agents on demand", "area", area, "n", len(defs))
	}
}

// synthesizeAgents asks the model to design fine-grained sub-specialty analyst agents
// for a practice area (taxonomy-driven, on-demand), returning ready AgentDefinitions.
func (o *Orchestrator) synthesizeAgents(area, sector, workType, key string, prov providers.Provider, model string, task *types.Task) []types.AgentDefinition {
	ctx := ""
	if sector != "" {
		ctx += " in the " + sector + " sector"
	}
	if workType != "" {
		ctx += ", " + workType + " work"
	}
	// Ground generation in THIS matter's actual allegations, not the area's generic
	// sub-areas. Keying off the area name alone produced off-topic specialists (an
	// Insider-Trading analyst on a cherry-picking matter) that diluted the pool; the
	// EXHAUSTIVE multi-query enumeration (vs one top-k query) makes every distinct
	// allegation — primary or secondary — visible, so each gets its own specialist
	// rather than an arbitrary 5-6 collapsing onto the dominant theme.
	allegations := o.ensureAllegations(task, prov, model)
	issues := ""
	if len(allegations) > 0 {
		issues = "- " + strings.Join(allegations, "\n- ")
	}
	var prompt string
	if strings.TrimSpace(issues) != "" {
		// One specialist per distinct allegation (merging only near-duplicates), so a
		// secondary allegation is never left unstaffed. Clamp to a sane pool size.
		n := len(allegations)
		if n < 5 {
			n = 5
		}
		if n > 8 {
			n = 8
		}
		prompt = fmt.Sprintf("A legal matter in %s%s raises the SPECIFIC allegations below. Design %d specialist legal analyst agents: design ONE per distinct allegation listed (merge only near-duplicates), EACH tailored to a SPECIFIC issue, allegation, or course of conduct IN THIS MATTER — NOT generic sub-areas of the practice area, and DO NOT collapse several allegations into one analyst. Name each for the conduct it analyses (e.g. a 'Trade-Allocation Analyst' for an allocation issue, a 'Directed-Brokerage Analyst' for a brokerage-kickback issue). Respond with ONLY a JSON array; each element: {\"name\":\"<issue-specific> Analyst\",\"description\":\"<the specific issue in THIS matter it analyses>\",\"framework\":\"<a numbered analytical framework of 4-6 steps>\",\"skills\":[\"<kebab-skill>\"]}\n\nMATTER ALLEGATIONS:\n%s",
			area, ctx, n, issues)
	} else {
		prompt = fmt.Sprintf("Design 5 to 6 FINE-GRAINED specialist legal analyst agents for the practice area \"%s\"%s. Each must be a DISTINCT sub-specialty of that area. Respond with ONLY a JSON array; each element: {\"name\":\"<sub-specialty> Analyst\",\"description\":\"<one sentence>\",\"framework\":\"<a numbered analytical framework of 4-6 steps>\",\"skills\":[\"<kebab-skill>\"]}",
			area, ctx)
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 2800,
		System:   "You design rigorous, specialised legal AI analyst agents. Output ONLY a JSON array, no prose before or after.",
		Messages: []providers.Message{{Role: "user", Content: prompt}}, CacheSystem: true, Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		slog.Warn("synthesizeAgents: chat error", "area", area, "err", err)
		return nil
	}
	o.recordCost(resp, model, cost.ContextTask, task.ID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	arr := parseAgentSpecs(text)
	if len(arr) == 0 {
		slog.Warn("synthesizeAgents: 0 specs parsed", "area", area, "respLen", len(text), "head", strutil.Truncate(strings.Join(strings.Fields(text), " "), 240))
	}
	dom := domainForWorkType(workType)
	var defs []types.AgentDefinition
	for i, a := range arr {
		name := strings.TrimSpace(a.Name)
		framework := strings.TrimSpace(string(a.Framework))
		if name == "" || framework == "" {
			continue
		}
		defs = append(defs, types.AgentDefinition{
			ID:           fmt.Sprintf("gen-%s-%d", key, i),
			Name:         name,
			Tier:         2,
			Type:         types.AgentTypeSpecialist,
			Domain:       dom,
			Description:  strings.TrimSpace(a.Description) + " Specialist in " + area + ".",
			SystemPrompt: "You are the " + name + ", a specialist in " + area + ".\n" + framework + "\nGround every finding in the matter's documents: quote verbatim evidence and cite its source.",
			AllowedTools: []string{"search_chunks", "extract_specifics", "search_knowledge", "read_document", "find_in_document", "list_documents"},
			Skills:       a.Skills,
			Metadata:     map[string]interface{}{"genArea": key, "practiceArea": area},
		})
	}
	return defs
}

type agentSpec struct {
	Name        string
	Description string
	Framework   flexText
	Skills      []string
}

// flexText accepts a JSON string OR an array of strings (a model designing a "numbered
// framework" naturally emits the steps as an array) — joining an array into a numbered
// block. Without this the whole agent object failed to unmarshal and was dropped, which
// is exactly why on-demand synthesis silently produced 0 agents.
type flexText string

func (f *flexText) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		return nil
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexText(s)
	case '[':
		var arr []string
		if json.Unmarshal(b, &arr) == nil {
			var sb strings.Builder
			for i, s := range arr {
				fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(s))
			}
			*f = flexText(strings.TrimSpace(sb.String()))
		} else {
			*f = flexText(strings.Trim(t, "[]"))
		}
	default:
		*f = flexText(t)
	}
	return nil
}

// parseAgentSpecs extracts agent specs from possibly-truncated model JSON: it tries the
// whole array first, then falls back to scanning complete top-level {...} objects — so a
// truncated final element (the 7B running out of tokens mid-array) still yields every
// complete earlier agent instead of dropping the whole batch.
func parseAgentSpecs(text string) []agentSpec {
	s := strings.Index(text, "[")
	if s < 0 {
		return nil
	}
	if e := strings.LastIndex(text, "]"); e > s {
		var arr []agentSpec
		if json.Unmarshal([]byte(text[s:e+1]), &arr) == nil && len(arr) > 0 {
			return arr
		}
	}
	var out []agentSpec
	depth, start := 0, -1
	for i := s; i < len(text); i++ {
		switch text[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth--; depth == 0 && start >= 0 {
				var sp agentSpec
				if json.Unmarshal([]byte(text[start:i+1]), &sp) == nil && strings.TrimSpace(sp.Name) != "" {
					out = append(out, sp)
				}
				start = -1
			}
		}
	}
	return out
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func domainForWorkType(wt string) types.AgentDomain {
	switch strings.ToLower(strings.TrimSpace(wt)) {
	case "litigious":
		return types.DomainInvestigation
	case "regulatory":
		return types.DomainCompliance
	case "transactional":
		return types.DomainDrafting
	default:
		return types.DomainResearch
	}
}

// sweepQueries runs one query-generation call over the matter's passages with the
// given instruction (figures or citations) and returns the parsed query lines. Used
// by specificsSweep to run the figure and citation hunts concurrently.
func (o *Orchestrator) sweepQueries(prov providers.Provider, model, taskID, passages, instruction string) []string {
	prompt := fmt.Sprintf("From the passages below, %s One query per line, no numbering.\n\nPASSAGES:\n%s", instruction, passages)
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 500,
		System:      "You generate precise, entity-named search queries to locate a legal matter's specific facts and citations. Output only the queries.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true, Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		return nil
	}
	o.recordCost(resp, model, cost.ContextTask, taskID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	var out []string
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-*•0123456789.) \t"))
		if len(ln) >= 4 {
			out = append(out, ln)
		}
	}
	return out
}

// allegationPassages gathers the matter's allegation-bearing passages using MULTIPLE
// complementary retrieval queries, merged and deduped. A single top-k query was the
// root cause of an entire allegation (a directed-brokerage scheme) being dropped from a
// securities matter: its passages never ranked in one query's top-k, so no specialist
// was recruited for it, no figures were swept, and no section was written. Several
// generic angles on "what is alleged" — phrasings only, never matter-specific terms —
// surface primary AND secondary allegations across every party. Returns the merged
// passages truncated to tokenBudget (empty when nothing is found).
func (o *Orchestrator) allegationPassages(task *types.Task, tokenBudget int) string {
	queries := []string{
		"allegation categories of potential violations enumerated; the referral identifies the following categories; counts of alleged violations; issues presented",
		"each distinct scheme, course of conduct, claim, or violation alleged against every named party, individual, entity, fund, or account",
		"secondary and additional allegations, separate counts, further charges, other misconduct beyond the principal claim",
		"every named individual, entity, account, fund, or third party and the specific wrongdoing, exposure, or liability attributed to it",
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, q := range queries {
		res, err := o.tools.Execute("search_chunks", map[string]interface{}{"query": q, "top_k": 8}, agents.ToolContext{TaskID: task.ID})
		if err != nil {
			continue
		}
		m, ok := res.(map[string]interface{})
		if !ok {
			continue
		}
		rows, _ := m["results"].([]map[string]interface{})
		for _, r := range rows {
			sn, _ := r["snippet"].(string)
			sn = strings.Join(strings.Fields(sn), " ")
			if sn == "" {
				continue
			}
			key := chunkKey(sn)
			if seen[key] {
				continue
			}
			seen[key] = true
			b.WriteString("- ")
			b.WriteString(sn)
			b.WriteString("\n")
		}
	}
	return strutil.TruncateToTokens(b.String(), tokenBudget)
}

// buildEvidenceGraph extracts grounded entity/relation facts from the matter's relational
// passages into a per-task Lite evidence graph, so synthesis can state relations with
// correct attribution (a "victim-of → directed-brokerage" edge can't render under cherry-
// picking) and render each party's full exposure. Two-pass, entity-anchored extraction
// (the probe showed single-pass drops parenthetical/omission facts like an ownership %);
// every fact is grounded (quote must be verbatim in its chunk) or dropped. Bounded to the
// retrieved allegation passages for now; true ingestion/per-chunk extraction is the
// follow-on once the lift is confirmed.
// reAllegationTerm scores how CONTROLLING-document-like a text is — the doc that STATES what
// must be assessed. Enforcement: accusation/charge language. Compliance/compare: instruction/
// requirement language. The controlling doc (referral, or client instruction memo) is where the
// issues are enumerated, so both vocabularies count.
var reAllegationTerm = regexp.MustCompile(`(?i)\balleg|\bviolat|\bthe division\b|\bsection\s+\d|\brule\s+\d|\bcount\s|\bfraud|\bbreach|\bscheme|\bfailed to|\brequire|\binstruct|\bshall\b|\bmust\b|\bshould\b|\bwants?\b|\bdirect`)

// chargingDocChunks pages through the matter's CHARGING document(s) — those densest in
// allegation language — up to a token budget, chunked. The conducts live in the charging doc;
// exhibits/policy docs are left to the cheaper figure sweep. This keeps the expensive spine
// pass bounded (paging the charging doc, not dumping every document). Returns nil if no doc
// yields usable text, so the caller can fall back.
func (o *Orchestrator) chargingDocChunks(task *types.Task, tokenBudget int) []string {
	type scored struct {
		text  string
		score int
	}
	var docs []scored
	for _, docID := range task.DocumentIDs {
		txt, err := o.knowledge.GetFullText(docID)
		if err != nil || strings.TrimSpace(txt) == "" {
			continue
		}
		docs = append(docs, scored{txt, len(reAllegationTerm.FindAllStringIndex(txt, -1))})
	}
	if len(docs) == 0 {
		return nil
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].score > docs[j].score })
	var out []string
	used := 0
	for _, d := range docs {
		if d.score == 0 || used >= tokenBudget {
			break // only allegation-bearing docs, only up to the budget
		}
		swept := d.text
		if maxChars := (tokenBudget - used) * 4; len(swept) > maxChars { // ~4 chars/token
			swept = swept[:maxChars]
		}
		out = append(out, chunkByTokens(swept, 1500)...)
		used += len(swept) / 4
	}
	return out
}

func (o *Orchestrator) buildEvidenceGraph(task *types.Task, prov providers.Provider, model string) {
	passages := o.allegationPassages(task, 6000)
	if strings.TrimSpace(passages) == "" {
		return
	}
	g := evidencegraph.New()
	kept, rej := 0, 0
	chunks := chunkByTokens(passages, 1500)
	// Phase 1 — entity/relation/allegation extraction on the bulk model (7B) over all chunks.
	for _, chunk := range chunks {
		k, r := evidencegraph.ExtractInto(g, prov, model, o.cfg.LLMTemperature, chunk, "")
		kept += k
		rej += r
	}
	// Phase 2 — the typed conduct/spine pass, optionally on a STRONGER model (BELO_SPINE_MODEL,
	// e.g. qwen2.5:14b). Conducts are document-level abstractions the 7B mislabels; a capable
	// model populates the Conduct nodes cleanly. Run as a separate phase so the GPU swaps models
	// once (not per chunk). Falls back to the bulk model/provider when SpineModel is unset.
	spineModel, spineProv := model, prov
	if sm := o.localize(o.cfg.Models.SpineModel); sm != "" {
		// sm is a routing model ID (e.g. "local:qwen2.5:14b"); Get() routes by its prefix, but
		// the PROVIDER call needs the bare model name (the prefix is stripped) — otherwise the
		// endpoint gets "local:qwen2.5:14b" and fails. Resolve to bare for the call; only switch
		// providers if Get succeeds (else keep the bulk provider).
		if p, err := o.provReg.Get(sm); err == nil {
			spineModel, spineProv = routing.ResolveModelID(sm), p
		} else {
			slog.Warn("BELO spine model provider unavailable; using bulk model", "spine_model", sm, "err", err)
		}
	}
	// FIX 1 — the conduct/spine pass sweeps the FULL text of every charging document, NOT the
	// allegationPassages semantic subset Phase 1 uses. That subset (4 queries × top_k 8, truncated
	// to 6000 tokens) structurally misses any category whose chunk doesn't rank (e.g. Books-&-
	// Records), so the spine silently dropped it. Reading every doc's full text guarantees every
	// category is seen; AddTriple dedups inside the graph, so overlapping sweeps are safe. Mirrors
	// the harvestAndBindFigures full-doc idiom (GetFullText, title via GetByID, 40k-token cap,
	// chunkByTokens). Falls back to the Phase-1 `chunks` if no document yields usable full text.
	// FIX 2 — run the conduct pass at temperature 0 (deterministic copy-out): the prior 0.2 caused
	// run-to-run wobble on what is fundamentally a transcribe-and-classify task.
	// Allegations live in the CHARGING document, not the exhibits. Sweeping all docs' full text
	// on the (stronger, slower) spine model is both wasteful and slow enough to stall the run, so
	// PAGE through the charging doc(s) — ranked by allegation-language density — up to a token
	// budget, the same bounded-paging discipline used elsewhere. The 7B figure sweep already
	// covers the exhibits. Falls back to the Phase-1 chunks if no document yields usable text.
	const spineTokenBudget = 20000 // ~ the charging doc; bounds the expensive spine pass to a few calls
	zero := 0.0
	spineChunks := o.chargingDocChunks(task, spineTokenBudget)
	if len(spineChunks) == 0 { // no usable doc text → degrade gracefully to the allegation passages
		spineChunks = chunks
	}
	ckept, crej := 0, 0
	for _, chunk := range spineChunks {
		k, r := evidencegraph.ExtractTriplesInto(g, spineProv, spineModel, &zero, chunk, "")
		ckept += k
		crej += r
	}
	// Zero-yield guard (the July-3 Haiku trigger regression): a spine pass that produces NOTHING
	// — not even rejected rows — means every call failed or returned unparseable output (chatJSON
	// swallows provider errors; on that run BELO_SPINE_MODEL resolved through localize() to a
	// "local:" ID whose endpoint was a dead placeholder). Without typed triples the graph carries
	// no subsection-level `violates` edges and the analytic defense layer starves. Retry once on
	// the bulk provider/model, which Phase 1 just used successfully.
	if ckept+crej == 0 && len(spineChunks) > 0 && (spineProv != prov || spineModel != model) {
		slog.Warn("BELO spine pass yielded zero triples; retrying on the bulk provider", "task", task.ID, "spine_model", spineModel)
		for _, chunk := range spineChunks {
			k, r := evidencegraph.ExtractTriplesInto(g, prov, model, &zero, chunk, "")
			ckept += k
			crej += r
		}
	}
	if ckept+crej == 0 {
		slog.Warn("BELO spine pass produced no typed triples — spine falls back to enumeration; defense issues derive from the charging documents directly", "task", task.ID, "spine_model", spineModel)
	}
	if g.Len() == 0 {
		return
	}
	o.egraphsMu.Lock()
	o.egraphs[task.ID] = g
	o.egraphsMu.Unlock()
	// Deterministic figure floor: harvest every $/%/date/account#/citation from the docs and
	// BIND each to the graph nodes it co-occurs with, then seed the figure-bearing sentences
	// as grounded findings. Removes the run-to-run figure variance (LLM-query-driven sweep
	// missed $7.8M/$438K some runs) and makes figures ride their node into synthesis.
	// Figure-extraction model: the user-picked small model (settings/env), falling back to
	// the tool model. A 7B-class model at temp 0 is enough for deterministic copy-out
	// extraction — keeps the pipeline efficient (the heavy model is not needed here).
	figModel := strings.TrimSpace(o.cfg.Models.FigureModel)
	if figModel == "" {
		figModel = model
	}
	// The harvest's second return value is its raw normalized figureHit records —
	// the seam detectCrossDocDiscrepancies notes: feed them to crossDocFindings and
	// crossdoc's own duplicate full-corpus sweep can be dropped (follow-up wiring).
	if figs, _ := o.harvestAndBindFigures(task, g, prov, figModel); len(figs) > 0 {
		o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, figs...) })
		slog.Info("figure harvest seeded findings", "task", task.ID, "n", len(figs), "model", figModel, "graph_facts_after", g.Len())
	}
	// Cross-document discrepancy pass (crossdoc.go): same metric identity (entity +
	// quantity-kind + referent) reported with different values in different documents,
	// plus event-date conflicts (metadata vs narrative). Augments the intra-harvest
	// contradiction dimension above.
	if xd := o.detectCrossDocDiscrepancies(task, g, prov, figModel); len(xd) > 0 {
		o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, xd...) })
	}
	// Stage 2 — for a COMPLIANCE (compare/review) matter, DETECT where the document DEVIATES from
	// the controlling standard per requirement. This is the finding such tasks are scored on
	// ("residuary should be 40/35/25, draft has …"), not a description of each requirement. Runs
	// on the spine model. Enforcement matters use the figure-discrepancy path instead.
	if o.routeMatter(g) == modeCompliance {
		if devs := o.detectDeviations(task, g, spineProv, spineModel); len(devs) > 0 {
			o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, devs...) })
			slog.Info("deviations detected", "task", task.ID, "n", len(devs))
		}
	}
	// The graph's per-chunk allegation extraction (g.Allegations()) is a grounded RECALL
	// floor — fine-grained sub-issues, the wrong altitude for section headings on their own.
	// We deliberately do NOT make the spine from them directly (medoid-of-cluster headings
	// scored 20: fragmented, lost whole rubric categories). Instead ensureAllegations feeds
	// them as a SEED into the holistic, category-level synthesis (which scored 26), getting
	// graph recall at the right altitude. So nothing to set here beyond the graph itself.
	slog.Info("evidence graph built", "task", task.ID, "facts", g.Len(), "kept", kept, "grounding_rejected", rej,
		"allegation_candidates", len(g.Allegations()), "conducts", len(g.Conducts()),
		"spine_model", spineModel, "conduct_triples_kept", ckept, "conduct_triples_rejected", crej)
}

// clusterAllegations merges near-duplicate candidate allegation headings into nodes by
// embedding cosine: each cluster keeps ALL its surface forms (aliases — alternate phrasings
// the docs use, which are routing/retrieval keys) and a canonical heading (the medoid). The
// number of semantically-distinct clusters is the natural section count; a safety cap still
// applies. Deterministic given the embeddings (no LLM variance). Returns (canonicals,
// canonical→aliases). Degrades to a hard cap when no embedder is available.
func (o *Orchestrator) clusterAllegations(raw []string) ([]string, map[string][]string) {
	if len(raw) == 0 {
		return nil, nil
	}
	hardCap := func() ([]string, map[string][]string) {
		c := raw
		if len(c) > maxSpineSections {
			c = c[:maxSpineSections]
		}
		al := make(map[string][]string, len(c))
		for _, s := range c {
			al[s] = []string{s}
		}
		return c, al
	}
	if o.embedC == nil || len(raw) <= 2 {
		return hardCap()
	}
	res, err := o.embedC.EmbedBatch(raw)
	if err != nil || len(res) != len(raw) {
		return hardCap()
	}
	vecs := make([][]float32, len(raw))
	for i := range res {
		vecs[i] = res[i].Embedding
	}
	const mergeThreshold = 0.80 // short headings: high enough to keep distinct allegations apart
	type cluster struct {
		idxs     []int
		centroid []float32
	}
	var clusters []*cluster
	for i, v := range vecs {
		if len(v) == 0 {
			clusters = append(clusters, &cluster{idxs: []int{i}})
			continue
		}
		best, bestSim := -1, mergeThreshold
		for ci, c := range clusters {
			if len(c.centroid) == 0 {
				continue
			}
			if s := embeddings.CosineSimilarity(v, c.centroid); s >= bestSim {
				best, bestSim = ci, s
			}
		}
		if best < 0 {
			clusters = append(clusters, &cluster{idxs: []int{i}, centroid: append([]float32(nil), v...)})
			continue
		}
		c := clusters[best]
		c.idxs = append(c.idxs, i)
		n := float32(len(c.idxs))
		for k := range c.centroid {
			if k < len(v) {
				c.centroid[k] += (v[k] - c.centroid[k]) / n
			}
		}
	}
	canon := make([]string, 0, len(clusters))
	aliases := make(map[string][]string, len(clusters))
	for _, c := range clusters {
		members := make([]string, 0, len(c.idxs))
		for _, idx := range c.idxs {
			members = append(members, raw[idx])
		}
		rep := medoid(c.idxs, vecs, raw)
		canon = append(canon, rep)
		aliases[rep] = members
	}
	if len(canon) > maxSpineSections { // largest-first so the cap keeps the best-attested
		sort.SliceStable(canon, func(i, j int) bool { return len(aliases[canon[i]]) > len(aliases[canon[j]]) })
		for _, c := range canon[maxSpineSections:] {
			delete(aliases, c)
		}
		canon = canon[:maxSpineSections]
	}
	return canon, aliases
}

// medoid returns the cluster member whose embedding is most central (highest summed cosine
// to the others) — the most representative heading. Falls back to the first member.
func medoid(idxs []int, vecs [][]float32, labels []string) string {
	if len(idxs) == 1 {
		return labels[idxs[0]]
	}
	best, bestScore := idxs[0], -1.0
	for _, a := range idxs {
		if len(vecs[a]) == 0 {
			continue
		}
		sum := 0.0
		for _, b := range idxs {
			if a != b && len(vecs[b]) > 0 {
				sum += embeddings.CosineSimilarity(vecs[a], vecs[b])
			}
		}
		if sum > bestScore {
			best, bestScore = a, sum
		}
	}
	return labels[best]
}

// maxSpineSections caps the coverage spine: more than this and synthesis (one paged
// drafter per section on a local model) runs too long; the matter's real distinct
// allegations comfortably fit.
const maxSpineSections = 12

// evidenceGraph returns the task's evidence graph, or nil if none was built.
func (o *Orchestrator) evidenceGraph(taskID string) *evidencegraph.Graph {
	o.egraphsMu.Lock()
	defer o.egraphsMu.Unlock()
	return o.egraphs[taskID]
}

// allegationAliases returns the canonical→surface-form alias map for the task's spine
// sections, so the writer can route facts using every phrasing the docs use, not just the
// canonical heading.
func (o *Orchestrator) allegationAliases(taskID string) map[string][]string {
	o.egraphsMu.Lock()
	defer o.egraphsMu.Unlock()
	return o.egraphAliases[taskID]
}

// respondentRoster returns the matter's named individual respondents — the Person-class
// parties the typed evidence graph records as having committed conduct (committedBy →
// Person). Name variants sharing a surname collapse to the fullest form, so "Whitmore"
// and "Gerald R. Whitmore" yield one roster entry. The writer enforces one exposure
// entry per name returned here.
func (o *Orchestrator) respondentRoster(taskID string) []string {
	g := o.evidenceGraph(taskID)
	if g == nil {
		return nil
	}
	bySurname := map[string]string{}
	var order []string
	for _, c := range g.Claims() {
		if c.P != "committedBy" || c.OClass != ontology.Person {
			continue
		}
		n := strings.TrimSpace(c.O)
		if n == "" || len(n) > 60 {
			continue
		}
		fields := strings.Fields(n)
		surname := strings.ToLower(strings.Trim(fields[len(fields)-1], ".,"))
		if surname == "" {
			continue
		}
		if have, ok := bySurname[surname]; !ok {
			bySurname[surname] = n
			order = append(order, surname)
		} else if len(n) > len(have) {
			bySurname[surname] = n // keep the fullest name variant
		}
	}
	const maxRespondents = 8
	out := make([]string, 0, len(order))
	for _, s := range order {
		out = append(out, bySurname[s])
		if len(out) >= maxRespondents {
			break
		}
	}
	return out
}

// groundedFacts converts the task's evidence-graph facts into the writer's per-section
// routable form: each fact carries its display Line and a lowercased Key (subject +
// relation + object + value + quote) the writer overlap-matches against each section.
func (o *Orchestrator) groundedFacts(taskID string) []writer.Fact {
	g := o.evidenceGraph(taskID)
	if g == nil || g.Len() == 0 {
		return nil
	}
	all := g.All()
	out := make([]writer.Fact, 0, len(all))
	for _, f := range all {
		line := strings.TrimSpace(evidencegraph.Render([]evidencegraph.Fact{f}))
		key := strings.ToLower(strings.Join([]string{f.Subject, f.Relation, f.Object, f.Value, f.Quote}, " "))
		out = append(out, writer.Fact{Line: line, Key: key, Entity: f.Subject})
	}
	return out
}

// chunkByTokens splits line-oriented text into windows of at most maxTok estimated tokens,
// never splitting a line (snippets stay intact for grounded extraction).
func chunkByTokens(text string, maxTok int) []string {
	var chunks, cur []string
	tok := 0
	for _, ln := range strings.Split(text, "\n") {
		lt := strutil.EstimateTokens(ln)
		if tok+lt > maxTok && len(cur) > 0 {
			chunks = append(chunks, strings.Join(cur, "\n"))
			cur, tok = nil, 0
		}
		cur = append(cur, ln)
		tok += lt
	}
	if len(cur) > 0 {
		chunks = append(chunks, strings.Join(cur, "\n"))
	}
	return chunks
}

// chunkKey normalizes a passage to its leading ~120 alphanumerics for dedup across the
// overlapping retrieval queries (different queries surface the same chunk).
func chunkKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 120 {
				break
			}
		}
	}
	return b.String()
}

// allegationContext returns the merged allegation passages (for downstream figure
// hunting) and the matter's distinct allegations as CATEGORY-LEVEL section headings —
// a holistic synthesis over the passages, recall-checked against the evidence graph's
// grounded candidates (seed) so no secondary allegation is missed. Category altitude is
// explicit: the per-chunk graph extraction produces fine-grained sub-issues/per-person
// questions, which made bad section headings ("Whether Chao was responsible…") and lost
// whole rubric categories; the holistic synthesis at category altitude is what scored 26.
// Document-grounded, never rubric-derived. allegations is nil when nothing is found.
func (o *Orchestrator) allegationContext(task *types.Task, prov providers.Provider, model string, seed []string) (string, []string) {
	passages := o.allegationPassages(task, 5000)
	if strings.TrimSpace(passages) == "" {
		return "", nil
	}
	seedBlock := ""
	if len(seed) > 0 {
		// Recall floor: the graph already extracted these grounded candidates; ensure each
		// distinct one is represented so the synthesis doesn't drop a secondary allegation the
		// graph caught — without collapsing distinct allegations together.
		seedBlock = "\n\nThese grounded allegation candidates were extracted from the same documents — make sure each genuinely distinct one is represented among the headings (do not omit a secondary allegation):\n- " + strings.Join(seed, "\n- ")
	}
	prompt := fmt.Sprintf("TASK: %s\n\nFrom the passages below, list EVERY DISTINCT allegation, claim, charge, scheme, or required topic this matter raises, as short section headings — be EXHAUSTIVE: include secondary and party-specific allegations, not only the most prominent. Prefer the document's own enumeration where it numbers or names them (e.g. a numbered allegation category, a count, a claim). Name the allegation or topic itself, not a procedural sub-question (write \"Cherry-Picking Trade Allocations\", not \"Whether X was responsible\"). One heading per line, no numbering, no preamble. Use the matter's own terms; only topics actually present.%s\n\nPASSAGES:\n%s",
		strings.Join(strings.Fields(task.Description), " "), seedBlock, passages)
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   800,
		System:      "You extract a legal matter's full set of distinct allegations as a clean, exhaustive list of section headings, nothing else.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		return passages, nil
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
		if len(out) >= 16 { // safety cap (matters can enumerate many distinct allegations)
			break
		}
	}
	return passages, out
}

// ensureAllegations enumerates the matter's distinct allegations ONCE and caches them on
// the task, so recruitment (synthesizeAgents) and the writer's coverage spine
// (extractCoverageSpine) staff and write the SAME set. Rolling two independent
// enumerations at temperature>0 let them diverge — recruitment recruited a Bellini
// specialist while the spine missed Bellini and duplicated cherry-picking 4× — so the
// allegation was found in the rounds but had no section to land in. The result is
// theme-deduped to collapse near-duplicate headings.
// matterMode is the analytic mode BELO dispatches on, derived from the dominant epistemic issue
// type in the evidence graph. It decouples "what analysis to run" from any one practice area: a
// matter is classified by the KIND of issues its documents actually raise, and each analytic pass
// fires for the mode it serves. Extensible — transactional (clause review) and diligence
// (red-flag triage) slot in as further modes as their issue classes and passes are added.
type matterMode string

const (
	modeCompliance  matterMode = "compliance"  // Requirement issues → deviation detection (compare/review/compliance)
	modeEnforcement matterMode = "enforcement" // Conduct issues → allegation spine + defense-issue analytics
)

// classifyMatter routes by predicate DOMINANCE over BELO's typed claims, not by the presence of
// any single predicate — one stray accusation triple the model extracted from a comparison matter
// must not flip the whole routing. It DEFAULTS to compliance: reviewing a document against a
// standard is the general case; enforcement is the specialization that must earn its framing by
// accusation predicates (violates/committedBy/harmed) outweighing requirement predicates
// (requires/satisfiedBy/deviatesFrom/prohibits).
func (o *Orchestrator) routeMatter(g *evidencegraph.Graph) matterMode {
	if g == nil {
		return modeCompliance
	}
	enf, comp := 0, 0
	for _, c := range g.Claims() {
		switch c.P {
		case "violates", "committedBy", "harmed":
			enf++
		case "requires", "satisfiedBy", "deviatesFrom", "prohibits":
			comp++
		}
	}
	mode := modeCompliance
	if enf > comp {
		mode = modeEnforcement
	}
	slog.Info("matter routing", "enforcement_claims", enf, "compliance_claims", comp, "mode", mode)
	return mode
}

// crossCuttingSections are the party/timeline-oriented sections a legal enforcement memo carries
// ALONGSIDE the matter-specific allegation categories. The rubric rewards them (per-person
// exposure, the examination timeline, parties and ownership stakes); the clean conduct-only BELO
// spine dropped them, costing cross-cutting criteria the messier enumeration spine had captured.
var crossCuttingSections = []string{
	"Parties, Entities, and Ownership Interests",
	"Individuals at Risk and Personal Exposure",
	"Key Dates and Examination Timeline",
}

func (o *Orchestrator) ensureAllegations(task *types.Task, prov providers.Provider, model string) []string {
	if len(task.Allegations) > 0 {
		return task.Allegations
	}
	// BELO spine: derive the allegations from the evidence graph's typed Conduct nodes
	// (DISCOVERED via conduct-domain predicates), consolidated into distinct categories. This
	// replaces the noisy, run-varying LLM enumeration over all-docs retrieval (which grabbed
	// Form-ADV review-triggers and dropped real allegations — the ±10 spine wobble). Falls back
	// to the enumeration if disabled or the graph is too sparse.
	if o.cfg.BELOSpine {
		if g := o.evidenceGraph(task.ID); g != nil {
			conducts := g.Conducts()
			slog.Info("BELO spine decision", "task", task.ID, "flag", o.cfg.BELOSpine, "conducts", len(conducts))
			if len(conducts) >= 2 {
				cats := o.consolidateConducts(task, prov, model, conducts)
				slog.Info("BELO spine consolidate", "task", task.ID, "conducts", len(conducts), "categories", len(cats))
				if len(cats) >= 2 {
					// Enforcement matters also need the CROSS-CUTTING sections the rubric rewards
					// (per-person exposure, timeline, parties/ownership). These are enforcement-
					// framed, so add them ONLY for enforcement matters — a compliance/compare
					// matter has Requirement issues and would read oddly with "Individuals at Risk".
					if o.routeMatter(g) == modeEnforcement {
						cats = append(cats, crossCuttingSections...)
					}
					o.update(task, func(t *types.Task) { t.Allegations = cats })
					slog.Info("BELO spine from conduct nodes", "task", task.ID, "conducts", len(conducts), "categories", len(cats))
					return cats
				}
			}
		}
	}
	// Seed the holistic synthesis with the evidence graph's grounded allegation candidates
	// (recall floor), so the category-level spine never misses a secondary allegation the
	// graph caught — without inheriting the graph's fine-grained altitude.
	var seed []string
	if g := o.evidenceGraph(task.ID); g != nil {
		seed = g.Allegations()
	}
	_, allegations := o.allegationContext(task, prov, model, seed)
	allegations = dedupAllegations(allegations)
	// Coverage-net (coverAllegationClusters) is DISABLED — it regressed the weak model twice
	// (7B 23→17 under FACTS_GLOBAL, 27→20 under paged facts). The mechanism is fact-routing
	// fragmentation: its extra granular sections out-score the broad category sections on
	// cosine and STEAL facts into thin, poorly-drafted fragments. More sections is the wrong
	// lever for a weak writer; mechanical per-section/party rendering is the way. Helper kept.
	if len(allegations) > 0 {
		o.update(task, func(t *types.Task) { t.Allegations = allegations })
	}
	return allegations
}

// consolidateConducts turns the evidence graph's typed Conduct nodes into the matter's distinct
// allegation-category headings: it merges restatements of the same charge (e.g. "Obstruction of
// Examination" / "Obstructive Conduct During Examination") and drops non-allegation conducts.
// Robust because it consolidates a small, already-grounded set — unlike enumerating from noisy
// all-docs retrieval. Falls back to the deduped raw conducts on any model/parse failure.
func (o *Orchestrator) consolidateConducts(task *types.Task, prov providers.Provider, model string, conducts []string) []string {
	prompt := "These are ISSUE nodes extracted from a legal matter's evidence graph — the distinct propositions the deliverable must assess (alleged violations, client requirements/instructions, or contract clauses, depending on the matter). Merge restatements of the SAME issue into one heading, drop anything that is not a distinct issue to assess, and output the DISTINCT issue/section headings in the matter's own terms — one heading per line, no numbering, no preamble.\n\nISSUE NODES:\n- " + strings.Join(conducts, "\n- ")
	// FIX 3 — deterministic merging: temperature 0 (not LLMTemperature 0.2). Consolidating the
	// conduct nodes into spine categories is a stable, set-merge task; any wobble here reshuffles
	// the matter's section spine run-to-run, which is the variance we're driving out.
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   600,
		System:      "You consolidate extracted conduct nodes into a legal matter's distinct allegation categories — clean section headings, nothing else.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return dedupAllegations(conducts)
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
		ln = strings.TrimSpace(strings.Trim(ln, "*_#:"))
		if n := len(ln); n < 4 || n > 90 {
			continue
		}
		if k := strings.ToLower(ln); !seen[k] {
			seen[k] = true
			out = append(out, ln)
		}
		if len(out) >= maxSpineSections {
			break
		}
	}
	if len(out) < 2 {
		return dedupAllegations(conducts)
	}
	return out
}

// coverAllegationClusters guarantees every grounded allegation cluster is represented in the
// spine. It clusters the graph's grounded candidates and appends the representative of any
// cluster not already covered (by embedding similarity) by the LLM enumeration. When the
// enumeration is empty (very weak model), this degrades to a pure grounded-cluster spine —
// still covering every allegation the graph caught. Generalizable: no rubric, no hardcoding.
func (o *Orchestrator) coverAllegationClusters(have, candidates []string) []string {
	if len(candidates) == 0 || o.embedC == nil {
		return have
	}
	reps, _ := o.clusterAllegations(candidates)
	if len(reps) == 0 {
		return have
	}
	all := append(append([]string{}, have...), reps...)
	res, err := o.embedC.EmbedBatch(all)
	if err != nil || len(res) != len(all) {
		return have
	}
	haveVecs, repVecs := res[:len(have)], res[len(have):]
	const coveredThreshold = 0.72 // short legal headings via nomic: same-topic, not identical
	out := append([]string{}, have...)
	for i, rep := range reps {
		rv := repVecs[i].Embedding
		if len(rv) == 0 {
			continue
		}
		covered := false
		for _, hv := range haveVecs {
			if len(hv.Embedding) > 0 && embeddings.CosineSimilarity(rv, hv.Embedding) >= coveredThreshold {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, rep) // a grounded allegation the enumeration missed
		}
	}
	out = dedupAllegations(out)
	if len(out) > maxSpineSections { // keep enumeration headings first, then the recovered gaps
		out = out[:maxSpineSections]
	}
	return out
}

// dedupAllegations collapses headings that name the same allegation under different
// category numbers/prefixes (e.g. "Allegation Category 1 — Cherry-Picking" and
// "Category 5 — Cherry-Picking"): it strips leading category/number/roman prefixes and
// keys on the remaining content words, keeping the first (richest-ordered) occurrence.
func dedupAllegations(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, h := range in {
		if themeKey(h) == "" || seen[themeKey(h)] {
			continue
		}
		seen[themeKey(h)] = true
		out = append(out, h)
	}
	return out
}

var reAllegPrefix = regexp.MustCompile(`(?i)^\s*(allegation\s+)?(category|count|claim|issue|item|no\.?|number|section|part)\s*[ivxlcdm0-9]+\s*[-–—:.)]*\s*`)

// themeKey reduces a heading to its content-word signature for dedup: drop category/
// number prefixes, lowercase, keep alphanumerics, sort the words (so order/phrasing
// differences collapse).
func themeKey(h string) string {
	h = reAllegPrefix.ReplaceAllString(strings.TrimSpace(h), "")
	var words []string
	for _, w := range strings.Fields(strings.ToLower(h)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if s := b.String(); len(s) >= 4 { // skip short connectives (and, the, of)
			words = append(words, s)
		}
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// draftingAgentVoices selects the writing-agent bench for synthesis and returns each as an
// EXPERTISE/voice line (name + description) — NOT the agent's whole-document template, which
// crammed IRAC / "exec-summary→findings→recommendations" into every section. The lead is
// index 0; contributors follow. Selected by semantic fit to the deliverable so the right
// agents (report/summary/exec, not a fixed memo agent) are seated; the writer appends these
// over its clean section-part base.
func (o *Orchestrator) draftingAgentVoices(task *types.Task) []string {
	n := o.cfg.Drafting.AgentsPerSection
	if n < 1 {
		n = 2
	}
	voice := func(a *types.AgentDefinition) string {
		return "Bring the perspective of the " + a.Name + ": " + strings.Join(strings.Fields(a.Description), " ")
	}
	var voices []string
	seen := map[string]bool{}
	query := strings.Join(strings.Fields(task.Description), " ")
	if cands, err := o.registry.Search(query, agents.SearchOpts{TopK: 50}); err == nil {
		for i := range cands {
			a := cands[i]
			if a.Domain != types.DomainDrafting || seen[a.ID] || strings.TrimSpace(a.Description) == "" {
				continue
			}
			seen[a.ID] = true
			voices = append(voices, voice(&a))
			if len(voices) >= n {
				break
			}
		}
	}
	// Ensure a lead + at least one contributor from known general drafters.
	for _, id := range []string{"due-diligence-report-drafter", "executive-summary-drafter", "legal-research-memo-drafter"} {
		if len(voices) >= n || len(voices) >= 2 && len(voices) >= n {
			break
		}
		if seen[id] {
			continue
		}
		if a := o.registry.GetByID(id); a != nil && strings.TrimSpace(a.Description) != "" {
			seen[id] = true
			voices = append(voices, voice(a))
		}
	}
	return voices
}

// writingAgentSystem is the single-drafter (non-DyTopo) path's voice: the lead drafting
// agent's expertise line, appended over the writer's clean section base.
func (o *Orchestrator) writingAgentSystem(task *types.Task) string {
	if v := o.draftingAgentVoices(task); len(v) > 0 {
		return v[0]
	}
	return ""
}

// extractCoverageSpine returns the matter's enumerated allegations as the writer's
// required sections — the SAME shared set recruitment staffed — so every allegation a
// specialist analysed has a guaranteed section. Returns nil when nothing enumerable is
// found (the writer then falls back to clustering).
func (o *Orchestrator) extractCoverageSpine(task *types.Task, prov providers.Provider, model string) []string {
	allegations := o.ensureAllegations(task, prov, model)
	if len(allegations) < 2 {
		return nil // not a usable spine; writer falls back to clustering
	}
	slog.Info("coverage spine extracted", "task", task.ID, "sections", len(allegations))
	return allegations
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
	quarantined := 0
	for _, t := range items {
		if _, ok := phaseSequences[t.WorkflowType]; !ok {
			continue
		}
		normalizeTask(t)
		if o.quarantineStaleTask(t) { // see restore.go — no runner survives a restart
			quarantined++
		}
		o.tasks[t.ID] = t
		o.gateChans[t.ID] = make(chan struct{}, 8)
	}
	o.mu.Unlock()
	if quarantined > 0 {
		o.persistTasks()
	}
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
