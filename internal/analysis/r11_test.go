// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"strings"
	"testing"
)

// lab-r4 r11 verifier: statesUnit never panics, and a unit it accepts (not a multiple's sign) is
// words written after the span.
func FuzzStatesUnitWordsAfter(f *testing.F) {
	f.Add("An answer is due within 21 business days.", "business days", false)
	f.Add("x 1.5 and 1.5× and Rule 12(b) of Section 30 of", "×", true)
	f.Add("1.5x", "x", true)
	f.Fuzz(func(t *testing.T, text, from string, multiple bool) {
		for _, s := range PassageOf("q1", text).Spans {
			if statesUnit(text, s, from, multiple) && !multiple && !strings.Contains(asciiLower(text[s.End:]), asciiLower(strings.TrimSpace(from))) {
				t.Fatalf("%q after %q states %q", text, s.Text, from)
			}
		}
	})
}
