// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// M6 — Composite integration: a dytopo cell is ONE decision/ONE reward but
// charges ALL inner tokens; beliefs update once; signature recomputed after.
package topoflow

import "testing"

type constRunner struct{ v float64 }

func (c constRunner) Run(string, map[string]any) float64 { return c.v }

func plannedSelector(plan []Action) func(Signature, []Action) Action {
	i := 0
	return func(sig Signature, legal []Action) Action {
		if i < len(plan) {
			want := plan[i]
			i++
			for _, a := range legal {
				if a == want {
					return a
				}
			}
		}
		for _, a := range legal {
			if a.Kind == "invoke" && a.Skill == "evaluator" {
				return a
			}
		}
		return legal[0]
	}
}

func m6Responder() func(LLMRequest) map[string]any {
	q := map[string]string{"Developer": "alpha", "Researcher": "gamma", "Tester": "beta", "Designer": "delta"}
	k := map[string]string{"Developer": "beta", "Researcher": "gamma", "Tester": "alpha", "Designer": "delta"}
	return func(req LLMRequest) map[string]any {
		if req.Purpose == "agent" {
			r := req.Role
			out := map[string]any{"public": r + "-pub", "private": r + "-priv",
				"q_desc": q[r], "k_desc": k[r], "_tokens": 50.0}
			if r == "Developer" {
				out["draft_answer"] = "42"
				out["complete"] = true
			}
			return out
		}
		if req.Role == "solver_cot" || req.Role == "solver_concise" || req.Role == "solver_evidence" {
			return map[string]any{"draft_answer": "42", "_tokens": 100.0}
		}
		return map[string]any{"_tokens": 100.0}
	}
}

func TestDytopoOneEntryOneRewardAllTokens(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TokenCap = 100000
	g := NewPolicyGraph(cfg)
	ctx := TaskContext{TaskID: "m6", Prompt: "solve", Domain: "code", GroundTruth: map[string]any{}}
	tx := &MockTransport{Responder: m6Responder()}

	plan := []Action{
		InvokeAction("solver_cot", "haiku"),
		SkipAction("planner"),
		DytopoAction(1, cfg.DytopoKIn, 0),
	}
	traj, err := RunTask(ctx, cfg, g, RunOptions{
		Transport: tx, Embedder: NewMockEmbedder(),
		Quality: GroundTruthQ{Runner: constRunner{1.0}}, SelectFn: plannedSelector(plan),
	})
	if err != nil {
		t.Fatal(err)
	}

	dp := traj.Trace.DecisionPath()
	if len(dp) != 3 || dp[0].Act.Kind != "invoke" || dp[1].Act.Kind != "skip" || dp[2].Act.Kind != "topology" {
		t.Fatalf("decision path = %+v", dp)
	}
	for _, d := range dp {
		e := g.edges[edgeKey(d)]
		if e == nil || e.Visits != 1.0 {
			t.Errorf("edge %v visits=%v want 1", d.Act, e)
		}
	}
	// dytopo charged all inner-round tokens: 4 agents * 50 * 1 round
	var topo map[string]any
	for _, ev := range traj.Trace.Events {
		if ev["type"] == "topology" {
			topo = ev
		}
	}
	if topo["rounds_run"].(int) != 1 || topo["total_tokens"].(int) != 4*50 {
		t.Errorf("topology tokens wrong: %v", topo)
	}
	if traj.Trace.Tokens != 100+200 {
		t.Errorf("trajectory tokens=%d want 300", traj.Trace.Tokens)
	}
	if traj.Quality != 1.0 {
		t.Errorf("quality=%v want 1.0", traj.Quality)
	}
}

func TestSignatureRecomputedAfterDytopo(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TokenCap = 100000
	g := NewPolicyGraph(cfg)
	ctx := TaskContext{TaskID: "m6b", Prompt: "solve", Domain: "code"}

	responder := func(req LLMRequest) map[string]any {
		q := map[string]string{"Developer": "alpha", "Researcher": "gamma", "Tester": "beta", "Designer": "delta"}
		k := map[string]string{"Developer": "beta", "Researcher": "gamma", "Tester": "alpha", "Designer": "delta"}
		if req.Purpose == "agent" {
			r := req.Role
			out := map[string]any{"public": r, "private": r, "q_desc": q[r], "k_desc": k[r], "_tokens": 10.0}
			if r == "Developer" {
				out["draft_answer"] = "42"
			}
			return out
		}
		if req.Role == "evaluator" {
			return map[string]any{"complete": true, "_tokens": 10.0}
		}
		return map[string]any{"_tokens": 10.0}
	}
	plan := []Action{DytopoAction(1, cfg.DytopoKIn, 0), InvokeAction("evaluator", "haiku")}
	traj, err := RunTask(ctx, cfg, g, RunOptions{
		Transport: &MockTransport{Responder: responder}, Embedder: NewMockEmbedder(),
		SelectFn: plannedSelector(plan),
	})
	if err != nil {
		t.Fatal(err)
	}
	dp := traj.Trace.DecisionPath()
	if len(dp) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(dp))
	}
	if dp[0].Sig == dp[1].Sig {
		t.Error("signature should be recomputed (differ) after the dytopo cell")
	}
	if dp[1].Sig.Mask[5] != 1 { // draft_answer index in HandoffFields
		t.Errorf("post-dytopo signature should show draft_answer set: %v", dp[1].Sig.Mask)
	}
}
