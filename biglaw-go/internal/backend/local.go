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

// Local holds the in-process services of the process that owns the data
// dir. It is not itself a Backend: every operation runs as somebody, so
// callers take a principal's view with As — the REST handlers with the
// request's session user, the in-process MCP server with its configured
// principal.
type Local struct {
	Orch      *orchestrator.Orchestrator
	Knowledge *knowledge.Store
	Registry  *agents.Registry
	Plugins   *adapters.Registry // nil: no plugins loaded
	Time      *timekeeping.TimeStore
	Audit     *audit.Logger // nil → audit.Default
}

// NewLocal wires a Local over the owner's services.
func NewLocal(
	orch *orchestrator.Orchestrator,
	knowledgeStore *knowledge.Store,
	registry *agents.Registry,
	plugins *adapters.Registry,
	timeStore *timekeeping.TimeStore,
) *Local {
	return &Local{Orch: orch, Knowledge: knowledgeStore, Registry: registry, Plugins: plugins, Time: timeStore}
}

// As returns the Backend seen by principal u. The view applies BigLaw's
// access rules: a partner sees everything; anyone else sees only tasks they
// created or are assigned to, their own documents, time entries and audit
// actions, and no partner-only listings. A zero SessionUser is anonymous and
// sees nothing private.
func (l *Local) As(u types.SessionUser) Backend {
	return &view{l: l, u: u}
}

// PrincipalFor builds the session principal for a stored lawyer profile —
// what a bearer credential bound to that profile acts as.
func PrincipalFor(p *types.LawyerProfile) types.SessionUser {
	return types.SessionUser{
		ProfileID: p.ID,
		Name:      p.Name,
		Email:     p.Email,
		Role:      p.Role,
		Mode:      auth.ResolveMode(p.Role, p.Mode),
	}
}

type view struct {
	l *Local
	u types.SessionUser
}

var _ Backend = (*view)(nil)

func (v *view) partner() bool { return auth.IsPartner(&v.u) }

func (v *view) canView(t *types.Task) bool {
	return v.partner() || (v.u.ProfileID != "" && t.CreatedByProfileID == v.u.ProfileID) ||
		auth.CanViewTask(&v.u, t.AssignedLawyerIDs)
}

func (v *view) audit() *audit.Logger {
	if v.l.Audit != nil {
		return v.l.Audit
	}
	return audit.Default
}

// visibleTask returns the task or the not-found error an invisible task
// also gets (existence is not disclosed).
func (v *view) visibleTask(taskID string) (*types.Task, error) {
	task := v.l.Orch.GetTask(taskID)
	if task == nil || !v.canView(task) {
		return nil, newErr(ErrNotFound, "task not found")
	}
	return task, nil
}

func (v *view) SubmitTask(_ context.Context, in SubmitTaskInput) (*types.Task, error) {
	if err := checkSize(in); err != nil {
		return nil, err
	}
	if in.Description == "" {
		return nil, newErr(ErrInvalid, "description is required")
	}
	if in.WorkflowType == "" {
		in.WorkflowType = types.WorkflowRoundtable
	}
	task, err := v.l.Orch.SubmitTask(orchestrator.SubmitParams{
		Description:        in.Description,
		WorkflowType:       in.WorkflowType,
		DocumentIDs:        in.DocumentIDs,
		Jurisdiction:       in.Jurisdiction,
		ClientNumber:       in.ClientNumber,
		MatterNumber:       in.MatterNumber,
		CreatedByProfileID: v.u.ProfileID,
	})
	if errors.Is(err, orchestrator.ErrTaskQueueFull) {
		return nil, newErr(ErrUnavailable, err.Error())
	}
	if err != nil {
		return nil, newErr(ErrInvalid, err.Error())
	}
	return task, nil
}

func (v *view) GetTask(_ context.Context, taskID string) (*types.Task, error) {
	return v.visibleTask(taskID)
}

