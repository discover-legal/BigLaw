// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"context"
	"errors"
	"time"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Local serves the Backend from the in-process services of the process that
// owns the data dir. It acts as auth.LocalPartner — the same principal the
// REST API assigns every request when AUTH_ENABLED=false — so tasks and
// documents it creates are attributed exactly as REST-created ones are.
type Local struct {
	Orch      *orchestrator.Orchestrator
	Knowledge *knowledge.Store
	Registry  *agents.Registry
	Plugins   *adapters.Registry // may be nil: no plugins loaded
	Time      *timekeeping.TimeStore
	Audit     *audit.Logger // nil → audit.Default

	actor types.SessionUser
}

// NewLocal wires a Local backend over the owner's services.
func NewLocal(
	orch *orchestrator.Orchestrator,
	knowledgeStore *knowledge.Store,
	registry *agents.Registry,
	plugins *adapters.Registry,
	timeStore *timekeeping.TimeStore,
) *Local {
	return &Local{
		Orch:      orch,
		Knowledge: knowledgeStore,
		Registry:  registry,
		Plugins:   plugins,
		Time:      timeStore,
		actor:     auth.LocalPartner,
	}
}

var _ Backend = (*Local)(nil)

func (l *Local) audit() *audit.Logger {
	if l.Audit != nil {
		return l.Audit
	}
	return audit.Default
}

func (l *Local) SubmitTask(_ context.Context, in SubmitTaskInput) (*types.Task, error) {
	task, err := l.Orch.SubmitTask(orchestrator.SubmitParams{
		Description:        in.Description,
		WorkflowType:       in.WorkflowType,
		Jurisdiction:       in.Jurisdiction,
		ClientNumber:       in.ClientNumber,
		MatterNumber:       in.MatterNumber,
		CreatedByProfileID: l.actor.ProfileID,
	})
	if errors.Is(err, orchestrator.ErrTaskQueueFull) {
		return nil, newErr(ErrUnavailable, err.Error())
	}
	if err != nil {
		return nil, newErr(ErrInvalid, err.Error())
	}
	return task, nil
}

func (l *Local) GetTask(_ context.Context, taskID string) (*types.Task, error) {
	task := l.Orch.GetTask(taskID)
	if task == nil {
		return nil, newErr(ErrNotFound, "task not found")
	}
	return task, nil
}

func (l *Local) ListTasks(context.Context) ([]*types.Task, error) {
	tasks := l.Orch.ListTasks()
	if tasks == nil {
		tasks = []*types.Task{}
	}
	return tasks, nil
}

// pendingGate mirrors the REST handlers' pre-check: an unknown task or a gate
// that is not pending reads as "gate not found".
func (l *Local) pendingGate(taskID, gateID string) bool {
	task := l.Orch.GetTask(taskID)
	if task == nil {
		return false
	}
	for _, g := range task.PendingGates {
		if g.ID == gateID && g.Status == "pending" {
			return true
		}
	}
	return false
}

func (l *Local) ApproveGate(_ context.Context, taskID, gateID, note string) error {
	if !l.pendingGate(taskID, gateID) {
		return newErr(ErrNotFound, "gate not found")
	}
	if err := l.Orch.ApproveGate(taskID, gateID, note, l.actor.ProfileID); err != nil {
		return newErr(ErrNotFound, err.Error())
	}
	return nil
}

func (l *Local) RejectGate(_ context.Context, taskID, gateID, reason string) error {
	if reason == "" {
		return newErr(ErrInvalid, "reason is required")
	}
	if !l.pendingGate(taskID, gateID) {
		return newErr(ErrNotFound, "gate not found")
	}
	if err := l.Orch.RejectGate(taskID, gateID, reason, l.actor.ProfileID); err != nil {
		return newErr(ErrNotFound, err.Error())
	}
	return nil
}

func (l *Local) SubmitFromTemplate(_ context.Context, in SubmitFromTemplateInput) (*types.Task, error) {
	if in.TemplateID == "" {
		return nil, newErr(ErrInvalid, "templateId is required")
	}
	task, err := l.Orch.SubmitFromTemplate(in.TemplateID, in.Substitutions, in.DocumentIDs,
		orchestrator.SubmitParams{CreatedByProfileID: l.actor.ProfileID})
	if err != nil {
		return nil, newErr(ErrInvalid, err.Error())
	}
	return task, nil
}

