// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/model"
)

// sweepReq is an extract_facts request as the subagent got it.
func sweepReq(req model.Request) (in struct{ SectionText, Header, SectionID string }) {
	jsonDecode(fmt.Sprint(req.Messages[1].Content), &in)
	return in
}

var lotRe = regexp.MustCompile(`deliver lot (\d+)`)

// byLot answers each extract_facts visit from the script of the lot its page states ("lot3"), so
// the replies do not depend on the order visits arrive in.
func byLot(req model.Request) string {
	if req.Agent != "extract_facts" {
		return req.Agent
	}
	if m := lotRe.FindStringSubmatch(sweepReq(req).SectionText); m != nil {
		return "lot" + m[1]
	}
	return req.Agent
}

// lots is a document of n clauses, each over half a sweep batch (a visit of its own), and the
// scripts that answer each clause's visit with its one finding (reps times).
func lots(n, reps int) (string, func(docID string) map[string][]model.Reply) {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d. Clause %d\nThe supplier shall deliver lot %d on time.\n%s\n\n", i, i, i, filler)
	}
	return b.String(), func(docID string) map[string][]model.Reply {
		out := map[string][]model.Reply{}
		for i := 1; i <= n; i++ {
			for range reps {
				out[fmt.Sprintf("lot%d", i)] = append(out[fmt.Sprintf("lot%d", i)], facts(on("I1", fd(fmt.Sprintf("Lot %d is due on time.", i), docID, fmt.Sprintf("The supplier shall deliver lot %d on time", i)))))
			}
		}
		return out
	}
}

// TestSweepPagesFitTheAnswer (bench FOLLOWUPS L-1, real Haiku on the SEC referral: a 2,000-token
// page of dense text overflowed the 8,192-token answer, so the visit failed and its section went
// unread): long sections are paged at sweepTokens, a sheet in whole rows (at most
// docs.TablePageRows) with its header row by reference, so every visit's answer fits and the sweep
// completes. The scripted model answers like the real one: a page over ~650 tokens is cut off.
func TestSweepPagesFitTheAnswer(t *testing.T) {
	t.Parallel()
	var prose, sheet strings.Builder
	prose.WriteString("1. Allegations\n")
	for i := range 90 {
		fmt.Fprintf(&prose, "On day %d the Firm allocated block trade %d to Oceanic Fund I LP at a profit of $%d. ", i, i, 1000+i)
	}
	head := "Date\tAccount\tShares\tProfit"
	sheet.WriteString("## Sheet: Trades\n" + head + "\n")
	for i := range 150 {
		fmt.Fprintf(&sheet, "2023-%02d-%02d\tOceanic-%d\t%d\t$%d.00\n", 1+i%12, 1+i%28, i, 100+i, 7*i)
	}
	f := newFixture(t, prose.String(), sheet.String())
	over := []model.Reply{}
	for range 40 {
		over = append(over, model.Reply{Truncated: true}) // the answer cut off before any text
	}
	pages := []model.Reply{}
	for range 40 {
		pages = append(pages, facts())
	}
	e, fake := f.engine(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Allegations?", "Conduct"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": pages, "overflow": over}), Config{})
	fake.Route = func(req model.Request) string {
		if req.Agent == "extract_facts" && strings.Count(sweepReq(req).SectionText, "")*2/7 > sweepTokens+50 {
			return "overflow"
		}
		return req.Agent
	}
	task := f.submit(0)
	f.step(context.Background())
	out := strings.Join(f.outputs(task.ID, "sweep"), "")
	if !strings.Contains(out, `"incomplete":false`) {
		t.Fatalf("sweep %s", out)
	}
	headed := 0
	for _, c := range fake.Calls() {
		if c.Agent != "extract_facts" {
			continue
		}
		in := sweepReq(c)
		if rows := strings.Count(in.SectionText, "\t") / 3; rows > docs.TablePageRows {
			t.Errorf("a page of %d rows", rows)
		}
		if in.Header != "" {
			headed++
			if in.Header != head || strings.Contains(in.SectionText, head) {
				t.Errorf("header %q on a page starting %q", in.Header, in.SectionText[:20])
			}
		}
	}
	if headed < 2 || e == nil {
		t.Fatalf("%d later sheet pages named the header row", headed)
	}
}

