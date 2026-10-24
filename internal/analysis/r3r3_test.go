// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import "testing"

// The lab-r3 round-2 verifier's join repros (M2, M3), kept as regression tests; each failed on 7a5d6be.

func r2vF(fid, doc, quote string, v Value) Finding {
	return Finding{FID: fid, Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}}, Values: []Value{v}}
}

// TestR2VScaledVsWholeFigure (M2): a whole figure against a scaled one under one label conflicts
// (their normalised magnitudes differ); a bare figure whose scale nothing states still does not pair
// with a scaled one, and one figure in two forms is no conflict.
func TestR2VScaledVsWholeFigure(t *testing.T) {
	spa := r2vF("F1", "spa", "The aggregate Purchase Price payable by Buyer is $92,000,000, subject to adjustment.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$92,000,000", Class: "Money"})
	for _, c := range []struct {
		name string
		b    Finding
		want int
	}{
		{"money", r2vF("F2", "board", "The Board approved a Purchase Price of $95 million payable by Buyer.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$95 million", Class: "Money"}), 1},
		{"money unit", r2vF("F2", "board", "The Board approved a Purchase Price of 95 million dollars payable by Buyer.", Value{Entity: "Buyer", Metric: "purchase price", Value: "95", Unit: "million dollars", Class: "Money"}), 1},
		{"one figure, two forms", r2vF("F2", "board", "The Board approved a Purchase Price of $92 million payable by Buyer.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$92 million", Class: "Money"}), 0},
	} {
		if cs := Join([]Finding{spa, c.b}, nil); len(cs) != c.want {
			t.Errorf("%s: %d conflicts, want %d: %+v", c.name, len(cs), c.want, cs)
		}
	}
	bare := r2vF("F3", "table", "Purchase price\t92", Value{Entity: "Buyer", Metric: "purchase price", Value: "92", Class: "Money"})
	scaled := r2vF("F4", "board", "The Board approved a Purchase Price of $95 million payable by Buyer.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$95 million", Class: "Money"})
	if cs := Join([]Finding{bare, scaled}, nil); len(cs) != 0 {
		t.Errorf("a bare figure of unknown scale paired with a scaled one: %+v", cs)
	}
}

// TestR2VHoldbackNearFalsePair (M3): part of a figure, labelled with the figure's own name in its
// quote (a closing payment net of a 5% holdback, a deposit toward the price), never pairs with it; a
// real misstatement of the price still does.
func TestR2VHoldbackNearFalsePair(t *testing.T) {
	a := r2vF("F1", "spa", "The Purchase Price is $1,000,000, payable by Buyer to Seller at the Closing.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$1,000,000", Class: "Money"})
	for _, b := range []Finding{
		r2vF("F2", "funds-flow", "At the Closing, Buyer shall wire $950,000 of the Purchase Price to Seller; the balance is held back.", Value{Entity: "Buyer", Metric: "closing payment", Value: "$950,000", Class: "Money"}),
		r2vF("F2", "funds-flow", "At the Closing, Buyer shall wire $950,000 of the Purchase Price to Seller; the balance is held back.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$950,000", Class: "Money"}),
		r2vF("F2", "escrow", "Buyer shall pay a deposit of $950,000 toward the Purchase Price into escrow.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$950,000", Class: "Money"}),
	} {
		if cs := Join([]Finding{a, b}, nil); len(cs) > 0 {
			t.Errorf("a part of the price is reported as the price stated differently: %q: %+v", b.Quotes[0].Quote, cs)
		}
	}
	real := r2vF("F3", "board", "The Board approved the Purchase Price of $950,000 payable by Buyer to Seller at the Closing.", Value{Entity: "Buyer", Metric: "purchase price", Value: "$950,000", Class: "Money"})
	if cs := Join([]Finding{a, real}, nil); len(cs) != 1 {
		t.Errorf("the price stated as $950,000 in another document: %+v", cs)
	}
	dep := r2vF("F4", "spa", "Buyer shall pay a deposit of $50,000 within two days of signing.", Value{Entity: "Buyer", Metric: "deposit", Value: "$50,000", Class: "Money"})
	dep2 := r2vF("F5", "escrow", "Buyer shall pay a deposit of $45,000 within two days of signing.", Value{Entity: "Buyer", Metric: "deposit", Value: "$45,000", Class: "Money"})
	if cs := Join([]Finding{dep, dep2}, nil); len(cs) != 1 {
		t.Errorf("two deposits stated differently: %+v", cs)
	}
}
