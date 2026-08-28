// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestChatRetriesWithReasoningEffortNone pins the provider-compat fallback:
// a reasoning-default model that 400s on function tools + reasoning_effort
// (e.g. OpenAI gpt-5.6-luna — even when the request never sent the parameter)
// gets one retry with an explicit reasoning_effort of "none".
func TestChatRetriesWithReasoningEffortNone(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["reasoning_effort"] != "none" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Function tools with reasoning_effort are not supported for this model. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","param":"reasoning_effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	// The pipeline's usual agent call sends NO reasoning_effort — the model's
	// implicit default still triggers the rejection.
	resp, err := p.Chat(ChatParams{
		Model:    "some-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []ToolParam{{Name: "t", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Chat should succeed via the effort=none retry, got: %v", err)
	}
	if resp.Content[0].Text != "ok" {
		t.Fatalf("unexpected content %q", resp.Content[0].Text)
	}
	if calls != 2 {
		t.Fatalf("want exactly 2 calls (reject + none retry), got %d", calls)
	}

	// An explicitly requested effort is also downgraded to "none" on rejection.
	calls = 0
	if _, err := p.Chat(ChatParams{
		Model:           "some-model",
		ReasoningEffort: "high",
		Messages:        []Message{{Role: "user", Content: "hi"}},
		Tools:           []ToolParam{{Name: "t", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}},
	}); err != nil {
		t.Fatalf("explicit-effort call should also recover, got: %v", err)
	}
	if calls != 2 {
		t.Fatalf("want 2 calls for explicit-effort case, got %d", calls)
	}
}

// A 400 unrelated to reasoning_effort is not retried.
func TestChatDoesNotRetryUnrelated400(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"model not found"}}`)
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	_, err := p.Chat(ChatParams{
		Model:           "nope",
		ReasoningEffort: "medium",
		Messages:        []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want error")
	}
	if calls != 1 {
		t.Fatalf("unrelated 400 must not be retried, got %d calls", calls)
	}
}

// Once learned, reasoning_effort=none is sent up front — no doubled call.
func TestEffortNoneIsSticky(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["reasoning_effort"] != "none" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"set reasoning_effort to 'none'","param":"reasoning_effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`)
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	msg := []Message{{Role: "user", Content: "hi"}}
	tools := []ToolParam{{Name: "t", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}}
	if _, err := p.Chat(ChatParams{Model: "m", Messages: msg, Tools: tools}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("first call should learn via retry (2 calls), got %d", calls)
	}
	if _, err := p.Chat(ChatParams{Model: "m", Messages: msg, Tools: tools}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("second call should send none up front (3 total), got %d", calls)
	}
}
