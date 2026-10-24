// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import "testing"

// lab-r4 r4 verifier repros (critique/biglaw-lab-r4-r4-verifier.md), redone under the owner's
// decision that number meaning, whose figure it is included, is the figure reader's (Value.Fig).

func fnd(fid, doc, quote string, ents []Entity, vals ...Value) Finding {
	return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Entities: ents, Values: vals}
}

func firm(n string) Entity { return Entity{Name: n, Class: "Firm"} }

// M1: the near join paired two parties' close figures of one metric whenever either party was named
// anywhere in the other's document (the normal multi-party matter). With the reader's labels each
// figure is its own party's (the two subsidiaries', the Buyer's and the Seller's, the two
// officers', the two tenants', the adviser's and its fund's), and two parties' figures never pair;
// a figure whose party the reader could not tell is never part of a conflict.
func TestR4V4NearJoinPartiesFromTheReader(t *testing.T) {
	cases := map[string][]Finding{
		"two subsidiaries": {
			fnd("F1", "10k", "Alpha Corp reported annual revenue of $12,300,000 for fiscal 2024.", []Entity{firm("Alpha Corp")}, Value{Entity: "Alpha Corp", Metric: "annual revenue", Value: "$12,300,000", Class: "Money"}),
			fnd("F2", "10k", "Beta Corp is a wholly owned subsidiary of Parent Holdings Inc.", []Entity{firm("Beta Corp"), firm("Parent Holdings Inc")}),
			fnd("F3", "board", "Beta Corp reported annual revenue of $12,800,000 for fiscal 2024.", []Entity{firm("Beta Corp")}, Value{Entity: "Beta Corp", Metric: "annual revenue", Value: "$12,800,000", Class: "Money"}),
			fnd("F4", "board", "Alpha Corp is a wholly owned subsidiary of Parent Holdings Inc.", []Entity{firm("Alpha Corp"), firm("Parent Holdings Inc")}),
		},
		"parent and its fund": {
			fnd("F1", "lpa", "Under the Agreement, Northwind Capital LLC receives an annual management fee of $1,250,000 from the Fund.", []Entity{firm("Northwind Capital LLC")}, Value{Entity: "Northwind Capital LLC", Metric: "annual management fee", Value: "$1,250,000", Class: "Money"}),
			fnd("F2", "ppm", "Northwind Opportunity Fund LP pays an annual management fee of $1,300,000 under its own advisory agreement.", []Entity{firm("Northwind Opportunity Fund LP")}, Value{Entity: "Northwind Opportunity Fund LP", Metric: "annual management fee", Value: "$1,300,000", Class: "Money"}),
			fnd("F3", "ppm", "Northwind Capital LLC is the general partner of the Fund.", []Entity{firm("Northwind Capital LLC")}),
		},
		"buyer and seller": {
			fnd("F1", "spa", "The Buyer shall pay an escrow deposit of $2,400,000 at signing.", []Entity{firm("Buyer")}, Value{Entity: "Buyer", Metric: "escrow deposit", Value: "$2,400,000", Class: "Money"}),
			fnd("F2", "disclosure", "The Seller shall fund an escrow deposit of $2,600,000 for indemnity claims.", []Entity{firm("Seller")}, Value{Entity: "Seller", Metric: "escrow deposit", Value: "$2,600,000", Class: "Money"}),
			fnd("F3", "disclosure", "The Buyer and the Seller agree the escrow deposit is held by the Escrow Agent.", []Entity{firm("Buyer"), firm("Seller")}),
		},
		"two officers, the clause naming the office": {
			fnd("F1", "hr", "The annual base salary for the Chief Executive Officer is $410,000 under the employment agreement.", nil, Value{Entity: "Jane Moss", Metric: "annual base salary", Value: "$410,000", Class: "Money"}),
			fnd("F2", "comp", "The annual base salary for the Chief Financial Officer is $395,000 under the employment agreement.", nil, Value{Entity: "Raj Patel", Metric: "annual base salary", Value: "$395,000", Class: "Money"}),
			fnd("F3", "comp", "Jane Moss serves as Chief Executive Officer.", []Entity{{Name: "Jane Moss", Class: "Person"}}),
			fnd("F4", "hr", "Raj Patel serves as Chief Financial Officer.", []Entity{{Name: "Raj Patel", Class: "Person"}}),
		},
		"two tenants": {
			fnd("F1", "lease1", "Tenant Harbor Foods Inc. shall pay monthly base rent of $18,500 for the Premises.", []Entity{firm("Harbor Foods Inc")}, Value{Entity: "Harbor Foods Inc", Metric: "monthly base rent", Value: "$18,500", Class: "Money"}),
			fnd("F2", "lease2", "Tenant Pine Street Bakery LLC shall pay monthly base rent of $19,250 for the Premises.", []Entity{firm("Pine Street Bakery LLC")}, Value{Entity: "Pine Street Bakery LLC", Metric: "monthly base rent", Value: "$19,250", Class: "Money"}),
			fnd("F3", "lease2", "Harbor Foods Inc. occupies the adjacent unit.", []Entity{firm("Harbor Foods Inc")}),
		},
	}
	for name, fs := range cases {
		if cs := joinRead(fs, nil); len(cs) > 0 {
			t.Errorf("%s: false conflicts %v (%+v)", name, pairs(cs), cs[0].Values)
		}
	}
	// one party's figure under two extractor labels pairs when the reader reads both as its own
	two := []Finding{
		fnd("F1", "spa", "The Buyer shall pay an escrow deposit of $2,400,000 at signing.", []Entity{firm("Buyer")}, Value{Entity: "Buyer", Metric: "escrow deposit", Value: "$2,400,000", Class: "Money"}),
		fnd("F2", "letter", "The escrow deposit the Buyer funded at signing was $2,600,000.", nil, Value{Entity: "Escrow Agent account", Metric: "escrow deposit", Value: "$2,600,000", Class: "Money"}),
	}
	if got := pairs(Join(readParties(two, map[string]string{"$2,600,000": "Buyer"}), nil)); len(got) != 1 {
		t.Errorf("one party's escrow deposit stated two ways is no conflict: %v", got)
	}
	unknown := readParties(two, map[string]string{"$2,600,000": "Buyer"})
	unknown[1].Values[0].Fig.Party = ""
	if got := pairs(Join(unknown, nil)); len(got) != 0 {
		t.Errorf("a figure whose party the reader could not tell is part of a conflict: %v", got)
	}
}

// Gate A (bench6, the ledger verbatim from biglaw-bench6.pgdump): no conflicts; at 4b5f776 the near
// join made up "service date 10/21 vs Oct 26 (file-note date)".
func TestJoinBench6GateANoConflicts(t *testing.T) {
	if cs := joinRead(bench6Ledger(t, "gatea"), nil); len(cs) > 0 {
		t.Errorf("gate A conflicts %v", pairs(cs))
	}
}
