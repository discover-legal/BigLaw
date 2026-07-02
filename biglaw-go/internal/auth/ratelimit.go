// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Sliding-window per-IP rate limiter for the auth endpoints (20 req/min in
// production) — slows credential brute-force without an external store.
// Stale keys are swept opportunistically so the map cannot grow unboundedly
// under a wave of unique attacker IPs.

package auth

import (
	"sync"
	"time"
)

// RateLimiter is a sliding-window counter keyed on an arbitrary string
// (the client IP for auth routes). Safe for concurrent use.
type RateLimiter struct {
	mu        sync.Mutex
	limit     int
	window    time.Duration
	hits      map[string][]time.Time
	lastSweep time.Time
}

// NewRateLimiter allows at most limit calls per key within window.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:     limit,
		window:    window,
		hits:      map[string][]time.Time{},
		lastSweep: time.Now(),
	}
}

// Allow records a hit for key and reports whether it is within the limit.
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Opportunistic sweep: every few windows drop keys with no live hits.
	if now.Sub(rl.lastSweep) > 5*rl.window {
		for k, hs := range rl.hits {
			if len(pruneHits(hs, cutoff)) == 0 {
				delete(rl.hits, k)
			}
		}
		rl.lastSweep = now
	}

	live := pruneHits(rl.hits[key], cutoff)
	if len(live) >= rl.limit {
		rl.hits[key] = live
		return false
	}
	rl.hits[key] = append(live, now)
	return true
}

func pruneHits(hits []time.Time, cutoff time.Time) []time.Time {
	var live []time.Time
	for _, t := range hits {
		if t.After(cutoff) {
			live = append(live, t)
		}
	}
	return live
}
