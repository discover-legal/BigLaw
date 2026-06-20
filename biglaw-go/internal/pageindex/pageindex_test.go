// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package pageindex

import (
	"strings"
	"testing"
)

// --- Fixtures ------------------------------------------------------------

// numberedContract uses decimal/outline numbering with (a)/(i) sub-items.
const numberedContract = `MASTER SERVICES AGREEMENT

1. Definitions
In this Agreement the following terms have the meanings set out below.
1.1 "Services" means the services described in Schedule A.
1.2 "Fees" means the amounts payable, comprising:
(a) the monthly retainer; and
(b) any of the following pass-through costs:
(i) travel expenses; and
(ii) third-party software fees.
2. Term
This Agreement commences on the Effective Date.
2.1 Renewal occurs automatically unless notice is given.
`

// articleContract uses ARTICLE/Section headings, a WHEREAS recital block, and
// a Definitions article.
const articleContract = `SHARE PURCHASE AGREEMENT

RECITALS
WHEREAS the Seller owns the Shares;
WHEREAS the Buyer wishes to purchase the Shares;
NOW, THEREFORE the parties agree as follows:

ARTICLE I Definitions and Interpretation
Section 1 Defined Terms
"Closing" means the completion of the sale.
Section 2 Interpretation
Headings are for convenience only.

ARTICLE II Purchase and Sale
Section 1 Sale of Shares
The Seller shall sell the Shares to the Buyer.
`

// flatDocument has no detectable structure.
const flatDocument = `This memorandum addresses the question of liability arising from the
incident on the loading dock. There is no formal clause structure here;
it is running prose intended to be read top to bottom without sections.

A second paragraph continues the analysis and reaches a conclusion.`

// --- Invariant: verbatim reconstruction ----------------------------------

// collectBodies walks the tree in pre-order and concatenates Body values.
func collectBodies(secs []Section) string {
	var b strings.Builder
	var walk func(s Section)
	walk = func(s Section) {
		b.WriteString(s.Body)
		for _, c := range s.Children {
			walk(c)
		}
	}
	for _, s := range secs {
		walk(s)
	}
	return b.String()
}

// checkSpans verifies that every Body is exactly the source slice it claims
// (ByteStart..ByteStart+len(Body)) and that subtree spans are well-formed.
func checkSpans(t *testing.T, src string, secs []Section) {
	t.Helper()
	var walk func(s Section)
	walk = func(s Section) {
		if s.ByteStart < 0 || s.ByteEnd > len(src) || s.ByteStart > s.ByteEnd {
			t.Fatalf("bad span [%d,%d) for %q (len=%d)", s.ByteStart, s.ByteEnd, s.Number, len(src))
		}
		want := src[s.ByteStart : s.ByteStart+len(s.Body)]
		if s.Body != want {
			t.Fatalf("Body not verbatim for %q: got %q want %q", s.Number, s.Body, want)
		}
		for _, c := range s.Children {
			walk(c)
		}
	}
	for _, s := range secs {
		walk(s)
	}
}

func TestReconstruction(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"numbered", numberedContract},
		{"article", articleContract},
		{"flat", flatDocument},
		{"empty", ""},
		{"only-newlines", "\n\n\n"},
		{"crlf", "1. One\r\nbody line\r\n2. Two\r\nmore\r\n"},
		{"no-trailing-newline", "1. One\nbody\n2. Two\nlast line no newline"},
		{"heading-only", "ARTICLE I Scope"},
		{"whitespace-prose", "   leading spaces and a 2026 year mid sentence then more text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secs := Parse(tc.src)
			got := collectBodies(secs)
			if got != tc.src {
				t.Fatalf("reconstruction mismatch\n got: %q\nwant: %q", got, tc.src)
			}
			checkSpans(t, tc.src, secs)
		})
	}
}

// --- Structure / nesting -------------------------------------------------

// find returns the first section matching number anywhere in the tree.
func find(secs []Section, number string) *Section {
	for i := range secs {
		if secs[i].Number == number {
			return &secs[i]
		}
		if got := find(secs[i].Children, number); got != nil {
			return got
		}
	}
	return nil
}

func TestNumberedNesting(t *testing.T) {
	secs := Parse(numberedContract)

	s1 := find(secs, "1")
	if s1 == nil {
		t.Fatal(`expected section "1"`)
	}
	if !s1.IsDefinitions {
		t.Errorf(`section 1 "Definitions" should set IsDefinitions`)
	}
	if s1.Scheme != SchemeDecimal {
		t.Errorf("section 1 scheme = %q, want decimal", s1.Scheme)
	}

	// 1.1 and 1.2 must nest under 1.
	if find([]Section{*s1}, "1.1") == nil || find([]Section{*s1}, "1.2") == nil {
		t.Errorf("1.1 / 1.2 should nest under section 1; children=%v", numbers(s1.Children))
	}

	// (a) and (b) nest under 1.2; (i)/(ii) nest under (b).
	s12 := find(secs, "1.2")
	if s12 == nil {
		t.Fatal(`expected "1.2"`)
	}
	a := find([]Section{*s12}, "(a)")
	b := find([]Section{*s12}, "(b)")
	if a == nil || b == nil {
		t.Fatalf("(a)/(b) should nest under 1.2; children=%v", numbers(s12.Children))
	}
	if a.Scheme != SchemeAlpha {
		t.Errorf("(a) scheme = %q, want alpha", a.Scheme)
	}
	if find([]Section{*b}, "(i)") == nil || find([]Section{*b}, "(ii)") == nil {
		t.Errorf("(i)/(ii) should nest under (b); children=%v", numbers(b.Children))
	}
	if r := find(secs, "(i)"); r != nil && r.Scheme != SchemeRoman {
		t.Errorf("(i) scheme = %q, want roman", r.Scheme)
	}

	// Level monotonicity: deeper numbers have higher Level.
	if find(secs, "1").Level >= find(secs, "1.1").Level {
		t.Errorf("level(1)=%d should be < level(1.1)=%d", find(secs, "1").Level, find(secs, "1.1").Level)
	}
	if find(secs, "1.2").Level >= find(secs, "(a)").Level {
		t.Errorf("level(1.2)=%d should be < level((a))=%d", find(secs, "1.2").Level, find(secs, "(a)").Level)
	}
	if find(secs, "(a)").Level >= find(secs, "(i)").Level {
		t.Errorf("level((a))=%d should be < level((i))=%d", find(secs, "(a)").Level, find(secs, "(i)").Level)
	}
}

