// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
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

// lab-r4 r8 verifier repros (critique/biglaw-lab-r4-r8-verifier.md) for the join, the labels and the
// calc, kept as regression tests.

// M2: a passage's first line gives the rows below it their scale only when it is a header (no
// sentence, no figure of its own the words scale) and the figure sits in a table row.
func TestHeaderScaleOnlyOverTableRows(t *testing.T) {
	read := func(text, from string, span int) Reading {
		rs, _ := ReadLabels([]Passage{PassageOf("q1", text)}, []Label{lbl(fmt.Sprintf("q1.s%d", span), KindMoney, "USD", "1000000", str(from), ".", "positive")})
		return rs[fmt.Sprintf("q1.s%d", span)]
	}
	for _, c := range []struct {
		text, from string
		span       int
		applied    bool
	}{
		{"Segment revenue (in millions)\nNorth\t4.2\nSouth\t3.9", "in millions", 2, true},
		{"Segment revenue (in millions)\n| North | 4.2 |\n| South | 3.9 |", "in millions", 1, true},
		{"Segment revenue (in millions)\nNorth\t4.2", "in millions", 1, false},                                                                              // one row is no table
		{"Amounts in millions\nThe filing fee, payable on filing, is $500 per application.\nThe late fee, payable later, is $50.", "in millions", 1, false}, // prose with commas (r9 L3)
		{"Revenue grew to $4.2 million in 2023.\nThe filing fee is $500 per application.", "million", 3, false},                                             // a sentence
		{"Revenue grew to $4.2 million\nFee\t500", "million", 2, false},                                                                                     // the first line's own figure's scale
		{"Amounts in millions\nThe filing fee is 500 per application.", "in millions", 1, false},                                                            // a prose line below
	} {
		if r := read(c.text, c.from, c.span); (r.Scale == "1000000" && !r.Unclear) != c.applied {
			t.Errorf("%q span %d: %+v, applied want %v", c.text, c.span, r, c.applied)
		}
	}
}

// H1 (r8, r9): a duration carries its unit and a number its multiple's sign, each with the words beside
// it that state them; a duration with no unit, a unit on another kind, or a unit or sign no adjacent
// words state ("Section 30" read as 30 days, "Section 12" as a multiple) is unclear.
func TestDurationAndNumberLabels(t *testing.T) {
	ps := []Passage{PassageOf("q1", "within 21 days, at 1.5x, under Section 30, thirty (30) days, a 21-day period, x 2")}
	label := func(span, kind, unit, from string) Label {
		l := lbl(span, kind, "none", "1", nil, map[bool]string{true: ".", false: "none"}[span == "q1.s2"], "positive")
		if unit != "" {
			l.Unit = &unit
		}
		if from != "" {
			l.UnitFrom = &from
		}
		return l
	}
	for _, c := range []struct {
		l     Label
		clear bool
	}{
		{label("q1.s1", KindDuration, "days", "days"), true},
		{label("q1.s2", KindNumber, "", "x"), true},
		{label("q1.s3", KindDuration, "days", "days"), false},       // "Section 30": the words after it are "thirty"
		{label("q1.s3", KindNumber, "", "Section"), false},          // a word before it is no sign
		{label("q1.s4", KindDuration, "days", "days"), true},        // "(30) days"
		{label("q1.s5", KindDuration, "days", "day"), true},         // "21-day"
		{label("q1.s6", KindNumber, "", "x"), true},                 // "x 2"
		{label("q1.s1", KindDuration, "fortnights", "days"), false}, // no such unit
		{label("q1.s1", KindDuration, "days", ""), false},           // no words
		{label("q1.s2", KindNumber, "days", "x"), false},            // a unit on a number
	} {
		rs, _ := ReadLabels(ps, []Label{c.l})
		r := rs[c.l.Span]
		if r.Unclear == c.clear {
			t.Errorf("%s %s %v from %v: %+v", c.l.Span, c.l.Kind, deref(c.l.Unit), deref(c.l.UnitFrom), r)
		}
		if c.clear && (r.UnitFrom == "" || c.l.Kind == KindDuration && r.Unit == "") {
			t.Errorf("%s: unit or its words not kept: %+v", c.l.Span, r)
		}
	}
}

