// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package writer

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

func sampleFindings(n int) []Finding {
	fs := make([]Finding, n)
	for i := 0; i < n; i++ {
		fs[i] = Finding{
			ID:       string(rune('a' + i)),
			Content:  "Conclusion number " + string(rune('A'+i)) + " about the matter",
			Evidence: "verbatim evidence " + string(rune('A'+i)),
			Source:   "doc-1",
			Grounded: true,
		}
	}
	return fs
}

// scriptProv is a scripted Provider: drafter calls (Tools present) emit one
// search_findings tool_use then the configured draft text; planner/stitch calls
// (tool-less) return text keyed by system prompt.
type scriptProv struct {
	draftText  string
	emptyDraft bool
	plannerTxt string
	stitchTxt  string
}

func (p *scriptProv) Chat(params providers.ChatParams) (*providers.ChatResponse, error) {
	text := func(s string) *providers.ChatResponse {
		return &providers.ChatResponse{StopReason: providers.StopEndTurn, Content: []providers.ContentBlock{{Type: providers.BlockText, Text: s}}}
	}
	if len(params.Tools) > 0 { // a drafter
		last := params.Messages[len(params.Messages)-1]
		if blocks, ok := last.Content.([]providers.ContentBlock); ok && len(blocks) > 0 && blocks[0].Type == providers.BlockToolResult {
			if p.emptyDraft {
				return &providers.ChatResponse{StopReason: providers.StopEndTurn}, nil
			}
			return text(p.draftText), nil
		}
		return &providers.ChatResponse{StopReason: providers.StopToolUse, Content: []providers.ContentBlock{
			{Type: providers.BlockToolUse, ID: "t1", Name: "search_findings", Input: map[string]interface{}{"query": "x"}},
		}}, nil
	}
	if params.System == plannerSystem {
		return text(p.plannerTxt), nil
	}
	return text(p.stitchTxt), nil
}

func TestWriteProducesGroundedDoc(t *testing.T) {
	w := New(nil, &scriptProv{draftText: "Drafted section prose.", stitchTxt: ""}, "m", Options{MaxFindingsPerSec: 2})
	out, err := w.Write("Summarize the matter", "roundtable", sampleFindings(5))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("output empty")
	}
	// stitch returns empty → falls back to the assembled sections, which contain the
	// drafted prose under headings.
	if !strings.Contains(out, "Drafted section prose.") {
		t.Errorf("expected drafted prose in output, got:\n%s", out)
	}
	if !strings.Contains(out, "##") {
		t.Errorf("expected section headings, got:\n%s", out)
	}
}

func TestWriteNeverEmpty_FallbackToConclusions(t *testing.T) {
	// Drafters return nothing AND stitch returns nothing — must still yield a
	// non-empty grounded document (the findings' own conclusions). This is the
	// guarantee that the empty-synthesis bug cannot recur.
	w := New(nil, &scriptProv{emptyDraft: true, stitchTxt: ""}, "m", Options{MaxFindingsPerSec: 2})
	out, err := w.Write("Summarize", "roundtable", sampleFindings(3))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("output empty — fallback failed")
	}
	if !strings.Contains(out, "Conclusion number A") {
		t.Errorf("expected a finding conclusion in fallback, got:\n%s", out)
	}
}

func TestWriteEmptyFindings(t *testing.T) {
	w := New(nil, &scriptProv{}, "m", Options{})
	out, err := w.Write("x", "roundtable", nil)
	if err != nil || out != "" {
		t.Errorf("expected empty output for no findings, got %q err %v", out, err)
	}
}

func TestWriterPullsSpecificsAtSynthesis(t *testing.T) {
	// The drafter must pull figures for its section at synthesis time (seeded), so a
	// Specifics func is invoked and its figure reaches the drafter — proving the
	// on-demand-at-synthesis path fires without findings pre-stuffing.
	var calls int
	var sawFigure bool
	prov := &capturingProv{onUser: func(s string) {
		if strings.Contains(s, "$7,800,000") {
			sawFigure = true
		}
	}}
	opt := Options{MaxFindingsPerSec: 2, Specifics: func(topic string, topK int) []SpecificHit {
		calls++
		return []SpecificHit{{Text: "Excess profits allocated to Oceanic Fund I LP\t$7,800,000", Source: "exhibit.xlsx", Context: "Sheet: Summary"}}
	}}
	w := New(nil, prov, "m", opt)
	if _, err := w.Write("Summarize the matter", "roundtable", sampleFindings(3)); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("Specifics was never called at synthesis")
	}
	if !sawFigure {
		t.Error("the seeded figure ($7,800,000) was not injected into a drafter prompt")
	}
}

