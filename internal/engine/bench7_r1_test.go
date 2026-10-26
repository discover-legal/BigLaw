// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// bench7 r1 (verifier 1): the section's reader call SUCCEEDS but gives the memo span no label. The memo's
// "$2,000,000 paid" (money) cites a finding whose quote the reader read clearly as a 2,000,000
// share COUNT. At cfa9fcb this was a blocking G8 line ("not traced"); at ef9e6e4 the omitted
// label is "not read: the figure reader gave it no label", which matches limitedMemoUnread's
// HasPrefix("not read") and the figure is delivered, traced as written by digits, with
// Limitations text claiming the reader's call "could not be funded or failed".
func TestUnlabelledMemoSpanIsNoCallFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Issuance\nThe Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.\n")
	d1 := f.docs[0].ID
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Company shall issue 2,000,000 shares to the Investor.", d1, "The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor",
			val("Company", "shares issued", "2,000,000", "Count")))},
		"draft_section": {section("The Investor paid $2,000,000 for its shares [F1].", "Paid [F1].")},
	}), Config{})
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind == "memo" {
			return model.Reply{Text: `{"labels":[]}`}, true // answered, but labelled nothing
		}
		return readerOracle(req)
	}
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	fs := ledgerFindings(t, f, v.ID)
	out := deref(v.Output)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	t.Logf("stop %s; F1 figures %+v", stop(v), fs[0].Figures)
	t.Logf("finish:\n%s", fin)
	t.Logf("output:\n%s", out)
	if strings.Contains(out, "$2,000,000") && !strings.Contains(fin, "not traced") {
		t.Errorf("memo money figure with no label delivered, traced by digits to a share count (stop %s); gated=%v",
			stop(v), slices.ContainsFunc(v.PendingGates, func(g Gate) bool { return g.GateID == "G-F1" }))
	}
	if strings.Contains(out, "its call could not be funded or failed") {
		t.Errorf("disclosure says the call could not be funded or failed, but it answered")
	}
}

// bench7 r1 (verifier 2): bench5 F388 again, but stating no typed value (the very shape claimSums exists for: the
// $103,800.00 sum in its claim and quote, typed nowhere). At ef9e6e4 such a finding is admitted
// unread and read only when a section cites it, so its claim sum never exists and finish's join
// never pairs $92,600 with $103,800.00 unless the memo happens to cite F2.
func TestValuelessClaimSumReachesTheJoin(t *testing.T) {
	t.Parallel()
	f := newFixture(t, compRef, compRows)
	f.parties = map[string]string{"92,600": "Marcus T. Bellini", "103,800.00": "Marcus T. Bellini"}
	f.measures = map[string]string{"92,600": "undisclosed compensation paid for Bellini", "103,800.00": "undisclosed compensation paid for Bellini"}
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	f752 := fd("Bellini received undisclosed compensation from Ostrowski totaling approximately $92,600 in connection with a directed brokerage arrangement with Lakeshore Trading.", d1,
		"The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600 ($45,000 initiation fee + $47,600 in monthly dues = $92,600).",
		val("Bellini", "total undisclosed compensation", "$92,600", "Money"))
	f388 := fd("Kevin Ostrowski's personal checking account at Coastal Commerce Bank was used to make payments totaling $103,800.00 to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini between May 3, 2022 and January 2, 2024.", d2,
		"Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini | 05/03/2022 – 01/02/2024 | 22 | | $103,800.00")
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What compensation did Bellini receive?", "RedFlag"))), calls(extract(d1, "compensation", "I1")), calls(extract(d2, "payments", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
		"extract_facts": {facts(f752), facts(f388)},
		"draft_section": {section("Bellini received about $92,600 [F1].", "s")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "")
	t.Logf("F2 unread=%v values=%v", fs[1].Unread, fs[1].Values)
	if !strings.Contains(fin, "[F1] $92,600 vs [F2] $103,800.00") {
		t.Errorf("the bench5 conflict is no longer proposed when F388 states no typed value (F2 unread=%v)", fs[1].Unread)
	}
}

// bench7 r1 (verifier 3): free-text "decimal" now admits "" (the schema dropped its enum, no minLength); Go's
// pre-existing branch reads "" as "none", so a label that never says which mark is the decimal
// point reads "1.200" as one thousand two hundred, clearly.
func TestEmptyDecimalIsUnclear(t *testing.T) {
	p := analysis.PassageOf("q1", "The fee is 1.200 per unit.")
	l := lab("q1.s1", analysis.KindCount, "", "1", "", "", "")
	l.Decimal = ""
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, []analysis.Label{l})
	r := rs["q1.s1"]
	t.Logf("%+v", r)
	if !r.Unclear {
		t.Errorf("decimal \"\" read clearly as amount %s", r.Amount)
	}
}

