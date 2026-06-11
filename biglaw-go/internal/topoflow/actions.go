// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

// Plan tracks which optional aux cells are still schedulable.
type Plan struct {
	optional []string
	invoked  map[string]bool
	skipped  map[string]bool
}

// NewPlan builds a plan over the optional aux cells.
func NewPlan(optional []string) *Plan {
	return &Plan{optional: append([]string(nil), optional...), invoked: map[string]bool{}, skipped: map[string]bool{}}
}

func (p *Plan) stillScheduled() []string {
	var out []string
	for _, c := range p.optional {
		if !p.invoked[c] && !p.skipped[c] {
			out = append(out, c)
		}
	}
	return out
}

func (p *Plan) markInvoked(cell string) { p.invoked[cell] = true }
func (p *Plan) markSkipped(cell string) { p.skipped[cell] = true }

func cellEnabled(skill string, plan *Plan, cfg Config) bool {
	if cfg.IsOptionalAux(skill) {
		return !plan.invoked[skill] && !plan.skipped[skill]
	}
	return true // solvers + evaluator always invocable (budget bounds them)
}

// legalActions enumerates A(s) (spec §5). Edges are never arms.
func legalActions(plan *Plan, cfg Config) []Action {
	var A []Action
	for _, km := range cfg.VariantPool() {
		if cellEnabled(km[0], plan, cfg) {
			A = append(A, InvokeAction(km[0], km[1]))
		}
	}
	for _, mode := range cfg.TopoModes {
		switch mode {
		case "linear":
			A = append(A, LinearTopoAction())
		case "dytopo":
			for tb := range cfg.TauBuckets {
				for rb := range cfg.RoundBuckets {
					A = append(A, DytopoAction(tb, cfg.DytopoKIn, rb))
				}
			}
		}
	}
	// skip:X only if >=1 other legal action remains
	if len(A) > 0 {
		for _, X := range plan.stillScheduled() {
			A = append(A, SkipAction(X))
		}
	}
	return pruneToKeepFinishable(A, cfg)
}

// pruneToKeepFinishable guarantees a termination path (the evaluator invoke).
func pruneToKeepFinishable(A []Action, cfg Config) []Action {
	for _, a := range A {
		if a.Kind == "invoke" && a.Skill == "evaluator" {
			return A
		}
	}
	return append(A, InvokeAction("evaluator", cfg.DefaultModelFor("evaluator")))
}
