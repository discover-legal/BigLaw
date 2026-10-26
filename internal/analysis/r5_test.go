// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"testing"
)

// lab-r4 r5 verifier repros (critique/biglaw-lab-r4-r5-verifier.md) and adversarial labels: what a
// wrong or malicious figure reader can make ReadLabels and the join assert.

// H1: a scale's source must be words of the span's own passage, whole words, not the span itself,
// never a span id or another passage's words; one that is not is never applied (M2: the figure reads
// at scale 1, unclear). A sourced scale is applied, but a scaled figure states another clearly only
// when that one is read at the same scale.
func TestR5ScaleSourceIsItsOwnWords(t *testing.T) {
	t.Parallel()
	q := "The filing fee is $500 per application."
	ps := []Passage{PassageOf("q1", q), PassageOf("v1", "$500,000"), PassageOf("q2", "Amounts in thousands.")}
	typed := Reading{Kind: KindMoney, Currency: "USD", Amount: "500000"}
	for _, from := range []string{"", "e", "ee", "v1.s1", "q1.s1", "500", "$500", "in thousands", "q2.s1", " "} {
		l := lbl("q1.s1", KindMoney, "USD", "1000", str(from), "none", "positive")
		if from == "" {
			l.ScaleFrom = nil
		}
		rs, _ := ReadLabels(ps, []Label{l})
		r := rs["q1.s1"]
		if !r.Unclear || r.Amount != "500" || r.Scale != "" {
			t.Errorf("scaleFrom %q: the scale was applied (or the reading is clear): %+v", from, r)
		}
		if same, soft := SameFigure(r, typed); same || soft {
			t.Errorf("scaleFrom %q: $500 matches a typed $500,000 (same %v, unclear %v)", from, same, soft)
		}
	}
	for _, from := range []string{"fee", "the", "per application", "FILING"} { // words of the passage, not a scale: applied, but no clear match
		rs, _ := ReadLabels(ps, []Label{lbl("q1.s1", KindMoney, "USD", "1000", str(from), "none", "positive")})
		r := rs["q1.s1"]
		if r.Amount != "500000" || r.Scale != "1000" {
			t.Errorf("scaleFrom %q: %+v", from, r)
		}
		if same, soft := SameFigure(r, typed); same || !soft {
			t.Errorf("scaleFrom %q: $500 read in thousands against a typed $500,000 (scale 1): same %v, unclear %v (want a gated match)", from, same, soft)
		}
	}
	header := PassageOf("q2", "Revenue (In Millions)\t4.2")
	typedM := PassageOf("v1", "$4.2 million")
	rs, _ := ReadLabels([]Passage{header, typedM}, []Label{
		lbl("q2.s1", KindMoney, "USD", "1000000", str("In Millions"), ".", "positive"),
		lbl("v1.s1", KindMoney, "USD", "1000000", str("million"), ".", "positive"),
	})
	if same, _ := SameFigure(rs["q2.s1"], rs["v1.s1"]); !same {
		t.Errorf("one scale in two writings is not one figure: %+v %+v", rs["q2.s1"], rs["v1.s1"])
	}
}

// The measure, period and typed-entity labels are kept as given (folded): context the join's judge
// reads, never a key. A range's other end must be a span of the same passage.
func TestLabelsCarryMeasureAndPeriod(t *testing.T) {
	t.Parallel()
	ps := []Passage{PassageOf("q1", "between 3 and 5 per cent for fiscal 2024"), PassageOf("q2", "7")}
	l := lbl("q1.s1", KindPercent, "none", "1", nil, "none", "positive")
	l.Measures, l.Period, l.TypedEntity, l.RangeWith = str("  management   fee "), str("fiscal 2024"), str("party"), str("q2.s1")
	other := lbl("q1.s2", KindPercent, "none", "1", nil, "none", "positive")
	other.RangeWith, other.TypedEntity = str("q1.s1"), str("someone")
	rs, _ := ReadLabels(ps, []Label{l, other})
	if r := rs["q1.s1"]; r.Measures != "management fee" || r.Period != "fiscal 2024" || r.TypedEntity != "party" || r.RangeWith != "" {
		t.Errorf("labels: %+v", r)
	}
	if r := rs["q1.s2"]; r.RangeWith != "q1.s1" || r.TypedEntity != "" {
		t.Errorf("a range's end of its own passage, an unknown typed-entity answer: %+v", r)
	}
}
