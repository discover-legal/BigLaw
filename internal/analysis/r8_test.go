// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"fmt"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// lab-r4 r8 verifier repros (critique/biglaw-lab-r4-r8-verifier.md) for the join, the labels and the
// calc, kept as regression tests.

// M1: what real ledgers put to the judge at a few budgets, and how many pairs past them are dropped.
func TestJoinBenchCandidateCounts(t *testing.T) {
	for _, name := range []string{"sec", "trust", "gatea"} {
		fs := read(bench6Ledger(t, name))
		all := len(rankPairs(fs))
		for _, calls := range []int{6, 16} {
			cands, dropped := Candidates(fs, CandidateLimit(calls), nil)
			if len(cands)+dropped != all || len(cands) != min(all, CandidateLimit(calls)) {
				t.Errorf("bench6 %s, %d calls: %d candidates + %d dropped, of %d pairs", name, calls, len(cands), dropped, all)
			}
			t.Logf("bench6 %s, %d judge calls: %d candidates, %d dropped (of %d pairs)", name, calls, len(cands), dropped, all)
		}
	}
}

// M1 (cap): a pair judged "yes" stays a conflict when later findings rank many closer pairs ahead of
// it: pairs judged already come first, whatever the cap.
func TestJoinYesStaysAheadOfTheCap(t *testing.T) {
	fs := []Finding{fh("$92,600", "Compensation was $92,600.", "referral", "Ostrowski", "compensation"),
		fh("$103,800", "Total paid: $103,800", "exhibit", "Ostrowski", "payments total")}
	cands, _ := Candidates(read(fs), testLimit, nil)
	if len(cands) != 1 {
		t.Fatalf("%d candidates", len(cands))
	}
	yes := cands[0]
	vs := map[string]Verdict{yes.ID: {Pair: yes.ID, SameQuantity: "yes", Reason: "r"}}
	for i := range 17 {
		fs = append(fs, fh(fmt.Sprintf("$%d,000", 300+i), fmt.Sprintf("A fee of $%d,000.", 300+i), fmt.Sprintf("doc%d", i), "Acme", "fee"))
	}
	judged := func(c Candidate) bool {
		return c.A.Canon == yes.A.Canon && c.B.Canon == yes.B.Canon && c.A.DocumentID == yes.A.DocumentID
	}
	again, dropped := Candidates(read(fs), testLimit, judged)
	if cs := Conflicts(again, vs); len(cs) != 1 {
		t.Errorf("the judged-yes pair %s is no longer a conflict (%d candidates, %d dropped)", yes.ID, len(again), dropped)
	}
	if cs, _, unjudged, _, _ := Join(context.Background(), testCall, read(fs), prior{judged}); len(cs) != 1 || unjudged != dropped+len(again)-1 {
		t.Errorf("%d conflicts, unjudged %d: the judged pair fell out or counted as not judged", len(cs), unjudged)
	}
}

// prior is a judge that has judged the pairs judged "yes" (and answers nothing new), as the engine's
// ledger does.
type prior struct{ judged func(Candidate) bool }

func (p prior) Judge(_ context.Context, _ agent.Call, cands []Candidate) (map[string]Verdict, agent.Usage, error) {
	out := map[string]Verdict{}
	for _, c := range cands {
		if p.judged(c) {
			out[c.ID] = Verdict{Pair: c.ID, SameQuantity: "yes", Reason: "earlier"}
		}
	}
	return out, agent.Usage{}, nil
}

func (p prior) Judged(context.Context, agent.Call) (func(Candidate) bool, error) {
	return p.judged, nil
}

