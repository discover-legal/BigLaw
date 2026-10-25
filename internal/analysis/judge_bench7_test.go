// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"strings"
	"testing"
)

// bench7 (SEC referral, a real model's judge): all four of its "yes" verdicts were false conflicts,
// and its two true targets were judged "no". Each is scripted here with the judge's own reason.

// candidateOf is the candidate of the two findings' values, if Candidates proposes it.
func candidateOf(t *testing.T, a, b Finding) (Candidate, bool) {
	t.Helper()
	cands, _ := Candidates(read([]Finding{a, b}), testLimit, nil)
	for _, c := range cands {
		if c.A.FID == a.FID && c.B.FID == b.FID || c.A.FID == b.FID && c.B.FID == a.FID {
			return c, true
		}
	}
	return Candidate{}, false
}

func TestBench7FalseYesesAreNotConflicts(t *testing.T) {
	for _, c := range []struct {
		name   string
		a, b   Finding
		reason string
	}{
		{"2,614 vs 5,776: the passages' shared figure", fh("5,776", "For 2,614 of the 5,776 omnibus equity trades (45.2%), WCA was unable to produce contemporaneous allocation records", "referral", "WCA", "total omnibus equity trades"),
			fh("2,614", "For 2,614 of the 5,776 omnibus equity trades executed during the Review Period (2,614 ÷ 5,776 = 45.2%), WCA was unable to produce any written record", "review", "WCA", "trades without allocation records"),
			`A says "For 2,614 of the 5,776 omnibus equity trades (45.2%), WCA was unable to produce contemporaneous allocation records"; B says "For 2,614 of the 5,776 omnibus equity trades executed during the Review Period (2,614 ÷ 5,776 = 45.2%), WCA was unable to produce any written record": both passages state the same count of 2,614 trades lacking allocation documentation.`},
		{"6% vs 20%: the hurdle against the allocation", fh("6%", "an incentive allocation equal to twenty percent (20%) of net new profits above a six percent (6%) annualized preferred return (the 'hurdle rate')", "lpa", "Oceanic Fund I LP", "preferred return hurdle rate"),
			fh("20%", "a performance allocation of 20% of net profits above the 6% preferred return hurdle", "adv", "WCA", "performance allocation"),
			`A says "six percent (6%) annualized preferred return (the 'hurdle rate')"; B says "performance allocation of 20% of net profits above the 6% preferred return hurdle": both passages describe the same hurdle rate of 6% for Oceanic Fund I LP's performance calculations.`},
		{"$45,000.00 vs $22.2 million: a fee against a valuation", fh("$45,000.00", "Club initiation fee paid to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini 05/03/2022 1 $45,000.00", "bank", "Marcus T. Bellini", "initiation fee"),
			fh("$22.2 million", "Whitmore holds a 12% limited partnership interest in Oceanic Fund I LP valued at approximately $22.2 million as of December 31, 2023", "referral", "Whitmore", "valuation"),
			`A says "Club initiation fee paid to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini 05/03/2022 1 $45,000.00"; B says "Whitmore holds a 12% limited partnership interest in Oceanic Fund I LP valued at approximately $22.2 million as of December 31, 2023": both state the same valuation of Whitmore's partnership interest at $22.2 million as of December 31, 2023.`},
	} {
		x, ok := candidateOf(t, c.a, c.b)
		if !ok {
			t.Errorf("%s: not proposed", c.name)
			continue
		}
		vs, _ := CheckVerdicts([]Candidate{x}, []Verdict{{Pair: x.ID, SameQuantity: "yes", Reason: c.reason}})
		if v := vs[x.ID]; v.SameQuantity == "yes" {
			t.Errorf("%s: a reason naming one figure (or another's) asserted a conflict: %+v", c.name, v)
		}
	}
	// one amount, one written with its cents, the other signed as a debit: never put to the judge
	if x, ok := candidateOf(t, fh("-$45,000.00", "ACH Debit Bayshore Palms Golf & Country Club Initiation Fee - Bellini, Marcus T. $45,000.00", "bank", "Kevin Ostrowski", "initiation fee"),
		fh("$45,000", "Undisclosed compensation received by Bellini from Ostrowski ($45,000 initiation fee + $47,600 in monthly dues for 17 months)", "referral", "Bellini", "initiation fee")); ok {
		t.Errorf("$45,000.00 against $45,000 proposed: %+v", x)
	}
}

