// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Behavioral tests for tabular_review and read_table_cells (clean-room spec §8
// items 8–9). No network, no real model: the provider registry is pointed at an
// in-process httptest server speaking the OpenAI-compatible chat wire format,
// and the knowledge store is a small in-memory fake.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Fakes ──────────────────────────────────────────────────────────────────

type fakeKnowledge struct {
	docs map[string]string
}

func (f *fakeKnowledge) Search(string, string, int) ([]types.SearchResult, error) { return nil, nil }

func (f *fakeKnowledge) GetFullText(docID string) (string, error) {
	if t, ok := f.docs[docID]; ok {
		return t, nil
	}
	return "", fmt.Errorf("document %s not found", docID)
}

func (f *fakeKnowledge) GetByID(string) *types.Document { return nil }

// newFakeModelServer serves canned per-cell extraction responses on the
// OpenAI-compatible chat completions endpoint. The reply is chosen from the
// user message content so different columns exercise different flags, and a
// prompt containing "GARBLED" yields a non-JSON body to exercise the
// extraction-failed path.
func newFakeModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var user string
		for _, m := range body.Messages {
			if m.Role == "user" {
				var s string
				_ = json.Unmarshal(m.Content, &s)
				user = s
			}
		}

		reply := `{"summary":"New York law [[page:2||quote:governed by the laws of the State of New York]]","flag":"green","reasoning":"Standard, unqualified choice-of-law clause."}`
		switch {
		case strings.Contains(user, "QUOTE (attributed to page"):
			// Citation paraphrase-judge call (tabcite.go): confidently supported.
			reply = `{"supported":true,"confidence":0.9}`
		case strings.Contains(user, "termination"):
			reply = `{"summary":"90-day convenience termination [[page:5||quote:either party may terminate on ninety days written notice]]","flag":"yellow","reasoning":"Notice period is longer than market standard."}`
		case strings.Contains(user, "GARBLED"):
			reply = "I am sorry, I cannot produce structured output right now."
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 120, "completion_tokens": 40},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// newTabularTestRegistry wires a tool registry whose extraction-tier model
// resolves to the fake server through the real provider registry. Reviews
// persist to a fresh in-memory repository; the docx output dir is a per-test
// temp dir so tests never write into the repo tree.
func newTabularTestRegistry(t *testing.T, serverURL string) *Registry {
	return newTabularTestRegistryWithRepo(t, serverURL, store.NewMemoryRepo())
}

// newTabularTestRegistryWithRepo lets a test share one review repository
// across registry instances (persistence round-trips).
func newTabularTestRegistryWithRepo(t *testing.T, serverURL string, repo store.ReviewRepository) *Registry {
	t.Helper()
	cfg := &config.Config{}
	cfg.Model.PrimaryURL = serverURL
	cfg.Model.PrimaryKey = "test-key"
	cfg.PDF.OutputDir = t.TempDir()
	return NewRegistry(cfg, providers.NewRegistry(cfg), nil, nil, repo)
}

func twoByTwoInput() map[string]interface{} {
	return map[string]interface{}{
		"documentIds": []interface{}{"doc-a", "doc-b"},
		"columns": []interface{}{
			map[string]interface{}{"name": "Governing Law", "prompt": "What law governs this agreement?"},
			map[string]interface{}{"name": "Termination", "prompt": "What termination rights exist?"},
		},
	}
}

func twoDocKnowledge() *fakeKnowledge {
	return &fakeKnowledge{docs: map[string]string{
		"doc-a": "This agreement is governed by the laws of the State of New York. Either party may terminate on ninety days written notice.",
		"doc-b": "This deed is governed by the laws of the State of New York. Either party may terminate on ninety days written notice.",
	}}
}

func runReview(t *testing.T, reg *Registry, input map[string]interface{}, ks agents.KnowledgeStore) map[string]interface{} {
	t.Helper()
	res, err := reg.Execute("tabular_review", input, agents.ToolContext{KnowledgeStore: ks, TaskID: "task-1"})
	if err != nil {
		t.Fatalf("tabular_review returned an error: %v", err)
	}
	out, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("tabular_review result is %T, want map", res)
	}
	return out
}

// ─── Acceptance §8 item 8: the matrix ───────────────────────────────────────

