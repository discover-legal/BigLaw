// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"compress/gzip"
	"encoding/json"
	"io"
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

// TestJoinBench6SECPairsAcrossLabels (bench6 SEC): the three bench5 pairs were verified this time but
// labelled "OQR" vs "Whitmore Capital Advisors LLC" (4,217 vs 4,312 trades), "Bellini" vs "Kevin
// Ostrowski" ($92,600 vs $103,800) and "Email transmission" vs "deletion instruction" (April 15 vs April
// 22), so the near join paired none of them. Paired by metric and quote context now, and nothing else
// over the whole ledger is (the label join's 7,912 vs 7,412 stays).
func TestJoinBench6SECPairsAcrossLabels(t *testing.T) {
	got := pairs(Join(bench6Ledger(t, "sec"), nil))
	want := []string{"F144/F547", "F178/F246", "F246/F83", "F269/F539"}
	if !slices.Equal(got, want) {
		t.Errorf("SEC conflicts %v, want %v", got, want)
	}
}

// TestJoinBench6TrustNamesPairwise (bench6 trust: "Sophia Davenport-Chen age 44 [F148] vs 71 [F107]"
// was false, F107's 71 is Margaret's: token containment chained the Davenport-Chen names through a
// shared surname). Names are compared pairwise: the one real difference, Sophia's share of the
// residuary estate (35% in the instructions, 30% in the draft), stays.
func TestJoinBench6TrustNamesPairwise(t *testing.T) {
	got := pairs(Join(bench6Ledger(t, "trust"), nil))
	if want := []string{"F170/F67"}; !slices.Equal(got, want) {
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
	if cs := Join(fs, nil); len(cs) > 0 {
		t.Errorf("two people's ages compared through their surname: %+v", cs)
	}
	if b, ok := fullName("ostrowski", []string{"ostrowski", "richard ostrowski"}); !ok || b != "richard ostrowski" {
		t.Errorf("a surname of one full name: %q %v", b, ok)
	}
	if _, ok := fullName("davenport-chen", []string{"davenport-chen", "sophia davenport-chen", "margaret davenport-chen"}); ok {
		t.Errorf("a surname two people share named one of them")
	}
}
