// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package strutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"exactly max", "hello", 5, "hello"},
		{"ascii cut", "hello world", 5, "hello"},
		{"empty", "", 5, ""},
		{"zero max", "hello", 0, ""},
		{"negative max", "hello", -3, ""},
		// "§" is 2 bytes (0xC2 0xA7): cutting at byte 1 must back off to 0.
		{"mid-rune two-byte", "§x", 1, ""},
		{"after two-byte rune", "§x", 2, "§"},
		// "—" (em dash) is 3 bytes; cut inside it.
		{"mid-rune three-byte", "a—b", 2, "a"},
		{"mid-rune three-byte deeper", "a—b", 3, "a"},
		{"whole three-byte rune", "a—b", 4, "a—"},
		// "🜲" is 4 bytes.
		{"mid-rune four-byte", "🜲", 3, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Truncate(c.in, c.max)
			if got != c.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("Truncate(%q, %d) = %q is not valid UTF-8", c.in, c.max, got)
			}
			if len(got) > c.max && c.max >= 0 {
				t.Errorf("Truncate(%q, %d) = %q exceeds byte budget", c.in, c.max, got)
			}
		})
	}

	// Every cut point over a mixed string must yield valid UTF-8 within budget.
	mixed := strings.Repeat("a§—🜲", 8)
	for max := 0; max <= len(mixed)+1; max++ {
		got := Truncate(mixed, max)
		if !utf8.ValidString(got) {
			t.Fatalf("Truncate(mixed, %d) produced invalid UTF-8", max)
		}
		if len(got) > max {
			t.Fatalf("Truncate(mixed, %d) returned %d bytes", max, len(got))
		}
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
	// Conservative: never under-counts vs a ~4-chars/token English baseline.
	if got := EstimateTokens(strings.Repeat("a", 700)); got < 700/4 {
		t.Errorf("EstimateTokens(700 chars) = %d, want >= %d (conservative)", got, 700/4)
	}
	// Counts runes, not bytes: a 2-byte rune weighs the same as a 1-byte one.
	if EstimateTokens(strings.Repeat("§", 70)) != EstimateTokens(strings.Repeat("a", 70)) {
		t.Error("EstimateTokens must count runes, not bytes")
	}
}

func TestTruncateToTokens(t *testing.T) {
	const src = "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu"

	if got := TruncateToTokens(src, 0); got != src {
		t.Errorf("non-positive budget should return unchanged, got %q", got)
	}
	if got := TruncateToTokens("short", 100); got != "short" {
		t.Errorf("fitting string should return unchanged, got %q", got)
	}

	for _, budget := range []int{2, 3, 5, 8} {
		got := TruncateToTokens(src, budget)
		if !strings.HasPrefix(src, got) {
			t.Fatalf("budget %d: %q is not a verbatim prefix of src", budget, got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("budget %d: %q is not valid UTF-8", budget, got)
		}
		if EstimateTokens(got) > budget {
			t.Fatalf("budget %d: %q estimates %d tokens (over budget)", budget, got, EstimateTokens(got))
		}
		// Never mid-word: when shortened, the next source char is whitespace.
		if got != src && got != "" {
			if next := src[len(got):]; !strings.ContainsAny(next[:1], " \t\n\r") {
				t.Fatalf("budget %d: %q cut mid-word (next %q)", budget, got, next[:1])
			}
		}
	}

	// Multibyte content stays valid UTF-8, a verbatim prefix, and within budget.
	const multi = "a—b a—b a—b a—b a—b a—b a—b a—b a—b a—b"
	got := TruncateToTokens(multi, 5)
	if !utf8.ValidString(got) || !strings.HasPrefix(multi, got) || EstimateTokens(got) > 5 {
		t.Fatalf("multibyte truncation: %q valid=%v est=%d", got, utf8.ValidString(got), EstimateTokens(got))
	}
}
