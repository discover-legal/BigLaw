// SPDX-License-Identifier: AGPL-3.0-only
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
