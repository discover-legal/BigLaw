// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import "testing"

func v3F(fid, doc, quote string, v Value) Finding {
	return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: []Value{v}}
}

// TestV3RealConflictsFound (lab-r3 r3 verifier M3): a portion word near a value, not framing it,
// no longer hides a real conflict; each case conflicted on 7a5d6be and was missed on c3274c3. The
// closing payment net of a holdback still does not pair (TestR2VHoldbackNearFalsePair).
func TestV3RealConflictsFound(t *testing.T) {
	mv := func(ent, metric, val string) Value {
		return Value{Entity: ent, Metric: metric, Value: val, Class: "Money"}
	}
	price := v3F("F1", "spa", "The Purchase Price payable by Buyer is $1,000,000.", mv("Buyer", "purchase price", "$1,000,000"))
	for _, c := range []struct {
		name string
		a, b Finding
	}{
		{"deposit 50k vs 60k", v3F("F1", "spa", "Buyer shall pay a deposit of $50,000 on signing.", mv("Buyer", "deposit", "$50,000")),
			v3F("F2", "loi", "Buyer shall pay a deposit of $60,000 on signing.", mv("Buyer", "deposit", "$60,000"))},
		{"payment of the purchase price", price, v3F("F2", "board", "Payment of the Purchase Price of $1,200,000 by Buyer shall be made at Closing.", mv("Buyer", "purchase price", "$1,200,000"))},
		{"price payable in instalments", price, v3F("F2", "board", "The Purchase Price payable by Buyer is $1,200,000, in two instalments.", mv("Buyer", "purchase price", "$1,200,000"))},
		{"price with deposit mention", price, v3F("F2", "board", "The Purchase Price payable by Buyer is $1,200,000, less the deposit already paid.", mv("Buyer", "purchase price", "$1,200,000"))},
		{"rent monthly payments", v3F("F1", "lease", "Tenant shall pay rent of $5,000 per month.", mv("Tenant", "monthly rent", "$5,000")),
			v3F("F2", "memo", "Tenant's monthly rent of $5,500 is due in monthly payments on the first.", mv("Tenant", "monthly rent", "$5,500"))},
		{"scaled", v3F("F1", "spa", "The Purchase Price payable by Buyer is $92,000,000.", mv("Buyer", "purchase price", "$92,000,000")),
			v3F("F2", "board", "The Board approved a Purchase Price of $95 million payable by Buyer.", mv("Buyer", "purchase price", "$95 million"))},
		{"loan advance", v3F("F1", "credit", "The Lender's commitment to Borrower is $10,000,000.", mv("Borrower", "commitment", "$10,000,000")),
			v3F("F2", "memo", "The Lender's commitment to Borrower is $12,000,000, available by advance.", mv("Borrower", "commitment", "$12,000,000"))},
	} {
		if cs := joinRead([]Finding{c.a, c.b}, nil); len(cs) == 0 {
			t.Errorf("missed %s", c.name)
		}
	}
}
