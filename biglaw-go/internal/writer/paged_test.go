// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import (
	"strings"
	"testing"
)

// The paged board must page sections out of working context (compact handles) yet keep the
// full text retrievable on demand and lossless at assembly — the whole point of the scheme.
func TestPagedBoard_CompactPagingIsLossless(t *testing.T) {
	b := newPagedBoard()
	b.put("Cherry-Picking", "Full cherry-picking section: $7,800,000 to Oceanic Fund I LP; 81.6% rate.", "- cherry-picking; Oceanic Fund I LP; $7.8M; 81.6%")
	b.put("Directed-Brokerage Kickback Scheme", "Full Bellini section: $291,400 excess commissions via Lakeshore Trading; Crescent Bay pension victim.", "- Bellini; $291,400; Lakeshore; Crescent Bay")

	// priorBlock shows COMPACT handles (small), not the full text.
	pb := b.priorBlock()
	if strings.Contains(pb, "excess commissions via Lakeshore") {
		t.Error("priorBlock leaked full section text; it must show only compacted handles")
	}
	if !strings.Contains(pb, "$291,400") {
		t.Error("compacted handle dropped a verbatim figure it was given")
	}

	// expand_section uncompacts on demand — exact and loose (paraphrased) title match.
	if got := b.expand("Directed-Brokerage Kickback Scheme"); !strings.Contains(got, "$291,400 excess commissions") {
		t.Errorf("expand by exact title failed: %q", got)
	}
	if got := b.expand("directed-brokerage"); !strings.Contains(got, "Lakeshore") {
		t.Errorf("expand by loose title match failed: %q", got)
	}

	// Assembly is lossless: every full section survives verbatim, in order.
	secs := []section{{Title: "Cherry-Picking"}, {Title: "Directed-Brokerage Kickback Scheme"}}
	w := &Writer{}
	out := w.assemblePaged(secs, b)
	for _, must := range []string{"$7,800,000", "81.6%", "$291,400", "Lakeshore Trading", "Crescent Bay"} {
		if !strings.Contains(out, must) {
			t.Errorf("assembly dropped %q — paging must be lossless:\n%s", must, out)
		}
	}
	if strings.Index(out, "Cherry-Picking") > strings.Index(out, "Directed-Brokerage") {
		t.Error("assembly did not preserve section order")
	}
}

// factsFor must route each grounded fact to the section it concerns — the directed-
// brokerage fact lands in that section, NOT in cherry-picking (the C-027 attribution fix).
func TestFactsFor_RoutesByAllegation(t *testing.T) {
	w := &Writer{opt: Options{Facts: []Fact{
		{Line: "- Crescent Bay victim of directed-brokerage kickback scheme", Key: "crescent bay victim directed brokerage kickback scheme bellini"},
		{Line: "- Ostrowski is 40% owner of Lakeshore Trading", Key: "ostrowski owner lakeshore trading directed brokerage"},
		{Line: "- Whitmore holds 12% LP interest in Oceanic Fund", Key: "whitmore holds 12% interest oceanic fund cherry picking allocation"},
	}}}
	ix := NewFindingIndex(nil, []Finding{
		{ID: "d1", Content: "The directed brokerage kickback scheme routed Crescent Bay pension trades through Lakeshore."},
		{ID: "c1", Content: "Cherry-picking allocation gave Oceanic Fund excess profits via Whitmore."},
	})
	db := w.factsFor(section{Title: "Directed-Brokerage Kickback Scheme", FindingIDs: []string{"d1"}}, ix, nil)
	if !strings.Contains(db, "Crescent Bay") || !strings.Contains(db, "Ostrowski") {
		t.Errorf("directed-brokerage section missing its facts:\n%s", db)
	}
	if strings.Contains(db, "Whitmore holds 12%") {
		t.Errorf("cherry-picking fact wrongly routed into directed-brokerage:\n%s", db)
	}
	cp := w.factsFor(section{Title: "Cherry-Picking Allocation", FindingIDs: []string{"c1"}}, ix, nil)
	if !strings.Contains(cp, "Whitmore holds 12%") {
		t.Errorf("cherry-picking section missing the Whitmore fact:\n%s", cp)
	}
	if strings.Contains(cp, "Crescent Bay victim") {
		t.Errorf("directed-brokerage fact wrongly routed into cherry-picking (the C-027 bug):\n%s", cp)
	}
}

// lookup_fact must let any author pull a relevant fact from the whole ledger by query —
// the recall escape hatch so per-section routing never starves a section.
func TestLookupFacts_PullsByQuery(t *testing.T) {
	w := &Writer{opt: Options{Facts: []Fact{
		{Line: "- Ostrowski is 40% owner of Lakeshore Trading", Key: "ostrowski owner lakeshore trading"},
		{Line: "- Whitmore holds 12% LP interest in Oceanic Fund", Key: "whitmore holds interest oceanic fund"},
		{Line: "- Bellini received undisclosed compensation", Key: "bellini received undisclosed compensation"},
	}}}
	got := w.lookupFacts("ostrowski ownership lakeshore", 3)
	if len(got) == 0 || !strings.Contains(got[0], "Ostrowski is 40%") {
		t.Errorf("lookup_fact failed to pull the Ostrowski fact: %v", got)
	}
	if got := w.lookupFacts("xyzzy nothing", 3); len(got) != 0 {
		t.Errorf("expected no matches for an unrelated query, got %v", got)
	}
}

// sanitizeDraft must strip the machine tells a human flags: process meta-commentary and
// leaked deliberation labels — while keeping substantive prose.
func TestSanitizeDraft(t *testing.T) {
	in := strings.Join([]string{
		"Since there are no existing findings for the Form ADV section, I will write it based on the provided grounded facts.",
		"Stronger View",
		"WCA failed to disclose the directed-brokerage arrangement, a material omission under Section 207.",
		"Brief Answer: Whitmore and Chao were responsible for the filings.",
		"It could be argued that delays were operational.",
	}, "\n")
	out := sanitizeDraft(in)
	if strings.Contains(out, "I will write") || strings.Contains(out, "Since there are no") {
		t.Errorf("meta-monologue survived:\n%s", out)
	}
	if strings.Contains(out, "Stronger View") {
		t.Errorf("leaked deliberation label survived:\n%s", out)
	}
	if !strings.Contains(out, "material omission under Section 207") {
		t.Errorf("substantive prose was wrongly removed:\n%s", out)
	}
	if !strings.Contains(out, "Whitmore and Chao were responsible") { // label stripped, prose kept
		t.Errorf("Brief Answer prose lost with its label:\n%s", out)
	}
}

// salientFigure must not surface a bare paragraph/list number as a figure, but must keep
// real bare counts and amounts.
func TestSalientFigure_ParagraphNumberSkip(t *testing.T) {
	if got := salientFigure("22. The Division alleges that Chao directed allocations"); got != "" {
		t.Errorf("paragraph number surfaced as figure: %q", got)
	}
	if got := salientFigure("312 Microsoft Excel spreadsheets"); got != "312" {
		t.Errorf("real count dropped: %q", got)
	}
	if got := salientFigure("excess profits of $7,800,000 to Oceanic"); got != "$7,800,000" {
		t.Errorf("amount dropped: %q", got)
	}
}
