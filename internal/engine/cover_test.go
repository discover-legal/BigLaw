// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

const referralText = `1. Review Period
The Division reviewed trades executed from January 1, 2021 through December 31, 2023.

2. Allocations
In 2021 the Firm allocated profitable trades to Oceanic Fund I LP.
`

// analysed is analyze_issue's answer: a rule, its application and a conclusion, no new findings.
func analysed(rule, app, concl string) model.Reply {
	return text(m{"rule": rule, "application": app, "conclusion": concl, "findings": []m{}, "authorities": []m{{"cite": "28 U.S.C. § 2462", "inRecord": false}}})
}

const (
	limitsRule = "Civil penalties are subject to the five-year limitations period of 28 U.S.C. § 2462 (general knowledge, not the record)."
	limitsConc = "Penalties for conduct in 2021 may be time-barred."
)

// TestCoveragePassFromTheReserve (bench, SEC referral: the run finished from its reserve with
// limitation periods and supervision touched, never analysed): before the forced finish, Go
// analyses each issue that was only touched, from the share kept for it, and redrafts it with
// that analysis; the redraft is reviewed, and what is still missing is disclosed.
func TestCoveragePassFromTheReserve(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	bad := model.Reply{Text: "not json"}
	e, _ := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"), issue("I2", "Is the Firm liable for failure to supervise?", "Conduct"))),
			calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {analysed(limitsRule, "The 2021 allocations [F1] fall outside five years of a 2026 action.", limitsConc), bad, bad},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s"),
			section("In 2021 the Firm allocated profitable trades to Oceanic [F1]. Under 28 U.S.C. § 2462 penalties for that conduct may be time-barred.", "s")},
	}), Config{})
	task := f.submit(6) // the run stops at its sixth step and finishes from its reserve
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	steps := f.steps(v.ID)
	out := deref(v.Output)
	if stop(v) != "awaiting_gate/max_steps" {
		t.Fatalf("%s\n%s", stop(v), steps)
	}
	if !strings.Contains(steps, "tool_call:cover:committed") || strings.Index(steps, "tool_call:cover") > strings.Index(steps, "tool_call:committee") ||
		!strings.Contains(out, "penalties for that conduct may be time-barred") || !strings.Contains(out, "Coverage: I2 (Is the Firm liable for failure to supervise?) was not analysed") ||
		strings.Contains(out, "Coverage: I1") {
		t.Fatalf("%s\n%s", steps, out)
	}
}

// TestCoveragePassOnSubmit: a funded run that submits with an issue only touched (facts, no
// analysis) gets the pass inside its submit: analyze_issue, the redraft with the analysis in its
// request, and a committee on the changed draft; the submit is then accepted, once.
func TestCoveragePassOnSubmit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"))), calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1")),
			calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {analysed(limitsRule, "The 2021 allocations [F1] fall outside five years.", limitsConc)},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s"),
			section("In 2021 the Firm allocated profitable trades to Oceanic [F1]; penalties for it may be time-barred.", "s")},
		"prosecute": {supported, supported}, "reproduce": {noAnswers, noAnswers}, "audit_gaps": {noGaps, noGaps},
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	fin := f.outputs(v.ID, "finish")
	if stop(v) != "awaiting_gate/finished" || len(fin) != 1 || !strings.Contains(deref(v.Output), "may be time-barred") || strings.Contains(deref(v.Output), "Coverage: I1") {
		t.Fatalf("%s %q\n%s", stop(v), fin, deref(v.Output))
	}
	var reqs []string
	crit := 0
	for _, c := range fake.Calls() {
		switch c.Agent {
		case "draft_section":
			reqs = append(reqs, fmt.Sprint(c.Messages[1].Content))
		case "prosecute":
			crit++
		}
	}
	if len(reqs) != 2 || strings.Contains(reqs[0], `"analysis"`) || !strings.Contains(reqs[1], `"analysis":{"rule":"Civil penalties`) || !strings.Contains(reqs[1], "Coverage pass") || crit != 2 {
		t.Fatalf("%d critics; draft requests %q", crit, reqs)
	}
	if touched, _ := e.touched(context.Background(), task.ID); len(touched) != 0 {
		t.Fatalf("still touched: %+v", touched)
	}
}

