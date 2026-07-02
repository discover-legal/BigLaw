// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"errors"
	"fmt"
	"strings"
)

// Budget caps total trajectory tokens.
type Budget struct {
	Cap  int
	Used int
}

func (b *Budget) charge(t int)    { b.Used += t }
func (b *Budget) exhausted() bool { return b.Used >= b.Cap }
func (b *Budget) remaining() int  { return b.Cap - b.Used }

// Trajectory is the outcome of a task run.
type Trajectory struct {
	Trace     *Trace
	Reward    float64
	Quality   float64
	SubScores map[string]any
	Report    *RunReport
	Aborted   bool
}

// SearchProvider is the web-search tool seam (spec §13).
type SearchProvider interface {
	Search(query string, k int) []map[string]any
}

// RunOptions configure a task run.
type RunOptions struct {
	Transport      Transport
	Embedder       Embedder
	Quality        QualitySignal
	Peers          []*Trace
	SearchProvider SearchProvider
	// SelectFn is an optional policy-injection seam (tests / future policies);
	// it must return one of the legal actions. Nil = UCB1 via the policy graph.
	SelectFn func(sig Signature, legal []Action) Action
}

func makeGenerator(mode string) (CompositeTopologyCell, error) {
	switch mode {
	case "linear":
		return LinearWithSkipGenerator{}, nil
	case "dytopo":
		return DyTopoGenerator{}, nil
	}
	return nil, fmt.Errorf("unknown topology mode %q", mode)
}

func runCell(skill, model string, ctx TaskContext, handoff *HandoffState, tx Transport, cfg Config, sp SearchProvider) (CellOutput, error) {
	s := strings.ToLower(skill)
	out := CellOutput{Skill: skill, Model: model, HandoffDelta: map[string]any{}, Meta: map[string]any{}}

	// web-search cells are tool actions, not LLM calls, when a provider is wired.
	if strings.HasPrefix(s, "web_search") && sp != nil {
		res := sp.Search(ctx.Prompt, 5)
		evidence := make([]any, len(res))
		for i, r := range res {
			evidence[i] = r
		}
		out.HandoffDelta["evidence"] = evidence
		return out, nil
	}

	var present []string
	for _, f := range HandoffFields {
		if handoff.Get(f) != nil {
			present = append(present, f)
		}
	}
	fields, tokens, err := tx.Complete(LLMRequest{
		Model: model, System: "You are the " + skill + " cell.",
		User:   "Task: " + ctx.Prompt + "\nKnown: " + strings.Join(present, ", "),
		Schema: []string{"answer"}, Purpose: "invoke", Role: skill, Meta: map[string]any{"skill": skill},
	})
	if err != nil {
		return out, err
	}
	out.Tokens = tokens
	out.Failed = asBool(fields["_failed"])
	switch {
	case strings.HasPrefix(s, "solver"):
		if v := fields["draft_answer"]; v != nil {
			out.HandoffDelta["draft_answer"] = v
		} else if v := fields["answer"]; v != nil {
			out.HandoffDelta["draft_answer"] = v
		}
	case s == "planner":
		if v := fields["goal"]; v != nil {
			out.HandoffDelta["goal"] = v
		}
		if v := fields["subproblem"]; v != nil {
			out.HandoffDelta["subproblem"] = v
		}
	case s == "memory" || strings.HasPrefix(s, "web_search"):
		if v := fields["evidence"]; v != nil {
			out.HandoffDelta["evidence"] = v
		}
	case strings.HasPrefix(s, "verifier"):
		if v := fields["verification"]; v != nil {
			out.HandoffDelta["verification"] = v
			out.Meta["verdict"] = v
		}
	case s == "evaluator":
		out.Complete = asBool(fields["complete"])
	}
	if out.Failed {
		out.Retries = 1
	}
	return out, nil
}

func applyHandoff(h *HandoffState, out CellOutput) {
	for k, v := range out.HandoffDelta {
		h.Set(k, v)
	}
}

func mergeHandoff(h *HandoffState, merged *HandoffState) {
	for k, v := range merged.Fields {
		if v != nil {
			h.Set(k, v)
		}
	}
}

