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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// refusing answers every call as the provider refusing the key: BigLaw's own error for a 401.
type refusing struct{}

func (refusing) Chat(context.Context, model.Request) (*model.Response, error) {
	return nil, &model.Error{Kind: model.ErrNotConfigured, Detail: "HTTP 401 (authentication_error: invalid x-api-key): check BIGLAW_MODEL_KEY"}
}

// suiteAPI serves the real API and engine over chat on a test server, like serve does.
func suiteAPI(t *testing.T, chat model.Chatter) (string, string) {
	dsn := storetest.DSN(t)
	st := storetest.Open(t, store.OpenPool, dsn)
	d := docs.New(st)
	e := engine.New(st, d, chat, model.Roles{Lead: "lead-m"}, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.DiscardHandler))
	mux := api.Handler(d, e, nil, slog.New(slog.DiscardHandler))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok","search":"lexical"}`)) })
	srv := httptest.NewServer(mux)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	return srv.URL, dsn
}

func dummySecret() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "sk-ant-smoke-" + hex.EncodeToString(b)
}

// noSecret fails the test for any file under dir holding secret.
func noSecret(t *testing.T, dir, secret string) {
	filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err == nil && de.Type().IsRegular() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), secret) {
				t.Errorf("%s holds the secret", p)
			}
		}
		return nil
	})
}

type benchSummary struct {
	Mode    string
	Stopped *struct {
		Reason   string
		Provider *string `json:"provider_error_type"`
	}
	Spend struct {
		Charged float64  `json:"charged_usd"`
		Judge   *float64 `json:"judge_usd_estimated"`
	}
	Tasks []struct {
		ID, Status string
		Score      struct{ Passed, Total *int }
		Criteria   map[string]string
		Calls      *int
		Reader     *struct{ Calls int }
		Judge      *struct{ Calls, Pairs *int }
		JudgeCost  *struct {
			USD   float64
			Basis string
		} `json:"judge_cost"`
		Conflicts *int
		Computed  *map[string]int `json:"computed_figures"`
		Gates     *int            `json:"finding_gates"`
		Charged   *float64        `json:"charged_usd"`
		Notes     []string
	}
}

func runSmoke(t *testing.T, apiURL, dsn, secret, out string) ([]byte, int, benchSummary) {
	cmd := exec.Command("python3", "bench/run.py", "smoke", "--api", apiURL, "--out", out)
	cmd.Env = append(os.Environ(), "BIGLAW_DATABASE_URL="+dsn, "BENCH_SMOKE_SECRET="+secret)
	log, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bench/run.py smoke: %v\n%s", err, log)
	}
	t.Logf("%s", log)
	var s benchSummary
	raw, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err != nil || len(s.Tasks) != 3 {
		t.Fatalf("summary: %v\n%s\n%s", err, raw, log)
	}
	return log, code, s
}

