// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Provider resilience: retry/backoff on transient HTTP failures, a client-side
// global rate limiter for the model-call path, and thinking-aware output helpers.
// All of this sits in front of the single OpenAI-compatible provider (ollama.go) —
// there is no Anthropic/Claude provider in this build (see registry.go), so this is
// the one place the whole platform's model traffic is shaped.
package providers

import (
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── Retry / backoff ──────────────────────────────────────────────────────────

const (
	// maxProviderRetries is the number of RETRIES (beyond the first attempt) granted
	// to a transient failure. 3 → up to 4 total attempts.
	maxProviderRetries = 3
	// maxBackoff caps a single backoff sleep. Hosted endpoints (Z.ai) were returning
	// 261–489 429s/minute under load; an unbounded backoff would stall a whole round.
	maxBackoff = 30 * time.Second
)

// retryableStatus reports whether an HTTP status is a transient failure worth
// retrying: rate limiting (429), Anthropic/Z.ai overload (529), and the 5xx gateway
// family (500/502/503). A 4xx other than 429 is a caller error — never retried.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		529:                            // Anthropic/Z.ai "overloaded"
		return true
	}
	return false
}

// parseRetryAfter reads a Retry-After header in either of its RFC 7231 forms —
// delta-seconds ("120") or an HTTP date — and returns the wait it implies. ok is
// false when the header is absent or unparseable (caller falls back to backoff).
func parseRetryAfter(h string) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// backoffDuration is the exponential backoff for a given retry attempt (0-indexed):
// 2s, 4s, 8s … capped at maxBackoff, plus up to 25% jitter so a fleet of agents that
// all got 429'd at once don't retry in lockstep and re-thundering-herd the endpoint.
func backoffDuration(attempt int) time.Duration {
	base := 2 * time.Second
	for i := 0; i < attempt; i++ {
		base *= 2
		if base >= maxBackoff {
			base = maxBackoff
			break
		}
	}
	if base > maxBackoff {
		base = maxBackoff
	}
	jitter := time.Duration(rand.Int63n(int64(base)/4 + 1))
	return base + jitter
}

// ─── Thinking-aware output helpers ────────────────────────────────────────────

// thinkingNotDisabled reports whether hybrid reasoning is enabled or left at the
// provider default — i.e. MODEL_THINKING is anything other than an explicit
// "disabled". Z.ai bills reasoning into the completion budget and emits it BEFORE
// the visible answer, so a 200-token descriptor call can spend its whole cap on
// reasoning and return empty content; the multiplier and the one-shot disabled
// retry both key off this.
func thinkingNotDisabled() bool {
	return strings.TrimSpace(os.Getenv("MODEL_THINKING")) != "disabled"
}

// thinkingTokensFactor is the multiplier applied to max_tokens on a versioned
// Zhipu-style endpoint when thinking is not explicitly disabled, so a structured
// output survives the reasoning tokens consumed ahead of it. Env-tunable via
// MODEL_THINKING_TOKENS_FACTOR (default 4); values < 1 fall back to the default.
func thinkingTokensFactor() int {
	if v := strings.TrimSpace(os.Getenv("MODEL_THINKING_TOKENS_FACTOR")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			return n
		}
	}
	return 4
}

// ─── Client-side global rate limiter ──────────────────────────────────────────

// rateLimiter is a token bucket bounding model calls to a fixed per-minute rate.
// The verification pipeline alone issues 11K+ calls per task and self-DoSes a
// hosted endpoint's per-minute quota — starving the NEXT round with 429s. This
// throttles the whole model-call path to stay under a configured ceiling.
type rateLimiter struct {
	mu     sync.Mutex
	perMin float64
	tokens float64
	max    float64
	last   time.Time
}

// newRateLimiter builds a limiter capped at perMin calls/minute. perMin <= 0 returns
// nil — a nil limiter's acquire is a no-op, so the default (0) preserves current
// behavior exactly.
func newRateLimiter(perMin int) *rateLimiter {
	if perMin <= 0 {
		return nil
	}
	return &rateLimiter{
		perMin: float64(perMin),
		tokens: float64(perMin), // start full: allow an initial burst up to the ceiling
		max:    float64(perMin),
		last:   time.Now(),
	}
}

// acquire blocks until a token is available, then consumes it. Nil-safe (no-op).
func (r *rateLimiter) acquire() {
	if r == nil {
		return
	}
	for {
		r.mu.Lock()
		now := time.Now()
		r.tokens += now.Sub(r.last).Seconds() * r.perMin / 60.0
		r.last = now
		if r.tokens > r.max {
			r.tokens = r.max
		}
		if r.tokens >= 1 {
			r.tokens--
			r.mu.Unlock()
			return
		}
		deficit := 1 - r.tokens
		wait := time.Duration(deficit / r.perMin * 60.0 * float64(time.Second))
		r.mu.Unlock()
		if wait <= 0 {
			wait = time.Millisecond
		}
		time.Sleep(wait)
	}
}

// globalRateLimiter returns the process-wide model-call limiter, built once from
// PROVIDER_MAX_CALLS_PER_MIN (default 0 = off). It is shared across every
// OpenAI-compatible provider instance so the local and primary stacks draw on one
// budget when they point at the same quota.
var (
	globalLimiterOnce sync.Once
	globalLimiter     *rateLimiter
)

func globalRateLimiter() *rateLimiter {
	globalLimiterOnce.Do(func() {
		n := 0
		if v := strings.TrimSpace(os.Getenv("PROVIDER_MAX_CALLS_PER_MIN")); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil {
				n = parsed
			}
		}
		globalLimiter = newRateLimiter(n)
	})
	return globalLimiter
}