func (l *Local) ListTemplates(context.Context) ([]types.TaskTemplate, error) {
	list := l.Orch.ListTemplates()
	if list == nil {
		list = []types.TaskTemplate{}
	}
	return list, nil
}

// GetRound addresses a round by its goal's round number, as
// GET /tasks/:id/rounds/:round does.
func (l *Local) GetRound(_ context.Context, taskID string, round int) (*types.RoundState, error) {
	task := l.Orch.GetTask(taskID)
	if task == nil {
		return nil, newErr(ErrNotFound, "task not found")
	}
	if round < 1 {
		return nil, newErr(ErrInvalid, "round must be a positive integer")
	}
	for i := range task.Rounds {
		if task.Rounds[i].Goal.Round == round {
			r := task.Rounds[i]
			return &r, nil
		}
	}
	return nil, newErr(ErrNotFound, "round not found")
}

func (l *Local) IngestDocument(ctx context.Context, in IngestInput) (*types.Document, error) {
	if in.Title == "" {
		return nil, newErr(ErrInvalid, "title is required")
	}
	if in.Content == "" {
		return nil, newErr(ErrInvalid, "content is required")
	}
	ownerID := in.OwnerID
	if !auth.IsPartner(&l.actor) || ownerID == "" {
		ownerID = l.actor.ProfileID
	}
	doc := types.Document{
		Title:      in.Title,
		Content:    in.Content,
		OwnerID:    ownerID,
		IngestedAt: time.Now(),
	}
	idCtx := store.WithIdentity(ctx, l.actor.ProfileID, auth.IsPartner(&l.actor))
	saved, err := l.Knowledge.Ingest(idCtx, doc)
	if err != nil {
		return nil, newErr(ErrInternal, "ingest failed: "+err.Error())
	}
	l.audit().Write(audit.WriteRequest{
		Event:   "document.ingested",
		ActorID: l.actor.ProfileID,
		Data:    map[string]interface{}{"docId": saved.ID, "title": saved.Title},
	})
	return saved, nil
}

func (l *Local) SearchKnowledge(_ context.Context, query string, topK int) ([]types.SearchResult, error) {
	if query == "" {
		return nil, newErr(ErrInvalid, "q parameter is required")
	}
	results, err := l.Knowledge.Search(query, knowledge.SearchOpts{TopK: clampTopK(topK)})
	if err != nil {
		return nil, newErr(ErrInternal, "search failed: "+err.Error())
	}
	if results == nil {
		results = []types.SearchResult{}
	}
	return results, nil
}

func (l *Local) ListAgents(context.Context) ([]types.AgentDefinition, error) {
	all := l.Registry.ListAll()
	if all == nil {
		all = []types.AgentDefinition{}
	}
	return all, nil
}

func (l *Local) ReadAudit(_ context.Context, q AuditQuery) ([]audit.AuditEntry, error) {
	entries := l.audit().ReadFiltered(audit.Filter{TaskID: q.TaskID, Limit: clampAuditLimit(q.Limit)})
	if entries == nil {
		entries = []audit.AuditEntry{}
	}
	return entries, nil
}

func (l *Local) ListPlugins(context.Context) ([]PluginSummary, error) {
	return SummarizePlugins(l.Plugins), nil
}

// SummarizePlugins renders a plugin registry as the GET /plugins payload.
func SummarizePlugins(reg *adapters.Registry) []PluginSummary {
	out := []PluginSummary{}
	if reg == nil {
		return out
	}
	for _, p := range reg.List() {
		out = append(out, PluginSummary{
			ID:          p.Plugin.ID,
			Name:        p.Plugin.Name,
			Version:     p.Plugin.Version,
			Description: p.Plugin.Description,
			Source:      "json", // the Go port loads JSON adapters only
			Enabled:     p.Enabled,
			Tools:       len(p.Plugin.Tools),
			Agents:      len(p.Plugin.Agents),
			Workflows:   len(p.Plugin.Workflows),
		})
	}
	return out
}

func (l *Local) ListTimeEntries(_ context.Context, f timekeeping.TimeFilter) ([]types.TimeEntry, error) {
	entries := l.Time.List(f)
	if entries == nil {
		entries = []types.TimeEntry{}
	}
	return entries, nil
}
