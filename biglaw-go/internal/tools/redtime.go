// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Redtime tools — the redline timeline. register_document_version registers
// an on-disk document (uploads, manual entry) into a version lineage;
// get_redline_timeline builds the per-clause negotiation history of a lineage
// so agents can reason over how the language evolved, who moved when, and how
// far the current draft sits from the playbook position. This file also holds
// the hook helpers respond_to_redline and edit_document call so versions
// accrue automatically.

package tools

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/negotiate"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/redtime"
	"github.com/discover-legal/biglaw-go/internal/store"
)

func (r *Registry) registerRedtimeTools() {
	r.Register(r.registerDocumentVersionTool())
	r.Register(r.getRedlineTimelineTool())
}

// versions returns the durable document-version repository when the
// configured store supports it. The registry holds its store handle as a
// ReviewRepository (the field predates Redtime); the concrete
// sqlite/postgres/memory store implements both interfaces, so a type
// assertion recovers the wider surface without changing the NewRegistry
// signature while the citation-ladder work is in flight. A nil or
// non-version store degrades gracefully: tools return a structured
// "version tracking unavailable" error and the hooks no-op.
// TODO(post-merge): rename the reviews field to a combined store interface.
func (r *Registry) versions() store.VersionRepository {
	v, _ := r.reviews.(store.VersionRepository)
	return v
}

// redtimeCtx is the identity version-store calls run as: tools and hooks are
// system callers (the request-level access checks already happened upstream).
func redtimeCtx() context.Context {
	return store.WithSystem(context.Background())
}

// ─── register_document_version ────────────────────────────────────────────────

func (r *Registry) registerDocumentVersionTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "register_document_version",
		Schema: providers.ToolParam{
			Name:        "register_document_version",
			Description: "Register a document file as a version in a negotiation lineage (Redtime), so successive rounds of the same contract can be tracked and diffed. Idempotent by content hash. Give lineage_id or parent_version_id to chain onto an existing lineage; omit both to start a new one. Returns the version and lineage IDs for use with get_redline_timeline.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":              map[string]interface{}{"type": "string", "description": "Document file (absolute, or relative to the document output directory); .docx text is extracted with insertions accepted"},
					"lineage_id":        map[string]interface{}{"type": "string", "description": "Optional: join this lineage (the latest version becomes the parent)"},
					"parent_version_id": map[string]interface{}{"type": "string", "description": "Optional: explicit parent version ID (wins over lineage_id)"},
					"source":            map[string]interface{}{"type": "string", "enum": []string{"ours", "theirs", "upload"}, "description": "Which side produced this version (default \"upload\")"},
					"author":            map[string]interface{}{"type": "string", "description": "Optional: who produced this version"},
				},
				"required": []string{"path"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			vrepo := r.versions()
			if vrepo == nil {
				return fail(redtime.ErrUnavailable.Error()), nil
			}
			abs, err := r.resolveDocxPath(strInput(input, "path"))
			if err != nil {
				return fail(err.Error()), nil
			}
			v, err := redtime.RegisterVersion(redtimeCtx(), vrepo, redtime.RegisterOpts{
				Path:      abs,
				LineageID: strInput(input, "lineage_id"),
				ParentID:  strInput(input, "parent_version_id"),
				Source:    strInput(input, "source"),
				Author:    strInput(input, "author"),
			})
			if err != nil {
				return fail(err.Error()), nil
			}
			return map[string]interface{}{"ok": true, "version": versionCard(v)}, nil
		},
	}
}

// versionCard renders a version for tool output — everything but the stored
// full text.
func versionCard(v *store.DocumentVersion) map[string]interface{} {
	return map[string]interface{}{
		"id":          v.ID,
		"lineageId":   v.LineageID,
		"parentId":    v.ParentID,
		"round":       v.Round,
		"source":      v.Source,
		"author":      v.Author,
		"createdAt":   v.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"path":        v.Path,
		"contentHash": v.ContentHash,
	}
}

// ─── get_redline_timeline ─────────────────────────────────────────────────────

