// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import "testing"

// TestNearNeverPairsOppositePayments (lab-r4 verifier M4): two payments in opposite directions
// between the same two parties, each quote naming both: the entities typed are each finding's role in
// the flow, so they are never one figure stated differently; the same payer typed on both still is.
func TestNearNeverPairsOppositePayments(t *testing.T) {
	fs := []Finding{
		{FID: "F1", Verified: true, Claim: "Acme Corp paid Baker LLC $50,000 for consulting services on March 3, 2024.",
			Quotes: []Quote{{DocumentID: "ledger", Quote: "On March 3, 2024, Acme Corp paid Baker LLC $50,000 by wire for consulting services."}},
			Values: []Value{{Entity: "Acme Corp", Metric: "payment", Value: "$50,000", Class: "Money"}}},
		{FID: "F2", Verified: true, Claim: "Baker LLC paid Acme Corp $47,000 for equipment on March 10, 2024.",
			Quotes: []Quote{{DocumentID: "bank", Quote: "On March 10, 2024, Baker LLC paid Acme Corp $47,000 by wire for used equipment."}},
			Values: []Value{{Entity: "Baker LLC", Metric: "payment", Value: "$47,000", Class: "Money"}}},
	}
	if cs := Join(fs, nil); len(cs) > 0 {
		t.Errorf("two different payments paired as one figure stated differently: %+v", cs)
	}
	fs[1].Quotes[0].Quote = "On March 10, 2024, Acme Corp paid Baker LLC $47,000 by wire for consulting services."
	fs[1].Values[0].Entity = "Acme Corp"
	if cs := Join(fs, nil); len(cs) != 1 {
		t.Errorf("one payment stated differently, the same payer typed, not paired: %+v", cs)
	}
}