func TestArticleAndRecitalNesting(t *testing.T) {
	secs := Parse(articleContract)

	a1 := find(secs, "ARTICLE I")
	a2 := find(secs, "ARTICLE II")
	if a1 == nil || a2 == nil {
		t.Fatalf("expected ARTICLE I and II at top level; got %v", numbers(secs))
	}
	if a1.Scheme != SchemeArticle {
		t.Errorf("ARTICLE I scheme = %q, want article", a1.Scheme)
	}
	if !a1.IsDefinitions {
		t.Errorf(`ARTICLE I "Definitions and Interpretation" should set IsDefinitions`)
	}

	// Section 1 / Section 2 nest under their article (levels: article=1,
	// section=2). Both articles define "Section 1", so check within a1.
	if find([]Section{*a1}, "Section 1") == nil || find([]Section{*a1}, "Section 2") == nil {
		t.Errorf("Section 1/2 should nest under ARTICLE I; children=%v", numbers(a1.Children))
	}
	for _, c := range a1.Children {
		if c.Scheme != SchemeSection {
			t.Errorf("child %q of ARTICLE I scheme = %q, want section", c.Number, c.Scheme)
		}
		if c.Level <= a1.Level {
			t.Errorf("section level %d should exceed article level %d", c.Level, a1.Level)
		}
	}

	// Recital framing: RECITALS header opens a block; WHEREAS / NOW THEREFORE
	// nest beneath it.
	rec := find(secs, "")
	_ = rec // root may be returned; ensure recital scheme nodes exist.
	w := findScheme(secs, SchemeRecital)
	if w == nil {
		t.Fatal("expected at least one recital-scheme node (RECITALS/WHEREAS/NOW THEREFORE)")
	}
	if find(secs, "WHEREAS") == nil {
		t.Errorf("expected a WHEREAS node")
	}
	if find(secs, "NOW, THEREFORE") == nil {
		t.Errorf("expected a NOW, THEREFORE node")
	}
}

func TestFlatDocument(t *testing.T) {
	secs := Parse(flatDocument)
	if len(secs) != 1 {
		t.Fatalf("flat document should yield exactly one root section, got %d", len(secs))
	}
	root := secs[0]
	if len(root.Children) != 0 {
		t.Errorf("flat root should have no children, got %d", len(root.Children))
	}
	if root.Body != flatDocument {
		t.Errorf("flat root Body should equal the whole input")
	}
	if root.Number != "" || root.Scheme != SchemeNone {
		t.Errorf("flat root should be an unnumbered SchemeNone node; got number=%q scheme=%q", root.Number, root.Scheme)
	}
}

func TestSchemes(t *testing.T) {
	src := "Schedule A Pricing\nrate card\nExhibit 1 Form of Notice\ntemplate\n§ 5 Governing Law\nNew York law applies.\n"
	secs := Parse(src)
	if find(secs, "Schedule A") == nil {
		t.Errorf("expected Schedule A; got %v", allNumbers(secs))
	}
	if find(secs, "Exhibit 1") == nil {
		t.Errorf("expected Exhibit 1; got %v", allNumbers(secs))
	}
	if s := find(secs, "Section 5"); s == nil || s.Scheme != SchemeSection {
		t.Errorf("expected § 5 to parse as Section 5; got %v", allNumbers(secs))
	}
	// Reconstruction still holds for the multi-scheme case.
	if collectBodies(secs) != src {
		t.Errorf("multi-scheme reconstruction mismatch")
	}
	checkSpans(t, src, secs)
}

// --- helpers for diagnostics --------------------------------------------

func numbers(secs []Section) []string {
	out := make([]string, 0, len(secs))
	for _, s := range secs {
		out = append(out, s.Number)
	}
	return out
}

func allNumbers(secs []Section) []string {
	var out []string
	var walk func(s Section)
	walk = func(s Section) {
		if s.Number != "" {
			out = append(out, s.Number)
		}
		for _, c := range s.Children {
			walk(c)
		}
	}
	for _, s := range secs {
		walk(s)
	}
	return out
}

func findScheme(secs []Section, sc Scheme) *Section {
	for i := range secs {
		if secs[i].Scheme == sc {
			return &secs[i]
		}
		if got := findScheme(secs[i].Children, sc); got != nil {
			return got
		}
	}
	return nil
}