// pool is test plumbing around model.Fake: the first sweepWorkers extract_facts calls wait until
// they are all in flight at once (or fail after two seconds), the visit of lot 1 answers last, and
// the most calls in flight at once is kept.
type pool struct {
	*model.Fake
	mu            sync.Mutex
	now, peak, in int
	all           chan struct{}
}

func (p *pool) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	if req.Agent != "extract_facts" {
		return p.Fake.Chat(ctx, req)
	}
	p.mu.Lock()
	p.now++
	p.peak = max(p.peak, p.now)
	if p.in++; p.in == sweepWorkers {
		close(p.all)
	}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.now--; p.mu.Unlock() }()
	select {
	case <-p.all:
	case <-time.After(2 * time.Second):
		return nil, errors.New("the sweep's visits ran one at a time")
	}
	if byLot(req) == "lot1" {
		time.Sleep(150 * time.Millisecond)
	}
	return p.Fake.Chat(ctx, req)
}

// TestSweepReadsInParallelInPlanOrder (bench: 51 visits one at a time took 40–55 minutes): the
// sweep reads sweepWorkers visits at once, never more, yet the ledger is a sequential sweep's: the
// slowest visit (lot 1, answering last) still holds F1, every finding and visit record is in plan
// order, and the sweep's output lists the fids in plan order.
func TestSweepReadsInParallelInPlanOrder(t *testing.T) {
	t.Parallel()
	doc, scripts := lots(8, 1)
	f := newFixture(t, doc)
	sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Deliveries?", "Requirement"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))}})
	for k, v := range scripts(f.docs[0].ID) {
		sc[k] = v
	}
	p := &pool{Fake: newFake(sc), all: make(chan struct{})}
	p.Route = byLot
	f.e = New(f.st, f.d, p, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	out := strings.Join(f.outputs(task.ID, "sweep"), "")
	if !strings.Contains(out, `"visited":8,"findings":["F1","F2","F3","F4","F5","F6","F7","F8"]`) || !strings.Contains(out, `"incomplete":false`) || p.peak != sweepWorkers || sweepWorkers < 2 {
		t.Fatalf("peak %d; sweep %s", p.peak, out)
	}
	fs, _ := load[Finding](context.Background(), f.e, task.ID, kFinding)
	vs, _ := f.e.rows(context.Background(), task.ID, kVisit)
	visits, _ := f.e.plan(context.Background(), f.ids(), f.ids())
	for i := range 8 {
		if fs[i].FID != fmt.Sprintf("F%d", i+1) || fs[i].Claim != fmt.Sprintf("Lot %d is due on time.", i+1) || vs[i].Key != visits[i].key() {
			t.Fatalf("finding %d is %s %q, visit record %s (want %s)", i+1, fs[i].FID, fs[i].Claim, vs[i].Key, visits[i].key())
		}
	}
}

// countKill is test plumbing around model.Fake: the kill-th extract_facts call stops the process
// (it never reaches the Fake).
type countKill struct {
	*model.Fake
	mu    sync.Mutex
	n, at int
	kill  func()
}

func (k *countKill) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	k.mu.Lock()
	k.n += btoi(req.Agent == "extract_facts")
	hit := req.Agent == "extract_facts" && k.n == k.at && k.kill != nil
	k.mu.Unlock()
	if hit {
		k.kill()
		return nil, context.Canceled
	}
	return k.Fake.Chat(ctx, req)
}