func TestTabularReviewMatrix(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	out := runReview(t, reg, twoByTwoInput(), twoDocKnowledge())

	if id, _ := out["reviewId"].(string); id == "" {
		t.Error("reviewId is empty")
	}
	cols, _ := out["columns"].([]string)
	if len(cols) != 2 || cols[0] != "Governing Law" || cols[1] != "Termination" {
		t.Errorf("columns = %v, want [Governing Law Termination]", cols)
	}

	rows, _ := out["rows"].([]ReviewRow)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if len(row.Cells) != 2 {
			t.Fatalf("row %s has %d cells, want 2", row.DocumentID, len(row.Cells))
		}
		for _, cell := range row.Cells {
			if _, ok := reviewFlagLegend[cell.Flag]; !ok {
				t.Errorf("cell %s/%s has invalid flag %q", row.DocumentID, cell.Column, cell.Flag)
			}
			if cell.Summary == "" {
				t.Errorf("cell %s/%s has empty summary", row.DocumentID, cell.Column)
			}
		}
		if row.Cells[0].Flag != "green" {
			t.Errorf("governing-law cell flag = %q, want green", row.Cells[0].Flag)
		}
		if row.Cells[1].Flag != "yellow" {
			t.Errorf("termination cell flag = %q, want yellow", row.Cells[1].Flag)
		}
		if !strings.Contains(row.Cells[0].Summary, "[[page:2||quote:") {
			t.Errorf("governing-law summary lost its citation: %q", row.Cells[0].Summary)
		}
	}

	tally, _ := out["flagTally"].(map[string]int)
	sum := 0
	for _, n := range tally {
		sum += n
	}
	if sum != 4 {
		t.Errorf("flagTally sums to %d, want 4 (tally: %v)", sum, tally)
	}
	if tally["green"] != 2 || tally["yellow"] != 2 {
		t.Errorf("flagTally = %v, want green:2 yellow:2", tally)
	}

	legend, _ := out["legend"].(map[string]string)
	for _, flag := range []string{"green", "grey", "yellow", "red"} {
		if legend[flag] == "" {
			t.Errorf("legend missing meaning for %q", flag)
		}
	}
}

func TestTabularReviewMissingDocGreyRow(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	input := twoByTwoInput()
	input["documentIds"] = []interface{}{"doc-a", "doc-missing"}
	out := runReview(t, reg, input, twoDocKnowledge())

	rows, _ := out["rows"].([]ReviewRow)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	missing := rows[1]
	if missing.DocumentID != "doc-missing" {
		t.Fatalf("row order changed: second row is %s", missing.DocumentID)
	}
	for _, cell := range missing.Cells {
		if cell.Flag != "grey" {
			t.Errorf("missing-doc cell %s flag = %q, want grey", cell.Column, cell.Flag)
		}
		if cell.Summary != "Document not found" {
			t.Errorf("missing-doc cell %s summary = %q, want \"Document not found\"", cell.Column, cell.Summary)
		}
	}
}

func TestTabularReviewGarbledCellDegradesToGrey(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	input := map[string]interface{}{
		"documentIds": []interface{}{"doc-a"},
		"columns": []interface{}{
			map[string]interface{}{"name": "Broken", "prompt": "GARBLED extraction request"},
		},
	}
	out := runReview(t, reg, input, twoDocKnowledge())

	rows, _ := out["rows"].([]ReviewRow)
	if len(rows) != 1 || len(rows[0].Cells) != 1 {
		t.Fatalf("want a 1x1 matrix, got %v", rows)
	}
	cell := rows[0].Cells[0]
	if cell.Flag != "grey" || cell.Summary != "Extraction failed" {
		t.Errorf("garbled cell = {%q %q}, want grey / Extraction failed", cell.Flag, cell.Summary)
	}
	if cell.Reasoning == "" {
		t.Error("garbled cell should carry the parse error in reasoning")
	}
}

func TestTabularReviewStructuredErrors(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	// Missing knowledge store → structured error, empty rows, no thrown error.
	res, err := reg.Execute("tabular_review", twoByTwoInput(), agents.ToolContext{TaskID: "task-1"})
	if err != nil {
		t.Fatalf("expected structured error, got thrown error: %v", err)
	}
	out := res.(map[string]interface{})
	if msg, _ := out["error"].(string); msg == "" {
		t.Error("missing knowledge store should return a structured error message")
	}
	if rows, _ := out["rows"].([]interface{}); len(rows) != 0 {
		t.Errorf("error result should carry empty rows, got %d", len(rows))
	}

	// Empty inputs → structured error too.
	for _, input := range []map[string]interface{}{
		{"documentIds": []interface{}{}, "columns": twoByTwoInput()["columns"]},
		{"documentIds": []interface{}{"doc-a"}, "columns": []interface{}{}},
	} {
		res, err := reg.Execute("tabular_review", input, agents.ToolContext{KnowledgeStore: twoDocKnowledge(), TaskID: "task-1"})
		if err != nil {
			t.Fatalf("expected structured error, got thrown error: %v", err)
		}
		if msg, _ := res.(map[string]interface{})["error"].(string); msg == "" {
			t.Errorf("input %v should return a structured error message", input)
		}
	}
}

