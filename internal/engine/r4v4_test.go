// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r4 verifier repros (critique/biglaw-lab-r4-r4-verifier.md): the mechanical LOWs (L7 the
// committee cap's disclosure, L8 the placement request's cap, L9 a merged duplicate's value notes).

// L9: an exact duplicate's dropped values were noted only in the call's notes, never on the kept finding.
func TestR4V4DuplicateKeepsValueNotes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Damages\nGreystone claims damages of $644,910 for unpaid invoices.\n")
	d1 := f.docs[0].ID
	q := "Greystone claims damages of $644,910 for unpaid invoices"
	first := fd("Greystone claims damages of $644,910.", d1, q, val("Greystone", "damages", "$644,910", "Money"))
	again := fd("Greystone claims damages of $644,910.", d1, q, val("Greystone", "damages", "$644,910", "Money"), val("Greystone", "interest", "$99,000", "Money"))
	v := f.run(f.with(map[string][]model.Reply{"extract_facts": {facts(first, again)}}), Config{})
	fs := ledgerFindings(t, f, v.ID)
	if len(fs) != 1 || !slices.ContainsFunc(fs[0].ValueNotes, func(n string) bool { return strings.Contains(n, "$99,000") }) {
		t.Errorf("the merged duplicate's dropped value is not on the kept finding: %+v", fs)
	}
}

// L8: a placement redraft's request cut a large section's own citations at maxDraftFIDs.
func TestR4V4PlacementKeepsCitations(t *testing.T) {
	var cites, left []string
	for i := range 95 {
		cites = append(cites, fmt.Sprintf("F%d", i+1))
	}
	for i := range 16 {
		left = append(left, fmt.Sprintf("F%d", 200+i))
	}
	p := &placement{FIDs: left, Conflicts: []analysis.Conflict{{Values: []analysis.ConflictVal{{FID: "F300"}, {FID: "F3"}}}}}
	fids, ok := placeFIDs(p, cites)
	if !ok || len(fids) != maxDraftFIDs || !slices.Contains(fids, "F300") || !slices.Contains(fids, "F200") || slices.Contains(fids, "F215") {
		t.Errorf("placement request %v (%v)", fids, ok)
	}
	for _, c := range cites {
		if !slices.Contains(fids, c) {
			t.Fatalf("the section's citation %s was cut from the request", c)
		}
	}
	for i := range 10 {
		cites = append(cites, fmt.Sprintf("F%d", 400+i))
	}
	if _, ok := placeFIDs(p, cites); ok {
		t.Errorf("a section citing more than the request holds was redrafted from fewer")
	}
}

// L7: after the committee's cap, fixes resolved after its last cycle were disclosed only generically.
func TestR4V4UnreviewedFixesNamed(t *testing.T) {
	crit := []Critic{
		{ID: "C1", Cycle: 1, Critic: "prosecute", Severity: "HIGH", Text: "reviewed fix", Status: "fixed", Resolved: 1},
		{ID: "C7", Cycle: 3, Critic: "reproduce", Severity: "HIGH", Text: "the deadline is wrong", Status: "fixed", Resolved: 3},
		{ID: "C2", Cycle: 2, Critic: "audit_gaps", Severity: "MEDIUM", Text: "late fix", Status: "fixed", Resolved: 3},
		{ID: "C8", Cycle: 3, Critic: "audit_gaps", Severity: "LOW", Text: "disclosed", Status: "disclosed", Resolved: 3},
	}
	got := unreviewedFixes(crit, 3)
	if !strings.Contains(got, "C7 (HIGH, reproduce): the deadline is wrong") || !strings.Contains(got, "C2 (MEDIUM") || strings.Contains(got, "C1") || strings.Contains(got, "C8") {
		t.Errorf("unreviewed fixes: %q", got)
	}
	if unreviewedFixes(crit[:1], 3) != "" {
		t.Errorf("a fix the committee reviewed was named")
	}
}
