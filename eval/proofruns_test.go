// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package eval's tests drive the proof-run matters through the real engine with a scripted model
// (E1: the oracle scores 1.0; E2: a fabricating model fails every matter through G4), score them
// with the Python scorer, and run the Harvey LAB driver against a live API (offline smoke).
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/api"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func TestMain(m *testing.M) {
	docs.MaybeRunChild()
	os.Exit(m.Run())
}

type item struct {
	ID, Title, Question, Expected string
	ModelAnswer                   string `json:"model_answer"`
	Documents, Evidence           []string
}

type matter struct {
	ID, Slug string
	Task     struct {
		Instructions string
		Deliverables []string
	}
	Items []item
	Docs  map[string]string // file name → text
}

func loadMatters(t *testing.T) []matter {
	var reg struct{ Matters []struct{ ID, Slug string } }
	readJSON(t, "matters.json", &reg)
	var out []matter
	for _, r := range reg.Matters {
		mt := matter{ID: r.ID, Slug: r.Slug, Docs: map[string]string{}}
		readJSON(t, filepath.Join("tasks", r.ID, "task.json"), &mt.Task)
		var key struct{ Facts, Dispositions []item }
		readJSON(t, filepath.Join("tasks", r.ID, "answer_key.json"), &key)
		for _, d := range key.Dispositions { // a disposition is drafted as "C-01 (title): ACCEPT. answer"
			d.ModelAnswer = fmt.Sprintf("%s (%s): %s. %s", d.ID, d.Title, d.Expected, d.ModelAnswer)
			d.Question = "What is the disposition of " + d.ID + "?"
			key.Facts = append(key.Facts, d)
		}
		mt.Items = key.Facts
		files, _ := filepath.Glob(filepath.Join("tasks", r.ID, "documents", "*"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			mt.Docs[filepath.Base(f)] = string(b)
		}
		out = append(out, mt)
	}
	return out
}

func readJSON(t *testing.T, path string, v any) {
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func call(name string, args any) model.ToolCall {
	b, _ := json.Marshal(args)
	return model.ToolCall{Function: model.Function{Name: name, Arguments: string(b)}}
}

func reply(v any) model.Reply {
	b, _ := json.Marshal(v)
	return model.Reply{Text: string(b)}
}

type m = map[string]any

func batches(cs []model.ToolCall) (out []model.Reply) {
	for len(cs) > 0 {
		n := min(8, len(cs))
		out, cs = append(out, model.Reply{ToolCalls: cs[:n]}), cs[n:]
	}
	return out
}

var critics = map[string][]model.Reply{"prosecute": {reply(m{"verdicts": []m{}})}, "reproduce": {reply(m{"answers": []m{}})},
	"audit_gaps": {reply(m{"gaps": []m{}})}, "meta": {{Text: "Every planted fact was drafted from a verified quote."}}}

// oracle scripts a perfect model over a matter: one plan issue, one verified finding (the answer
// key's evidence, verbatim) and one drafted section (its model answer) per planted fact; every
// figure the answer computes goes through numbers{op:"calc"} (literal operands), so G8 holds.
func oracle(mt matter, ids map[string]string) map[string][]model.Reply {
	var issues []m
	var extracts, sheets, drafts []model.ToolCall
	sc := map[string][]model.Reply{}
	for k, v := range critics {
		sc[k] = v
	}
	for i, it := range mt.Items {
		iid, fid := fmt.Sprintf("I%d", i+1), fmt.Sprintf("F%d", i+1)
		issues = append(issues, m{"id": iid, "question": it.Question, "class": "RedFlag", "practice": nil})
		quotes := []m{}
		for _, ev := range it.Evidence {
			for _, d := range append(append([]string{}, it.Documents...), names(mt)...) {
				if strings.Contains(mt.Docs[d], ev) {
					quotes = append(quotes, m{"documentId": ids[d], "quote": ev, "locator": nil})
					break
				}
			}
		}
		extracts = append(extracts, call("extract_facts", m{"question": it.Question, "documentId": nil, "sectionId": nil, "offset": nil, "query": it.Question, "issueId": iid}))
		sc["extract_facts"] = append(sc["extract_facts"], reply(m{"findings": []m{{"claim": it.ModelAnswer, "quotes": quotes, "values": []m{}, "entities": []m{},
			"relations": []m{}, "confidence": 1}}, "gaps": []string{}}))
		for _, x := range engine.Untraced(it.ModelAnswer, strings.Join(it.Evidence, "\n"), false) {
			op := []m{{"ref": nil, "literal": x, "unit": nil}, {"ref": nil, "literal": "0", "unit": nil}}
			fn := "sum"
			if strings.Contains(x, "-") || len(x) == 4 && strings.HasPrefix(x, "20") {
				fn, op[0]["literal"], op[0]["unit"] = "date_add", (x + "-01-01")[:10], "date"
				if len(x) == 7 {
					op[0]["literal"] = x + "-01"
				}
			}
			sheets = append(sheets, call("numbers", m{"op": "calc", "fn": fn, "operands": op, "holidays": nil, "fids": nil, "salientTop": nil}))
		}
		drafts = append(drafts, call("draft_section", m{"issueId": iid, "fids": []string{fid}, "revise": nil}))
		sc["draft_section"] = append(sc["draft_section"], reply(m{"markdown": it.ModelAnswer + " [" + fid + "]", "summary": "s"}))
	}
	plan := call("ledger", m{"op": "plan", "issues": issues, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil})
	lead := []model.Reply{{ToolCalls: []model.ToolCall{plan}}}
	lead = append(append(append(lead, batches(extracts)...), batches(sheets)...), batches(drafts)...)
	sc["lead"] = append(lead, model.Reply{ToolCalls: []model.ToolCall{call("committee", m{})}}, model.Reply{ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}})
	return sc
}

// fabricator scripts a model that invents its evidence: eight findings whose quotes are in no
// document, judged unsupported.
func fabricator(mt matter, ids map[string]string) map[string][]model.Reply {
	var fs []m
	for i := range 8 {
		fs = append(fs, m{"claim": fmt.Sprintf("Invented fact %d.", i), "quotes": []m{{"documentId": ids[firstDoc(mt)], "quote": fmt.Sprintf("the parties agreed in writing that consent number %d was obtained in full", i), "locator": nil}},
			"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 0.9})
	}
	no := reply(m{"supported": false, "confidence": 0.9})
	plan := call("ledger", m{"op": "plan", "issues": []m{{"id": "I1", "question": "What happened?", "class": "RedFlag", "practice": nil}}, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil})
	return map[string][]model.Reply{
		"lead":          {{ToolCalls: []model.ToolCall{plan}}, {ToolCalls: []model.ToolCall{call("extract_facts", m{"question": "q", "documentId": nil, "sectionId": nil, "offset": nil, "query": "consent", "issueId": "I1"})}}},
		"extract_facts": {reply(m{"findings": fs, "gaps": []string{}})},
		"verify_quote":  {no, no},
	}
}

func firstDoc(mt matter) string {
	for name := range mt.Docs {
		return name
	}
	return ""
}

// drive runs one matter through the engine on scripts and writes a run dir the scorer reads.
func drive(t *testing.T, mt matter, scripts func(matter, map[string]string) map[string][]model.Reply) (engine.TaskView, string) {
	st, err := store.Open(context.Background(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := docs.New(st)
	ids, names := map[string]string{}, map[string]string{}
	var docIDs []string
	for name, text := range mt.Docs {
		doc, err := d.IngestText(context.Background(), name, text)
		if err != nil {
			t.Fatal(err)
		}
		ids[name], names[doc.ID] = doc.ID, name
		docIDs = append(docIDs, doc.ID)
	}
	fake := model.NewFake(scripts(mt, ids))
	t.Cleanup(func() {
		for _, c := range fake.Calls() {
			if c.Agent == "extract_facts" && len(c.Messages) > 3 && t.Failed() {
				t.Logf("repair: %v", c.Messages[len(c.Messages)-1].Content)
				break
			}
		}
	})
	e := engine.New(st, d, fake, model.Roles{Lead: "lead-m"}, nil,
		engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.NewTextHandler(tlog{t}, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Start(ctx) }()
	defer func() { cancel(); <-done }()
	task, err := e.Submit(ctx, engine.Task{Description: mt.Task.Instructions, WorkflowType: "memo", Jurisdiction: "US", MatterNumber: mt.Slug, DocumentIDs: docIDs, ToolBandit: true})
	if err != nil {
		t.Fatal(err)
	}
	var v engine.TaskView
	for range 400 {
		if v, _ = e.Get(ctx, task.ID); v.Status != "queued" && v.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ss, _ := e.Steps(ctx, task.ID, 0); v.Status == "failed" {
		for _, s := range ss {
			if s.Tool == "finish" {
				t.Logf("finish: %s", s.Output)
			}
		}
	}
	if v.Status == "awaiting_gate" && len(v.PendingGates) == 1 {
		v, _ = e.Decide(ctx, task.ID, v.PendingGates[0].GateID, true, "")
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "output"), 0o755)
	for i, name := range mt.Task.Deliverables {
		out := ""
		if i == 0 && v.Output != nil {
			out = *v.Output
		}
		os.WriteFile(filepath.Join(dir, "output", name), []byte(out), 0o644)
	}
	write := func(name string, x any) { b, _ := json.Marshal(x); os.WriteFile(filepath.Join(dir, name), b, 0o644) }
	write("biglaw_tasks.json", []any{v})
	write("documents.json", names)
	write("metrics.json", m{"completed": v.Status == "complete", "error": v.Error})
	return v, dir
}

type scores struct {
	Score, NPassed, NCriteria float64
	Grounding                 struct {
		CitationVerifiedRate *float64 `json:"citation_verified_rate"`
		GroundingAlerts      []string `json:"grounding_alerts"`
	}
	CriteriaResults []struct{ ID, Verdict string } `json:"criteria_results"`
}

func score(t *testing.T, mt matter, dir string) scores {
	out, err := exec.Command("python3", "run.py", "score", "--matter", mt.ID, dir, "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("scorer: %v\n%s", err, out)
	}
	var s scores
	if i := strings.Index(string(out), "\n{"); i < 0 || json.Unmarshal(out[i+1:], &s) != nil {
		t.Fatalf("scorer output:\n%s", out)
	}
	s.NPassed, s.NCriteria = 0, float64(len(s.CriteriaResults))
	for _, r := range s.CriteriaResults {
		if r.Verdict == "pass" {
			s.NPassed++
		}
	}
	return s
}

// TestProofRunsOracle (E1): the five matters, the oracle model, the Python scorer: 1.0, and every
// citation verified.
func TestProofRunsOracle(t *testing.T) {
	for _, mt := range loadMatters(t) {
		t.Run(mt.Slug, func(t *testing.T) {
			t.Parallel()
			v, dir := drive(t, mt, oracle)
			if v.Status != "complete" {
				t.Fatalf("%s %s", v.Status, deref(v.Error))
			}
			s := score(t, mt, dir)
			if s.Score != 1 || s.Grounding.CitationVerifiedRate == nil || *s.Grounding.CitationVerifiedRate != 1 {
				var fails []string
				for _, r := range s.CriteriaResults {
					if r.Verdict != "pass" {
						fails = append(fails, r.ID)
					}
				}
				t.Fatalf("score %.3f (%v of %v), verified %v, failing %v\n%s", s.Score, s.NPassed, s.NCriteria, s.Grounding.CitationVerifiedRate, fails, deref(v.Output))
			}
		})
	}
}

// TestProofRunsFabricator (E2): a model that invents its evidence fails every matter with a
// grounding alert, delivering nothing.
func TestProofRunsFabricator(t *testing.T) {
	for _, mt := range loadMatters(t) {
		t.Run(mt.Slug, func(t *testing.T) {
			t.Parallel()
			v, dir := drive(t, mt, fabricator)
			if v.Status != "failed" || v.GroundingAlert == nil || v.Output != nil {
				t.Fatalf("%s alert %v", v.Status, v.GroundingAlert)
			}
			if s := score(t, mt, dir); len(s.Grounding.GroundingAlerts) != 1 || s.Score >= 0.5 {
				t.Fatalf("scored %+v", s)
			}
		})
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestLabRunnerSmoke: eval/lab/run.py, the Harvey LAB driver, runs a LAB-shaped task end to end
// against a live API (an httptest server over a scripted model), writes the deliverable, the task
// record and the metrics where LAB's eval phase reads them, and approves the gate itself.
func TestLabRunnerSmoke(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	st, err := store.Open(context.Background(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := docs.New(st)
	labs := t.TempDir()
	task := filepath.Join(labs, "tasks", "smoke", "deadline")
	os.MkdirAll(filepath.Join(task, "documents"), 0o755)
	os.WriteFile(filepath.Join(task, "task.json"), []byte(`{"title": "Smoke", "work_type": "analyze", "instructions": "When is the answer due?", "deliverables": ["memo.md"]}`), 0o644)
	os.WriteFile(filepath.Join(task, "documents", "docket.md"), []byte("Docket\nTalbot was served on 10/21/2026 via its registered agent.\n"), 0o644)
	plan := call("ledger", m{"op": "plan", "issues": []m{{"id": "I1", "question": "When was Talbot served?", "class": "Requirement", "practice": nil}}, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil})
	sc := map[string][]model.Reply{
		"lead": {{ToolCalls: []model.ToolCall{plan}}, {ToolCalls: []model.ToolCall{call("extract_facts", m{"question": "q", "documentId": nil, "sectionId": nil, "offset": nil, "query": "served", "issueId": "I1"})}},
			{ToolCalls: []model.ToolCall{call("draft_section", m{"issueId": "I1", "fids": []string{"F1"}, "revise": nil})}},
			{ToolCalls: []model.ToolCall{call("committee", m{})}}, {ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}}},
		"extract_facts": {reply(m{"findings": []m{{"claim": "Talbot was served on 10/21/2026.", "quotes": []m{{"documentId": "docket.md", "quote": "Talbot was served on 10/21/2026 via its registered agent", "locator": nil}},
			"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 1}}, "gaps": []string{}})},
		"draft_section": {reply(m{"markdown": "Talbot was served on 10/21/2026 via its registered agent [F1].", "summary": "s"})},
	}
	for k, v := range critics {
		sc[k] = v
	}
	e := engine.New(st, d, model.NewFake(sc), model.Roles{Lead: "lead-m"}, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.DiscardHandler))
	mux := api.Handler(d, e, nil, slog.New(slog.DiscardHandler))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Start(ctx) }()
	defer func() { cancel(); <-done }()
	results := filepath.Join(labs, "results")
	out, err := exec.Command("python3", "lab/run.py", "--labs-dir", labs, "--task", "smoke/deadline", "--api", srv.URL, "--model-dir", "smoke", "--results-dir", results, "--poll", "0.05", "--timeout", "60").CombinedOutput()
	if err != nil {
		t.Fatalf("lab/run.py: %v\n%s", err, out)
	}
	memo, _ := filepath.Glob(filepath.Join(results, "smoke", "deadline", "smoke", "*", "output", "memo.md"))
	metrics, _ := filepath.Glob(filepath.Join(results, "smoke", "deadline", "smoke", "*", "metrics.json"))
	if len(memo) != 1 || len(metrics) != 1 {
		t.Fatalf("run dir:\n%s", out)
	}
	body, _ := os.ReadFile(memo[0])
	var mx struct {
		Completed bool
		BigLaw    struct {
			GatesResolved int `json:"gates_resolved"`
		} `json:"biglaw"`
	}
	readJSON(t, metrics[0], &mx)
	if !strings.Contains(string(body), "served on 10/21/2026") || !mx.Completed || mx.BigLaw.GatesResolved != 1 {
		t.Fatalf("memo:\n%s\nmetrics %+v\n%s", body, mx, out)
	}
}

// tlog sends the engine's log to the test's.
type tlog struct{ t *testing.T }

func (l tlog) Write(b []byte) (int, error) { l.t.Log(strings.TrimSpace(string(b))); return len(b), nil }

func names(mt matter) (out []string) {
	for n := range mt.Docs {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}
