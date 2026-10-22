// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/model"
)

// killer is test plumbing around model.Fake: at the lead's call number killAt it runs kill (stop
// the process, or cancel the task) and, when stop is set, never reaches the Fake (the call was in
// flight when the process died).
type killer struct {
	*model.Fake
	mu     sync.Mutex
	leads  int
	killAt int
	stop   bool
	kill   func()
}

func (k *killer) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	k.mu.Lock()
	if req.Agent == "lead" {
		k.leads++
	}
	hit := req.Agent == "lead" && k.leads == k.killAt && k.kill != nil
	k.mu.Unlock()
	if hit {
		k.kill()
		if k.stop {
			return nil, context.Canceled
		}
	}
	return k.Fake.Chat(ctx, req)
}

// reopen is a second process over the same database and model: a new engine (a new lease owner).
func (f *fixture) reopen(chat model.Chatter, prices *model.Prices) *Engine {
	f.e = New(f.st, f.d, chat, testRoles, prices, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	return f.e
}

// stale makes a task's lease look expired (a dead process's), so another process takes it.
func (f *fixture) stale(id string) {
	if _, err := f.st.DB().Exec(`UPDATE tasks SET lease_until = now() - interval '1 second' WHERE id = $1`, id); err != nil {
		f.t.Fatal(err)
	}
}

type ledgerRow struct{ Kind, Key, Data string }

func (f *fixture) ledger(id string) []ledgerRow {
	q, err := f.st.DB().Query(`SELECT kind, key, data::text FROM ledger WHERE task_id = $1 ORDER BY kind, key`, id)
	if err != nil {
		f.t.Fatal(err)
	}
	defer q.Close()
	var out []ledgerRow
	for q.Next() {
		var r ledgerRow
		q.Scan(&r.Kind, &r.Key, &r.Data)
		out = append(out, r)
	}
	return out
}

// TestResumeAfterKillMidRun (G10): killed after step k's tool result commits, and again during step
// k+1's model call; a later process resumes, re-issues only step k+1, and the ledger equals an
// uninterrupted run's (TestResumeDoesNotRepeatCommittedSteps: no committed call is made twice).
func TestResumeAfterKillMidRun(t *testing.T) {
	t.Parallel()
	ref := newFixture(t)
	want := ref.run(ref.happy(), Config{})
	wantCalls := len(ref.e.chat.(*model.Fake).Calls())

	f := newFixture(t)
	// Same document ids in both (the scripts name them): copy ref's ids into f's ledger comparison by
	// rebuilding the scripts over f's documents.
	k := &killer{Fake: model.NewFake(f.happy())}
	e := f.reopen(k, nil)
	task := f.submit(0)
	const after = 4 // extract_facts' step
	e.afterCommit = func(id string, n int) {
		if n == after {
			e.active(id).cancel() // the process dies right after the commit
		}
	}
	f.step(context.Background())
	if st := f.steps(task.ID); !strings.HasSuffix(st, "4:tool_call:extract_facts:committed") {
		t.Fatalf("first process: %s", st)
	}
	k.killAt, k.stop = 3, true // the third lead call (step 5) is in flight when the second process dies
	e2 := f.reopen(k, nil)
	k.kill = func() { e2.active(task.ID).cancel() }
	f.stale(task.ID)
	f.step(context.Background())
	if st := f.steps(task.ID); !strings.HasSuffix(st, "5:lead_turn:lead:started") {
		t.Fatalf("second process: %s", st)
	}
	k.kill = nil
	f.reopen(k, nil)
	f.stale(task.ID)
	f.step(context.Background())
	v := f.get(task.ID)
	if stop(v) != "awaiting_gate/finished" || f.steps(task.ID) != ref.steps(want.ID) {
		t.Fatalf("resumed: %s\n%s\nwant\n%s", stop(v), f.steps(task.ID), ref.steps(want.ID))
	}
	norm := func(rs []ledgerRow, ids []string, to []string) string {
		b, _ := json.Marshal(rs)
		s := string(b)
		for i := range ids {
			s = strings.ReplaceAll(s, ids[i], to[i])
		}
		return s
	}
	if a, b := norm(f.ledger(task.ID), f.ids(), []string{"D1", "D2"}), norm(ref.ledger(want.ID), ref.ids(), []string{"D1", "D2"}); a != b {
		t.Fatalf("ledger after resume differs:\n%s\nwant\n%s", a, b)
	}
	if got := len(k.Fake.Calls()); got != wantCalls {
		t.Fatalf("the resumed run made %d model calls, the uninterrupted one %d: a committed step was repeated", got, wantCalls)
	}
}

func TestResumeDoesNotRepeatCommittedSteps(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	k := &killer{Fake: model.NewFake(f.happy())}
	e := f.reopen(k, nil)
	task := f.submit(0)
	e.afterCommit = func(id string, n int) {
		if n == 6 { // after draft_section
			e.active(id).cancel()
		}
	}
	f.step(context.Background())
	f.reopen(k, nil)
	f.stale(task.ID)
	f.step(context.Background())
	per := map[string]int{}
	for _, c := range k.Fake.Calls() {
		per[c.Agent]++
	}
	if want := map[string]int{"lead": 5, "extract_facts": 1, "draft_section": 1, "prosecute": 1, "reproduce": 1, "audit_gaps": 1, "meta": 1}; !reflect.DeepEqual(per, want) {
		t.Fatalf("calls per agent %v, want %v", per, want)
	}
}

// TestResumeChargesInFlightCallConservatively: a model call in flight when the process died is
// charged at its reserved grant (one call and its worst-case USD), on top of the re-issued call.
func TestResumeChargesInFlightCallConservatively(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	prices, err := model.LoadPrices(`{"lead-m": {"in": 1, "out": 5}, "spec-m": {"in": 1, "out": 5}, "crit-m": {"in": 1, "out": 5}}`)
	if err != nil {
		t.Fatal(err)
	}
	cost := 0.001
	sc := f.happy()
	for _, k := range []string{"lead", "extract_facts", "draft_section", "prosecute", "reproduce", "audit_gaps", "meta"} {
		for i := range sc[k] {
			sc[k][i].Usage = model.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: &cost}
		}
	}
	k := &killer{Fake: model.NewFake(sc), killAt: 2, stop: true}
	e := f.reopen(k, prices)
	task := f.submit(0)
	k.kill = func() { e.active(task.ID).cancel() }
	f.step(context.Background())
	var held agent.Grant
	var grant []byte
	f.st.DB().QueryRow(`SELECT grant_held FROM task_steps WHERE task_id = $1 AND status = 'started'`, task.ID).Scan(&grant)
	json.Unmarshal(grant, &held)
	if held.Calls != 1 || held.USD <= cost {
		t.Fatalf("the in-flight step holds %+v", held)
	}
	k.kill = nil
	f.reopen(k, prices)
	f.stale(task.ID)
	f.step(context.Background())
	v := f.get(task.ID)
	c, _ := f.e.Cost(context.Background(), task.ID)
	if stop(v) != "awaiting_gate/finished" || v.Run.Budget.Calls != 12 || c.Summary.Calls != 12 || c.Summary.USD == nil || *c.Summary.USD < 11*cost+held.USD-1e-9 {
		t.Fatalf("%s: run budget %+v, cost %+v (lost grant %+v)", stop(v), v.Run.Budget, c.Summary, held)
	}
}

