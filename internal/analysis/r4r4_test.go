// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
)

// bench6Ledger is a bench6 run's findings, verbatim from the run's database (biglaw-bench6.pgdump:
// the task's ledger rows of kind finding, gzipped).
func bench6Ledger(t *testing.T, name string) []Finding {
	t.Helper()
	fh, err := os.Open("testdata/bench6-" + name + "-ledger.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	z, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	var fs []Finding
	if err == nil {
		err = json.Unmarshal(b, &fs)
	}
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func pairs(cs []Conflict) []string {
	var out []string
	for _, c := range cs {
		ids := []string{c.Values[0].FID, c.Values[1].FID}
		slices.Sort(ids)
		out = append(out, strings.Join(ids, "/"))
	}
	slices.Sort(out)
	return out
}

// bench6SECLabels are the SEC pairs' values as the figure reader reads them (the right labels): whose
// figure, what it measures, and what the typed entity is to it ("OQR", "Email transmission", "deletion
// instruction", "total equity trades": no party; Kevin Ostrowski: the payer of Bellini's compensation,
// another party).
var bench6SECLabels = func() map[string]relabel {
	wca, bellini, whitmore := "Whitmore Capital Advisors LLC", "Marcus T. Bellini", "Gerald R. Whitmore"
	trades, all, comp, instr := "omnibus equity trades analysed", "equity trades during the review period", "undisclosed compensation paid for Bellini", "instruction to clean up the shared drive"
	return map[string]relabel{"F178/4,217": {wca, trades, "", "none"}, "F246/4,312": {wca, trades, "", "party"},
		"F83/7,912": {wca, all, "", "none"}, "F246/7,412": {wca, all, "", "party"}, "F269/$103,800.00": {bellini, comp, "", "other"},
		"F539/$92,600": {bellini, comp, "", "party"}, "F144/April 22, 2024": {whitmore, instr, "", "none"}, "F547/15 Apr 2024 09:23:47": {whitmore, instr, "", "none"}}
}()

// TestJoinBench6SECPairsAcrossLabels (bench6 SEC): the three bench5 pairs were verified this time but
// typed "OQR" vs "Whitmore Capital Advisors LLC" (4,217 vs 4,312 trades), "Bellini" vs "Kevin
// Ostrowski" ($92,600 vs $103,800) and "Email transmission" vs "deletion instruction" (April 15 vs April
// 22). Read as one measure of one party each, the trades and the dates pair, and so do 7,912 vs 7,412
// equity trades, and nothing else over the whole ledger does. $103,800 is typed under its payer, Kevin
// Ostrowski, another party than the reader's (Bellini): no conflict is asserted on it (lab-r4 r5 M4).
func TestJoinBench6SECPairsAcrossLabels(t *testing.T) {
	got := pairs(Join(readAs(bench6Ledger(t, "sec"), bench6SECLabels), nil))
	want := []string{"F144/F547", "F178/F246", "F246/F83"}
	if !slices.Equal(got, want) {
		t.Errorf("SEC conflicts %v, want %v", got, want)
	}
	ls := maps.Clone(bench6SECLabels)
	ls["F269/$103,800.00"] = relabel{"Marcus T. Bellini", "undisclosed compensation paid for Bellini", "", "none"}
	if got := pairs(Join(readAs(bench6Ledger(t, "sec"), ls), nil)); !slices.Contains(got, "F269/F539") {
		t.Errorf("read with no other party typed, $92,600 vs $103,800 is not a conflict: %v", got)
	}
}

// TestJoinBench6TrustNamesPairwise (bench6 trust: "Sophia Davenport-Chen age 44 [F148] vs 71 [F107]"
// was false, F107's 71 is Margaret's: token containment chained the Davenport-Chen names through a
// shared surname). Whose figure each is, is the figure reader's now (lab-r4 r4): Sophia's share of the
// residuary estate (35% in the instructions, 30% in the draft) stays, and with the protected trust's
// termination date read as the trust's (not, as before, joined to Sophia's own date of birth, which
// both documents state, so neither side was disjoint) the documents' termination ages (sixty, October
// 8, 2040, against fifty-five, October 8, 2035) are a conflict too.
func TestJoinBench6TrustNamesPairwise(t *testing.T) {
	got := pairs(Join(readAs(bench6Ledger(t, "trust"), map[string]relabel{"F67/35": {party: "Sophia Davenport-Chen"}, "F170/30%": {party: "Sophia Davenport-Chen", entity: "party"}}), nil))
	if want := []string{"F117/F471", "F170/F67"}; !slices.Equal(got, want) {
		t.Errorf("trust conflicts %v, want %v", got, want)
	}
	age := func(fid, doc, who, v, quote string) Finding {
		return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: []Value{{Entity: who, Metric: "age", Value: v, Class: "Count"}}}
	}
	fs := []Finding{
		age("F1", "decl", "Sophia Davenport-Chen", "44", "Sophia Davenport-Chen, age forty-four (44), resides in Portland."),
		age("F2", "trust", "Margaret Davenport-Chen", "71", "The Settlor, Margaret Davenport-Chen, is seventy-one (71) years of age."),
		age("F3", "memo", "Davenport-Chen", "70", "Mrs. Davenport-Chen is 70."),
	}
	if cs := joinRead(fs, nil); len(cs) > 0 {
		t.Errorf("two people's ages compared through their surname: %+v", cs)
	}
}
