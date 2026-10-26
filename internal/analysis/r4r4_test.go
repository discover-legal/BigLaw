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