// ─── Acceptance §8 item 9: read_table_cells ─────────────────────────────────

func TestReadTableCellsSlicing(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	review := runReview(t, reg, twoByTwoInput(), twoDocKnowledge())
	reviewID := review["reviewId"].(string)

	// Column 1, row 0 — with an out-of-range column index that must be ignored.
	res, err := reg.Execute("read_table_cells", map[string]interface{}{
		"review_id":   reviewID,
		"col_indices": []interface{}{float64(1), float64(9)},
		"row_indices": []interface{}{float64(0)},
	}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("read_table_cells: %v", err)
	}
	out := res.(map[string]interface{})
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("read_table_cells not ok: %v", out["error"])
	}
	cols := out["columns"].([]string)
	if len(cols) != 1 || cols[0] != "Termination" {
		t.Errorf("columns = %v, want [Termination]", cols)
	}
	rows := out["rows"].([]ReviewRow)
	if len(rows) != 1 || rows[0].DocumentID != "doc-a" {
		t.Fatalf("rows = %v, want the single doc-a row", rows)
	}
	if len(rows[0].Cells) != 1 || rows[0].Cells[0].Column != "Termination" {
		t.Errorf("cells = %v, want the single Termination cell", rows[0].Cells)
	}

	// Omitting both index lists reads the whole matrix.
	res, err = reg.Execute("read_table_cells", map[string]interface{}{"review_id": reviewID}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("read_table_cells (all): %v", err)
	}
	out = res.(map[string]interface{})
	if rows := out["rows"].([]ReviewRow); len(rows) != 2 || len(rows[0].Cells) != 2 {
		t.Errorf("full read should return the 2x2 matrix, got %v", rows)
	}
}

func TestReadTableCellsUnknownID(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	res, err := reg.Execute("read_table_cells", map[string]interface{}{"review_id": "no-such-review"}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("read_table_cells: %v", err)
	}
	out := res.(map[string]interface{})
	if ok, _ := out["ok"].(bool); ok {
		t.Error("unknown review_id should return ok: false")
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Error("unknown review_id should carry an error message")
	}
}

// ─── Enhancement: durable persistence (store.ReviewRepository) ──────────────

func TestTabularReviewPersistsToRepoAndFallsBack(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	repo := store.NewMemoryRepo()
	reg := newTabularTestRegistryWithRepo(t, srv.URL, repo)

	out := runReview(t, reg, twoByTwoInput(), twoDocKnowledge())
	reviewID := out["reviewId"].(string)

	// The completed review is persisted through the repository with the full
	// return payload plus a createdAt timestamp.
	payload, found, err := repo.GetReview(context.Background(), reviewID)
	if err != nil || !found {
		t.Fatalf("persisted review missing: found=%v err=%v", found, err)
	}
	var rec ReviewRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		t.Fatalf("persisted payload is not valid JSON: %v", err)
	}
	if rec.ReviewID != reviewID {
		t.Errorf("persisted reviewId = %q, want %q", rec.ReviewID, reviewID)
	}
	if _, err := time.Parse(time.RFC3339, rec.CreatedAt); err != nil {
		t.Errorf("createdAt %q is not RFC 3339: %v", rec.CreatedAt, err)
	}
	if len(rec.Columns) != 2 || len(rec.Rows) != 2 {
		t.Errorf("persisted matrix is %dx%d, want 2x2", len(rec.Rows), len(rec.Columns))
	}
	sum := 0
	for _, n := range rec.FlagTally {
		sum += n
	}
	if sum != 4 {
		t.Errorf("persisted flagTally sums to %d, want 4", sum)
	}
	for _, flag := range []string{"green", "grey", "yellow", "red"} {
		if rec.Legend[flag] == "" {
			t.Errorf("persisted legend missing %q", flag)
		}
	}

	// Evict the in-process cache entry and resolve through a fresh registry
	// sharing the same repository: read_table_cells must hit the store.
	reviewStoreMu.Lock()
	delete(reviewStore, reviewID)
	reviewStoreMu.Unlock()

	reg2 := newTabularTestRegistryWithRepo(t, srv.URL, repo)
	res, err := reg2.Execute("read_table_cells", map[string]interface{}{"review_id": reviewID}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("read_table_cells after eviction: %v", err)
	}
	got := res.(map[string]interface{})
	if ok, _ := got["ok"].(bool); !ok {
		t.Fatalf("repository fallback failed: %v", got["error"])
	}
	if rows := got["rows"].([]ReviewRow); len(rows) != 2 || len(rows[0].Cells) != 2 {
		t.Errorf("repository fallback returned %v, want the 2x2 matrix", rows)
	}
}

