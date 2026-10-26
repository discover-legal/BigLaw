// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r12 verifier repros (critique/biglaw-lab-r4-r12-verifier.md M1, LOW-4), kept as regression
// tests.

// countAt21 reads the "21" of a quote as a count (one wrong kind: "21 business days" is a period),
// its other spans as dates.
func countAt21(s analysis.Span) analysis.Label {
	return lab("", cmpStr(s.Text == "21", analysis.KindCount, analysis.KindDate), "", "1", "", "", "")
}

// M1: a value with no unit, read as a count, as date_add's day count (Calc takes a unitless number
// as days): a period only by Calc's default, never the reader's: ⚠, and its finding gated.
func TestRefCountNoUnitUsedAsPeriodGated(t *testing.T) {
	f1, w := refFinding(t, businessDaysQuote, countAt21, "date_add",
		analysis.Value{Entity: "Plaintiff", Metric: "service date", Value: "March 1, 2024", Class: "Date"},
		analysis.Value{Entity: "Defendant", Metric: "answer period", Value: "21", Class: "Count", Fig: &analysis.Reading{Kind: analysis.KindCount, Amount: "21", Text: "21"}})
	lines, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(computed) != 1 || !computed[0].Mismatch || !strings.Contains(computed[0].Text, ": the figure check read it as a count figure, not a period in days") || gate["F1"] != limitedUnit {
		t.Errorf("a count used as date_add's days (no unit): lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// M1: the same through a literal "21" with no unit whose cited reading is a count.
func TestLiteralCountNoUnitUsedAsPeriodGated(t *testing.T) {
	p := analysis.PassageOf("q1", businessDaysQuote)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := countAt21(s)
		l.Span = analysis.SpanID("q1", i)
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: businessDaysQuote, Method: "exact"}}}
	for i := range p.Spans {
		f1.Figures = append(f1.Figures, rs[analysis.SpanID("q1", i)])
	}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", ""})
	lines, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(computed) != 1 || !computed[0].Mismatch || !strings.Contains(computed[0].Text, ": the figure check read it as a count figure, not a period in days") || gate["F1"] != limitedUnit {
		t.Errorf("a count literal used as date_add's days (no unit): lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// LOW-4: a ref's snippet is its own span's (admission's match, Reading.Stated), not the first span
// of the same amount.
func TestRefSnippetIsItsOwnSpan(t *testing.T) {
	quote := "The deposit is $5,000,000, refundable. The holdback, also $5,000,000, is released at closing."
	money := func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }
	fig := func(span string) *analysis.Reading {
		return &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000", Stated: span}
	}
	f1, w := refFinding(t, quote, money, "sum",
		analysis.Value{Entity: "Buyer", Metric: "holdback", Value: "$5,000,000", Class: "Money", Fig: fig("q1.s2")},
		analysis.Value{Entity: "Buyer", Metric: "deposit", Value: "$5,000,000", Class: "Money", Fig: fig("q1.s1")})
	_, _, _, computed := lintLimited("I1", labelledSection("Together they are $10,000,000 [F1].", money), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(computed) != 1 || !strings.HasPrefix(computed[0].Text, "$10,000,000 = $5,000,000 [F1] “…The holdback, also $5,000,000, is released at…” + $5,000,000 [F1] “The deposit is $5,000,000, refundable. The…”") {
		t.Errorf("%+v", computed)
	}
}

// M1 e2e: the extractor's value has no unit ("unit": null), the reader reads the "21" of "21 business
// days" as a count, and date_add takes it as calendar days: the memo's March 22 (the record: April
// 1) is listed ⚠ and F1 gated.
func TestRefCountNoUnitDeadlineGated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	f.reads = map[string]analysis.Label{"quote:21": lab("", analysis.KindCount, "", "1", "", "", ""), "value:21": lab("", analysis.KindCount, "", "1", "", "", "")}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	period := m{"entity": "Defendant", "metric": "answer period", "value": "21", "unit": nil, "date": nil, "class": "Count"}
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days",
			val("Defendant", "service date", "March 1, 2024", "Date"), period))},
		"draft_section": {section("The answer is due March 22, 2024 [F1].", "Due [F1].")},
	}), Config{})
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	out := deref(v.Output)
	if !strings.Contains(out, "The answer is due March 22, 2024") || !strings.Contains(out, "⚠ March 22, 2024 = March 1, 2024 [F1]") ||
		!strings.Contains(out, "not a period in days") || !gated {
		t.Errorf("calendar March 22 from a count used as days: not delivered ⚠ and gated (gated %v); stop %s\n%s", gated, stop(v), out)
	}
}
