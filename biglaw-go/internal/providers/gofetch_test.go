// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// fakeGoFetch mimics the daemon's control API and OpenAI passthrough.
type fakeGoFetch struct {
	loads    atomic.Int64
	chats    atomic.Int64
	failLoad bool
}

func (f *fakeGoFetch) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/vram/load", func(w http.ResponseWriter, r *http.Request) {
		f.loads.Add(1)
		if f.failLoad {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"unknown model"}`)
			return
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		fmt.Fprintf(w, `{"active":%q}`, req["model"])
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.chats.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestGoFetchProvider(url string) *GoFetchProvider {
	return NewGoFetchProvider(NewOpenAICompatProvider(url, "local"), url)
}

func TestGoFetchChatEnsuresResidency(t *testing.T) {
	f := &fakeGoFetch{}
	srv := f.server(t)
	p := newTestGoFetchProvider(srv.URL)

	resp, err := p.Chat(ChatParams{Model: "local:alpha", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content[0].Text != "ok" {
		t.Fatalf("unexpected content %q", resp.Content[0].Text)
	}
	if f.loads.Load() != 1 || f.chats.Load() != 1 {
		t.Fatalf("want 1 load + 1 chat, got %d/%d", f.loads.Load(), f.chats.Load())
	}

	// Same model again: residency call is deduped, chat still goes through.
	if _, err := p.Chat(ChatParams{Model: "local:alpha", Messages: []Message{{Role: "user", Content: "again"}}}); err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	if f.loads.Load() != 1 || f.chats.Load() != 2 {
		t.Fatalf("want deduped load (1) + 2 chats, got %d/%d", f.loads.Load(), f.chats.Load())
	}

	// Different model: residency requested again.
	if _, err := p.Chat(ChatParams{Model: "local:beta", Messages: []Message{{Role: "user", Content: "swap"}}}); err != nil {
		t.Fatalf("Chat 3: %v", err)
	}
	if f.loads.Load() != 2 {
		t.Fatalf("want 2 loads after model switch, got %d", f.loads.Load())
	}
}

func TestGoFetchChatFailsOpenOnResidencyError(t *testing.T) {
	f := &fakeGoFetch{failLoad: true}
	srv := f.server(t)
	p := newTestGoFetchProvider(srv.URL)

	resp, err := p.Chat(ChatParams{Model: "local:alpha", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat should fail open, got: %v", err)
	}
	if resp.Content[0].Text != "ok" {
		t.Fatalf("unexpected content %q", resp.Content[0].Text)
	}
	// Failed residency is not cached: the next call retries the control plane.
	if _, err := p.Chat(ChatParams{Model: "local:alpha", Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat 2: %v", err)
	}
	if f.loads.Load() != 2 {
		t.Fatalf("want residency retried after failure, got %d loads", f.loads.Load())
	}
}

// TestGoFetchLiveE2E drives a real GoFetch daemon. Skipped unless
// GOFETCH_E2E_URL is set, e.g.
//
//	GOFETCH_E2E_URL=http://127.0.0.1:8180 GOFETCH_E2E_MODEL=alpha-4m go test ./internal/providers -run LiveE2E -v
func TestGoFetchLiveE2E(t *testing.T) {
	url := os.Getenv("GOFETCH_E2E_URL")
	if url == "" {
		t.Skip("GOFETCH_E2E_URL not set")
	}
	model := os.Getenv("GOFETCH_E2E_MODEL")
	if model == "" {
		model = "alpha-4m"
	}
	p := newTestGoFetchProvider(url)

	if err := p.Warm(model, ""); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	resp, err := p.Chat(ChatParams{Model: "local:" + model, Messages: []Message{{Role: "user", Content: "live e2e ping"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text == "" {
		t.Fatalf("empty completion from live daemon")
	}
	t.Logf("live completion: %q", resp.Content[0].Text)
}
