// SPDX-License-Identifier: Apache-2.0
package evidencegraph

import "testing"

func TestConductsFromTypedTriples(t *testing.T) {
	src := "The Division alleges WCA engaged in Cherry-Picking Trade Allocations in violation of Section 206 of the Advisers Act, and that Marcus Bellini directed brokerage to Lakeshore, and Whitmore instructed deletion of records (Obstruction of Examination)."
	g := New()
	add := func(s, sc, p, o, oc, q string) {
		if !g.AddTriple(s, sc, p, o, oc, "", q, "ref", src) {
			t.Logf("REJECTED: %s -%s-> %s (q=%s)", s, p, o, q)
		}
	}
	add("Cherry-Picking Trade Allocations", "Conduct", "committedBy", "WCA", "Party", "WCA engaged in Cherry-Picking Trade Allocations")
	add("Cherry-Picking Trade Allocations", "Conduct", "violates", "Section 206 of the Advisers Act", "Authority", "in violation of Section 206 of the Advisers Act")
	// reversed direction — should be re-oriented by Normalize:
	add("Section 206 of the Advisers Act", "Authority", "violates", "Obstruction of Examination", "Conduct", "Obstruction of Examination")
	add("Marcus Bellini", "Party", "directed brokerage to", "Lakeshore", "Broker", "Bellini directed brokerage to Lakeshore")
	conducts := g.Conducts()
	t.Logf("Conducts() = %v", conducts)
	if len(conducts) < 2 {
		t.Errorf("expected >=2 conducts, got %d: %v", len(conducts), conducts)
	}
}
