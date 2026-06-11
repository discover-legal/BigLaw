// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import "strings"

const beliefStep = 0.15 // default fixed step (0.1–0.2)
const beliefSmall = 0.1

// roleOf maps a skill name to its §10 belief-source role category.
func roleOf(skill string) string {
	s := strings.ToLower(skill)
	switch {
	case strings.HasPrefix(s, "solver"):
		return "solver"
	case s == "planner":
		return "planner"
	case s == "memory" || strings.HasPrefix(s, "web_search"):
		return "memory"
	case strings.HasPrefix(s, "verifier"):
		return "verifier"
	case s == "synthesiser" || s == "synthesizer":
		return "synthesiser"
	case s == "critic":
		return "critic"
	case s == "evaluator":
		return "evaluator"
	}
	return s
}

func applyRole(b BeliefVector, role string, delta map[string]any, meta map[string]any) BeliefVector {
	has := func(k string) bool { v, ok := delta[k]; return ok && v != nil }
	switch role {
	case "planner":
		if has("subproblem") {
			b.HandoffQuality = clamp01(b.HandoffQuality + beliefStep)
			b.Uncertainty = clamp01(b.Uncertainty - beliefStep)
		}
	case "memory":
		nb := 0
		if lst, ok := delta["evidence"].([]any); ok {
			nb = len(lst)
		} else if has("evidence") {
			nb = 1
		}
		inc := beliefSmall * float64(max1(nb))
		if inc > 0.3 {
			inc = 0.3
		}
		b.Evidence = clamp01(b.Evidence + inc)
		b.Uncertainty = clamp01(b.Uncertainty - beliefSmall)
		b.HandoffQuality = clamp01(b.HandoffQuality + beliefSmall)
	case "solver":
		if has("draft_answer") {
			b.Correctness = clamp01(b.Correctness + beliefStep)
			b.Uncertainty = clamp01(b.Uncertainty - beliefStep)
			b.HandoffQuality = clamp01(b.HandoffQuality + beliefStep)
		}
	case "critic":
		if has("critique") {
			b.Contradiction = clamp01(b.Contradiction + beliefStep)
			b.Uncertainty = clamp01(b.Uncertainty + beliefSmall)
		}
	case "verifier":
		verdict := strings.ToLower(asString(meta["verdict"]) + asString(delta["verification"]))
		switch {
		case strings.Contains(verdict, "support") || strings.Contains(verdict, "pass") || strings.Contains(verdict, "correct"):
			b.Correctness = clamp01(b.Correctness + beliefStep)
			b.Uncertainty = clamp01(b.Uncertainty - beliefStep)
			b.Contradiction = clamp01(b.Contradiction - beliefSmall)
			b.Evidence = clamp01(b.Evidence + beliefSmall)
		case strings.Contains(verdict, "refut") || strings.Contains(verdict, "fail") || strings.Contains(verdict, "incorrect"):
			b.Correctness = clamp01(b.Correctness - beliefStep)
			b.Contradiction = clamp01(b.Contradiction + beliefStep)
			b.Uncertainty = clamp01(b.Uncertainty + beliefSmall)
		}
	case "synthesiser":
		if has("merged_answer") {
			b.Correctness = clamp01(b.Correctness + beliefStep)
			b.HandoffQuality = clamp01(b.HandoffQuality + beliefStep)
			b.Uncertainty = clamp01(b.Uncertainty - beliefStep)
		}
	}
	// evaluator: no belief delta.
	return b
}

func updateBeliefsCell(b BeliefVector, out CellOutput) BeliefVector {
	return applyRole(b, roleOf(out.Skill), out.HandoffDelta, out.Meta)
}

// updateBeliefsTopology derives the delta from which handoff fields the merged
// result populated + the final verifier/tester outcome — the §10 table, once [NEW].
func updateBeliefsTopology(b BeliefVector, res TopologyResult) BeliefVector {
	f := res.MergedHandoff.Fields
	if f["merged_answer"] != nil {
		b = applyRole(b, "synthesiser", f, nil)
	} else if f["draft_answer"] != nil {
		b = applyRole(b, "solver", f, nil)
	}
	if f["verification"] != nil {
		b = applyRole(b, "verifier", f, map[string]any{"verdict": f["verification"]})
	}
	if f["evidence"] != nil {
		b = applyRole(b, "memory", f, nil)
	}
	return b
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
