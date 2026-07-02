// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for the sliding-window auth rate limiter.

package auth

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToLimit(t *testing.T) {
	rl := NewRateLimiter(3, 100*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !rl.Allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.Allow("1.2.3.4") {
		t.Error("request over the limit should be denied")
	}
	// A different key has its own window.
	if !rl.Allow("5.6.7.8") {
		t.Error("independent key should be allowed")
	}
}

func TestRateLimiterWindowSlides(t *testing.T) {
	rl := NewRateLimiter(2, 50*time.Millisecond)
	rl.Allow("ip")
	rl.Allow("ip")
	if rl.Allow("ip") {
		t.Fatal("third request inside window should be denied")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow("ip") {
		t.Error("request after the window slid should be allowed")
	}
}
