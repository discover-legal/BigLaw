// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

var ctx = context.Background()

// order is the hint's ranked tools, in order.
func order(hint string) []string {
	_, rest, _ := strings.Cut(hint, "; then ")
	var out []string
	for _, p := range strings.Split(strings.TrimSuffix(rest, "."), ", ") {
		out = append(out, strings.Fields(p)[0])
	}
	return out
}

func seed(st *MemStats, sig, tool string, rewards ...float64) {
	for _, r := range rewards {
		_ = st.Add(ctx, sig, Arm{Tool: tool}, Stat{1, r, r * r, map[bool]float64{true: 1}[r == 0], 1, 100})
	}
}

func TestBanditRewardsChangeRanking(t *testing.T) {
	st := &MemStats{}
	visible := []string{"tool_a", "tool_b"}
	for i := 0; i < 20; i++ {
		seed(st, "memo|lit", "tool_a", 0.2)
		seed(st, "memo|lit", "tool_b", 0.9)
		_ = st.Add(ctx, "memo|lit", Arm{"tool_b", "m-other"}, Stat{1, 0.9, 0.81, 0, 1, 0}) // a tool's record spans its model bindings
		seed(st, "memo|ma", "tool_a", 0.9)
		seed(st, "memo|ma", "tool_b", 0.2)
	}
	h := (&Bandit{Stats: st, Sig: "memo|lit", BudgetCalls: 150}).Hint(ctx, visible)
	if got := order(h); !slices.Equal(got, []string{"tool_b", "tool_a"}) || !strings.Contains(h, "tool_b 0.90 (40 calls)") {
		t.Fatalf("litigation signature: %q, want B above A over all its bindings", h)
	}
	if got := order((&Bandit{Stats: st, Sig: "memo|ma", BudgetCalls: 150}).Hint(ctx, visible)); !slices.Equal(got, []string{"tool_a", "tool_b"}) {
		t.Fatalf("another signature: %v, want A above B", got)
	}
}

// Trap: pinned tools come first, in order, whatever their record; verify_quote is never ranked.
func TestBanditNeverStarvesPinnedTools(t *testing.T) {
	st, pinned, skip := &MemStats{}, []string{"retrieve", "ledger", "committee", "finish"}, []string{"verify_quote"}
	for _, p := range pinned {
		seed(st, "s", p, make([]float64, 50)...)
	}
	seed(st, "s", "sweep", 1, 1, 1)
	h := (&Bandit{Stats: st, Sig: "s", BudgetCalls: 150, Pinned: pinned, Skip: skip}).Hint(ctx, []string{"sweep", "finish", "verify_quote", "committee", "ledger", "retrieve"})
	if !strings.Contains(h, ": retrieve, ledger, committee, finish; then sweep") || strings.Contains(h, "verify_quote") || !slices.Equal(order(h), []string{"sweep"}) {
		t.Fatalf("hint: %q", h)
	}
	_ = (&Bandit{Stats: st, Sig: "s", Skip: skip}).Record(ctx, Arm{Tool: "verify_quote"}, 0, true, Usage{})
	if rows, _ := st.Load(ctx, "s"); rows[Arm{Tool: "verify_quote"}].N != 0 {
		t.Fatal("verify_quote was recorded")
	}
}

func TestBanditExploresUntriedTool(t *testing.T) {
	st := &MemStats{}
	seed(st, "s", "tool_a", 1, 1, 1, 1)
	b := &Bandit{Stats: st, Sig: "s", BudgetCalls: 150}
	if got := order(b.Hint(ctx, []string{"tool_a", "tool_new"})); got[0] != "tool_new" {
		t.Fatalf("first hint: %v, want the untried tool first", got)
	}
	if got := order(b.Hint(ctx, []string{"tool_a", "tool_new"})); got[0] != "tool_a" {
		t.Fatalf("second hint: %v, want the untried tool first only once", got)
	}
}

// Same mean reward; the one that fails more ranks lower.
func TestBanditFailRatePenalised(t *testing.T) {
	st := &MemStats{}
	for i := 0; i < 5; i++ {
		_ = st.Add(ctx, "s", Arm{Tool: "flaky"}, Stat{2, 1.6, 1.28, 1, 2, 0}) // 1.0 and 0.6, one failed
		_ = st.Add(ctx, "s", Arm{Tool: "steady"}, Stat{2, 1.6, 1.28, 0, 2, 0})
	}
	if got := order((&Bandit{Stats: st, Sig: "s", BudgetCalls: 150}).Hint(ctx, []string{"flaky", "steady"})); got[0] != "steady" {
		t.Fatalf("%v: the failing tool ranked first", got)
	}
}

// Trap: a strong record for a model not configured for the role never binds it.
func TestBanditPicksOnlyConfiguredModels(t *testing.T) {
	st := &MemStats{}
	for i := 0; i < 30; i++ {
		_ = st.Add(ctx, "s", Arm{"extract_facts", "m-unconfigured"}, Stat{1, 1, 1, 0, 1, 0})
		_ = st.Add(ctx, "s", Arm{"extract_facts", "m-spec"}, Stat{1, 0.1, 0.01, 0, 1, 0})
		_ = st.Add(ctx, "s", Arm{"extract_facts", "m-lead"}, Stat{1, 0.9, 0.81, 0, 1, 0})
	}
	var replies []model.Reply
	for i := 0; i < 6; i++ {
		replies = append(replies, model.Reply{Text: `{"ok":true}`})
	}
	rt, f := rig(t, map[string][]model.Reply{"extract_facts": replies}, &counter{s: goSpec("retrieve")}, &Subagent{S: subSpec("extract_facts", "retrieve"), Prompt: "x"})
	rt.Bandit = &Bandit{Stats: st, Sig: "s", BudgetCalls: 150}
	for i := 0; i < 6; i++ {
		res, err := rt.Invoke(ctx, lead("extract_facts"), Call{}, "extract_facts", json.RawMessage(q), NewBudget(10, 1, 0), false, 0)
		if err != nil || res.Model != f.Calls()[i].Model {
			t.Fatal(err, res.Model)
		}
	}
	for _, r := range f.Calls() {
		if r.Model != "m-spec" && r.Model != "m-lead" {
			t.Fatalf("bound to %q, which is not configured for the specialist role", r.Model)
		}
	}
	if f.Calls()[5].Model != "m-lead" {
		t.Fatalf("the better configured model (m-lead) is not preferred: %q", f.Calls()[5].Model)
	}
	if got := (&Bandit{Off: true}).PickModel(ctx, "x", []string{"m-spec", "m-lead"}); got != "m-spec" {
		t.Fatalf("off: %q, want the role's own model", got)
	}
}

func TestSignatureBuckets(t *testing.T) {
	for docs, want := range map[int]string{1: "memo|litigation|US|m|1-3", 5: "memo|litigation|US|m|4-9", 12: "memo|litigation|US|m|10+"} {
		if got := Signature("memo", "litigation", "us-ny", "m", docs); got != want {
			t.Errorf("%d docs: %q, want %q", docs, got, want)
		}
	}
}