// TestRunLeaseSingleOwner: two processes, one owner; a stale lease is taken over.
func TestRunLeaseSingleOwner(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(f.happy(), Config{})
	task := f.submit(0)
	e1, e2 := f.e, f.reopen(model.NewFake(nil), nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	got := make([]*run, 2)
	for i, e := range []*Engine{e1, e2} {
		wg.Add(1)
		go func() { defer wg.Done(); got[i], _ = e.claim(ctx) }()
	}
	wg.Wait()
	if (got[0] == nil) == (got[1] == nil) {
		t.Fatalf("claims: %v %v", got[0], got[1])
	}
	again, _ := e2.claim(ctx)
	if again != nil {
		t.Fatal("a live lease was taken")
	}
	f.stale(task.ID)
	if r, _ := e2.claim(ctx); r == nil {
		t.Fatal("a stale lease was not taken over")
	}
}

// TestBudgetStopEndsRunWithDisclosure: the budget runs out mid-fix: Go finishes from the reserve
// (budget_exhausted) and the open finding goes into Limitations.
func TestBudgetStopEndsRunWithDisclosure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	search := calls(tc("retrieve", m{"op": "search", "query": "damages", "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil}))
	lead := []model.Reply{calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1")), calls(committee())}
	for range 20 {
		lead = append(lead, search)
	}
	v := f.run(f.with(map[string][]model.Reply{"lead": lead,
		"prosecute": {text(m{"verdicts": []m{{"claim": "c", "verdict": "overstated", "why": "no"}}})}}),
		Config{BudgetCalls: 20, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"})
	if v.Run.StopReason == nil || *v.Run.StopReason != "budget_exhausted" || !strings.Contains(deref(v.Output), "budget ran out") ||
		!strings.Contains(deref(v.Output), "C1") || v.Run.Budget.Calls > 20 {
		t.Fatalf("%s spent %+v:\n%s\n%s", stop(v), v.Run.Budget, deref(v.Output), f.steps(v.ID))
	}
}

// TestUSDBoundFinishDeliversWithoutMeta (4a r2 L8): the floor's USD share is proportional to the
// calls, so a USD-bound run may not fund the meta call; finish still delivers, and says so.
func TestUSDBoundFinishDeliversWithoutMeta(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	prices, err := model.LoadPrices(`{"lead-m": {"in": 1, "out": 5}, "spec-m": {"in": 1, "out": 5}, "crit-m": {"in": 1, "out": 5}}`)
	if err != nil {
		t.Fatal(err)
	}
	f.e = New(f.st, f.d, model.NewFake(map[string][]model.Reply{}), testRoles, prices, Config{BudgetCalls: 150, BudgetUSD: 0.001, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	if v.Status != "awaiting_gate" || v.Run.StopReason == nil || *v.Run.StopReason != "budget_exhausted" || !strings.Contains(deref(v.Output), "could not fund its call") {
		t.Fatalf("%s %v:\n%s\n%s", stop(v), deref(v.Error), deref(v.Output), f.steps(task.ID))
	}
}

// TestMaxStepsStop: at maxSteps the run finishes from the reserve (max_steps) with a disclosure.
func TestMaxStepsStop(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	f.engine(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")),
		calls(draft("I1", "F1")), calls(committee())}}), Config{})
	task := f.submit(6)
	f.step(context.Background())
	v := f.get(task.ID)
	if v.Run.StopReason == nil || *v.Run.StopReason != "max_steps" || !strings.Contains(deref(v.Output), "step limit") || v.Run.Step != 7 {
		t.Fatalf("%s step %d:\n%s\n%s", stop(v), v.Run.Step, f.steps(task.ID), deref(v.Output))
	}
}

// TestFinishRejectedByCriticContinuesLooping (P10): a refused finish returns to the lead, which
// fixes the finding and finishes later.
func TestFinishRejectedByCriticContinuesLooping(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {
			calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1")),
			calls(committee()), calls(finishCall("submit")),
			calls(tc("draft_section", m{"issueId": "I1", "fids": []string{"F1"}, "revise": "Quote it."})),
			calls(ledgerOp("resolve", "criticId", "C1", "resolution", "fixed", "note", "Quoted.")),
			calls(committee()), calls(finishCall("submit")),
		},
		"draft_section": {section("Greystone claims {Zephyr} [F1].", "s"), section("Greystone “claims damages of $644,910 for unpaid invoices” [F1].", "s")},
		"prosecute":     {text(m{"verdicts": []m{{"claim": "c", "verdict": "unsupported", "why": "x"}}}), supported},
		"reproduce":     {noAnswers, noAnswers}, "audit_gaps": {noGaps, noGaps},
	}), Config{})
	fin := f.outputs(v.ID, "finish")
	if stop(v) != "awaiting_gate/finished" || len(fin) != 2 || !strings.Contains(fin[0], `"accepted":false`) {
		t.Fatalf("%s %q", stop(v), fin)
	}
}

// TestAwaitingGatePausesAndResumes: a finding gate pauses the run (awaiting_gate); deciding it
// resumes the lead with the decision; a rejected deliverable sends the lead back with the reason.
func TestAwaitingGatePausesAndResumes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1")),
			calls(committee()), calls(finishCall("submit")), calls(finishCall("submit")), calls(finishCall("submit"))},
		"prosecute": {text(m{"verdicts": []m{{"claim": "c", "verdict": "unsupported", "why": "no"}}})},
		"meta":      {metaNote, metaNote, metaNote},
	}), Config{BudgetCalls: 24, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"})
	task := f.submit(0)
	ctx := context.Background()
	f.step(ctx)
	v := f.get(task.ID)
	if stop(v) != "awaiting_gate/awaiting_gate" || len(v.PendingGates) != 1 || v.PendingGates[0].GateID != "G-F1" {
		t.Fatalf("paused: %s %+v", stop(v), v.PendingGates)
	}
	if v, err := f.e.Decide(ctx, task.ID, "G-F1", true, ""); err != nil || v.Status != "running" {
		t.Fatalf("decide: %v %s", err, v.Status)
	}
	f.step(ctx)
	v = f.get(task.ID)
	if stop(v) != "awaiting_gate/finished" || len(v.PendingGates) != 1 || v.PendingGates[0].Kind != "deliverable" || !strings.Contains(f.steps(task.ID), "note:gate") {
		t.Fatalf("resumed: %s %+v %s", stop(v), v.PendingGates, f.steps(task.ID))
	}
	if _, err := f.e.Decide(ctx, task.ID, v.PendingGates[0].GateID, false, ""); err == nil {
		t.Fatal("a rejection without a reason was accepted")
	}
	if _, err := f.e.Decide(ctx, task.ID, v.PendingGates[0].GateID, false, "Add the answer deadline."); err != nil {
		t.Fatal(err)
	}
	f.step(ctx)
	v = f.get(task.ID)
	ss, _ := f.e.Steps(ctx, task.ID, 0)
	if stop(v) != "awaiting_gate/finished" || v.PendingGates[0].GateID != "G-deliverable-2" || len(v.DecidedGates) != 2 || !strings.Contains(ss[len(ss)-3].Summary, "Add the answer deadline.") {
		t.Fatalf("after rejection: %s %+v %+v", stop(v), v.PendingGates, v.DecidedGates)
	}
	if v, err := f.e.Decide(ctx, task.ID, "G-deliverable-2", true, ""); err != nil || v.Status != "complete" {
		t.Fatalf("approve: %v %s", err, v.Status)
	}
	if _, err := f.e.Decide(ctx, task.ID, "G-deliverable-2", true, ""); err == nil {
		t.Fatal("a decided gate was decided again")
	}
}

