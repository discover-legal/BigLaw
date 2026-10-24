// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

// lab-r4 r8 verifier repros (critique/biglaw-lab-r4-r8-verifier.md), kept as regression tests.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
)

// R8-H1 / choice (a), unit: a section number the reader reads as kind "other" (as read_figures.md
// tells it: "other (an identifier, a section or clause number, a duration ...)") is accepted as a
// stated day count, so date_add(served, 30 days) traces although the record's period is 21 days.
func TestSectionNumberIsNoDayCount(t *testing.T) {
	ground := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days.\n"
	known := []analysis.Reading{
		{Kind: analysis.KindOther, Amount: "30", Text: "30"},                                    // "Section 30": a section number, kind other
		{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"}, // "21 days": the period
	}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"30", "days"})
	if got := tracedSheets([]analysis.Worksheet{w}, ground, nil, known); len(got) == 1 {
		t.Errorf("date_add(2024-03-01, 30 days) traced from the section number \"Section 30\" (result %s); the record's period is 21 days", w.Result)
	}
}

// R8-H1, unit: the same hole for products. A plain-number operand is accepted when the reader read the
// same digits as kind other (literalTraced: kind == count && k.Kind == other), so "Section 12" is a
// multiplier: product(12, $1,000,000) = $12,000,000 traces a memo figure no source states.
func TestSectionNumberIsNoMultiplier(t *testing.T) {
	quote := "Under Section 12 of the Agreement, the Buyer shall pay $1,000,000 at Closing."
	known := []analysis.Reading{{Kind: analysis.KindOther, Amount: "12", Text: "12"}, {Kind: analysis.KindMoney, Currency: "USD", Amount: "1000000", Text: "1,000,000"}}
	cited := map[string]bool{"F1": true}
	w := sheet(t, "product", [2]string{"12", ""}, [2]string{"1,000,000", "USD"})
	memo := "The Buyer pays $12,000,000 at Closing."
	sheets := tracedSheets([]analysis.Worksheet{w}, quote+"\n", cited, known)
	if got := g8(memo, quote+"\n", 0, nil, memoRead(memo, lab("m.s1", analysis.KindMoney, "USD", "1", "", "", "")), known, nil, sheets, cited, nil); len(got) == 0 {
		t.Errorf("%q traced by worksheet %s = %s %s from a section number times the price", memo, w.WID, w.Result, w.Unit)
	}
}

// R8-H1, e2e: the wrong deadline reaches the deliverable with no line. The stand-in reads "Section 30"
// as other (oracleRef), as the prompt says; the lead computes date_add(served, 30 days); the drafter
// writes the computed date out (no handle).
func TestSectionNumberDeadlineNotTraced(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nUnder Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("30", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 days.", d1,
			"Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days"))},
		"draft_section": {section("The answer is due March 31, 2024 [F1].", "Due [F1].")},
	}), Config{})
	out, fin := deref(v.Output), strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(out, "March 31, 2024") && !strings.Contains(fin, "not traced") && !strings.Contains(out, "typed inputs") {
		t.Errorf("a 30-day count taken from \"Section 30\" traced the memo's March 31, 2024 (the record says 21 days: March 22), undisclosed; stop %s", stop(v))
	}
}

// R8-H1, e2e: the money half through a run: "$12,000,000" delivered, traced to 12 x $1,000,000.
func TestSectionNumberProductNotTraced(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Price\nUnder Section 12 of the Agreement, the Buyer shall pay $1,000,000 at Closing.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What does the Buyer pay?", "Conduct"))), calls(extract(d1, "pay", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "product", "operands": []m{op("12", ""), op("1,000,000", "USD")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The Buyer shall pay $1,000,000 at Closing.", d1, "Under Section 12 of the Agreement, the Buyer shall pay $1,000,000 at Closing",
			val("Buyer", "closing payment", "$1,000,000", "Money")))},
		"draft_section": {section("The Buyer pays $12,000,000 at Closing [F1].", "Pays [F1].")},
	}), Config{})
	out, fin := deref(v.Output), strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(out, "$12,000,000") && !strings.Contains(fin, "not traced") && !strings.Contains(out, "typed inputs") {
		t.Errorf("\"$12,000,000\" (Section 12 x $1,000,000) delivered traced, undisclosed; stop %s", stop(v))
	}
}

// R8: the judge, unfunded or failing at finish: every pair is unjudged, and the delivered memo must
// say so (a conflict may be missing). Also counts the judge's calls on a failing judge.
func TestFailingJudgeDisclosedInMemo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, feeLetter, feeSchedule)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	bad := model.Reply{Text: "not json"}
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023",
			val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
			facts(fd("The Adviser paid Lakeshore Trading LLC commissions of $310,000.", d2, "The Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023",
				val("Lakeshore Trading LLC", "commissions", "$310,000", "Money")))},
		"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
		"judge_figures": {bad, bad, bad, bad, bad, bad, bad, bad, bad, bad, bad, bad},
	}), Config{})
	out := deref(v.Output)
	if !strings.Contains(out, "not judged") {
		t.Errorf("a failing judge left the commissions pair unjudged, and the memo does not say so; stop %s\n%s", stop(v), out)
	}
}

