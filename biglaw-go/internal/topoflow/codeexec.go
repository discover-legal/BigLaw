// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"context"
	"os/exec"
	"time"
)

// SubprocessCodeRunner executes candidate code against ground-truth tests in a
// real interpreter subprocess (HumanEval `check(entry_point)` harness, or an
// APPS-style list of assert snippets). This is the standard HumanEval evaluation
// approach.
//
// SECURITY: it runs model-generated code. A per-call timeout is enforced, but
// there is NO namespace/seccomp isolation here — a production deployment MUST run
// this inside a container/sandbox. Tests pass trusted fixtures only.
type SubprocessCodeRunner struct {
	Python     string
	TimeoutSec int
}

// NewSubprocessCodeRunner returns a runner using python3 with a 10s timeout.
func NewSubprocessCodeRunner() *SubprocessCodeRunner {
	return &SubprocessCodeRunner{Python: "python3", TimeoutSec: 10}
}

// Run returns the fraction of tests passed.
func (r *SubprocessCodeRunner) Run(candidate string, gt map[string]any) float64 {
	if candidate == "" {
		return 0
	}
	// HumanEval-style: a `check(candidate)` harness + entry_point.
	if test, ok := gt["test"].(string); ok && test != "" {
		entry, _ := gt["entry_point"].(string)
		script := candidate + "\n" + test + "\n"
		if entry != "" {
			script += "check(" + entry + ")\n"
		}
		if r.exec(script) == nil {
			return 1.0
		}
		return 0.0
	}
	// APPS-style: a list of independent assert snippets.
	if tests, ok := toStringList(gt["tests"]); ok && len(tests) > 0 {
		passed := 0
		for _, t := range tests {
			if r.exec(candidate+"\n"+t+"\n") == nil {
				passed++
			}
		}
		return float64(passed) / float64(len(tests))
	}
	return 0
}

func (r *SubprocessCodeRunner) exec(script string) error {
	to := r.TimeoutSec
	if to <= 0 {
		to = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(to)*time.Second)
	defer cancel()
	py := r.Python
	if py == "" {
		py = "python3"
	}
	cmd := exec.CommandContext(ctx, py, "-c", script)
	return cmd.Run() // nil == exit 0 == all assertions held
}

func toStringList(v any) ([]string, bool) {
	switch xs := v.(type) {
	case []string:
		return xs, true
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}
