// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
	"strings"
	"testing"
)

// Carried from v1 orchestrator/crossdoc_test.go (10 of 11 vectors; TestCrossDocFindingsRideDiscrepancySection
// is dropped with the writer's section routing). v2's join is model-free: the alias judge becomes the
// lead's alias map, the adjudicator and its significance notes are gone (conflicts are reported with
// both quotes, not adjudicated), and the inputs are typed values of verified findings.

var nfid int

// fh is one verified finding holding one value, quoted from doc (v1's figureHit).
func fh(value, quote, doc, entity, metric string) Finding {
	nfid++
	return Finding{FID: fmt.Sprintf("F%d", nfid), Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}},
		Values: []Value{{Entity: entity, Metric: metric, Value: value}}}
}

func has(cs []Conflict, a, b string) *Conflict {
	for i, c := range cs {
		var vals []string
		for _, v := range c.Values {
			vals = append(vals, v.Value)
		}
		if j := strings.Join(vals, "|"); strings.Contains(j, a) && strings.Contains(j, b) {
			return &cs[i]
		}
	}
	return nil
}

const (
	qTradesReferral = "The staff reviewed 4,217 omnibus equity trades allocated by Ostrowski during the review period."
	qTradesExhibit  = "Total Omnibus Equity Trades Analyzed | 4,312"
	qCompReferral   = "¶56. Ostrowski received undisclosed compensation totaling $92,600 from Bayshore Palms during the relevant period."
	qCompExhibit    = "Grand total of 22 payments, 05/2022 through 01/2024: $103,800"
	qRateReferral   = "Approximately 73% of the profitable omnibus trades were allocated to the Lakeshore proprietary account."
	qRateExhibit    = "Profitable omnibus trade allocation rate across the review population: 78%"
	qDateEmail      = "From: Ostrowski To: Chen Date: Mon, 15 Apr 2024 09:23:47 -0400 Subject: need you to clean up the shared drive before Friday"
	qDateReferral   = "On or about April 22, 2024, Ostrowski instructed Chen to \"clean up the shared drive\"."
)

// fourPlanted is the four planted discrepancies, each value read as the record has it: the exhibit
// rows name no party, the reader reads them as Ostrowski's, and it reads the e-mail's date and the
// instruction's as one event's, the instruction to clean up the shared drive (the right labels); the
// e-mail's typed entity, Chen, is its recipient, another party than the reader's.
func fourPlanted() []Finding {
	instruction := "instruction to clean up the shared drive"
	return readAs([]Finding{
		fh("4,217", qTradesReferral, "referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "exhibit-c", "", "omnibus trade count"),
		fh("$92,600", qCompReferral, "referral", "Ostrowski", "undisclosed compensation total"),
		fh("$103,800", qCompExhibit, "exhibit-d", "Bayshore Palms LLC", "undisclosed compensation total"),
		fh("Mon, 15 Apr 2024 09:23:47 -0400", qDateEmail, "exhibit-e", "Chen", "email sent date"),
		fh("April 22, 2024", qDateReferral, "referral", "Ostrowski", "obstruction instruction date"),
		fh("73%", qRateReferral, "referral", "Ostrowski", "profitable omnibus allocation rate"),
		fh("78%", qRateExhibit, "exhibit-f", "", "profitable omnibus allocation rate"),
	}, map[string]relabel{"4,312": {party: "Ostrowski"}, "78%": {party: "Ostrowski"},
		"Mon, 15 Apr 2024 09:23:47 -0400": {party: "Ostrowski", measures: instruction, entity: "other"}, "April 22, 2024": {measures: instruction, entity: "party"}})
}

func TestParseCrossDocDate(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Mon, 15 Apr 2024 09:23:47 -0400": "2024-04-15", "April 22, 2024": "2024-04-22", "on or about April 22, 2024": "2024-04-22",
		"15 Apr 2024": "2024-04-15", "2024-04-15": "2024-04-15", "04/22/2024": "2024-04-22", "May 2022": "2022-05", "05/2022": "2022-05",
		"early 2022": "2022", "2021": "2021", "Nov. 12, 2026": "2026-11-12", "4,217": "", "not a date": "",
	} {
		d, ok := ParseDate(in)
		if ok != (want != "") || ok && d.Key() != want {
			t.Errorf("ParseDate(%q) = %s %v, want %q", in, d.Key(), ok, want)
		}
	}
}