// L1: a verdict's reason must quote both passages: a reason quoting neither, or one, is unclear.
func TestVerdictReasonQuotesBothPassages(t *testing.T) {
	fs := read([]Finding{
		fh("$92,600", "Ostrowski received undisclosed compensation payments totaling $92,600.", "referral", "Ostrowski", "compensation"),
		fh("$103,800", "Grand total of payments on behalf of Bellini: $103,800", "exhibit", "Ostrowski", "payments total"),
	})
	cands, _ := Candidates(fs, testLimit, nil)
	id := cands[0].ID
	for reason, want := range map[string]string{
		`A says "received undisclosed compensation payments totaling $92,600"; B says "Grand total of payments on behalf of Bellini".`: "yes",
		`A says "the parties agreed a $5,000,000 cap in the side letter"; B says "see the side letter".`:                               "unclear",
		`A says "received undisclosed compensation payments totaling $92,600"; B is the same figure.`:                                  "unclear",
		"": "unclear",
	} {
		if vs, _ := CheckVerdicts(cands, []Verdict{{Pair: id, SameQuantity: "yes", Reason: reason}}); vs[id].SameQuantity != want {
			t.Errorf("%q: %s, want %s", reason, vs[id].SameQuantity, want)
		}
	}
}

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
		{"Segment revenue (in millions)\n| North | 4.2 |", "in millions", 1, true},
		{"Revenue grew to $4.2 million in 2023.\nThe filing fee is $500 per application.", "million", 3, false}, // a sentence
		{"Revenue grew to $4.2 million\nFee\t500", "million", 2, false},                                         // the first line's own figure's scale
		{"Amounts in millions\nThe filing fee is 500 per application.", "in millions", 1, false},                // a prose line below
	} {
		if r := read(c.text, c.from, c.span); (r.Scale == "1000000" && !r.Unclear) != c.applied {
			t.Errorf("%q span %d: %+v, applied want %v", c.text, c.span, r, c.applied)
		}
	}
}

// H1: a duration carries its unit, a number is a bare number or multiple; a duration with no unit, or
// a unit on another kind, is unclear.
func TestDurationAndNumberLabels(t *testing.T) {
	ps := []Passage{PassageOf("q1", "within 21 days, at 1.5x, under Section 30")}
	days, none := "days", "fortnights"
	l1 := lbl("q1.s1", KindDuration, "none", "1", nil, "none", "positive")
	l1.Unit = &days
	l2 := lbl("q1.s2", KindNumber, "none", "1", nil, ".", "positive")
	l3 := lbl("q1.s3", KindDuration, "none", "1", nil, "none", "positive")
	l3.Unit = &none
	rs, _ := ReadLabels(ps, []Label{l1, l2, l3})
	if r := rs["q1.s1"]; r.Kind != KindDuration || r.Unit != "days" || r.Unclear {
		t.Errorf("21 days: %+v", r)
	}
	if r := rs["q1.s2"]; r.Kind != KindNumber || !r.Clear() || r.Amount != "1.5" {
		t.Errorf("1.5x: %+v", r)
	}
	if r := rs["q1.s3"]; !r.Unclear {
		t.Errorf("a duration in an unknown unit: %+v", r)
	}
	l2.Unit = &days
	if rs, _ := ReadLabels(ps, []Label{l2}); !rs["q1.s2"].Unclear {
		t.Errorf("a unit on a number: %+v", rs["q1.s2"])
	}
}

// H1 (E1's one-year check): date_add takes a period in weeks, months or years.
func TestDateAddPeriods(t *testing.T) {
	lit := func(x, unit string) Operand { return Operand{Literal: &x, Unit: &unit} }
	for _, c := range []struct{ n, unit, want string }{{"1", "years", "2026-03-31"}, {"1", "year", "2026-03-31"}, {"2", "months", "2025-05-31"}, {"-2", "weeks", "2025-03-17"}, {"21", "days", "2025-04-21"}} {
		w, err := Calc(nil, "date_add", []Operand{lit("2025-03-31", "date"), lit(c.n, c.unit)}, nil)
		if err != nil || w.Result != c.want {
			t.Errorf("%s %s: %s (%v), want %s", c.n, c.unit, w.Result, err, c.want)
		}
	}
	if _, err := Calc(nil, "business_days", []Operand{lit("2025-03-31", "date"), lit("1", "years")}, nil); err == nil {
		t.Error("business_days of years")
	}
}
