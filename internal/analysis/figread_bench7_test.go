// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import "testing"

// bench7: three label checks misfired on a real reader's answers, each making a plain figure unclear
// (its values dropped or gated).
func TestBench7LabelChecks(t *testing.T) {
	t.Parallel()
	str := func(s string) *string { return &s }
	label := func(span, kind, cur, scale string) Label {
		return Label{Span: span, Kind: kind, Currency: cur, Scale: scale, Decimal: "none", Sign: "positive"}
	}
	// "21 months" read as a count with unit months: the reader's own unit, its words beside it
	p := PassageOf("q1", "Ostrowski paid the monthly dues for 21 months.")
	l := label("q1.s1", KindCount, "none", "1")
	l.Unit, l.UnitFrom = str("months"), str("months")
	if r, _ := ReadLabels([]Passage{p}, []Label{l}); r["q1.s1"].Unclear || r["q1.s1"].Kind != KindDuration || r["q1.s1"].Unit != "months" {
		t.Errorf("21 months: %+v", r["q1.s1"])
	}
	// the same with no words beside it for the unit stays a count, and unclear for its stray unit
	l.UnitFrom = str("years")
	if r, _ := ReadLabels([]Passage{p}, []Label{l}); !r["q1.s1"].Unclear {
		t.Errorf("a unit with no words beside it read clearly: %+v", r["q1.s1"])
	}
	// "$2,800 per month": money with the unit of its period is money, the unit dropped
	p = PassageOf("q1", "Monthly membership dues of $2,800 per month.")
	l = label("q1.s1", KindMoney, "USD", "1")
	l.Unit, l.UnitFrom = str("months"), str("per month")
	if r, _ := ReadLabels([]Passage{p}, []Label{l}); r["q1.s1"].Unclear || r["q1.s1"].Amount != "2800" || r["q1.s1"].Unit != "" {
		t.Errorf("$2,800 per month: %+v", r["q1.s1"])
	}
	// "$8,238,000" with its own trailing zeros cited as a scale: the digits written out, at scale 1
	p = PassageOf("q1", "The total excess profits are estimated at $8,238,000.")
	for _, from := range []string{"000", ",000"} {
		l = label("q1.s1", KindMoney, "USD", "1000")
		l.ScaleFrom = str(from)
		if r, _ := ReadLabels([]Passage{p}, []Label{l}); r["q1.s1"].Unclear || r["q1.s1"].Amount != "8238000" || r["q1.s1"].Scale != "" {
			t.Errorf("$8,238,000 scaled from %q: %+v", from, r["q1.s1"])
		}
	}
	// a scale from nothing, or from words the passage does not state beside it, stays unclear (r5 M2)
	for _, from := range []string{"", "in thousands", "238"} {
		l = label("q1.s1", KindMoney, "USD", "1000")
		l.ScaleFrom = str(from)
		if r, _ := ReadLabels([]Passage{p}, []Label{l}); !r["q1.s1"].Unclear || r["q1.s1"].Amount != "8238000" {
			t.Errorf("a scale from %q: %+v", from, r["q1.s1"])
		}
	}
}
