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
	if cs := Join(fs, nil); len(cs) != 1 || cs[0].Entity != "Ostrowski" || cs[0].Metric != "undisclosed compensation total" || has(cs, "$92,600", "$103,800") == nil {
		t.Fatalf("without the alias: %+v (r3: the near join finds the pair, its quotes sharing their words)", cs)
	}
}

// TestNearConflictsAcrossLabels (bench r3, SEC: the lead's join found nothing, though the record
// states 4,217 vs 4,312 trades analysed, $92,600 vs $103,800 paid to the club, 73% vs 78% of volume
// and April 22 vs April 15 for the deletion instruction): values of one class from two documents,
// near each other, whose quotes share three distinctive words are a conflict whatever their labels;
// a figure restated in the other document, far apart values, or quotes about other things are not.
func TestNearConflictsAcrossLabels(t *testing.T) {
	v := func(value, class, metric string) Value {
		return Value{Entity: "WCA", Metric: metric, Value: value, Class: class}
	}
	f := func(fid, doc, quote string, vals ...Value) Finding {
		return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: vals}
	}
	fs := []Finding{
		f("F1", "referral", "The Division's Office of Quantitative Research (\"OQR\") analyzed 4,217 equity trades executed through the omnibus account during the Review Period.", v("4,217", "Count", "trades analyzed by OQR")),
		f("F2", "stats", "Total Omnibus Equity Trades Analyzed\t4,312\tTrades executed through WCA omnibus account at Meridian Clearing Corp. during Review Period", v("4,312", "Count", "total omnibus equity trades")),
		f("F3", "referral", "The total undisclosed compensation received by Bellini, in the form of payments made by Ostrowski to Bayshore Palms Golf & Country Club for Bellini's benefit, was approximately $92,600", v("$92,600", "Money", "undisclosed compensation")),
		f("F4", "bank", "GRAND TOTAL\tTotal of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini\t05/03/2022 – 01/02/2024\t22\t\t$103,800.00", v("$103,800.00", "Money", "grand total of payments")),
		f("F5", "referral", "WCA executed 5,776 equity trades through the omnibus account out of a total of 7,912 equity trades during the Review Period, representing approximately 73% of total equity trading volume.", v("73%", "Percentage", "omnibus share of equity trades")),
		f("F6", "stats", "Omnibus Trades as % of Total Equity Trading Volume\tApproximately 78%\t5,776 omnibus trades out of total equity trading volume", v("78%", "Percentage", "omnibus trades percentage")),
		f("F7", "referral", "On or about April 22, 2024, during the pendency of the SEC examination of WCA (which had commenced on March 11, 2024), Whitmore instructed WCA's IT Administrator, Tomás Delgado (\"Delgado\"), to \"clean up the shared drive\"", v("April 22, 2024", "Date", "deletion instruction")),
		f("F8", "email", "From: Gerald R. Whitmore <gwhitmore@whitmorecapital.com>\nTo: Tomás Delgado <tdelgado@whitmorecapital.com>\nDate: Mon, 15 Apr 2024 09:23:47 -0400", v("Mon, 15 Apr 2024", "Date", "email sent")),
		// not conflicts: the same figure in both, a far value, unrelated quotes
		f("F9", "stats", "WCA executed 5,776 omnibus trades out of 7,912 equity trades.", v("5,776", "Count", "omnibus trades")),
		f("F10", "bank", "Initiation fee paid to Bayshore Palms Golf & Country Club for Bellini: $45,000.00", v("$45,000.00", "Money", "initiation fee")),
		f("F11", "manual", "Access persons file quarterly reports of personal securities transactions within 30 days.", v("4,400", "Count", "reports filed")),
		// rows of two tables (a trade each), and small figures, are not compared by nearness
		f("F12", "stats", "OMN-2021-00142\t2021-02-17\tXYZ Corp (XYZ)\tBuy\t25,000\t$48.32\t$1,208,000\tOceanic Fund I LP", v("$1,208,000", "Money", "notional"), v("2021-02-17", "Date", "trade date")),
		f("F13", "log", "2021-02-24\tXYZ Corp (XYZ)\tBuy\t26,000\t$48.90\t$1,271,400\tOceanic Fund I LP", v("$1,271,400", "Money", "trade value"), v("2021-02-24", "Date", "executed")),
		f("F14", "bank", "Monthly dues for Bellini at Bayshore Palms Golf & Country Club were $2,800 per month for 21 months", v("21", "Count", "months of dues")),
		f("F15", "referral", "Monthly membership dues for Bellini at Bayshore Palms Golf & Country Club of $2,800 per month for a period of 17 months", v("17", "Count", "months")),
	}
	cs := Join(fs, nil)
	for _, p := range [][2]string{{"4,217", "4,312"}, {"$92,600", "$103,800.00"}, {"73%", "78%"}, {"April 22, 2024", "Mon, 15 Apr 2024"}} {
		if has(cs, p[0], p[1]) == nil {
			t.Errorf("%s vs %s not found: %+v", p[0], p[1], cs)
		}
	}
	if len(cs) != 4 {
		t.Errorf("%d conflicts, want the 4: %+v", len(cs), cs)
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
