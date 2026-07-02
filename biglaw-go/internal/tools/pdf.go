// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// PDF tools — wrap scripts/pdf_tools.py via subprocess, ported from
// src/tools/pdf.ts. Protocol: python3 pdf_tools.py <operation> '<json_args>',
// single JSON object on stdout (errors exit 1 with JSON error on stdout).
//
//	pdf_extract_text   — PyMuPDF: text + block structure from any PDF
//	pdf_extract_tables — Camelot: table extraction (lattice → stream fallback)
//	pdf_ocr            — Tesseract 5 OCR of scanned PDFs / images
//	pdf_generate       — PyMuPDF Story: paginated legal PDFs from markdown
//	                     strings or structured section arrays
//
// A missing python3 or script returns a structured {"error": …} map — never a
// hard failure — so the tools are always safe to register in allowedTools.

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// ─── Subprocess limits ────────────────────────────────────────────────────────

const (
	pythonTimeout  = 30 * time.Second
	maxStdoutBytes = 50 * 1024 * 1024 // 50 MB
)

var pdfPagesRe = regexp.MustCompile(`^(\d+(-\d+)?|all)$`)
var ocrLangRe = regexp.MustCompile(`^[a-z]{2,8}(\+[a-z]{2,8})*$`)

// ─── Script path resolution ───────────────────────────────────────────────────

// pdfToolsScript resolves scripts/pdf_tools.py relative to the working
// directory (the binary runs from the repo root), with a PDF_TOOLS_SCRIPT
// env override for non-standard layouts.
func pdfToolsScript() string {
	if p := os.Getenv("PDF_TOOLS_SCRIPT"); p != "" {
		return p
	}
	abs, err := filepath.Abs(filepath.Join("scripts", "pdf_tools.py"))
	if err != nil {
		return filepath.Join("scripts", "pdf_tools.py")
	}
	return abs
}

// ─── Path safety ──────────────────────────────────────────────────────────────
// The read tools accept a caller-supplied file path. Without a guard, an agent
// induced via prompt injection could read arbitrary files (.env, key material,
// system files) and surface their contents in findings. Restrict reads to an
// allow-list of base directories. Ported from src/tools/pdf.ts.

// safeReadPath validates a caller-supplied path against the allowed roots
// (PDF_ALLOWED_DIRS / cfg.PDF.AllowedDirs; default: cwd, tmp, output dir).
// Within the default roots it also denies dotfiles, the vector data dir and
// .git — those hold secrets a prompt-injected agent must not surface.
func (r *Registry) safeReadPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("a file path is required")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}

	allowed := r.cfg.PDF.AllowedDirs
	if len(allowed) == 0 {
		if env := os.Getenv("PDF_ALLOWED_DIRS"); env != "" {
			for _, d := range strings.Split(env, ",") {
				if d = strings.TrimSpace(d); d != "" {
					allowed = append(allowed, d)
				}
			}
		}
	}
	explicit := len(allowed) > 0
	if !explicit {
		cwd, _ := os.Getwd()
		allowed = []string{cwd, os.TempDir(), r.cfg.PDF.OutputDir}
	}

	inRoot := func(root string) bool {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			return false
		}
		return abs == rootAbs || strings.HasPrefix(abs, rootAbs+string(filepath.Separator))
	}
	ok := false
	roots := make([]string, 0, len(allowed))
	for _, root := range allowed {
		rootAbs, _ := filepath.Abs(root)
		roots = append(roots, rootAbs)
		if inRoot(root) {
			ok = true
		}
	}
	if !ok {
		slog.Warn("Blocked PDF tool read outside allowed roots", "requested", p)
		return "", fmt.Errorf("refusing to read '%s': path is outside the allowed directories (%s). Set PDF_ALLOWED_DIRS to widen",
			p, strings.Join(roots, ", "))
	}

	// Block dotfiles (.env, .clients.json, …) and the data/.git directories
	// regardless of the default allow-list, unless an operator explicitly
	// widened the roots.
	if !explicit {
		cwd, _ := os.Getwd()
		denied := strings.HasPrefix(filepath.Base(abs), ".")
		for _, root := range []string{r.cfg.VectorDB.DataDir, filepath.Join(cwd, ".git")} {
			if root == "" {
				continue
			}
			if inRoot := func() bool {
				ra, err := filepath.Abs(root)
				if err != nil {
					return false
				}
				return abs == ra || strings.HasPrefix(abs, ra+string(filepath.Separator))
			}(); inRoot {
				denied = true
			}
		}
		if denied {
			slog.Warn("Blocked PDF tool read of a sensitive path", "requested", p)
			return "", fmt.Errorf("refusing to read '%s': sensitive (dotfile or data) path", p)
		}
	}
	return abs, nil
}

