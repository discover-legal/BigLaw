// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"reflect"
	"strings"
	"testing"
)

const handleFixture = `INVESTMENT ADVISORY AGREEMENT

ARTICLE I
Section 1.1 Definitions
"Adviser" means Meridian Capital Management LLC, an investment adviser registered with the Commission.
Section 9.1 Confidentiality
(a) The Adviser shall not disclose client records to any third party.
(b) Exceptions require the client's prior written consent.

Section 9.2 Compliance
The Adviser shall adopt a code of ethics complying with Rule 204A-1 under the Advisers Act.
Claims for civil penalties are time-barred under § 2462 after five years.
The Adviser failed to disclose the arrangement in Item 6 of its brochure.
`

// The saturation walk is deterministic: the same document yields the same
// chunk set every run — no harvest lottery.
func TestSectionChunks_Deterministic(t *testing.T) {
	a := sectionChunks(handleFixture, 60)
	b := sectionChunks(handleFixture, 60)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("sectionChunks is not deterministic:\n%v\nvs\n%v", a, b)
	}
	if len(a) < 2 {
		t.Fatalf("fixture should split into multiple windows at 60 tokens, got %d", len(a))
	}
}

// The walk covers the WHOLE document — pre-order body concatenation is the
// pageindex byte-exact reconstruction, so joining the chunks reproduces the
// source. Nothing is truncated, nothing visited twice.
func TestSectionChunks_FullCover(t *testing.T) {
	for _, maxTok := range []int{25, 60, 1500} {
		joined := strings.Join(sectionChunks(handleFixture, maxTok), "")
		if joined != handleFixture {
			t.Fatalf("maxTok=%d: chunk concatenation does not reproduce the source (len %d vs %d)",
				maxTok, len(joined), len(handleFixture))
		}
	}
}

// A section smaller than the window budget is never split across chunks — the
// window flushes at section boundaries, so a total stays with its components.
func TestSectionChunks_SectionAligned(t *testing.T) {
	section92 := "Section 9.2 Compliance\nThe Adviser shall adopt a code of ethics complying with Rule 204A-1 under the Advisers Act.\nClaims for civil penalties are time-barred under § 2462 after five years.\nThe Adviser failed to disclose the arrangement in Item 6 of its brochure.\n"
	hits := 0
	for _, c := range sectionChunks(handleFixture, 80) {
		if strings.Contains(c, section92) {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("Section 9.2's body should sit whole inside exactly one window, found in %d", hits)
	}
}

// Section/rule/item identifiers are harvested as first-class handles —
// headings from the tree walk, statutory cites from the inline scan — each
// grounded by a verbatim quote.
func TestHarvestSectionHandles(t *testing.T) {
	hs := harvestSectionHandles(handleFixture)
	byNorm := map[string]sectionHandle{}
	for _, h := range hs {
		byNorm[figNorm(h.Handle)] = h
	}
	for _, want := range []string{"Section 9.1", "Section 9.1(a)", "Rule 204A-1", "§ 2462", "Item 6"} {
		h, ok := byNorm[figNorm(want)]
		if !ok {
			t.Errorf("handle %q not harvested (got %v)", want, keysOf(byNorm))
			continue
		}
		if strings.TrimSpace(h.Quote) == "" {
			t.Errorf("handle %q has no grounding quote", want)
			continue
		}
		// Grounding by construction: the quote is a verbatim slice of the source.
		if !strings.Contains(figNorm(handleFixture), figNorm(h.Quote)) {
			t.Errorf("handle %q quote is not verbatim in the source: %q", want, h.Quote)
		}
	}
}

// Two harvests of the same fixture produce identical handle sets, in order.
func TestHarvestSectionHandles_Deterministic(t *testing.T) {
	if !reflect.DeepEqual(harvestSectionHandles(handleFixture), harvestSectionHandles(handleFixture)) {
		t.Fatalf("harvestSectionHandles is not deterministic")
	}
}

// Handles are deduped by normalized identifier — a heading referenced inline
// twice yields one handle.
func TestHarvestSectionHandles_Dedup(t *testing.T) {
	text := "Section 4.2 Fees\nThe fee is due under Section 4.2 quarterly. Section 4.2 also governs refunds.\n"
	n := 0
	for _, h := range harvestSectionHandles(text) {
		if figNorm(h.Handle) == figNorm("Section 4.2") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 deduped handle for Section 4.2, got %d", n)
	}
}

// The inline-cite scan carries the writer's full salientCite keyword set
// (Section/Rule/Item/Part/Article/Clause/Paragraph/Exhibit + Form), so a handle
// the writer can render is a handle the harvest can seed.
func TestHarvestSectionHandles_WriterConsistentKeywords(t *testing.T) {
	text := "The chart at Exhibit 3 summarizes the losses. See Paragraph 12 of the complaint and Clause 4.2 of the agreement. Part 2A of the brochure was never amended.\n"
	hs := harvestSectionHandles(text)
	got := map[string]bool{}
	for _, h := range hs {
		got[figNorm(h.Handle)] = true
	}
	for _, want := range []string{"Exhibit 3", "Paragraph 12", "Clause 4.2", "Part 2A"} {
		if !got[figNorm(want)] {
			t.Errorf("inline handle %q not harvested (got %v)", want, hs)
		}
	}
}

func keysOf(m map[string]sectionHandle) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