func (v *view) ListTasks(context.Context) ([]*types.Task, error) {
	out := []*types.Task{}
	for _, t := range v.l.Orch.ListTasks() {
		if v.canView(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// reviewableGate: the task is visible and the gate is still pending; anything
// else reads as "gate not found".
func (v *view) reviewableGate(taskID, gateID string) error {
	task, err := v.visibleTask(taskID)
	if err == nil {
		for _, g := range task.PendingGates {
			if g.ID == gateID && g.Status == "pending" {
				return nil
			}
		}
	}
	return newErr(ErrNotFound, "gate not found")
}

func (v *view) ApproveGate(_ context.Context, taskID, gateID, note string) error {
	if err := checkSize(gateNote{note}); err != nil {
		return err
	}
	if err := v.reviewableGate(taskID, gateID); err != nil {
		return err
	}
	if err := v.l.Orch.ApproveGate(taskID, gateID, note, v.u.ProfileID); err != nil {
		return newErr(ErrNotFound, err.Error())
	}
	return nil
}

func (v *view) RejectGate(_ context.Context, taskID, gateID, reason string) error {
	if err := checkSize(gateReason{reason}); err != nil {
		return err
	}
	if reason == "" {
		return newErr(ErrInvalid, "reason is required")
	}
	if err := v.reviewableGate(taskID, gateID); err != nil {
		return err
	}
	if err := v.l.Orch.RejectGate(taskID, gateID, reason, v.u.ProfileID); err != nil {
		return newErr(ErrNotFound, err.Error())
	}
	return nil
}

func (v *view) SubmitFromTemplate(_ context.Context, in SubmitFromTemplateInput) (*types.Task, error) {
	if err := checkSize(in); err != nil {
		return nil, err
	}
	if in.TemplateID == "" {
		return nil, newErr(ErrInvalid, "templateId is required")
	}
	task, err := v.l.Orch.SubmitFromTemplate(in.TemplateID, in.Substitutions, in.DocumentIDs,
		orchestrator.SubmitParams{CreatedByProfileID: v.u.ProfileID})
	if err != nil {
		return nil, newErr(ErrInvalid, err.Error())
	}
	return task, nil
}

func (v *view) ListTemplates(context.Context) ([]types.TaskTemplate, error) {
	list := v.l.Orch.ListTemplates()
	if list == nil {
		list = []types.TaskTemplate{}
	}
	return list, nil
}

// GetRound addresses a round by its goal's round number.
func (v *view) GetRound(_ context.Context, taskID string, round int) (*types.RoundState, error) {
	task, err := v.visibleTask(taskID)
	if err != nil {
		return nil, err
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

func (v *view) IngestDocument(ctx context.Context, in IngestInput) (*types.Document, error) {
	if err := checkSize(in); err != nil {
		return nil, err
	}
	if in.Title == "" {
		return nil, newErr(ErrInvalid, "title is required")
	}
	if in.Content == "" {
		return nil, newErr(ErrInvalid, "content is required")
	}
	ownerID := in.OwnerID
	if !v.partner() || ownerID == "" {
		ownerID = v.u.ProfileID // only a partner files a document under someone else
	}
	doc := types.Document{Title: in.Title, Content: in.Content, OwnerID: ownerID, IngestedAt: time.Now()}
	saved, err := v.l.Knowledge.Ingest(store.WithIdentity(ctx, v.u.ProfileID, v.partner()), doc)
	if err != nil {
		return nil, newErr(ErrInternal, "ingest failed: "+err.Error())
	}
	v.audit().Write(audit.WriteRequest{
		Event:   "document.ingested",
		ActorID: v.u.ProfileID,
		Data:    map[string]interface{}{"docId": saved.ID, "title": saved.Title},
	})
	return saved, nil
}

func (v *view) SearchKnowledge(_ context.Context, query string, topK int) ([]types.SearchResult, error) {
	if query == "" {
		return nil, newErr(ErrInvalid, "q parameter is required")
	}
	opts := knowledge.SearchOpts{TopK: clampTopK(topK)}
	if !v.partner() {
		opts.OwnerID = v.u.ProfileID // lawyers search their own documents only
		if opts.OwnerID == "" {
			return []types.SearchResult{}, nil
		}
	}
	results, err := v.l.Knowledge.Search(query, opts)
	if err != nil {
		return nil, newErr(ErrInternal, "search failed: "+err.Error())
	}
	if results == nil {
		results = []types.SearchResult{}
	}
	return results, nil
}

func (v *view) ListAgents(context.Context) ([]types.AgentDefinition, error) {
	all := v.l.Registry.ListAll()
	if all == nil {
		all = []types.AgentDefinition{}
	}
	return all, nil
}

func (v *view) ReadAudit(_ context.Context, q AuditQuery) ([]audit.AuditEntry, error) {
	f := audit.Filter{TaskID: q.TaskID, ActorID: q.ActorID, Event: q.Event, Limit: clampAuditLimit(q.Limit)}
	if !v.partner() {
		f.ActorID = v.u.ProfileID // lawyers see only their own actions
		if f.ActorID == "" {
			return []audit.AuditEntry{}, nil
		}
	}
	entries := v.audit().ReadFiltered(f)
	if entries == nil {
		entries = []audit.AuditEntry{}
	}
	return entries, nil
}

func (v *view) ListPlugins(context.Context) ([]PluginSummary, error) {
	if !v.partner() {
		return nil, newErr(ErrForbidden, "partner access required")
	}
	return SummarizePlugins(v.l.Plugins), nil
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

func (v *view) ListTimeEntries(_ context.Context, f timekeeping.TimeFilter) ([]types.TimeEntry, error) {
	if !v.partner() {
		f.ProfileID = v.u.ProfileID // lawyers are locked to their own entries
		if f.ProfileID == "" {
			return []types.TimeEntry{}, nil
		}
	}
	entries := v.l.Time.List(f)
	if entries == nil {
		entries = []types.TimeEntry{}
	}
	return entries, nil
}