func TestAttachKeyFigures(t *testing.T) {
	hits := []SpecificHit{
		{Text: "Excess profits allocated to Oceanic Fund I LP\t$7,800,000", Source: "exhibit.xlsx"},
		{Text: "Chao personal account profitable allocation rate\t81.6%", Source: "exhibit.xlsx"},
	}
	// Narrative already states $7,800,000 → that figure is NOT re-listed; the
	// un-stated 81.6% IS appended, so the figure lands by construction.
	text := "The cherry-picking scheme produced $7,800,000 in excess profits to Oceanic Fund."
	out := attachKeyFigures(text, hits)
	if !strings.Contains(out, "Key figures") {
		t.Fatal("expected a Key figures block")
	}
	if !strings.Contains(out, "81.6%") {
		t.Error("the un-stated 81.6% figure was not attached")
	}
	if strings.Count(out, "$7,800,000") != 1 {
		t.Error("the already-stated $7,800,000 should not be duplicated")
	}
	// No hits → text unchanged.
	if attachKeyFigures("x", nil) != "x" {
		t.Error("no hits should leave text unchanged")
	}
}

// capturingProv records each user message and returns a fixed draft (no tool use),
// so a test can assert what the drafter was shown.
type capturingProv struct{ onUser func(string) }

func (p *capturingProv) Chat(params providers.ChatParams) (*providers.ChatResponse, error) {
	for _, m := range params.Messages {
		if m.Role == "user" {
			if s, ok := m.Content.(string); ok && p.onUser != nil {
				p.onUser(s)
			}
		}
	}
	return &providers.ChatResponse{StopReason: providers.StopEndTurn, Content: []providers.ContentBlock{{Type: providers.BlockText, Text: "Section prose."}}}, nil
}

func TestSearchScopedCoverage(t *testing.T) {
	ix := NewFindingIndex(nil, sampleFindings(5))
	allow := map[string]bool{"a": true, "c": true}
	got := ix.SearchScoped("matter", 0, allow)
	if len(got) != 2 {
		t.Fatalf("scoped search returned %d, want 2 (only allowed)", len(got))
	}
	for _, f := range got {
		if !allow[f.ID] {
			t.Errorf("scoped search leaked finding %s outside the partition", f.ID)
		}
	}
}

func TestChunkFindings(t *testing.T) {
	fs := sampleFindings(5)
	parts := chunkFindings(fs, 2)
	if len(parts) != 3 || len(parts[0]) != 2 || len(parts[2]) != 1 {
		t.Errorf("chunkFindings split wrong: %v", func() []int {
			n := []int{}
			for _, p := range parts {
				n = append(n, len(p))
			}
			return n
		}())
	}
	// total preserved (coverage)
	tot := 0
	for _, p := range parts {
		tot += len(p)
	}
	if tot != 5 {
		t.Errorf("chunkFindings dropped findings: total %d", tot)
	}
}

func TestParsePlanLine(t *testing.T) {
	cases := []struct {
		in    string
		n     int
		title string
	}{
		{"[1] Allegations — the SEC claims", 1, "Allegations"},
		{"[2] Financial Harm - the losses", 2, "Financial Harm"},
		{"[3] Compliance: the failures", 3, "Compliance"},
		{"1. Cherry-Picking", 1, "Cherry-Picking"},
		{"2) Directed Brokerage — the kickback", 2, "Directed Brokerage"},
		{"- [4] Recordkeeping", 4, "Recordkeeping"},
		{"**5.** Obstruction: deletion of records", 5, "Obstruction"},
		{"no number here", 0, ""},
	}
	for _, c := range cases {
		n, title, _ := parsePlanLine(c.in)
		if n != c.n || title != c.title {
			t.Errorf("parsePlanLine(%q) = (%d,%q), want (%d,%q)", c.in, n, title, c.n, c.title)
		}
	}
}

func TestLabelClustersDistinct(t *testing.T) {
	// Both clusters share "violations"; each has a distinctive theme. TF-IDF must
	// surface the distinctive term, not the shared one, and labels must differ.
	clusters := [][]Finding{
		{{Content: "cherry-picking allocation violations in trading accounts"}, {Content: "allocation cherry-picking violations favoring accounts"}},
		{{Content: "recordkeeping violations deleting books and records"}, {Content: "books recordkeeping violations missing records"}},
	}
	labels := labelClusters(clusters)
	if labels[0] == labels[1] {
		t.Fatalf("labels not distinct: %v", labels)
	}
	if !strings.Contains(strings.ToLower(labels[0]), "cherry") && !strings.Contains(strings.ToLower(labels[0]), "allocation") {
		t.Errorf("cluster 0 label should reflect its distinctive theme, got %q", labels[0])
	}
	if !strings.Contains(strings.ToLower(labels[1]), "record") && !strings.Contains(strings.ToLower(labels[1]), "books") {
		t.Errorf("cluster 1 label should reflect its distinctive theme, got %q", labels[1])
	}
}

func TestBatchByTokens(t *testing.T) {
	blocks := []string{strings.Repeat("a ", 100), strings.Repeat("b ", 100), strings.Repeat("c ", 100)}
	// budget that holds ~2 blocks (each ~100 tokens via 3.5 chars/token est).
	batches := batchByTokens(blocks, 70)
	tot := 0
	for _, b := range batches {
		tot += len(b)
	}
	if tot != 3 {
		t.Errorf("batchByTokens dropped blocks: total %d of 3", tot)
	}
	if len(batches) < 2 {
		t.Errorf("expected blocks split across batches, got %d batch(es)", len(batches))
	}
}