// R8: an "unclear" verdict reaches the delivered memo as a low-confidence line.
func TestUnclearVerdictReachesMemo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, feeLetter, feeSchedule)
	f.judge = func(a, b figtest.JudgeSide) string { return "unclear" }
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023",
			val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
			facts(fd("The Adviser paid Lakeshore Trading LLC commissions of $310,000.", d2, "The Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023",
				val("Lakeshore Trading LLC", "commissions", "$310,000", "Money")))},
		"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
	}), Config{})
	if out := deref(v.Output); !strings.Contains(out, "low confidence") {
		t.Errorf("an unclear verdict is not in the delivered memo; stop %s\n%s", stop(v), out)
	}
}

// R8 (cache, matter isolation): a task with no matter number gets scope "matter:" (not ""), so the
// judge's verdicts (and the reader's labels) are cached and reused across every task that has no
// matter number: a second, unrelated no-matter task over the same documents asks the judge nothing.
func TestNoMatterTasksDoNotShareVerdicts(t *testing.T) {
	t.Parallel()
	f := newFixture(t, feeLetter, feeSchedule)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	scripts := func() map[string][]model.Reply {
		return f.with(map[string][]model.Reply{
			"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
				calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
			"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023",
				val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
				facts(fd("The Adviser paid Lakeshore Trading LLC commissions of $310,000.", d2, "The Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023",
					val("Lakeshore Trading LLC", "commissions", "$310,000", "Money")))},
			"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
		})
	}
	var n []int
	for i := range 2 {
		_, fake := f.engine(scripts(), Config{})
		task, err := f.e.Submit(context.Background(), Task{Description: []string{"Client A intake.", "Client B intake."}[i], WorkflowType: "memo", Jurisdiction: "US",
			MatterNumber: "", DocumentIDs: f.ids(), ToolBandit: true})
		if err != nil {
			t.Fatal(err)
		}
		f.step(context.Background())
		_ = task
		n = append(n, judgeCallCount(fake))
	}
	if n[0] != 1 || n[1] != 1 {
		t.Errorf("two tasks with no matter number share the judge's cache: judge calls per run %v, want [1 1]", n)
	}
}

// R8 (judge fabricates a reason): CheckVerdicts keeps any non-blank reason; the README says the
// reason is "quoted ... checked by CheckVerdicts". A reason quoting nothing in either passage is kept
// and becomes the conflict's reason the lead reads.
func TestJudgeReasonMustQuoteThePassages(t *testing.T) {
	t.Parallel()
	made := "A says \"the parties agreed a $5,000,000 cap in the side letter\"; B says \"see the side letter\"."
	f, v, _ := feeRun(t, nil, map[string][]model.Reply{"judge_figures": {func() model.Reply {
		data, _ := json.Marshal(map[string]any{"verdicts": []map[string]string{{"pair": "F1.v1|F2.v1", "sameQuantity": "yes", "reason": made}}})
		return model.Reply{Text: string(data)}
	}()}})
	j, err := f.e.joined(context.Background(), v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.all) != 0 || len(j.unclear) != 1 {
		t.Errorf("a reason quoting neither passage asserted a conflict: %+v", j)
	}
}

// R8-M (r7 M2 residual): "the passage's first line when the span is on a later one" is any first line,
// not a header: a quote whose first line is a sentence ("Revenue grew to $4.2 million in 2023.") lends
// its "million" to a figure on the next line, so one wrong reader label verifies a typed "$500 million"
// from a quote stating a $500 fee.
func TestFirstLineSentenceScalesNoLaterLine(t *testing.T) {
	q := "Revenue grew to $4.2 million in 2023.\nThe filing fee is $500 per application."
	p := analysis.PassageOf("q1", q)
	var id500 string
	var ls []analysis.Label
	for i, s := range p.Spans {
		if s.Text == "500" {
			id500 = fmt.Sprintf("q1.s%d", i+1)
		}
	}
	ls = append(ls, lab(id500, analysis.KindMoney, "USD", "1000000", "million", "", ""))
	ls = append(ls, lab("v1.s1", analysis.KindMoney, "USD", "1000000", "million", "", ""))
	rs, _ := analysis.ReadLabels([]analysis.Passage{p, analysis.PassageOf("v1", "$500 million")}, ls)
	if r := rs[id500]; r.Scale != "" && !r.Unclear {
		t.Errorf("the fee's $500 took \"million\" from the first line's sentence: %+v", r)
	}
	if _, ok, low, _ := valueStated(mv3("$500 million", "Money"), 0, rs, q); ok && !low {
		t.Errorf("typed \"$500 million\" verified (not gated) from %q", q)
	}
}
