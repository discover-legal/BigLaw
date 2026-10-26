// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	prices := testPrices(t)
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

// The lab-r3 verifier's repros (H1–H3, M1–M3), kept as regression tests: each failed on 1814a6a.

// TestDistinctPagesAreRead (M1): a page that differs from an earlier one only in its guarantor, or
// in a "not", is read; only an exact copy (case and white space aside) is skipped.
func TestDistinctPagesAreRead(t *testing.T) {
	body := func(who, verb string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "Guaranty.\n%s hereby %s guarantee to the Lender the punctual payment of $2,500,000 due under the Credit Agreement dated March 1, 2024. ", who, verb)
		ws := strings.Fields("lender borrower guarantor obligations continuing absolute waiver notice demand protest subrogation setoff collateral release amendment extension indulgence bankruptcy insolvency reinstatement")
		for i := range 30 {
			fmt.Fprintf(&b, "The %s and the %s clause covers %s, %s and %s without any further %s. ", ws[i%len(ws)], ws[(i*3+1)%len(ws)], ws[(i*5+2)%len(ws)], ws[(i*7+3)%len(ws)], ws[(i*11+4)%len(ws)], ws[(i*13+5)%len(ws)])
		}
		return b.String()
	}
	a := Visit{DocumentID: "d", SectionID: "s1", text: body("Alpha Holdings LLC", "irrevocably and unconditionally")}
	for _, b := range []Visit{
		{DocumentID: "d", SectionID: "s2", text: body("Beta Partners LP", "irrevocably and unconditionally")},
		{DocumentID: "d", SectionID: "s3", text: body("Alpha Holdings LLC", "does not")},
		{DocumentID: "d", SectionID: "s4", text: body("Alpha Holdings LLC", "irrevocably and unconditionally")[:400]}, // a part of it: read
	} {
		if why, _ := skipWhy(b, []Visit{a}); why != "" {
			t.Errorf("%s skipped as %s: %.140q", b.SectionID, why, b.text)
		}
	}
	copied := Visit{DocumentID: "d", SectionID: "s5", text: strings.ToUpper(strings.ReplaceAll(a.text, ". ", ".\n  "))}
	if why, src := skipWhy(copied, []Visit{a}); why != "duplicate" || src != 0 {
		t.Errorf("an exact copy (case, white space): %q %d", why, src)
	}
}

// TestDataTableIsNotBoilerplate (H3): a trade blotter (tab-separated, a share count last), a payment
// schedule with PDF spacing (a year last) and an amortization page with no words are read; a real
// table of contents (a Contents heading, or dot-leadered entries) is skipped; r3: rising numbers alone are read.
func TestDataTableIsNotBoilerplate(t *testing.T) {
	var tab, sp, nums, pipes, lead, rise strings.Builder
	tab.WriteString("Trade Date\tAccount\tSecurity\tSide\tShares\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&tab, "2021-02-%02d\tOceanic Fund I LP\tXYZ Corp\tBuy\t%d\n", i, 100*i)
		fmt.Fprintf(&sp, "Payment %d due under the Note      %d\n", i, 2024+i/12)
		fmt.Fprintf(&nums, "%02d/01/2025    $12,500.00    $%d,000.00\n", i%12+1, 500-i)
		fmt.Fprintf(&pipes, "| Payment %d | %d |\n", i, 10+i)
		fmt.Fprintf(&lead, "Article %d Definitions and interpretation ........ %d\n", i, 3)
		fmt.Fprintf(&rise, "Article %d Definitions and interpretation\t%d\n", i, 2*i+1)
	}
	for name, text := range map[string]string{"trade blotter": tab.String(), "payment schedule (PDF spacing)": sp.String(), "amortization page with no words": nums.String(), "a |-table": pipes.String(), "rising numbers, no Contents heading (r3: M4)": rise.String(),
		"a short contents batched with a clause": "Contents\n" + lead.String()[:300] + "\n2. Price\nThe price for the goods is $100,000, payable within thirty days of delivery.\n"} {
		if why, _ := skipWhy(Visit{DocumentID: "d", SectionID: "s9", text: text}, nil); why != "" {
			t.Errorf("%s skipped as %s", name, why)
		}
	}
	for name, text := range map[string]string{"dot leaders": lead.String(), "Contents heading": "Contents\nDefinitions\t1\nThe Loan\t4\nRepayment\t4\n", "blank": " — \n · \n"} {
		if why, _ := skipWhy(Visit{DocumentID: "d", SectionID: "s9", text: text}, nil); why != "boilerplate" {
			t.Errorf("%s: %q, want boilerplate", name, why)
		}
	}
}