func TestReadTableCellsCorruptPayloadIsNotFound(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	repo := store.NewMemoryRepo()
	reg := newTabularTestRegistryWithRepo(t, srv.URL, repo)

	if err := repo.PutReview(context.Background(), "corrupt-rev-1", time.Now(), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Execute("read_table_cells", map[string]interface{}{"review_id": "corrupt-rev-1"}, agents.ToolContext{})
	if err != nil {
		t.Fatalf("read_table_cells: %v", err)
	}
	out := res.(map[string]interface{})
	if ok, _ := out["ok"].(bool); ok {
		t.Error("corrupt review payload should resolve as not found (ok: false)")
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Error("corrupt review payload should carry an error message")
	}
}

// ─── Enhancement: landscape .docx export ────────────────────────────────────

func TestTabularReviewDocxExport(t *testing.T) {
	srv := newFakeModelServer(t)
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	out := runReview(t, reg, twoByTwoInput(), twoDocKnowledge())
	path, _ := out["outputPath"].(string)
	if path == "" {
		t.Fatalf("tabular_review returned no outputPath (docxError: %v)", out["docxError"])
	}
	filename, _ := out["outputFilename"].(string)
	if !strings.HasSuffix(filename, ".docx") {
		t.Errorf("outputFilename = %q, want a .docx name", filename)
	}
	root, _ := filepath.Abs(reg.cfg.PDF.OutputDir)
	if abs, _ := filepath.Abs(path); !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		t.Errorf("outputPath %q is outside the document output dir %q", path, root)
	}

	doc, names := readDocxPart(t, path, "word/document.xml")
	joined := strings.Join(names, "|")
	for _, part := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		if !strings.Contains(joined, part) {
			t.Errorf("archive missing part %s", part)
		}
	}
	for _, snippet := range []string{
		"Tabular Review",
		`w:orient="landscape"`,
		"<w:tbl>",
		"Document", "Governing Law", "Termination", // header row
		"doc-a", "doc-b", // row labels (title falls back to the doc id)
		"[green]", "[yellow]", // flags inline with the summaries
	} {
		if !strings.Contains(doc, snippet) {
			t.Errorf("document.xml missing %q", snippet)
		}
	}
}

// ─── Enhancement: global extraction concurrency cap ─────────────────────────

// TestTabularReviewConcurrencyCap floods a single review with the maximum 30
// columns against a slow fake provider that tracks in-flight requests, and
// asserts the invocation never exceeds maxConcurrentCellCalls.
func TestTabularReviewConcurrencyCap(t *testing.T) {
	var inFlight, maxSeen, total int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cur := atomic.AddInt64(&inFlight, 1)
		atomic.AddInt64(&total, 1)
		for {
			m := atomic.LoadInt64(&maxSeen)
			if cur <= m || atomic.CompareAndSwapInt64(&maxSeen, m, cur) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond) // hold the slot so overlap is observable
		atomic.AddInt64(&inFlight, -1)

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": `{"summary":"Not Found","flag":"grey","reasoning":"n/a"}`,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 10},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	reg := newTabularTestRegistry(t, srv.URL)

	columns := make([]interface{}, 0, maxReviewColumns)
	for i := 0; i < maxReviewColumns; i++ {
		columns = append(columns, map[string]interface{}{
			"name":   fmt.Sprintf("Col %d", i),
			"prompt": fmt.Sprintf("Extract field %d", i),
		})
	}
	input := map[string]interface{}{
		"documentIds": []interface{}{"doc-a"},
		"columns":     columns,
	}
	out := runReview(t, reg, input, twoDocKnowledge())

	rows := out["rows"].([]ReviewRow)
	if len(rows) != 1 || len(rows[0].Cells) != maxReviewColumns {
		t.Fatalf("want a 1x%d matrix, got %d rows", maxReviewColumns, len(rows))
	}
	if got := atomic.LoadInt64(&total); got != maxReviewColumns {
		t.Errorf("model calls = %d, want %d", got, maxReviewColumns)
	}
	if got := atomic.LoadInt64(&maxSeen); got > maxConcurrentCellCalls {
		t.Errorf("max concurrent extraction calls = %d, want <= %d", got, maxConcurrentCellCalls)
	}
}
