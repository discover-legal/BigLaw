// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

// lab-r4 r6 verifier repros (critique/biglaw-lab-r4-r6-verifier.md), in the tree: each failed at
// db887ef.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// R6-H1 (unit): a scale's source only has to be whole words of the passage outside the span, so the
// quote's "$500" read "in thousands" from the word "fee" (or from another figure's "(in thousands)"
// header) is a clear 500000 at scale 1000; a typed "$500 thousand" read at scale 1000 from its own
// "thousand" then matches it CLEARLY (same scale string), so it is admitted verified, not gated.
func TestR6ScaleFromAnyWordVerifiesAScaledValue(t *testing.T) {
	for name, c := range map[string]struct{ q, from string }{
		"a word that is no scale": {"The filing fee is $500 per application.", "fee"},
		"another figure's header": {"The filing fee is $500 per application. Program costs (in thousands): 1,200.", "in thousands"},
	} {
		ps := []analysis.Passage{analysis.PassageOf("q1", c.q), analysis.PassageOf("v1", "$500 thousand")}
		rs, notes := analysis.ReadLabels(ps, []analysis.Label{
			lab("q1.s1", analysis.KindMoney, "USD", "1000", c.from, "", ""),
			lab("v1.s1", analysis.KindMoney, "USD", "1000", "thousand", "", ""),
		})
		if _, ok, low, _ := valueStated(mv3("$500 thousand", "Money"), 0, rs, c.q); ok && !low {
			t.Errorf("%s: typed \"$500 thousand\" VERIFIED (not gated) from %q (quote read %+v, notes %v)", name, c.q, rs["q1.s1"], notes)
		}
	}
}

// R6-H1 (unit, G8): the same one wrong label on the quote traces a memo's "$500 thousand".
func TestR6ScaleFromAnyWordTracesAScaledMemoFigure(t *testing.T) {
	q := "The filing fee is $500 per application."
	qr, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{lab("q1.s1", analysis.KindMoney, "USD", "1000", "fee", "", "")})
	memo := "The filing fee is $500 thousand."
	mr, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("m", memo)}, []analysis.Label{lab("m.s1", analysis.KindMoney, "USD", "1000", "thousand", "", "")})
	read := map[int]analysis.Reading{}
	for _, r := range mr {
		read[r.Start] = r
	}
	if got := g8(memo, q, 0, nil, read, []analysis.Reading{qr["q1.s1"]}, nil, nil, nil, nil); len(got) == 0 {
		t.Errorf("G8 traces the memo's $500 thousand to a quote stating $500 (quote read %+v)", qr["q1.s1"])
	}
}

// R6-H1 (e2e): the extractor types "$500 thousand", the reader (one wrong label) scales the quote's
// $500 from "fee"; the value is admitted verified and the memo's "$500 thousand" is delivered traced.
func TestR6ScaleSmuggleWithAScaledValueReachesMemo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe filing fee is $500 per application.\n")
	f.reads = map[string]analysis.Label{"quote:500": lab("", analysis.KindMoney, "USD", "1000", "fee", "", "")}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The filing fee is $500 thousand.", d1, "The filing fee is $500 per application", val("Company", "filing fee", "$500 thousand", "Money")))},
		"draft_section": {section("The filing fee is $500 thousand per application [F1].", "Fee [F1].")},
	}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) == 1 && len(fs[0].Values) == 1 && !fs[0].Low {
		t.Errorf("typed $500 thousand verified (not gated) from a quote stating $500: %+v", fs[0].Values[0].Fig)
	}
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(deref(v.Output), "$500 thousand") && !strings.Contains(fin, "not traced") && !strings.Contains(deref(v.Output), "written at a scale") {
		t.Errorf("memo states $500 thousand from a $500 quote; stop %s, no G8 line, no Limitations line", stop(v))
	}
}