// bench7 r1 (verifier 6): the span's-own-zeros scale rule only ever lowers to scale 1; ×1000 needs words outside the span.
func TestOwnZerosScaleNeverMultiplies(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, c := range []struct {
		text, from, scale, want string
		unclear                 bool
	}{
		{"The fee was $5,000.", "000", "1000", "5000", false},
		{"The fee was $5,000.", ",000", "1000000", "5000", false},
		{"The fee was $5,000.", "5,000", "1000", "5000", true},
		{"The fee was $5,000 000 total.", "000", "1000", "", false},  // a second 000 outside the span: Go cannot tell
		{"Amounts ($000)\nFee\t$5,000", "000", "1000", "5000", true}, // the header states "000" too: which it read Go cannot tell, so unclear
		{"The fee was $15,000.", "000", "1000", "15000", false},
	} {
		p := passageQ1(c.text)
		id := p.ID + ".s1"
		for i, sp := range p.Spans { // the fee's own span (a header's "$000" is a span too)
			if strings.HasSuffix(sp.Text, ",000") {
				id = analysis.SpanID(p.ID, i)
				break
			}
		}
		l := lab(id, analysis.KindMoney, "USD", c.scale, "", "", "")
		l.ScaleFrom = str(c.from)
		rs, _ := analysis.ReadLabels([]analysis.Passage{p}, []analysis.Label{l})
		r := rs[id]
		t.Logf("%q from %q x%s -> amount %s scale %q unclear %v (%s)", c.text, c.from, c.scale, r.Amount, r.Scale, r.Unclear, r.Why)
		if c.want != "" && (r.Amount != c.want || r.Unclear != c.unclear) {
			t.Errorf("%q: %+v", c.text, r)
		}
	}
}

func passageQ1(text string) analysis.Passage { return analysis.PassageOf("q1", text) }

// killAtCall stops the process at the at-th call of one agent (it never reaches the Fake).
type killAtCall struct {
	*model.Fake
	mu    sync.Mutex
	agent string
	n, at int
	kill  func()
}

func (k *killAtCall) Chat(ctx context.Context, req model.Request) (*model.Response, error) {
	k.mu.Lock()
	k.n += btoi(req.Agent == k.agent)
	hit := req.Agent == k.agent && k.n == k.at && k.kill != nil
	k.mu.Unlock()
	if hit {
		k.kill()
		return nil, context.Canceled
	}
	return k.Fake.Chat(ctx, req)
}

// bench7 r1 (verifier 5a): a process killed at the sweep's end-of-sweep batched reader call commits none of the
// sweep; the resumed run's findings (values, figures, unread flags) and reader stats equal an
// uninterrupted run's.
func TestResumeAtSweepEndRead(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&doc, "%d. Lot %d\nThe supplier shall be paid $%d,000 for lot %d.\n%s\n\n", i, i, 100+i, i, filler)
	}
	runIt := func(killAt int) (*fixture, Task) {
		f := newFixture(t, doc.String())
		d1 := f.docs[0].ID
		sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the supplier paid?", "Conduct"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
			"draft_section": {section("Lot 1 costs $101,000 [F1].", "s")}})
		for range 2 {
			for i := 1; i <= 6; i++ {
				q := fmt.Sprintf("The supplier shall be paid $%d,000 for lot %d", 100+i, i)
				k := fmt.Sprintf("lot%d", i)
				sc[k] = append(sc[k], facts(on("I1", fd(fmt.Sprintf("Lot %d costs $%d,000.", i, 100+i), d1, q, val("Supplier", fmt.Sprintf("lot %d price", i), fmt.Sprintf("$%d,000", 100+i), "Money")))))
			}
		}
		k := &killAtCall{Fake: newFake(sc), agent: "read_figures", at: killAt}
		k.Route = func(req model.Request) string {
			if req.Agent != "extract_facts" {
				return req.Agent
			}
			if m := regexp.MustCompile(`for lot (\d+)\.`).FindStringSubmatch(sweepReq(req).SectionText); m != nil {
				return "lot" + m[1]
			}
			return req.Agent
		}
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
	f, task := runIt(1)
	norm := func(f *fixture, id string) string {
		var b strings.Builder
		for _, r := range f.ledger(id) {
			if r.Kind == kFinding || r.Kind == kReadStat {
				d := r.Data
				if r.Kind == kReadStat { // usage may differ by lost-call accounting; keep counts
					var st ReaderStats
					_ = json.Unmarshal([]byte(d), &st)
					d = fmt.Sprintf("calls=%d failed=%d unfunded=%d spans=%d unread=%d sweepCalls=%d", st.Calls, st.Failed, st.Unfunded, st.Spans, st.Unread, st.Sweep.Calls)
				}
				b.WriteString(r.Kind + " " + r.Key + " " + d + "\n")
			}
		}
		return strings.ReplaceAll(b.String(), f.docs[0].ID, "D1")
	}
	v := f.get(task.ID)
	a, w := norm(f, task.ID), norm(ref, want.ID)
	if stop(v) != stop(ref.get(want.ID)) || a != w {
		t.Fatalf("%s vs %s: resumed ledger\n%s\nwant\n%s", stop(v), stop(ref.get(want.ID)), a, w)
	}
	s1, _ := f.e.classSpend(context.Background(), task.ID)
	s0, _ := ref.e.classSpend(context.Background(), want.ID)
	var lost json.RawMessage
	f.st.DB().QueryRow(`SELECT lost FROM tasks WHERE id = $1`, task.ID).Scan(&lost)
	t.Logf("resumed: sweep %+v read %+v evidence %+v; lost %s", s1["sweep"], s1["read"], s1["evidence"], lost)
	t.Logf("ref:     sweep %+v read %+v evidence %+v", s0["sweep"], s0["read"], s0["evidence"])
}

