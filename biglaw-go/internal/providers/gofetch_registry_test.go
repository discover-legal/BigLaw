// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// TestRegistryWrapsLocalWithGoFetch verifies the env→config→registry path:
// GOFETCH_URL alone activates local inference and wraps it in the
// residency-controlled provider, and a "local:" model resolves to it.
func TestRegistryWrapsLocalWithGoFetch(t *testing.T) {
	f := &fakeGoFetch{}
	srv := f.server(t)

	t.Setenv("GOFETCH_URL", srv.URL)
	t.Setenv("LOCAL_INFERENCE_URL", "")
	t.Setenv("LOCAL_INFERENCE_MODEL", "alpha-4m")
	cfg := config.Load()
	if cfg.Local.LocalInferenceURL != srv.URL {
		t.Fatalf("GOFETCH_URL should backfill LocalInferenceURL, got %q", cfg.Local.LocalInferenceURL)
	}

	r := NewRegistry(cfg)
	p, err := r.Get("local:alpha-4m")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := p.(*GoFetchProvider); !ok {
		t.Fatalf("local provider is %T, want *GoFetchProvider", p)
	}
	if _, err := p.Chat(ChatParams{Model: "local:alpha-4m", Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if f.loads.Load() != 1 || f.chats.Load() != 1 {
		t.Fatalf("want 1 residency load + 1 chat through gofetch, got %d/%d", f.loads.Load(), f.chats.Load())
	}
}
