// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A base URL already carrying a non-/v1 version segment (Zhipu/Z.ai's
// /api/paas/v4) must get only /chat/completions appended — /v4/v1/... 404s
// every call. Plain and /v1-suffixed bases keep the classic path.
func TestVersionedBaseGetsBareCompletionsPath(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer ts.Close()

	cases := map[string]string{
		ts.URL + "/api/paas/v4": "/api/paas/v4/chat/completions",
		ts.URL + "/v1":          "/v1/chat/completions",
		ts.URL:                  "/v1/chat/completions",
	}
	for base, want := range cases {
		p := NewOpenAICompatProvider(base, "k")
		if _, err := p.Chat(ChatParams{Model: "m", MaxTokens: 8, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
		if gotPath != want {
			t.Fatalf("base %q: got path %q, want %q", base, gotPath, want)
		}
	}
}

// MODEL_THINKING drives Zhipu/Z.ai's hybrid-reasoning toggle; empty omits it.
func TestModelThinkingToggle(t *testing.T) {
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer ts.Close()
	p := NewOpenAICompatProvider(ts.URL+"/api/paas/v4", "k")
	call := func() {
		if _, err := p.Chat(ChatParams{Model: "glm-5.2", MaxTokens: 8, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MODEL_THINKING", "disabled")
	call()
	if !strings.Contains(gotBody, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("thinking field missing: %s", gotBody)
	}
	t.Setenv("MODEL_THINKING", "")
	call()
	if strings.Contains(gotBody, `"thinking"`) {
		t.Fatalf("thinking field should be omitted when unset: %s", gotBody)
	}
}
