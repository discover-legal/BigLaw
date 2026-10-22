// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// Allocation is the task's call budget split by class (README § The engine, DESIGN § Budgets).
type Allocation struct {
	Plan, Evidence, Sweep, Draft int // the most each class may spend (plan: before a plan exists)
	Cycle                        int // one committee cycle: prosecute + reproduce + audit_gaps
	Critics                      [3]int
	Reserve, Meta                int // kept for finish and its meta note (the root budget's floor)
	FixMin                       int // calls a fix stretch needs, beside a cycle, to count as funded
}

// Allocate splits a call budget: plan ≤ 7% (min 3), evidence ≤ 50% of which sweep ≤ 35%, draft
// ≤ 15%, a committee cycle of 20 (8/8/4) or 20% of a smaller budget, reserve 5 and meta 3.
func Allocate(calls int) Allocation {
	a := Allocation{Plan: max(3, calls*7/100), Evidence: calls / 2, Sweep: calls * 35 / 100, Draft: calls * 15 / 100,
		Cycle: min(20, calls/5), Reserve: 5, Meta: 3, FixMin: 3}
	a.Critics[0] = a.Cycle * 2 / 5
	a.Critics[1] = a.Critics[0]
	a.Critics[2] = a.Cycle - 2*a.Critics[0]
	return a
}

// share is the most a class may spend; 0 means the class has no share (free, committee, plan).
func (a Allocation) share(tool, class string) int {
	switch {
	case tool == "sweep":
		return a.Sweep
	case class == "evidence":
		return a.Evidence
	case class == "draft":
		return a.Draft
	}
	return 0
}

// policy is the task's rulebook beyond the runtime's own (DESIGN § The lead's tool-choice policy):
// P1 plan first (with the checklist fallback) and P4 class shares. P6/P7 are finish's own checks.
type policy struct{ r *run }

func (p policy) Allow(ctx context.Context, x agent.Pending) *agent.Refusal {
	if x.Depth > 0 || x.Agent != "lead" { // subagents, and sweep/committee's own calls (they size their work)
		return nil
	}
	e, id := p.r.e, p.r.t.ID
	a := Allocate(e.cfg.BudgetCalls)
	spent, err := e.classSpend(ctx, id)
	if err != nil {
		return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "the task's spend could not be read", Next: "try again"}
	}
	issues, err := load[Issue](ctx, e, id, kIssue)
	if err == nil && len(issues) == 0 && x.Spec.Name != "retrieve" && x.Spec.Name != "ledger" && x.Spec.Name != "finish" {
		if spent["*"] < a.Plan {
			return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "no plan is recorded yet: until it is, only retrieve and ledger run",
				Next: `call ledger {"op":"plan","issues":[{"id":"I1","question":"…","class":"Conduct","practice":"litigation"}, …]} with the questions the deliverable must answer`}
		}
		if err := p.r.fallbackPlan(ctx); err != nil {
			return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "the checklist plan could not be recorded", Next: "record a plan with ledger op plan"}
		}
	}
	if sh := a.share(x.Spec.Name, x.Spec.Class); sh > 0 {
		used := spent[x.Spec.Class]
		if x.Spec.Name == "sweep" {
			used = spent["sweep"]
		}
		if x.Spec.Kind == "subagent" && used+x.Spec.MaxTurns > sh || used >= sh {
			return &agent.Refusal{Kind: agent.ErrPolicy, Reason: fmt.Sprintf("%s's budget class (%s) has spent %d of its %d calls", x.Spec.Name, cmpStr(x.Spec.Name == "sweep", "sweep", x.Spec.Class), used, sh),
				Next: "work with what the ledger holds: draft, run committee, or finish"}
		}
	}
	return nil
}

func cmpStr(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// classSpend is the calls the task's lead steps spent, by tool class ("sweep" for sweep alone,
// "*" for all), from the step log.
func (e *Engine) classSpend(ctx context.Context, taskID string) (map[string]int, error) {
	q, err := e.db.QueryContext(ctx, `SELECT tool, coalesce(sum((usage->>'calls')::int), 0) FROM task_steps WHERE task_id = $1 AND status <> 'started' GROUP BY tool`, taskID)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := map[string]int{}
	for q.Next() {
		var tool string
		var n int
		if err := q.Scan(&tool, &n); err != nil {
			return nil, err
		}
		s := specs[tool]
		out[s.Class] += n
		out["*"] += n
		if tool == "sweep" {
			out["sweep"] += n
		}
	}
	return out, q.Err()
}

// fallbackPlan records the packs' checklist as the plan and says so in Limitations (P1).
func (r *run) fallbackPlan(ctx context.Context) error {
	items := Checklist(r.t.Jurisdiction, "")
	if len(items) == 0 {
		items = []string{r.t.Description}
	}
	for i, it := range items {
		is := Issue{Issue: analysis.Issue{ID: fmt.Sprintf("C%d", i+1), Question: it, Class: "RedFlag"}}
		if err := r.e.write(ctx, r.t.ID, kIssue, is.ID, is, false); err != nil {
			return err
		}
	}
	return r.e.write(ctx, r.t.ID, kNote, "plan", "The lead recorded no plan within its plan budget, so the practice packs' checklist was used as the plan.", false)
}

// funded reports whether the run can still pay for a fix stretch and another committee cycle.
func (r *run) funded(b *agent.Budget) bool {
	calls, _ := b.Remaining()
	a := Allocate(r.e.cfg.BudgetCalls)
	return calls >= a.Cycle+a.FixMin
}
