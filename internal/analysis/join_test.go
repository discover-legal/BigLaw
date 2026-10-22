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

func fourPlanted() []Finding {
	return []Finding{
		fh("4,217", qTradesReferral, "referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "exhibit-c", "", "omnibus trade count"),
		fh("$92,600", qCompReferral, "referral", "Ostrowski", "undisclosed compensation total"),
		fh("$103,800", qCompExhibit, "exhibit-d", "Bayshore Palms LLC", "undisclosed compensation total"),
		fh("Mon, 15 Apr 2024 09:23:47 -0400", qDateEmail, "exhibit-e", "Chen", "email sent date"),
		fh("April 22, 2024", qDateReferral, "referral", "Ostrowski", "obstruction instruction date"),
		fh("73%", qRateReferral, "referral", "Ostrowski", "profitable omnibus allocation rate"),
		fh("78%", qRateExhibit, "exhibit-f", "", "profitable omnibus allocation rate"),
	}
}

func TestCrossDocAllFourPlantedDiscrepancies(t *testing.T) {
	cs := Join(fourPlanted(), []Alias{{"Ostrowski", []string{"Bayshore Palms LLC"}}})
	if len(cs) != 4 {
		t.Fatalf("want 4 discrepancies, got %d: %+v", len(cs), cs)
	}
	for _, p := range [][2]string{{"4,217", "4,312"}, {"$92,600", "$103,800"}, {"73%", "78%"}, {"15 Apr 2024", "April 22, 2024"}} {
		c := has(cs, p[0], p[1])
		if c == nil || c.Values[0].DocumentID == c.Values[1].DocumentID || c.Values[0].FID == "" {
			t.Errorf("%s vs %s: %+v", p[0], p[1], c)
		}
	}
	if c := has(cs, "15 Apr", "April 22"); c == nil || c.Kind != "date" {
		t.Error("the e-mail header date did not join the instruction date as one event")
	}
	// Without the lead's alias, Ostrowski and Bayshore Palms LLC stay separate parties.
	if c := has(Join(fourPlanted(), nil), "$92,600", "$103,800"); c != nil {
		t.Error("parties were unified on a guess")
	}
}

// Both documents state both vintages: an enumeration, not a conflict.
func TestCrossDocVintageEnumerationNotFlagged(t *testing.T) {
	qRef := "The deleted spreadsheets had been created in 2021 and early 2022."
	qFor := "Recovered file fragments bore creation dates in 2021 and early 2022."
	cs := Join([]Finding{
		fh("2021", qRef, "referral", "Ostrowski", "deleted spreadsheet vintage"),
		fh("early 2022", qRef, "referral", "Ostrowski", "deleted spreadsheet vintage"),
		fh("2021", qFor, "forensic", "Ostrowski", "deleted spreadsheet vintage"),
		fh("early 2022", qFor, "forensic", "Ostrowski", "deleted spreadsheet vintage"),
	}, nil)
	if len(cs) != 0 {
		t.Fatalf("vintage enumeration flagged: %+v", cs)
	}
}

// Two different obligations: distinct labels never meet, and a shared label over durations is never
// compared (v1's adjudicator rejected that shape; without a model, durations are not compared at all).
func TestCrossDocDifferentObligationsNotFlagged(t *testing.T) {
	qInitial := "Each access person shall file an initial holdings report within 10 days of becoming an access person."
	qAnnual := "The referral notes annual holdings reports were due within 45 days of the fiscal year end."
	for _, labels := range [][2]string{{"initial holdings report deadline", "annual holdings report deadline"}, {"holdings report deadline", "holdings report deadline"}} {
		cs := Join([]Finding{
			fh("10 days", qInitial, "code", "access persons", labels[0]),
			fh("45 days", qAnnual, "referral", "access persons", labels[1]),
		}, nil)
		if len(cs) != 0 {
			t.Fatalf("%v: different obligations flagged: %+v", labels, cs)
		}
	}
}

// v1's graph tie: an alias the record asserts unifies the parties; here the lead passes it.
func TestCrossDocAliasUnificationDeterministicGraphTie(t *testing.T) {
	fs := []Finding{
		fh("$92,600", "¶56. Ostrowski received undisclosed compensation payments totaling $92,600 from Bayshore Palms.", "referral", "Ostrowski", "undisclosed compensation total"),
		fh("$103,800", "Grand total of undisclosed compensation payments received: $103,800", "exhibit-d", "Bayshore Palms LLC", "undisclosed compensation total"),
	}
	if c := has(Join(fs, []Alias{{"Ostrowski", []string{"Bayshore Palms LLC"}}}), "$92,600", "$103,800"); c == nil {
		t.Fatal("the aliased pair was not flagged")
	}
	if cs := Join(fs, nil); len(cs) != 0 {
		t.Fatalf("without the alias the pair unified: %+v", cs)
	}
}

func TestCrossDocAliasTokenContainment(t *testing.T) {
	cs := Join([]Finding{
		fh("4,217", "Ostrowski reviewed a total of 4,217 omnibus equity trades in the sample.", "referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", "Richard Ostrowski analyzed 4,312 omnibus equity trades across the sample.", "exhibit-c", "Richard Ostrowski", "omnibus trade count"),
	}, nil)
	if has(cs, "4,217", "4,312") == nil {
		t.Fatalf("token-contained entities did not unify: %+v", cs)
	}
}

// v1's no-provider path is v2's only path: numeric and date conflicts survive without a model.
func TestCrossDocNoProviderDegradation(t *testing.T) {
	cs := Join([]Finding{
		fh("4,217", qTradesReferral, "referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "exhibit-c", "", "omnibus trade count"),
		fh("Mon, 15 Apr 2024 09:23:47 -0400", qDateEmail, "exhibit-e", "Chen", "email sent date"),
		fh("April 22, 2024", qDateReferral, "referral", "Ostrowski", "obstruction instruction date"),
	}, nil)
	if has(cs, "4,217", "4,312") == nil || has(cs, "15 Apr", "April 22") == nil {
		t.Fatalf("conflicts lost: %+v", cs)
	}
}

// v1's substring lock: a value whose quote did not verify never joins (its finding is quarantined).
func TestCrossDocSubstringLockDropsFabricatedQuote(t *testing.T) {
	fake := fh("4,312", "a fabricated quote that appears nowhere", "exhibit-c", "", "omnibus trade count")
	fake.Verified = false
	if cs := Join([]Finding{fh("4,217", qTradesReferral, "referral", "Ostrowski", "omnibus trade count"), fake}, nil); len(cs) != 0 {
		t.Fatalf("a quarantined value joined: %+v", cs)
	}
}

func TestCrossDocCanonicalValueEquality(t *testing.T) {
	cs := Join([]Finding{
		fh("4,217", "The staff reviewed 4,217 omnibus equity trades in total.", "referral", "Ostrowski", "omnibus trade count"),
		fh("4217", "A population of 4217 omnibus equity trades was reviewed.", "exhibit-c", "Ostrowski", "omnibus trade count"),
	}, nil)
	if len(cs) != 0 {
		t.Fatalf("equal canonical values flagged: %+v", cs)
	}
}

func TestCrossDocDatePrecisionNotAConflict(t *testing.T) {
	cs := Join([]Finding{
		fh("April 2024", "In April 2024 Ostrowski instructed Chen to clean up the shared drive.", "memo", "Ostrowski", "obstruction instruction date"),
		fh("April 22, 2024", qDateReferral, "referral", "Ostrowski", "obstruction instruction date"),
	}, nil)
	if len(cs) != 0 {
		t.Fatalf("coarse vs precise dates flagged: %+v", cs)
	}
}

func TestParseCrossDocDate(t *testing.T) {
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

func TestJoinNumericNormalization(t *testing.T) {
	cs := Join([]Finding{
		fh("$9.0 million", "Proceeds were $9.0 million.", "a", "Fund", "sale proceeds"),
		{FID: "Fx", Verified: true, Quotes: []Quote{{DocumentID: "b", Quote: "Proceeds: 9,000,000 (USD)"}}, Values: []Value{{Entity: "Fund", Metric: "sale proceeds", Value: "9,000,000", Unit: "USD"}}},
		fh("Nov. 12, 2026", "Closing on Nov. 12, 2026.", "a", "Fund", "closing date"),
		fh("2026-11-12", "Closing date: 2026-11-12", "b", "Fund", "closing date"),
	}, nil)
	if len(cs) != 0 {
		t.Fatalf("equal values in different notation flagged: %+v", cs)
	}
}

func TestJoinAliasMap(t *testing.T) {
	fs := []Finding{
		fh("$1,000", "Smithers paid $1,000.", "a", "J. Smithers", "fee paid"),
		fh("$1,500", "Acme Holdings paid $1,500.", "b", "Acme Holdings", "fee paid"),
	}
	if len(Join(fs, nil)) != 0 || len(Join(fs, []Alias{{"acme holdings", []string{"J.  Smithers"}}})) != 1 {
		t.Fatal("the alias map is not applied (case and spacing folded)")
	}
}

// Trap: $9,000,000 versus 9,000,000 shares of one metric is not a conflict.
func TestJoinComparesOnlySameQuantityClass(t *testing.T) {
	cs := Join([]Finding{
		fh("$9,000,000", "The stake is worth $9,000,000.", "a", "Fund", "stake"),
		{FID: "Fs", Verified: true, Quotes: []Quote{{DocumentID: "b", Quote: "The stake is 9,000,000 shares."}}, Values: []Value{{Entity: "Fund", Metric: "stake", Value: "9,000,000", Unit: "shares"}}},
		{FID: "Fy", Verified: true, Quotes: []Quote{{DocumentID: "c", Quote: "A stake of $9,000,000."}}, Values: []Value{{Entity: "Fund", Metric: "stake", Value: "$9,000,000", Class: "Count"}}},
	}, nil)
	if len(cs) != 0 {
		t.Fatalf("money compared with a share count: %+v", cs)
	}
}
