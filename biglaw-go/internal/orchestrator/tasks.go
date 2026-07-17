// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/clientvoice"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/templates"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/google/uuid"
)

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

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskActive   = errors.New("task is already active")
)

func (o *Orchestrator) SubmitTask(params SubmitParams) (*types.Task, error) {
	if len(params.Description) > maxDescriptionChars {
		return nil, fmt.Errorf("description exceeds %d character limit", maxDescriptionChars)
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
		Status:             types.TaskStatusQueued,
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

	o.mu.Lock()
	o.tasks[task.ID] = task
	o.gateChans[task.ID] = make(chan struct{}, 8)
	o.mu.Unlock()
	if err := o.persistTasks(); err != nil {
		o.mu.Lock()
		delete(o.tasks, task.ID)
		delete(o.gateChans, task.ID)
		o.mu.Unlock()
		return nil, fmt.Errorf("persist queued task: %w", err)
	}
	if err := o.scheduler.enqueue(queuedTask{id: task.ID, workflow: task.WorkflowType}); err != nil {
		o.mu.Lock()
		delete(o.tasks, task.ID)
		delete(o.gateChans, task.ID)
		o.mu.Unlock()
		_ = o.persistTasks()
		return nil, err
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "task.created",
		ActorID: orSystem(task.CreatedByProfileID),
		TaskID:  task.ID,
		Data:    map[string]interface{}{"description": strutil.Truncate(params.Description, 200), "workflowType": params.WorkflowType},
	})
	emitProgress(task.ID, "queued", map[string]interface{}{"workflowType": task.WorkflowType})
	return o.GetTask(task.ID), nil
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
	t := o.tasks[id]
	if t == nil {
		o.mu.RUnlock()
		return nil
	}
	cp := cloneTaskForRead(t)
	o.mu.RUnlock()
	cp.Queue = o.taskQueueInfo(cp, time.Now())
	return cp
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
	cp := cloneTaskForRead(task)
	o.mu.RUnlock()
	return cp
}

func (o *Orchestrator) ListTasks() []*types.Task {
	o.mu.RLock()
	out := make([]*types.Task, 0, len(o.tasks))
	for _, t := range o.tasks {
		out = append(out, cloneTaskForRead(t))
	}
	o.mu.RUnlock()
	now := time.Now()
	for _, task := range out {
		task.Queue = o.taskQueueInfo(task, now)
	}
	return out
}

func cloneTaskForRead(task *types.Task) *types.Task {
	cp := *task
	cp.AssignedLawyerIDs = append([]string(nil), task.AssignedLawyerIDs...)
	cp.DocumentIDs = append([]string(nil), task.DocumentIDs...)
	cp.ActiveAgentIDs = append([]string(nil), task.ActiveAgentIDs...)
	cp.Rounds = append([]types.RoundState(nil), task.Rounds...)
	cp.Findings = append([]types.Finding(nil), task.Findings...)
	cp.PendingGates = append([]types.GateRequest(nil), task.PendingGates...)
	cp.Controversies = append([]types.Controversy(nil), task.Controversies...)
	cp.Allegations = append([]string(nil), task.Allegations...)
	cp.StarvedRounds = append([]types.StarvedRound(nil), task.StarvedRounds...)
	cp.Queue = nil
	return &cp
}

func (o *Orchestrator) taskQueueInfo(task *types.Task, now time.Time) *types.TaskQueueInfo {
	if o.scheduler == nil {
		return nil
	}
	return o.scheduler.info(task.ID, task.Status, task.WorkflowType, now)
}

func (o *Orchestrator) DeleteTask(id string) error {
	o.mu.RLock()
	task := o.tasks[id]
	if task == nil {
		o.mu.RUnlock()
		return ErrTaskNotFound
	}
	status := task.Status
	o.mu.RUnlock()

	if status == types.TaskStatusRunning || status == types.TaskStatusAwaitingGate {
		return ErrTaskActive
	}
	if (status == types.TaskStatusQueued || status == types.TaskStatusPending) && o.scheduler != nil && !o.scheduler.remove(id) {
		// A worker claimed it between the status read and queue removal.
		return ErrTaskActive
	}

	o.mu.Lock()
	if _, exists := o.tasks[id]; !exists {
		o.mu.Unlock()
		return ErrTaskNotFound
	}
	delete(o.tasks, id)
	delete(o.gateChans, id)
	o.mu.Unlock()
	if err := o.persistTasks(); err != nil {
		return fmt.Errorf("persist task deletion: %w", err)
	}
	audit.Default.Write(audit.WriteRequest{Event: "task.deleted", ActorID: audit.ActorSystem, TaskID: id, Data: map[string]interface{}{}})
	return nil
}

func (o *Orchestrator) AssignLawyers(taskID string, lawyerIDs []string, actorID string) *types.Task {
	o.mu.Lock()
	task := o.tasks[taskID]
	if task == nil {
		o.mu.Unlock()
		return nil
	}
	task.AssignedLawyerIDs = append([]string(nil), lawyerIDs...)
	task.UpdatedAt = time.Now()
	cp := cloneTaskForRead(task)
	o.mu.Unlock()
	o.persistTasks()
	audit.Default.Write(audit.WriteRequest{Event: "task.assigned", ActorID: orSystem(actorID), TaskID: taskID, Data: map[string]interface{}{"lawyerIds": lawyerIDs}})
	cp.Queue = o.taskQueueInfo(cp, time.Now())
	return cp
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
	found := false
	for i := range gates {
		if gates[i].ID == gateID && gates[i].Status == "pending" {
			found = true
			gates[i].Status = "approved"
			gates[i].ReviewerNote = note
			now := time.Now()
			gates[i].ReviewedAt = &now
			task.UpdatedAt = time.Now()
			break
		}
	}
	if !found {
		o.mu.Unlock()
		return fmt.Errorf("pending gate not found: %s", gateID)
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
	found := false
	for i := range gates {
		if gates[i].ID == gateID && gates[i].Status == "pending" {
			found = true
			gates[i].Status = "rejected"
			gates[i].ReviewerNote = reason
			now := time.Now()
			gates[i].ReviewedAt = &now
			findingID = gates[i].FindingID
			task.UpdatedAt = time.Now()
			break
		}
	}
	if !found {
		o.mu.Unlock()
		return fmt.Errorf("pending gate not found: %s", gateID)
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
