// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package modeltier

import "testing"

func TestRate(t *testing.T) {
	cases := map[string]Tier{
		// local param-size ratings, with routing prefixes and quant suffixes
		"qwen2.5:1.5b":              TierD,
		"local:qwen2.5:3b":          TierD,
		"llama3.2:1b":               TierD,
		"qwen2.5:7b":                TierC,
		"ollama:qwen2.5:14b":        TierC,
		"llama3.1:8b-instruct-q4_0": TierC,
		"mixtral-8x7b":              TierC,
		"llama3.1:70b":              TierB,
		"llama3.1:405b":             TierA,
		// cloud families, vendor-neutral
		"claude-haiku-4-5":          TierB,
		"gpt-5-mini":                TierB,
		"gpt-4o-mini":               TierB,
		"gemini-2.5-flash":          TierB,
		"qwen-turbo":                TierB,
		"qwen-plus":                 TierB,
		"claude-sonnet-5":           TierA,
		"gpt-5.2":                   TierA,
		"gpt-4o":                    TierA,
		"gemini-2.5-pro":            TierA,
		"qwen-max":                  TierA,
		"qwen-vl-max":               TierA,
		"deepseek-r1":               TierA,
		"openrouter/x-ai/grok-4":    TierA,
		"claude-opus-5":             TierS,
		"anthropic/claude-opus-4-6": TierS,
		"o3":                        TierS,
		"o4-2026":                   TierS,
		"gpt-5-pro":                 TierS,
		// unknowns are conservatively C
		"totally-new-model": TierC,
		"":                  TierC,
	}
	for id, want := range cases {
		if got := Rate(id); got != want {
			t.Errorf("Rate(%q) = %s, want %s", id, got, want)
		}
	}
}

func TestOrderingAndNames(t *testing.T) {
	if !(TierD < TierC && TierC < TierB && TierB < TierA && TierA < TierS) {
		t.Fatal("tier ordering must ascend D < C < B < A < S")
	}
	for tier, name := range map[Tier]string{
		TierS: "S", TierA: "A", TierB: "B", TierC: "C", TierD: "D",
	} {
		if tier.String() != name {
			t.Errorf("%d.String() = %q, want %q", tier, tier.String(), name)
		}
	}
}

func TestNeedsCompensators(t *testing.T) {
	for tier, want := range map[Tier]bool{
		TierD: true, TierC: true, TierB: false, TierA: false, TierS: false,
	} {
		if got := tier.NeedsCompensators(); got != want {
			t.Errorf("%s.NeedsCompensators() = %v, want %v", tier, got, want)
		}
	}
}

func TestLadderIsTopToBottom(t *testing.T) {
	ladder := Ladder()
	want := []string{"S", "A", "B", "C", "D"}
	if len(ladder) != len(want) {
		t.Fatalf("ladder has %d rows, want %d", len(ladder), len(want))
	}
	for i, info := range ladder {
		if info.Name != want[i] {
			t.Errorf("row %d = %q, want %q", i, info.Name, want[i])
		}
		if info.Blurb == "" || info.Guidance == "" || len(info.Examples) == 0 {
			t.Errorf("row %q is missing blurb/guidance/examples", info.Name)
		}
	}
}
