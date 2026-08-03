// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package modeltier

import "testing"

func TestRate(t *testing.T) {
	cases := map[string]Tier{
		// local param-size ratings, with routing prefixes and quant suffixes
		"qwen2.5:1.5b":              Smol,
		"local:qwen2.5:3b":          Smol,
		"llama3.2:1b":               Smol,
		"qwen2.5:7b":                Mid,
		"ollama:qwen2.5:14b":        Mid,
		"llama3.1:8b-instruct-q4_0": Mid,
		"mixtral-8x7b":              Mid,
		"llama3.1:70b":              Based,
		"llama3.1:405b":             Gigachad,
		// cloud families, vendor-neutral
		"claude-haiku-4-5":            Based,
		"gpt-5-mini":                  Based,
		"gpt-4o-mini":                 Based,
		"gemini-2.5-flash":            Based,
		"qwen-turbo":                  Based,
		"qwen-plus":                   Based,
		"claude-sonnet-5":             Gigachad,
		"gpt-5.2":                     Gigachad,
		"gpt-4o":                      Gigachad,
		"gemini-2.5-pro":              Gigachad,
		"qwen-max":                    Gigachad,
		"qwen-vl-max":                 Gigachad,
		"deepseek-r1":                 Gigachad,
		"openrouter/x-ai/grok-4":      Gigachad,
		"claude-opus-5":               GalaxyBrain,
		"anthropic/claude-opus-4-6":   GalaxyBrain,
		"o3":                          GalaxyBrain,
		"o4-2026":                     GalaxyBrain,
		"gpt-5-pro":                   GalaxyBrain,
		// unknowns are conservatively mid
		"totally-new-model": Mid,
		"":                  Mid,
	}
	for id, want := range cases {
		if got := Rate(id); got != want {
			t.Errorf("Rate(%q) = %s, want %s", id, got, want)
		}
	}
}

func TestNeedsCompensators(t *testing.T) {
	for tier, want := range map[Tier]bool{
		Smol: true, Mid: true, Based: false, Gigachad: false, GalaxyBrain: false,
	} {
		if got := tier.NeedsCompensators(); got != want {
			t.Errorf("%s.NeedsCompensators() = %v, want %v", tier, got, want)
		}
	}
}

func TestLadderOrderMatchesTiers(t *testing.T) {
	ladder := Ladder()
	if len(ladder) != int(GalaxyBrain)+1 {
		t.Fatalf("ladder has %d rungs, want %d", len(ladder), int(GalaxyBrain)+1)
	}
	for i, info := range ladder {
		if info.Name != Tier(i).String() {
			t.Errorf("rung %d = %q, want %q", i, info.Name, Tier(i))
		}
		if info.Meme == "" || info.Guidance == "" || len(info.Examples) == 0 {
			t.Errorf("rung %q is missing meme/guidance/examples", info.Name)
		}
	}
}
