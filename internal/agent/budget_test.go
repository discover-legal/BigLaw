// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

// 500 seeded random sequences of grants (nested to depth 3), well-behaved charges (a callee
// spends at most its grant, as the loop guarantees with each call's worst-case bound) and closes:
// no level ever spends past its cap, the root never holds and spends more than it has, the floor
// is only spent by final grants, and once every grant is closed nothing stays held.
func TestLedgerNeverExceedsCaps(t *testing.T) {
	for seed := uint64(1); seed <= 500; seed++ {
		r := rand.New(rand.NewPCG(seed, 99))
		root := NewBudget(20+r.IntN(200), 1+r.Float64()*10, r.IntN(10))
		open := []*Budget{root}
		var all []*Budget
		for op := 0; op < 300; op++ {
			b := open[r.IntN(len(open))]
			switch k := r.IntN(10); {
			case k < 4 && len(open) < 12: // grant
				final := b == root && r.IntN(5) == 0
				usd := -1.0
				if r.IntN(2) == 0 {
					usd = r.Float64() * 2
				}
				before, _ := b.Remaining()
				g, err := b.Grant(1+r.IntN(8), usd, final)
				if err != nil {
					if !errors.Is(err, ErrBudget) {
						t.Fatal(err)
					}
					continue
				}
				if !final && g.calls > before {
					t.Fatalf("seed %d: a non-final grant of %d calls took the floor (%d were free)", seed, g.calls, before)
				}
				open, all = append(open, g), append(all, g)
			case k < 8: // charge within what b has left
				b.mu.Lock()
				lc, lu := b.calls-b.spent.Calls-b.heldCalls, b.usd-b.spent.USD-b.heldUSD
				b.mu.Unlock()
				if lc > 0 {
					b.Charge(Usage{Calls: 1 + r.IntN(lc), USD: r.Float64() * max(lu, 0), InputTokens: 10})
				}
			default: // close a grant whose own grants are closed (calls return innermost first); never the root
				i := r.IntN(len(open))
				leaf := !slices.ContainsFunc(open, func(x *Budget) bool { return x.parent == open[i] })
				if open[i] != root && leaf {
					open[i].Close()
					open = append(open[:i], open[i+1:]...)
				}
			}
			for _, x := range append([]*Budget{root}, all...) {
				if x.spent.Calls > x.calls || x.spent.USD > x.usd+1e-9 {
					t.Fatalf("seed %d: spent %+v past the cap %d/$%.4f", seed, x.spent, x.calls, x.usd)
				}
			}
			if root.spent.Calls+root.heldCalls > root.calls || root.spent.USD+root.heldUSD > root.usd+1e-9 {
				t.Fatalf("seed %d: the root spends and holds more than it has", seed)
			}
		}
		for i := len(open) - 1; i > 0; i-- { // innermost first
			open[i].Close()
		}
		if root.heldCalls != 0 || root.heldUSD > 1e-9 || root.heldUSD < -1e-9 {
			t.Fatalf("seed %d: %d calls / $%f still held after every grant closed", seed, root.heldCalls, root.heldUSD)
		}
	}
}

// A subagent granted 3 calls that uses 1 returns 2 to the task.
func TestUnusedBudgetRollsForward(t *testing.T) {
	rt, _ := rig(t, map[string][]model.Reply{"extract_facts": {{Text: `{"ok":true}`}}}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	b := NewBudget(10, 1, 2)
	if _, err := rt.Invoke(context.Background(), lead("extract_facts"), Call{}, "extract_facts", json.RawMessage(q), b, false, 0); err != nil {
		t.Fatal(err)
	}
	if c, _ := b.Remaining(); c != 7 || b.Spent().Calls != 1 {
		t.Fatalf("remaining %d (want 10 − 1 spent − 2 floor = 7), spent %+v", c, b.Spent())
	}
}

// The grant is released when the subagent returns, also when it fails.
func TestGrantReleasedOnSubagentReturn(t *testing.T) {
	rt, _ := rig(t, map[string][]model.Reply{"extract_facts": {{Text: "x"}, {Text: "y"}}}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	b := NewBudget(10, 1, 0)
	if _, err := rt.Invoke(context.Background(), lead("extract_facts"), Call{}, "extract_facts", json.RawMessage(q), b, false, 0); !errors.Is(err, ErrToolOutput) {
		t.Fatalf("want ErrToolOutput, got %v", err)
	}
	if b.heldCalls != 0 || b.heldUSD != 0 || b.Spent().Calls != 2 {
		t.Fatalf("after a failed subagent: held %d/$%f, spent %+v", b.heldCalls, b.heldUSD, b.Spent())
	}
	// A partial grant: the turn cap is a maximum, not a need.
	g, err := NewBudget(2, 1, 0).Grant(3, -1, false)
	if err != nil || g.calls != 2 {
		t.Fatalf("partial grant: %v %+v", err, g)
	}
}
