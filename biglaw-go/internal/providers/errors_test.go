// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func chatOnce(p *OllamaProvider, ctx context.Context) error {
	_, err := p.Chat(ChatParams{Ctx: ctx, Model: "m", MaxTokens: 5, Messages: []Message{{Role: "user", Content: "hi"}}})
	return err
}

// A 429 that survives retries is ErrModelRateLimited carrying Retry-After.
func TestChat429IsRateLimitedWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":"rate"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	err := chatOnce(NewOpenAICompatProvider(srv.URL, "k"), nil)
	var rl *RateLimitError
	if !errors.Is(err, ErrModelRateLimited) || !errors.As(err, &rl) || rl.RetryAfter != time.Second {
		t.Fatalf("want RateLimitError{RetryAfter:1s}, got %v", err)
	}
	if !errors.Is(ModelFailure("step", err), ErrModelRateLimited) || errors.Is(ModelFailure("step", err), ErrModelUnavailable) {
		t.Fatal("ModelFailure must keep the rate-limit classification")
	}
}

func TestChatTimeoutIsErrModelTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	p := NewOpenAICompatProvider(srv.URL, "k")
	p.client.Timeout = 150 * time.Millisecond
	if err := chatOnce(p, nil); !errors.Is(err, ErrModelTimeout) {
		t.Fatalf("want ErrModelTimeout, got %v", err)
	}
}

// Cancelling the caller's context aborts the in-flight request promptly.
func TestChatHonoursContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := chatOnce(NewOpenAICompatProvider(srv.URL, "k"), ctx)
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("want prompt context.Canceled, got %v after %s", err, time.Since(start))
	}
}

func TestUnconfiguredAndDescribe(t *testing.T) {
	_, err := Unconfigured(errors.New("unset")).Chat(ChatParams{})
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("Unconfigured: want ErrNoProvider, got %v", err)
	}
	if got := Describe(ModelFailure("s", errors.New("http://10.0.0.5:8080 secret body"))); strings.Contains(got, "10.0.0.5") || got == "" {
		t.Fatalf("Describe leaked or empty: %q", got)
	}
}
