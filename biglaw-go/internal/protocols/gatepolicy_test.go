// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package protocols

import (
	"fmt"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func policyRunner(policy string, threshold float64, budget, sampleK int) *Runner {
	cfg := &config.Config{}
	cfg.Debate.GateConfidenceThreshold = threshold
	cfg.Gate.Policy = policy
	cfg.Gate.MaxPerTask = budget
	cfg.Gate.RankedSampleK = sampleK
	return New(cfg, nil, nil)
}

func countPending(gates []types.GateRequest) (pending, deferred int) {
	for _, g := range gates {
		switch {
		case g.Status == "pending":
			pending++
		case g.Status == "auto_deferred" && g.AutoDeferred:
			deferred++
		}
	}
	return
}

// The incident case: a model emits uniform confidence on 200 findings, all
// below the threshold. Calibrated policy must gate at most the budget, must
// include every challenged finding first, and must record the overflow as
// auto-deferred rather than demanding 200 clicks.
func TestSelectGates_Uniform200GatesWithinBudget(t *testing.T) {
	r := policyRunner("calibrated", 0.85, 25, 10)
	findings := make([]types.Finding, 200)
	challengedIDs := map[string]bool{}
	for i := range findings {
		findings[i] = types.Finding{ID: fmt.Sprintf("f%d", i), Confidence: 0.8}
		if i%50 == 0 { // f0, f50, f100, f150 challenged & unresolved
			findings[i].Challenged = true
			challengedIDs[findings[i].ID] = true
		}
	}
	gates := r.SelectGates("t1", findings, 0)
	pending, deferred := countPending(gates)
	if pending > 25 {
		t.Fatalf("pending=%d exceeds budget 25", pending)
	}
	if pending == 0 {
		t.Fatalf("expected some pending gates")
	}
	// Distribution is degenerate → ranked sampling caps at K=10.
	if pending != 10 {
		t.Errorf("degenerate round should gate RankedSampleK=10, got %d", pending)
	}
	if deferred != 200-pending {
		t.Errorf("deferred=%d, want %d", deferred, 200-pending)
	}
	// Every challenged finding must be inside the pending sample.
	got := map[string]bool{}
	for _, g := range gates {
		if g.Status == "pending" {
			got[g.FindingID] = true
		}
	}
	for id := range challengedIDs {
		if !got[id] {
			t.Errorf("challenged finding %s was not gated", id)
		}
	}
	// Auto-deferred records must carry a reason and never block review flows.
	for _, g := range gates {
		if g.AutoDeferred && g.DeferReason == "" {
			t.Errorf("auto-deferred gate %s has no DeferReason", g.ID)
		}
	}
}

// A varied (healthy) distribution under budget behaves exactly like today:
// same findings gated as the strict identifier, all pending.
func TestSelectGates_VariedDistributionMatchesStrict(t *testing.T) {
	r := policyRunner("calibrated", 0.80, 25, 10)
	findings := []types.Finding{
		{ID: "a", Confidence: 0.95},
		{ID: "b", Confidence: 0.55},
		{ID: "c", Confidence: 0.90},
		{ID: "d", Confidence: 0.30},
		{ID: "e", Confidence: 0.85, Challenged: true},
		{ID: "f", Confidence: 0.70},
		{ID: "g", Confidence: 0.99},
		{ID: "h", Confidence: 0.10},
	}
	strict := r.IdentifyGates("t1", findings)
	calibrated := r.SelectGates("t1", findings, 0)
	pending, deferred := countPending(calibrated)
	if deferred != 0 {
		t.Fatalf("varied distribution under budget should defer nothing, deferred=%d", deferred)
	}
	if pending != len(strict) {
		t.Fatalf("pending=%d, strict would gate %d", pending, len(strict))
	}
	want := map[string]bool{}
	for _, g := range strict {
		want[g.FindingID] = true
	}
	for _, g := range calibrated {
		if !want[g.FindingID] {
			t.Errorf("calibrated gated %s which strict would not", g.FindingID)
		}
	}
}

// GATE_POLICY=strict preserves today's behaviour exactly: every finding under
// the threshold gates as pending, no budget, no deferral — even on the
// degenerate 200×0.8 distribution.
func TestSelectGates_StrictModeUnchanged(t *testing.T) {
	r := policyRunner("strict", 0.85, 25, 10)
	findings := make([]types.Finding, 200)
	for i := range findings {
		findings[i] = types.Finding{ID: fmt.Sprintf("f%d", i), Confidence: 0.8}
	}
	gates := r.SelectGates("t1", findings, 0)
	if len(gates) != 200 {
		t.Fatalf("strict mode gated %d, want 200", len(gates))
	}
	for _, g := range gates {
		if g.Status != "pending" || g.AutoDeferred {
			t.Fatalf("strict mode produced non-pending gate: %+v", g)
		}
	}
}

// Budget 0: nothing demands a click; every gate-worthy finding is recorded
// auto-deferred.
func TestSelectGates_BudgetZero(t *testing.T) {
	r := policyRunner("calibrated", 0.80, 0, 10)
	findings := []types.Finding{
		{ID: "a", Confidence: 0.2, Challenged: true},
		{ID: "b", Confidence: 0.5},
	}
	gates := r.SelectGates("t1", findings, 0)
	pending, deferred := countPending(gates)
	if pending != 0 {
		t.Fatalf("budget 0 must gate nothing, pending=%d", pending)
	}
	if deferred != 2 {
		t.Fatalf("deferred=%d, want 2", deferred)
	}
}

// Exhausted budget mid-task: alreadyGated at/over the cap defers everything
// new; a partially spent budget admits only the remainder, challenged first.
func TestSelectGates_BudgetAccountsPriorGates(t *testing.T) {
	r := policyRunner("calibrated", 0.80, 5, 10)
	findings := []types.Finding{
		{ID: "a", Confidence: 0.5},
		{ID: "b", Confidence: 0.4, Challenged: true},
		{ID: "c", Confidence: 0.3},
		{ID: "d", Confidence: 0.79},
		{ID: "e", Confidence: 0.6},
	}
	gates := r.SelectGates("t1", findings, 4) // one slot left
	pending, deferred := countPending(gates)
	if pending != 1 || deferred != 4 {
		t.Fatalf("pending=%d deferred=%d, want 1/4", pending, deferred)
	}
	for _, g := range gates {
		if g.Status == "pending" && g.FindingID != "b" {
			t.Errorf("remaining slot should go to the challenged finding, got %s", g.FindingID)
		}
	}
	gates = r.SelectGates("t1", findings, 9) // over budget already
	pending, deferred = countPending(gates)
	if pending != 0 || deferred != 5 {
		t.Fatalf("over-budget: pending=%d deferred=%d, want 0/5", pending, deferred)
	}
}

// Degenerate detection: uniform and >80%-share distributions trigger; a
// spread distribution and tiny samples do not.
func TestDegenerateConfidence(t *testing.T) {
	uniform := make([]types.Finding, 20)
	for i := range uniform {
		uniform[i].Confidence = 0.8
	}
	if !degenerateConfidence(uniform) {
		t.Error("uniform 0.8 x20 should be degenerate")
	}
	share := make([]types.Finding, 20)
	for i := range share {
		share[i].Confidence = 0.8
	}
	share[0].Confidence = 0.2
	share[1].Confidence = 0.5
	share[2].Confidence = 0.9 // 17/20 = 85% share one value
	if !degenerateConfidence(share) {
		t.Error(">80% single-value share should be degenerate")
	}
	varied := []types.Finding{
		{Confidence: 0.1}, {Confidence: 0.3}, {Confidence: 0.5},
		{Confidence: 0.6}, {Confidence: 0.8}, {Confidence: 0.9},
		{Confidence: 0.95}, {Confidence: 0.4},
	}
	if degenerateConfidence(varied) {
		t.Error("spread distribution should not be degenerate")
	}
	tiny := []types.Finding{{Confidence: 0.8}, {Confidence: 0.8}}
	if degenerateConfidence(tiny) {
		t.Error("samples below the minimum should never be degenerate")
	}
}