// ─── Shared python runner ─────────────────────────────────────────────────────

// runPDFPython executes pdf_tools.py with the operation + JSON args. Missing
// interpreter/script and timeouts are returned as structured error maps so
// agent loops degrade gracefully.
func (r *Registry) runPDFPython(operation string, args map[string]interface{}) (interface{}, error) {
	script := pdfToolsScript()
	if _, err := os.Stat(script); err != nil {
		return map[string]interface{}{
			"error": fmt.Sprintf("pdf_tools.py not found at %s — set PDF_TOOLS_SCRIPT or run from the repo root", script),
		}, nil
	}
	python := r.cfg.PDF.PythonBin
	if python == "" {
		python = "python3"
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), pythonTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, script, operation, string(argsJSON))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return map[string]interface{}{
			"error": fmt.Sprintf("pdf_tools.py timed out after %ds", int(pythonTimeout.Seconds())),
		}, nil
	}
	if stdout.Len() > maxStdoutBytes {
		return map[string]interface{}{"error": "pdf_tools.py stdout exceeded maximum size"}, nil
	}

	var result interface{}
	if jsonErr := json.Unmarshal(stdout.Bytes(), &result); jsonErr == nil && result != nil {
		// The script writes error JSON to stdout even on non-zero exit.
		return result, nil
	}
	if runErr != nil {
		errText := strings.TrimSpace(stderr.String())
		if len(errText) > 200 {
			errText = strutil.Truncate(errText, 200)
		}
		if _, isExit := runErr.(*exec.ExitError); !isExit {
			return map[string]interface{}{
				"error": fmt.Sprintf("failed to spawn %s: %s. Is Python 3 installed?", python, runErr),
			}, nil
		}
		slog.Error("pdf_tools.py exited with error", "operation", operation, "stderr", errText)
		return map[string]interface{}{
			"error": fmt.Sprintf("pdf_tools.py failed: %s", errText),
		}, nil
	}
	out := stdout.String()
	if len(out) > 200 {
		out = strutil.Truncate(out, 200)
	}
	return map[string]interface{}{
		"error": fmt.Sprintf("failed to parse pdf_tools.py output: %s", out),
	}, nil
}

// ─── register ─────────────────────────────────────────────────────────────────

func (r *Registry) registerPdfTools() {
	r.Register(r.pdfExtractTextTool())
	r.Register(r.pdfExtractTablesTool())
	r.Register(r.pdfOcrTool())
	r.Register(r.pdfGenerateTool())
}

// ─── pdf_extract_text ─────────────────────────────────────────────────────────

func (r *Registry) pdfExtractTextTool() *ToolImpl {
	return &ToolImpl{
		Name: "pdf_extract_text",
		Schema: providers.ToolParam{
			Name: "pdf_extract_text",
			Description: "Extract full text and block structure from a PDF file using PyMuPDF. " +
				"Returns page-by-page text and bounding-box blocks for layout analysis.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":  map[string]interface{}{"type": "string", "description": "Absolute or relative path to the PDF file"},
					"pages": map[string]interface{}{"type": "string", "description": "Optional page range to extract, e.g. '1-5' or '3'. Defaults to all pages."},
				},
				"required": []string{"path"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			pages := strInput(input, "pages")
			if pages != "" && !pdfPagesRe.MatchString(pages) {
				return nil, fmt.Errorf("invalid pages format: %s. Use format like '1', '1-5', or 'all'", pages)
			}
			path, err := r.safeReadPath(strInput(input, "path"))
			if err != nil {
				return nil, err
			}
			args := map[string]interface{}{"path": path}
			if pages != "" {
				args["pages"] = pages
			}
			return r.runPDFPython("extract_text", args)
		},
	}
}

// ─── pdf_extract_tables ───────────────────────────────────────────────────────

func (r *Registry) pdfExtractTablesTool() *ToolImpl {
	return &ToolImpl{
		Name: "pdf_extract_tables",
		Schema: providers.ToolParam{
			Name: "pdf_extract_tables",
			Description: "Extract tables from a PDF using Camelot. Returns each table as an array of rows " +
				"with headers and data cells. Automatically falls back from lattice to stream mode " +
				"if no bordered tables are found.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":  map[string]interface{}{"type": "string", "description": "Absolute or relative path to the PDF file"},
					"pages": map[string]interface{}{"type": "string", "description": "Pages to scan, e.g. 'all', '1', '1-3'. Defaults to 'all'."},
					"flavor": map[string]interface{}{
						"type": "string",
						"enum": []string{"lattice", "stream"},
						"description": "lattice (default): bordered tables with ruled lines. " +
							"stream: whitespace-separated columns (no visible borders).",
					},
				},
				"required": []string{"path"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			pages := strInput(input, "pages")
			if pages == "" {
				pages = "all"
			}
			if pages != "all" && !pdfPagesRe.MatchString(pages) {
				return nil, fmt.Errorf("invalid pages format: %s. Use format like '1', '1-5', or 'all'", pages)
			}
			flavor := strInput(input, "flavor")
			if flavor != "lattice" && flavor != "stream" {
				flavor = "lattice"
			}
			path, err := r.safeReadPath(strInput(input, "path"))
			if err != nil {
				return nil, err
			}
			return r.runPDFPython("extract_tables", map[string]interface{}{
				"path":   path,
				"pages":  pages,
				"flavor": flavor,
			})
		},
	}
}