// bench7 r1 (verifier 5b): killed at the lazy read a drafted section makes of a cited finding that states no typed
// value (its reader call holds the memo and the finding): the resumed run equals an uninterrupted one.
func TestResumeAtLazySectionRead(t *testing.T) {
	t.Parallel()
	runIt := func(killAt int) (*fixture, Task) {
		f := newFixture(t)
		d1 := f.docs[0].ID
		sc := f.with(map[string][]model.Reply{
			"extract_facts": {facts(fd("Greystone claims damages of $644,910.", d1, "Greystone claims damages of $644,910 for unpaid invoices"))},
			"draft_section": {section("Greystone claims damages of $644,910 for unpaid invoices [F1].", "Damages [F1].")},
		})
		sc["draft_section"] = append(sc["draft_section"], sc["draft_section"]...)
		sc["lead"] = append(sc["lead"], sc["lead"]...)
		k := &killAtCall{Fake: newFake(sc), agent: "read_figures", at: killAt}
		e := f.reopen(k, nil)
		task := f.submit(0)
		if killAt > 0 {
			k.kill = func() { e.active(task.ID).cancel() }
			f.step(context.Background())
			t.Logf("first process: %s", f.steps(task.ID))
			k.kill = nil
			f.reopen(k, nil)
			f.stale(task.ID)
		}
		f.step(context.Background())
		return f, task
	}
	ref, want := runIt(0)
	f, task := runIt(1)
	norm := func(f *fixture, id string) string {
		var b strings.Builder
		for _, r := range f.ledger(id) {
			if r.Kind == kFinding || r.Kind == kSection {
				b.WriteString(r.Kind + " " + r.Key + " " + r.Data + "\n")
			}
		}
		return strings.ReplaceAll(b.String(), f.docs[0].ID, "D1")
	}
	v := f.get(task.ID)
	if a, w := norm(f, task.ID), norm(ref, want.ID); stop(v) != stop(ref.get(want.ID)) || a != w {
		t.Fatalf("%s vs %s:\n%s\nwant\n%s", stop(v), stop(ref.get(want.ID)), a, w)
	}
	t.Logf("ok %s", stop(v))
}

