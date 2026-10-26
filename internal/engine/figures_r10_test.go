// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r10 verifier repros (critique/biglaw-lab-r4-r10-verifier.md H1, M1, L2, L5), kept as
// regression tests.

// M1: a memo period is the cited one only when their unit words agree as written: "21 business days"
// read in days traces no "within 21 days" (limited: listed first, its finding gated).
func TestMemoDaysFromBusinessDaysLimited(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 21 business days."
	known := []analysis.Reading{{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "business days", Amount: "21", Text: "21"}}
	memo := "An answer is due within 21 days of service."
	days := "days"
	l := lab("m.s1", analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &days, &days
	got := g8(memo, quote+"\n", 0, nil, memoRead(memo, l), known, nil, nil, map[string]bool{"F1": true}, nil)
	if len(got) != 1 || got[0].limited != limitedUnit {
		t.Errorf("%q traced to \"21 business days\" read in days: %+v", memo, got)
	}
}

// M1, L2: "Section 30 of" read as 30 days citing "of" (a word beside it the reader may cite: Go checks
// only where and its shape) never traces the memo's "within 30 days": the words differ.
func TestSectionOfIsNoMemoPeriod(t *testing.T) {
	quote := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days."
	days, of := "days", "of"
	l := lab("q1.s1", analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &days, &of
	rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", quote)}, []analysis.Label{l})
	memo := "An answer is due within 30 days of service."
	ml := lab("m.s1", analysis.KindDuration, "", "1", "", "", "")
	ml.Unit, ml.UnitFrom = &days, &days
	if got := g8(memo, quote+"\n", 0, nil, memoRead(memo, ml), []analysis.Reading{rs["q1.s1"]}, nil, nil, map[string]bool{"F1": true}, nil); len(got) != 1 {
		t.Errorf("%q traced to \"Section 30 of\": %+v", memo, got)
	}
}

// L2: a letter in parentheses after the digits ("Rule 12(b)") is no multiple's sign, nor a unit.
func TestRuleLetterIsNoMultiple(t *testing.T) {
	q := "Under Rule 12(b) the Buyer shall pay $1,000,000 within 10 days."
	b, days := "b", "days"
	for _, l := range []analysis.Label{lab("q1.s1", analysis.KindNumber, "", "1", "", "", ""), lab("q1.s1", analysis.KindDuration, "", "1", "", "", "")} {
		l.UnitFrom = &b
		if l.Kind == analysis.KindDuration {
			l.Unit = &days
		}
		if rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{l}); !rs["q1.s1"].Unclear {
			t.Errorf("\"Rule 12(b)\" read as a %s of %q: %+v", l.Kind, b, rs["q1.s1"])
		}
	}
}

// L5: a memo number the reader read as an identifier traces as written, but when a cited quantity
// states the same digits it is listed for the lawyer to check first.
func TestOtherWithAQuantitysDigitsListed(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 30 days."
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}},
		Figures: []analysis.Reading{{Span: "q1.s4", Text: "30", Start: 60, End: 62, Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "30"}}}
	sec := Section{Markdown: "An answer is due within 30 days [F1]."} // the memo's "30" mislabelled an identifier
	body, _ := memoBody(sec)
	at := strings.Index(body, "30")
	sec.Read = &SectionRead{Hash: textHash(body), Figs: []analysis.Reading{{Span: "m.s1", Text: "30", Start: at, End: at + 2, Kind: analysis.KindOther, Amount: "30"}}}
	lines, _, _, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, nil)
	if len(lines) != 0 || len(computed) != 1 || !computed[0].Check || !strings.Contains(computed[0].Text, "“30” in the memo is read as an identifier and traces as written, but [F1] “") {
		t.Errorf("lint %q, computed %+v", lines, computed)
	}
}

// H1 e2e: the deadline written without its year ("March 22") is in the block too (G8 decides what a
// section states, whatever the form).
func TestBusinessDaysDeadlineNoYearDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	days, from := "days", "business days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &from
	f.reads = map[string]analysis.Label{"quote:21": wrong}
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("21", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days"))},
		"draft_section": {section("The answer is due March 22 [F1].", "Due [F1].")},
	}), Config{})
	if out := deref(v.Output); !strings.Contains(out, "The answer is due March 22 [F1].") || !strings.Contains(out, "⚠ March 22, 2024 = March 1, 2024 [F1]") {
		t.Errorf("\"March 22\" (21 calendar days; the record: 21 business days) delivered without its computation; stop %s\n%s", stop(v), out)
	}
}

// M1 e2e: the memo's "within 21 days" for the record's "21 business days" read in days: listed first
// and its finding gated, never traced silently.
func TestMemoDaysFromBusinessDaysDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	days, from := "days", "business days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &from
	f.reads = map[string]analysis.Label{"quote:21": wrong}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days"))},
		"draft_section": {section("The answer is due within 21 days of service [F1].", "Due [F1].")},
	}), Config{})
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	if out := deref(v.Output); strings.Contains(out, "within 21 days of service") && (!strings.Contains(out, "⚠ 21 days in the memo (its words “days”) is the cited period [F1]") || !gated) {
		t.Errorf("\"within 21 days\" (the record: 21 business days) delivered without the mismatch, or ungated (gated %v); stop %s\n%s", gated, stop(v), out)
	}
}
