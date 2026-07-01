// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Document production tools: docx_generate builds a Word document from
// structured content; replicate_document clones an existing .docx so the
// copies can be adapted as templates. Both write only inside the configured
// document output directory (cfg.PDF.OutputDir) — caller-supplied directories
// are ignored and traversal outside the root is rejected.

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

func (r *Registry) registerDocxTools() {
	r.Register(r.docxGenerateTool())
	r.Register(r.replicateDocumentTool())
}

// ─── Shared input helpers ─────────────────────────────────────────────────────

func boolInput(input map[string]interface{}, key string) bool {
	v, _ := input[key].(bool)
	return v
}

// strSlice coerces a JSON array input value into a []string.
func strSlice(v interface{}) []string {
	items, _ := v.([]interface{})
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, _ := it.(string)
		out = append(out, s)
	}
	return out
}

// truncateUTF8 caps s at max bytes without splitting a multi-byte rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// ─── Path safety ──────────────────────────────────────────────────────────────
// The document tools accept caller-supplied names and paths. An agent induced
// via prompt injection must not be able to write or read outside the document
// output root, so every name is slugged to a single path component and every
// resolved path is verified to stay inside the root.

// docxOutputRoot returns the absolute output directory, creating it if
// missing.
func (r *Registry) docxOutputRoot() (string, error) {
	root, err := filepath.Abs(r.cfg.PDF.OutputDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

// insideRoot reports whether abs is root itself or a descendant of it.
func insideRoot(abs, root string) bool {
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

// resolveDocxPath resolves a caller-supplied path against the output root
// (absolute paths are taken as-is) and rejects any result that escapes it.
func (r *Registry) resolveDocxPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("a file path is required")
	}
	root, err := r.docxOutputRoot()
	if err != nil {
		return "", err
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs, err = filepath.Abs(filepath.Clean(abs))
	if err != nil {
		return "", err
	}
	if !insideRoot(abs, root) {
		return "", fmt.Errorf("refusing '%s': path is outside the document output directory", p)
	}
	return abs, nil
}

// slugStem reduces a caller-supplied name to a safe single-component filename
// stem: any .docx extension is stripped, every rune outside [A-Za-z0-9_-]
// becomes a dash, and dashes are collapsed. Empty input yields "document".
func slugStem(name string) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(filepath.Ext(name), ".docx") {
		name = name[:len(name)-len(".docx")]
	}
	mapped := strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			return c
		default:
			return '-'
		}
	}, name)
	var b strings.Builder
	dash := false
	for _, c := range mapped {
		if c == '-' {
			if dash || b.Len() == 0 {
				continue
			}
			dash = true
		} else {
			dash = false
		}
		b.WriteRune(c)
	}
	stem := strings.TrimRight(b.String(), "-")
	if stem == "" {
		return "document"
	}
	return stem
}

// ─── docx_generate ────────────────────────────────────────────────────────────

func (r *Registry) docxGenerateTool() *ToolImpl {
	return &ToolImpl{
		Name: "docx_generate",
		Schema: providers.ToolParam{
			Name:        "docx_generate",
			Description: "Generate a Word (.docx) legal document from structured content (headings, prose, bullet lists, tables). Supports landscape orientation and page breaks. Returns the output file path.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":     map[string]interface{}{"type": "string", "description": "Document title; rendered as the top heading and used as the default filename"},
					"filename":  map[string]interface{}{"type": "string", "description": "Optional output filename (extension forced to .docx); defaults to a slug of the title"},
					"landscape": map[string]interface{}{"type": "boolean", "description": "Landscape orientation when true"},
					"sections": map[string]interface{}{
						"type":        "array",
						"description": "Document sections, rendered in order",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"heading":   map[string]interface{}{"type": "string", "description": "Optional section heading"},
								"level":     map[string]interface{}{"type": "integer", "description": "Heading level, clamped to 1-3 (default 2)"},
								"content":   map[string]interface{}{"type": "string", "description": "Prose; blank lines separate paragraphs; lines starting with '- ' render as bullets"},
								"pageBreak": map[string]interface{}{"type": "boolean", "description": "Start this section on a new page"},
								"table": map[string]interface{}{
									"type":        "object",
									"description": "Optional table for this section",
									"properties": map[string]interface{}{
										"headers": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
										"rows":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}}},
									},
									"required": []string{"headers", "rows"},
								},
							},
						},
					},
				},
				"required": []string{"title", "sections"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			title := strings.TrimSpace(strInput(input, "title"))
			if title == "" {
				title = "Legal Document"
			}
			name := strInput(input, "filename")
			if strings.TrimSpace(name) == "" {
				name = title
			}
			filename := slugStem(name) + ".docx"

			outputPath, err := r.resolveDocxPath(filename)
			if err != nil {
				return nil, err
			}

			landscape := boolInput(input, "landscape")
			b := ooxml.NewBuilder()
			b.SetLandscape(landscape)
			b.Heading(1, title)

			sections, _ := input["sections"].([]interface{})
			sectionCount := 0
			for _, raw := range sections {
				sec, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				sectionCount++
				if boolInput(sec, "pageBreak") {
					b.PageBreak()
				}
				if heading := strings.TrimSpace(strInput(sec, "heading")); heading != "" {
					b.Heading(clampLevel(intInput(sec, "level", 2)), heading)
				}
				writeProse(b, strInput(sec, "content"))
				if tbl, ok := sec["table"].(map[string]interface{}); ok {
					headers := strSlice(tbl["headers"])
					if len(headers) > 0 { // a table with no headers is skipped
						rowsRaw, _ := tbl["rows"].([]interface{})
						rows := make([][]string, 0, len(rowsRaw))
						for _, rr := range rowsRaw {
							rows = append(rows, strSlice(rr))
						}
						b.Table(headers, rows)
						b.Spacer()
					}
				}
			}

			data, err := b.Bytes()
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(outputPath, data, 0o644); err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"outputPath":    outputPath,
				"filename":      filename,
				"sectionCount":  sectionCount,
				"landscape":     landscape,
				"fileSizeBytes": len(data),
			}, nil
		},
	}
}

