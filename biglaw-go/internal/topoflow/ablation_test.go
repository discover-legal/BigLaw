// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// No-skip ablation (§6.2): SkipEnabled=false removes skip:X from A(s), without
// disturbing any other legal action, so topology compression can be isolated.
package topoflow

import "testing"

func countSkips(A []Action) int {
	n := 0
	for _, a := range A {
		if a.Kind == "skip" {
			n++
		}
	}
	return n
}

func TestSkipEnabledGatesSkipActions(t *testing.T) {
	cfg := DefaultConfig()
	plan := NewPlan(cfg.OptionalAux)

	on := legalActions(plan, cfg)
	if countSkips(on) == 0 {
		t.Fatal("SkipEnabled=true should expose at least one skip:X action")
	}

	cfg.SkipEnabled = false
	off := legalActions(plan, cfg)
	if countSkips(off) != 0 {
		t.Fatalf("SkipEnabled=false (no-skip ablation) must expose no skip:X, got %d", countSkips(off))
	}

	// Disabling skip must not strip any non-skip action or break finishability.
	if len(off) != len(on)-countSkips(on) {
		t.Errorf("no-skip arm should drop exactly the skip actions: on=%d off=%d skips=%d",
			len(on), len(off), countSkips(on))
	}
	if pruneToKeepFinishable(off, cfg) == nil {
		t.Error("no-skip action set must still be finishable")
	}
}