// TestCancelStopsRun: cancelling a running task stops it at its next step; a queued one at once.
func TestCancelStopsRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	k := &killer{Fake: model.NewFake(f.happy()), killAt: 3}
	e := f.reopen(k, nil)
	task := f.submit(0)
	other := New(f.st, f.d, k, testRoles, nil, Config{BudgetCalls: 150}, nil) // the cancel arrives through another process
	k.kill = func() { _, _ = other.Cancel(context.Background(), task.ID) }
	f.step(context.Background())
	v := f.get(task.ID)
	if stop(v) != "cancelled/cancelled" || strings.Contains(f.steps(task.ID), "committee") {
		t.Fatalf("%s %s", stop(v), f.steps(task.ID))
	}
	if _, err := e.Cancel(context.Background(), task.ID); err == nil {
		t.Fatal("cancelling an ended task was accepted")
	}
	q := f.submit(0)
	if v, err := e.Cancel(context.Background(), q.ID); err != nil || stop(v) != "cancelled/cancelled" {
		t.Fatalf("queued: %v %s", err, stop(v))
	}
}

// subKiller stops the process at a subagent's first model call (the tool step is in flight).
type subKiller struct {
	*model.Fake
	agent string
	kill  func()
}

func (k *subKiller) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	if req.Agent == k.agent && k.kill != nil {
		k.kill()
		return nil, context.Canceled
	}
	return k.Fake.Chat(ctx, req)
}