// bench7 r1 (verifier 8): a sweep whose later visit hits a model error returns that error before its end-of-sweep
// read; the step's buffered writes still commit (Record commits any non-started status), so the
// earlier visits' findings stay in the ledger admitted Unread with their typed values never checked
// (valueStated never ran). What does the run do next, and what does a reader of the ledger see?
func TestFailedSweepDropsUncheckedValues(t *testing.T) {
	t.Parallel()
	var doc strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&doc, "%d. Lot %d\nThe supplier shall be paid $%d,000 for lot %d.\n%s\n\n", i, i, 100+i, i, filler)
	}
	f := newFixture(t, doc.String())
	d1 := f.docs[0].ID
	sc := f.with(map[string][]model.Reply{"lead": {calls(plan(issue("I1", "What is the supplier paid?", "Conduct"))), calls(tc("sweep", m{"over": "sections", "documentIds": nil})),
		calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"draft_section": {section("Lot 1 costs $999,000 [F1].", "s")}})
	for i := 1; i <= 4; i++ {
		q := fmt.Sprintf("The supplier shall be paid $%d,000 for lot %d", 100+i, i)
		k := fmt.Sprintf("lot%d", i)
		// lot 1's analyst typed a value its quote does NOT state ($999,000): valueStated would drop it
		v := fmt.Sprintf("$%d,000", 100+i)
		if i == 1 {
			v = "$999,000"
		}
		r := facts(on("I1", fd(fmt.Sprintf("Lot %d costs %s.", i, v), d1, q, val("Supplier", fmt.Sprintf("lot %d price", i), v, "Money"))))
		if i == 4 {
			r = model.Reply{Err: model.ErrUnavailable}
		}
		sc[k] = []model.Reply{r, r, r, r}
	}
	fake := newFake(sc)
	fake.Route = func(req model.Request) string {
		if req.Agent != "extract_facts" {
			return req.Agent
		}
		if mm := regexp.MustCompile(`for lot (\d+)\.`).FindStringSubmatch(sweepReq(req).SectionText); mm != nil {
			return "lot" + mm[1]
		}
		return req.Agent
	}
	f.e = New(f.st, f.d, fake, testRoles, nil, Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	t.Logf("stop %s; steps %s; error %v", stop(v), f.steps(task.ID), v.Error)
	fs, _ := load[Finding](context.Background(), f.e, task.ID, kFinding)
	for _, x := range fs {
		t.Logf("%s unread=%v values=%v figures=%d notes=%q", x.FID, x.Unread, x.Values, len(x.Figures), x.ValueNotes)
		if x.Unread && len(x.Values) > 0 {
			t.Errorf("%s committed Unread with unchecked typed values %v", x.FID, x.Values)
		}
	}
	t.Logf("output: %s", deref(v.Output))
	for _, fv := range v.Findings {
		t.Logf("view %+v", fv)
	}
}

// bench7 r1 (verifier 9): limitedMemoUnread when the only ground stating the memo's unread figure is a worksheet's
// result (no cited finding's quote or value writes it): what is listed, what is gated?
func TestUnreadMemoWorksheetOnlyIsFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe first instalment is $5,000. The second instalment is $3,000.\n")
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What are the fees?", "Conduct"))), calls(extract(d1, "instalment", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The instalments are $5,000 and $3,000.", d1,
			"The first instalment is $5,000. The second instalment is $3,000",
			val("Company", "first instalment", "$5,000", "Money"), val("Company", "second instalment", "$3,000", "Money")))},
		"draft_section": {section("The instalments total $8,000 [F1].", "Fees [F1].")},
	}), Config{})
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind == "memo" {
			return model.Reply{Text: "not json"}, true
		}
		return readerOracle(req)
	}
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	fin, out := strings.Join(f.outputs(v.ID, "finish"), "\n"), deref(v.Output)
	t.Logf("stop %s gated %v\nfinish %s\n%s", stop(v), gated, fin, out)
	if strings.Contains(out, "to []") || strings.Contains(out, "total $8,000") && !strings.Contains(out, "is not traced") || !strings.Contains(fin, "the figure 8000 is not traced (G8)") {
		t.Errorf("an unread memo figure only a worksheet states is not flagged, or is listed as gated by no finding (stop %s):\n%s\n%s", stop(v), fin, out)
	}
}

// bench7 r1 (verifier 10): r11 H (TestRefOnlyScaledSumDisclosedAndGated) with the section's reader call failing: one
// wrong scale ("$5 billion" and "$3 billion" read x10^6 from "billion") summed by ref, the memo's
// "$8,000,000". At cfa9fcb the unread memo figure was a blocking G8 line. At ef9e6e4 it matches the
// worksheet's result as written, so it is limitedMemoUnread, which `continue`s past the worksheet
// listing: no ⚠ line with inputs, and F1 is NOT gated ("to [], whose findings are gated").
func TestUnreadMemoScaledSumNeverUngated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe first instalment is $5 billion. The second instalment is $3 billion.\n")
	wrong := lab("", analysis.KindMoney, "USD", "1000000", "billion", "", "")
	f.reads = map[string]analysis.Label{"quote:5": wrong, "quote:3": wrong, "value:5": wrong, "value:3": wrong}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	_, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What are the fees?", "Conduct"))), calls(extract(d1, "instalment", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The instalments are $5 billion and $3 billion.", d1,
			"The first instalment is $5 billion. The second instalment is $3 billion",
			val("Company", "first instalment", "$5 billion", "Money"), val("Company", "second instalment", "$3 billion", "Money")))},
		"draft_section": {section("The instalments total $8,000,000 [F1].", "Fees [F1].")},
	}), Config{})
	oracle := fake.Respond
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind == "memo" {
			return model.Reply{Text: "not json"}, true // the section's reader call fails
		}
		return oracle(req)
	}
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	out := deref(v.Output)
	t.Logf("stop %s gated %v\n%s", stop(v), gated, out)
	if strings.Contains(out, "total $8,000,000") && (!strings.Contains(out, "⚠ $8,000,000 = $5,000,000 [F1]") || !gated) {
		t.Errorf("$8,000,000 (the record: $8 billion) by a ref-only sum delivered, not listed ⚠ or not gated (gated %v); stop %s", gated, stop(v))
	}
}

