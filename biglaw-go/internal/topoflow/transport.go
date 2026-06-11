// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

// LLMRequest is a typed structured-output request.
type LLMRequest struct {
	Model   string
	System  string
	User    string
	Schema  []string // required output field names
	Purpose string   // "invoke" | "agent" | "judge"
	Role    string
	Meta    map[string]any
}

// Transport is the typed LLM I/O seam. Complete returns (fields, tokens, error).
type Transport interface {
	Complete(req LLMRequest) (map[string]any, int, error)
}

// MockTransport returns deterministic structured output for tests (no network).
type MockTransport struct {
	Responder          func(LLMRequest) map[string]any
	Answer             string
	TokensPerCall      int
	EvaluatorCompletes bool
	FailSkills         map[string]bool
	Calls              []LLMRequest
}

// NewMockTransport returns a MockTransport with sensible defaults.
func NewMockTransport() *MockTransport {
	return &MockTransport{Answer: "ANSWER", TokensPerCall: 100, EvaluatorCompletes: true}
}

// Complete implements Transport.
func (m *MockTransport) Complete(req LLMRequest) (map[string]any, int, error) {
	m.Calls = append(m.Calls, req)
	if m.Responder != nil {
		out := m.Responder(req)
		tokens := m.TokensPerCall
		if t, ok := asFloat(out["_tokens"]); ok {
			tokens = int(t)
		}
		return out, tokens, nil
	}
	return m.deflt(req), m.TokensPerCall, nil
}

func (m *MockTransport) deflt(req LLMRequest) map[string]any {
	role := strings.ToLower(req.Role)
	out := map[string]any{}
	switch req.Purpose {
	case "agent":
		out["public"] = req.Role + ": progress"
		out["private"] = req.Role + ": note"
		out["q_desc"] = req.Role + " needs inputs"
		out["k_desc"] = req.Role + " provides expertise"
		if strings.Contains(role, "test") || strings.Contains(role, "verif") {
			out["verification"] = "supported"
		}
		if strings.Contains(role, "solver") || strings.Contains(role, "develop") {
			out["draft_answer"] = m.Answer
		}
		return out
	case "judge":
		return map[string]any{"scores": req.Meta["default_scores"]}
	}
	// invoke
	switch {
	case strings.HasPrefix(role, "solver"):
		out["draft_answer"] = m.Answer
	case role == "planner":
		out["goal"] = "solve"
		out["subproblem"] = "step 1"
	case role == "memory" || strings.HasPrefix(role, "web_search"):
		out["evidence"] = []any{"fact A", "fact B"}
	case strings.HasPrefix(role, "verifier"):
		out["verification"] = "supported"
	case role == "evaluator":
		out["complete"] = m.EvaluatorCompletes
	}
	if m.FailSkills[role] {
		out["_failed"] = true
	}
	return out
}

// defaultModelAliases maps TopoFlow's abstract model bindings to concrete
// provider model IDs. Override via AnthropicTransport.Aliases.
var defaultModelAliases = map[string]string{
	"haiku": "claude-haiku-4-5",
	"fast":  "claude-haiku-4-5",
	"mini":  "claude-sonnet-4-6",
}

// AnthropicTransport adapts a providers.Provider into a structured Transport by
// instructing JSON output and parsing the first JSON object from the response.
type AnthropicTransport struct {
	prov      providers.Provider
	maxTokens int
	// Aliases resolves abstract model names (e.g. "haiku") to real provider IDs.
	Aliases map[string]string
}

// NewAnthropicTransport wraps a provider (live path).
func NewAnthropicTransport(prov providers.Provider, maxTokens int) *AnthropicTransport {
	if maxTokens <= 0 {
		maxTokens = 2000
	}
	return &AnthropicTransport{prov: prov, maxTokens: maxTokens, Aliases: defaultModelAliases}
}

func (a *AnthropicTransport) resolveModel(m string) string {
	if a.Aliases != nil {
		if real, ok := a.Aliases[m]; ok {
			return real
		}
	}
	return m
}

// Complete implements Transport over an Anthropic-style provider.
func (a *AnthropicTransport) Complete(req LLMRequest) (map[string]any, int, error) {
	sys := req.System + "\nRespond with ONE JSON object containing the fields: " +
		strings.Join(req.Schema, ", ") + ". No prose outside the JSON."
	resp, err := a.prov.Chat(providers.ChatParams{
		Model:       a.resolveModel(req.Model),
		MaxTokens:   a.maxTokens,
		System:      sys,
		CacheSystem: true,
		Messages:    []providers.Message{{Role: "user", Content: req.User}},
	})
	if err != nil {
		return nil, 0, err
	}
	text := ""
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			text = blk.Text
			break
		}
	}
	js := extractJSON(text)
	out := map[string]any{}
	if js != "" {
		if err := json.Unmarshal([]byte(js), &out); err != nil {
			return nil, 0, fmt.Errorf("transport: bad JSON from model: %w", err)
		}
	}
	tokens := resp.Usage.InputTokens + resp.Usage.OutputTokens
	return out, tokens, nil
}