// TestCoverStepGrantIsItsShare (lab-improve r2 verifier M4): the coverage pass's journalled grant
// (what a hard crash charges as lost) is the share kept for it, not the review level's whole free
// budget.
func TestCoverStepGrantIsItsShare(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	bad := model.Reply{Text: "not json"}
	e, _ := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"), issue("I2", "Is the Firm liable for failure to supervise?", "Conduct"))),
			calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {analysed(limitsRule, "The 2021 allocations [F1] fall outside five years of a 2026 action.", limitsConc), bad, bad},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s"),
			section("In 2021 the Firm allocated profitable trades to Oceanic [F1]. Under 28 U.S.C. § 2462 penalties for that conduct may be time-barred.", "s")},
	}), Config{})
	task := f.submit(6)
	f.step(context.Background())
	var raw []byte
	if err := f.st.DB().QueryRow(`SELECT grant_held FROM task_steps WHERE task_id = $1 AND tool = 'cover'`, task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var g agent.Grant
	_ = json.Unmarshal(raw, &g)
	a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
	if g.Calls > a.Cover || g.USD > a.dollars(a.Cover)+1e-9 {
		t.Fatalf("cover step holds %d calls ($%.4f); its share is %d calls ($%.4f)", g.Calls, g.USD, a.Cover, a.dollars(a.Cover))
	}
}

// TestCoveragePassRecordedWhenAModelFails (lab-improve r2 verifier L5): a pass that a model
// failure ends is still recorded as run, so a retried run does not spend a fresh share on it.
func TestCoveragePassRecordedWhenAModelFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	down := model.Reply{Err: model.ErrUnavailable}
	_, _ = f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"))),
			calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {down, down, down, down},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s")},
	}), Config{})
	task := f.submit(4)
	f.step(context.Background())
	steps := f.steps(task.ID)
	if !strings.Contains(steps, "tool_call:cover:failed") {
		t.Fatalf("the pass did not fail on the model error:\n%s", steps)
	}
	if rows, err := f.e.rows(context.Background(), task.ID, kCover); err != nil || len(rows) != 1 {
		t.Fatalf("cover rows %d (%v), want the pass recorded once\n%s", len(rows), err, steps)
	}
}

