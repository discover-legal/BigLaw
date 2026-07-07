// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// okBody is a minimal OpenAI-compatible success reply with visible content.
func okBody(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, content)
}

// TestChatRetriesOn429ThenSucceeds: a 429 followed by a 200 is transparently
// retried, and the caller sees the successful body.
func TestChatRetriesOn429ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody("recovered")))
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	// Keep the test fast: the first backoff would otherwise be ~2s. Honor a short
	// Retry-After path instead by shrinking the client budget is not enough — instead
	// we rely on the small backoff being acceptable in CI. Use a Retry-After header.
	// (Handled below by TestChatHonorsRetryAfter; here we simply tolerate the wait.)
	start := time.Now()
	resp, err := p.Chat(ChatParams{Model: "m", MaxTokens: 16, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat errored: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("server saw %d calls, want 2 (one 429 + one success)", calls.Load())
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "recovered" {
		t.Fatalf("unexpected content: %+v", resp.Content)
	}
	if time.Since(start) < time.Second {
		t.Errorf("expected a backoff delay before the retry, elapsed=%v", time.Since(start))
	}
}

// TestChatHonorsRetryAfter: a Retry-After header dictates the wait, so a short value
// makes the retry prompt.
func TestChatHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody("ok")))
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	start := time.Now()
	if _, err := p.Chat(ChatParams{Model: "m", MaxTokens: 16, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat errored: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Errorf("Retry-After: 1 not honored, elapsed=%v (want ~1s)", elapsed)
	}
	// The 1s Retry-After must be honored, but must not have waited the ~2s exponential
	// backoff, proving the header (not the backoff) drove the wait.
	if elapsed > 1800*time.Millisecond {
		t.Errorf("waited %v; Retry-After should have driven a ~1s wait, not the backoff", elapsed)
	}
}

// TestChatNoRetryOn400: a non-retryable status returns immediately, no retry.
func TestChatNoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	if _, err := p.Chat(ChatParams{Model: "m", MaxTokens: 16, Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("expected an error for HTTP 400")
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d calls, want 1 (400 is not retried)", calls.Load())
	}
}

// TestThinkingDisabledRetry: an empty-content response carrying reasoning_content
// triggers exactly one retry with thinking:{"type":"disabled"}, and the second
// response's visible content is returned.
func TestThinkingDisabledRetry(t *testing.T) {
	t.Setenv("MODEL_THINKING", "") // default (not disabled) → retry eligible
	var calls atomic.Int32
	var sawThinkingDisabled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		if th, ok := body["thinking"].(map[string]interface{}); ok && th["type"] == "disabled" {
			sawThinkingDisabled.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			// Empty content, all budget spent on reasoning, length finish.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"lots of thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":200}}`))
			return
		}
		_, _ = w.Write([]byte(okBody("visible answer")))
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	resp, err := p.Chat(ChatParams{Model: "m", MaxTokens: 200, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat errored: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("server saw %d calls, want 2 (one empty + one disabled-thinking retry)", calls.Load())
	}
	if !sawThinkingDisabled.Load() {
		t.Error("retry did not carry thinking:{type:disabled}")
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "visible answer" {
		t.Fatalf("unexpected content after retry: %+v", resp.Content)
	}
}

// TestThinkingDisabledRetry_SkippedWhenDisabled: when MODEL_THINKING=disabled, an
// empty response is NOT retried (there's nothing left to fall back to).
func TestThinkingDisabledRetry_SkippedWhenDisabled(t *testing.T) {
	t.Setenv("MODEL_THINKING", "disabled")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"x"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":200}}`))
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(srv.URL, "k")
	if _, err := p.Chat(ChatParams{Model: "m", MaxTokens: 200, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat errored: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("server saw %d calls, want 1 (no retry when thinking already disabled)", calls.Load())
	}
}

// TestThinkingTokensMultiplier: on a versioned Zhipu-style base (…/v4) with thinking
// not disabled, max_tokens is inflated by MODEL_THINKING_TOKENS_FACTOR.
func TestThinkingTokensMultiplier(t *testing.T) {
	t.Setenv("MODEL_THINKING", "")
	t.Setenv("MODEL_THINKING_TOKENS_FACTOR", "4")
	var gotMax float64
	// A path ending in /v4 makes reVersionedBase match.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		if v, ok := body["max_tokens"].(float64); ok {
			gotMax = v
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(okBody("ok")))
	}))
	defer srv.Close()

	// Force the versioned-base branch by appending /api/paas/v4 to the test URL.
	p := NewOpenAICompatProvider(srv.URL+"/api/paas/v4", "k")
	// Point the provider back at the test server root for the actual POST: the
	// versioned base changes the completions path to /chat/completions, which the
	// httptest mux serves at any path.
	p.baseURL = srv.URL + "/api/paas/v4"
	if _, err := p.Chat(ChatParams{Model: "m", MaxTokens: 200, Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat errored: %v", err)
	}
	if gotMax != 800 {
		t.Errorf("max_tokens = %v, want 800 (200 × factor 4)", gotMax)
	}
}

// ─── Rate limiter ──────────────────────────────────────────────────────────────

// TestRateLimiterThrottles: a limiter at 120/min (2/sec) admits a burst then paces
// subsequent acquires at ~0.5s each.
func TestRateLimiterThrottles(t *testing.T) {
	rl := newRateLimiter(120) // 2 per second
	// Drain the initial burst without timing.
	for i := 0; i < 120; i++ {
		rl.acquire()
	}
	start := time.Now()
	rl.acquire() // must wait ~0.5s for the next token
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond {
		t.Errorf("acquire returned in %v; a 2/sec limiter should pace ~0.5s after the burst", elapsed)
	}
}

// TestRateLimiterNoOpAtZero: a zero (or negative) rate yields a nil limiter whose
// acquire is an instant no-op — preserving current behavior when unconfigured.
func TestRateLimiterNoOpAtZero(t *testing.T) {
	rl := newRateLimiter(0)
	if rl != nil {
		t.Fatal("newRateLimiter(0) should return nil (unlimited)")
	}
	start := time.Now()
	for i := 0; i < 1000; i++ {
		rl.acquire() // nil-safe no-op
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Errorf("nil limiter acquire should be instant, took %v", time.Since(start))
	}
}
