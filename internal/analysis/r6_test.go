// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"slices"
	"testing"
)

// lab-r4 r6 verifier repros for the join (critique/biglaw-lab-r4-r6-verifier.md), and joins driven by
// deliberately wrong reader labels (r6 L5): what one wrong label can and cannot make the join assert.

// M1: bench6 SEC $92,600 (typed Bellini) vs $103,800 (typed under its payer, Kevin Ostrowski, which
// the reader calls another party) is one payee's compensation stated two ways: LAB ISSUE_003.
func TestR6SECPaymentTypedUnderThePayerStillJoins(t *testing.T) {
	got := pairs(Join(readAs(bench6Ledger(t, "sec"), bench6SECLabels), nil))
	if !slices.Contains(got, "F269/F539") {
		t.Errorf("$92,600 vs $103,800 (one party's compensation, one side typed under its payer) not reported: %v", got)
	}
}

// M1: the e-mail date typed under its recipient Chen and the referral's April 22 typed under
// Ostrowski (the party) are one instruction's date: four planted, four found.
func TestR6FourPlantedFindsFour(t *testing.T) {
	if cs := Join(fourPlanted(), []Alias{{"Ostrowski", []string{"Bayshore Palms LLC"}}}); len(cs) != 4 {
		t.Errorf("want the 4 planted discrepancies, got %d: %v", len(cs), pairs(cs))
	}
}