// TestSweepResumesAfterKillMidSweep (G10): a process stopped in the middle of a parallel sweep
// commits none of it; the next process re-issues the sweep, and the run's ledger equals an
// uninterrupted run's.
func TestSweepResumesAfterKillMidSweep(t *testing.T) {
	t.Parallel()
	doc, scripts := lots(8, 2)
	runIt := func(killAt int) (*fixture, Task) {
		f := newFixture(t, doc)
		d1 := f.docs[0].ID
		sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Deliveries?", "Requirement"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
			"draft_section": {section("Lot 1 is due on time [F1].", "s")}})
		for k, v := range scripts(d1) {
			sc[k] = v
		}
		k := &countKill{Fake: newFake(sc), at: killAt}
		k.Route = byLot
		e := f.reopen(k, nil)
		task := f.submit(0)
		if killAt > 0 {
			k.kill = func() { e.active(task.ID).cancel() }
			f.step(context.Background())
			if st := f.steps(task.ID); !strings.HasSuffix(st, "4:tool_call:sweep:started") {
				t.Fatalf("first process: %s", st)
			}
			k.kill = nil
			f.reopen(k, nil)
			f.stale(task.ID)
		}
		f.step(context.Background())
		return f, task
	}
	ref, want := runIt(0)
	f, task := runIt(3)
	v := f.get(task.ID)
	norm := func(f *fixture, id string) string {
		var b strings.Builder
		for _, r := range f.ledger(id) {
			if r.Kind == kFinding || r.Kind == kVisit {
				b.WriteString(r.Kind + " " + r.Key + " " + strings.ReplaceAll(r.Data, f.docs[0].ID, "D1") + "\n")
			}
		}
		return strings.ReplaceAll(b.String(), f.docs[0].ID, "D1")
	}
	if stop(v) != "awaiting_gate/finished" || norm(f, task.ID) != norm(ref, want.ID) {
		t.Fatalf("%s: resumed ledger\n%s\nwant\n%s", stop(v), norm(f, task.ID), norm(ref, want.ID))
	}
	var lost json.RawMessage
	f.st.DB().QueryRow(`SELECT lost FROM tasks WHERE id = $1`, task.ID).Scan(&lost)
	if !strings.Contains(string(lost), `"calls"`) {
		t.Fatalf("the stopped sweep's spend was not charged: %s", lost)
	}
}

