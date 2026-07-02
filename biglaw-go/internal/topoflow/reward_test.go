// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M7 — Reward & judge.
package topoflow

import (
	"math"
	"testing"
)

// humanEvalRunner is a fake CodeRunner that "passes" iff the candidate contains
// the expected substring — a deterministic stand-in for executing code offline.
type humanEvalRunner struct{ needs string }

func (r humanEvalRunner) Run(candidate string, gt map[string]any) float64 {
	if r.needs != "" && contains(candidate, r.needs) {
		return 1.0
	}
	if frac, ok := gt["fraction"].(float64); ok {
		return frac
	}
	return 0.0
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestGroundTruthCodeFraction(t *testing.T) {
	q := GroundTruthQ{Runner: humanEvalRunner{needs: "return a+b"}}
	ctx := TaskContext{Domain: "code", GroundTruth: map[string]any{}}
	good := &Trace{FinalAnswer: "def add(a,b):\n return a+b"}
	bad := &Trace{FinalAnswer: "def add(a,b):\n return a-b"}
	if qv, sub := q.Score(ctx, good, nil); qv != 1.0 || sub["confidence"] != 1.0 {
		t.Errorf("good code: q=%v sub=%v", qv, sub)
	}
	if qv, _ := q.Score(ctx, bad, nil); qv != 0.0 {
		t.Errorf("bad code q=%v want 0", qv)
	}
}

func TestGroundTruthMathExactMatch(t *testing.T) {
	q := GroundTruthQ{}
	ctx := TaskContext{Domain: "math", GroundTruth: "4"}
	if qv, _ := q.Score(ctx, &Trace{FinalAnswer: "the answer is 4."}, nil); qv != 1.0 {
		t.Errorf("math exact-match q=%v want 1", qv)
	}
	if qv, _ := q.Score(ctx, &Trace{FinalAnswer: "5"}, nil); qv != 0.0 {
		t.Errorf("math wrong q=%v want 0", qv)
	}
}

func judgeResponder(scoresByModel map[string][]map[string]any) func(LLMRequest) map[string]any {
	return func(req LLMRequest) map[string]any {
		if req.Purpose == "judge" {
			rows := scoresByModel[req.Model]
			arr := make([]any, len(rows))
			for i, r := range rows {
				arr[i] = r
			}
			return map[string]any{"scores": arr}
		}
		return map[string]any{"_tokens": 10.0}
	}
}

func axisRow(id int, v float64) map[string]any {
	return map[string]any{"id": float64(id), "goal_achievement": v, "grounding": v, "coordination": v, "recovery": v}
}

func TestRelativeJudgeAxesSingleJudgeConfidence(t *testing.T) {
	cfg := DefaultConfig()
	scores := map[string][]map[string]any{
		"j1": {
			{"id": 0.0, "goal_achievement": 0.9, "grounding": 0.8, "coordination": 0.7, "recovery": 0.6},
			{"id": 1.0, "goal_achievement": 0.3, "grounding": 0.4, "coordination": 0.2, "recovery": 0.1},
		},
	}
	rj := NewRelativeJudge(&MockTransport{Responder: judgeResponder(scores)}, []string{"j1"}, cfg)
	res := rj.ScoreGroup(TaskContext{Prompt: "p"}, []*Trace{{FinalAnswer: "A"}, {FinalAnswer: "B"}})
	if math.Abs(res[0].Q-(0.9+0.8+0.7+0.6)/4) > 1e-9 {
		t.Errorf("q0=%v", res[0].Q)
	}
	if res[0].Sub["confidence"].(float64) != 1.0 {
		t.Errorf("single judge confidence should be 1.0")
	}
	if res[0].Q <= res[1].Q {
		t.Error("A should rank above B")
	}
}

func TestCrossJudgeDisagreementStd(t *testing.T) {
	cfg := DefaultConfig()
	scores := map[string][]map[string]any{
		"j1": {axisRow(0, 1.0)}, "j2": {axisRow(0, 0.0)}, "j3": {axisRow(0, 0.5)},
	}
	rj := NewRelativeJudge(&MockTransport{Responder: judgeResponder(scores)}, []string{"j1", "j2", "j3"}, cfg)
	res := rj.ScoreGroup(TaskContext{Prompt: "p"}, []*Trace{{FinalAnswer: "A"}})
	sub := res[0].Sub
	if sub["n_judges"].(int) != 3 {
		t.Error("n_judges should be 3")
	}
	if sub["disagreement_std"].(float64) <= 0.3 {
		t.Errorf("expected large disagreement std, got %v", sub["disagreement_std"])
	}
	if sub["confidence"].(float64) >= 0.7 {
		t.Errorf("expected low confidence, got %v", sub["confidence"])
	}
	if math.Abs(res[0].Q-0.5) > 1e-9 {
		t.Errorf("q=%v want 0.5", res[0].Q)
	}
}

func TestSingleVsThreeJudgeReportedSeparately(t *testing.T) {
	cfg := DefaultConfig()
	scores := map[string][]map[string]any{
		"claude-haiku-4-5": {axisRow(0, 0.8)},
		"gpt-5-mini":       {axisRow(0, 0.6)},
		"qwen-flash":       {axisRow(0, 0.4)},
	}
	tx := &MockTransport{Responder: judgeResponder(scores)}
	ctx := TaskContext{Prompt: "p"}
	group := []*Trace{{FinalAnswer: "A"}}
	live := NewRelativeJudge(tx, []string{cfg.LiveJudge}, cfg).ScoreGroup(ctx, group)[0]
	audit := AuditRescore(ctx, group, tx, cfg)[0]
	if math.Abs(live.Q-0.8) > 1e-9 || math.Abs(audit.Q-0.6) > 1e-9 {
		t.Errorf("live=%v audit=%v want 0.8 / 0.6", live.Q, audit.Q)
	}
}

func TestConfidenceWeightingScalesBackup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TopoModes = []string{"linear"}
	cfg.QualityStrategy = "judged"
	g := NewPolicyGraph(cfg)
	scores := map[string][]map[string]any{"j1": {axisRow(0, 1.0)}, "j2": {axisRow(0, 0.0)}}
	tx := &MockTransport{Responder: func(req LLMRequest) map[string]any {
		if req.Purpose == "judge" {
			return judgeResponder(scores)(req)
		}
		if req.Role == "evaluator" {
			return map[string]any{"complete": true, "_tokens": 10.0}
		}
		return map[string]any{"_tokens": 10.0}
	}}
	rj := NewRelativeJudge(tx, []string{"j1", "j2"}, cfg)
	pickEval := func(sig Signature, legal []Action) Action {
		for _, a := range legal {
			if a.Kind == "invoke" && a.Skill == "evaluator" {
				return a
			}
		}
		return legal[0]
	}
	ctx := TaskContext{TaskID: "cw", Prompt: "p", Domain: "advisory"}
	if _, err := RunTask(ctx, cfg, g, RunOptions{Transport: tx, Quality: JudgedQ{Judge: rj}, SelectFn: pickEval}); err != nil {
		t.Fatal(err)
	}
	if len(g.edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.edges))
	}
	for _, e := range g.edges {
		// disagreeing judges (1.0 vs 0.0) -> std 0.5 -> confidence 0.5 -> visit 0.5
		if math.Abs(e.Visits-0.5) > 1e-9 {
			t.Errorf("fractional visit=%v want 0.5", e.Visits)
		}
	}
}