// TestSkipOfUnreadPageIsUnread (M2): a page skipped as a copy of a page the sweep then failed to read
// is not covered either: its document is reported unread, to the lead and to join_discrepancies.
func TestSkipOfUnreadPageIsUnread(t *testing.T) {
	t.Parallel()
	shared := "The Seller represents that the warehouse lease expires on June 30, 2027, that no default has occurred under it, that the landlord has consented to the assignment, that the security deposit of $250,000 is held by the landlord, and that no amendment has been made to the lease since it was signed by both parties."
	f := newFixture(t, "1. Lease\n"+shared+"\n\n2. Other\nFurther text of the disclosure schedule follows here.\n", "1. Lease\n"+shared+"\n")
	fake := newFake(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "Is anything unusual?", "Conduct"))),
		calls(tc("sweep", m{"over": "sections", "documentIds": nil}))}, "extract_facts": {{Text: "not json"}, {Text: "not json"}, {Text: "not json"}, {Text: "not json"}}}))
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	sw := strings.Join(f.outputs(task.ID, "sweep"), "")
	ids, _ := f.e.Unread(context.Background(), task.ID)
	if !strings.Contains(sw, `"incomplete":true`) || !slices.Contains(ids, f.docs[1].ID) {
		t.Errorf("doc2 was never read (its page a copy of an unread page) yet is not reported unread: %v\nsweep: %s", ids, sw)
	}
}

