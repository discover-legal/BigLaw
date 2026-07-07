// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M1 — Types & signature: Fold is deterministic and usable as a map key.
package topoflow

import (
	"math/rand"
	"testing"
)

func ctxCode() TaskContext { return TaskContext{TaskID: "t1", Prompt: "p", Domain: "code"} }

func TestBucketClampsAndBins(t *testing.T) {
	cases := []struct {
		x    float64
		bins int
		want int
	}{
		{0.0, 4, 0}, {1.0, 4, 3}, {0.24, 4, 0}, {0.25, 4, 1}, {0.5, 4, 2},
		{0.4, 2, 0}, {0.6, 2, 1},
	}
	for _, c := range cases {
		if got := bucket(c.x, c.bins); got != c.want {
			t.Errorf("bucket(%v,%d)=%d want %d", c.x, c.bins, got, c.want)
		}
	}
}

func TestFoldDeterministicAndMapKey(t *testing.T) {
	cfg := DefaultConfig()
	h := NewHandoffState()
	h.Set("goal", "g")
	h.Set("draft_answer", "d")
	b := BeliefVector{Correctness: 0.7, Uncertainty: 0.2, Evidence: 0.6}
	s1 := Fold(ctxCode(), h, b, cfg)
	s2 := Fold(ctxCode(), h, b, cfg)
	if s1 != s2 {
		t.Fatal("Fold not deterministic")
	}
	m := map[Signature]int{s1: 1}
	if m[s2] != 1 {
		t.Fatal("Signature not usable as a map key")
	}
}

func TestEqualObservationsEqualSignature(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 200; i++ {
		b := BeliefVector{
			Correctness:   rng.Float64(),
			Uncertainty:   rng.Float64(),
			Contradiction: rng.Float64(),
			Evidence:      rng.Float64(),
		}
		h1, h2 := NewHandoffState(), NewHandoffState()
		if rng.Float64() > 0.5 {
			h1.Set("goal", "x")
			h2.Set("goal", "x")
		}
		if Fold(ctxCode(), h1, b, cfg) != Fold(ctxCode(), h2, b, cfg) {
			t.Fatal("equal observations -> unequal signatures")
		}
	}
}

func TestBeliefBinsChangesBucketCount(t *testing.T) {
	h := NewHandoffState()
	b := BeliefVector{Correctness: 0.55, Uncertainty: 0.55, Contradiction: 0.55, Evidence: 0.55}
	coarse := Fold(ctxCode(), h, b, withBins(2))
	fine := Fold(ctxCode(), h, b, withBins(10))
	if coarse.CorrectnessB != 1 {
		t.Errorf("coarse bucket=%d want 1", coarse.CorrectnessB)
	}
	if fine.CorrectnessB != 5 {
		t.Errorf("fine bucket=%d want 5", fine.CorrectnessB)
	}
}

func TestHandoffMaskOrder(t *testing.T) {
	h := NewHandoffState()
	h.Set("goal", "g")
	h.Set("merged_answer", "m")
	got := h.Mask()
	want := [7]int{1, 0, 0, 0, 0, 0, 1}
	if got != want {
		t.Errorf("mask=%v want %v", got, want)
	}
}

func TestRegimeContradictionDominates(t *testing.T) {
	s := Fold(ctxCode(), NewHandoffState(), BeliefVector{Contradiction: 0.8}, DefaultConfig())
	if s.Regime != RegimeContradictory {
		t.Errorf("regime=%v want contradictory", s.Regime)
	}
}

func withBins(n int) Config {
	c := DefaultConfig()
	c.BeliefBins = n
	return c
}