// r1 LOW-5: finish's judge keeps a floor when the root is below the drafting share's keep.
func TestFinishJudgeHasAFloor(t *testing.T) {
	if g := judgeGrant(agent.Grant{Calls: 40, USD: 2}, agent.Usage{Calls: 10, USD: 0.4}); g.Calls != 7 || g.USD != judgeShare*1.6 {
		t.Errorf("beside the keep: %+v", g)
	}
	if g := judgeGrant(agent.Grant{Calls: 6, USD: 0.2}, agent.Usage{Calls: 20, USD: 1}); g.Calls != 1 || g.USD != judgeShare*0.2 {
		t.Errorf("below the keep: %+v, want one call", g)
	}
	if g := judgeGrant(agent.Grant{Calls: 1, USD: 0.1}, agent.Usage{Calls: 20, USD: 1}); g.Calls != 0 {
		t.Errorf("one call left: %+v", g)
	}
}

// r1 LOW-2: a batched reader call that fails is read again in halves, once, so one failure does not
// drop every finding in it.
func TestFailedReaderBatchSplitInHalves(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, fake := f.engine(nil, Config{})
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if len(in.Findings) > 2 {
			return model.Reply{Text: "not json"}, true // the batch of four fails; its halves do not
		}
		return readerOracle(req)
	}
	task := f.submit(0)
	rt, err := f.e.runtime(&run{e: f.e, t: task})
	if err != nil {
		t.Fatal(err)
	}
	var items []*reading
	for i := range 4 {
		items = append(items, findingReading(Finding{Finding: analysis.Finding{Claim: "Fee."}}, []analysis.Quote{{DocumentID: f.docs[0].ID, Quote: fmt.Sprintf("Lot %d costs $%d,000.", i, 100+i)}}, nil))
	}
	if err := f.e.readFigures(context.Background(), rt, agent.Call{TaskID: task.ID, DocumentIDs: task.DocumentIDs}, agent.NewBudget(50, 50, 0), "test", items); err != nil {
		t.Fatal(err)
	}
	for i, it := range items {
		if rs, _ := it.readings(); !it.read || slices.ContainsFunc(slices.Collect(maps.Values(rs)), func(r analysis.Reading) bool { return r.Unclear }) {
			t.Errorf("item %d not read after the split: %+v", i, rs)
		}
	}
}

// r1 MEDIUM-3: claims the reader could not read before the join (the valueless F2, and F1 whose read failed) are counted in
// Limitations, never silent.
func TestUnreadClaimsDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, compRef, compRows)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	f752 := fd("Bellini received undisclosed compensation of about $92,600.", d1,
		"The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600 ($45,000 initiation fee + $47,600 in monthly dues = $92,600).",
		val("Bellini", "total undisclosed compensation", "$92,600", "Money"))
	f388 := fd("Ostrowski's account paid $103,800.00 to Bayshore Palms on behalf of Bellini.", d2,
		"Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini | 05/03/2022 – 01/02/2024 | 22 | | $103,800.00")
	v := f.run(f.with(map[string][]model.Reply{"read_figures": {},
		"lead": {calls(plan(issue("I1", "What compensation did Bellini receive?", "RedFlag"))), calls(extract(d1, "compensation", "I1")), calls(extract(d2, "payments", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit")), calls(finishCall("submit")), calls(finishCall("submit"))},
		"extract_facts": {facts(f752), facts(f388)},
		"draft_section": {section("Bellini received about $92,600 [F1].", "s")},
	}), Config{})
	task, err := f.e.task(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := finish{e: f.e, r: &run{e: f.e, t: task}}.check(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Join(rep.limits, "\n")
	if !strings.Contains(out, "Figures: 2 findings whose claims state a figure were never read (the figure check ran out of budget or failed), so the cross-document comparison could not see their claims' sums: F1, F2.") {
		t.Errorf("the unread claim is not disclosed (%s):\n%s", stop(v), out)
	}
}