// ─── pdf_ocr ──────────────────────────────────────────────────────────────────

func (r *Registry) pdfOcrTool() *ToolImpl {
	return &ToolImpl{
		Name: "pdf_ocr",
		Schema: providers.ToolParam{
			Name: "pdf_ocr",
			Description: "OCR a scanned PDF or image file using Tesseract 5. " +
				"PDF pages are rasterised at 300 DPI via PyMuPDF before OCR so quality " +
				"is consistent for any scan resolution. Returns full text and per-page breakdown. " +
				"Useful for scanned regulatory filings, court documents, and client contracts " +
				"that contain no embedded selectable text.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Absolute path to the PDF or image file (PNG/JPEG/TIFF)"},
					"lang": map[string]interface{}{
						"type": "string",
						"description": "Tesseract language code(s). Default 'eng'. " +
							"Multi-language: 'eng+fra', 'eng+deu', etc. " +
							"Available: eng, fra, deu, ita, spa, nld, por",
					},
					"pages": map[string]interface{}{"type": "string", "description": "Page range for PDFs, e.g. '1-3' or '2'. Defaults to all pages."},
					"dpi":   map[string]interface{}{"type": "number", "description": "Rasterisation DPI for PDF pages. Default 300. Higher = better OCR, slower."},
				},
				"required": []string{"path"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			// Tesseract lang codes are lowercase alpha + optional "+"-joined
			// codes; reject anything else before it reaches the binary.
			lang := strInput(input, "lang")
			if lang == "" || !ocrLangRe.MatchString(lang) {
				lang = "eng"
			}
			pages := strInput(input, "pages")
			if pages != "" && !pdfPagesRe.MatchString(pages) {
				return nil, fmt.Errorf("invalid pages format: %s. Use format like '1', '1-5', or 'all'", pages)
			}
			dpi := intInput(input, "dpi", 300)
			if dpi < 72 {
				dpi = 72
			}
			if dpi > 600 {
				dpi = 600
			}
			path, err := r.safeReadPath(strInput(input, "path"))
			if err != nil {
				return nil, err
			}
			args := map[string]interface{}{"path": path, "lang": lang, "dpi": dpi}
			if pages != "" {
				args["pages"] = pages
			}
			return r.runPDFPython("ocr", args)
		},
	}
}

// ─── pdf_generate ─────────────────────────────────────────────────────────────

func (r *Registry) pdfGenerateTool() *ToolImpl {
	return &ToolImpl{
		Name: "pdf_generate",
		Schema: providers.ToolParam{
			Name: "pdf_generate",
			Description: "Generate a properly formatted legal PDF document using PyMuPDF. " +
				"Accepts either a markdown string or a structured sections array. " +
				"Handles automatic pagination, justified text, headings, and bullet lists. " +
				"Returns the output file path and page count.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":    map[string]interface{}{"type": "string", "description": "Document title (rendered as H1 on first page)"},
					"filename": map[string]interface{}{"type": "string", "description": "Output filename, e.g. 'competition-brief-2026.pdf'"},
					"content": map[string]interface{}{
						"description": "Document body as a markdown string, " +
							"or a structured array: [{heading, content, subsections?}]",
					},
					"author":       map[string]interface{}{"type": "string", "description": "Optional author / firm name for the metadata line"},
					"confidential": map[string]interface{}{"type": "boolean", "description": "If true, adds a CONFIDENTIAL — LEGALLY PRIVILEGED banner"},
				},
				"required": []string{"title", "filename", "content"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			confidential, _ := input["confidential"].(bool)
			return r.runPDFPython("generate", map[string]interface{}{
				"title": strInput(input, "title"),
				// Sanitise to a bare filename so the output can't escape
				// output_dir via separators or traversal in a supplied name.
				"filename":     filepath.Base(strInput(input, "filename")),
				"content":      input["content"],
				"output_dir":   r.cfg.PDF.OutputDir,
				"author":       strInput(input, "author"),
				"confidential": confidential,
			})
		},
	}
}
