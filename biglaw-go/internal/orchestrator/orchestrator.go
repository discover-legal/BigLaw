// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Top-level orchestrator — task lifecycle, phase sequencing, synthesis.

package orchestrator

import (
	"fmt"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/agents"
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
	"github.com/discover-legal/biglaw-go/internal/protocols"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
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
	maxDescriptionChars = 20_000
)

// Orchestrator ties together all subsystems.
type Orchestrator struct {
	mu          sync.RWMutex
	persistMu   sync.Mutex
	tasks       map[string]*types.Task
	gateChans   map[string]chan struct{} // taskID → signal channel for gate resolution
	scheduler   *taskScheduler
	workersOnce sync.Once

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

	// reentry holds the per-task re-entrant-machinery state (reentry.go): the graph
	// snapshot the round-boundary delta is computed against, the dedup keys for
	// everything the machinery already emitted, and the round-0 figure harvest the
	// cross-document re-join reuses. Keyed by task ID; transient, like egraphs.
	reentry   map[string]*reentryState
	reentryMu sync.Mutex

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
		scheduler:     newTaskScheduler(cfg.Queue.Concurrency, cfg.Queue.MaxPending),
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
		reentry:       map[string]*reentryState{},
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
	o.startTaskWorkers()
	return nil
}
