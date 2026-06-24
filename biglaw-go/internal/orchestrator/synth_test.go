// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"strings"
	"testing"
)

// The 7B emits "framework" as a JSON ARRAY of steps — which previously failed to
// unmarshal (Framework was a string), silently dropping every generated agent.
func TestParseAgentSpecs_FrameworkAsArray(t *testing.T) {
	raw := `[
	  {"name":"SEC Reporting Analyst","description":"Analyzes filings.",
	   "framework":["Gather filings","Identify requirements","Compare","Report"],
	   "skills":["regulatory-analysis","sec-filings"]},
	  {"name":"Insider Trading Analyst","description":"Detects insider trading.",
	   "framework":"1. Monitor insiders 2. Track trades 3. Report","skills":["detection"]}
	]`
	specs := parseAgentSpecs(raw)
	if len(specs) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(specs))
	}
	if string(specs[0].Framework) == "" {
		t.Error("array framework did not coerce to text — the original bug")
	}
	if string(specs[1].Framework) == "" {
		t.Error("string framework lost")
	}
}

// dedupAllegations must collapse the same allegation listed under different category
// numbers — the failure mode that gave the writer 4 cherry-picking sections and no
// Bellini section even though Bellini was found in the rounds.
func TestDedupAllegations_CollapsesThemeDuplicates(t *testing.T) {
	in := []string{
		"Allegation Category 1 — Cherry-Picking and Misleading Disclosures",
		"Allegation Category 2 — Misleading Form ADV Disclosures",
		"Category 3 — Cherry-Picking and Misleading Disclosures",
		"ALLEGATION CATEGORY 5 — CHERRY-PICKING AND MISLEADING DISCLOSURES",
		"VI. Failure to Maintain Required Books and Records",
		"Directed-Brokerage Kickback Scheme",
	}
	out := dedupAllegations(in)
	if len(out) != 4 {
		t.Fatalf("want 4 distinct allegations, got %d: %v", len(out), out)
	}
	joined := strings.Join(out, "|")
	for _, must := range []string{"Form ADV", "Books and Records", "Directed-Brokerage"} {
		if !strings.Contains(joined, must) {
			t.Errorf("deduped set lost a distinct allegation %q: %v", must, out)
		}
	}
}
