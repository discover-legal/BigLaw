// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package eval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/api"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

// byTask answers each task of a suite from its own scripted model: a lead request holding a task's
// marker (its instructions) switches to that task's Fake, and every later call goes there until the
// next task's lead starts (the suite runs its tasks one at a time).
type byTask struct {
	mu      sync.Mutex
	markers []string
	fakes   []*model.Fake
	cur     int
}

func (b *byTask) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	b.mu.Lock()
	if req.Agent == "lead" {
		text := fmt.Sprint(req.Messages)
		for i, m := range b.markers {
			if strings.Contains(text, m) {
				b.cur = i
			}
		}
	}
	f := b.fakes[b.cur]
	b.mu.Unlock()
	return f.Chat(ctx, req)
}

// TestBenchSmoke is the offline smoke of the benchmark suite (eval/bench, docs/benchmarks.md): the
// real API and engine over scripted models, driven by `bench/run.py smoke` with eval/bench/smoke.json.
// It checks the pipeline end to end with no key and no credit: the LAB-shaped task is driven, judged
// by the stub judge and measured; the litigation proof run (the E1 oracle) is driven and scored by
// eval/proofrun; the spend cap stops the suite before the third task; the summary validates against
// its schema; compare.py finds the run comparable with itself and not with a baseline; and the
// smoke's dummy secret appears in no file the suite wrote.
func TestBenchSmoke(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	t.Parallel()
	dsn := storetest.DSN(t)
	st, err := store.OpenPool(context.Background(), dsn, nil, storetest.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := docs.New(st)
	var lit matter
	for _, mt := range loadMatters(t) {
		if mt.Slug == "litigation" {
			lit = mt
		}
	}
	names := map[string]string{} // the driver ingests documents by file name, which resolves as a document id
	for n := range lit.Docs {
		names[n] = n
	}
	plan := call("ledger", m{"op": "plan", "issues": []m{{"id": "I1", "question": "When was Talbot served?", "class": "Requirement", "practice": nil}}, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil})
	lab := map[string][]model.Reply{
		"lead": {{ToolCalls: []model.ToolCall{plan}}, {ToolCalls: []model.ToolCall{call("extract_facts", m{"question": "q", "documentId": nil, "sectionId": nil, "through": nil, "offset": nil, "query": "served", "issueId": "I1"})}},
			{ToolCalls: []model.ToolCall{call("draft_section", m{"issueId": "I1", "fids": []string{"F1"}, "revise": nil})}},
			{ToolCalls: []model.ToolCall{call("committee", m{})}}, {ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}}},
		"extract_facts": {reply(m{"findings": []m{{"issueId": nil, "claim": "Talbot was served on 10/21/2026.", "quotes": []m{{"documentId": "docket.md", "quote": "Talbot was served on 10/21/2026 via its registered agent", "locator": nil}},
			"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 1}}, "gaps": []string{}})},
		"draft_section": {reply(m{"markdown": "Talbot was served on 10/21/2026 via its registered agent [F1].", "summary": "s"})},
	}
	for k, v := range critics {
		lab[k] = v
	}
	chat := &byTask{markers: []string{"Bench smoke task", lit.Task.Instructions[:60]}, fakes: []*model.Fake{readerFake(lab), readerFake(oracle(lit, names))}}
	e := engine.New(st, d, chat, model.Roles{Lead: "lead-m"}, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.DiscardHandler))
	mux := api.Handler(d, e, nil, slog.New(slog.DiscardHandler))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok","search":"lexical"}`)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Start(ctx) }()
	defer func() { cancel(); <-done }()

	secret := make([]byte, 16)
	rand.Read(secret)
	dummy := "sk-ant-smoke-" + hex.EncodeToString(secret)
	out := t.TempDir()
	cmd := exec.Command("python3", "bench/run.py", "smoke", "--api", srv.URL, "--out", out)
	cmd.Env = append(os.Environ(), "BIGLAW_DATABASE_URL="+dsn, "BENCH_SMOKE_SECRET="+dummy)
	log, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bench/run.py smoke: %v\n%s", err, log)
	}
	t.Logf("%s", log)
	var s struct {
		Mode    string
		Stopped *struct{ Reason string }
		Spend   struct {
			Charged float64 `json:"charged_usd"`
		}
		Tasks []struct {
			ID, Status string
			Score      struct{ Passed, Total *int }
			Criteria   map[string]string
			Calls      *int
			Reader     *struct{ Calls int }
			Judge      *struct{ Calls, Pairs *int }
			Conflicts  *int
			Computed   *map[string]int `json:"computed_figures"`
			Gates      *int            `json:"finding_gates"`
			Charged    *float64        `json:"charged_usd"`
			Notes      []string
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	t.Logf("%s", raw)
	if err != nil || len(s.Tasks) != 3 {
		t.Fatalf("summary: %v\n%s\n%s", err, raw, log)
	}
	lt, pr, cp := s.Tasks[0], s.Tasks[1], s.Tasks[2]
	if lt.Status != "scored" || lt.Score.Passed == nil || *lt.Score.Passed != 2 || *lt.Score.Total != 3 || lt.Criteria["C-001"] != "pass" || lt.Criteria["C-003"] != "fail" {
		t.Errorf("the LAB-shaped task, judged by the stub: %+v\n%s", lt, log)
	}
	if pr.Status != "scored" || pr.Score.Total == nil || *pr.Score.Passed != *pr.Score.Total || *pr.Score.Total != 20 {
		t.Errorf("the litigation oracle, scored by eval/proofrun: %+v\n%s", pr, log)
	}
	for _, x := range s.Tasks[:2] {
		if x.Calls == nil || *x.Calls == 0 || x.Reader == nil || x.Computed == nil || x.Gates == nil || x.Charged == nil {
			t.Errorf("%s: unmeasured fields: %+v", x.ID, x)
		}
		if _, err := exec.LookPath("psql"); err == nil && (x.Conflicts == nil || x.Judge == nil || x.Judge.Calls == nil) {
			t.Errorf("%s: psql is installed but the database readings are null: %+v", x.ID, x)
		}
	}
	// Unpriced calls are charged at the task's budget ($5) and the judged task its judge's reserve ($1):
	// $11 charged, so the third task's $5 does not fit the $12 cap and the suite stops before it.
	if cp.Status != "skipped_cap" || s.Stopped == nil || s.Stopped.Reason != "spend_cap" || s.Spend.Charged != 11 {
		t.Errorf("the spend cap: %+v, stopped %+v, charged %v\n%s", cp, s.Stopped, s.Spend.Charged, log)
	}
	if b, err := exec.Command("python3", "bench/run.py", "check", filepath.Join(out, "summary.json")).CombinedOutput(); err != nil {
		t.Errorf("run.py check: %v\n%s", err, b)
	}
	if b, err := exec.Command("python3", "bench/compare.py", "--require-comparable", filepath.Join(out, "summary.json"), filepath.Join(out, "summary.json")).CombinedOutput(); err != nil || !strings.Contains(string(b), "gained (0)") {
		t.Errorf("a run compared with itself: %v\n%s", err, b)
	}
	if b, err := exec.Command("python3", "bench/compare.py", "--require-comparable", "bench/baselines/v2-bench6.json", filepath.Join(out, "summary.json")).CombinedOutput(); err == nil || !strings.Contains(string(b), "NOT COMPARABLE") {
		t.Errorf("a smoke run compared with a baseline must be flagged not comparable: %v\n%s", err, b)
	}
	filepath.WalkDir(out, func(p string, de fs.DirEntry, err error) error {
		if err == nil && de.Type().IsRegular() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), dummy) {
				t.Errorf("%s holds the secret", p)
			}
		}
		return nil
	})
}
