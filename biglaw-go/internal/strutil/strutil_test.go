// SPDX-License-Identifier: AGPL-3.0-only
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
