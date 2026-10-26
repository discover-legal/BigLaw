// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package eval's tests drive the proof-run matters through the real engine with a scripted model
// (E1: the oracle scores 1.0; E2: a fabricating model fails every matter through G4), score them
// with the Python scorer, and run the Harvey LAB driver against a live API (offline smoke).
package eval

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/api"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func TestMain(m *testing.M) {
	docs.MaybeRunChild()
	var err error
	if runsDir, err = os.MkdirTemp("", "biglaw-proofruns-"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(runsDir)
	os.Exit(code)
}

// sharedRun is one proof run of a matter under one script. TestProofRunsGolden and
// TestProofRunsOracle (and …Fabricator) assert on the same run, the same matter under the same
// script, so whichever asks first drives it and the other reads it: each run is made once, not
// twice, which halves this package's engine work. Its files live in runsDir until the package ends.
type sharedRun struct {
	once    sync.Once
	ok      bool   // drive returned: false when the run failed (or skipped) the test that drove it
	skipped bool   // the test that drove it skipped (no database): the others skip too
	by      string // the test that drove it
	seen    map[string]bool
	v       engine.TaskView
	dir     string
	gates   []string
}

var (
	runsMu  sync.Mutex
	runs    = map[string]*sharedRun{} // matter slug + "/" + script name → the current run
	runsDir string                    // the shared runs' files (TestMain)
)

// driveShared is drive, once per matter and script (sharedRun). A test asking again for a run it
// already read is a new -count iteration, so it starts a fresh run: -count=N still drives N runs.
func driveShared(t *testing.T, mt matter, name string, scripts func(matter, map[string]string) map[string][]model.Reply) (engine.TaskView, string, []string) {
	t.Helper()
	key := mt.Slug + "/" + name
	runsMu.Lock()
	s := runs[key]
	if s == nil || s.seen[t.Name()] {
		s = &sharedRun{seen: map[string]bool{}}
		runs[key] = s
	}
	s.seen[t.Name()] = true
	runsMu.Unlock()
	s.once.Do(func() { // a Fatal or Skip in drive still ends the Once: s.ok stays false
		s.by = t.Name()
		defer func() { s.skipped = t.Skipped() }()
		s.v, s.dir, s.gates = drive(t, mt, scripts)
		s.ok = true
	})
	if s.skipped {
		t.Skipf("the %s run of %s was skipped in %s", name, mt.Slug, s.by)
	}
	if !s.ok {
		t.Fatalf("the %s run of %s failed in %s, which drove it for this test too", name, mt.Slug, s.by)
	}
	return s.v, s.dir, s.gates
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
	"audit_gaps": {reply(m{"gaps": []m{}})}, "meta": {{Text: "Every planted fact was drafted from a verified quote."}, {Text: "Every planted fact was drafted from a verified quote."}}}

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
		extracts = append(extracts, call("extract_facts", m{"question": it.Question, "documentId": nil, "sectionId": nil, "through": nil, "offset": nil, "query": it.Question, "issueId": iid}))
		sc["extract_facts"] = append(sc["extract_facts"], reply(m{"findings": []m{{"issueId": nil, "claim": it.ModelAnswer, "quotes": quotes, "values": []m{}, "entities": []m{},
			"relations": []m{}, "confidence": 1}}, "gaps": []string{}}))
		extra, calcs, also := traceable(mt, i)
		for _, q := range extra { // the item's documents' lines that state the answer's other figures: cited too
			for _, d := range append(append([]string{}, it.Documents...), names(mt)...) {
				if strings.Contains(mt.Docs[d], q) {
					quotes = append(quotes, m{"documentId": ids[d], "quote": q, "locator": nil})
					break
				}
			}
		}
		sc["extract_facts"][len(sc["extract_facts"])-1] = reply(m{"findings": []m{{"issueId": nil, "claim": it.ModelAnswer, "quotes": quotes, "values": []m{}, "entities": []m{},
			"relations": []m{}, "confidence": 1}}, "gaps": []string{}})
		for _, c := range calcs {
			sheets = append(sheets, call("numbers", m{"op": "calc", "fn": c.fn, "operands": c.ops, "holidays": c.holidays, "fids": nil, "salientTop": nil}))
		}
		fids, cites := []string{fid}, " ["+fid+"]"
		for _, k := range also { // another item's finding that states one of its figures (a cross-issue citation)
			fids, cites = append(fids, fmt.Sprintf("F%d", k+1)), cites+fmt.Sprintf(" [F%d]", k+1)
		}
		drafts = append(drafts, call("draft_section", m{"issueId": iid, "fids": fids, "revise": nil}))
		sc["draft_section"] = append(sc["draft_section"], reply(m{"markdown": it.ModelAnswer + cites, "summary": "s"}))
	}
	plan := call("ledger", m{"op": "plan", "issues": issues, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil})
	lead := []model.Reply{{ToolCalls: []model.ToolCall{plan}}}
	lead = append(append(append(lead, batches(extracts)...), batches(sheets)...), batches(drafts)...)
	submit := model.Reply{ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}}                   // a figure no cited span computes is flagged, then disclosed (maxLintRounds)
	sc["lead"] = append(lead, model.Reply{ToolCalls: []model.ToolCall{call("committee", m{})}}, submit, submit, submit, submit) // the last after a lawyer decides gated findings
	return sc
}

