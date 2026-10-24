// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import "testing"

// TestPartOfWordings (lab-r3 r3 M3, r4 M3 and L5): every wording from both verifier rounds, one table.
// A part of the price (a closing payment net of a holdback, a sum held in escrow, an instalment under
// a "payable as follows:" frame) never pairs with the price; the price stated differently does,
// whatever deductions or payment terms follow it.
func TestPartOfWordings(t *testing.T) {
	mv := func(ent, metric, val string) Value {
		return Value{Entity: ent, Metric: metric, Value: val, Class: "Money"}
	}
	f := func(fid, doc, quote string, v Value) Finding {
		return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: []Value{v}}
	}
	price := f("F1", "spa", "The Purchase Price is $1,000,000, payable by Buyer to Seller at the Closing.", mv("Buyer", "purchase price", "$1,000,000"))
	p950 := func(q string) Finding { return f("F2", "ff", q, mv("Buyer", "purchase price", "$950,000")) }
	p12 := func(q string) Finding { return f("F2", "board", q, mv("Buyer", "purchase price", "$1,200,000")) }
	for _, c := range []struct {
		name string
		b    Finding
		want bool // a conflict
	}{
		// r4 M3: closing payments, parts of the price
		{"wire X of the price", p950("At the Closing, Buyer shall wire $950,000 of the Purchase Price to Seller; the balance is held back."), false},
		{"wire X, being the price less holdback", p950("At the Closing, Buyer shall wire $950,000 to Seller, being the Purchase Price less the Holdback."), false},
		{"closing payment (price net of holdback)", p950("Buyer shall pay $950,000 at Closing (the Purchase Price net of the 5% Holdback)."), false},
		{"closing payment of X", p950("The closing payment of $950,000 shall be wired by Buyer to Seller."), false},
		{"X at closing, part held in escrow", p950("Buyer shall pay Seller $950,000 at Closing, with $50,000 of the Purchase Price held in escrow."), false},
		{"payable as follows: X at closing", p950("The Purchase Price is payable as follows: $950,000 at Closing and $50,000 into escrow."), false},
		{"payable in instalments: X", p950("The Purchase Price is payable in two instalments: $950,000 at Closing and $50,000 a year later."), false},
		{"X minus the holdback", p950("Buyer shall pay $950,000 to Seller at Closing, that is the Purchase Price minus the Holdback."), false},
		{"deposit toward the price", p950("Buyer shall pay a deposit of $950,000 toward the Purchase Price into escrow."), false},
		// the price stated differently: conflicts
		{"real misstatement $950,000", p950("The Board approved the Purchase Price of $950,000 payable by Buyer to Seller at the Closing."), true},
		{"payment of the price of X", p12("Payment of the Purchase Price of $1,200,000 by Buyer shall be made at Closing."), true},
		{"price is X, deposit credited", p12("The Purchase Price is $1,200,000, payable by Buyer; the deposit is credited against it."), true},
		{"payment obligation: price is X", p12("Buyer's payment obligation: the Purchase Price is $1,200,000."), true},
		{"price: balance of X", p12("The Purchase Price shall be a balance of $1,200,000 payable by Buyer."), true},
		{"price is X in two instalments", p12("The Purchase Price payable by Buyer is $1,200,000, in two instalments."), true},
		{"price is X, less the deposit", p12("The Purchase Price payable by Buyer is $1,200,000, less the deposit already paid."), true},
		{"price is X, with part held in escrow", p12("The Purchase Price is $1,200,000, of which $50,000 shall be held in escrow."), true},
		{"L5 total payments of X", p12("The Purchase Price, being total payments of $1,200,000 by Buyer."), true},
		// r5 M1: a holdback or deduction elsewhere in the sentence does not make the price a part
		{"pay X for the Shares, of which Y in escrow", p12("Buyer will pay $1,200,000 for the Shares (the Purchase Price), of which $100,000 will be held in escrow."), true},
		{"pay X at Closing as the price, less the deposit", p12("Buyer shall pay Seller $1,200,000 at Closing as the Purchase Price, less the Deposit already paid."), true},
		{"the price, X, net of the deposit", p12("Buyer shall pay the Purchase Price, $1,200,000, at Closing, net of the Deposit."), true},
		{"price shall equal X; holdback elsewhere", p12("The Purchase Price shall equal $1,200,000 and the Holdback Amount shall be $50,000."), true},
		{"price is X less deposit", p12("The Purchase Price is $1,200,000, less the deposit."), true},
		{"price of X payable as follows: X", p12("The Purchase Price of $1,200,000 is payable as follows: $1,200,000 at Closing."), true},
		// r5 (LOW, pre-existing): a portion of the price, and the price less a holdback, being X
		{"price less holdback, being X", p950("At Closing Buyer shall pay the Purchase Price less the Holdback, being $950,000."), false},
		{"balance of the price of X", p950("The balance of the Purchase Price of $950,000 shall be paid by Buyer at Closing."), false},
		{"cash portion of the price is X", p950("The cash portion of the Purchase Price is $950,000, payable by Buyer at Closing."), false},
		{"remaining portion of the price, X,", p950("The remaining portion of the Purchase Price, $950,000, is payable by Buyer at Closing."), false},
		{"X held in escrow", p950("Buyer shall deposit $950,000 to be held in escrow until Closing."), false},
	} {
		if got := len(Join([]Finding{price, c.b}, nil)) > 0; got != c.want {
			t.Errorf("%s: conflict %v, want %v", c.name, got, c.want)
		}
	}
}
