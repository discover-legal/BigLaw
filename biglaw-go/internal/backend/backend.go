// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package backend is the seam between BigLaw's MCP server and the platform
// it drives. Backend names exactly what the MCP tools need; two
// implementations satisfy it:
//
//   - Local wraps the in-process services of the process that owns the data
//     dir (standalone mode, or auto mode when it wins the data-dir lock).
//   - Remote is a thin HTTP client of an owner's REST API (mcp mode, or auto
//     mode when another process holds the lock). It opens no database, starts
//     no monitors and loads no vector store.
//
// Local deliberately mirrors the REST handlers' semantics and error texts for
// its (partner) principal, so a tool returns the same result whichever side
// of the seam it runs on — internal/mcp's contract test pins that.
//
// This is the Go counterpart of the TypeScript LegalBackend / LocalBackend /
// RemoteBackend seam (git tag typescript-final, src/backend/index.ts).
package backend

import (
	"context"
	"errors"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Backend is everything the MCP server needs from BigLaw — one method per
// tool need (query_memory and get_audit share ReadAudit).
type Backend interface {
	SubmitTask(ctx context.Context, in SubmitTaskInput) (*types.Task, error)
	GetTask(ctx context.Context, taskID string) (*types.Task, error)
	ListTasks(ctx context.Context) ([]*types.Task, error)
	ApproveGate(ctx context.Context, taskID, gateID, note string) error
	RejectGate(ctx context.Context, taskID, gateID, reason string) error
	SubmitFromTemplate(ctx context.Context, in SubmitFromTemplateInput) (*types.Task, error)
	ListTemplates(ctx context.Context) ([]types.TaskTemplate, error)
	GetRound(ctx context.Context, taskID string, round int) (*types.RoundState, error)
	IngestDocument(ctx context.Context, in IngestInput) (*types.Document, error)
	SearchKnowledge(ctx context.Context, query string, topK int) ([]types.SearchResult, error)
	ListAgents(ctx context.Context) ([]types.AgentDefinition, error)
	ReadAudit(ctx context.Context, q AuditQuery) ([]audit.AuditEntry, error)
	ListPlugins(ctx context.Context) ([]PluginSummary, error)
	ListTimeEntries(ctx context.Context, f timekeeping.TimeFilter) ([]types.TimeEntry, error)
}

// SubmitTaskInput is the body of POST /tasks.
type SubmitTaskInput struct {
	Description  string             `json:"description"`
	WorkflowType types.WorkflowType `json:"workflowType"`
	Jurisdiction string             `json:"jurisdiction,omitempty"`
	ClientNumber string             `json:"clientNumber,omitempty"`
	MatterNumber string             `json:"matterNumber,omitempty"`
}

// SubmitFromTemplateInput is the body of POST /tasks/from-template.
type SubmitFromTemplateInput struct {
	TemplateID    string            `json:"templateId"`
	Substitutions map[string]string `json:"substitutions,omitempty"`
	DocumentIDs   []string          `json:"documentIds,omitempty"`
}

// IngestInput is the body of POST /documents. OwnerID is honoured only for a
// partner principal; otherwise (or when empty) the principal owns the doc.
type IngestInput struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	OwnerID string `json:"ownerID,omitempty"`
}

// AuditQuery selects recent audit entries (GET /audit). Limit <= 0 means the
// REST default (200); values above 1000 are clamped.
type AuditQuery struct {
	TaskID string
	Limit  int
}

// PluginSummary describes one loaded external plugin (GET /plugins):
// descriptor fields and counts, never API keys or endpoints.
type PluginSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	Enabled     bool   `json:"enabled"`
	Tools       int    `json:"tools"`
	Agents      int    `json:"agents"`
	Workflows   int    `json:"workflows"`
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// Error kinds. Every error a Backend returns wraps exactly one of these, so
// callers branch with errors.Is regardless of which implementation ran.
var (
	ErrInvalid      = errors.New("invalid request")        // REST 400
	ErrUnauthorized = errors.New("unauthorized")           // REST 401 / 403
	ErrNotFound     = errors.New("not found")              // REST 404
	ErrUnavailable  = errors.New("backend unavailable")    // REST 503, or unreachable
	ErrInternal     = errors.New("backend internal error") // REST 5xx, anything else
)

// Error is a Backend failure: a kind plus the human message the REST API
// would put in its {"error": …} body. Error() returns only the message.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

func newErr(kind error, msg string) error { return &Error{Kind: kind, Msg: msg} }

// ─── Shared defaults (one definition for both implementations) ───────────────

const (
	defaultSearchTopK = 5
	maxSearchTopK     = 50
	defaultAuditLimit = 200
	maxAuditLimit     = 1000
)

func clampTopK(k int) int {
	if k < 1 {
		return defaultSearchTopK
	}
	if k > maxSearchTopK {
		return maxSearchTopK
	}
	return k
}

func clampAuditLimit(n int) int {
	if n < 1 {
		return defaultAuditLimit
	}
	if n > maxAuditLimit {
		return maxAuditLimit
	}
	return n
}

// timeParam renders a filter bound the way GET /time-entries parses it.
func timeParam(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
