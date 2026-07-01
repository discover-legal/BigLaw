// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package tools

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
)

// newDocToolsRegistry builds a minimal registry with only the document tools,
// rooted at a per-test output directory. No provider or knowledge store is
// needed: these tools make no model calls.
func newDocToolsRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{}
	cfg.PDF.OutputDir = root
	r := &Registry{tools: map[string]*ToolImpl{}, cfg: cfg}
	r.registerDocxTools()
	r.registerTrackedChangesTools()
	return r, root
}

func execTool(t *testing.T, r *Registry, name string, input map[string]interface{}) map[string]interface{} {
	t.Helper()
	tool, ok := r.tools[name]
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	res, err := tool.Exec(input, agents.ToolContext{})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("%s returned %T, want map", name, res)
	}
	return m
}

// readDocxPart extracts one part of a .docx on disk.
func readDocxPart(t *testing.T, path, partName string) (string, []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("%s is not a valid zip: %v", path, err)
	}
	names := make([]string, 0, len(zr.File))
	content := ""
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == partName {
			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open part %s: %v", partName, err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read part %s: %v", partName, err)
			}
			content = string(b)
		}
	}
	return content, names
}

func generateSampleDoc(t *testing.T, r *Registry, extra map[string]interface{}) string {
	t.Helper()
	input := map[string]interface{}{
		"title": "Facility Agreement Review",
		"sections": []interface{}{
			map[string]interface{}{
				"heading": "Key Findings",
				"level":   float64(2),
				"content": "The margin is 2.5 percent over SONIA.\n\n- security over all assets\n- guarantee from the parent",
				"table": map[string]interface{}{
					"headers": []interface{}{"Index", "Clause"},
					"rows":    []interface{}{[]interface{}{"1", "14.2"}},
				},
			},
		},
	}
	for k, v := range extra {
		input[k] = v
	}
	res := execTool(t, r, "docx_generate", input)
	path, _ := res["outputPath"].(string)
	if path == "" {
		t.Fatalf("docx_generate returned no outputPath: %v", res)
	}
	return path
}

// ─── Acceptance §8.1: docx_generate ───────────────────────────────────────────

func TestDocxGenerateWritesValidDocx(t *testing.T) {
	r, root := newDocToolsRegistry(t)
	res := execTool(t, r, "docx_generate", map[string]interface{}{
		"title": "Facility Agreement Review",
		"sections": []interface{}{
			map[string]interface{}{
				"heading": "Key Findings",
				"level":   float64(2),
				"content": "The margin is 2.5 percent over SONIA.\n\n- security over all assets",
				"table": map[string]interface{}{
					"headers": []interface{}{"Index", "Clause"},
					"rows":    []interface{}{[]interface{}{"1", "14.2"}},
				},
			},
		},
	})

	path := res["outputPath"].(string)
	if !strings.HasPrefix(path, root) {
		t.Errorf("output %q not under output dir %q", path, root)
	}
	if got := res["sectionCount"].(int); got != 1 {
		t.Errorf("sectionCount = %d, want 1", got)
	}
	if size := res["fileSizeBytes"].(int); size <= 0 {
		t.Errorf("fileSizeBytes = %d, want > 0", size)
	}
	doc, names := readDocxPart(t, path, "word/document.xml")
	joined := strings.Join(names, "|")
	for _, part := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		if !strings.Contains(joined, part) {
			t.Errorf("archive missing part %s", part)
		}
	}
	for _, snippet := range []string{"Facility Agreement Review", "Key Findings", "security over all assets", "<w:tbl>", "14.2"} {
		if !strings.Contains(doc, snippet) {
			t.Errorf("document.xml missing %q", snippet)
		}
	}
}

// ─── Acceptance §8.2: landscape ───────────────────────────────────────────────

func TestDocxGenerateLandscape(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	path := generateSampleDoc(t, r, map[string]interface{}{"landscape": true})
	doc, _ := readDocxPart(t, path, "word/document.xml")
	if !strings.Contains(doc, `w:orient="landscape"`) {
		t.Error("landscape document lacks landscape section properties")
	}
}

// ─── Acceptance §8.7: path traversal ──────────────────────────────────────────

func TestDocxGeneratePathTraversalNeutralised(t *testing.T) {
	r, root := newDocToolsRegistry(t)
	path := generateSampleDoc(t, r, map[string]interface{}{"filename": "../../escape/../evil.docx"})
	abs, _ := filepath.Abs(path)
	rootAbs, _ := filepath.Abs(root)
	if !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		t.Errorf("output escaped the output dir: %q", abs)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("output file missing: %v", err)
	}
}

func TestReplicateRejectsEscapingPath(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	res := execTool(t, r, "replicate_document", map[string]interface{}{
		"path": ".." + string(filepath.Separator) + "outside.docx",
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Errorf("expected rejection, got %v", res)
	}
}

func TestEditDocumentRejectsEscapingPath(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	res := execTool(t, r, "edit_document", map[string]interface{}{
		"path": ".." + string(filepath.Separator) + "outside.docx",
		"edits": []interface{}{
			map[string]interface{}{"find": "a", "replace": "b", "context_before": "x", "context_after": "y"},
		},
	})
	if ok, _ := res["ok"].(bool); ok {
		t.Errorf("expected rejection, got %v", res)
	}
}

// ─── Acceptance §8.6: replicate_document ──────────────────────────────────────

func TestReplicateDocumentMakesDistinctCopies(t *testing.T) {
	r, _ := newDocToolsRegistry(t)
	src := generateSampleDoc(t, r, nil)
	srcData, _ := os.ReadFile(src)

	res := execTool(t, r, "replicate_document", map[string]interface{}{
		"path":  filepath.Base(src), // exercise output-dir-relative resolution
		"count": float64(3),
	})
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("replicate failed: %v", res)
	}
	copies, _ := res["copies"].([]map[string]interface{})
	if len(copies) != 3 {
		t.Fatalf("want 3 copies, got %d", len(copies))
	}
	seen := map[string]bool{src: true}
	for _, c := range copies {
		p := c["path"].(string)
		if seen[p] {
			t.Errorf("duplicate copy path %q", p)
		}
		seen[p] = true
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("copy missing: %v", err)
			continue
		}
		if !bytes.Equal(data, srcData) {
			t.Errorf("copy %q is not byte-for-byte identical", p)
		}
	}
}

func TestReplicateRejectsNonDocx(t *testing.T) {
	r, root := newDocToolsRegistry(t)
	txt := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(txt, []byte("not a docx"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := execTool(t, r, "replicate_document", map[string]interface{}{"path": "notes.txt"})
	if ok, _ := res["ok"].(bool); ok {
		t.Errorf("expected ok:false for non-docx input, got %v", res)
	}
}

// ─── slug / prose helpers ─────────────────────────────────────────────────────

func TestSlugStem(t *testing.T) {
	cases := map[string]string{
		"Facility Agreement (v2).docx": "Facility-Agreement-v2",
		"../../etc/passwd":             "etc-passwd",
		"  ":                           "document",
		"CP_Checklist":                 "CP_Checklist",
	}
	for in, want := range cases {
		if got := slugStem(in); got != want {
			t.Errorf("slugStem(%q) = %q, want %q", in, got, want)
		}
	}
}