// R6-M (unit, G8, choice (a)): the memo's "$5" read at 10^6 from any whole word of its own sentence
// ("share") is "limited" beside a cited $5,000,000, never flagged: a per-share figure the source never
// states is delivered with only a Limitations line.
func TestR6LimitedScaleFromAnyWord(t *testing.T) {
	memo := "Each holder received $5 per share."
	mr, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("m", memo)}, []analysis.Label{lab("m.s1", analysis.KindMoney, "USD", "1000000", "share", "", "")})
	read := map[int]analysis.Reading{}
	for _, r := range mr {
		read[r.Start] = r
	}
	known := []analysis.Reading{{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000"}}
	got := g8(memo, "The aggregate consideration is $5,000,000.", 0, nil, read, known, nil, nil, nil, nil)
	if len(got) == 1 && got[0].limited != "" {
		t.Errorf("the memo's \"$5 per share\" (scale from \"share\") is limited, not flagged: %+v", got)
	}
}

// R6-L (accounting, choice (f)): the coverage pass's redraft runs its section's figure reader too; its
// calls are added to kRead and subtracted from the draft class's calls although the cover step's
// calls were never the draft class's, so P4 sees fewer drafter calls than the lead made.
func TestR6CoverReaderCallsLeaveTheDraftShare(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	_, fake := f.engine(f.with(map[string][]model.Reply{
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
	spent, err := f.e.classSpend(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps := f.steps(task.ID)
	drafts := 0
	for _, c := range fake.Calls() {
		if c.Agent == "draft_section" {
			drafts++
		}
	}
	if drafts != 2 {
		t.Fatalf("want the lead's draft and the coverage pass's redraft, got %d drafter calls\n%s", drafts, steps)
	}
	if spent["draft"].Calls != 1 {
		t.Errorf("the lead's one draft_section drafter call shows as %d draft-class calls (cover's reader calls subtracted)\n%s", spent["draft"].Calls, steps)
	}
}

// R6-M (pre-existing, widened use): a worksheet of literals only ("sum of 9000000000 and 0", no ref to
// any verified value) traces any memo figure equal to its result: G8 puts every worksheet's result in
// ground and matches a clear reading against every sheet, and the "calculation from typed inputs"
// Limitations line fires only when the section writes the worksheet's {W…} handle. The eval oracle
// (eval/proofruns_test.go) now uses exactly this to pass G8 on its answers' figures.
func TestR6LiteralWorksheetLaundersAFigure(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Settlement\nThe settlement payment is $9,000.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What is the settlement?", "Conduct"))), calls(extract(d1, "settlement", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{op("9000000000", "USD"), op("0", "USD")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The settlement payment is $9,000.", d1, "The settlement payment is $9,000", val("Company", "settlement payment", "$9,000", "Money")))},
		"draft_section": {section("The settlement is $9,000,000,000 [F1].", "Settlement [F1].")},
	}), Config{})
	out := deref(v.Output)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(out, "$9,000,000,000") && !strings.Contains(fin, "not traced") && !strings.Contains(out, "typed inputs") {
		t.Errorf("a literal-only worksheet traced the memo's $9,000,000,000 against a $9,000 quote, undisclosed; stop %s\n%s", stop(v), f.outputs(v.ID, "numbers"))
	}
}

// R6 M2: the admission's reader call fails, the section's succeeds: the memo's clear "$2,000,000 paid"
// traces only as written to a quote's unread "2,000,000 shares": disclosed in Limitations, and the
// finding it cites is gated for a lawyer (G-F1), though it has no typed value.
func TestR6UnreadQuoteTracesAsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Issuance\nThe Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor.\n")
	d1 := f.docs[0].ID
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Company shall issue 2,000,000 shares to the Investor.", d1, "The Company shall issue Two Million (2,000,000) shares of Common Stock to the Investor"))},
		"draft_section": {section("The Investor paid $2,000,000 for its shares [F1].", "Paid [F1].")},
	}), Config{})
	fake.Respond = func(req model.Request) (model.Reply, bool) {
		if req.Agent != "read_figures" {
			return model.Reply{}, false
		}
		var in readRequest
		requestOf(req, &in)
		if len(in.Findings) > 0 && in.Findings[0].Passages[0].Kind != "memo" {
			return model.Reply{Text: "not json"}, true // the admission's reading fails
		}
		return readerOracle(req)
	}
	_ = e
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	fs := ledgerFindings(t, f, v.ID)
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	out := deref(v.Output)
	if strings.Contains(out, "$2,000,000") && !strings.Contains(fin, "not traced") && !(len(fs) == 1 && fs[0].Low) {
		t.Errorf("delivered %s with the memo's $2,000,000 traced only as written and nothing gated", stop(v))
	}
	if !strings.Contains(out, "traced only as written") || !slices.ContainsFunc(v.PendingGates, func(g Gate) bool { return g.GateID == "G-F1" }) && !slices.ContainsFunc(v.DecidedGates, func(g Gate) bool { return g.GateID == "G-F1" }) {
		t.Errorf("the finding a memo figure traces to only as written is not disclosed and gated (gates %+v):\n%s", v.PendingGates, out)
	}
}

// L3: a complete answer that reads nothing clearly (unclear throughout) is never cached: a later run,
// or a stronger candidate model, reads those figures again.
func TestR6AllUnclearAnswerIsNotCached(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	u := lab("", analysis.KindUnclear, "unclear", "", "", "", "unclear")
	unclear := scriptedLabels(func() []analysis.Label {
		var ls []analysis.Label
		for _, id := range []string{"1.q1.s1", "1.v1.s1", "1.c.s1"} {
			l := u
			l.Span = id
			ls = append(ls, l)
		}
		return ls
	}()...)
	f.run(f.with(map[string][]model.Reply{"read_figures": {unclear, unclear, unclear}}), Config{})
	var n int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM figure_reads`).Scan(&n); err != nil || n != 0 {
		t.Errorf("an all-unclear answer was cached: %d rows (%v)", n, err)
	}
}
