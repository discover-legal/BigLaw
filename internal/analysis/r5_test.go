// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"strings"
	"testing"
)

// lab-r4 r5 verifier repros (critique/biglaw-lab-r4-r5-verifier.md) and adversarial labels: what a
// wrong or malicious figure reader can make ReadLabels and the join assert.

// H1: a scale's source must be words of the span's own passage, whole words, not the span itself,
// never a span id or another passage's words; one that is not is never applied (M2: the figure reads
// at scale 1, unclear). A sourced scale is applied, but a scaled figure states another clearly only
// when that one is read at the same scale.
func TestR5ScaleSourceIsItsOwnWords(t *testing.T) {
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

// The measure, period and typed-entity labels are kept as given (folded), and a range's other end
// must be a span of the same passage.
func TestR5LabelsCarryMeasureAndPeriod(t *testing.T) {
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

// M4: the label join keys on the reader's party, and the extractor's typed entities still stand: a
// payer read as the party of two employees' salaries is no conflict between them.
func TestR5LabelJoinRespectsTypedEntity(t *testing.T) {
	salary := func(fid, doc, quote, who, amount, value string) Finding {
		return fnd(fid, doc, quote, nil, Value{Entity: who, Metric: "annual base salary", Value: value, Class: "Money", Fig: figOf(KindMoney, "USD", amount, "Acme Corp", "annual base salary", "none")})
	}
	fs := []Finding{
		salary("F1", "hr", "Acme Corp shall pay the Chief Executive Officer an annual base salary of $410,000.", "Jane Moss", "410000", "$410,000"),
		salary("F2", "comp", "Acme Corp shall pay the Chief Financial Officer an annual base salary of $395,000.", "Raj Patel", "395000", "$395,000"),
	}
	if cs := Join(fs, nil); len(cs) > 0 {
		t.Errorf("two employees' salaries (typed entities differ) joined as one: %v", pairs(cs))
	}
	fs[1].Values[0].Fig.TypedEntity = "other" // the reader says Raj Patel is not its party
	if cs := Join(fs, nil); len(cs) > 0 {
		t.Errorf("a figure whose typed entity the reader calls another party joined: %v", pairs(cs))
	}
	fs[1].Values[0].Entity = "Jane Moss" // one employee's salary in two documents
	fs[1].Values[0].Fig.TypedEntity = ""
	if cs := Join(fs, nil); len(cs) != 1 {
		t.Errorf("one employee's salary stated two ways: %v", pairs(cs))
	}
}

// L2: what a figure measures and the period it is for are the reader's labels: one party's two
// deposits, or one revenue line's two fiscal years, are not one figure stated twice; the same measure
// for the same period is.
func TestR5JoinOnMeasureAndPeriod(t *testing.T) {
	rev := func(fid, doc, period, amount, value string) Finding {
		return fnd(fid, doc, "Alpha Corp reported annual revenue of "+value+" for "+period+".", nil,
			Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: value, Class: "Money", Fig: figOf(KindMoney, "USD", amount, "Alpha Corp", "annual revenue", period)})
	}
	if cs := Join([]Finding{rev("F1", "10k", "fiscal 2023", "12300000", "$12,300,000"), rev("F2", "deck", "fiscal 2024", "12800000", "$12,800,000")}, nil); len(cs) > 0 {
		t.Errorf("two fiscal years' revenue joined as one: %v", pairs(cs))
	}
	if cs := Join([]Finding{rev("F1", "10k", "fiscal 2024", "12300000", "$12,300,000"), rev("F2", "deck", "Fiscal 2024", "12800000", "$12,800,000")}, nil); len(cs) != 1 {
		t.Errorf("one year's revenue stated two ways: %v", pairs(cs))
	}
	if cs := Join([]Finding{rev("F1", "10k", "fiscal 2024", "12300000", "$12,300,000"), rev("F2", "deck", "", "12800000", "$12,800,000")}, nil); len(cs) > 0 {
		t.Errorf("a figure whose period one side does not state asserted a conflict: %v", pairs(cs))
	}
	dep := func(fid, doc, quote, measures, amount, value string) Finding {
		return fnd(fid, doc, quote, nil, Value{Entity: "Buyer", Metric: "deposit", Value: value, Class: "Money", Fig: figOf(KindMoney, "USD", amount, "Buyer", measures, "none")})
	}
	two := []Finding{
		dep("F1", "spa", "The Buyer paid an initial deposit of $250,000 on signing.", "initial deposit", "250000", "$250,000"),
		dep("F2", "amendment", "The Buyer paid an additional deposit of $260,000 on extension.", "additional deposit", "260000", "$260,000"),
	}
	if cs := Join(two, nil); len(cs) > 0 {
		t.Errorf("two deposits of one party under one extractor label asserted as a conflict: %v", pairs(cs))
	}
	two[1].Values[0].Fig.Measures = "Initial Deposit"
	if cs := Join(two, nil); len(cs) != 1 {
		t.Errorf("one deposit stated two ways: %v", pairs(cs))
	}
	two[1].Values[0].Fig.Measures = ""
	if cs := Join(two, nil); len(cs) > 0 {
		t.Errorf("a figure the reader did not say the measure of joined: %v", pairs(cs))
	}
	if !strings.Contains(fold("The  Initial-Deposit"), "initial deposit") {
		t.Error(fold("The  Initial-Deposit"))
	}
}