// lab-r4 r9 verifier repros (critique/biglaw-lab-r4-r9-verifier.md M2, L1, L2, L3, residual a), kept
// as regression tests.

// L3: a caption over a prose line, commas or not, is no table header: the fee keeps scale 1 (the
// reader's scale from the caption is unclear).
func TestCaptionOverProseWithCommas(t *testing.T) {
	for _, text := range []string{
		"Amounts in millions\nThe filing fee, payable on filing, is $500 per application.",
		"Amounts in millions\nThe filing fee payable on filing is $500 per application.",
	} {
		rs, _ := ReadLabels([]Passage{PassageOf("q1", text)}, []Label{lbl("q1.s1", KindMoney, "USD", "1000000", str("in millions"), "none", "positive")})
		if r := rs["q1.s1"]; r.Scale == "1000000" && !r.Unclear {
			t.Errorf("%q: the prose line took the caption's scale: %+v", text, r)
		}
	}
}

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

// The measure, period and typed-entity labels are kept as given (folded): context the join's judge
// reads, never a key. A range's other end must be a span of the same passage.
func TestLabelsCarryMeasureAndPeriod(t *testing.T) {
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

// lab-r4 r6 verifier repros for the join (critique/biglaw-lab-r4-r6-verifier.md), and joins driven by
// deliberately wrong reader labels (r6 L5): what one wrong label can and cannot make the join assert.

// H1: a scale's source words must lie before the figure (a heading, a header, a lead-in) or right
// after its digits, and a clear match at a scale needs equivalent words on both sides (folded: case,
// punctuation, a plural); "fee" never matches "thousand", "(In Millions)" matches "million".
func TestR6ScaleWordsAndPlace(t *testing.T) {
	read := func(text, from, scale string) Reading {
		rs, _ := ReadLabels([]Passage{PassageOf("q1", text)}, []Label{lbl("q1.s1", KindMoney, "USD", scale, str(from), ".", "positive")})
		return rs["q1.s1"]
	}
	if r := read("The filing fee is $500 per application. Program costs (in thousands): 1,200.", "in thousands", "1000"); !r.Unclear || r.Scale != "" {
		t.Errorf("a scale from a later figure's header applied: %+v", r)
	}
	if r := read("Each holder received $5 per share.", "share", "1000000"); !r.Unclear || r.Scale != "" {
		t.Errorf("a scale from a word after the figure (not right after it) applied: %+v", r)
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{{"(In Millions)", "million", true}, {"in thousands", "Thousand", true}, {"fee", "thousand", false}, {"($000)", "$000", true}, {"($000)", "thousand", false}, {"mm", "million", false}} {
		x := Reading{Kind: KindMoney, Currency: "USD", Amount: "500000", Scale: "1000", ScaleFrom: c.a}
		y := Reading{Kind: KindMoney, Currency: "USD", Amount: "500000", Scale: "1000", ScaleFrom: c.b}
		if same, soft := SameFigure(x, y); same != c.same || !same && !soft {
			t.Errorf("%q vs %q: same %v unclear %v, want same %v (else unclear)", c.a, c.b, same, soft, c.same)
		}
	}
}

// lab-r4 r11 verifier: statesUnit never panics, and a unit it accepts (not a multiple's sign) is
// words written after the span.
func FuzzStatesUnitWordsAfter(f *testing.F) {
	f.Add("An answer is due within 21 business days.", "business days", false)
	f.Add("x 1.5 and 1.5× and Rule 12(b) of Section 30 of", "×", true)
	f.Add("1.5x", "x", true)
	f.Fuzz(func(t *testing.T, text, from string, multiple bool) {
		for _, s := range PassageOf("q1", text).Spans {
			if statesUnit(text, s, from, multiple) && !multiple && !strings.Contains(asciiLower(text[s.End:]), asciiLower(strings.TrimSpace(from))) {
				t.Fatalf("%q after %q states %q", text, s.Text, from)
			}
		}
	})
}
