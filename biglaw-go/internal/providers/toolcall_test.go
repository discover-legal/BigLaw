// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercises the full OpenAI-compatible tool-calling round-trip through Chat():
// tools are sent, a tool_calls response becomes tool_use blocks + StopToolUse,
// and a follow-up turn re-serializes the assistant tool_calls + tool result.
// This is the path that was silently broken — tools were never sent and
// tool_calls never parsed, so qwen agents could not read documents at all.
func TestOllamaToolCallingRoundTrip(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"searching","tool_calls":[{"id":"call_1","type":"function","function":{"name":"search_knowledge","arguments":"{\"query\":\"violations\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "")

	resp, err := p.Chat(ChatParams{
		Model:     "qwen2.5:14b",
		MaxTokens: 100,
		Tools:     []ToolParam{{Name: "search_knowledge", Description: "search", InputSchema: map[string]interface{}{"type": "object"}}},
		Messages:  []Message{{Role: "user", Content: "find violations"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != StopToolUse {
		t.Fatalf("want StopToolUse, got %q", resp.StopReason)
	}
	var tu *ContentBlock
	for i := range resp.Content {
		if resp.Content[i].Type == BlockToolUse {
			tu = &resp.Content[i]
		}
	}
	if tu == nil {
		t.Fatal("no tool_use block parsed from response")
	}
	if tu.Name != "search_knowledge" || tu.ID != "call_1" {
		t.Errorf("bad tool_use block: %+v", tu)
	}
	if tu.Input["query"] != "violations" {
		t.Errorf("tool arguments not parsed: %+v", tu.Input)
	}
	if !strings.Contains(gotBody, `"name":"search_knowledge"`) {
		t.Errorf("tools not sent in request: %s", gotBody)
	}

	// Follow-up turn: assistant tool_use + the tool result must serialize as
	// tool_calls + a role:"tool" message linked by tool_call_id.
	_, err = p.Chat(ChatParams{
		Model:     "qwen2.5:14b",
		MaxTokens: 100,
		Messages: []Message{
			{Role: "user", Content: "find violations"},
			{Role: "assistant", Content: []ContentBlock{{Type: BlockToolUse, ID: "call_1", Name: "search_knowledge", Input: map[string]interface{}{"query": "violations"}}}},
			{Role: "user", Content: []ContentBlock{{Type: BlockToolResult, ToolUseID: "call_1", Content: "found: Section 10(b)"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"tool_calls"`, `"role":"tool"`, `"tool_call_id":"call_1"`, `found: Section 10(b)`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("follow-up request missing %s in body: %s", want, gotBody)
		}
	}
}
