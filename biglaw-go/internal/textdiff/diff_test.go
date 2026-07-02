// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package textdiff

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestIdenticalTextsNoChanges(t *testing.T) {
	text := "The liability cap is twelve (12) months of fees paid under this Agreement."
	if hunks := Changes(text, text); len(hunks) != 0 {
		t.Fatalf("Changes(identical) = %d hunks, want 0: %+v", len(hunks), hunks)
	}
	full := Diff(text, text)
	if len(full) != 1 || full[0].Kind != Equal {
		t.Fatalf("Diff(identical) = %+v, want a single equal hunk", full)
	}
	if full[0].OurText != text || full[0].TheirText != text {
		t.Errorf("equal hunk texts = %q / %q, want the input", full[0].OurText, full[0].TheirText)
	}
}

func TestBothEmpty(t *testing.T) {
	if hunks := Diff("", ""); len(hunks) != 0 {
		t.Fatalf("Diff(empty, empty) = %+v, want none", hunks)
	}
}

func TestWhitespaceOnlyDifferencesAreEqual(t *testing.T) {
	a := "Either party may terminate\non thirty days notice."
	b := "Either  party may terminate on thirty\n\ndays   notice."
	if hunks := Changes(a, b); len(hunks) != 0 {
		t.Fatalf("whitespace-only difference produced changes: %+v", hunks)
	}
}

func TestPureInsertion(t *testing.T) {
	a := "This Agreement is governed by the laws of England and Wales."
	b := "This Agreement is governed by the laws of England and Wales, excluding its conflict of laws rules."
	hunks := Changes(a, b)
	// The trailing word differs ("Wales." vs "Wales,") so the minimal edit may
	// touch it; the inserted language must be present in a their-side hunk.
	var inserted string
	for _, h := range hunks {
		if h.TheirText != "" {
			inserted = h.TheirText
		}
	}
	if !strings.Contains(inserted, "excluding its conflict of laws rules") {
		t.Fatalf("insertion not reported; hunks: %+v", hunks)
	}
}

func TestCleanInsertion(t *testing.T) {
	a := "The parties agree to the terms below."
	b := "The parties hereby agree to the terms below."
	hunks := Changes(a, b)
	if len(hunks) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(hunks), hunks)
	}
	h := hunks[0]
	if h.Kind != Insert || h.TheirText != "hereby" || h.OurText != "" {
		t.Errorf("hunk = %+v, want insert of %q", h, "hereby")
	}
	if h.TheirOffset != strings.Index(b, "hereby") {
		t.Errorf("TheirOffset = %d, want %d", h.TheirOffset, strings.Index(b, "hereby"))
	}
	if !strings.Contains(h.ContextBefore, "The parties") || !strings.Contains(h.ContextAfter, "agree to") {
		t.Errorf("context = %q / %q", h.ContextBefore, h.ContextAfter)
	}
}

func TestCleanDeletion(t *testing.T) {
	a := "Either party may terminate for convenience on thirty days notice."
	b := "Either party may terminate on thirty days notice."
	hunks := Changes(a, b)
	if len(hunks) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(hunks), hunks)
	}
	h := hunks[0]
	if h.Kind != Delete || h.OurText != "for convenience" || h.TheirText != "" {
		t.Errorf("hunk = %+v, want delete of %q", h, "for convenience")
	}
	if h.OurOffset != strings.Index(a, "for convenience") {
		t.Errorf("OurOffset = %d, want %d", h.OurOffset, strings.Index(a, "for convenience"))
	}
	if !strings.Contains(h.ContextBefore, "may terminate") || !strings.Contains(h.ContextAfter, "on thirty") {
		t.Errorf("context = %q / %q", h.ContextBefore, h.ContextAfter)
	}
}

func TestSubstitution(t *testing.T) {
	a := "The liability cap is twelve months of fees."
	b := "The liability cap is thirty-six months of fees."
	hunks := Changes(a, b)
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want delete+insert pair: %+v", len(hunks), hunks)
	}
	if hunks[0].Kind != Delete || hunks[0].OurText != "twelve" {
		t.Errorf("hunk 0 = %+v, want delete of %q", hunks[0], "twelve")
	}
	if hunks[1].Kind != Insert || hunks[1].TheirText != "thirty-six" {
		t.Errorf("hunk 1 = %+v, want insert of %q", hunks[1], "thirty-six")
	}
	// The pair anchors at the same equal-context boundary.
	if hunks[0].OurOffset != strings.Index(a, "twelve") || hunks[1].TheirOffset != strings.Index(b, "thirty-six") {
		t.Errorf("offsets: delete at %d, insert at %d", hunks[0].OurOffset, hunks[1].TheirOffset)
	}
}

func TestUnicodeContent(t *testing.T) {
	a := "Der Vertrag unterliegt schweizerischem Recht — Gerichtsstand Zürich."
	b := "Der Vertrag unterliegt französischem Recht — Gerichtsstand Zürich."
	hunks := Changes(a, b)
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want 2: %+v", len(hunks), hunks)
	}
	if hunks[0].OurText != "schweizerischem" || hunks[1].TheirText != "französischem" {
		t.Errorf("texts = %q / %q", hunks[0].OurText, hunks[1].TheirText)
	}
	// Context windows must never split a multibyte rune.
	for _, h := range hunks {
		for _, s := range []string{h.ContextBefore, h.ContextAfter, h.OurText, h.TheirText} {
			if !utf8.ValidString(s) {
				t.Errorf("invalid UTF-8 in hunk field %q", s)
			}
		}
	}
}

func TestEmptyVersusText(t *testing.T) {
	hunks := Changes("", "entirely new text")
	if len(hunks) != 1 || hunks[0].Kind != Insert || hunks[0].TheirText != "entirely new text" {
		t.Fatalf("empty→text: %+v", hunks)
	}
	hunks = Changes("entirely old text", "")
	if len(hunks) != 1 || hunks[0].Kind != Delete || hunks[0].OurText != "entirely old text" {
		t.Fatalf("text→empty: %+v", hunks)
	}
}

// TestOversizeMiddleFallsBackCoarsely: when the differing middle would exceed
// the DP cell cap, the diff degrades to one delete + one insert rather than
// allocating unbounded memory.
func TestOversizeMiddleFallsBackCoarsely(t *testing.T) {
	var aw, bw []string
	for i := 0; i < 2100; i++ {
		aw = append(aw, fmt.Sprintf("alpha%d", i))
		bw = append(bw, fmt.Sprintf("beta%d", i))
	}
	a := "same prefix " + strings.Join(aw, " ") + " same suffix"
	b := "same prefix " + strings.Join(bw, " ") + " same suffix"
	hunks := Diff(a, b)
	if len(hunks) != 4 {
		t.Fatalf("got %d hunks, want equal+delete+insert+equal: kinds %v", len(hunks), kinds(hunks))
	}
	if hunks[0].Kind != Equal || hunks[1].Kind != Delete || hunks[2].Kind != Insert || hunks[3].Kind != Equal {
		t.Fatalf("kinds = %v", kinds(hunks))
	}
	if !strings.HasPrefix(hunks[1].OurText, "alpha0") || !strings.HasPrefix(hunks[2].TheirText, "beta0") {
		t.Errorf("coarse hunks do not cover the middle: %q / %q", hunks[1].OurText[:20], hunks[2].TheirText[:20])
	}
}

func kinds(hunks []Hunk) []Kind {
	out := make([]Kind, len(hunks))
	for i, h := range hunks {
		out[i] = h.Kind
	}
	return out
}