func evaluatorMarksComplete(h *HandoffState) bool { return asBool(h.Get("complete")) }

// RunTask is the macro control loop (spec §9) [AF] + [NEW].
func RunTask(ctx TaskContext, cfg Config, pg *PolicyGraph, opts RunOptions) (*Trajectory, error) {
	tx := opts.Transport
	if err := preflight(ctx, cfg, tx); err != nil {
		if errors.Is(err, ErrGovernanceAbort) {
			tr := NewTrace(ctx.TaskID)
			rep := buildRunReport(tr, 0, 0, map[string]any{"confidence": 1.0}, true, err.Error())
			return &Trajectory{Trace: tr, Report: rep, Aborted: true}, nil
		}
		return nil, err
	}

	handoff := NewHandoffState()
	beliefs := NewBeliefVector()
	trace := NewTrace(ctx.TaskID)
	budget := &Budget{Cap: cfg.TokenCap}
	plan := NewPlan(cfg.OptionalAux)
	steps := 0

	for {
		steps++
		sig := Fold(ctx, handoff, beliefs, cfg)
		legal := legalActions(plan, cfg)
		if len(legal) == 0 || budget.exhausted() || steps > cfg.MaxMacroSteps {
			break
		}
		var action Action
		if opts.SelectFn != nil {
			action = opts.SelectFn(sig, legal)
		} else {
			action = pg.Select(sig, legal)
		}
		trace.recordDecision(sig, action)

		switch action.Kind {
		case "skip":
			plan.markSkipped(action.Target)
			trace.add(skipEvent(action))
			continue

		case "topology":
			gen, err := makeGenerator(action.TopoMode)
			if err != nil {
				return nil, err
			}
			agents := RoleSetFor(ctx.Domain, cfg)
			res, err := gen.Run(ctx, handoff, agents, action, tx, opts.Embedder, cfg, budget.remaining())
			if err != nil {
				return nil, err
			}
			mergeHandoff(handoff, res.MergedHandoff)
			beliefs = updateBeliefsTopology(beliefs, res)
			budget.charge(res.TotalTokens)
			trace.Tokens += res.TotalTokens
			trace.Retries += res.Retries
			if res.Failed {
				trace.AnyFailure = true
			}
			trace.add(topologyEvent(action, res))
			if res.Failed || evaluatorMarksComplete(handoff) {
				goto finish
			}
			continue

		case "invoke":
			if cfg.IsOptionalAux(action.Skill) {
				plan.markInvoked(action.Skill)
			}
			out, err := runCell(action.Skill, action.Model, ctx, handoff, tx, cfg, opts.SearchProvider)
			if err != nil {
				return nil, err
			}
			applyHandoff(handoff, out)
			beliefs = updateBeliefsCell(beliefs, out)
			budget.charge(out.Tokens)
			trace.Tokens += out.Tokens
			trace.Retries += out.Retries
			if out.Failed {
				trace.AnyFailure = true
			}
			trace.add(invokeEvent(action, out))
			if action.Skill == "evaluator" && out.Complete {
				goto finish
			}
			if violation(trace, cfg) {
				goto finish
			}
		}
	}

finish:
	if v := handoff.Get("merged_answer"); v != nil {
		trace.FinalAnswer = fmt.Sprintf("%v", v)
	} else if v := handoff.Get("draft_answer"); v != nil {
		trace.FinalAnswer = fmt.Sprintf("%v", v)
	}

	q := opts.Quality
	if q == nil {
		q = MakeQuality(ctx, cfg, nil, nil)
	}
	Q, sub := q.Score(ctx, trace, opts.Peers)
	confidence := 1.0
	if c, ok := asFloat(sub["confidence"]); ok {
		confidence = c
	}
	r := hybridReward(Q, trace.Tokens, trace.Retries, cfg)

	for _, d := range trace.DecisionPath() {
		pg.Backup(d.Sig, d.Act, r, trace.Tokens, trace.AnyFailure, confidence)
	}

	report := buildRunReport(trace, r, Q, sub, false, "")
	return &Trajectory{Trace: trace, Reward: r, Quality: Q, SubScores: sub, Report: report}, nil
}
