// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"testing"
)

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
