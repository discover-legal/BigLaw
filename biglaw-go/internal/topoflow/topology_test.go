// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M5 — DyTopo generator + sync barrier.
package topoflow

import (
	"strings"
	"testing"
)

func scriptedResponder() func(LLMRequest) map[string]any {
	q := map[string]string{"Developer": "alpha", "Researcher": "gamma", "Tester": "beta", "Designer": "delta"}
	k := map[string]string{"Developer": "beta", "Researcher": "gamma", "Tester": "alpha", "Designer": "delta"}
	return func(req LLMRequest) map[string]any {
		r := req.Role
		out := map[string]any{
			"public": r + "-public", "private": r + "-private",
			"q_desc": q[r], "k_desc": k[r], "_tokens": 50.0,
		}
		if r == "Developer" || r == "Solver" {
			out["draft_answer"] = "42"
		}
		if r == "Tester" || r == "Verifier" {
			out["verification"] = "supported"
		}
		return out
	}
}

func dytopoAction(cfg Config, roundBucket int) Action {
	return DytopoAction(1, cfg.DytopoKIn, roundBucket)
}

func TestSyncBarrierExcludesNonNeighborPrivates(t *testing.T) {
	cfg := DefaultConfig()
	tx := &MockTransport{Responder: scriptedResponder()}
	ctx := TaskContext{TaskID: "m5", Prompt: "solve", Domain: "code"}
	res, err := DyTopoGenerator{}.Run(ctx, NewHandoffState(), RoleSetFor("code", cfg),
		dytopoAction(cfg, 0), tx, NewMockEmbedder(), cfg, -1)
	if err != nil {
		t.Fatal(err)
	}
	rounds := res.Subtrace["rounds"].([]map[string]any)
	if len(rounds) != cfg.RoundBuckets[0] {
		t.Fatalf("rounds_run=%d want %d", len(rounds), cfg.RoundBuckets[0])
	}
	mem := rounds[0]["memory_after"].(map[string]any)
	devMem := strings.Join(toStrings(mem["Developer"]), " ")
	if !strings.Contains(devMem, "[from Tester] Tester-private") {
		t.Errorf("Developer should have Tester's private: %q", devMem)
	}
	if strings.Contains(devMem, "Researcher-private") || strings.Contains(devMem, "Designer-private") {
		t.Errorf("non-neighbor private leaked into Developer memory: %q", devMem)
	}
}

func TestInnerRoundTokensAggregate(t *testing.T) {
	cfg := DefaultConfig()
	tx := &MockTransport{Responder: scriptedResponder()}
	ctx := TaskContext{TaskID: "m5", Prompt: "solve", Domain: "code"}
	res, _ := DyTopoGenerator{}.Run(ctx, NewHandoffState(), RoleSetFor("code", cfg),
		dytopoAction(cfg, 1), tx, NewMockEmbedder(), cfg, -1)
	if res.RoundsRun != 6 {
		t.Fatalf("rounds_run=%d want 6", res.RoundsRun)
	}
	if res.TotalTokens != 4*50*6 {
		t.Errorf("total_tokens=%d want %d", res.TotalTokens, 4*50*6)
	}
}

func TestDeterministicMergedHandoff(t *testing.T) {
	cfg := DefaultConfig()
	ctx := TaskContext{TaskID: "m5", Prompt: "solve", Domain: "code"}
	run := func() *HandoffState {
		res, _ := DyTopoGenerator{}.Run(ctx, NewHandoffState(), RoleSetFor("code", cfg),
			dytopoAction(cfg, 0), &MockTransport{Responder: scriptedResponder()}, NewMockEmbedder(), cfg, -1)
		return res.MergedHandoff
	}
	h1, h2 := run(), run()
	if h1.Get("draft_answer") != "42" || h1.Get("merged_answer") != "42" || h1.Get("verification") != "supported" {
		t.Errorf("unexpected merged handoff: %v", h1.Fields)
	}
	if len(h1.Fields) != len(h2.Fields) {
		t.Error("merged handoff not deterministic")
	}
}

func TestNestedSubtraceRecordsEveryRound(t *testing.T) {
	cfg := DefaultConfig()
	ctx := TaskContext{TaskID: "m5", Prompt: "solve", Domain: "code"}
	res, _ := DyTopoGenerator{}.Run(ctx, NewHandoffState(), RoleSetFor("code", cfg),
		dytopoAction(cfg, 2), &MockTransport{Responder: scriptedResponder()}, NewMockEmbedder(), cfg, -1)
	rounds := res.Subtrace["rounds"].([]map[string]any)
	if len(rounds) != 10 {
		t.Fatalf("rounds=%d want 10", len(rounds))
	}
	for i, rd := range rounds {
		if rd["t"].(int) != i {
			t.Errorf("round t=%v want %d", rd["t"], i)
		}
		if rd["edges"] == nil || rd["order"] == nil || rd["descriptors"] == nil {
			t.Error("round missing edges/order/descriptors")
		}
	}
}

func toStrings(v any) []string {
	if ss, ok := v.([]string); ok {
		return ss
	}
	return nil
}
