// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package providers

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// qwenCfg is a minimal config with the default Qwen stack wired to a (dummy)
// primary endpoint.
func qwenCfg() *config.Config {
	c := &config.Config{}
	c.Model = config.ModelConfig{
		Stack:      "qwen",
		PrimaryURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		PrimaryKey: "qwen-test",
		Heavy:      "qwen-max", Mid: "qwen-plus", Light: "qwen-turbo", Vision: "qwen-vl-max",
	}
	return c
}

// TestStackTierSelection: the default stack routes tiers to Qwen models.
func TestStackTierSelection(t *testing.T) {
	c := qwenCfg()
	root := types.TierRoot
	tool := types.TierTool
	cases := []struct {
		name string
		p    routing.SelectParams
		want string
	}{
		{"root→heavy", routing.SelectParams{Tier: &root}, "qwen-max"},
		{"synthesis→heavy", routing.SelectParams{TaskType: routing.TaskSynthesis}, "qwen-max"},
		{"tool→light", routing.SelectParams{Tier: &tool}, "qwen-turbo"},
		{"descriptor→light", routing.SelectParams{TaskType: routing.TaskDescriptor}, "qwen-turbo"},
		{"default→mid", routing.SelectParams{TaskType: routing.TaskReasoning}, "qwen-plus"},
	}
	for _, tc := range cases {
		if got := routing.SelectModel(c, tc.p); got != tc.want {
			t.Errorf("%s: SelectModel = %q, want %q", tc.name, got, tc.want)
		}
	}
	if v := routing.Vision(c); v != "qwen-vl-max" {
		t.Errorf("Vision() = %q, want qwen-vl-max", v)
	}
}

// TestRegistryRouting: the stack's bare IDs route to the primary
// OpenAI-compatible provider. There is no Anthropic provider in this build.
func TestRegistryRouting(t *testing.T) {
	r := NewRegistry(qwenCfg())

	qwen, err := r.Get("qwen-vl-max")
	if err != nil {
		t.Fatalf("Get(qwen-vl-max) error: %v", err)
	}
	if _, ok := qwen.(*OllamaProvider); !ok {
		t.Errorf("qwen-vl-max should route to the OpenAI-compatible provider, got %T", qwen)
	}
}

// TestRegistryNoPrimaryErrors: with no primary endpoint configured, a bare
// model ID has nowhere to go and errors loudly (rather than silently picking a
// vendor).
func TestRegistryNoPrimaryErrors(t *testing.T) {
	r := NewRegistry(&config.Config{})
	if _, err := r.Get("qwen-max"); err == nil {
		t.Error("expected an error when no provider is configured")
	}
}