func TestBench7TrueTargetsReachAYes(t *testing.T) {
	for _, c := range []struct {
		a, b   Finding
		reason string
	}{
		{fh("4,217", "The Division's Office of Quantitative Research (OQR) analyzed 4,217 equity trades executed through the omnibus account", "referral", "WCA", "omnibus equity trades analyzed"),
			fh("4,312", "A total of 4,312 omnibus equity trades were identified from Meridian Clearing Corp. transaction records", "analysis", "WCA", "omnibus equity trades reviewed"),
			`A says "analyzed 4,217 equity trades executed through the omnibus account"; B says "A total of 4,312 omnibus equity trades were identified": one review population of WCA's omnibus trades stated as 4,217 and as 4,312.`},
		{fh("$92,600", "The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600", "referral", "Bellini", "undisclosed compensation"),
			fh("$103,800.00", "Total of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini $103,800.00", "bank", "Bellini", "payments on behalf of Bellini"),
			`A says "payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit"; B says "Total of all payments from Ostrowski personal account to Bayshore Palms": Ostrowski's payments for Bellini stated as $92,600 and as $103,800.00.`},
	} {
		x, ok := candidateOf(t, c.a, c.b)
		if !ok {
			t.Errorf("%s|%s not proposed", c.a.Values[0].Value, c.b.Values[0].Value)
			continue
		}
		vs, _ := CheckVerdicts([]Candidate{x}, []Verdict{{Pair: x.ID, SameQuantity: "yes", Reason: c.reason}})
		if cs := Conflicts([]Candidate{x}, vs); len(cs) != 1 || !strings.Contains(cs[0].Reason, c.a.Values[0].Value) {
			t.Errorf("%s|%s: %+v", c.a.Values[0].Value, c.b.Values[0].Value, vs[x.ID])
		}
	}
}

// bench7 r1 (verifier LOW-3): a yes's quotations are the runs its passages hold, not what sits between
// quote marks: a passage that quotes a figure itself never turns that figure into the reason's own
// words; and a figure is named by its amount ("$103,800" names "103,800.00").
func TestNamesValuesReadsQuotationsByPassage(t *testing.T) {
	a := fh("5,776", "WCA executed 5,776 omnibus equity trades during the Review Period", "referral", "WCA", "total omnibus equity trades")
	b := fh("2,614", `the review lists "2,614" omnibus equity trades with no allocation record`, "review", "WCA", "trades without allocation records")
	x, ok := candidateOf(t, a, b)
	if !ok {
		t.Fatal("not proposed")
	}
	reason := `A says "WCA executed 5,776 omnibus equity trades"; B says "the review lists "2,614" omnibus equity trades with no allocation record": both state the same count of 5,776 trades.`
	if vs, _ := CheckVerdicts([]Candidate{x}, []Verdict{{Pair: x.ID, SameQuantity: "yes", Reason: reason}}); vs[x.ID].SameQuantity == "yes" {
		t.Errorf("a yes naming only 5,776 in its own words passed: %+v", vs[x.ID])
	}
	c := fh("$92,600", "payments made by Ostrowski to Bayshore Palms for Bellini's benefit were approximately $92,600", "referral", "Bellini", "undisclosed compensation")
	d := fh("$103,800.00", "Total of all payments from Ostrowski personal account to Bayshore Palms on behalf of Bellini $103,800.00", "bank", "Bellini", "payments on behalf of Bellini")
	y, ok := candidateOf(t, c, d)
	if !ok {
		t.Fatal("not proposed")
	}
	reason = `A says "payments made by Ostrowski to Bayshore Palms"; B says "Total of all payments from Ostrowski personal account": one total stated as $92,600 and as $103,800.`
	if vs, _ := CheckVerdicts([]Candidate{y}, []Verdict{{Pair: y.ID, SameQuantity: "yes", Reason: reason}}); vs[y.ID].SameQuantity != "yes" {
		t.Errorf("$103,800 does not name 103,800.00: %+v", vs[y.ID])
	}
}
