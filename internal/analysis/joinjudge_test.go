// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// The cross-document join as Go candidates and a judge's verdicts (lab-r4 r7, owner decision). Every
// earlier join scenario is kept here as a scripted one: Go must put the pair to the judge (seen), a
// conflict is asserted only on the judge's "yes", and "no" or "unclear" assert nothing. Which pairs
// are one quantity is the scenario's truth, scripted, never a rule of the package.

// sides of a case: the findings, the pairs that are one quantity stated two ways, and the pairs that
// are two quantities (the judge sees them and says no).
type joinCase struct {
	fs        []Finding
	same, not [][2]string
}

func (c joinCase) check(t *testing.T, name string) {
	t.Helper()
	for _, p := range append(slices.Clone(c.same), c.not...) {
		if !seen(c.fs, p[0], p[1]) {
			t.Errorf("%s: %s vs %s is not put to the judge", name, p[0], p[1])
		}
	}
	got := judged(c.fs, c.same...)
	for _, p := range c.same {
		if has(got, p[0], p[1]) == nil {
			t.Errorf("%s: %s vs %s judged one quantity, not reported: %v", name, p[0], p[1], pairs(got))
		}
	}
	if len(got) != len(c.same) {
		t.Errorf("%s: %d conflicts, want the %d judged so: %v", name, len(got), len(c.same), pairs(got))
	}
	for _, answer := range []string{"no", "unclear"} {
		cs, unclear, _, _, _ := Join(context.Background(), testCall, read(c.fs), scripted{answer: answer})
		if len(cs) > 0 || answer == "unclear" && len(c.same)+len(c.not) > 0 && len(unclear) == 0 {
			t.Errorf("%s: judged %s throughout, %d conflicts and %d unclear", name, answer, len(cs), len(unclear))
		}
	}
}

