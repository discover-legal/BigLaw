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
// unused part (DESIGN § Budgets). The root keeps a floor (reserve + meta) that only a Final tool
// may spend, so finish always runs.
type Budget struct {
	mu        *sync.Mutex // one per tree
	parent    *Budget
	calls     int
	usd       float64
	floor     int
	spent     Usage
	heldCalls int
	heldUSD   float64
	closed    bool
}

// NewBudget is a task's ledger: calls and USD caps, and the calls kept back for finish.
func NewBudget(calls int, usd float64, floor int) *Budget {
	return &Budget{mu: &sync.Mutex{}, calls: calls, usd: usd, floor: floor}
}

// free is what a new grant may take: unspent and unheld, less the floor unless final. The floor
// keeps its proportional share of the USD too.
func (b *Budget) free(final bool) (int, float64) {
	rc, ru := b.calls-b.spent.Calls-b.heldCalls, b.usd-b.spent.USD-b.heldUSD
	if f := min(b.floor, max(rc, 0)); !final && f > 0 {
		ru -= ru * float64(f) / float64(rc)
		rc -= f
	}
	return max(rc, 0), max(ru, 0)
}

// Remaining is what a non-final call could still be granted.
func (b *Budget) Remaining() (calls int, usd float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.free(false)
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
func (b *Budget) Grant(calls int, usd float64, final bool) (*Budget, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fc, fu := b.free(final)
	if calls > 0 && fc == 0 || usd > fu {
		return nil, fmt.Errorf("%w: %d calls and $%.4f are free (the run keeps %d calls for finish)", ErrBudget, fc, fu, b.floor)
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