// TestResumeAfterStopMidToolChargesWhatItSpent (PR 4b r1 H2): a process stopped (SIGTERM, a
// deploy) while a subagent tool is in flight records what the tool spent, its aborted call at
// its worst-case bound, so the resumed run is charged that, not the tool's whole grant (every
// free dollar of the task), and goes on to finish instead of finishing from its reserve.
func TestResumeAfterStopMidToolChargesWhatItSpent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	prices, err := model.LoadPrices(`{"lead-m": {"in": 1, "out": 5}, "spec-m": {"in": 1, "out": 5}, "crit-m": {"in": 1, "out": 5}}`)
	if err != nil {
		t.Fatal(err)
	}
	cost := 0.001
	sc := f.happy()
	for k := range sc {
		for i := range sc[k] {
			sc[k][i].Usage = model.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: &cost}
		}
	}
	k := &subKiller{Fake: model.NewFake(sc), agent: "extract_facts"}
	e := f.reopen(k, prices)
	task := f.submit(0)
	k.kill = func() { e.active(task.ID).cancel() }
	f.step(context.Background())
	if st := f.steps(task.ID); !strings.HasSuffix(st, "4:tool_call:extract_facts:started") {
		t.Fatalf("first process: %s", st)
	}
	k.kill = nil
	f.reopen(k, prices)
	f.stale(task.ID)
	f.step(context.Background())
	v := f.get(task.ID)
	c, _ := f.e.Cost(context.Background(), task.ID)
	var lost agent.Usage
	f.st.DB().QueryRow(`SELECT lost FROM tasks WHERE id = $1`, task.ID).Scan(jsonScan{&lost})
	if stop(v) != "awaiting_gate/finished" || lost.Calls != 1 || lost.USD <= 0 || lost.USD > 0.1 || c.Summary.USD == nil || *c.Summary.USD > 1 {
		t.Fatalf("%s: lost %+v, cost %+v", stop(v), lost, c.Summary)
	}
}