// TestCoverageDraftsLeftOutEvidence (bench r3: the trust memo never cited the verified findings on
// the incapacity standard, the disinheritance clause, the contingent guardian and the no-contest
// clause; the SEC memo stated 4,217 trades but never the statistical exhibit's 4,312): a figure two
// documents state differently is a finish check until the memo states both, and a salient verified
// finding no section cites is placed by the coverage pass, which redrafts its issue with it; the
// redraft is reviewed before the deliverable.
func TestCoverageDraftsLeftOutEvidence(t *testing.T) {
	t.Parallel()
	referral := "1. Analysis\nThe Division's Office of Quantitative Research analyzed 4,217 equity trades executed through the omnibus account during the Review Period.\n\n" +
		"2. Recordkeeping\nThe Firm did not retain allocation records for 2,614 of the omnibus trades executed during the Review Period.\n"
	stats := "Total Omnibus Equity Trades Analyzed\t4,312\tTrades executed through WCA omnibus account at Meridian Clearing Corp. during Review Period\n"
	f := newFixture(t, referral, stats)
	f.measures = map[string]string{"4,217": "omnibus equity trades analysed", "4,312": "omnibus equity trades analysed"} // the right labels: one count typed two ways
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	q1 := "The Division's Office of Quantitative Research analyzed 4,217 equity trades executed through the omnibus account during the Review Period"
	q2 := "Total Omnibus Equity Trades Analyzed\t4,312\tTrades executed through WCA omnibus account at Meridian Clearing Corp. during Review Period"
	q3 := "The Firm did not retain allocation records for 2,614 of the omnibus trades executed during the Review Period"
	sweep := facts(on("I1", fd("The Firm did not retain allocation records for 2,614 omnibus trades.", d1, q3))) // a finding the call named no issue for
	three := func(r model.Reply) []model.Reply { return []model.Reply{r, r, r} }
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What did the Division's analysis of the omnibus trades find?", "Clause"))),
			calls(extract(d1, "trades analyzed", "I1"), extract(d2, "trades analyzed", "I1")),
			calls(tc("extract_facts", m{"question": "Recordkeeping?", "documentId": d1, "sectionId": nil, "through": nil, "offset": nil, "query": "records", "issueId": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit")),
			calls(tc("draft_section", m{"issueId": "I1", "fids": []string{"F1", "F2"}, "revise": "state both counts"})), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("OQR analyzed 4,217 omnibus equity trades.", d1, q1, val("WCA", "trades analyzed", "4,217", "Count"))),
			facts(fd("The statistical summary counts 4,312 omnibus equity trades analyzed.", d2, q2, val("WCA", "total omnibus equity trades", "4,312", "Count"))), sweep},
		"draft_section": {section("OQR analyzed 4,217 omnibus trades [F1].", "s"),
			section("OQR analyzed 4,217 omnibus trades [F1], while the statistical summary counts 4,312 [F2]: the record does not explain the difference.", "s"),
			section("OQR analyzed 4,217 omnibus trades [F1], while the statistical summary counts 4,312 [F2]. The Firm kept no allocation records for 2,614 omnibus trades [F3].", "s")},
		"prosecute": three(supported), "reproduce": three(noAnswers), "audit_gaps": three(noGaps),
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	fin := f.outputs(v.ID, "finish")
	out := deref(v.Output)
	if len(fin) != 2 || !strings.Contains(fin[0], "memo: the documents state") || !strings.Contains(fin[0], "[F1] 4,217 vs [F2] 4,312") {
		t.Fatalf("finish %q\n%s", fin, f.steps(v.ID))
	}
	var placed string
	for _, c := range fake.Calls() {
		if c.Agent == "draft_section" {
			placed = fmt.Sprint(c.Messages[1].Content)
		}
	}
	if stop(v) != "awaiting_gate/finished" || !strings.Contains(placed, "These findings never reached the memo: F3") || !strings.Contains(out, "2,614 omnibus trades [F3]") ||
		!strings.Contains(out, "4,312") || strings.Count(f.steps(v.ID), "tool_call:committee") != 2 {
		t.Fatalf("%s: last drafter request %.300q\n%s\n%s\n%q", stop(v), placed, f.steps(v.ID), out, f.outputs(v.ID, "finish"))
	}
	if strings.Contains(out, "never reached the memo") {
		t.Fatalf("a placed finding was disclosed as left out:\n%s", out)
	}
}

// TestCoverageLinesSummarised (bench5 SEC: 101 "Coverage:" lines in Limitations, one per party and
// figure): the parties the memo never names are one line and the missing figures another, each
// listing at most maxListed and counting the rest.
func TestCoverageLinesSummarised(t *testing.T) {
	var ps []analysis.PartyGap
	var hs []analysis.Handle
	for i := range 30 {
		ps = append(ps, analysis.PartyGap{Name: fmt.Sprintf("Party %d", i), FIDs: []string{fmt.Sprintf("F%d", i), "F99", "F100", "F101"}})
		hs = append(hs, analysis.Handle{FID: fmt.Sprintf("F%d", i), Label: "fee", Value: fmt.Sprintf("$%d,000", i+1)})
	}
	p, h := partyLine(ps), figureLine(hs)
	if !strings.HasPrefix(p, "Coverage: 30 parties named in verified findings never appear in the memo: Party 0 (F0, F99, F100); ") || !strings.HasSuffix(p, " and 18 more.") ||
		!strings.HasPrefix(h, "Coverage: 30 salient figures never appear in the memo: $1,000 (fee, F0); ") || !strings.HasSuffix(h, " and 18 more.") ||
		partyLine(nil) != "" || figureLine(nil) != "" {
		t.Fatalf("%q\n%q", p, h)
	}
}

// TestPlacementsStrongestFirst: the coverage pass places an issue with a figure conflict, then the
// issues by their strongest left-out finding, not in plan order, so a pass whose share runs out has
// placed the most salient evidence. (The conflict is the figure judge's verdict, which join_discrepancies
// asks for.)
func TestPlacementsStrongestFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe Firm failed to disclose the fee. The fee was $291,400 in 2023.\n", "Schedule\nThe fee was $310,000 in 2023.\n")
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Was the fee disclosed?", "RedFlag"), issue("I2", "What was the fee?", "RedFlag"))),
			calls(extract(d1, "disclose", "I1")), calls(extract(d1, "fee", "I2")), calls(extract(d2, "fee", "I2")), calls(tc("join_discrepancies", m{"fids": nil, "aliases": nil}))},
		"extract_facts": {facts(fd("The Firm failed to disclose the fee.", d1, "The Firm failed to disclose the fee")),
			facts(fd("The fee was $291,400.", d1, "The fee was $291,400 in 2023", val("Firm", "fee", "$291,400", "Money"))),
			facts(fd("The fee was $310,000.", d2, "The fee was $310,000 in 2023", val("Firm", "fee", "$310,000", "Money")))},
	}), Config{})
	order, place, err := f.e.placements(context.Background(), v.ID)
	if err != nil || strings.Join(order, ",") != "I2,I1" || len(place["I2"].Conflicts) != 1 || !slices.Contains(place["I1"].FIDs, "F1") {
		t.Fatalf("order %v, %+v, %v", order, place, err)
	}
}

