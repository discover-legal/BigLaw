// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Tests for docx_generate / replicate_document plus the tabular
// read_table_cells slicer and fetch_documents.

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	cfg := &config.Config{}
	cfg.PDF.OutputDir = t.TempDir()
	reg := NewRegistry(cfg, nil, nil, nil)
	// registerAll does not yet wire the document-production groups.
	reg.registerDocxTools()
	reg.registerTabularTools()
	reg.registerDocumentExtraTools()
	return reg
}

// ─── docx_generate ────────────────────────────────────────────────────────────

func TestDocxGenerate(t *testing.T) {
	reg := newTestRegistry(t)
	res, err := reg.tools["docx_generate"].Exec(map[string]interface{}{
		"title":     "Share Purchase Agreement — Summary",
		"landscape": true,
		"sections": []interface{}{
			map[string]interface{}{
				"heading": "Key Terms",
				"level":   float64(1),
				"content": "The purchase price is USD 5m.\n\n- Completion within 60 days\n- Escrow of 10%",
			},
			map[string]interface{}{
				"pageBreak": true,
				"heading":   "Conditions Precedent",
				"level":     float64(2),
				"table": map[string]interface{}{
					"headers": []interface{}{"CP", "Status"},
					"rows":    []interface{}{[]interface{}{"FDI clearance", "Pending"}},
				},
			},
		},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("docx_generate: %v", err)
	}
	m := res.(map[string]interface{})
	outputPath, _ := m["outputPath"].(string)
	if !strings.HasSuffix(outputPath, "share-purchase-agreement-summary.docx") {
		t.Errorf("unexpected filename: %q", outputPath)
	}
	if m["landscape"] != true || m["sectionCount"] != 2 {
		t.Errorf("unexpected metadata: %+v", m)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated docx: %v", err)
	}
	body := acceptedText(t, data)
	for _, want := range []string{
		"Share Purchase Agreement — Summary",
		"Key Terms",
		"The purchase price is USD 5m.",
		"•  Completion within 60 days",
		"Conditions Precedent",
		"FDI clearance",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("generated body missing %q; got:\n%s", want, body)
		}
	}
	if xml := documentXMLOf(t, data); !strings.Contains(xml, `w:orient="landscape"`) {
		t.Errorf("expected landscape orientation in sectPr")
	}
	if xml := documentXMLOf(t, data); !strings.Contains(xml, `<w:br w:type="page"/>`) {
		t.Errorf("expected a page break")
	}
}