func (r *Registry) getRedlineTimelineTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "get_redline_timeline",
		Schema: providers.ToolParam{
			Name:        "get_redline_timeline",
			Description: "Build the per-clause redline timeline of a document lineage (Redtime): how each clause's language evolved across negotiation rounds, who moved when (tracked-change authors, or the sending side for silent edits), which of our counters were accepted/rejected/silently modified, and how far the current draft sits from the firm's playbook position (drift). Identify the lineage by lineage_id, by the path of any registered version, or by a content hash.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"lineage_id":    map[string]interface{}{"type": "string", "description": "Lineage ID (or a version ID — resolved to its lineage)"},
					"path":          map[string]interface{}{"type": "string", "description": "Alternative: the path of any registered version"},
					"content_hash":  map[string]interface{}{"type": "string", "description": "Alternative: the content hash of any registered version"},
					"matter_number": map[string]interface{}{"type": "string", "description": "Matter number for playbook cascade scoping (drift)"},
					"client_number": map[string]interface{}{"type": "string", "description": "Client number for playbook cascade scoping (drift)"},
					"owner_id":      map[string]interface{}{"type": "string", "description": "Lawyer profile ID for personal-playbook cascade scoping (drift)"},
				},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			vrepo := r.versions()
			if vrepo == nil {
				return fail(redtime.ErrUnavailable.Error()), nil
			}
			sctx := redtimeCtx()
			lineageID, err := r.resolveLineageID(sctx, vrepo, input)
			if err != nil {
				return fail(err.Error()), nil
			}
			opts := redtime.OptsFromConfig(r.cfg, r.provReg, playbook.ResolveOpts{
				MatterNumber: strInput(input, "matter_number"),
				ClientID:     strInput(input, "client_number"),
				ProfileID:    strInput(input, "owner_id"),
			}, ctx.TaskID)
			tl, err := redtime.BuildTimeline(sctx, vrepo, lineageID, opts)
			if err != nil {
				return fail(err.Error()), nil
			}
			return map[string]interface{}{"ok": true, "timeline": tl}, nil
		},
	}
}

// resolveLineageID resolves the tool input to a lineage: a lineage ID (or a
// version ID, forgivingly), a registered version's path, or a content hash.
func (r *Registry) resolveLineageID(ctx context.Context, vrepo store.VersionRepository, input map[string]interface{}) (string, error) {
	if id := strings.TrimSpace(strInput(input, "lineage_id")); id != "" {
		// Accept a version ID where a lineage ID was expected.
		if vs, err := vrepo.ListLineage(ctx, id); err == nil && len(vs) > 0 {
			return id, nil
		}
		if v, found, err := vrepo.GetVersion(ctx, id); err == nil && found {
			return v.LineageID, nil
		}
		return id, nil // let BuildTimeline report not-found
	}
	if p := strings.TrimSpace(strInput(input, "path")); p != "" {
		abs, err := r.resolveDocxPath(p)
		if err != nil {
			return "", err
		}
		if v, found, err := vrepo.FindVersionByPath(ctx, abs); err == nil && found {
			return v.LineageID, nil
		}
		// The file may have been registered from another path — try its hash.
		if data, rerr := os.ReadFile(abs); rerr == nil {
			if v, found, err := vrepo.FindVersionByHash(ctx, redtime.HashBytes(data)); err == nil && found {
				return v.LineageID, nil
			}
		}
		return "", redtime.ErrNotFound
	}
	if h := strings.TrimSpace(strInput(input, "content_hash")); h != "" {
		if v, found, err := vrepo.FindVersionByHash(ctx, h); err == nil && found {
			return v.LineageID, nil
		}
		return "", redtime.ErrNotFound
	}
	return "", redtime.ErrNotFound
}

// ─── Hooks — versions accrue automatically ────────────────────────────────────

// findVersionByPathOrHash resolves an on-disk file to its registered version:
// by path first, then by content hash (the same file may have been registered
// from another path). Returns nil, false when the file is not in any lineage.
func findVersionByPathOrHash(ctx context.Context, vrepo store.VersionRepository, path string) (*store.DocumentVersion, bool) {
	if path == "" {
		return nil, false
	}
	if v, found, err := vrepo.FindVersionByPath(ctx, path); err == nil && found {
		return v, true
	}
	if data, err := os.ReadFile(path); err == nil {
		if v, found, herr := vrepo.FindVersionByHash(ctx, redtime.HashBytes(data)); herr == nil && found {
			return v, true
		}
	}
	return nil, false
}