func TestJoinScenarios(t *testing.T) {
	sal := func(fid, doc, quote, ent, v string) Finding {
		return fnd(fid, doc, quote, nil, Value{Entity: ent, Metric: "annual base salary", Value: v, Class: "Money"})
	}
	mv := func(ent, metric, val string) Value {
		return Value{Entity: ent, Metric: metric, Value: val, Class: "Money"}
	}
	f := func(fid, doc, quote string, v Value) Finding {
		return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: []Value{v}}
	}
	price := f("F1", "spa", "The Purchase Price is $1,000,000, payable by Buyer to Seller at the Closing.", mv("Buyer", "purchase price", "$1,000,000"))
	oceanic := func(fid, doc, quote, value, unit string) Finding {
		return f(fid, doc, quote, Value{Entity: "Oceanic Offshore Fund Ltd.", Metric: "net assets", Value: value, Unit: unit, Class: "Money"})
	}
	wca := func(fid, doc, quote, value, class, metric string) Finding {
		return f(fid, doc, quote, Value{Entity: "WCA", Metric: metric, Value: value, Class: class})
	}
	for name, c := range map[string]joinCase{
		"four planted": {fourPlanted(), [][2]string{{"4,217", "4,312"}, {"$92,600", "$103,800"}, {"73%", "78%"}, {"Mon, 15 Apr 2024 09:23:47 -0400", "April 22, 2024"}}, nil},
		"one party's compensation under two names (an alias)": {[]Finding{
			fh("$92,600", "¶56. Ostrowski received undisclosed compensation payments totaling $92,600 from Bayshore Palms.", "referral", "Ostrowski", "undisclosed compensation total"),
			fh("$103,800", "Grand total of undisclosed compensation payments received: $103,800", "exhibit-d", "Bayshore Palms LLC", "undisclosed compensation total"),
		}, [][2]string{{"$92,600", "$103,800"}}, nil},
		"one figure under four extractor labels (bench r3 SEC)": {[]Finding{
			wca("F1", "referral", "The Division's Office of Quantitative Research (\"OQR\") analyzed 4,217 equity trades executed through the omnibus account during the Review Period.", "4,217", "Count", "trades analyzed by OQR"),
			wca("F2", "stats", "Total Omnibus Equity Trades Analyzed\t4,312\tTrades executed through WCA omnibus account at Meridian Clearing Corp. during Review Period", "4,312", "Count", "total omnibus equity trades"),
			wca("F3", "referral", "The total undisclosed compensation received by Bellini was approximately $92,600", "$92,600", "Money", "undisclosed compensation"),
			wca("F4", "bank", "GRAND TOTAL\tTotal of all payments from Ostrowski personal account to Bayshore Palms Golf & Country Club on behalf of Marcus T. Bellini\t$103,800.00", "$103,800.00", "Money", "grand total of payments"),
			wca("F5", "referral", "representing approximately 73% of total equity trading volume.", "73%", "Percentage", "omnibus share of equity trades"),
			wca("F6", "stats", "Omnibus Trades as % of Total Equity Trading Volume\tApproximately 78%", "78%", "Percentage", "omnibus trades percentage"),
			wca("F7", "referral", "On or about April 22, 2024, Whitmore instructed WCA's IT Administrator to \"clean up the shared drive\"", "April 22, 2024", "Date", "deletion instruction"),
			wca("F8", "email", "From: Gerald R. Whitmore\nDate: Mon, 15 Apr 2024 09:23:47 -0400", "Mon, 15 Apr 2024", "Date", "email sent"),
			wca("F10", "bank", "Initiation fee paid to Bayshore Palms Golf & Country Club for Bellini: $45,000.00", "$45,000.00", "Money", "initiation fee"),
		}, [][2]string{{"4,217", "4,312"}, {"$92,600", "$103,800.00"}, {"73%", "78%"}, {"April 22, 2024", "Mon, 15 Apr 2024"}}, [][2]string{{"$45,000.00", "$92,600"}}},
		"two vintages both documents state": {[]Finding{
			fh("2021", "The deleted spreadsheets had been created in 2021 and early 2022.", "referral", "Ostrowski", "deleted spreadsheet vintage"),
			fh("early 2022", "The deleted spreadsheets had been created in 2021 and early 2022.", "referral", "Ostrowski", "deleted spreadsheet vintage"),
			fh("2021", "Recovered file fragments bore creation dates in 2021 and early 2022.", "forensic", "Ostrowski", "deleted spreadsheet vintage"),
			fh("early 2022", "Recovered file fragments bore creation dates in 2021 and early 2022.", "forensic", "Ostrowski", "deleted spreadsheet vintage"),
		}, nil, [][2]string{{"2021", "early 2022"}}},
		"different facts near each other (lab-r3 H2/M4)": {[]Finding{
			f("F1", "lpa", "The Management Fee shall equal 2% per annum of the Fund's net asset value.", Value{Entity: "Fund", Metric: "management fee", Value: "2%", Class: "Percentage"}),
			f("F2", "ppm", "The Incentive Allocation equals 2.5% of the Fund's net asset value appreciation.", Value{Entity: "Fund", Metric: "incentive allocation", Value: "2.5%", Class: "Percentage"}),
			f("F3", "complaint", "On March 3, 2024, Acme Corp. sent Baker a written notice of termination.", Value{Entity: "Acme", Metric: "termination notice", Value: "March 3, 2024", Class: "Date"}),
			f("F4", "letter", "Acme Corp. hereby demands that Baker cure the default; dated March 18, 2024.", Value{Entity: "Acme", Metric: "cure demand", Value: "March 18, 2024", Class: "Date"}),
		}, nil, [][2]string{{"2%", "2.5%"}, {"March 3, 2024", "March 18, 2024"}}},
		"a part of the price, and the price misstated (r2 M3, r3 M3)": {[]Finding{price,
			f("F2", "funds-flow", "At the Closing, Buyer shall wire $950,000 of the Purchase Price to Seller; the balance is held back.", mv("Buyer", "purchase price", "$950,000")),
			f("F3", "board", "Payment of the Purchase Price of $1,200,000 by Buyer shall be made at Closing.", mv("Buyer", "purchase price", "$1,200,000")),
		}, [][2]string{{"$1,000,000", "$1,200,000"}}, [][2]string{{"$1,000,000", "$950,000"}, {"$950,000", "$1,200,000"}}},
		"two deposits stated differently, and two deposits (r3 M3, r5 L2)": {[]Finding{
			f("F4", "spa", "Buyer shall pay a deposit of $50,000 within two days of signing.", mv("Buyer", "deposit", "$50,000")),
			f("F5", "escrow", "Buyer shall pay a deposit of $45,000 within two days of signing.", mv("Buyer", "deposit", "$45,000")),
			f("F6", "amendment", "The Buyer paid an additional deposit of $260,000 on extension.", mv("Buyer", "deposit", "$260,000")),
		}, [][2]string{{"$50,000", "$45,000"}}, [][2]string{{"$45,000", "$260,000"}}},
		"scaled and whole figures (r2 M2, bench4)": {[]Finding{
			oceanic("F5", "referral", "The fund held approximately $92 million in net assets.", "92", "million"),
			oceanic("F6", "summary", "The fund held approximately $97 million in net assets.", "$97 million", ""),
			f("F7", "spa", "The aggregate Purchase Price payable by Buyer is $92,000,000.", mv("Buyer", "purchase price", "$92,000,000")),
		}, [][2]string{{"92", "$97 million"}}, [][2]string{{"$97 million", "$92,000,000"}}},
		"two parties' figures (r4 M1, r5 M4, r7 H2: role names)": {[]Finding{
			sal("F1", "ceo-agreement", "The Company shall pay the Executive an annual base salary of $410,000.", "Jane Moss", "$410,000"),
			sal("F2", "cfo-agreement", "The Company shall pay the Executive an annual base salary of $395,000.", "Raj Patel", "$395,000"),
			fnd("F3", "lease1", "Tenant shall pay Landlord monthly base rent of $18,500.", nil, Value{Entity: "Harbor Foods Inc", Metric: "monthly base rent", Value: "$18,500", Class: "Money"}),
			fnd("F4", "lease2", "Tenant shall pay Landlord monthly base rent of $19,250.", nil, Value{Entity: "Pine Street Bakery LLC", Metric: "monthly base rent", Value: "$19,250", Class: "Money"}),
			fnd("F5", "spa-alpha", "The Buyer shall pay a deposit of $2,400,000 at signing.", nil, Value{Entity: "Alpha Holdings", Metric: "deposit", Value: "$2,400,000", Class: "Money"}),
			fnd("F6", "spa-beta", "The Buyer shall pay a deposit of $2,600,000 at signing.", nil, Value{Entity: "Beta Partners", Metric: "deposit", Value: "$2,600,000", Class: "Money"}),
		}, nil, [][2]string{{"$410,000", "$395,000"}, {"$18,500", "$19,250"}, {"$2,400,000", "$2,600,000"}}},
		"opposite payments (r4 M4)": {[]Finding{
			f("F1", "ledger", "On March 3, 2024, Acme Corp paid Baker LLC $50,000 by wire for consulting services.", mv("Acme Corp", "payment", "$50,000")),
			f("F2", "bank", "On March 10, 2024, Baker LLC paid Acme Corp $47,000 by wire for used equipment.", mv("Baker LLC", "payment", "$47,000")),
		}, nil, [][2]string{{"$50,000", "$47,000"}}},
		"two fiscal years, one fiscal year (r5 L2, r7 M3)": {[]Finding{
			fnd("F1", "10k", "Alpha Corp reported annual revenue of $12,300,000 for FY2023.", nil, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,300,000", Class: "Money"}),
			fnd("F2", "deck", "Alpha Corp reported annual revenue of $12,800,000 for FY2024.", nil, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,800,000", Class: "Money"}),
			fnd("F3", "board", "Alpha Corp's FY2023 revenue was $12,900,000.", nil, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,900,000", Class: "Money"}),
		}, [][2]string{{"$12,300,000", "$12,900,000"}}, [][2]string{{"$12,300,000", "$12,800,000"}, {"$12,800,000", "$12,900,000"}}},
	} {
		c.check(t, name)
	}
}

// What Go never puts to the judge: a quarantined finding's value, one value in two notations (equal
// canons), dates at two precisions, durations and identifiers (kind other), money against a share
// count, a figure read unclearly, two values of one finding or one document.
func TestJoinNeverProposes(t *testing.T) {
	quarantined := fh("4,312", "a fabricated quote that appears nowhere", "exhibit-c", "", "omnibus trade count")
	quarantined.Verified = false
	for name, fs := range map[string][]Finding{
		"a quarantined value": {fh("4,217", qTradesReferral, "referral", "Ostrowski", "omnibus trade count"), quarantined},
		"one value in two notations": {fh("4,217", "The staff reviewed 4,217 omnibus equity trades in total.", "referral", "Ostrowski", "omnibus trade count"),
			fh("4217", "A population of 4217 omnibus equity trades was reviewed.", "exhibit-c", "Ostrowski", "omnibus trade count")},
		"one sum in two notations": {fh("$9.0 million", "Proceeds were $9.0 million.", "a", "Fund", "sale proceeds"),
			{FID: "Fx", Verified: true, Quotes: []Quote{{DocumentID: "b", Quote: "Proceeds: 9,000,000 (USD)"}}, Values: []Value{{Entity: "Fund", Metric: "sale proceeds", Value: "9,000,000", Unit: "USD"}}}},
		"one date in two notations": {fh("Nov. 12, 2026", "Closing on Nov. 12, 2026.", "a", "Fund", "closing date"), fh("2026-11-12", "Closing date: 2026-11-12", "b", "Fund", "closing date")},
		"dates at two precisions": {fh("April 2024", "In April 2024 Ostrowski instructed Chen to clean up the shared drive.", "memo", "Ostrowski", "instruction date"),
			fh("April 22, 2024", qDateReferral, "referral", "Ostrowski", "instruction date")},
		"two durations": {fh("10 days", "Each access person shall file an initial holdings report within 10 days.", "code", "access persons", "holdings report deadline"),
			fh("45 days", "Annual holdings reports were due within 45 days of the fiscal year end.", "referral", "access persons", "holdings report deadline")},
		"money and a share count": {fh("$9,000,000", "The stake is worth $9,000,000.", "a", "Fund", "stake"),
			{FID: "Fs", Verified: true, Quotes: []Quote{{DocumentID: "b", Quote: "The stake is 9,000,000 shares."}}, Values: []Value{{Entity: "Fund", Metric: "stake", Value: "9,100,000", Unit: "shares"}}}},
		"a figure whose scale nothing states": {r2vF("F3", "table", "Purchase price\t92", Value{Entity: "Buyer", Metric: "purchase price", Value: "92", Class: "Money"}),
			r2vF("F4", "board", "The Board approved a Purchase Price of $95 million payable by Buyer.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$95 million", Class: "Money"})},
		"one document": {fh("$50,000", "A deposit of $50,000; a second deposit of $60,000.", "spa", "Buyer", "deposit"), fh("$60,000", "A deposit of $50,000; a second deposit of $60,000.", "spa", "Buyer", "deposit")},
	} {
		if cands, _ := Candidates(read(fs), testLimit, nil); len(cands) > 0 {
			t.Errorf("%s: put to the judge: %v", name, cands[0].ID)
		}
	}
}

// Go rejects a verdict for a pair it did not propose (a malicious "yes" on two figures that are no
// candidate: not real spans, a share count against money, a quarantined value), keeps one verdict per
// pair (a second makes it unclear), reads an answer other than yes/no/unclear as unclear, and
// asserts only "yes"; with no judge, nothing is asserted and every pair is counted as not judged.
func TestJoinVerdictsAreChecked(t *testing.T) {
	fs := read([]Finding{
		fh("$92,600", qCompReferral, "referral", "Ostrowski", "compensation"),
		fh("$103,800", qCompExhibit, "exhibit-d", "Ostrowski", "compensation"),
		{FID: "Fs", Verified: true, Quotes: []Quote{{DocumentID: "b", Quote: "The stake is 9,000,000 shares."}}, Values: []Value{{Entity: "Ostrowski", Metric: "compensation", Value: "9,000,000", Unit: "shares"}}},
	})
	cands, _ := Candidates(fs, testLimit, nil)
	if len(cands) != 1 {
		t.Fatalf("candidates %+v", cands)
	}
	id := cands[0].ID
	vs, notes := CheckVerdicts(cands, []Verdict{
		{Pair: "Fs.v1|" + cands[0].A.Ref, SameQuantity: "yes"}, {Pair: "F999.v1|F998.v1", SameQuantity: "yes"}, {Pair: id, SameQuantity: "yes"}, {Pair: id, SameQuantity: "yes"}})
	if len(notes) != 2 || vs[id].SameQuantity != "unclear" || len(vs) != 1 {
		t.Errorf("verdicts %+v notes %q", vs, notes)
	}
	if cs := Conflicts(cands, vs); len(cs) != 0 {
		t.Errorf("a pair judged twice asserted: %+v", cs)
	}
	if vs, _ := CheckVerdicts(cands, []Verdict{{Pair: id, SameQuantity: "probably"}}); vs[id].SameQuantity != "unclear" {
		t.Errorf("an unknown answer: %+v", vs)
	}
	cs, unclear, unjudged, _, err := Join(context.Background(), testCall, fs, nil)
	if err != nil || len(cs) != 0 || len(unclear) != 0 || unjudged != 1 {
		t.Errorf("no judge: %d conflicts, %d unclear, %d not judged (%v)", len(cs), len(unclear), unjudged, err)
	}
}

// The candidates are capped by the judge's budget (the rest counted for Limitations) and ordered the
// same way every time: each value's most alike, closest counterparts first, so every value gets its
// nearest before any value gets more (lab-r4 r8 M1); pairs judged already come first whatever the cap,
// so a "yes" never falls out as findings arrive.
func TestJoinCandidatesCappedAndOrdered(t *testing.T) {
	var fs []Finding
	for i := range 20 {
		fs = append(fs, fh(fmt.Sprintf("$%d,000", 100+i), fmt.Sprintf("A fee of $%d,000.", 100+i), fmt.Sprintf("doc%d", i), "Acme", []string{"legal fee", "audit fee", "filing fee"}[i%3]))
	}
	cands, dropped := Candidates(read(fs), testLimit, nil)
	if len(cands) != testLimit || dropped != 20*19/2-testLimit {
		t.Fatalf("%d candidates, %d dropped", len(cands), dropped)
	}
	again, _ := Candidates(read(slices.Clone(fs)), testLimit, nil)
	if !slices.EqualFunc(cands, again, func(a, b Candidate) bool { return a.ID == b.ID }) {
		t.Error("the order is not fixed")
	}
	if cands[0].A.Fig.Measures != cands[0].B.Fig.Measures {
		t.Errorf("first candidate %s is not of one measure", cands[0].ID)
	}
	first := map[string]bool{} // within the first pairs, every value already has a counterpart
	for _, c := range cands[:20] {
		first[c.A.Ref], first[c.B.Ref] = true, true
	}
	if len(first) < 15 {
		t.Errorf("the first 20 pairs cover %d values: each value's nearest come first", len(first))
	}
	if c, d := Candidates(read(fs), 0, nil); len(c) != 0 || d != 190 {
		t.Errorf("no judge budget: %d candidates, %d dropped", len(c), d)
	}
	last := cands[len(cands)-1]
	kept, _ := Candidates(read(fs), 1, func(c Candidate) bool { return c.ID == last.ID })
	if len(kept) != 1 || kept[0].ID != last.ID {
		t.Errorf("a pair judged already is not kept ahead of the cap: %v", kept)
	}
}

// The ledgers of real runs: every true find is put to the judge within a six-call join's cap, under
// the stand-in reader's labels, the right labels, prompt-faithful ones, and (lab-r4 r8 M1) measures
// worded differently on each side, as each side's own reader call words them; and judged so it is
// reported. Pairs are named by their canons (one question per two values in two documents: which
// findings' refs stand for it is the ranking's).
func TestJoinBenchLedgers(t *testing.T) {
	bench6 := func(name string, ls map[string]relabel) []Finding { return readAs(bench6Ledger(t, name), ls) }
	prompted := maps.Clone(bench6SECLabels) // a prompt-faithful reader: counterparties from each passage, a period on one side
	prompted["F269/$103,800.00"] = relabel{"Marcus T. Bellini", "undisclosed compensation paid for Bellini", "2022-05-03/2024-01-02", "other"}
	prompted["F539/$92,600"] = relabel{"Marcus T. Bellini", "undisclosed compensation paid for Bellini", "", "party"}
	wca, bellini, whitmore := "Whitmore Capital Advisors LLC", "Marcus T. Bellini", "Gerald R. Whitmore"
	reworded := map[string]relabel{"F178/4,217": {wca, "equity trades analyzed by OQR", "", "none"}, "F246/4,312": {wca, "total omnibus equity trades", "", "party"},
		"F83/7,912": {wca, "total equity trades in the review period", "", "none"}, "F246/7,412": {wca, "equity trades executed", "", "party"},
		"F269/$103,800.00": {bellini, "grand total of payments on behalf of Bellini", "", "other"}, "F539/$92,600": {bellini, "undisclosed compensation received", "", "party"},
		"F144/April 22, 2024": {whitmore, "date of the instruction to clean up the drive", "", "none"}, "F547/15 Apr 2024 09:23:47": {whitmore, "email sent", "", "none"}}
	sec := [][2]string{{"n:4217", "n:4312"}, {"n:7912", "n:7412"}, {"usd:92600", "usd:103800"}, {"date:2024-04-22", "date:2024-04-15"}}
	for name, c := range map[string]struct {
		fs   []Finding
		same [][2]string
	}{
		"bench5 SEC":                         {readAs(bench5Findings(t), bench5Labels), [][2]string{{"n:4217", "n:4312"}, {"usd:92600", "usd:103800"}, {"date:2024-04-22", "date:2024-04-15"}}},
		"bench6 SEC":                         {bench6("sec", bench6SECLabels), sec},
		"bench6 SEC, the stand-in's labels":  {read(bench6Ledger(t, "sec")), sec},
		"bench6 SEC, measures reworded":      {bench6("sec", reworded), sec},
		"bench6 SEC, prompt-faithful labels": {bench6("sec", prompted), [][2]string{{"usd:92600", "usd:103800"}}},
		"bench6 trust": {bench6("trust", map[string]relabel{"F67/35": {party: "Sophia Davenport-Chen"}, "F170/30%": {party: "Sophia Davenport-Chen", entity: "party"}}),
			[][2]string{{"pct:35", "pct:30"}, {"date:2040-10-08", "date:2035-10-08"}}},
		"bench6 trust, the stand-in's labels": {read(bench6Ledger(t, "trust")), [][2]string{{"date:2040-10-08", "date:2035-10-08"}}},
	} {
		cands, dropped := Candidates(c.fs, testLimit, nil)
		t.Logf("%s: %d candidates, %d dropped", name, len(cands), dropped)
		for _, p := range c.same {
			if !slices.ContainsFunc(cands, func(x Candidate) bool {
				return x.A.Canon == p[0] && x.B.Canon == p[1] || x.A.Canon == p[1] && x.B.Canon == p[0]
			}) {
				t.Errorf("%s: %s vs %s not put to the judge (%d candidates, %d dropped)", name, p[0], p[1], len(cands), dropped)
			}
		}
		cs, _, _, _, _ := Join(context.Background(), testCall, c.fs, scripted{yes: c.same})
		if len(cs) < len(c.same) {
			t.Errorf("%s: %d conflicts judged, want %d: %v", name, len(cs), len(c.same), pairs(cs))
		}
	}
	for _, name := range []string{"gatea"} { // gate A: whatever is put to the judge, "no" asserts nothing
		if cs, _, _, _, _ := Join(context.Background(), testCall, read(bench6Ledger(t, name)), scripted{}); len(cs) > 0 {
			t.Errorf("%s: %v", name, pairs(cs))
		}
	}
}

// r7 M3, H2b, L3: the reader's party, counterparty, period and typed-entity labels are context the
// judge reads, never keys Go matches on. Prompt-faithful labels (a date's own date as its period, a
// period on one side only, counterparties as each passage names them, a role name as the party or
// counterparty, one side's counterparty unknown) leave every pair put to the judge, so the true finds
// stay found on its yes and the role-name pairs stay out on its no.
func TestJoinLabelsAreContextNotKeys(t *testing.T) {
	set := func(fs []Finding, fid, value string, g func(r *Reading)) {
		for i := range fs {
			for j, v := range fs[i].Values {
				if fs[i].FID == fid && v.Value == value && v.Fig != nil {
					g(fs[i].Values[j].Fig)
				}
			}
		}
	}
	trust := readAs(bench6Ledger(t, "trust"), map[string]relabel{"F67/35": {party: "Sophia Davenport-Chen"}, "F170/30%": {party: "Sophia Davenport-Chen", entity: "party"}})
	for i := range trust {
		for j, v := range trust[i].Values {
			if d, ok := ParseDate(v.Value); ok && d.Day != 0 && v.Fig != nil && ClassOf(v) == "Date" {
				trust[i].Values[j].Fig.Period = d.Key()
			}
		}
	}
	sec := readAs(bench6Ledger(t, "sec"), bench6SECLabels)
	set(sec, "F269", "$103,800.00", func(r *Reading) {
		r.Period, r.Counterparty = "2022-05-03/2024-01-02", "Bayshore Palms Golf & Country Club"
	})
	set(sec, "F539", "$92,600", func(r *Reading) { r.Period, r.Counterparty = "", "Ostrowski" })
	leases := readAs([]Finding{
		fnd("F1", "lease1", "Tenant shall pay Landlord monthly base rent of $18,500.", nil, Value{Entity: "Harbor Foods Inc", Metric: "monthly base rent", Value: "$18,500", Class: "Money"}),
		fnd("F2", "lease2", "Tenant shall pay Landlord monthly base rent of $19,250.", nil, Value{Entity: "Pine Street Bakery LLC", Metric: "monthly base rent", Value: "$19,250", Class: "Money"}),
		fnd("F3", "rentroll", "Acme Realty: monthly base rent $19,900.", nil, Value{Metric: "monthly base rent", Value: "$19,900", Class: "Money"}),
	}, map[string]relabel{"$18,500": {party: "Landlord", measures: "monthly base rent", period: "none", entity: "other"}, "$19,250": {party: "Landlord", measures: "monthly base rent", period: "none", entity: "other"},
		"$19,900": {party: "Acme Realty", measures: "monthly base rent", period: "none"}})
	set(leases, "F1", "$18,500", func(r *Reading) { r.Counterparty = "Tenant" })
	set(leases, "F2", "$19,250", func(r *Reading) { r.Counterparty = "Tenant" })
	fy := readAs([]Finding{
		fnd("F1", "10k", "Alpha Corp reported revenue of $12,300,000 for FY2023.", nil, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,300,000", Class: "Money"}),
		fnd("F2", "board", "Alpha Corp's FY2023 revenue was $12,800,000.", nil, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,800,000", Class: "Money"}),
	}, map[string]relabel{"$12,300,000": {party: "Alpha Corp", measures: "annual revenue", period: "FY2023", entity: "party"}, "$12,800,000": {party: "Alpha Corp", measures: "annual revenue", period: "FY2023", entity: "party"}})
	for name, c := range map[string]struct {
		fs        []Finding
		same, not [][2]string
	}{
		"trust dates, each its own date as period":                          {trust, [][2]string{{"October 8, 2040", "October 8, 2035"}}, nil},
		"SEC: one side's period, counterparties as each passage names them": {sec, [][2]string{{"$92,600", "$103,800.00"}}, nil},
		"two tenants by role names, one rent roll naming none":              {leases, nil, [][2]string{{"$18,500", "$19,250"}, {"$19,250", "$19,900"}}},
		"one FY2023 revenue stated two ways":                                {fy, [][2]string{{"$12,300,000", "$12,800,000"}}, nil},
	} {
		cands, _ := Candidates(c.fs, testLimit, nil)
		for _, p := range append(slices.Clone(c.same), c.not...) {
			if !slices.ContainsFunc(cands, func(x Candidate) bool {
				return x.A.Value == p[0] && x.B.Value == p[1] || x.A.Value == p[1] && x.B.Value == p[0]
			}) {
				t.Errorf("%s: %s vs %s not put to the judge", name, p[0], p[1])
			}
		}
		cs, _, _, _, _ := Join(context.Background(), testCall, c.fs, scripted{yes: c.same})
		for _, p := range c.same {
			if has(cs, p[0], p[1]) == nil {
				t.Errorf("%s: %s vs %s judged one quantity, not reported: %v", name, p[0], p[1], pairs(cs))
			}
		}
		for _, p := range c.not {
			if has(cs, p[0], p[1]) != nil {
				t.Errorf("%s: %s vs %s judged two quantities, reported", name, p[0], p[1])
			}
		}
	}
}