// TestSkippedPagesReachLimitations (M3): the pages a sweep skipped are disclosed as a Limitations
// line (a Go-written note: finish puts every one in the memo's Limitations), with their count and
// documents, not only in the lead's tool output.
func TestSkippedPagesReachLimitations(t *testing.T) {
	t.Parallel()
	var toc strings.Builder
	toc.WriteString("Contents\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&toc, "Terms of the agreement, part %d ........ %d\n", i, i+2)
	}
	price := "2. Price\nThe price for the goods is $100,000, payable within thirty days of delivery.\n" + filler + "\n"
	f := newFixture(t, toc.String()+"\n"+price, price)
	var ex []model.Reply
	for range 6 {
		ex = append(ex, facts())
	}
	v := f.run(f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the price?", "Clause"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"extract_facts": ex}), Config{})
	notes, _ := f.e.rows(context.Background(), v.ID, kNote)
	var lim string
	for _, n := range notes {
		if n.Key == "sweep-skipped" {
			_ = json.Unmarshal(n.Data, &lim)
		}
	}
	if !strings.Contains(lim, "did not read 2 pages it judged exact duplicates of pages it read or boilerplate") || !strings.Contains(lim, f.docs[0].Name+" (1 of 2)") || !strings.Contains(lim, f.docs[1].Name+" (1 of 1)") {
		t.Errorf("skipped pages not disclosed: %q", lim)
	}
}

// TestSweepAdmitsOnlyThePlansFindingsOnce (bench5 SEC: 896 findings from 142 pages, 177 from one
// brochure section of generic process text, figures restated page after page): a visit is sent the
// plan's issues; a finding that names none is not admitted, findings repeating one admitted in the
// same issue (the same quote and values) merge into it, and a later page's restatement in another
// issue stays a finding of its own (lab-r4 verifier H2: only exact duplicates merge, never across
// issues; no per-call cap, M2).
func TestSweepAdmitsOnlyThePlansFindingsOnce(t *testing.T) {
	t.Parallel()
	doc := "1. Fees\nThe fee of $5,000 is due on March 1, 2026.\n" + filler + "\n\n2. Summary\nAs stated above, the fee of $5,000 is due on March 1, 2026.\n" + filler + "\n"
	f := newFixture(t, doc)
	d1 := f.docs[0].ID
	first := []m{on("I1", fd("The fee of $5,000 is due on March 1, 2026.", d1, "The fee of $5,000 is due on March 1, 2026", val("Buyer", "fee", "$5,000", "Money"), val("Buyer", "fee due", "March 1, 2026", "Date"))),
		fd("Further delivery terms apply.", d1, "Further delivery terms apply.")} // names no issue: boilerplate
	for i := range 13 {
		first = append(first, on("I2", fd(fmt.Sprintf("Delivery term %d applies.", i), d1, "Further delivery terms apply.")))
	}
	again := facts(on("I2", fd("The summary restates that the $5,000 fee falls due on March 1, 2026.", d1, "the fee of $5,000 is due on March 1, 2026",
		val("Buyer", "fee amount", "$5,000", "Money"), val("Buyer", "due date", "March 1, 2026", "Date"))))
	fake := newFake(f.with(map[string][]model.Reply{
		"lead":  {calls(plan(issue("I1", "When is the fee due?", "Clause"), issue("I2", "What delivery terms apply?", "Clause"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil}))},
		"page1": {facts(first...)}, "page2": {again},
	}))
	fake.Route = func(r model.Request) string {
		if r.Agent != "extract_facts" {
			return r.Agent
		}
		return map[bool]string{true: "page2", false: "page1"}[strings.Contains(sweepReq(r).SectionText, "As stated above")]
	}
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	fs := ledgerFindings(t, f, task.ID)
	if len(fs) != 3 || fs[0].IssueID != "I1" || fs[1].IssueID != "I2" || fs[2].IssueID != "I2" || len(fs[0].Citations) != 1 || !strings.Contains(fs[2].Claim, "summary restates") ||
		slices.ContainsFunc(fs, func(x Finding) bool { return x.Claim == "Further delivery terms apply." }) {
		t.Fatalf("%d findings: %+v", len(fs), fs)
	}
	for _, c := range fake.Calls() {
		if c.Agent == "extract_facts" && !strings.Contains(fmt.Sprint(c.Messages[1].Content), `"issues":[{"id":"I1","question":"When is the fee due?"},{"id":"I2","question":"What delivery terms apply?"}]`) {
			t.Errorf("a visit was not sent the plan's issues: %s", c.Messages[1].Content)
		}
	}
}

// secItem is page n's two long sentences (about 1.1 KB each): the bench5 SEC referral's paragraphs are
// that long, and a drafter's request carries every named finding's quotes whole.
func secItem(n, k int) string {
	return fmt.Sprintf("Item %d disclosure %c: the Division alleges that the Firm failed to disclose to its advisory clients, in its Form ADV brochure and in its client communications for the period under review, the conflict of interest described in this paragraph, number %d, which arose from the allocation of omnibus trades, the directed brokerage arrangement and the personal accounts of its supervised persons, ", n, 'a'+k, n) +
		strings.Repeat(fmt.Sprintf("and the record for item %d shows the same pattern of allocation across the Review Period and the accounts concerned, ", n), 6) + "as the examination staff found."
}

// secShaped answers a SEC-shaped run by what each request holds, billed at bench5's measured costs
// per call (SEC referral at $11: a lead turn $0.026, a sweep visit $0.014, a drafter $0.026, a
// committee cycle $0.41 over its three critics, the coverage pass $0.52 for three analyses and their
// redrafts, so an analysis about $0.15).
type secShaped struct {
	mu   sync.Mutex
	lead []model.Reply
	n    int
	reqs []model.Request
}

var itemRe = regexp.MustCompile(`Item (\d+) disclosure a`)

var fidListRe = regexp.MustCompile(`"fid":"(F\d+)"`)

func (s *secShaped) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	usd := map[string]float64{"lead": 0.026, "extract_facts": 0.014, "draft_section": 0.026, "analyze_issue": 0.15, "prosecute": 0.14, "reproduce": 0.14,
		"audit_gaps": 0.13, "meta": 0.002, "section_check": 0.005, "read_figures": 0.003}[req.Agent]
	body := fmt.Sprint(req.Messages[len(req.Messages)-1].Content)
	var r model.Reply
	switch req.Agent {
	case "lead":
		s.mu.Lock()
		if s.n >= len(s.lead) {
			s.mu.Unlock()
			return nil, fmt.Errorf("%w: the SEC-shaped lead has no more turns", model.ErrScriptExhausted)
		}
		r = s.lead[s.n]
		s.n++
		s.mu.Unlock()
	case "extract_facts":
		var in struct{ DocumentID, SectionText string }
		jsonDecode(body, &in)
		fs := []m{}
		for _, x := range itemRe.FindAllStringSubmatch(in.SectionText, -1) {
			var n int
			fmt.Sscan(x[1], &n)
			fs = append(fs, m{"issueId": fmt.Sprintf("I%d", n%6+1), "claim": fmt.Sprintf("The Firm failed to disclose conflict %d to its clients.", n),
				"quotes": []m{{"documentId": in.DocumentID, "quote": secItem(n, 0), "locator": nil}, {"documentId": in.DocumentID, "quote": secItem(n, 1), "locator": nil}},
				"values": []m{}, "entities": []m{}, "relations": []m{}, "confidence": 0.9})
		}
		r = text(m{"findings": fs, "gaps": []string{}})
	case "draft_section":
		var cites []string
		for _, x := range fidListRe.FindAllStringSubmatch(strings.ReplaceAll(body, " ", ""), -1) {
			cites = append(cites, "["+x[1]+"]")
		}
		r = section("The record shows these failures to disclose "+strings.Join(cites, " ")+".", "the failures to disclose")
	case "analyze_issue":
		r = analysed("Section 206(2) reaches negligent nondisclosure of a conflict (general knowledge, not the record).", "The Firm did not disclose the conflicts.", "The nondisclosures support a 206(2) claim.")
	case "prosecute":
		r = text(m{"verdicts": []m{}})
	case "reproduce":
		r = noAnswers
	case "audit_gaps":
		r = noGaps
	case "read_figures": // the figure reader's stand-in (figreader_test.go), at a small critic call's cost
		r, _ = readerOracle(req)
	default:
		r = metaNote
	}
	resp := &model.Response{Model: req.Model, Text: r.Text, Usage: model.Usage{InputTokens: 2000, OutputTokens: 400, CostUSD: &usd}}
	for i, c := range r.ToolCalls {
		c.ID, c.Type = fmt.Sprintf("%s-%d-%d", req.Agent, s.n, i), "function"
		resp.ToolCalls = append(resp.ToolCalls, c)
	}
	return resp, nil
}