// The counterparty rule keeps every r4 false conflict closed, also when a typed entity is read as
// another party: CEO vs CFO under one payer, two employees typed under their payer, two tenants of
// one landlord, Buyer vs Seller with the escrow agent read as the party, two subsidiaries.
func TestR6OtherTypedEntityKeepsR4FalseConflictsClosed(t *testing.T) {
	sal := func(fid, doc, quote, ent, v string) Finding {
		return fnd(fid, doc, quote, nil, Value{Entity: ent, Metric: "annual base salary", Value: v, Class: "Money"})
	}
	cases := map[string]struct {
		fs []Finding
		ls map[string]relabel
	}{
		"CEO vs CFO, the payer read as party, the officers other": {[]Finding{
			sal("F1", "hr", "Acme Corp shall pay the Chief Executive Officer an annual base salary of $410,000.", "Jane Moss", "$410,000"),
			sal("F2", "comp", "Acme Corp shall pay the Chief Financial Officer an annual base salary of $395,000.", "Raj Patel", "$395,000"),
		}, map[string]relabel{"$410,000": {party: "Acme Corp", measures: "annual base salary", entity: "other"}, "$395,000": {party: "Acme Corp", measures: "annual base salary", entity: "other"}}},
		"two employees typed under one payer": {[]Finding{
			sal("F1", "hr", "Acme Corp pays Jane Moss an annual base salary of $410,000.", "Acme Corp", "$410,000"),
			sal("F2", "comp", "Acme Corp pays Raj Patel an annual base salary of $395,000.", "Acme Corp", "$395,000"),
		}, map[string]relabel{"$410,000": {party: "Jane Moss", measures: "annual base salary", entity: "other"}, "$395,000": {party: "Raj Patel", measures: "annual base salary", entity: "other"}}},
		"two tenants, the landlord read as party": {[]Finding{
			fnd("F1", "lease1", "Tenant Harbor Foods Inc. shall pay Landlord monthly base rent of $18,500.", nil, Value{Entity: "Harbor Foods Inc", Metric: "monthly base rent", Value: "$18,500", Class: "Money"}),
			fnd("F2", "lease2", "Tenant Pine Street Bakery LLC shall pay Landlord monthly base rent of $19,250.", nil, Value{Entity: "Pine Street Bakery LLC", Metric: "monthly base rent", Value: "$19,250", Class: "Money"}),
		}, map[string]relabel{"$18,500": {party: "Landlord", measures: "monthly base rent", entity: "other"}, "$19,250": {party: "Landlord", measures: "monthly base rent", entity: "other"}}},
		"Buyer vs Seller, the escrow agent read as party": {[]Finding{
			fnd("F1", "spa", "The Buyer shall pay an escrow deposit of $2,400,000 to the Escrow Agent.", nil, Value{Entity: "Buyer", Metric: "escrow deposit", Value: "$2,400,000", Class: "Money"}),
			fnd("F2", "disclosure", "The Seller shall fund an escrow deposit of $2,600,000 with the Escrow Agent.", nil, Value{Entity: "Seller", Metric: "escrow deposit", Value: "$2,600,000", Class: "Money"}),
		}, map[string]relabel{"$2,400,000": {party: "Escrow Agent", measures: "escrow deposit", entity: "other"}, "$2,600,000": {party: "Escrow Agent", measures: "escrow deposit", entity: "other"}}},
		"two subsidiaries": {[]Finding{
			fnd("F1", "10k", "Alpha Corp reported annual revenue of $12,300,000 for fiscal 2024.", nil, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,300,000", Class: "Money"}),
			fnd("F2", "board", "Beta Corp reported annual revenue of $12,800,000 for fiscal 2024.", nil, Value{Entity: "Beta Corp", Metric: "annual revenue", Value: "$12,800,000", Class: "Money"}),
		}, map[string]relabel{"$12,300,000": {party: "Alpha Corp", measures: "annual revenue", period: "fiscal 2024", entity: "party"}, "$12,800,000": {party: "Beta Corp", measures: "annual revenue", period: "fiscal 2024", entity: "party"}}},
	}
	for name, c := range cases {
		if cs := Join(readAs(c.fs, c.ls), nil); len(cs) > 0 {
			t.Errorf("%s: false conflict %v", name, pairs(cs))
		}
	}
}

// L2: a period is compared only as the reader states it absolutely, with its year: "the second
// quarter" of two years (or no period given) is unknown, and an unknown period never matches another.
func TestR6PeriodTextCollides(t *testing.T) {
	fs := []Finding{
		fnd("F1", "q2-2023", "Alpha Corp's revenue for the second quarter was $4,100,000.", nil, Value{Entity: "Alpha Corp", Metric: "quarterly revenue", Value: "$4,100,000", Class: "Money"}),
		fnd("F2", "q2-2024", "Alpha Corp's revenue for the second quarter was $4,600,000.", nil, Value{Entity: "Alpha Corp", Metric: "quarterly revenue", Value: "$4,600,000", Class: "Money"}),
	}
	for _, p := range [][2]string{{"the second quarter", "the second quarter"}, {"", ""}, {"Q2", "Q2"}, {"2023-Q2", "the second quarter"}} {
		ls := map[string]relabel{"$4,100,000": {party: "Alpha Corp", measures: "revenue", period: p[0], entity: "party"}, "$4,600,000": {party: "Alpha Corp", measures: "revenue", period: p[1], entity: "party"}}
		fs := readAs(fs, ls)
		fs[0].Values[0].Fig.Period, fs[1].Values[0].Fig.Period = p[0], p[1]
		if cs := Join(fs, nil); len(cs) > 0 {
			t.Errorf("periods %q: two quarters joined by an unknown period: %v", p, pairs(cs))
		}
	}
	ls := map[string]relabel{"$4,100,000": {party: "Alpha Corp", measures: "revenue", period: "2024-Q2", entity: "party"}, "$4,600,000": {party: "Alpha Corp", measures: "revenue", period: "2024-q2", entity: "party"}}
	if cs := Join(readAs(fs, ls), nil); len(cs) != 1 {
		t.Errorf("one stated quarter's revenue two ways: %v", pairs(cs))
	}
}

// L5: joins driven by wrong reader labels. One wrong label never makes a conflict the typed
// entities, counterparties or periods contradict; a wrong measure or party only loses one. What two
// consistent wrong labels assert is the reader's (its labels are checked for form, not truth).
func TestR6WrongLabelsAssertNoConflict(t *testing.T) {
	salary := func(fid, doc, who, amount, value string, f *Reading) Finding {
		return fnd(fid, doc, "Acme Corp shall pay "+who+" an annual base salary of "+value+".", nil,
			Value{Entity: who, Metric: "annual base salary", Value: value, Class: "Money", Fig: f})
	}
	acme := func(amount string) *Reading {
		return figOf(KindMoney, "USD", amount, "Acme Corp", "annual base salary", "none")
	}
	with := func(r *Reading, f func(*Reading)) *Reading { f(r); return r }
	for name, fs := range map[string][]Finding{
		"the payer read as both officers' party": {salary("F1", "hr", "Jane Moss", "410000", "$410,000", acme("410000")), salary("F2", "comp", "Raj Patel", "395000", "$395,000", acme("395000"))},
		"one side wrongly says its typed name is the party": {
			salary("F1", "hr", "Jane Moss", "410000", "$410,000", with(acme("410000"), func(r *Reading) { r.TypedEntity = "party" })),
			salary("F2", "comp", "Raj Patel", "395000", "$395,000", acme("395000"))},
		"two counterparties for one party's figure": {
			salary("F1", "hr", "", "410000", "$410,000", with(acme("410000"), func(r *Reading) { r.Party, r.Counterparty = "Jane Moss", "Acme Corp" })),
			salary("F2", "comp", "", "395000", "$395,000", with(acme("395000"), func(r *Reading) { r.Party, r.Counterparty = "Jane Moss", "Acme Holdings" }))},
		"a counterparty against the other side's unexplained typed name": {
			salary("F1", "hr", "", "410000", "$410,000", with(acme("410000"), func(r *Reading) { r.Party, r.Counterparty = "Jane Moss", "Acme Corp" })),
			salary("F2", "comp", "Beta LLC", "395000", "$395,000", with(acme("395000"), func(r *Reading) { r.Party = "Jane Moss" }))},
		"one measure misnamed": {salary("F1", "hr", "", "410000", "$410,000", acme("410000")),
			salary("F2", "comp", "", "395000", "$395,000", with(acme("395000"), func(r *Reading) { r.Measures = "signing bonus" }))},
		"a period with no year": {salary("F1", "hr", "", "410000", "$410,000", with(acme("410000"), func(r *Reading) { r.Period = "this year" })),
			salary("F2", "comp", "", "395000", "$395,000", with(acme("395000"), func(r *Reading) { r.Period = "this year" }))},
		"an unclear reading": {salary("F1", "hr", "", "410000", "$410,000", acme("410000")),
			salary("F2", "comp", "", "395000", "$395,000", with(acme("395000"), func(r *Reading) { r.Unclear = true }))},
	} {
		if cs := Join(fs, nil); len(cs) > 0 {
			t.Errorf("%s: a conflict asserted on wrong labels: %v", name, pairs(cs))
		}
	}
	// the reader's counterparty separates Buyer→Seller from Seller→Buyer even when it names one party for both
	dep := func(fid, doc, from, to, amount, value string) Finding {
		r := figOf(KindMoney, "USD", amount, "Escrow Agent", "escrow deposit", "none")
		r.Counterparty = from
		return fnd(fid, doc, from+" shall deposit "+value+" with the Escrow Agent for "+to+".", nil, Value{Metric: "escrow deposit", Value: value, Class: "Money", Fig: r})
	}
	if cs := Join([]Finding{dep("F1", "spa", "Buyer", "Seller", "2400000", "$2,400,000"), dep("F2", "side", "Seller", "Buyer", "2600000", "$2,600,000")}, nil); len(cs) > 0 {
		t.Errorf("two depositors' deposits under one agent joined: %v", pairs(cs))
	}
	if cs := Join([]Finding{dep("F1", "spa", "Buyer", "Seller", "2400000", "$2,400,000"), dep("F2", "side", "Buyer", "Seller", "2600000", "$2,600,000")}, nil); len(cs) != 1 {
		t.Errorf("one depositor's deposit stated two ways: %v", pairs(cs))
	}
}

// H1: a scale's source words must lie before the figure (a heading, a header, a lead-in) or right
// after its digits, and a clear match at a scale needs equivalent words on both sides (folded: case,
// punctuation, a plural); "fee" never matches "thousand", "(In Millions)" matches "million".
func TestR6ScaleWordsAndPlace(t *testing.T) {
	read := func(text, from, scale string) Reading {
		rs, _ := ReadLabels([]Passage{PassageOf("q1", text)}, []Label{lbl("q1.s1", KindMoney, "USD", scale, str(from), ".", "positive")})
		return rs["q1.s1"]
	}
	if r := read("The filing fee is $500 per application. Program costs (in thousands): 1,200.", "in thousands", "1000"); !r.Unclear || r.Scale != "" {
		t.Errorf("a scale from a later figure's header applied: %+v", r)
	}
	if r := read("Each holder received $5 per share.", "share", "1000000"); !r.Unclear || r.Scale != "" {
		t.Errorf("a scale from a word after the figure (not right after it) applied: %+v", r)
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{{"(In Millions)", "million", true}, {"in thousands", "Thousand", true}, {"fee", "thousand", false}, {"($000)", "$000", true}, {"($000)", "thousand", false}, {"mm", "million", false}} {
		x := Reading{Kind: KindMoney, Currency: "USD", Amount: "500000", Scale: "1000", ScaleFrom: c.a}
		y := Reading{Kind: KindMoney, Currency: "USD", Amount: "500000", Scale: "1000", ScaleFrom: c.b}
		if same, soft := SameFigure(x, y); same != c.same || !same && !soft {
			t.Errorf("%q vs %q: same %v unclear %v, want same %v (else unclear)", c.a, c.b, same, soft, c.same)
		}
	}
}
