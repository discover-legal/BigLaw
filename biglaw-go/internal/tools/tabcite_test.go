// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Behavioral tests for the tabular-review citation verification ladder
// (tabcite.go). No network, no real model: a scriptable httptest server
// speaks the OpenAI-compatible chat wire format and distinguishes extraction
// calls from paraphrase-judge calls, counting each so the tests can assert
// exactly how far up the ladder a citation climbed.

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/store"
)

// ─── Scriptable fake ─────────────────────────────────────────────────────────

// citeFake serves extraction and judge calls separately. Judge replies are
// consumed FIFO from judgeQueue (the last entry repeats once exhausted);
// judgeStatus, when non-zero, fails every judge call with that HTTP status.
type citeFake struct {
	srv             *httptest.Server
	extractionCalls int64
	judgeCalls      int64

	mu           sync.Mutex
	extractReply string
	judgeQueue   []string
	judgeStatus  int
}

func newCiteFake(t *testing.T, extractReply string, judgeQueue ...string) *citeFake {
	t.Helper()
	f := &citeFake{extractReply: extractReply, judgeQueue: judgeQueue}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
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

		var reply string
		if strings.Contains(user, "QUOTE (attributed to page") {
			atomic.AddInt64(&f.judgeCalls, 1)
			f.mu.Lock()
			status := f.judgeStatus
			if len(f.judgeQueue) > 1 {
				reply, f.judgeQueue = f.judgeQueue[0], f.judgeQueue[1:]
			} else if len(f.judgeQueue) == 1 {
				reply = f.judgeQueue[0]
			}
			f.mu.Unlock()
			if status != 0 {
				http.Error(w, "judge unavailable", status)
				return
			}
			if reply == "" {
				reply = `{"supported":true,"confidence":0.9}`
			}
		} else {
			atomic.AddInt64(&f.extractionCalls, 1)
			f.mu.Lock()
			reply = f.extractReply
			f.mu.Unlock()
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{{
				"message":       map[string]interface{}{"role": "assistant", "content": reply},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 100, "completion_tokens": 30},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// citeDoc is the source document every ladder test verifies against.
const citeDoc = `EMPLOYMENT AGREEMENT. Payment is due within 30 days of invoice. ` +
	`The Executive's base salary is $250,000 per annum, payable monthly in arrears. ` +
	`This agreement is governed by the laws of the State of Delaware.`

func oneCellInput() map[string]interface{} {
	return map[string]interface{}{
		"documentIds": []interface{}{"doc-x"},
		"columns": []interface{}{
			map[string]interface{}{"name": "Compensation", "prompt": "What is the base salary?"},
		},
	}
}

func citeKnowledge() *fakeKnowledge {
	return &fakeKnowledge{docs: map[string]string{"doc-x": citeDoc}}
}

// firstCell runs a review through the fake and returns its single cell.
func firstCell(t *testing.T, f *citeFake, input map[string]interface{}) (ReviewCell, map[string]interface{}) {
	t.Helper()
	reg := newTabularTestRegistry(t, f.srv.URL)
	out := runReview(t, reg, input, citeKnowledge())
	rows, _ := out["rows"].([]ReviewRow)
	if len(rows) != 1 || len(rows[0].Cells) == 0 {
		t.Fatalf("want at least a 1x1 matrix, got %v", rows)
	}
	return rows[0].Cells[0], out
}

// ─── Rungs 1–2: string matches, zero judge calls ─────────────────────────────

func TestCitationExactAndTolerantNoJudge(t *testing.T) {
	// First citation is a verbatim substring; the second uses a curly
	// apostrophe and a doubled space where the source has straight/single —
	// only the tolerant rung matches it.
	reply := `{"summary":"$250,000 per annum [[page:1||quote:base salary is $250,000 per annum]] paid monthly [[page:1||quote:The Executive’s base salary is  $250,000 per annum, payable monthly]]","flag":"green","reasoning":"Clearly stated."}`
	f := newCiteFake(t, reply)

	cell, out := firstCell(t, f, oneCellInput())

	if got := atomic.LoadInt64(&f.judgeCalls); got != 0 {
		t.Errorf("judge calls = %d, want 0 (string rungs must not call the model)", got)
	}
	if got := atomic.LoadInt64(&f.extractionCalls); got != 1 {
		t.Errorf("extraction calls = %d, want 1", got)
	}
	if cell.CitationsTotal != 2 || cell.CitationsVerified != 2 {
		t.Fatalf("cell counts = %d/%d, want 2/2 (citations: %+v)", cell.CitationsVerified, cell.CitationsTotal, cell.Citations)
	}
	c0, c1 := cell.Citations[0], cell.Citations[1]
	if !c0.Verified || c0.Method != citeMethodExact || c0.Confidence != citeExactConfidence {
		t.Errorf("citation[0] = %+v, want verified exact_match confidence 1.0", c0)
	}
	if c0.Page != 1 {
		t.Errorf("citation[0].Page = %d, want 1", c0.Page)
	}
	if !c1.Verified || c1.Method != citeMethodTolerant || c1.Confidence != citeTolerantConfidence {
		t.Errorf("citation[1] = %+v, want verified tolerant_match confidence 0.95", c1)
	}

	tally, _ := out["citationTally"].(*CitationTally)
	if tally == nil {
		t.Fatal("output missing citationTally")
	}
	if tally.Total != 2 || tally.Verified != 2 {
		t.Errorf("citationTally = %d/%d, want 2/2", tally.Verified, tally.Total)
	}
	if tally.ByMethod[citeMethodExact] != 1 || tally.ByMethod[citeMethodTolerant] != 1 {
		t.Errorf("byMethod = %v, want exact_match:1 tolerant_match:1", tally.ByMethod)
	}
	for _, m := range []string{citeMethodParaphrase, citeMethodEnsemble, citeMethodUnverified} {
		if tally.ByMethod[m] != 0 {
			t.Errorf("byMethod[%s] = %d, want 0", m, tally.ByMethod[m])
		}
	}
}

// ─── Rung 3: paraphrase judge — exactly one call ─────────────────────────────

func TestCitationParaphraseJudgeOneCall(t *testing.T) {
	reply := `{"summary":"$250k annually [[page:1||quote:the Executive receives base compensation of two hundred fifty thousand dollars each year]]","flag":"green","reasoning":"Paraphrased."}`
	f := newCiteFake(t, reply, `{"supported":true,"confidence":0.9}`)

	cell, _ := firstCell(t, f, oneCellInput())

	if got := atomic.LoadInt64(&f.judgeCalls); got != 1 {
		t.Errorf("judge calls = %d, want exactly 1", got)
	}
	c := cell.Citations[0]
	if !c.Verified || c.Method != citeMethodParaphrase {
		t.Fatalf("citation = %+v, want verified paraphrase_judge", c)
	}
	if c.Confidence != citeParaphraseCap {
		t.Errorf("confidence = %v, want the judge's 0.9 capped at %v", c.Confidence, citeParaphraseCap)
	}
	if cell.CitationsVerified != 1 || cell.CitationsTotal != 1 {
		t.Errorf("cell counts = %d/%d, want 1/1", cell.CitationsVerified, cell.CitationsTotal)
	}
}

// ─── Rung 4: uncertain judge escalates to the 3-vote ensemble ────────────────

func TestCitationUncertainJudgeTriggersEnsemble(t *testing.T) {
	reply := `{"summary":"$250k annually [[page:1||quote:the Executive receives base compensation of two hundred fifty thousand dollars each year]]","flag":"green","reasoning":"Paraphrased."}`
	f := newCiteFake(t, reply,
		`{"supported":true,"confidence":0.5}`,  // uncertain — inside the band
		`{"supported":true,"confidence":0.9}`,  // vote 1
		`{"supported":false,"confidence":0.9}`, // vote 2
		`{"supported":true,"confidence":0.9}`,  // vote 3
	)

	cell, out := firstCell(t, f, oneCellInput())

	if got := atomic.LoadInt64(&f.judgeCalls); got != 4 {
		t.Errorf("judge calls = %d, want 4 (1 uncertain + 3 ensemble votes)", got)
	}
	c := cell.Citations[0]
	if !c.Verified || c.Method != citeMethodEnsemble {
		t.Fatalf("citation = %+v, want verified ensemble_majority (2/3 votes)", c)
	}
	want := citeEnsembleScale * 2.0 / 3.0
	if diff := c.Confidence - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("confidence = %v, want %v (0.75 × 2/3)", c.Confidence, want)
	}
	tally := out["citationTally"].(*CitationTally)
	if tally.ByMethod[citeMethodEnsemble] != 1 {
		t.Errorf("byMethod = %v, want ensemble_majority:1", tally.ByMethod)
	}
}

func TestCitationEnsembleMajorityUnsupported(t *testing.T) {
	reply := `{"summary":"value [[page:1||quote:the Company grants the Executive a private jet allowance]]","flag":"green","reasoning":"n/a"}`
	f := newCiteFake(t, reply,
		`{"supported":true,"confidence":0.4}`,  // uncertain
		`{"supported":false,"confidence":0.9}`, // votes: 1 for, 2 against
		`{"supported":true,"confidence":0.6}`,
		`{"supported":false,"confidence":0.8}`,
	)

	cell, _ := firstCell(t, f, oneCellInput())

	c := cell.Citations[0]
	if c.Verified || c.Method != citeMethodUnverified || c.Confidence != 0 {
		t.Fatalf("citation = %+v, want unverified with confidence 0", c)
	}
	if c.Note == "" {
		t.Error("majority-unsupported citation should carry a note")
	}
	if got := atomic.LoadInt64(&f.judgeCalls); got != 4 {
		t.Errorf("judge calls = %d, want 4", got)
	}
}

// ─── Fabricated quote → unverified; judge failure degrades gracefully ────────

func TestCitationFabricatedQuoteUnverified(t *testing.T) {
	reply := `{"summary":"jet allowance [[page:9||quote:the Company grants the Executive a private jet allowance]]","flag":"green","reasoning":"n/a"}`
	f := newCiteFake(t, reply, `{"supported":false,"confidence":0.95}`)

	cell, out := firstCell(t, f, oneCellInput())

	if got := atomic.LoadInt64(&f.judgeCalls); got != 1 {
		t.Errorf("judge calls = %d, want 1 (a confident rejection must not escalate)", got)
	}
	c := cell.Citations[0]
	if c.Verified || c.Method != citeMethodUnverified || c.Confidence != 0 {
		t.Fatalf("citation = %+v, want unverified / confidence 0", c)
	}
	if !strings.Contains(c.Note, "unsupported") {
		t.Errorf("note = %q, want the unsupported verdict noted", c.Note)
	}
	if cell.CitationsVerified != 0 || cell.CitationsTotal != 1 {
		t.Errorf("cell counts = %d/%d, want 0/1", cell.CitationsVerified, cell.CitationsTotal)
	}
	tally := out["citationTally"].(*CitationTally)
	if tally.Total != 1 || tally.Verified != 0 || tally.ByMethod[citeMethodUnverified] != 1 {
		t.Errorf("citationTally = %+v, want total:1 verified:0 unverified:1", tally)
	}
}

func TestCitationJudgeFailureDegradesCitationOnly(t *testing.T) {
	reply := `{"summary":"$250k annually [[page:1||quote:the Executive receives base compensation of two hundred fifty thousand dollars each year]]","flag":"green","reasoning":"Paraphrased."}`
	f := newCiteFake(t, reply)
	f.mu.Lock()
	f.judgeStatus = http.StatusInternalServerError
	f.mu.Unlock()

	cell, out := firstCell(t, f, oneCellInput())

	// The cell and matrix survive; only the citation degrades.
	if cell.Flag != "green" || !strings.Contains(cell.Summary, "$250k") {
		t.Errorf("cell degraded beyond the citation: %+v", cell)
	}
	c := cell.Citations[0]
	if c.Verified || c.Method != citeMethodUnverified || c.Confidence != 0 {
		t.Fatalf("citation = %+v, want unverified after judge failure", c)
	}
	if !strings.Contains(c.Note, "judge call failed") {
		t.Errorf("note = %q, want the judge error noted", c.Note)
	}
	if out["error"] != nil {
		t.Errorf("matrix must not fail on judge errors, got error %v", out["error"])
	}
}

// ─── Tally across a small matrix + persistence + docx stamp ──────────────────

func TestCitationTallyAcrossMatrixPersistsAndStamps(t *testing.T) {
	// Two docs × one column; each cell carries one exact and one fabricated
	// citation → 4 total, 2 verified (exact_match:2, unverified:2).
	reply := `{"summary":"$250,000 [[page:1||quote:base salary is $250,000 per annum]] and a jet [[page:3||quote:the Company grants the Executive a private jet allowance]]","flag":"yellow","reasoning":"n/a"}`
	f := newCiteFake(t, reply, `{"supported":false,"confidence":0.95}`)

	repo := store.NewMemoryRepo()
	reg := newTabularTestRegistryWithRepo(t, f.srv.URL, repo)
	ks := &fakeKnowledge{docs: map[string]string{"doc-x": citeDoc, "doc-y": citeDoc}}
	input := map[string]interface{}{
		"documentIds": []interface{}{"doc-x", "doc-y"},
		"columns": []interface{}{
			map[string]interface{}{"name": "Compensation", "prompt": "What is the base salary?"},
		},
	}
	out := runReview(t, reg, input, ks)

	rows := out["rows"].([]ReviewRow)
	for _, row := range rows {
		cell := row.Cells[0]
		if cell.CitationsTotal != 2 || cell.CitationsVerified != 1 {
			t.Errorf("row %s counts = %d/%d, want 1/2", row.DocumentID, cell.CitationsVerified, cell.CitationsTotal)
		}
	}
	tally := out["citationTally"].(*CitationTally)
	if tally.Total != 4 || tally.Verified != 2 {
		t.Fatalf("citationTally = %d/%d, want 2/4", tally.Verified, tally.Total)
	}
	if tally.ByMethod[citeMethodExact] != 2 || tally.ByMethod[citeMethodUnverified] != 2 {
		t.Errorf("byMethod = %v, want exact_match:2 unverified:2", tally.ByMethod)
	}

	// The persisted payload carries the new fields untouched.
	reviewID := out["reviewId"].(string)
	payload, found, err := repo.GetReview(context.Background(), reviewID)
	if err != nil || !found {
		t.Fatalf("persisted review missing: found=%v err=%v", found, err)
	}
	var rec ReviewRecord
	if err := json.Unmarshal(payload, &rec); err != nil {
		t.Fatalf("persisted payload invalid: %v", err)
	}
	if rec.CitationTally == nil || rec.CitationTally.Total != 4 || rec.CitationTally.Verified != 2 {
		t.Errorf("persisted citationTally = %+v, want 2/4", rec.CitationTally)
	}
	pc := rec.Rows[0].Cells[0]
	if pc.CitationsTotal != 2 || len(pc.Citations) != 2 || pc.Citations[0].Method != citeMethodExact {
		t.Errorf("persisted cell lost citation fields: %+v", pc)
	}

	// The .docx carries the verification stamp under the H1.
	path, _ := out["outputPath"].(string)
	if path == "" {
		t.Fatalf("no docx output (docxError: %v)", out["docxError"])
	}
	doc, _ := readDocxPart(t, path, "word/document.xml")
	if !strings.Contains(doc, "Citations verified: 2/4") {
		t.Error("document.xml missing the stamp \"Citations verified: 2/4\"")
	}
}

// ─── Parser unit coverage ────────────────────────────────────────────────────

func TestParseCitations(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    []Citation
	}{
		{"none", "Not Found", []Citation{}},
		{"single", "X [[page:3||quote:hello world]]", []Citation{{Page: 3, Quote: "hello world"}}},
		{"ordered pair", "A [[page:1||quote:first]] B [[page:2||quote:second]]",
			[]Citation{{Page: 1, Quote: "first"}, {Page: 2, Quote: "second"}}},
		{"non-numeric page kept as 0", "[[page:iv||quote:roman numeral page]]",
			[]Citation{{Page: 0, Quote: "roman numeral page"}}},
		{"malformed marker skipped", "[[page:1|quote:missing sep]] then [[page:2||quote:good]]",
			[]Citation{{Page: 2, Quote: "good"}}},
		{"unterminated dropped", "[[page:1||quote:never closes", []Citation{}},
		{"empty quote dropped", "[[page:1||quote:]] then [[page:2||quote:kept]]",
			[]Citation{{Page: 2, Quote: "kept"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCitations(tc.summary)
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %d citations, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].Page != tc.want[i].Page || got[i].Quote != tc.want[i].Quote {
					t.Errorf("citation[%d] = {%d %q}, want {%d %q}",
						i, got[i].Page, got[i].Quote, tc.want[i].Page, tc.want[i].Quote)
				}
			}
		})
	}
}

