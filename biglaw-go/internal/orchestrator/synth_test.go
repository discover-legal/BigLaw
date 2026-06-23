// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package orchestrator

import "testing"

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
