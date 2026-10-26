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

// bench5Labels are the bench5 pairs' values as the figure reader reads them (the right labels): the
// extractor typed them under other labels ("Office of Quantitative Research", "Omnibus Equity Trades",
// "Email": no party at all) and metrics; the record says whose figures they are and what they measure.
var bench5Labels = func() map[string]relabel {
	wca, bellini, whitmore := "Whitmore Capital Advisors LLC", "Marcus T. Bellini", "Gerald R. Whitmore"
	trades, comp, instr := "omnibus equity trades analysed", "undisclosed compensation paid for Bellini", "instruction to clean up the shared drive"
	return map[string]relabel{"F80/4,217": {wca, trades, "", "none"}, "F170/4,312": {wca, trades, "", "none"},
		"F752/$92,600": {bellini, comp, "", "party"}, "F388/$103,800.00": {bellini, comp, "", ""},
		"F96/April 22, 2024": {whitmore, instr, "", "party"}, "F805/15 Apr 2024": {whitmore, instr, "", "none"}, "F934/15 Apr 2024": {whitmore, instr, "", "party"}}
}()

// TestJoinDiscrepanciesFidsDoNotScopeTheJoin (bench5: per-issue fids split each pair across issues):
// naming one side's findings still finds the conflict with the other side's, and only conflicts
// holding a named finding are reported.
func TestJoinDiscrepanciesFidsDoNotScopeTheJoin(t *testing.T) {
	t.Parallel()
	fs := readAs(bench5Findings(t), bench5Labels)
	rt := &agent.Runtime{Reg: agent.NewRegistry()}
	for _, tool := range Tools(judgingSource{memSource(fs), scripted{yes: [][2]string{{"4,217", "4,312"}}}}) {
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

// judgingSource is a memSource whose join a scripted judge decides.
type judgingSource struct {
	memSource
	scripted
}

func (m memSource) Findings(context.Context, string) ([]Finding, error) { return m, nil }
