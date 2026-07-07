// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Clio tool definitions — 7 tools exposing Clio practice management to agents.
// All tools follow the connector pattern: return a result object or
// {"error": ...} — never an error that aborts the agentic loop.

package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/integrations"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

// clioDownloadTextCap mirrors the TS clio_download_document 50k-char cap.
const clioDownloadTextCap = 50_000

// registerClioTools adds the Clio practice-management tools.
func (r *Registry) registerClioTools() {
	r.Register(clioListMattersTool())
	r.Register(clioGetMatterTool())
	r.Register(clioListDocumentsTool())
	r.Register(clioDownloadDocumentTool())
	r.Register(clioCreateActivityTool())
	r.Register(clioCreateNoteTool())
	r.Register(clioListContactsTool())
}

// clioGuard returns a structured "not configured" object when the Clio OAuth
// app is absent, so the tools are always safe to list in allowedTools.
func clioGuard() (interface{}, bool) {
	if !integrations.DefaultClioClient.IsConfigured() {
		return map[string]interface{}{
			"error": "not configured: CLIO_CLIENT_ID is not set",
		}, false
	}
	return nil, true
}

// clioResult flattens a client call into the never-throw tool contract.
func clioResult(result interface{}, err error) (interface{}, error) {
	if err != nil {
		return map[string]interface{}{"error": err.Error()}, nil
	}
	return result, nil
}

// clioFloatInput reads a float input (JSON numbers decode as float64).
func clioFloatInput(input map[string]interface{}, key string, def float64) float64 {
	switch v := input[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return def
}

// ─── clio_list_matters ────────────────────────────────────────────────────────

func clioListMattersTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_list_matters",
		Schema: providers.ToolParam{
			Name:        "clio_list_matters",
			Description: "List open matters from Clio. Returns id, display_number, description, status, client name, practice area.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{"type": "string", "description": "Filter by status: open (default), pending, closed, or all"},
					"limit":  map[string]interface{}{"type": "number", "description": "Maximum results (default 50, max 200)"},
					"page":   map[string]interface{}{"type": "number", "description": "Page number (default 1)"},
				},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			status := strInput(input, "status")
			if status == "" {
				status = "open"
			}
			limit := intInput(input, "limit", 50)
			if limit > 200 {
				limit = 200
			}
			return clioResult(integrations.DefaultClioClient.ListMatters(status, limit, intInput(input, "page", 1)))
		},
	}
}

// ─── clio_get_matter ──────────────────────────────────────────────────────────

func clioGetMatterTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_get_matter",
		Schema: providers.ToolParam{
			Name:        "clio_get_matter",
			Description: "Get full details of a Clio matter by ID, including client, practice area, responsible attorney, and custom fields.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"matter_id": map[string]interface{}{"type": "number", "description": "Clio matter ID"},
				},
				"required": []string{"matter_id"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			return clioResult(integrations.DefaultClioClient.GetMatter(intInput(input, "matter_id", 0)))
		},
	}
}

// ─── clio_list_documents ──────────────────────────────────────────────────────

func clioListDocumentsTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_list_documents",
		Schema: providers.ToolParam{
			Name:        "clio_list_documents",
			Description: "List documents attached to a Clio matter.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"matter_id": map[string]interface{}{"type": "number", "description": "Clio matter ID"},
					"limit":     map[string]interface{}{"type": "number", "description": "Maximum results (default 50)"},
				},
				"required": []string{"matter_id"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			return clioResult(integrations.DefaultClioClient.ListDocuments(intInput(input, "matter_id", 0), intInput(input, "limit", 50)))
		},
	}
}

// ─── clio_download_document ───────────────────────────────────────────────────

func clioDownloadDocumentTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_download_document",
		Schema: providers.ToolParam{
			Name:        "clio_download_document",
			Description: "Download a Clio document and return its text content for analysis. Plain-text formats only (PDF/DOCX extraction is not available in the Go backend).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"document_id": map[string]interface{}{"type": "number", "description": "Clio document ID"},
					"filename":    map[string]interface{}{"type": "string", "description": "Filename including extension (e.g. memo.txt) — used to determine extraction method"},
				},
				"required": []string{"document_id", "filename"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			buf, err := integrations.DefaultClioClient.DownloadDocument(intInput(input, "document_id", 0))
			if err != nil {
				return map[string]interface{}{"error": err.Error()}, nil
			}
			text, ok := clioExtractText(buf, strInput(input, "filename"))
			if !ok {
				return map[string]interface{}{
					"error": fmt.Sprintf("text extraction for %q is not available in the Go backend — only plain-text documents are supported", strInput(input, "filename")),
				}, nil
			}
			truncated := len(text) > clioDownloadTextCap
			if truncated {
				text = text[:clioDownloadTextCap]
			}
			return map[string]interface{}{"text": text, "truncated": truncated}, nil
		},
	}
}

// ─── clio_create_activity ─────────────────────────────────────────────────────

func clioCreateActivityTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_create_activity",
		Schema: providers.ToolParam{
			Name:        "clio_create_activity",
			Description: "Create a time entry (activity) on a Clio matter. Use this to log billable time from Big Michael back to Clio.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"matter_id":      map[string]interface{}{"type": "number", "description": "Clio matter ID"},
					"description":    map[string]interface{}{"type": "string", "description": "Description of the work performed"},
					"date":           map[string]interface{}{"type": "string", "description": "ISO date (YYYY-MM-DD)"},
					"duration_hours": map[string]interface{}{"type": "number", "description": "Duration in hours (e.g. 0.5 for 30 min)"},
				},
				"required": []string{"matter_id", "description", "date", "duration_hours"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			return clioResult(integrations.DefaultClioClient.CreateActivity(
				intInput(input, "matter_id", 0),
				strInput(input, "description"),
				strInput(input, "date"),
				clioFloatInput(input, "duration_hours", 0),
			))
		},
	}
}

// ─── clio_create_note ─────────────────────────────────────────────────────────

func clioCreateNoteTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_create_note",
		Schema: providers.ToolParam{
			Name:        "clio_create_note",
			Description: "Post a note to a Clio matter. Use this to save synthesis output, findings, or research memos back into the client file.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"matter_id": map[string]interface{}{"type": "number", "description": "Clio matter ID"},
					"subject":   map[string]interface{}{"type": "string", "description": "Note subject"},
					"content":   map[string]interface{}{"type": "string", "description": "Note body"},
				},
				"required": []string{"matter_id", "subject", "content"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			return clioResult(integrations.DefaultClioClient.CreateNote(
				intInput(input, "matter_id", 0),
				strInput(input, "subject"),
				strInput(input, "content"),
			))
		},
	}
}

// ─── clio_list_contacts ───────────────────────────────────────────────────────

func clioListContactsTool() *ToolImpl {
	return &ToolImpl{
		Name: "clio_list_contacts",
		Schema: providers.ToolParam{
			Name:        "clio_list_contacts",
			Description: "List contacts from Clio (clients, companies, people).",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":  map[string]interface{}{"type": "string", "description": "Filter by type: Person or Company"},
					"limit": map[string]interface{}{"type": "number", "description": "Maximum results (default 50)"},
				},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			if errObj, ok := clioGuard(); !ok {
				return errObj, nil
			}
			return clioResult(integrations.DefaultClioClient.ListContacts(strInput(input, "type"), intInput(input, "limit", 50)))
		},
	}
}

// ─── text extraction ──────────────────────────────────────────────────────────

// clioTextExts mirrors the plain-text extension list of the upload handler
// (internal/api/content.go) — the Go backend has no PDF/DOCX pipeline.
var clioTextExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true,
	".json": true, ".log": true, ".text": true, ".rtf": true,
}

// clioExtractText returns the document text when the payload is plain text
// (by extension, or any valid NUL-free UTF-8 body). Binary formats return
// ok=false — the TS extractWritingSamples PDF/DOCX path has no Go equivalent.
func clioExtractText(buf []byte, filename string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(filename))
	if clioTextExts[ext] {
		return string(buf), true
	}
	if utf8.Valid(buf) && !strings.ContainsRune(string(buf), 0) {
		return string(buf), true
	}
	return "", false
}
