// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"slices"
	"testing"
)

// lab-r4 r9 verifier repros (critique/biglaw-lab-r4-r9-verifier.md M2, L1, L2, L3, residual a), kept
// as regression tests.

// M2: the ranking is a function of its input: bench6 SEC ranks the same way on 50 calls, and every
// cap's candidate set (what the judge is asked, and the cache keys it uses) with it.
func TestRankPairsDeterministic(t *testing.T) {
	fs := read(bench6Ledger(t, "sec"))
	ids := func() []string {
		var out []string
		for _, c := range rankPairs(fs) {
			out = append(out, c.ID)
		}
		return out
	}
	first := ids()
	for i := range 50 {
		if got := ids(); !slices.Equal(got, first) {
			k := 0
			for k < len(got) && got[k] == first[k] {
				k++
			}
			t.Fatalf("run %d ranks differently from position %d (%s vs %s)", i, k, got[k], first[k])
		}
	}
	for _, name := range []string{"trust", "gatea"} {
		gs := read(bench6Ledger(t, name))
		a, _ := Candidates(gs, 120, nil)
		for range 10 {
			if b, _ := Candidates(gs, 120, nil); !slices.EqualFunc(a, b, func(x, y Candidate) bool { return x.ID == y.ID }) {
				t.Fatalf("bench6 %s: the candidates at a cap of 120 vary", name)
			}
		}
	}
}

// L2: closeness orders a value's counterparts even when their contexts share no word: $100,000's
// first counterpart is $100,100 (0.1% apart), not $1,000.
func TestClosenessOrdersWithoutSharedWords(t *testing.T) {
	fs := read([]Finding{
		fh("$100,000", "Paid $100,000.", "d1", "Alpha", "xx"),
		fh("$1,000", "Owed $1,000.", "d2", "Beta", "yy"),
		fh("$100,100", "Owed $100,100.", "d3", "Gamma", "zz"),
	})
	for _, c := range rankPairs(fs) {
		if c.A.Value == "$100,000" || c.B.Value == "$100,000" {
			if c.A.Value != "$100,100" && c.B.Value != "$100,100" {
				t.Errorf("$100,000's first counterpart is %s~%s", c.A.Value, c.B.Value)
			}
			break
		}
	}
}

// L1: words both passages share ("in accordance with the Agreement") quote neither: a reason must
// quote words that are one side's alone, for each side.
func TestReasonBoilerplateQuotesNeither(t *testing.T) {
	fs := read([]Finding{
		fh("$291,400", "In accordance with the Agreement, the Adviser paid commissions of $291,400 to Lakeshore.", "letter", "Lakeshore", "commissions"),
		fh("$310,000", "In accordance with the Agreement, a fee of $310,000 was invoiced for audit services.", "schedule", "Lakeshore", "audit fee"),
	})
	cands, _ := Candidates(fs, testLimit, nil)
	id := cands[0].ID
	for reason, want := range map[string]string{
		`Both say "in accordance with the agreement", so they are the same.`:                                             "unclear",
		`A says "the Adviser paid commissions of $291,400"; B says "a fee of $310,000 was invoiced for audit services".`: "yes",
	} {
		if vs, _ := CheckVerdicts(cands, []Verdict{{Pair: id, SameQuantity: "yes", Reason: reason}}); vs[id].SameQuantity != want {
			t.Errorf("%q: %s, want %s", reason, vs[id].SameQuantity, want)
		}
	}
}

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

// Residual a: the values a join this size leaves without any counterpart judged are counted (what the
// Limitations line leads with), and fall as the budget grows.
func TestJoinCountsUncoveredValues(t *testing.T) {
	fs := read(bench6Ledger(t, "sec"))
	last := -1
	for _, calls := range []int{2, 6, 15, 16} {
		cands, dropped, uncovered := CandidatesCover(fs, CandidateLimit(calls), nil)
		t.Logf("bench6 sec, %d judge calls: %d candidates, %d dropped, %d values with no counterpart judged", calls, len(cands), dropped, uncovered)
		if last >= 0 && uncovered > last || uncovered == 0 {
			t.Errorf("%d calls: %d uncovered values (before: %d)", calls, uncovered, last)
		}
		last = uncovered
	}
	if _, _, u := CandidatesCover(fs, len(rankPairs(fs)), nil); u != 0 {
		t.Errorf("every pair asked, %d values uncovered", u)
	}
}
