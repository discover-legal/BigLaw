// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// M9 — Live wiring. The provider-backed transport adapter is exercised offline
// with a fake providers.Provider; the real network run is a separate, gated path.
package topoflow

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

// erroringTransport always fails — simulates a live transport hitting a bad key
// or rate limit. The harness must degrade gracefully, not panic.
type erroringTransport struct{}

func (erroringTransport) Complete(LLMRequest) (map[string]any, int, error) {
	return nil, 0, errString("simulated transport failure")
}

type errString string

func (e errString) Error() string { return string(e) }

func TestHarnessSurvivesTransportErrors(t *testing.T) {
	rep, err := RunSuite(DefaultConfig(), SuiteOptions{Transport: erroringTransport{}, Epochs: 1})
	if err != nil {
		t.Fatalf("RunSuite should not error on transport failures: %v", err)
	}
	if len(rep.Arms) != 8 {
		t.Fatalf("expected 8 arms even under transport failure, got %d", len(rep.Arms))
	}
	// failed trajectories => zero quality, no panic
	for name, a := range rep.Arms {
		if a.MeanQuality != 0 {
			t.Errorf("arm %s should be zero-quality under total transport failure, got %v", name, a.MeanQuality)
		}
	}
}

// fakeProvider returns a canned JSON text block (no network).
type fakeProvider struct{ text string }

func (f fakeProvider) Chat(p providers.ChatParams) (*providers.ChatResponse, error) {
	return &providers.ChatResponse{
		Content: []providers.ContentBlock{{Type: providers.BlockText, Text: f.text}},
		Usage:   providers.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

func TestAnthropicTransportParsesJSON(t *testing.T) {
	tx := NewAnthropicTransport(fakeProvider{text: `here: {"draft_answer":"42","q_desc":"need"} done`}, 1000)
	fields, tokens, err := tx.Complete(LLMRequest{Model: "claude-haiku-4-5", System: "s", User: "u",
		Schema: []string{"draft_answer", "q_desc"}, Purpose: "agent", Role: "Solver"})
	if err != nil {
		t.Fatal(err)
	}
	if fields["draft_answer"] != "42" || fields["q_desc"] != "need" {
		t.Errorf("parsed fields wrong: %v", fields)
	}
	if tokens != 15 {
		t.Errorf("tokens=%d want 15", tokens)
	}
}

func TestAnthropicTransportEndToEnd(t *testing.T) {
	// A provider-backed transport drives a full linear trajectory offline.
	cfg := DefaultConfig()
	cfg.TopoModes = []string{"linear"}
	g := NewPolicyGraph(cfg)
	tx := NewAnthropicTransport(fakeProvider{text: `{"draft_answer":"4","complete":true}`}, 1000)
	traj, err := RunTask(
		TaskContext{TaskID: "live", Prompt: "2+2?", Domain: "math", GroundTruth: "4"},
		cfg, g, RunOptions{Transport: tx, Quality: GroundTruthQ{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if traj.Report == nil {
		t.Fatal("expected a report")
	}
}

func TestWebSearchCellUsesProvider(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TopoModes = []string{"linear"}
	g := NewPolicyGraph(cfg)
	tx := &MockTransport{Responder: func(req LLMRequest) map[string]any {
		if req.Role == "evaluator" {
			return map[string]any{"complete": true, "_tokens": 10.0}
		}
		return map[string]any{"_tokens": 10.0}
	}}
	used := false
	sel := func(sig Signature, legal []Action) Action {
		for _, a := range legal {
			if a.Kind == "invoke" && a.Skill == "web_search_exa" {
				used = true
				return a
			}
		}
		return evalOrFirst(legal)
	}
	_, err := RunTask(TaskContext{TaskID: "ws", Prompt: "find facts", Domain: "advisory"},
		cfg, g, RunOptions{Transport: tx, SelectFn: sel, SearchProvider: NewMockSearchProvider()})
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("web_search cell should have been selected and used the provider")
	}
}
