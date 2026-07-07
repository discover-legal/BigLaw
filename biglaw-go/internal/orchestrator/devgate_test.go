// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Deviation-pass gating (fix 6). routeMatter flips compliance/enforcement near 50/50
// on identical submissions (57-45 one run, 52-59 the next), silently adding/removing
// the deviation pass that swings 3-5 rubric criteria. The gate is now non-exclusive:
// both borderline routings run the deviation pass, while a decisively-enforcement
// matter still skips it.

package orchestrator

import "testing"

func TestDeviationGate_BothBorderlineRoutingsOpen(t *testing.T) {
	// The two observed claim-count fixtures for the SAME submission across runs.
	fixtures := []struct {
		name      string
		enf, comp int
	}{
		{"run A — enforcement leads by 12", 57, 45},
		{"run B — compliance leads", 52, 59},
	}
	for _, f := range fixtures {
		if !deviationGateOpen(f.enf, f.comp) {
			t.Errorf("%s: deviation gate closed for a borderline routing (enf=%d comp=%d); it must run in both", f.name, f.enf, f.comp)
		}
	}
}

func TestDeviationGate_DecisiveEnforcementSkips(t *testing.T) {
	// A matter whose accusation predicates dominate decisively keeps pure enforcement
	// behavior: the deviation pass does not run.
	if deviationGateOpen(90, 5) {
		t.Error("deviation gate should be closed for a decisively-enforcement matter (enf=90 comp=5)")
	}
}

func TestDeviationGate_ComplianceAndTiesOpen(t *testing.T) {
	if !deviationGateOpen(0, 0) {
		t.Error("empty graph should default to compliance framing (gate open)")
	}
	if !deviationGateOpen(30, 30) {
		t.Error("a tie should open the gate (compliance-leaning default)")
	}
	if !deviationGateOpen(3, 40) {
		t.Error("compliance-dominant should open the gate")
	}
}

// shouldRunDeviationPass on a nil graph defaults to compliance framing → open.
func TestShouldRunDeviationPass_NilGraph(t *testing.T) {
	o := &Orchestrator{}
	if !o.shouldRunDeviationPass(nil) {
		t.Error("nil graph should default to running the deviation pass")
	}
}