// Generated docs must be editable by edit_document end-to-end.
func TestDocxGenerateThenEdit(t *testing.T) {
	reg := newTestRegistry(t)
	reg.registerTrackedChangesTools()
	res, err := reg.tools["docx_generate"].Exec(map[string]interface{}{
		"title":    "NDA",
		"filename": "nda",
		"sections": []interface{}{
			map[string]interface{}{"content": "The term of this Agreement is two (2) years."},
		},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("docx_generate: %v", err)
	}
	filename := res.(map[string]interface{})["filename"].(string)
	res2, err := reg.tools["edit_document"].Exec(map[string]interface{}{
		"path": filename,
		"edits": []interface{}{map[string]interface{}{
			"find": "two (2) years", "replace": "three (3) years",
			"context_before": "Agreement is ", "context_after": ".",
		}},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("edit_document: %v", err)
	}
	m := res2.(map[string]interface{})
	if m["ok"] != true || m["appliedCount"] != 1 {
		t.Fatalf("edit over generated doc failed: %+v", m)
	}
	data, err := os.ReadFile(m["outputPath"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if got := acceptedText(t, data); !strings.Contains(got, "three (3) years") {
		t.Errorf("accepted view = %q", got)
	}
}

// ─── replicate_document ───────────────────────────────────────────────────────

func TestReplicateDocument(t *testing.T) {
	reg := newTestRegistry(t)
	src := filepath.Join(reg.cfg.PDF.OutputDir, "template.docx")
	original := fixtureDocx(t, fixturePara(fixtureRun("Template body.")))
	if err := os.WriteFile(src, original, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := reg.tools["replicate_document"].Exec(map[string]interface{}{
		"path":         "template.docx",
		"count":        float64(3),
		"new_filename": "draft",
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("replicate_document: %v", err)
	}
	m := res.(map[string]interface{})
	if m["ok"] != true || m["count"] != 3 {
		t.Fatalf("unexpected result: %+v", m)
	}
	copies := m["copies"].([]map[string]interface{})
	for _, c := range copies {
		data, err := os.ReadFile(c["path"].(string))
		if err != nil {
			t.Fatalf("read copy: %v", err)
		}
		if string(data) != string(original) {
			t.Errorf("copy %s is not byte-for-byte identical", c["filename"])
		}
	}
	if copies[0]["filename"] != "draft (1).docx" {
		t.Errorf("unexpected copy name: %v", copies[0]["filename"])
	}
}

// ─── read_table_cells ─────────────────────────────────────────────────────────

func TestReadTableCells(t *testing.T) {
	reg := newTestRegistry(t)
	tabularMu.Lock()
	tabularReviews["rev-1"] = &tabularReviewResult{
		ReviewID: "rev-1",
		Columns:  []string{"Term", "Governing Law"},
		Rows: []tabularRow{
			{DocumentID: "d1", Document: "d1", Cells: []tabularCell{
				{Column: "Term", Summary: "2 years", Flag: "green"},
				{Column: "Governing Law", Summary: "England", Flag: "green"},
			}},
			{DocumentID: "d2", Document: "d2", Cells: []tabularCell{
				{Column: "Term", Summary: "Not Found", Flag: "grey"},
				{Column: "Governing Law", Summary: "Delaware", Flag: "yellow"},
			}},
		},
	}
	tabularMu.Unlock()

	res, err := reg.tools["read_table_cells"].Exec(map[string]interface{}{
		"review_id":   "rev-1",
		"col_indices": []interface{}{float64(1)},
		"row_indices": []interface{}{float64(1)},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("read_table_cells: %v", err)
	}
	m := res.(map[string]interface{})
	if m["ok"] != true || m["cellCount"] != 1 {
		t.Fatalf("unexpected result: %+v", m)
	}
	cell := m["cells"].([]map[string]interface{})[0]
	if cell["summary"] != "Delaware" || cell["flag"] != "yellow" || cell["column"] != "Governing Law" {
		t.Errorf("unexpected cell: %+v", cell)
	}

	// Unknown review id → clean error.
	res2, _ := reg.tools["read_table_cells"].Exec(map[string]interface{}{"review_id": "nope"}, agents.ToolContext{})
	if res2.(map[string]interface{})["ok"] != false {
		t.Errorf("expected not-found result, got %+v", res2)
	}
}

// ─── fetch_documents ──────────────────────────────────────────────────────────

type fakeKnowledge struct{ docs map[string]string }

func (f *fakeKnowledge) Search(string, string, int) ([]types.SearchResult, error) { return nil, nil }
func (f *fakeKnowledge) GetFullText(docID string) (string, error) {
	return f.docs[docID], nil
}
func (f *fakeKnowledge) GetByID(docID string) *types.Document {
	text, ok := f.docs[docID]
	if !ok {
		return nil
	}
	return &types.Document{ID: docID, Content: text}
}

func TestFetchDocuments(t *testing.T) {
	reg := newTestRegistry(t)
	ks := &fakeKnowledge{docs: map[string]string{"a": "Alpha contract text", "b": "Beta brief"}}

	res, err := reg.tools["fetch_documents"].Exec(map[string]interface{}{
		"doc_ids": []interface{}{"a", "b", "missing"},
	}, agents.ToolContext{KnowledgeStore: ks})
	if err != nil {
		t.Fatalf("fetch_documents: %v", err)
	}
	m := res.(map[string]interface{})
	docs := m["documents"].([]map[string]interface{})
	if m["count"] != 3 || len(docs) != 3 {
		t.Fatalf("unexpected count: %+v", m)
	}
	if docs[0]["ok"] != true || docs[0]["content"] != "Alpha contract text" {
		t.Errorf("doc a: %+v", docs[0])
	}
	if docs[2]["ok"] != false || docs[2]["error"] != "Not found" {
		t.Errorf("doc missing: %+v", docs[2])
	}

	// The 20-ID cap applies.
	var many []interface{}
	for i := 0; i < 30; i++ {
		many = append(many, "missing")
	}
	res2, err := reg.tools["fetch_documents"].Exec(map[string]interface{}{"doc_ids": many},
		agents.ToolContext{KnowledgeStore: ks})
	if err != nil {
		t.Fatal(err)
	}
	if res2.(map[string]interface{})["count"] != maxFetchDocs {
		t.Errorf("expected cap at %d, got %+v", maxFetchDocs, res2.(map[string]interface{})["count"])
	}
}
