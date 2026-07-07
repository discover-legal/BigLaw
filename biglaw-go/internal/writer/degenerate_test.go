// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Degenerate-spine guard + writer cost tracking (fix 7). The thinking wreck produced
// ~88 sections all sharing one cluster-label title after coverage-spine + planOutline
// both failed and one giant cluster was chunked N ways. The guard collapses such an
// outline to distinctly-labeled sections. Writer model calls also route through the
// cost-recording hook.

package writer

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

func TestGuardDegenerateOutline_CollapsesSharedTitle(t *testing.T) {
	// 88 sections all under one TF-IDF cluster label (the wreck), plus two legitimate
	// distinct sections that must survive untouched.
	var secs []section
	for i := 0; i < 88; i++ {
		secs = append(secs, section{Title: "Allocation Defense Trading Personal", FindingIDs: []string{"f" + strconv.Itoa(i)}})
	}
	secs = append(secs,
		section{Title: "Parties and Ownership", FindingIDs: []string{"p1"}},
		section{Title: "Examination Timeline", FindingIDs: []string{"t1"}},
	)

	got := guardDegenerateOutline(secs)

	// The 88 same-title sections collapse to ONE; the two distinct sections remain.
	if len(got) != 3 {
		t.Fatalf("collapsed outline has %d sections, want 3 (1 merged + 2 distinct)", len(got))
	}
	// No title may appear more than the cap after collapse — distinct labels only.
	seen := map[string]int{}
	for _, s := range got {
		seen[s.Title]++
	}
	for title, n := range seen {
		if n > 1 {
			t.Errorf("title %q still appears %d times after collapse", title, n)
		}
	}
	// The merged section keeps all 88 findings (union, deduped).
	var mergedIDs int
	for _, s := range got {
		if s.Title == "Allocation Defense Trading Personal" {
			mergedIDs = len(s.FindingIDs)
		}
	}
	if mergedIDs != 88 {
		t.Errorf("merged section carries %d findings, want 88 (union of the chunked cluster)", mergedIDs)
	}
}

func TestGuardDegenerateOutline_HealthyOutlineUnchanged(t *testing.T) {
	secs := []section{
		{Title: "A"}, {Title: "B"}, {Title: "A"}, {Title: "C"},
	} // "A" appears twice — within the cap; not degenerate.
	got := guardDegenerateOutline(secs)
	if len(got) != len(secs) {
		t.Errorf("healthy outline mutated: %d → %d sections", len(secs), len(got))
	}
}

// costProv counts provider calls and always answers with visible text (no tools).
type costProv struct{ calls atomic.Int32 }

func (p *costProv) Chat(params providers.ChatParams) (*providers.ChatResponse, error) {
	p.calls.Add(1)
	// A drafter (tools present) that hasn't searched yet gets a tool_use turn; once a
	// tool result comes back, emit the draft. Simplest: always return text — the writer
	// tolerates a drafter that skips the tool call.
	return &providers.ChatResponse{
		StopReason: providers.StopEndTurn,
		Content:    []providers.ContentBlock{{Type: providers.BlockText, Text: "Grounded section prose about the matter."}},
		Usage:      providers.Usage{InputTokens: 10, OutputTokens: 5},
	}, nil
}

// TestWriterCallsAreCostTracked: every writer→provider call reaches the RecordCost
// hook, so writer synthesis is visible in cost records (it was invisible before).
func TestWriterCallsAreCostTracked(t *testing.T) {
	var recorded atomic.Int32
	prov := &costProv{}
	w := New(nil, prov, "m", Options{
		MaxFindingsPerSec: 2,
		RecordCost: func(resp *providers.ChatResponse) {
			recorded.Add(1)
		},
	})
	out, err := w.Write("Summarize the matter", "roundtable", sampleFindings(4))
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("writer produced no output")
	}
	if prov.calls.Load() == 0 {
		t.Fatal("no provider calls were made")
	}
	if recorded.Load() != prov.calls.Load() {
		t.Errorf("RecordCost fired %d times but the provider was called %d times — some writer calls are cost-invisible",
			recorded.Load(), prov.calls.Load())
	}
}