// TestSECShapedRunAnalysesAndPlacesWithin11 (bench5 SEC: every stage budget was hit, the coverage
// pass analysed three issues and then had $0.14 for a placement redraft whose worst case is more, so
// 296 findings were disclosed and none placed): a run shaped like it, at $11 and 900 calls, the same
// plan (three RedFlag issues, two Conduct, one Requirement), 142 pages swept, a lead that searches,
// drafts and reruns the committee until the budget stops it, priced per call as the bench measured.
// The coverage pass analyses every law issue the lead left touched and still places the strongest
// left-out findings, every issue is drafted, and the run spends no more than $11.
func TestSECShapedRunAnalysesAndPlacesWithin11(t *testing.T) {
	t.Parallel()
	var docTexts []string
	n := 0
	for _, pages := range []int{60, 40, 42} {
		var b strings.Builder
		for i := 1; i <= pages; i++ {
			n++
			fmt.Fprintf(&b, "%d. Allegation %d\n%s\n%s\n\n", i, n, secItem(n, 0), secItem(n, 1))
		}
		docTexts = append(docTexts, b.String())
	}
	f := newFixture(t, docTexts...)
	search := func(q string) model.Reply {
		return calls(tc("retrieve", m{"op": "search", "query": q, "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil}))
	}
	lead := []model.Reply{calls(plan(issue("I1", "What are the specific allegations against the Firm?", "RedFlag"), issue("I2", "What evidence supports each allegation?", "RedFlag"),
		issue("I3", "What compliance violations are identified?", "Conduct"), issue("I4", "What are the key defense priorities?", "Conduct"),
		issue("I5", "What quantitative findings does the analysis reveal?", "RedFlag"), issue("I6", "What is the chronology of relevant events?", "Requirement"))),
		calls(tc("sweep", m{"over": "sections", "documentIds": nil}))}
	for i := range 30 {
		lead = append(lead, search(fmt.Sprintf("disclosure conflict allocation item %d", i)))
	}
	for range 12 { // draft every issue, review, again: the bench lead's loop
		for k := 1; k <= 6; k++ {
			lead = append(lead, calls(draft(fmt.Sprintf("I%d", k), fmt.Sprintf("F%d", k), fmt.Sprintf("F%d", k+6))))
		}
		lead = append(lead, calls(committee()))
		for i := range 4 {
			lead = append(lead, search(fmt.Sprintf("failed to disclose %d", i)))
		}
	}
	chat := &secShaped{lead: lead}
	prices := testPrices(t)
	cfg := Config{BudgetCalls: 900, BudgetUSD: 11, CollapseThreshold: 0.5, CollapseAction: "strict"}
	f.e = New(f.st, f.d, chat, testRoles, prices, cfg, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	c, _ := f.e.Cost(context.Background(), task.ID)
	cov, _ := f.e.coverRow(context.Background(), task.ID)
	placed := 0
	for _, r := range chat.reqs {
		placed += btoi(r.Agent == "draft_section" && strings.Contains(fmt.Sprint(r.Messages[1].Content), "These findings never reached the memo"))
	}
	secs := ledgerSections(t, f, task.ID)
	if n := strings.Count(deref(v.Output), "Coverage:"); n > 8 {
		t.Errorf("%d Coverage lines in the deliverable", n)
	}
	if c.Summary.USD == nil || *c.Summary.USD > 11 || cov == nil || strings.Join(cov.Analysed, ",") != "I3,I4,I6" || len(cov.Missing) != 0 || placed == 0 || len(secs) != 6 ||
		!strings.HasPrefix(stop(v), "awaiting_gate") {
		cs, _ := json.Marshal(cov)
		t.Fatalf("%s: cost %+v, cover %s, %d placement redrafts, %d sections\n%s", stop(v), c.Summary, cs, placed, len(secs), f.steps(task.ID))
	}
	t.Logf("spent $%.2f in %d calls; cover %+v (%d left out); %d placement redrafts", *c.Summary.USD, c.Summary.Calls, cov.Analysed, len(cov.LeftOut), placed)
}

func ledgerSections(t *testing.T, f *fixture, id string) []Section {
	t.Helper()
	ss, err := load[Section](context.Background(), f.e, id, kSection)
	if err != nil {
		t.Fatal(err)
	}
	return ss
}