// negotiationHistory is the judge-memory hook for respond_to_redline: when
// the prior version we sent (or the inbound document itself) belongs to a
// Redtime lineage, the lineage's stored decisions come back as per-clause
// negotiation history for the judge. Best-effort — no version store, no
// lineage, or a store failure returns nil and the judge runs amnesiac,
// exactly as before.
func (r *Registry) negotiationHistory(inboundPath, priorPath string) negotiate.History {
	vrepo := r.versions()
	if vrepo == nil {
		return nil
	}
	ctx := redtimeCtx()
	lineageID := ""
	if p := strings.TrimSpace(priorPath); p != "" {
		if abs, err := r.resolveDocxPath(p); err == nil {
			if v, found := findVersionByPathOrHash(ctx, vrepo, abs); found {
				lineageID = v.LineageID
			}
		}
	}
	if lineageID == "" {
		if v, found := findVersionByPathOrHash(ctx, vrepo, inboundPath); found {
			lineageID = v.LineageID
		}
	}
	if lineageID == "" {
		return nil
	}
	h, err := redtime.NegotiationHistory(ctx, vrepo, lineageID)
	if err != nil {
		slog.Warn("redtime: negotiation history unavailable — judging without memory",
			"lineage", lineageID, "error", err)
		return nil
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

// recordNegotiationVersions is the respond_to_redline hook: it registers the
// inbound opposing draft ("theirs", parented on the version we last sent when
// prior_version_path identifies one) and the produced response ("ours") with
// the decision summary attached. Best-effort — a failure is logged and the
// negotiation result stands; returns nil when version tracking is
// unavailable, else a small lineage card for the tool output.
func (r *Registry) recordNegotiationVersions(inboundPath, responsePath, priorPath, responseAuthor string, revs []ooxml.Revision, decisions []negotiate.Decision) interface{} {
	vrepo := r.versions()
	if vrepo == nil {
		return nil
	}
	ctx := redtimeCtx()

	// The version we last sent, when supplied, roots (or extends) the lineage.
	// RegisterVersion is idempotent, so an already-registered prior version
	// resolves to its existing lineage node.
	parentID := ""
	if p := strings.TrimSpace(priorPath); p != "" {
		if abs, err := r.resolveDocxPath(p); err == nil {
			if prior, rerr := redtime.RegisterVersion(ctx, vrepo, redtime.RegisterOpts{Path: abs, Source: redtime.SourceOurs}); rerr == nil {
				parentID = prior.ID
			} else {
				slog.Warn("redtime: could not register prior version", "path", abs, "error", rerr)
			}
		}
	}

	opposingAuthor := ""
	for _, rv := range revs {
		if rv.Author != "" {
			opposingAuthor = rv.Author
			break
		}
	}
	inbound, err := redtime.RegisterVersion(ctx, vrepo, redtime.RegisterOpts{
		Path: inboundPath, Source: redtime.SourceTheirs, Author: opposingAuthor, ParentID: parentID,
	})
	if err != nil {
		slog.Warn("redtime: could not register inbound version", "path", inboundPath, "error", err)
		return nil
	}
	var attach interface{}
	if len(decisions) > 0 {
		attach = decisions
	}
	response, err := redtime.RegisterVersion(ctx, vrepo, redtime.RegisterOpts{
		Path: responsePath, Source: redtime.SourceOurs, Author: responseAuthor,
		ParentID: inbound.ID, Decisions: attach,
	})
	if err != nil {
		slog.Warn("redtime: could not register response version", "path", responsePath, "error", err)
		return map[string]interface{}{"lineageId": inbound.LineageID, "inboundVersionId": inbound.ID}
	}
	return map[string]interface{}{
		"lineageId":         inbound.LineageID,
		"inboundVersionId":  inbound.ID,
		"responseVersionId": response.ID,
		"rounds":            response.Round,
	}
}

// recordEditVersion is the edit_document hook: when the source document is
// already part of a lineage (by path, or by content hash for a file
// registered under another path), the redlined output accrues as the next
// "ours" version. One-off edits of untracked documents don't force-create
// lineages. Best-effort; nil when nothing was recorded.
func (r *Registry) recordEditVersion(srcPath, outputPath, author string) interface{} {
	vrepo := r.versions()
	if vrepo == nil {
		return nil
	}
	ctx := redtimeCtx()
	parent, found := findVersionByPathOrHash(ctx, vrepo, srcPath)
	if !found {
		return nil
	}
	v, err := redtime.RegisterVersion(ctx, vrepo, redtime.RegisterOpts{
		Path: outputPath, Source: redtime.SourceOurs, Author: author, ParentID: parent.ID,
	})
	if err != nil {
		slog.Warn("redtime: could not register edited version", "path", outputPath, "error", err)
		return nil
	}
	return map[string]interface{}{"lineageId": v.LineageID, "versionId": v.ID, "round": v.Round}
}
