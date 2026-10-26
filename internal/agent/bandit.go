// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
)

// The tool bandit (AgensFlow, DESIGN § The tool bandit): a record of how well each tool, and each
// model binding of a subagent, has done per kind of task, used to rank the tools in one hint line.
// It never hides, adds or reorders tool definitions and never refuses a call (P9).

// Arm is one (tool, model) pair; Model is "" for a Go tool.
type Arm struct{ Tool, Model string }

// Stat is the running record of an arm: calls (N), summed reward and its square, failures, and
// summed model calls and tokens spent.
type Stat struct{ N, Reward, RewardSq, Fails, Calls, Tokens float64 }

func (s Stat) plus(d Stat) Stat {
	return Stat{s.N + d.N, s.Reward + d.Reward, s.RewardSq + d.RewardSq, s.Fails + d.Fails, s.Calls + d.Calls, s.Tokens + d.Tokens}
}

// Stats stores the record (in memory here; Postgres in engine, PR 4b). Add adds d to an arm's row.
type Stats interface {
	Load(ctx context.Context, sig string) (map[Arm]Stat, error)
	Add(ctx context.Context, sig string, a Arm, d Stat) error
}

// Signature is the kind of task the record is kept for: workflow, practice, jurisdiction prefix,
// lead model and the number of documents in buckets.
func Signature(workflow, practice, jurisdiction, leadModel string, docs int) string {
	bucket := map[bool]string{true: "1-3", false: map[bool]string{true: "4-9", false: "10+"}[docs < 10]}[docs < 4]
	jur, _, _ := strings.Cut(strings.ToUpper(jurisdiction), "-")
	return strings.Join([]string{workflow, practice, jur, leadModel, bucket}, "|")
}

const (
	failPenalty = 0.5 // β: per unit of failure rate
	costWeight  = 1.0 // γ: per share of the task's call budget one call of the tool spends
	explore     = 0.5 // c: the UCB1 exploration weight
)

// Bandit ranks tools for one run. Off (the task's toolBandit: "off") sends no hint and picks the
// role's own model, but the record is still kept.
type Bandit struct {
	Stats       Stats
	Sig         string
	BudgetCalls int
	Off         bool
	Pinned      []string // always listed first in the hint, in this order; ranked are the rest
	Skip        []string // outside the bandit, never ranked or recorded (verify_quote: Go also runs it on every quote)

	mu       sync.Mutex
	promoted map[Arm]bool // untried arms already offered first once
}

// score is mean − β·failRate − γ·meanCalls/budget + c·√(ln N / (n+1)); an untried arm is +Inf
// the first time it is ranked, so every tool and model is tried.
func (b *Bandit) score(s Stat, total float64, a Arm) float64 {
	if s.N <= 0 {
		return map[bool]float64{false: math.Inf(1)}[b.promoted[a]] + explore*math.Sqrt(math.Log(total+1))
	}
	return (s.Reward-failPenalty*s.Fails-costWeight*s.Calls/float64(max(b.BudgetCalls, 1)))/s.N + explore*math.Sqrt(math.Log(total+1)/(s.N+1))
}

type ranked struct {
	arm   Arm
	stat  Stat
	score float64
}

// rank orders arms by score (ties by name) and marks the untried one it put first as offered.
func (b *Bandit) rank(stats map[Arm]Stat, arms []Arm) []ranked {
	var total float64
	for _, s := range stats {
		total += s.N
	}
	out := make([]ranked, len(arms))
	for i, a := range arms {
		out[i] = ranked{a, stats[a], b.score(stats[a], total, a)}
	}
	slices.SortStableFunc(out, func(x, y ranked) int {
		return cmp.Or(cmp.Compare(y.score, x.score), cmp.Compare(x.arm.Tool, y.arm.Tool), cmp.Compare(x.arm.Model, y.arm.Model))
	})
	if len(out) > 0 && math.IsInf(out[0].score, 1) {
		if b.promoted == nil {
			b.promoted = map[Arm]bool{}
		}
		b.promoted[out[0].arm] = true
	}
	return out
}

// Hint is the one line appended to the latest tool result before a lead turn ("" when off).
func (b *Bandit) Hint(ctx context.Context, visible []string) string {
	if b == nil || b.Off {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stats, err := b.Stats.Load(ctx, b.Sig)
	if err != nil {
		return ""
	}
	byTool := map[Arm]Stat{} // a tool's record over all its model bindings
	for a, s := range stats {
		byTool[Arm{Tool: a.Tool}] = byTool[Arm{Tool: a.Tool}].plus(s)
	}
	var pinned []string
	var arms []Arm
	for _, p := range b.Pinned {
		if slices.Contains(visible, p) {
			pinned = append(pinned, p)
		}
	}
	for _, t := range visible {
		if !slices.Contains(b.Pinned, t) && !slices.Contains(b.Skip, t) {
			arms = append(arms, Arm{Tool: t})
		}
	}
	parts := []string{}
	for _, r := range b.rank(byTool, arms) {
		if r.stat.N == 0 {
			parts = append(parts, r.arm.Tool+" untried")
		} else {
			parts = append(parts, fmt.Sprintf("%s %.2f (%d calls)", r.arm.Tool, r.stat.Reward/r.stat.N, int(r.stat.N)))
		}
	}
	return "Track record for this kind of task (a hint, not a rule; every tool stays available): " +
		strings.Join(pinned, ", ") + "; then " + strings.Join(parts, ", ") + "."
}

// PickModel chooses which configured model runs a subagent call, only ever among candidates (the
// models configured for its role: the role's own and the lead's). Off picks the first.
func (b *Bandit) PickModel(ctx context.Context, tool string, candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	if b == nil || b.Off || len(candidates) == 1 {
		return candidates[0]
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	stats, err := b.Stats.Load(ctx, b.Sig)
	if err != nil {
		return candidates[0]
	}
	arms := make([]Arm, len(candidates))
	for i, m := range candidates {
		arms[i] = Arm{tool, m}
	}
	return b.rank(stats, arms)[0].arm.Model
}

// Record adds one tool result: reward 0–1 (a refused or failed call scores 0) and what it spent.
func (b *Bandit) Record(ctx context.Context, a Arm, reward float64, failed bool, u Usage) error {
	if b == nil || slices.Contains(b.Skip, a.Tool) {
		return nil
	}
	f := map[bool]float64{true: 1}[failed]
	return b.Stats.Add(ctx, b.Sig, a, Stat{1, reward, reward * reward, f, float64(u.Calls), float64(u.InputTokens + u.OutputTokens)})
}