// ─── Window location ─────────────────────────────────────────────────────────

func TestCitationWindowLocatesRegion(t *testing.T) {
	// Rare-token overlap: the target clause sits deep in a long document; the
	// window shipped to the judge must contain it without being the whole doc.
	filler := strings.Repeat("General boilerplate provisions apply to this section as stated. ", 400)
	doc := filler + "The indemnification obligations survive termination for a period of seventy-two months. " + filler
	win := citationWindow(doc, 12, "indemnification obligations survive for seventy-two months")
	if !strings.Contains(win, "seventy-two months") {
		t.Errorf("window missed the target clause; got %d chars starting %q", len(win), truncateUTF8(win, 80))
	}
	if len(win) >= len(doc) {
		t.Errorf("window is the whole document (%d chars) — must be a bounded region", len(win))
	}

	// Form-feed page breaks: the cited page (± a neighbour) is used directly.
	paged := "page one text\fpage two holds the golden clause\fpage three text"
	win = citationWindow(paged, 2, "golden clause")
	if !strings.Contains(win, "golden clause") {
		t.Errorf("paged window missed page 2: %q", win)
	}
	sizeLimited := citationWindow(paged, 99, "golden clause") // out-of-range page → rare-token fallback
	if !strings.Contains(sizeLimited, "golden clause") {
		t.Errorf("out-of-range page should fall back to rare-token overlap: %q", sizeLimited)
	}
}

func TestVerifyCellCitationsAlwaysSetsSlice(t *testing.T) {
	// A cell with no citations must still carry a non-nil empty slice and
	// zero counts — the JSON return shape is stable.
	cell := ReviewCell{Column: "X", Summary: "Not Found", Flag: "grey"}
	(&Registry{}).verifyCellCitations(nil, "", "", citeDoc, &cell)
	if cell.Citations == nil || len(cell.Citations) != 0 {
		t.Errorf("Citations = %#v, want empty non-nil slice", cell.Citations)
	}
	if cell.CitationsTotal != 0 || cell.CitationsVerified != 0 {
		t.Errorf("counts = %d/%d, want 0/0", cell.CitationsVerified, cell.CitationsTotal)
	}
}
