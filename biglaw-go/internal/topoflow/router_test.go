// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M3 — Linear generator + macro loop (recovers AgensFlow).
package topoflow

import "testing"

func linearCfg() Config {
	c := DefaultConfig()
	c.TopoModes = []string{"linear"}
	c.TokenCap = 4000
	return c
}

func mathTask() TaskContext {
	return TaskContext{TaskID: "m3", Prompt: "2+2?", Domain: "math", GroundTruth: "4"}
}

func TestEndToEndEmitsReportAndTerminates(t *testing.T) {
	cfg := linearCfg()
	g := NewPolicyGraph(cfg)
	tx := NewMockTransport()
	tx.Answer = "4"
	traj, err := RunTask(mathTask(), cfg, g, RunOptions{Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if traj.Report == nil {
		t.Fatal("expected a RunReport")
	}
	for _, d := range traj.Report.DecisionPath {
		if act, ok := d["action"].(Action); ok && act.Kind == "terminate" {
			t.Fatal("terminate must never be selected directly")
		}
	}
}

func TestTerminatesViaBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TopoModes = []string{"linear"}
	cfg.TokenCap = 600
	cfg.MaxMacroSteps = 1000
	g := NewPolicyGraph(cfg)
	tx := NewMockTransport()
	tx.Answer = "4"
	tx.EvaluatorCompletes = false
	tx.TokensPerCall = 100
	traj, err := RunTask(mathTask(), cfg, g, RunOptions{Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	if traj.Trace.Tokens < 600 && len(traj.Trace.DecisionPath()) > cfg.MaxMacroSteps {
		t.Fatal("should terminate on budget / max steps")
	}
}

func TestGroundTruthRewardsCorrectAnswer(t *testing.T) {
	cfg := linearCfg()
	good := mustRun(t, cfg, NewPolicyGraph(cfg), txWith("4"))
	bad := mustRun(t, cfg, NewPolicyGraph(cfg), txWith("5"))
	if good.Quality != 1.0 {
		t.Errorf("good quality=%v want 1.0", good.Quality)
	}
	if bad.Quality != 0.0 {
		t.Errorf("bad quality=%v want 0.0", bad.Quality)
	}
	if good.Reward <= bad.Reward {
		t.Error("correct answer should earn higher reward")
	}
}

func TestTrainingProducesNonuniformPreferences(t *testing.T) {
	cfg := linearCfg()
	g := NewPolicyGraph(cfg)
	tx := txWith("4")
	for i := 0; i < 60; i++ {
		if _, err := RunTask(mathTask(), cfg, g, RunOptions{Transport: tx}); err != nil {
			t.Fatal(err)
		}
	}
	summ := g.Summary()
	if len(summ) == 0 {
		t.Fatal("policy graph should have learned edges")
	}
	nonuniform := false
	for _, actions := range summ {
		var means []float64
		for _, a := range actions {
			if a["visits"].(float64) >= 1 {
				means = append(means, a["meanReward"].(float64))
			}
		}
		if len(means) >= 2 {
			mn, mx := means[0], means[0]
			for _, v := range means {
				if v < mn {
					mn = v
				}
				if v > mx {
					mx = v
				}
			}
			if mx-mn > 0.05 {
				nonuniform = true
			}
		}
	}
	if !nonuniform {
		t.Error("expected non-uniform action preferences for some signature")
	}
}

func txWith(answer string) *MockTransport {
	tx := NewMockTransport()
	tx.Answer = answer
	return tx
}

func mustRun(t *testing.T, cfg Config, g *PolicyGraph, tx Transport) *Trajectory {
	t.Helper()
	traj, err := RunTask(mathTask(), cfg, g, RunOptions{Transport: tx})
	if err != nil {
		t.Fatal(err)
	}
	return traj
}
