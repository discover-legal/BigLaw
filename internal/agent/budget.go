// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"fmt"
	"sync"
)

// Budget is a task ledger, or a grant carved out of one. All budget arithmetic is here, in Go: no
// model computes its own budget. A grant holds its calls and USD in its parent until it is closed;
// a charge anywhere in the tree is spent at every level above it, and closing a grant returns its
// unused part (DESIGN § Budgets). The root keeps back, in calls and USD alike, what the run's later
// phases need (Keep): a grant at one level never takes what is kept for a higher one, so evidence
// cannot spend the drafting share, nothing but the committee and finish its cycle, and only a Final
// tool the reserve, so finish always runs.
type Budget struct {
	mu        *sync.Mutex // one per tree
	parent    *Budget
	calls     int
	usd       float64
	keep      [LevelFinal + 1]Grant // keep[l]: held back from grants below level l
	spent     Usage
	heldCalls int
	heldUSD   float64
	closed    bool
}

// The budget levels, in the order a run's phases need them: a grant at a level may spend what is
// kept for its level and below. Work is a lead turn, drafting and the free tools.
const (
	LevelEvidence = iota
	LevelWork
	LevelReview
	LevelFinal
)

// Level is the budget level a tool's grant is taken at, by its class.
func Level(s Spec) int {
	switch {
	case s.Final:
		return LevelFinal
	case s.Class == "evidence" || s.Class == "plan":
		return LevelEvidence
	case s.Class == "committee":
		return LevelReview
	}
	return LevelWork
}

// NewBudget is a task's ledger: calls and USD caps, and the calls kept back for finish (with
// their proportional share of the USD).
func NewBudget(calls int, usd float64, floor int) *Budget {
	b := &Budget{mu: &sync.Mutex{}, calls: calls, usd: usd}
	if floor > 0 && calls > 0 {
		b.keep[LevelFinal] = Grant{floor, usd * float64(floor) / float64(calls)}
	}
	return b
}

// Keep holds g back from every grant below level (it replaces what that level kept before).
func (b *Budget) Keep(level int, g Grant) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.keep[level] = g
}

// free is what a new grant at level may take: unspent and unheld, less what higher levels keep.
func (b *Budget) free(level int) (int, float64) {
	rc, ru := b.calls-b.spent.Calls-b.heldCalls, b.usd-b.spent.USD-b.heldUSD
	for _, k := range b.keep[level+1:] {
		rc, ru = rc-k.Calls, ru-k.USD
	}
	return max(rc, 0), max(ru, 0)
}

// Remaining is what a call at the work level (a lead turn, drafting) could still be granted.
func (b *Budget) Remaining() (calls int, usd float64) {
	return b.RemainingAt(LevelWork)
}

// RemainingAt is what a call at level could still be granted.
func (b *Budget) RemainingAt(level int) (calls int, usd float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.free(level)
}

// Spent is everything charged to b, nested grants included.
func (b *Budget) Spent() Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Grant carves a grant of up to calls model calls out of b: as many as are free (a turn cap is a
// maximum, not a need), refused only when none is free and calls > 0. usd < 0 grants all the free
// USD; usd ≥ 0 must fit (a model call's worst-case cost). Close it when the call returns.
func (b *Budget) Grant(calls int, usd float64, level int) (*Budget, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fc, fu := b.free(level)
	if calls > 0 && fc == 0 || usd > fu {
		kept := 0
		for _, k := range b.keep[level+1:] {
			kept += k.Calls
		}
		return nil, fmt.Errorf("%w: %d calls and $%.4f are free at this stage (the run keeps %d calls for its later stages and finish)", ErrBudget, fc, fu, kept)
	}
	if usd < 0 {
		usd = fu
	}
	g := &Budget{mu: b.mu, parent: b, calls: min(calls, fc), usd: usd}
	b.heldCalls, b.heldUSD = b.heldCalls+g.calls, b.heldUSD+g.usd
	return g, nil
}

// Charge spends u at b and every level above it, converting the parents' holds into spending.
// Money already spent is always charged, even past a cap (a misbehaving remote tool): the cap is
// enforced before calls, by Grant.
func (b *Budget) Charge(u Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := b; c != nil; c = c.parent {
		if p := c.parent; p != nil && !c.closed {
			p.heldCalls -= min(u.Calls, max(0, c.calls-c.spent.Calls))
			p.heldUSD -= min(u.USD, max(0, c.usd-c.spent.USD))
		}
		c.spent.Add(u)
	}
}

// Close returns a grant's unused part to its parent. Closing twice is a no-op.
func (b *Budget) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.parent; p != nil && !b.closed {
		p.heldCalls -= max(0, b.calls-b.spent.Calls)
		p.heldUSD -= max(0, b.usd-b.spent.USD)
	}
	b.closed = true
}

// Grant is the value a callee receives (Call.Budget).
func (b *Budget) Value() Grant {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Grant{Calls: b.calls, USD: b.usd}
}
