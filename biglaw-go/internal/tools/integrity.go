// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// check_document_integrity: can you trust this inbound document? Runs the
// Unicode obfuscation scan (homoglyphs, invisible characters, bidi controls,
// mixed-script words) over a .docx in the output directory or a knowledge-
// store document, and — given the version we last sent — the unmarked-change
// detector, which flags every edit NOT accounted for by the received
// document's tracked changes. Shared helpers here also serve the integrity
// wiring inside respond_to_redline (negotiate.go).

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/integrity"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

func (r *Registry) registerIntegrityTools() {
	r.Register(r.checkDocumentIntegrityTool())
}

// ─── check_document_integrity ─────────────────────────────────────────────────

func (r *Registry) checkDocumentIntegrityTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "check_document_integrity",
		Schema: providers.ToolParam{
			Name:        "check_document_integrity",
			Description: "Check whether an inbound document can be trusted. Scans for Unicode obfuscation — homoglyph substitutions (Cyrillic/Greek/fullwidth lookalikes in Latin words), zero-width and invisible characters, bidirectional control characters, and mixed-script words. Given prior_version_path (the version we last sent), also detects UNMARKED changes: edits in the received document that are not accounted for by its tracked changes. Returns findings, unmarked-change hunks, a clean verdict, and a one-line summary.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":               map[string]interface{}{"type": "string", "description": "A .docx to check (absolute, or relative to the document output directory). Exactly one of path or document_id is required."},
					"document_id":        map[string]interface{}{"type": "string", "description": "A knowledge-store document ID to scan instead of a file. Obfuscation scan only."},
					"prior_version_path": map[string]interface{}{"type": "string", "description": "The version we last sent (.docx or .txt), for unmarked-change detection. Only valid together with path."},
				},
			},
		},
		Exec: func(input map[string]interface{}, ctx agents.ToolContext) (interface{}, error) {
			path := strings.TrimSpace(strInput(input, "path"))
			docID := strings.TrimSpace(strInput(input, "document_id"))
			prior := strings.TrimSpace(strInput(input, "prior_version_path"))
			if (path == "") == (docID == "") {
				return fail("exactly one of path or document_id is required"), nil
			}
			if docID != "" && prior != "" {
				return fail("prior_version_path is only valid together with path"), nil
			}

			var findings []integrity.Finding
			var report *integrity.UnmarkedReport
			switch {
			case path != "":
				src, err := r.resolveDocxPath(path)
				if err != nil {
					return fail(err.Error()), nil
				}
				if !strings.EqualFold(filepath.Ext(src), ".docx") {
					return fail("only .docx files can be checked by path"), nil
				}
				doc, err := ooxml.OpenFile(src)
				if err != nil {
					return fail(fmt.Sprintf("cannot open document: %v", err)), nil
				}
				findings = integrity.ScanText(doc.Text())
				if prior != "" {
					sentText, perr := r.readPriorVersion(prior)
					if perr != nil {
						return fail(perr.Error()), nil
					}
					rep := integrity.CompareVersions(sentText, doc)
					report = &rep
				}
			default: // document_id
				if ctx.KnowledgeStore == nil {
					return fail("knowledge store unavailable"), nil
				}
				text, err := ctx.KnowledgeStore.GetFullText(docID)
				if err != nil {
					return fail(fmt.Sprintf("cannot read document: %v", err)), nil
				}
				if text == "" {
					return fail(fmt.Sprintf("document not found: %s", docID)), nil
				}
				findings = integrity.ScanText(text)
			}

			clean := integrityClean(findings, report)
			out := map[string]interface{}{
				"ok":       true,
				"findings": findings,
				"clean":    clean,
				"summary":  integritySummary(findings, report),
			}
			if report != nil {
				out["unmarkedChanges"] = map[string]interface{}{
					"hunks": report.Hunks,
					"count": report.Count,
				}
			}
			return out, nil
		},
	}
}

// ─── Shared integrity helpers (also used by respond_to_redline) ──────────────

// readPriorVersion loads the text of the version we last sent: a .docx (text
// with insertions accepted) or a plain-text .txt, path-safety enforced via
// the same output-root containment as every other document tool.
func (r *Registry) readPriorVersion(p string) (string, error) {
	abs, err := r.resolveDocxPath(p)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(filepath.Ext(abs)) {
	case ".docx":
		doc, err := ooxml.OpenFile(abs)
		if err != nil {
			return "", fmt.Errorf("cannot open prior version: %v", err)
		}
		return doc.Text(), nil
	case ".txt":
		data, err := os.ReadFile(abs)
		if err != nil {
			return "", fmt.Errorf("cannot read prior version: %v", err)
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("prior_version_path must be a .docx or .txt file")
	}
}

// integrityClean is the overall verdict: no obfuscation findings at warning
// severity or above, and no unmarked changes (when the check ran).
func integrityClean(findings []integrity.Finding, report *integrity.UnmarkedReport) bool {
	return integrity.Clean(findings) && (report == nil || report.Count == 0)
}

// integritySummary renders the whole result as one short human sentence.
func integritySummary(findings []integrity.Finding, report *integrity.UnmarkedReport) string {
	parts := []string{integrity.Summarize(findings)}
	switch {
	case report == nil:
		parts = append(parts, "unmarked-change check not run (no prior version supplied)")
	case report.Count == 0:
		parts = append(parts, "no unmarked changes — every edit is accounted for by tracked changes")
	default:
		plural := "s"
		if report.Count == 1 {
			plural = ""
		}
		parts = append(parts, fmt.Sprintf("%d unmarked change%s NOT accounted for by tracked changes", report.Count, plural))
	}
	summary := strings.Join(parts, "; ") + "."
	if integrityClean(findings, report) {
		return "Document is clean: " + summary
	}
	return "Integrity issues found: " + summary
}