// fabricator scripts a model that invents its evidence: eight findings whose quotes are in no
// document, judged unsupported.
func fabricator(mt matter, ids map[string]string) map[string][]model.Reply {
	var fs []m
	for i := range 8 {
		fs = append(fs, m{"issueId": nil, "claim": fmt.Sprintf("Invented fact %d.", i), "quotes": []m{{"documentId": ids[firstDoc(mt)], "quote": fmt.Sprintf("the parties agreed in writing that consent number %d was obtained in full", i), "locator": nil}},
			"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 0.9})
	}
	no := reply(m{"supported": false, "confidence": 0.9})
	plan := call("ledger", m{"op": "plan", "issues": []m{{"id": "I1", "question": "What happened?", "class": "RedFlag", "practice": nil}}, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil})
	return map[string][]model.Reply{
		"lead":          {{ToolCalls: []model.ToolCall{plan}}, {ToolCalls: []model.ToolCall{call("extract_facts", m{"question": "q", "documentId": nil, "sectionId": nil, "through": nil, "offset": nil, "query": "consent", "issueId": "I1"})}}},
		"extract_facts": {reply(m{"findings": fs, "gaps": []string{}})},
		"verify_quote":  {no, no, no, no, no, no, no, no}, // every quote judged: findings stating no value hold no reader reserve back from the judges (bench7)
	}
}

func firstDoc(mt matter) string {
	for name := range mt.Docs {
		return name
	}
	return ""
}

// drive runs one matter through the engine on scripts and writes a run dir the scorer reads.
func drive(t *testing.T, mt matter, scripts func(matter, map[string]string) map[string][]model.Reply) (engine.TaskView, string, []string) {
	st := storetest.Open(t, store.OpenPool)
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
	fake := readerFake(scripts(mt, ids))
	t.Cleanup(func() {
		per := map[string]int{}
		for _, c := range fake.Calls() {
			per[c.Agent]++
		}
		t.Logf("%s: %d model calls, %d figure reader, %d figure judge", mt.Slug, len(fake.Calls()), per["read_figures"], per["judge_figures"])
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
	settle := func() engine.TaskView { // waits for the run to stop or pause: a run still going at the limit fails the test, never asserted on half done
		t.Helper()
		limit := time.Now().Add(2 * time.Minute)
		for {
			v, err := e.Get(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Status != "queued" && v.Status != "running" {
				return v
			}
			if time.Now().After(limit) {
				t.Fatalf("%s: the run is still %s after 2 minutes", mt.Slug, v.Status)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	v := settle()
	if ss, _ := e.Steps(ctx, task.ID, 0); v.Status == "failed" {
		for _, s := range ss {
			if s.Tool == "finish" {
				t.Logf("finish: %s", s.Output)
			}
		}
	}
	var gates []string // what the reviewing lawyer approved, in order
	for range 50 {     // the reviewing lawyer approves each gate (a finding gated for a lawyer, then the deliverable)
		if v.Status != "awaiting_gate" || len(v.PendingGates) == 0 {
			break
		}
		g := v.PendingGates[0]
		gates = append(gates, g.Kind+":"+cmp.Or(g.FindingID, g.GateID))
		if _, err := e.Decide(ctx, task.ID, g.GateID, true, "", engine.Person{ID: "lawyer", Name: "Lawyer"}); err != nil {
			t.Fatal(err)
		}
		v = settle()
	}
	dir, err := os.MkdirTemp(runsDir, mt.Slug+"-") // shared: it outlives the test that drove it (sharedRun)
	if err != nil {
		t.Fatal(err)
	}
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
	return v, dir, gates
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

// unresolvedG8 pins E1's figure checks the oracle's memo is delivered with unresolved, per matter: a
// ratchet that may only fall (lab-r4 r7 M4: 26 at the r7 review, 5 at r8; the one-year date closed by
// date_add's years, r8). What is left is what the stand-in reader cannot read: a memo's bare "(2,810 + 310)"
// the stand-in reads as counts beside the record's EUR millions, and a code section written without
// its "Section" ("280G", "s.16600") the stand-in reads as a count.
var unresolvedG8 = map[string]int{"msa-negotiation": 0, "merger-control": 2, "ma-diligence": 2, "litigation": 0, "ip-license": 0}

// findingGates pins the findings E1's run gates for the reviewing lawyer (every one approved by the
// driver), per matter: a ratchet like unresolvedG8 (lab-r4 r8 L5), so a regression that gates every
// finding fails E1.
var findingGates = map[string]int{"msa-negotiation": 1, "merger-control": 3, "ma-diligence": 0, "litigation": 0, "ip-license": 0}

// TestProofRunsOracle (E1): the five matters, the oracle model, the Python scorer: 1.0, every
// citation verified, and no more unresolved figure checks than pinned.
func TestProofRunsOracle(t *testing.T) {
	for _, mt := range loadMatters(t) {
		t.Run(mt.Slug, func(t *testing.T) {
			t.Parallel()
			v, dir, gates := driveShared(t, mt, "oracle", oracle)
			if v.Status != "complete" {
				t.Fatalf("%s %s", v.Status, deref(v.Error))
			}
			var lines []string
			for _, l := range strings.Split(deref(v.Output), "\n") {
				if strings.Contains(l, "Delivered with an unresolved check") {
					lines = append(lines, l)
				}
			}
			if want, ok := unresolvedG8[mt.Slug]; !ok || len(lines) > want {
				t.Errorf("%d unresolved figure checks, pinned at %d (a ratchet: fix the regression):\n%s", len(lines), want, strings.Join(lines, "\n"))
			} else if len(lines) < want {
				t.Logf("%d unresolved figure checks, below the pinned %d: lower the pin", len(lines), want)
			}
			disclosed, tier := map[string]int{}, ""
			for _, l := range strings.Split(deref(v.Output), "\n") {
				switch {
				case strings.HasPrefix(l, "Check first"):
					tier = "check first"
				case strings.HasPrefix(l, "Arithmetic over"):
					tier = "arithmetic"
				case strings.HasPrefix(l, "## "):
					tier = ""
				case tier != "" && strings.HasPrefix(l, "- "):
					disclosed[tier]++
				}
				for _, k := range []string{"Figures (low confidence)", "counterparts put to the figure judge", "written at another scale"} {
					if strings.Contains(l, k) {
						disclosed[k]++
					}
				}
			}
			t.Logf("%s: figure disclosures %v, gates %v", mt.Slug, disclosed, gates)
			found := slices.DeleteFunc(slices.Clone(gates), func(g string) bool { return !strings.HasPrefix(g, "finding:") })
			if want, ok := findingGates[mt.Slug]; !ok || len(found) > want {
				t.Errorf("%s: %d findings gated for a lawyer (%v), pinned at %d (a ratchet: fix the regression)", mt.Slug, len(found), found, want)
			} else if len(found) < want {
				t.Logf("%s: %d findings gated (%v), below the pinned %d: lower the pin", mt.Slug, len(found), found, want)
			}
			if len(gates) == 0 || !strings.HasPrefix(gates[len(gates)-1], "deliverable:") {
				t.Errorf("gates approved %v: the deliverable's gate last", gates)
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
			v, dir, _ := driveShared(t, mt, "fabricator", fabricator)
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
	st := storetest.Open(t, store.OpenPool)
	d := docs.New(st)
	labs := t.TempDir()
	task := filepath.Join(labs, "tasks", "smoke", "deadline")
	os.MkdirAll(filepath.Join(task, "documents"), 0o755)
	os.WriteFile(filepath.Join(task, "task.json"), []byte(`{"title": "Smoke", "work_type": "analyze", "instructions": "When is the answer due?", "deliverables": ["memo.md"]}`), 0o644)
	os.WriteFile(filepath.Join(task, "documents", "docket.md"), []byte("Docket\nTalbot was served on 10/21/2026 via its registered agent.\n"), 0o644)
	plan := call("ledger", m{"op": "plan", "issues": []m{{"id": "I1", "question": "When was Talbot served?", "class": "Requirement", "practice": nil}}, "issueId": nil, "fid": nil, "detail": nil, "criticId": nil, "resolution": nil, "note": nil, "entity": nil, "offset": nil})
	sc := map[string][]model.Reply{
		"lead": {{ToolCalls: []model.ToolCall{plan}}, {ToolCalls: []model.ToolCall{call("extract_facts", m{"question": "q", "documentId": nil, "sectionId": nil, "through": nil, "offset": nil, "query": "served", "issueId": "I1"})}},
			{ToolCalls: []model.ToolCall{call("draft_section", m{"issueId": "I1", "fids": []string{"F1"}, "revise": nil})}},
			{ToolCalls: []model.ToolCall{call("committee", m{})}}, {ToolCalls: []model.ToolCall{call("finish", m{"op": "submit", "limitations": nil})}}},
		"extract_facts": {reply(m{"findings": []m{{"issueId": nil, "claim": "Talbot was served on 10/21/2026.", "quotes": []m{{"documentId": "docket.md", "quote": "Talbot was served on 10/21/2026 via its registered agent", "locator": nil}},
			"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 1}}, "gaps": []string{}})},
		"draft_section": {reply(m{"markdown": "Talbot was served on 10/21/2026 via its registered agent [F1].", "summary": "s"})},
	}
	for k, v := range critics {
		sc[k] = v
	}
	e := engine.New(st, d, readerFake(sc), model.Roles{Lead: "lead-m"}, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.DiscardHandler))
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

// readerFake is a scripted model whose figure reader is the stand-in (figtest.Reader), so a run's
// figures are read as the engine's tests read them.
func readerFake(sc map[string][]model.Reply) *model.Fake {
	f := model.NewFake(sc)
	f.Respond = figtest.Reader
	return f
}

// readAs is the stand-in reader's reading of one passage of kind ("quote", "memo") over text.
func readAs(kind, text string) []analysis.Reading {
	p := analysis.PassageOf("p", text)
	fp := figtest.Passage{ID: "p", Kind: kind, Text: text}
	for i, s := range p.Spans {
		fp.Spans = append(fp.Spans, struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}{analysis.SpanID("p", i), s.Text})
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, figtest.Labels(fp, nil, nil))
	var out []analysis.Reading
	for i := range p.Spans {
		out = append(out, rs[analysis.SpanID("p", i)])
	}
	return out
}

// calc is one numbers call the oracle makes: fn over operands (holidays: a deadline's roll).
type calc struct {
	fn       string
	ops      []m
	holidays []string
}

// traceable is what a perfect drafter of item i cites and computes so that every figure of its answer
// traces (G8, lab-r4 r6 M3, r7): another item's finding whose evidence states a figure as the memo
// reads it (also: a cross-issue citation; the same kind, currency and amount, or a number of another
// kind as written), else the document line that does and shares the memo's words around it (extra:
// cited too), and calcs over cited figures only (literals with their units: the reading's currency,
// "%", or none): a sum, difference, product of up to three (at most one of them money) or percentage
// of cited figures (one of them maybe from a document line, cited too), the days between two cited
// dates, or a cited date shifted by a period the evidence states (rolled past a cited holiday). A
// figure it can neither cite nor compute stays untraced: the run flags it, then discloses it.
func traceable(mt matter, i int) (extra []string, calcs []calc, also []int) {
	it := mt.Items[i]
	answer := analysis.MaskLocators(it.ModelAnswer)
	type src struct {
		r    analysis.Reading
		item int    // the item whose evidence states it (-1: a document line)
		line string // the line, for -1
	}
	var results []analysis.Reading
	cite := func(k int) {
		if k != i && k >= 0 && !slices.Contains(also, k) {
			also = append(also, k)
		}
	}
	pool := func() (out []src) { // the cited figures: the item's evidence and lines, then every other item's evidence
		for _, ev := range append(slices.Clone(it.Evidence), extra...) {
			for _, r := range readAs("quote", ev) {
				out = append(out, src{r, i, ""})
			}
		}
		for k, x := range mt.Items {
			for _, ev := range x.Evidence {
				for _, r := range readAs("quote", ev) {
					out = append(out, src{r, k, ""})
				}
			}
		}
		return out
	}
	states := func(r, k analysis.Reading) bool { // k states r as the memo reads it
		if !r.Clear() {
			return !k.Clear() && k.Kind == r.Kind && k.Text == r.Text
		}
		same, soft := analysis.SameFigure(r, k)
		return same || soft && k.Clear() && (k.Scale == "") != (r.Scale == "") // limited and disclosed, not untraced
	}
	context := func(r analysis.Reading) []string { // the memo's words around a figure
		lo, hi := max(0, r.Start-40), min(len(answer), r.End+40)
		var ws []string
		for _, w := range strings.FieldsFunc(strings.ToLower(answer[lo:hi]), func(c rune) bool { return !unicode.IsLetter(c) }) {
			if len(w) > 3 && !slices.Contains(ws, w) {
				ws = append(ws, w)
			}
		}
		return ws
	}
	docLines := func() (out []string) { // the item's documents' lines first, then the others'
		for _, d := range append(slices.Clone(it.Documents), names(mt)...) {
			for _, l := range strings.Split(mt.Docs[d], "\n") {
				if l = strings.TrimSpace(l); l != "" && len(l) < 1500 && !slices.Contains(out, l) {
					out = append(out, l)
				}
			}
		}
		return out
	}
	line := func(ws []string, f func(string) bool) string { // the line that does and shares the most of the memo's words ws (at least one)
		best, score := "", 0
		for _, l := range docLines() {
			n := 0
			for _, w := range ws {
				n += map[bool]int{true: 1}[strings.Contains(strings.ToLower(l), w)]
			}
			if n > score && f(l) {
				best, score = l, n
			}
		}
		return best
	}
	lit := func(k analysis.Reading) m {
		unit := any(nil)
		switch k.Kind {
		case analysis.KindMoney:
			unit = k.Currency
		case analysis.KindPercent:
			unit = "%"
		}
		return m{"ref": nil, "literal": k.Amount, "unit": unit}
	}
	memo := readAs("memo", answer)
	for _, r := range memo {
		if r.Kind == analysis.KindDate || slices.ContainsFunc(results, func(k analysis.Reading) bool { return states(r, k) }) {
			continue
		}
		if k := slices.IndexFunc(pool(), func(x src) bool { return states(r, x.r) }); k >= 0 {
			cite(pool()[k].item)
			continue
		}
		if l := line(context(r), func(l string) bool {
			return slices.ContainsFunc(readAs("quote", l), func(k analysis.Reading) bool { return states(r, k) })
		}); l != "" {
			extra = append(extra, l)
			continue
		}
		if !r.Clear() {
			continue
		}
		var ks []analysis.Reading
		from := map[int]int{} // the pool index of each operand
		for _, x := range pool() {
			ks = append(ks, x.r)
		}
		c, used, ok := compute(r, ks, results, lit)
		if !ok { // operands from document lines too (cited with it)
			all, of := slices.Clone(ks), map[int]string{}
			for _, l := range docLines() {
				for _, x := range readAs("quote", l) {
					of[len(all)] = l
					all = append(all, x)
				}
			}
			if c, used, ok = compute(r, all, results, lit); ok {
				for _, u := range used {
					if l, from := of[u]; from && !slices.Contains(extra, l) {
						extra = append(extra, l)
					}
				}
			}
		}
		if ok {
			for _, u := range used {
				if u < len(ks) {
					from[u] = pool()[u].item
					cite(from[u])
				}
			}
			calcs = append(calcs, c)
			results = append(results, analysis.Reading{Kind: r.Kind, Currency: r.Currency, Amount: r.Amount, Text: r.Text})
		}
	}
	cited := func() []analysis.Date { // the full dates the cited evidence states
		evs := append(slices.Clone(it.Evidence), extra...)
		for _, k := range also {
			evs = append(evs, mt.Items[k].Evidence...)
		}
		ds, _ := analysis.Literal(strings.Join(evs, "\n"))
		var out []analysis.Date
		for _, k := range ds {
			if d, _ := analysis.ParseDate(k); d.Day != 0 {
				out = append(out, d)
			}
		}
		return out
	}
	dates, _ := analysis.Literal(it.ModelAnswer)
	var due []analysis.Date
	for _, k := range dates { // a date another item's evidence or a line states, else a deadline (below)
		d, _ := analysis.ParseDate(k)
		if d.Day == 0 || slices.ContainsFunc(cited(), func(x analysis.Date) bool { return x.Compatible(d) }) {
			continue
		}
		states := func(text string) bool {
			ds, _ := analysis.Literal(text)
			return slices.ContainsFunc(ds, func(c string) bool { x, _ := analysis.ParseDate(c); return x.Compatible(d) })
		}
		if k := slices.IndexFunc(mt.Items, func(x item) bool { return states(strings.Join(x.Evidence, "\n")) }); k >= 0 {
			cite(k)
			continue
		}
		if l := line(append(context(analysis.Reading{Start: len(answer) / 2, End: len(answer) / 2}), ""), states); l != "" { // "": a line that states it may share no word
			extra = append(extra, l)
			continue
		}
		due = append(due, d)
	}
	for _, r := range memo { // a number of days: the days between two cited dates
		if r.Kind != analysis.KindDuration || r.Unit != "days" || slices.ContainsFunc(results, func(k analysis.Reading) bool { return states(r, k) }) ||
			slices.ContainsFunc(pool(), func(x src) bool { return states(r, x.r) }) {
			continue
		}
		ds := cited()
	pairs:
		for a, x := range ds {
			for _, y := range ds[a+1:] {
				if n := analysis.DaysBetween(x, y); r.Rat() != nil && r.Rat().Cmp(big.NewRat(n, 1)) == 0 {
					calcs = append(calcs, calc{fn: "date_diff", ops: []m{{"ref": nil, "literal": x.Key(), "unit": "date"}, {"ref": nil, "literal": y.Key(), "unit": "date"}}})
					results = append(results, r)
					break pairs
				}
			}
		}
	}
	for _, d := range due {
		var ks []analysis.Reading
		for _, x := range pool() {
			if x.item == i || slices.Contains(also, x.item) {
				ks = append(ks, x.r)
			}
		}
		if c, ok := deadline(d, cited(), ks); ok {
			calcs = append(calcs, c)
		}
	}
	return extra, calcs, also
}

// compute is a calc over clear cited amounts ks (and earlier results) whose result is r's amount, of
// r's kind: a sum or difference of two of one kind, a product of two or three with at most one money
// factor (a percentage as a fraction); used are the ks indexes of its operands (one past ks: a result).
func compute(r analysis.Reading, ks, results []analysis.Reading, lit func(analysis.Reading) m) (calc, []int, bool) {
	want := r.Rat()
	type opnd struct {
		k analysis.Reading
		i int
	}
	var xs []opnd
	for i, k := range append(slices.Clone(ks), results...) {
		if k.Clear() && !slices.ContainsFunc(xs, func(x opnd) bool { return x.k.Kind == k.Kind && x.k.Currency == k.Currency && x.k.Amount == k.Amount }) {
			xs = append(xs, opnd{k, i})
		}
	}
	frac := func(k analysis.Reading) *big.Rat {
		if k.Kind == analysis.KindPercent {
			return new(big.Rat).Quo(k.Rat(), big.NewRat(100, 1))
		}
		return k.Rat()
	}
	kindOf := func(fs ...opnd) (string, string) { // a product's kind: its money factor's, else a count's, else a percentage
		kind := analysis.KindPercent
		for _, f := range fs {
			if f.k.Kind == analysis.KindMoney {
				return f.k.Kind, f.k.Currency
			}
			if f.k.Kind == analysis.KindCount {
				kind = analysis.KindCount
			}
		}
		return kind, ""
	}
	money := func(fs ...opnd) int {
		n := 0
		for _, f := range fs {
			n += map[bool]int{true: 1}[f.k.Kind == analysis.KindMoney]
		}
		return n
	}
	idx := func(fs ...opnd) (out []int) {
		for _, f := range fs {
			out = append(out, f.i)
		}
		return out
	}
	for a, x := range xs {
		for b, y := range xs {
			if a == b || x.k.Kind != y.k.Kind || x.k.Currency != y.k.Currency || x.k.Kind != r.Kind || x.k.Currency != r.Currency {
				continue
			}
			if a < b && new(big.Rat).Add(x.k.Rat(), y.k.Rat()).Cmp(want) == 0 {
				return calc{fn: "sum", ops: []m{lit(x.k), lit(y.k)}}, idx(x, y), true
			}
			if new(big.Rat).Sub(x.k.Rat(), y.k.Rat()).Cmp(want) == 0 {
				return calc{fn: "diff", ops: []m{lit(x.k), lit(y.k)}}, idx(x, y), true
			}
		}
	}
	target := map[bool]*big.Rat{true: frac(r), false: want}[r.Kind == analysis.KindPercent]
	for a, x := range xs {
		for b, y := range xs {
			if a >= b || money(x, y) > 1 {
				continue
			}
			if k, c := kindOf(x, y); k == r.Kind && c == r.Currency && new(big.Rat).Mul(frac(x.k), frac(y.k)).Cmp(target) == 0 {
				return calc{fn: "product", ops: []m{lit(x.k), lit(y.k)}}, idx(x, y), true
			}
		}
	}
	if len(xs) <= 150 { // three factors (shares × price × multiple), over a small pool
		for a, x := range xs {
			for b := a + 1; b < len(xs); b++ {
				for c := b + 1; c < len(xs); c++ {
					y, z := xs[b], xs[c]
					if money(x, y, z) > 1 {
						continue
					}
					if k, cur := kindOf(x, y, z); k == r.Kind && cur == r.Currency && k != analysis.KindPercent && new(big.Rat).Mul(new(big.Rat).Mul(frac(x.k), frac(y.k)), frac(z.k)).Cmp(want) == 0 {
						return calc{fn: "product", ops: []m{lit(x.k), lit(y.k), lit(z.k)}}, idx(x, y, z), true
					}
				}
			}
		}
	}
	return calc{}, nil, false
}

// deadline is a date_add from a cited date by a period the evidence states (a duration the reader
// reads in calendar days, weeks, months or years: "21 days", "one (1) year"), forward or back, rolled past the cited dates
// as holidays when the raw date is one of them; none when no cited date and period reach d.
func deadline(d analysis.Date, dates []analysis.Date, ks []analysis.Reading) (calc, bool) {
	var holidays []string
	for _, x := range dates {
		holidays = append(holidays, x.Key())
	}
	for _, x := range dates {
		for _, k := range ks {
			if k.Kind != analysis.KindDuration || k.Unit == "business days" || k.Unclear || k.Rat() == nil || !k.Rat().IsInt() {
				continue
			}
			n := new(big.Rat).Abs(k.Rat()).Num().Int64()
			for _, sign := range []int64{1, -1} {
				for _, hol := range [][]string{nil, holidays} {
					ops := []m{{"ref": nil, "literal": x.Key(), "unit": "date"}, {"ref": nil, "literal": strconv.FormatInt(sign*n, 10), "unit": k.Unit}}
					var o []analysis.Operand
					data, _ := json.Marshal(ops)
					_ = json.Unmarshal(data, &o)
					if w, err := analysis.Calc(nil, "date_add", o, hol); err == nil && w.Result == d.Key() {
						return calc{fn: "date_add", ops: ops, holidays: hol}, true
					}
				}
			}
		}
	}
	return calc{}, false
}
