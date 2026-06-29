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
	var sawHandle, sawRawDigit bool
	prov := &capturingProv{onUser: func(s string) {
		// The figure reaches the drafter as a MASKED HANDLE: its context is shown with the
		// digit replaced by a neutral name (Zephyr = figureHandles[0]), so the model never
		// reads "$7,800,000" but knows the name refers to the Oceanic excess-profits figure.
		if strings.Contains(s, "Oceanic Fund I LP") && strings.Contains(s, "Zephyr") {
			sawHandle = true
		}
		if strings.Contains(s, "$7,800,000") {
			sawRawDigit = true
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
	if !sawHandle {
		t.Error("the seeded figure was not presented to a drafter as a masked handle")
	}
	if sawRawDigit {
		t.Error("the raw digit ($7,800,000) leaked into the drafter prompt — masking failed")
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
	if attachKeyFigures("x", nil) != "x" {
		t.Error("no hits should leave text unchanged")
	}
}

func TestAttachKeyFiguresYearLeadTrap(t *testing.T) {
	// THE BUG: rows lead with a year. A narrative mentioning the review-period year
	// must NOT suppress the row — the salient $ figure isn't stated, so it must land.
	hits := []SpecificHit{
		{Text: "Excess profits from cherry-picking allocated to Oceanic Fund I LP (2021-2023)\t$7,800,000", Source: "x.xlsx"},
	}
	narrative := "The Review Period spans January 1, 2021 to December 31, 2023." // mentions 2021/2023
	out := attachKeyFigures(narrative, hits)
	if !strings.Contains(out, "$7,800,000") {
		t.Errorf("year-lead row was wrongly suppressed; $7,800,000 must be attached:\n%s", out)
	}
}

func TestDedupeFindings(t *testing.T) {
	in := []Finding{
		{ID: "a", Content: "The Division of Examinations commenced its examination of WCA on March 11, 2024, and concluded on August 2, 2024."},
		{ID: "b", Content: "The Division of Examinations commenced its examination of WCA on March 11, 2024, and concluded on August 2, 2024 (restated)."},
		{ID: "c", Content: "Excess profits allocated to Oceanic Fund I LP: $7,800,000."},
	}
	if out := dedupeFindings(in); len(out) != 2 {
		t.Fatalf("expected 2 after dedup (near-identical timeline paras collapse), got %d", len(out))
	}
}

func TestSalientFigureCitationAware(t *testing.T) {
	cases := map[string]string{
		"Excess profits to Oceanic Fund I LP (2021-2023) $7,800,000": "$7,800,000",
		"Chao personal account profitable allocation rate 81.6%":     "81.6%",
		"312 Microsoft Excel spreadsheets with backdated metadata":   "312",
		"in violation of Sections 206(1) and 206(2) of the Act":      "", // citation, not a figure
		"as required by Rule 204-2 under the Advisers Act":           "", // citation
	}
	for in, want := range cases {
		if got := salientFigure(in); got != want {
			t.Errorf("salientFigure(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAttachKeyFiguresSelective(t *testing.T) {
	hits := []SpecificHit{
		{Text: "Excess profits to Oceanic Fund I LP (2021-2023) $7,800,000", Source: "x.xlsx"},
		{Text: "Excess profits to Oceanic Fund I LP duplicate row $7,800,000", Source: "x.xlsx"},
		{Text: "in violation of Sections 206(1) and 206(2)", Source: "y.docx"},
	}
	out := attachKeyFigures("Some prose with no figures.", hits)
	if strings.Count(out, "$7,800,000") != 1 {
		t.Errorf("expected the $ figure once (deduped by figure):\n%s", out)
	}
	if strings.Contains(out, "206") {
		t.Errorf("citation row should not appear in Key figures:\n%s", out)
	}
	if strings.Contains(out, "duplicate row") {
		t.Errorf("raw duplicate row leaked into output:\n%s", out)
	}
}

func TestResolveFigurePlaceholders(t *testing.T) {
	figs := []SpecificHit{
		{Text: "Chao personal account profitable allocation rate\t81.6%", Source: "x.xlsx"},
		{Text: "Excess profits allocated to Oceanic Fund I LP\t$7,800,000", Source: "x.xlsx"},
	}
	text := "Chao's account showed a rate of {{FIG: Chao personal account profitable allocation rate}}, and {{FIG: Oceanic Fund excess profits}} in excess profits."
	out := resolveFigurePlaceholders(text, figs)
	if !strings.Contains(out, "81.6%") {
		t.Errorf("rate placeholder not resolved to grounded figure: %s", out)
	}
	if !strings.Contains(out, "$7,800,000") {
		t.Errorf("amount placeholder not resolved: %s", out)
	}
	if strings.Contains(out, "{{FIG") {
		t.Errorf("placeholders left unresolved: %s", out)
	}
	// The model can never inject a hallucinated value (68.6%) — only grounded figures
	// appear, because the number comes from figs, not the model.
	if strings.Contains(out, "68.6") {
		t.Error("a non-grounded value appeared — impossible via placeholders")
	}
	// Unmatched placeholder is DROPPED (never guessed), leaving clean prose.
	un := resolveFigurePlaceholders("a value of {{FIG: something not in the figures}} here", figs)
	if strings.Contains(un, "{{FIG") || strings.Contains(un, "81.6") {
		t.Errorf("unmatched placeholder mishandled: %q", un)
	}
}

func TestSalientFigure(t *testing.T) {
	cases := map[string]string{
		"Oceanic Fund I LP (2021-2023) $7,800,000": "$7,800,000",
		"profitable allocation rate 81.6%":         "81.6%",
		"4,217 equity trades analyzed in 2023":     "4,217", // longest non-year number, not 2023
		"filed on March 28, 2023":                  "28",    // no $/%, "2023" is a year → "28"
	}
	for in, want := range cases {
		if got := salientFigure(in); got != want {
			t.Errorf("salientFigure(%q)=%q want %q", in, got, want)
		}
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
