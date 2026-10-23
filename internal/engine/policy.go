// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/analysis"
)

// Allocation is the task's budget split by phase (README § The engine, DESIGN § Budgets), in calls
// and in USD alike: each share's USD is its fraction of the calls applied to the USD cap, except a
// committee cycle, whose critics read the whole draft, which may spend a fifth of the USD.
type Allocation struct {
	Plan, Evidence, Sweep, Draft int // the most each class may spend (plan: before a plan exists)
	Cover                        int // the coverage pass's part of the evidence share, kept back for it
	Cycle                        int // one committee cycle: prosecute + reproduce + audit_gaps
	Critics                      [3]int
	Reserve, Meta                int     // kept for finish and its meta note (the root budget's floor)
	FixMin                       int     // calls a fix stretch needs, beside a cycle, to count as funded
	Calls                        int     // the task's caps
	USD, CycleUSD                float64 // CycleUSD: what one committee cycle may spend
}

// Allocate splits a budget: plan ≤ 7% (min 3), evidence ≤ 50% of which sweep ≤ 35% and the
// coverage pass 6% (kept for it; the lead's evidence calls may spend the rest), draft ≤ 15%, a
// committee cycle of 20 calls (8/8/4) or 20% of a smaller budget, reserve 5 and meta 3.
func Allocate(calls int, usd float64) Allocation {
	a := Allocation{Plan: max(3, calls*7/100), Evidence: calls / 2, Sweep: calls * 35 / 100, Cover: calls * 6 / 100, Draft: calls * 15 / 100,
		Cycle: min(20, calls/5), Reserve: 5, Meta: 3, FixMin: 3, Calls: calls, USD: usd, CycleUSD: usd / 5}
	a.Critics[0] = a.Cycle * 2 / 5
	a.Critics[1] = a.Critics[0]
	a.Critics[2] = a.Cycle - 2*a.Critics[0]
	return a
}

// dollars is the USD share of n calls.
func (a Allocation) dollars(n int) float64 { return a.USD * float64(n) / float64(max(a.Calls, 1)) }

// keep holds back from the root budget, in calls and USD, what the later phases need (DESIGN §
// Budgets): the reserve and meta from all but finish, one committee cycle and (until it has run)
// the coverage pass from everything below the committee (so the last draft can always be checked
// and reviewed), and the unspent drafting share from evidence.
func (r *run) keep(a Allocation, spent map[string]agent.Usage, covered bool) {
	d, cover := spent["draft"], a.Cover
	if covered {
		cover = 0
	}
	r.b.Keep(agent.LevelFinal, agent.Grant{Calls: a.Reserve + a.Meta, USD: a.dollars(a.Reserve + a.Meta)})
	r.b.Keep(agent.LevelReview, agent.Grant{Calls: a.Cycle + cover, USD: a.CycleUSD + a.dollars(cover)})
	r.b.Keep(agent.LevelWork, agent.Grant{Calls: max(0, a.Draft-d.Calls), USD: max(0, a.dollars(a.Draft)-d.USD)})
}

// share is the most a class may spend; 0 means the class has no share (free, committee, plan,
// cover: the coverage pass sizes its own work).
func (a Allocation) share(tool, class string) int {
	switch {
	case tool == "sweep":
		return a.Sweep
	case class == "evidence":
		return a.Evidence - a.Cover
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
	a := Allocate(e.cfg.BudgetCalls, e.cfg.BudgetUSD)
	spent, err := e.classSpend(ctx, id)
	if err != nil {
		return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "the task's spend could not be read", Next: "try again"}
	}
	covered, err := e.covered(ctx, id)
	if err != nil {
		return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "the task's ledger could not be read", Next: "try again"}
	}
	p.r.keep(a, spent, covered)
	issues, err := load[Issue](ctx, e, id, kIssue)
	if err == nil && len(issues) == 0 && x.Spec.Name != "retrieve" && x.Spec.Name != "ledger" && x.Spec.Name != "finish" {
		if spent["*"].Calls < a.Plan {
			return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "no plan is recorded yet: until it is, only retrieve and ledger run",
				Next: `call ledger {"op":"plan","issues":[{"id":"I1","question":"…","class":"Conduct","practice":"litigation"}, …]} with the questions the deliverable must answer`}
		}
		if err := p.r.fallbackPlan(ctx); err != nil {
			return &agent.Refusal{Kind: agent.ErrPolicy, Reason: "the checklist plan could not be recorded", Next: "record a plan with ledger op plan"}
		}
	}
	if sh := a.share(x.Spec.Name, x.Spec.Class); sh > 0 {
		used := spent[cmpStr(x.Spec.Name == "sweep", "sweep", x.Spec.Class)]
		if x.Spec.Kind == "subagent" && used.Calls+x.Spec.MaxTurns > sh || used.Calls >= sh || a.USD > 0 && used.USD >= a.dollars(sh) {
			return &agent.Refusal{Kind: agent.ErrPolicy, Reason: fmt.Sprintf("%s's budget class (%s) has spent %d of its %d calls ($%.2f of $%.2f)", x.Spec.Name,
				cmpStr(x.Spec.Name == "sweep", "sweep", x.Spec.Class), used.Calls, sh, used.USD, a.dollars(sh)), Next: "work with what the ledger holds: draft, run committee, or finish"}
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

// classSpend is what the task's lead steps spent (calls and USD), by tool class ("sweep" for sweep
// alone, "*" for all, lead turns included), from the step log.
func (e *Engine) classSpend(ctx context.Context, taskID string) (map[string]agent.Usage, error) {
	q, err := e.db.QueryContext(ctx, `SELECT tool, coalesce(sum((usage->>'calls')::int), 0), coalesce(sum((usage->>'usd')::float8), 0)
		FROM task_steps WHERE task_id = $1 AND status <> 'started' GROUP BY tool`, taskID)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	out := map[string]agent.Usage{}
	for q.Next() {
		var tool string
		var u agent.Usage
		if err := q.Scan(&tool, &u.Calls, &u.USD); err != nil {
			return nil, err
		}
		keys := []string{specs[tool].Class, "*"}
		if tool == "sweep" {
			keys = append(keys, "sweep")
		}
		for _, k := range keys {
			v := out[k]
			v.Add(u)
			out[k] = v
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

// funded reports whether the run can still pay for a fix stretch beside the committee cycle,
// reserve and (until it has run) coverage pass it keeps back, in calls and in USD (unspent: finish's
// own grant holds all the free USD, and held of its calls, which count as free).
func (r *run) funded(b *agent.Budget, held int, covered bool) bool {
	calls, _ := b.RemainingBeside(agent.LevelWork, agent.Grant{Calls: held})
	a := Allocate(r.e.cfg.BudgetCalls, r.e.cfg.BudgetUSD)
	cover := map[bool]float64{false: a.dollars(a.Cover)}[covered]
	return calls >= a.FixMin && a.USD-b.Spent().USD-a.CycleUSD-cover-a.dollars(a.Reserve+a.Meta) >= a.dollars(a.FixMin)
}
