// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"strings"
	"testing"
)

func lbl(span, kind, cur, scale string, from *string, dec, sign string) Label {
	return Label{Span: span, Kind: kind, Currency: cur, Scale: scale, ScaleFrom: from, Decimal: dec, Sign: sign}
}

func str(s string) *string { return &s }

// TestReadLabelsChecksEveryLabel (lab-r4 r4, owner decision): the figure reader may only label the
// spans it was given; its scale must come from a span or words the passage states and be a power of
// ten; its decimal mark must fit the digits as written; a sign it gives may not contradict a written
// minus. What it cannot be held to is unclear (gated), and an amount is always the written digits.
func TestReadLabelsChecksEveryLabel(t *testing.T) {
	q := PassageOf("q1", "Revenue (in thousands)\t4,000 and a fee of $500 and -1,250 and 1,200")
	if len(q.Spans) != 4 {
		t.Fatalf("spans %+v", q.Spans)
	}
	rs, notes := ReadLabels([]Passage{q}, []Label{
		lbl("q1.s1", KindMoney, "USD", "1000", str("in thousands"), "none", "positive"),
		lbl("q1.s2", KindMoney, "USD", "1", nil, "none", "positive"),
		lbl("q1.s3", KindMoney, "USD", "1", nil, "none", "positive"), // a written minus read as positive
		lbl("q1.s4", KindCount, "none", "1", nil, ",", "positive"),   // "1,200" with a decimal comma: 1.2
		lbl("q1.s9", KindMoney, "USD", "1", nil, "none", "positive"), // no such span
		lbl("q2.s1", KindMoney, "USD", "1", nil, "none", "positive"), // nor passage
	})
	if len(notes) != 2 || !strings.Contains(notes[0], "q1.s9") || !strings.Contains(notes[1], "q2.s1") {
		t.Errorf("labels for spans not given were not rejected: %q", notes)
	}
	if _, ok := rs["q1.s9"]; ok {
		t.Error("a reading for a span not given")
	}
	if r := rs["q1.s1"]; !r.Clear() || r.Amount != "4000000" || r.Currency != "USD" || r.ScaleFrom != "in thousands" {
		t.Errorf("a header-scaled figure: %+v", r)
	}
	if r := rs["q1.s2"]; !r.Clear() || r.Amount != "500" {
		t.Errorf("$500: %+v", r)
	}
	if r := rs["q1.s3"]; !r.Unclear || r.Amount != "-1250" {
		t.Errorf("a written minus read as positive is unclear (its amount as written): %+v", r)
	}
	if r := rs["q1.s4"]; !r.Clear() || r.Amount != "1.2" {
		t.Errorf("a decimal comma: %+v", r)
	}
	for name, l := range map[string]Label{
		"a scale from words the passage does not state": lbl("q1.s2", KindMoney, "USD", "1000", str("in millions"), "none", "positive"),
		"a scale from nothing":                          lbl("q1.s2", KindMoney, "USD", "1000", nil, "none", "positive"),
		"a scale that is no power of ten":               lbl("q1.s2", KindMoney, "USD", "15.554", str("fee"), "none", "positive"),
		"money with no currency code":                   lbl("q1.s2", KindMoney, "dollars", "1", nil, "none", "positive"),
		"a currency on a count":                         lbl("q1.s2", KindCount, "USD", "1", nil, "none", "positive"),
		"a decimal mark the digits do not have":         lbl("q1.s1", KindMoney, "USD", "1", nil, ".", "positive"),
		"an unclear kind":                               lbl("q1.s2", KindUnclear, "none", "1", nil, "none", "positive"),
		"an unclear sign":                               lbl("q1.s2", KindMoney, "USD", "1", nil, "none", "unclear"),
		"an unknown kind":                               lbl("q1.s2", "share", "none", "1", nil, "none", "positive"),
	} {
		rs, _ := ReadLabels([]Passage{q}, []Label{l})
		if r := rs[l.Span]; r.Clear() {
			t.Errorf("%s read clearly: %+v", name, r)
		}
	}
	twice, notes := ReadLabels([]Passage{q}, []Label{lbl("q1.s2", KindMoney, "USD", "1", nil, "none", "positive"), lbl("q1.s2", KindCount, "none", "1", nil, "none", "positive")})
	if r := twice["q1.s2"]; !r.Unclear || len(notes) != 1 {
		t.Errorf("a span labelled twice: %+v %q", r, notes)
	}
	none, _ := ReadLabels([]Passage{q}, nil)
	if r := none["q1.s2"]; !r.Unclear || r.Amount != "" {
		t.Errorf("an unlabelled span: %+v", r)
	}
}

// TestSameFigure: two readings state one figure when kind, currency and exact amount agree; one
// unclear reading agreeing on what can be read is an unclear match (a lawyer confirms it), never a
// clear one; a count never states a sum.
func TestSameFigure(t *testing.T) {
	money := Reading{Kind: KindMoney, Currency: "USD", Amount: "2000000"}
	for name, c := range map[string]struct {
		b              Reading
		same, unclearO bool
	}{
		"the same sum":          {money, true, false},
		"a share count":         {Reading{Kind: KindCount, Amount: "2000000"}, false, false},
		"another currency":      {Reading{Kind: KindMoney, Currency: "EUR", Amount: "2000000"}, false, false},
		"another amount":        {Reading{Kind: KindMoney, Currency: "USD", Amount: "2000"}, false, false},
		"an unclear reading":    {Reading{Kind: KindUnclear, Amount: "2000000", Unclear: true}, false, true},
		"an unclear sign":       {Reading{Kind: KindMoney, Currency: "USD", Amount: "-2000000", Unclear: true}, false, true},
		"no amount":             {Reading{Kind: KindMoney, Currency: "USD", Unclear: true}, false, false},
		"an unclear other kind": {Reading{Kind: KindCount, Amount: "2000000", Unclear: true}, false, false},
	} {
		if same, unclear := SameFigure(money, c.b); same != c.same || unclear != c.unclearO {
			t.Errorf("%s: same %v unclear %v", name, same, unclear)
		}
	}
}

// TestCanonNeedsAClearReading: a value is a comparable quantity only by its figure reader's clear
// reading of its class; with none, an unclear one, or a kind other than its class, it is not compared.
func TestCanonNeedsAClearReading(t *testing.T) {
	v := Value{Value: "$2,000,000", Class: "Money"}
	for _, f := range []*Reading{nil, {Kind: KindMoney, Currency: "USD", Amount: "2000000", Unclear: true}, {Kind: KindCount, Amount: "2000000"}} {
		v.Fig = f
		if _, c, _, ok := Canon(v); ok {
			t.Errorf("%+v: canon %s", f, c)
		}
	}
	v.Fig = fig(KindMoney, "EUR", "2000000", "")
	if _, c, _, ok := Canon(v); !ok || c != "eur:2000000" {
		t.Errorf("canon %s %v", c, ok)
	}
}