// TestSweepFallsBackToSerialWhenAShareCannotFundACall (lab-improve r2 verifier M2): on a USD-bound
// run whose extractor's worst-case call costs more than a worker's quarter of the sweep's USD (but
// less than all of it), the sweep reads every page one at a time, as a serial sweep would, and
// counts only the visits that ran.
func TestSweepFallsBackToSerialWhenAShareCannotFundACall(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&doc, "%d. Clause %d\nThe supplier shall deliver lot %d on time.\n%s\n\n", i, i, i, filler)
	}
	f := newFixture(t, doc.String())
	prices, err := model.LoadPrices(`{"lead-m": {"in": 3, "out": 15}, "spec-m": {"in": 3, "out": 15}, "crit-m": {"in": 3, "out": 15}}`)
	if err != nil {
		t.Fatal(err)
	}
	var ex []model.Reply
	for range 8 {
		ex = append(ex, costly(facts(), 0.01))
	}
	scripts := f.with(map[string][]model.Reply{
		"lead":          {calls(plan(issue("I1", "Deliveries?", "Requirement"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": ex,
	})
	f.e = New(f.st, f.d, newFake(scripts), testRoles, prices, Config{BudgetCalls: 150, BudgetUSD: 1.5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil) // the sweep's $0.30 (its reader's share beside it)
	task := f.submit(0)
	f.step(context.Background())
	sw := strings.Join(f.outputs(task.ID, "sweep"), "")
	if !strings.Contains(sw, `"visited":8,`) || !strings.Contains(sw, `"incomplete":false`) {
		t.Fatalf("sweep %s", sw)
	}
	if vs, _ := f.e.rows(context.Background(), task.ID, kVisit); len(vs) != 8 {
		t.Fatalf("%d visit records, want 8", len(vs))
	}
}

// TestHardCrashChargesOnlyRecordedSpend (bench r3: a container restart mid-sweep charged the
// sweep's whole $6.37 grant as lost, so the resumed run had no evidence budget and the re-issued
// sweep was refused): each model call inside a tool's step is journaled as it happens, so a process
// that dies without a word (its lease gone, its last writes refused) is charged only what its calls
// spent plus the worst case of those in flight; the next process re-issues the sweep and reads
// every page.
func TestHardCrashChargesOnlyRecordedSpend(t *testing.T) {
	t.Parallel()
	doc, scripts := lots(8, 2)
	f := newFixture(t, doc)
	sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Deliveries?", "Requirement"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
		calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"draft_section": {section("Lot 1 is due on time [F1].", "s")}})
	for k, v := range scripts(f.docs[0].ID) {
		for i := range v {
			v[i] = costly(v[i], 0.01)
		}
		sc[k] = v
	}
	prices, err := model.LoadPrices(`{"lead-m": {"in": 1, "out": 1}, "spec-m": {"in": 1, "out": 1}, "crit-m": {"in": 1, "out": 1}}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{BudgetCalls: 150, BudgetUSD: 1, CollapseThreshold: 0.5, CollapseAction: "strict"}
	k := &countKill{Fake: newFake(sc), at: 3}
	k.Route = byLot
	f.e = New(f.st, f.d, k, testRoles, prices, cfg, nil)
	task := f.submit(0)
	k.kill = func() { // the process dies: another holds the lease, so nothing it says is recorded
		if _, err := f.st.DB().Exec(`UPDATE tasks SET lease_owner = 'dead' WHERE id = $1`, task.ID); err != nil {
			t.Error(err)
		}
		f.e.active(task.ID).cancel()
	}
	f.step(context.Background())
	var held agent.Grant
	if err := f.st.DB().QueryRow(`SELECT grant_held FROM task_steps WHERE task_id = $1 AND tool = 'sweep' AND status = 'started'`, task.ID).Scan(jsonScan{&held}); err != nil {
		t.Fatalf("the crashed sweep's started step: %v (%s)", err, f.steps(task.ID))
	}
	k.kill = nil
	f.e = New(f.st, f.d, k, testRoles, prices, cfg, nil)
	f.stale(task.ID)
	f.step(context.Background())
	var lost agent.Usage
	f.st.DB().QueryRow(`SELECT lost FROM tasks WHERE id = $1`, task.ID).Scan(jsonScan{&lost})
	a := Allocate(cfg.BudgetCalls, cfg.BudgetUSD)
	worst := 0.03 // one visit's worst case at these prices is about $0.02
	v := f.get(task.ID)
	sw := strings.Join(f.outputs(task.ID, "sweep"), "")
	if lost != (agent.Usage{Calls: held.Calls, USD: held.USD}) || lost.Calls < 2 || lost.Calls > 2+sweepWorkers || lost.USD < 0.02 || lost.USD > 0.02+sweepWorkers*worst ||
		lost.USD >= a.dollars(a.Sweep)/2 {
		t.Fatalf("lost %+v (journaled %+v; the sweep's share is $%.2f)", lost, held, a.dollars(a.Sweep))
	}
	if !strings.Contains(sw, `"visited":8`) || !strings.Contains(sw, `"incomplete":false`) || stop(v) != "awaiting_gate/finished" {
		t.Fatalf("%s: resumed sweep %s\n%s", stop(v), sw, f.steps(task.ID))
	}
}

// TestSweepReadsRelevantPagesFirst (bench r3, SEC: the share ran out a third of the way through 169
// pages read in document order): the sweep reads the pages the plan's issues name first, so a
// budget that funds a few visits of eight reads the one page about the issue (document order
// would have read lots 1 to 4); each visit is one call
// on its page alone (no tools offered), and the unread pages and their document are reported.
func TestSweepReadsRelevantPagesFirst(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 8; i++ {
		body := fmt.Sprintf("The supplier shall deliver lot %d on time.", i)
		if i == 7 {
			body = "Lot 7 ships by barge from the Rotterdam terminal under the barge charter."
		}
		fmt.Fprintf(&doc, "%d. Clause %d\n%s\n%s\n\n", i, i, body, filler)
	}
	f := newFixture(t, doc.String())
	prices, err := model.LoadPrices(`{"lead-m": {"in": 1, "out": 5}, "spec-m": {"in": 1, "out": 5}, "crit-m": {"in": 1, "out": 5}}`)
	if err != nil {
		t.Fatal(err)
	}
	var ex []model.Reply
	for range 8 {
		ex = append(ex, costly(facts(), 0.3))
	}
	fake := newFake(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Which lot ships by barge, and from which terminal?", "Conduct"))),
		calls(tc("sweep", m{"over": "sections", "documentIds": nil}))}, "extract_facts": ex}))
	f.e = New(f.st, f.d, fake, testRoles, prices, Config{BudgetCalls: 150, BudgetUSD: 2, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	var read []string
	for _, c := range fake.Calls() {
		if c.Agent == "extract_facts" {
			read = append(read, sweepReq(c).SectionText)
			if len(c.Tools) != 0 {
				t.Errorf("a sweep visit was offered %d tools", len(c.Tools))
			}
		}
	}
	sw := strings.Join(f.outputs(task.ID, "sweep"), "")
	if len(read) == 0 || len(read) > 4 || !strings.Contains(strings.Join(read, ""), "barge") || !strings.Contains(sw, `"incomplete":true`) ||
		!strings.Contains(sw, `"planned":8,"documentId":"`+f.docs[0].ID+`"`) {
		t.Fatalf("read %d pages (%.60q); sweep %s", len(read), read, sw)
	}
	unswept, err := f.e.unswept(context.Background(), task.ID)
	ids, uerr := f.e.Unread(context.Background(), task.ID) // what join_discrepancies names
	if err != nil || uerr != nil || len(unswept) != 1 || unswept[0].Planned != 8 || unswept[0].Read != len(read) || len(ids) != 1 || ids[0] != f.docs[0].ID {
		t.Fatalf("recorded coverage %+v %v", unswept, err)
	}
}

// TestSweepSkipsDuplicatesAndBoilerplate: a table of contents and a page another document already
// holds word for word are not read, and are reported as skipped; a page that differs from another
// only by a figure is read (a figure is what the cross-document checks compare).
func TestSweepSkipsDuplicatesAndBoilerplate(t *testing.T) {
	t.Parallel()
	var toc strings.Builder
	toc.WriteString("Contents\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&toc, "Terms of the agreement, part %d ........ %d\n", i, i+2)
	}
	price := func(n int, amount string) string {
		return fmt.Sprintf("%d. Price\nThe price for the goods is %s, payable within thirty days of delivery.\n%s\n\n", n, amount, filler)
	}
	f := newFixture(t, toc.String()+"\n"+price(2, "$100,000"), price(2, "$100,000")+price(3, "$120,000"))
	var ex []model.Reply
	for range 8 {
		ex = append(ex, facts())
	}
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the price?", "Clause"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": ex}), Config{})
	var read []string
	for _, c := range f.e.chat.(*model.Fake).Calls() {
		if c.Agent == "extract_facts" {
			read = append(read, sweepReq(c).SectionText)
		}
	}
	sw := strings.Join(f.outputs(v.ID, "sweep"), "")
	joined := strings.Join(read, "\n")
	if len(read) != 2 || strings.Contains(joined, "........") || strings.Count(joined, "$100,000") != 1 || !strings.Contains(joined, "$120,000") ||
		strings.Count(sw, `"reason":"boilerplate"`) != 1 || strings.Count(sw, `"reason":"duplicate"`) != 1 || !strings.Contains(sw, `"incomplete":false`) || strings.Contains(sw, `"unreadDocuments":[{`) {
		t.Fatalf("read %q; sweep %s", read, sw)
	}
}
