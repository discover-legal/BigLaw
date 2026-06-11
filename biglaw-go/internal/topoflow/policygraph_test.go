// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// M2 — Policy graph: UCB1, annealed exploration, Welford, persistence, bandit.
package topoflow

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"
)

func sigT(tag int) Signature {
	return Signature{Regime: RegimeStraightforward, CorrectnessB: tag}
}

func TestUnvisitedReturnsInf(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	if !math.IsInf(g.Score(sigT(0), InvokeAction("x", "haiku")), 1) {
		t.Fatal("unvisited action should score +Inf")
	}
}

func TestCSAnnealEq5(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	check := func(ns int, want float64) {
		if math.Abs(g.cs(ns)-want) > 1e-9 {
			t.Errorf("cs(%d)=%v want %v", ns, g.cs(ns), want)
		}
	}
	check(0, 1.4)
	check(50, 0.7)
	check(75, 0.5) // 1.4*2^-1.5=0.494 -> floored to 0.5
}

func TestWelfordMatchesReference(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	sig, a := sigT(0), InvokeAction("s", "haiku")
	rewards := []float64{0.2, 0.8, 0.5, 0.9, 0.1, 0.4}
	for _, r := range rewards {
		g.Backup(sig, a, r, 10, false, 1.0)
	}
	e := g.edges[edgeKey{sig, a}]
	var sum float64
	for _, x := range rewards {
		sum += x
	}
	mean := sum / float64(len(rewards))
	var v float64
	for _, x := range rewards {
		v += (x - mean) * (x - mean)
	}
	v /= float64(len(rewards))
	if math.Abs(e.MeanReward-mean) > 1e-9 {
		t.Errorf("mean=%v want %v", e.MeanReward, mean)
	}
	if math.Abs(e.Variance()-v) > 1e-9 {
		t.Errorf("variance=%v want %v", e.Variance(), v)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	sig := sigT(2)
	g.Backup(sig, InvokeAction("s", "haiku"), 0.3, 5, false, 1.0)
	g.Backup(sig, InvokeAction("s", "haiku"), 0.7, 5, false, 1.0)
	g.Backup(sig, InvokeAction("t", "haiku"), 0.1, 5, true, 1.0)
	p := filepath.Join(t.TempDir(), "g.json")
	if err := g.Save(p); err != nil {
		t.Fatal(err)
	}
	g2, err := LoadPolicyGraph(p, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	e1 := g.edges[edgeKey{sig, InvokeAction("s", "haiku")}]
	e2 := g2.edges[edgeKey{sig, InvokeAction("s", "haiku")}]
	if math.Abs(e1.MeanReward-e2.MeanReward) > 1e-12 {
		t.Errorf("mean mismatch after load")
	}
	if g2.sigVisits[sig] != g.sigVisits[sig] {
		t.Errorf("sigVisits mismatch after load")
	}
}

func TestTwoArmBanditConverges(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	sig := sigT(0)
	good, bad := InvokeAction("good", "haiku"), InvokeAction("bad", "haiku")
	rng := rand.New(rand.NewSource(0))
	for i := 0; i < 400; i++ {
		a := g.Select(sig, []Action{good, bad})
		r := 0.2
		if a == good {
			r = 0.8
		}
		r += rng.Float64()*0.1 - 0.05
		g.Backup(sig, a, r, 1, false, 1.0)
	}
	eg := g.edges[edgeKey{sig, good}]
	eb := g.edges[edgeKey{sig, bad}]
	if eg.Visits < eb.Visits*3 {
		t.Errorf("good arm under-pulled: good=%v bad=%v", eg.Visits, eb.Visits)
	}
	if g.Select(sig, []Action{good, bad}) != good {
		t.Error("should select the higher-mean arm")
	}
}

func TestFailurePenaltyDemotes(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	sig := sigT(0)
	hiFail, loOK := InvokeAction("hi", "haiku"), InvokeAction("lo", "haiku")
	for i := 0; i < 50; i++ {
		g.Backup(sig, hiFail, 0.9, 1, true, 1.0)
		g.Backup(sig, loOK, 0.6, 1, false, 1.0)
	}
	if g.Score(sig, loOK) <= g.Score(sig, hiFail) {
		t.Error("failure penalty should demote the high-reward-but-failing arm")
	}
}

func TestConfidenceWeightFractionalVisit(t *testing.T) {
	g := NewPolicyGraph(DefaultConfig())
	sig, a := sigT(0), InvokeAction("s", "haiku")
	g.Backup(sig, a, 1.0, 1, false, 0.25)
	e := g.edges[edgeKey{sig, a}]
	if math.Abs(e.Visits-0.25) > 1e-9 || math.Abs(e.MeanReward-1.0) > 1e-9 {
		t.Errorf("fractional backup wrong: visits=%v mean=%v", e.Visits, e.MeanReward)
	}
}
