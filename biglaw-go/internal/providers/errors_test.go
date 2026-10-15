// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
)

func TestModelFailureWrapsUpstreamAsUnavailable(t *testing.T) {
	err := ModelFailure("step", errors.New("HTTP 500: boom"))
	if !errors.Is(err, ErrModelUnavailable) || errors.Is(err, ErrNoProvider) {
		t.Fatalf("want ErrModelUnavailable only, got %v", err)
	}
	if !IsModelFailure(err) {
		t.Fatal("IsModelFailure = false")
	}
	if ModelFailure("step", nil) == nil || !errors.Is(ModelFailure("step", nil), ErrModelUnavailable) {
		t.Fatal("nil cause must still be a model failure")
	}
}

func TestModelFailureKeepsNoProvider(t *testing.T) {
	base := errors.Join(ErrNoProvider, errors.New("x"))
	err := ModelFailure("step", base)
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("ErrNoProvider lost: %v", err)
	}
	if errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("a missing provider must not also read as an upstream failure: %v", err)
	}
}

func TestRegistryGetUnconfiguredIsErrNoProvider(t *testing.T) {
	r := NewRegistry(&config.Config{})
	if _, err := r.Get("qwen-plus"); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("primary: want ErrNoProvider, got %v", err)
	}
	if _, err := r.Get("local:qwen2.5:14b"); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("local: want ErrNoProvider, got %v", err)
	}
}

// An endpoint that demands credentials when none are configured is a missing
// configuration (503), not an upstream outage (502).
func TestChatUnauthorizedWithoutKeyIsErrNoProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := NewOpenAICompatProvider(srv.URL, "").Chat(ChatParams{Model: "m", MaxTokens: 5, Messages: []Message{{Role: "user", Content: "hi"}}})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("no key + 401: want ErrNoProvider, got %v", err)
	}
	_, err = NewOpenAICompatProvider(srv.URL, "sk-wrong").Chat(ChatParams{Model: "m", MaxTokens: 5, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil || errors.Is(err, ErrNoProvider) {
		t.Fatalf("configured key + 401 must stay an ordinary upstream error, got %v", err)
	}
}