func clampLevel(level int) int {
	if level < 1 {
		return 1
	}
	if level > 3 {
		return 3
	}
	return level
}

// writeProse renders section content: blank lines separate paragraphs and
// lines beginning with "- " render as bullet items.
func writeProse(b *ooxml.Builder, content string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	var para []string
	flush := func() {
		if len(para) > 0 {
			b.Paragraph(strings.Join(para, " "))
			para = para[:0]
		}
	}
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case strings.HasPrefix(trimmed, "- "):
			flush()
			b.Bullet(strings.TrimSpace(trimmed[2:]))
		default:
			para = append(para, trimmed)
		}
	}
	flush()
}

// ─── replicate_document ───────────────────────────────────────────────────────

func (r *Registry) replicateDocumentTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "replicate_document",
		Schema: providers.ToolParam{
			Name:        "replicate_document",
			Description: "Make byte-for-byte copies of an existing .docx as new files, so the copies can be adapted as templates without touching the original. Returns the new paths, ready to pass to edit_document.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":         map[string]interface{}{"type": "string", "description": "Source .docx (absolute, or relative to the document output directory)"},
					"count":        map[string]interface{}{"type": "integer", "description": "Number of copies (default 1, max 20)"},
					"new_filename": map[string]interface{}{"type": "string", "description": "Optional base name for the copies (extension forced to .docx)"},
				},
				"required": []string{"path"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			src, err := r.resolveDocxPath(strInput(input, "path"))
			if err != nil {
				return fail(err.Error()), nil
			}
			if !strings.EqualFold(filepath.Ext(src), ".docx") {
				return fail("only .docx files can be replicated"), nil
			}
			data, err := os.ReadFile(src)
			if err != nil {
				return fail(fmt.Sprintf("cannot read source: %v", err)), nil
			}

			count := intInput(input, "count", 1)
			if count < 1 {
				count = 1
			}
			if count > 20 {
				count = 20
			}

			stem := slugStem(strInput(input, "new_filename"))
			if strInput(input, "new_filename") == "" {
				stem = slugStem(filepath.Base(src)) + "-copy"
			}

			dir := filepath.Dir(src)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fail(err.Error()), nil
			}
			copies := make([]map[string]interface{}, 0, count)
			for i := 1; i <= count; i++ {
				name := stem + ".docx"
				if count > 1 {
					name = fmt.Sprintf("%s (%d).docx", stem, i)
				}
				dst := filepath.Join(dir, name)
				// Never overwrite the source or an existing file.
				for n := 2; dst == src || fileExists(dst); n++ {
					name = fmt.Sprintf("%s (%d).docx", stem, count*n+i)
					dst = filepath.Join(dir, name)
				}
				if err := os.WriteFile(dst, data, 0o644); err != nil {
					return fail(err.Error()), nil
				}
				copies = append(copies, map[string]interface{}{"path": dst, "filename": name})
			}
			return map[string]interface{}{"ok": true, "count": len(copies), "copies": copies}, nil
		},
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
