// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
)

func bench5Findings(t *testing.T) []Finding {
	t.Helper()
	data, err := os.ReadFile("testdata/bench5-sec-conflicts.json")
	if err != nil {
		t.Fatal(err)
	}
	var in struct{ Findings []Finding }
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	return in.Findings
}

// pairOf reports whether conflicts hold a conflict between values a and b.
func pairOf(cs []Conflict, a, b string) bool {
	return slices.ContainsFunc(cs, func(c Conflict) bool {
		vs := map[string]bool{}
		for _, v := range c.Values {
			vs[v.Value] = true
		}
		return vs[a] && vs[b]
	})
}

// TestJoinPairsBench5Conflicts (bench5 SEC, C-032 C-035 C-041: 4,217 vs 4,312 trades, $92,600 vs
// $103,800, April 22 vs April 15 were never paired although each value was in the ledger): over the
// run's own ledger rows, the join pairs all three. 4,217 is the "Office of Quantitative Research"'s
// count of trades it analysed "through the omnibus account", 4,312 the "Omnibus Equity Trades"' count:
// one figure's two entity labels, joined because one side's quotes name the other's entity
// (mentions). $103,800 and the April 15 e-mail are the rows the verifier fixes now admit.
func TestJoinPairsBench5Conflicts(t *testing.T) {
	fs := bench5Findings(t)
	cs := Join(fs, nil)
	for _, p := range [][2]string{{"4,217", "4,312"}, {"$92,600", "$103,800.00"}, {"April 22, 2024", "15 Apr 2024"}} {
		if !pairOf(cs, p[0], p[1]) {
			t.Errorf("%s vs %s not paired: %+v", p[0], p[1], cs)
		}
	}
	for _, p := range [][2]string{{"$47,600", "$103,800.00"}, {"March 11, 2024", "15 Apr 2024"}} { // a part against the whole; two events
		if pairOf(cs, p[0], p[1]) {
			t.Errorf("%s vs %s paired: %+v", p[0], p[1], cs)
		}
	}
}

// TestJoinDiscrepanciesFidsDoNotScopeTheJoin (bench5: per-issue fids split each pair across issues):
// naming one side's findings still finds the conflict with the other side's, and only conflicts
// holding a named finding are reported.
func TestJoinDiscrepanciesFidsDoNotScopeTheJoin(t *testing.T) {
	fs := bench5Findings(t)
	rt := &agent.Runtime{Reg: agent.NewRegistry()}
	for _, tool := range Tools(memSource(fs)) {
		if err := rt.Reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	res, err := rt.Invoke(context.Background(), agent.Agent{Name: "lead", Visible: []string{"numbers", "join_discrepancies"}}, agent.Call{TaskID: "t"}, "join_discrepancies",
		json.RawMessage(`{"fids":["F80"],"aliases":null}`), agent.NewBudget(10, 1, 0), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Conflicts []Conflict }
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Conflicts) != 1 || !pairOf(out.Conflicts, "4,217", "4,312") {
		t.Fatalf("conflicts for F80: %s", res.Output)
	}
	if !strings.Contains(spec("join_discrepancies").Description, "fids only choose which conflicts are reported") {
		t.Error("the spec does not say fids do not scope the join")
	}
}

type memSource []Finding

func (m memSource) Findings(context.Context, string) ([]Finding, error) { return m, nil }
