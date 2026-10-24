// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// The lab-r4 r2 verifier's repros (H1, M1–M6, L3, L4, L7), kept as regression tests: each failed at
// 4c54aeb. The figure parser's full specification is figures_test.go.

func mv2(v, c string) analysis.Value { return analysis.Value{Value: v, Class: c} }

// H1: a decimal comma is read as a thousands separator: "EUR 3,5 million" is 35,000,000.
func TestR4V2DecimalCommaTenfold(t *testing.T) {
	q := "The Purchaser shall pay EUR 3,5 million on Closing."
	for _, v := range []analysis.Value{mv2("€35 million", "Money"), mv2("$35 million", "Money"), mv2("15%", "Percentage")} {
		qq := q
		if v.Class == "Percentage" {
			qq = "Interest accrues at 1,5 % per annum."
		}
		if valueInQuote(v, qq) {
			t.Errorf("%s verified from %q", v.Value, qq)
		}
	}
	if got := Untraced("The purchase price was €35 million.", q, false); len(got) == 0 {
		t.Errorf("G8 traces €35 million to %q", q)
	}
	if !valueInQuote(mv2("€3.5 million", "Money"), q) {
		t.Errorf("the correct €3.5 million is rejected")
	}
}

// H1 end to end: the tenfold figure is a verified value of a verified finding.
func TestR4V2DecimalCommaReachesLedger(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Price\nThe Purchaser shall pay EUR 3,5 million on Closing.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The Purchaser pays €35 million on Closing.", d1, "The Purchaser shall pay EUR 3,5 million on Closing", val("Purchaser", "purchase price", "€35 million", "Money")))},
	}), Config{})
	for _, x := range ledgerFindings(t, f, v.ID) {
		for _, vv := range x.Values {
			if strings.Contains(vv.Value, "35") && x.Verified {
				t.Errorf("%s verified with value %+v from a quote stating EUR 3,5 million", x.FID, vv)
			}
		}
	}
	if out := deref(v.Output); strings.Contains(out, "35 million") || strings.Contains(out, "35,000,000") {
		t.Errorf("the deliverable states a tenfold sum:\n%s", out)
	}
	fs := ledgerFindings(t, f, v.ID) // M5: the dropped value is noted in its finding, never silent
	if len(fs) != 1 || len(fs[0].Values) != 0 || len(fs[0].ValueNotes) != 1 || !strings.Contains(fs[0].ValueNotes[0], "€35 million") {
		t.Errorf("%+v", fs)
	}
}

// L3: a conflict's sides are addressed only by figures of their kind: a memo's "3,000,000 shares"
// never states a usd:3000000 side.
func TestR4V2AddressedReadsKind(t *testing.T) {
	c := analysis.Conflict{Kind: "numeric", Values: []analysis.ConflictVal{{Value: "$3,000,000", Canon: "usd:3000000"}, {Value: "$2,500,000", Canon: "usd:2500000"}}}
	if addressed(c, oracleSections("The Company issued 3,000,000 shares and paid $2,500,000."), nil) {
		t.Error("a share count addresses a dollar side")
	}
	if !addressed(c, oracleSections("The records differ: $3 million against $2,500,000."), nil) {
		t.Error("both sides stated, not addressed")
	}
	if addressed(c, oracleSections("The records differ: €3 million against $2,500,000."), nil) {
		t.Error("a euro sum addresses a dollar side")
	}
}

// L4: identical lines of two documents (invoices 1042 and 1043) are two findings, never merged.
func TestR4V2RestatedComparesDocuments(t *testing.T) {
	v := analysis.Value{Entity: "Firm", Metric: "retainer", Value: "$5,000", Class: "Money"}
	x := fnd("I1", "Invoice 1042 bills the monthly retainer of $5,000.", "Monthly retainer: $5,000", v)
	y := fnd("I1", "Invoice 1043 bills the monthly retainer of $5,000.", "Monthly retainer: $5,000", v)
	x.Quotes[0].DocumentID, y.Quotes[0].DocumentID = "inv1042", "inv1043"
	if restated(x, y) {
		t.Error("identical lines of two documents merged")
	}
	y.Quotes[0].DocumentID = "inv1042"
	if !restated(x, y) {
		t.Error("an exact duplicate in one document is not merged")
	}
}

// L7: a long requirement's passage query keeps its names, defined terms and numbers first.
func TestR4V2PassageQueryKeepsNames(t *testing.T) {
	r := "Notwithstanding anything to the contrary in the Engagement Letter, the Consultant, Dr. Lee, shall provide comprehensive quarterly compliance certifications, independently audited reconciliation statements, contemporaneous documentation of every subcontracting arrangement, written acknowledgements from representatives, confidentiality undertakings, indemnification confirmations and insurance certificates evidencing professional liability coverage within thirty days of the Fee being paid to Dr. Wu."
	q := " " + passageQuery(r) + " "
	for _, w := range []string{"lee", "wu", "thirty", "fee", "consultant", "engagement"} {
		if !strings.Contains(q, " "+w+" ") {
			t.Errorf("%q is not in the passage query %q", w, q)
		}
	}
	if n := len(strings.Fields(q)); n > passageTerms {
		t.Errorf("%d words", n)
	}
}