// TestRejectedFindingStaysOutOfForcedDeliverable (PR 4b r1 M4, M5): a finding the lawyer rejected
// at its gate is out of the memo even when the run then finishes from its reserve with the
// section still citing it (the sentence is dropped and disclosed), and the Evidence Note counts it
// as rejected, not as a quote that failed verification.
func TestRejectedFindingStaysOutOfForcedDeliverable(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	lead := []model.Reply{calls(plan(issue("I1", "What does Greystone claim?", "Conduct"))), calls(extract(d1, "damages", "I1")), calls(draft("I1", "F1")),
		calls(committee()), calls(finishCall("submit"))}
	for range 20 {
		lead = append(lead, calls(finishCall("submit")))
	}
	f.engine(f.with(map[string][]model.Reply{
		"lead":          lead,
		"draft_section": {section("Greystone is the plaintiff. Greystone claims damages of {Zephyr} for unpaid invoices [F1].", "s")},
		"prosecute":     {text(m{"verdicts": []m{{"claim": "c", "verdict": "unsupported", "why": "no"}}})},
		"meta":          {metaNote, metaNote, metaNote},
	}), Config{BudgetCalls: 24, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"})
	task := f.submit(0)
	ctx := context.Background()
	f.step(ctx)
	if v := f.get(task.ID); stop(v) != "awaiting_gate/awaiting_gate" || len(v.PendingGates) != 1 || v.PendingGates[0].GateID != "G-F1" {
		t.Fatalf("paused: %s %+v", stop(v), v.PendingGates)
	}
	if _, err := f.e.Decide(ctx, task.ID, "G-F1", false, "The invoices are disputed."); err != nil {
		t.Fatal(err)
	}
	f.step(ctx)
	v := f.get(task.ID)
	out := deref(v.Output)
	body, _, _ := strings.Cut(out, "## Sources")
	if v.Status != "awaiting_gate" || strings.Contains(body, "$644,910") || strings.Contains(body, "[F1]") || !strings.Contains(body, "Greystone is the plaintiff.") {
		t.Fatalf("%s: the rejected finding reached the memo:\n%s", stop(v), out)
	}
	if strings.Contains(out, "0 findings were verified") || strings.Contains(out, "1 were quarantined") || !strings.Contains(out, "1 verified finding rejected by the lawyer at a gate is kept out of it too") ||
		!strings.Contains(out, "1 sentence citing a finding that is not usable (rejected at its gate or quarantined) was removed") {
		t.Fatalf("evidence note:\n%s", out)
	}
}