// lab-r4 r4 verifier repros (critique/biglaw-lab-r4-r4-verifier.md): the mechanical LOWs (L7 the
// committee cap's disclosure, L8 the placement request's cap, L9 a merged duplicate's value notes).

// L9: an exact duplicate's dropped values were noted only in the call's notes, never on the kept finding.
func TestR4V4DuplicateKeepsValueNotes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n")
	d1 := f.docs[0].ID
	q := "Greystone claims damages of $644,910 for unpaid invoices"
	first := fd("Greystone claims damages of $644,910.", d1, q, val("Greystone", "damages", "$644,910", "Money"))
	again := fd("Greystone claims damages of $644,910.", d1, q, val("Greystone", "damages", "$644,910", "Money"), val("Greystone", "interest", "$99,000", "Money"))
	v := f.run(f.with(map[string][]model.Reply{"extract_facts": {facts(first, again)}}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || !slices.ContainsFunc(fs[0].ValueNotes, func(n string) bool { return strings.Contains(n, "$99,000") }) {
		t.Errorf("the merged duplicate's dropped value is not on the kept finding: %+v", fs)
	}
}

// L8: a placement redraft's request cut a large section's own citations at maxDraftFIDs.
func TestR4V4PlacementKeepsCitations(t *testing.T) {
	var cites, left []string
	for i := range 95 {
		cites = append(cites, fmt.Sprintf("F%d", i+1))
	}
	for i := range 16 {
		left = append(left, fmt.Sprintf("F%d", 200+i))
	}
	p := &placement{FIDs: left, Conflicts: []analysis.Conflict{{Values: []analysis.ConflictVal{{FID: "F300"}, {FID: "F3"}}}}}
	fids, ok := placeFIDs(p, cites)
	if !ok || len(fids) != maxDraftFIDs || !slices.Contains(fids, "F300") || !slices.Contains(fids, "F200") || slices.Contains(fids, "F215") {
		t.Errorf("placement request %v (%v)", fids, ok)
	}
	for _, c := range cites {
		if !slices.Contains(fids, c) {
			t.Fatalf("the section's citation %s was cut from the request", c)
		}
	}
	for i := range 10 {
		cites = append(cites, fmt.Sprintf("F%d", 400+i))
	}
	if _, ok := placeFIDs(p, cites); ok {
		t.Errorf("a section citing more than the request holds was redrafted from fewer")
	}
}

// L7: after the committee's cap, fixes resolved after its last cycle were disclosed only generically.
func TestR4V4UnreviewedFixesNamed(t *testing.T) {
	crit := []Critic{
		{ID: "C1", Cycle: 1, Critic: "prosecute", Severity: "HIGH", Text: "reviewed fix", Status: "fixed", Resolved: 1},
		{ID: "C7", Cycle: 3, Critic: "reproduce", Severity: "HIGH", Text: "the deadline is wrong", Status: "fixed", Resolved: 3},
		{ID: "C2", Cycle: 2, Critic: "audit_gaps", Severity: "MEDIUM", Text: "late fix", Status: "fixed", Resolved: 3},
		{ID: "C8", Cycle: 3, Critic: "audit_gaps", Severity: "LOW", Text: "disclosed", Status: "disclosed", Resolved: 3},
	}
	got := unreviewedFixes(crit, 3)
	if !strings.Contains(got, "C7 (HIGH, reproduce): the deadline is wrong") || !strings.Contains(got, "C2 (MEDIUM") || strings.Contains(got, "C1") || strings.Contains(got, "C8") {
		t.Errorf("unreviewed fixes: %q", got)
	}
	if unreviewedFixes(crit[:1], 3) != "" {
		t.Errorf("a fix the committee reviewed was named")
	}
}