// TestBenchSmoke is the offline smoke of the benchmark suite (eval/bench, docs/benchmarks.md): the
// real API and engine over scripted models, driven by `bench/run.py smoke` with eval/bench/smoke.json.
// It checks the pipeline end to end with no key and no credit: the LAB-shaped task is driven, its
// judge cost estimated and charged, judged by the stub judge and measured; the litigation proof run
// (the E1 oracle) is driven and scored by eval/proofrun; the spend cap stops the suite before the third
// task; the summary validates against its schema; compare.py finds the run comparable with itself and
// not with a baseline; and the smoke's dummy secret, which the stub judge echoes, is in no file.
func TestBenchSmoke(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	t.Parallel()
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
	apiURL, dsn := suiteAPI(t, &byTask{markers: []string{"Bench smoke task", lit.Task.Instructions[:60]}, fakes: []*model.Fake{readerFake(lab), readerFake(oracle(lit, names))}})
	secret, out := dummySecret(), t.TempDir()
	log, code, s := runSmoke(t, apiURL, dsn, secret, out)
	if code != 0 {
		t.Fatalf("run.py smoke exited %d\n%s", code, log)
	}
	lt, pr, cp := s.Tasks[0], s.Tasks[1], s.Tasks[2]
	if lt.Status != "scored" || lt.Score.Passed == nil || *lt.Score.Passed != 2 || *lt.Score.Total != 3 || lt.Criteria["C-001"] != "pass" || lt.Criteria["C-003"] != "fail" {
		t.Errorf("the LAB-shaped task, judged by the stub: %+v\n%s", lt, log)
	}
	if lt.JudgeCost == nil || lt.JudgeCost.Basis != "estimate" || lt.JudgeCost.USD <= 0 || s.Spend.Judge == nil || *s.Spend.Judge != lt.JudgeCost.USD {
		t.Errorf("the judge's estimated cost: %+v, spend %+v", lt.JudgeCost, s.Spend)
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
	// Unpriced calls are charged at the task's budget ($5) and the judged task its judge estimate (cents):
	// about $10 charged, so the third task's $5 does not fit the $12 cap and the suite stops before it.
	if cp.Status != "skipped_cap" || s.Stopped == nil || s.Stopped.Reason != "spend_cap" || s.Spend.Charged <= 10 || s.Spend.Charged > 10.5 {
		t.Errorf("the spend cap: %+v, stopped %+v, charged %v\n%s", cp, s.Stopped, s.Spend.Charged, log)
	}
	sum := filepath.Join(out, "summary.json")
	if b, err := exec.Command("python3", "bench/run.py", "check", sum).CombinedOutput(); err != nil {
		t.Errorf("run.py check: %v\n%s", err, b)
	}
	// Compared with itself, as a clean build (a dirty worktree is NOT COMPARABLE by rule; compare's tests cover it).
	var clean map[string]any
	raw, _ := os.ReadFile(sum)
	json.Unmarshal(raw, &clean)
	clean["build"].(map[string]any)["dirty"], clean["build"].(map[string]any)["patch"] = false, nil
	cleanPath := filepath.Join(t.TempDir(), "clean.json")
	b, _ := json.Marshal(clean)
	os.WriteFile(cleanPath, b, 0o644)
	if b, err := exec.Command("python3", "bench/compare.py", "--require-comparable", cleanPath, cleanPath).CombinedOutput(); err != nil || !strings.Contains(string(b), "gained (0)") || !strings.Contains(string(b), "COMPARABLE") {
		t.Errorf("a run compared with itself: %v\n%s", err, b)
	}
	if b, err := exec.Command("python3", "bench/compare.py", "--require-comparable", "bench/baselines/v2-bench6.json", sum).CombinedOutput(); err == nil || !strings.Contains(string(b), "NOT COMPARABLE") {
		t.Errorf("a smoke run compared with a baseline must be flagged not comparable: %v\n%s", err, b)
	}
	judgeLog, _ := os.ReadFile(filepath.Join(out, "logs", "judge-lab-smoke.log"))
	if !strings.Contains(string(judgeLog), "ANTHROPIC_API_KEY=[redacted]") {
		t.Errorf("the stub judge echoes its key; the suite's judge log must hold it redacted:\n%s", judgeLog)
	}
	noSecret(t, out, secret)
}

// TestBenchSmokeProviderRefusal: a provider that refuses the key (BigLaw's 401 error) stops the suite
// at the first task, which is cancelled; the rest are not run; run.py exits 3 and names the error type.
func TestBenchSmokeProviderRefusal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	t.Parallel()
	apiURL, dsn := suiteAPI(t, refusing{})
	secret, out := dummySecret(), t.TempDir()
	log, code, s := runSmoke(t, apiURL, dsn, secret, out)
	if code != 3 || s.Stopped == nil || s.Stopped.Reason != "provider_error" || s.Stopped.Provider == nil || *s.Stopped.Provider != "authentication_error (HTTP 401)" {
		t.Fatalf("exit %d, stopped %+v\n%s", code, s.Stopped, log)
	}
	if got := []string{s.Tasks[0].Status, s.Tasks[1].Status, s.Tasks[2].Status}; strings.Join(got, ",") != "provider_error,not_run,not_run" {
		t.Errorf("statuses %v", got)
	}
	if strings.Contains(string(log), "invalid x-api-key") {
		t.Errorf("the provider's message reached the suite's output (only its type may):\n%s", log)
	}
	noSecret(t, out, secret)
}

// TestBenchPreflight builds the binary and starts `biglaw serve` with exactly the environment the
// real suite gives it (config.json: the Anthropic endpoint, its vendor opt-in, the budgets, the
// roles), a dummy key and a new database, up to /health, then stops: any startup refusal (PR 63 r1
// B1: the vendor breaker) fails it. Outbound HTTPS goes to a loopback proxy that refuses everything
// and must see nothing: startup makes no model call. The suite creates its own database, so the test
// role needs CREATEDB (CI's postgres superuser has it); without it the test skips and says why.
func TestBenchPreflight(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql is not installed: the suite creates its database with it")
	}
	t.Parallel()
	base := strings.TrimSpace(os.Getenv("BIGLAW_TEST_DSN"))
	if base == "" {
		storetest.DSN(t) // skips, or fails under BIGLAW_REQUIRE_DB
		return
	}
	if out, err := exec.Command("psql", base, "-XAtc", "SELECT rolcreatedb OR rolsuper FROM pg_roles WHERE rolname = current_user").Output(); err != nil || strings.TrimSpace(string(out)) != "t" {
		t.Skip("BIGLAW_TEST_DSN's role cannot create databases (CREATEDB): the suite always creates its own, so this test cannot run here")
	}
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connects.Add(1)
		http.Error(w, "no outbound calls in tests", http.StatusForbidden)
	}))
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL)
	secret, out := dummySecret(), t.TempDir()
	cmd := exec.Command("python3", "bench/run.py", "preflight", "--out", out)
	cmd.Env = append(os.Environ(), "BIGLAW_DATABASE_URL="+base, "ANTHROPIC_API_KEY="+secret, "ANTHROPIC_API_KEY_FILE=",
		"HTTPS_PROXY=http://"+u.Host, "https_proxy=http://"+u.Host, "NO_PROXY=", "no_proxy=")
	log, err := cmd.CombinedOutput()
	serveLog, _ := os.ReadFile(filepath.Join(out, "logs", "serve.log"))
	if err != nil || !strings.Contains(string(log), "preflight ok") {
		t.Fatalf("run.py preflight: %v\n%s\nserve.log:\n%s", err, log, serveLog)
	}
	if n := connects.Load(); n != 0 {
		t.Errorf("serve's startup made %d outbound connections", n)
	}
	if !strings.Contains(string(log), "dropped database biglaw_bench_") {
		t.Errorf("the suite's database was not dropped:\n%s", log)
	}
	noSecret(t, out, secret)
}
