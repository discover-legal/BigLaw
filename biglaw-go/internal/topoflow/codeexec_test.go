// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M7-real — the SubprocessCodeRunner executes real Python against real HumanEval
// problems (no fakes). Skipped only if python3 is unavailable.
package topoflow

import (
	"os/exec"
	"testing"
)

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func TestRealCodeRunnerHumanEval(t *testing.T) {
	requirePython(t)
	runner := NewSubprocessCodeRunner()
	q := GroundTruthQ{Runner: runner}
	probs := RealHumanEvalSample()

	// HumanEval/0 — correct vs wrong implementation, executed for real.
	he0 := probs[0]
	correct0 := he0.Prompt + "\n" +
		"    for i in range(len(numbers)):\n" +
		"        for j in range(i+1, len(numbers)):\n" +
		"            if abs(numbers[i]-numbers[j]) < threshold:\n" +
		"                return True\n" +
		"    return False\n"
	if v, _ := q.Score(he0, &Trace{FinalAnswer: correct0}, nil); v != 1.0 {
		t.Errorf("correct has_close_elements should pass all tests, got %v", v)
	}
	wrong0 := he0.Prompt + "\n    return False\n" // never detects closeness
	if v, _ := q.Score(he0, &Trace{FinalAnswer: wrong0}, nil); v != 0.0 {
		t.Errorf("wrong has_close_elements should fail, got %v", v)
	}

	// HumanEval/53 add — real execution incl. the 100 randomized assertions.
	he53 := probs[1]
	if v, _ := q.Score(he53, &Trace{FinalAnswer: "def add(x, y):\n    return x + y\n"}, nil); v != 1.0 {
		t.Errorf("correct add should pass, got %v", v)
	}
	if v, _ := q.Score(he53, &Trace{FinalAnswer: "def add(x, y):\n    return x - y\n"}, nil); v != 0.0 {
		t.Errorf("wrong add should fail, got %v", v)
	}
}

func TestRealCodeRunnerAppsFraction(t *testing.T) {
	requirePython(t)
	runner := NewSubprocessCodeRunner()
	// APPS-style: a list of independent assert snippets; 2 of 3 hold.
	gt := map[string]any{"tests": []any{"assert f(1)==1", "assert f(2)==4", "assert f(3)==9"}}
	ctx := TaskContext{Domain: "code", GroundTruth: gt}
	q := GroundTruthQ{Runner: runner}
	cand := "def f(x):\n    return x*x if x < 3 else 0\n"
	if v, _ := q.Score(ctx, &Trace{FinalAnswer: cand}, nil); v < 0.66 || v > 0.67 {
		t.Errorf("expected ~2/3, got %v", v)
	}
}

func TestRealCodeRunnerTimeoutAndCrash(t *testing.T) {
	requirePython(t)
	runner := &SubprocessCodeRunner{Python: "python3", TimeoutSec: 2}
	gt := map[string]any{"entry_point": "f", "test": "def check(candidate):\n    assert candidate()==1\n"}
	ctx := TaskContext{Domain: "code", GroundTruth: gt}
	q := GroundTruthQ{Runner: runner}
	// infinite loop -> timeout -> non-pass (no panic, no hang)
	if v, _ := q.Score(ctx, &Trace{FinalAnswer: "def f():\n    while True:\n        pass\n"}, nil); v != 0.0 {
		t.Errorf("infinite loop should not pass, got %v", v)
	}
	// syntax error -> non-pass
	if v, _ := q.Score(ctx, &Trace{FinalAnswer: "def f(:\n"}, nil); v != 0.0 {
		t.Errorf("syntax error should not pass, got %v", v)
	}
}

func TestRealCodeRunnerEndToEndMacroLoop(t *testing.T) {
	requirePython(t)
	cfg := DefaultConfig()
	cfg.TopoModes = []string{"linear"}
	g := NewPolicyGraph(cfg)
	he53 := RealHumanEvalSample()[1]
	// Deterministic transport returns the real correct solution; the CodeRunner
	// then EXECUTES it for real to score the trajectory.
	tx := &MockTransport{Responder: func(req LLMRequest) map[string]any {
		role := req.Role
		if role == "evaluator" {
			return map[string]any{"complete": true, "_tokens": 10.0}
		}
		if len(role) >= 6 && role[:6] == "solver" {
			return map[string]any{"draft_answer": "def add(x, y):\n    return x + y\n", "_tokens": 50.0}
		}
		return map[string]any{"_tokens": 10.0}
	}}
	traj, err := RunTask(he53, cfg, g, RunOptions{
		Transport: tx, Quality: GroundTruthQ{Runner: NewSubprocessCodeRunner()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if traj.Quality != 1.0 {
		t.Errorf("end-to-end real execution should yield Q=1.0, got %v", traj.Quality)
	}
}
