// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agents

import (
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

// scriptedProvider replays canned responses and records every user prompt, so
// tests can drive the staged extractor without a live model.
type scriptedProvider struct {
	prompts   []string
	responses []string
}

func (s *scriptedProvider) Chat(p providers.ChatParams) (*providers.ChatResponse, error) {
	var user string
	if len(p.Messages) > 0 {
		if str, ok := p.Messages[len(p.Messages)-1].Content.(string); ok {
			user = str
		}
	}
	s.prompts = append(s.prompts, user)
	resp := ""
	if idx := len(s.prompts) - 1; idx < len(s.responses) {
		resp = s.responses[idx]
	} else if len(s.responses) > 0 {
		resp = s.responses[len(s.responses)-1]
	}
	return &providers.ChatResponse{
		StopReason: providers.StopEndTurn,
		Content:    []providers.ContentBlock{{Type: providers.BlockText, Text: resp}},
	}, nil
}

type fakeRegistry struct{ p providers.Provider }

func (f fakeRegistry) Get(string) (providers.Provider, error) { return f.p, nil }

// smallCfg mimics Load()'s defaults for the caps knobs (auto everywhere).
func smallCfg() *config.Config {
	c := &config.Config{}
	c.Agents.MaxToolResultTokens = -1
	return c
}

func newTestAgent(prov providers.Provider) *Agent {
	return NewAgent(testDef, smallCfg(), fakeRegistry{prov}, &cost.Store{})
}

// ─── Fact-bearing extraction regressions ─────────────────────────────────────

// Colon-cut regression: the $92,600 case. A total that follows a colon-introduced
// component list must survive the lock as ONE quote — extraction previously cut
// at the colon, one clause before the figures.
func TestExtractEvidence_ColonCutCarriesThroughTotal(t *testing.T) {
	quote := "In exchange for directing brokerage to Meridian, Hargrove received the following: (a) an initiation fee of $45,000; (b) four quarterly payments of $11,900, totaling $47,600 — a combined $92,600 in undisclosed compensation."
	passage := quote + " The arrangement was never disclosed in the Form ADV."

	prov := &scriptedProvider{responses: []string{"[1] QUOTE: " + quote}}
	a := newTestAgent(prov)
	caps := capsFor(a.cfg, "ollama:qwen2.5:14b")

	ev := a.extractEvidenceBatch("undisclosed compensation", []retrievedPassage{{source: "referral-1", text: passage}}, "ollama:qwen2.5:14b", "t1", caps)
	if len(ev) != 1 {
		t.Fatalf("want 1 evidence, got %d", len(ev))
	}
	for _, must := range []string{"$45,000", "$11,900", "$47,600", "$92,600"} {
		if !strings.Contains(ev[0].quote, must) {
			t.Errorf("locked quote lost %q: %q", must, ev[0].quote)
		}
	}
	if ev[0].source != "referral-1" {
		t.Errorf("source: %q", ev[0].source)
	}
	// The prompt must carry the anti-colon-cut instruction (the behavioural fix)
	// and must no longer impose the old 2-sentence ceiling.
	if len(prov.prompts) == 0 || !strings.Contains(prov.prompts[0], "colon") {
		t.Errorf("extraction prompt lacks the carry-through-the-colon instruction")
	}
	if strings.Contains(prov.prompts[0], "up to 2 complete sentences") {
		t.Errorf("extraction prompt still imposes the 2-sentence cap")
	}
}

// Off-by-one regression: the "forty-seven (47) deleted files" case. The
// number-bearing sentence and its elaboration are BOTH extracted (multiple
// QUOTE lines per passage are legal).
func TestExtractEvidence_NumberSentenceAndElaboration(t *testing.T) {
	s1 := "Forensic imaging identified forty-seven (47) deleted files on the laptop."
	s2 := "The deletions occurred within hours of the litigation hold notice."
	passage := s1 + " " + s2

	prov := &scriptedProvider{responses: []string{"[1] QUOTE: " + s1 + "\n[1] QUOTE: " + s2}}
	a := newTestAgent(prov)
	caps := capsFor(a.cfg, "ollama:qwen2.5:14b")

	ev := a.extractEvidenceBatch("spoliation", []retrievedPassage{{source: "forensics", text: passage}}, "ollama:qwen2.5:14b", "t1", caps)
	if len(ev) != 2 {
		t.Fatalf("want both sentences extracted, got %d: %+v", len(ev), ev)
	}
	if !strings.Contains(ev[0].quote, "forty-seven (47)") {
		t.Errorf("number-bearing sentence missing: %q", ev[0].quote)
	}
}

// Passages beyond one call's batch roll into further extraction calls — they
// are chunked through the stages, not truncated away.
func TestExtractEvidence_BatchesBeyondPerCallCap(t *testing.T) {
	var passages []retrievedPassage
	for i := 1; i <= 12; i++ {
		passages = append(passages, retrievedPassage{
			source: fmt.Sprintf("doc-%d", i),
			text:   fmt.Sprintf("Fact %d: the recorded amount was $%d00.", i, i),
		})
	}
	prov := &scriptedProvider{responses: []string{
		"[1] QUOTE: Fact 1: the recorded amount was $100.",
		// Second call re-numbers from 1: its passage 1 is global passage 9.
		"[1] QUOTE: Fact 9: the recorded amount was $900.",
	}}
	a := newTestAgent(prov)
	caps := capsFor(a.cfg, "ollama:qwen2.5:14b") // passagesPerCall = 8

	ev := a.extractEvidenceBatch("amounts", passages, "ollama:qwen2.5:14b", "t1", caps)
	if len(prov.prompts) != 2 {
		t.Fatalf("want 2 extraction calls for 12 passages at 8/call, got %d", len(prov.prompts))
	}
	found := false
	for _, e := range ev {
		if strings.Contains(e.quote, "Fact 9") {
			found = true
			if e.source != "doc-9" {
				t.Errorf("second-batch source: %q want doc-9", e.source)
			}
		}
	}
	if !found {
		t.Errorf("evidence from the second batch never arrived: %+v", ev)
	}
}

// ─── Tool-result shapes reaching the evidence pool ───────────────────────────

// A text-shaped tool result (read_document / read_section) must reach the
// evidence pool, chunked, with the document attributed — previously it
// contributed ZERO passages.
func TestExtractPassages_TextShaped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&b, "Paragraph %d of the referral describes conduct in detail.\n", i+1)
	}
	marker := "The respondent transferred $781,000 to an account ending 74892."
	b.WriteString(marker + "\n")

	ps := extractPassages("read_document", map[string]interface{}{"doc_id": "referral-1"},
		map[string]interface{}{"text": b.String()}, 100)
	if len(ps) < 2 {
		t.Fatalf("long text should chunk into multiple passages, got %d", len(ps))
	}
	var joined strings.Builder
	for _, p := range ps {
		if p.source != "referral-1" {
			t.Fatalf("source: %q want referral-1", p.source)
		}
		joined.WriteString(p.text)
		joined.WriteString("\n")
	}
	if !strings.Contains(normalizeWS(joined.String()), normalizeWS(marker)) {
		t.Errorf("tail of the document was truncated away — marker sentence missing")
	}
}

// The snippet-shaped result path (search_chunks et al.) is unchanged.
func TestExtractPassages_ResultsShaped(t *testing.T) {
	res := map[string]interface{}{"results": []map[string]interface{}{
		{"snippet": "The fee schedule lists 1.5% on assets.", "title": "Advisory Agreement", "context": "Sheet1 | Fee | Rate"},
	}}
	ps := extractPassages("search_chunks", nil, res, 450)
	if len(ps) != 1 || ps[0].source != "Advisory Agreement" || ps[0].context == "" {
		t.Fatalf("results-shaped extraction regressed: %+v", ps)
	}
}

// find_in_document excerpts count as passages too.
func TestExtractPassages_MatchesShaped(t *testing.T) {
	res := map[string]interface{}{
		"docId": "agmt-2",
		"matches": []map[string]interface{}{
			{"position": 10, "excerpt": "termination fee of $250,000 is payable"},
		},
	}
	ps := extractPassages("find_in_document", map[string]interface{}{"doc_id": "agmt-2"}, res, 450)
	if len(ps) != 1 || ps[0].source != "agmt-2" || !strings.Contains(ps[0].text, "$250,000") {
		t.Fatalf("matches-shaped extraction failed: %+v", ps)
	}
}

// chunkTextByTokens yields verbatim slices only — every chunk must
// substring-verify against the source after whitespace normalization.
func TestChunkTextByTokens_Verbatim(t *testing.T) {
	long := strings.Repeat("An unusually long unbroken line of prose about the omnibus account allocations and totals. ", 40)
	text := "Line one.\n" + long + "\nLine three."
	src := normalizeWS(text)
	for _, c := range chunkTextByTokens(text, 60) {
		if !strings.Contains(src, normalizeWS(c)) {
			t.Fatalf("chunk is not verbatim source text: %q", c)
		}
	}
}

// fakeToolRegistry serves minimal schemas and canned results for loop tests.
type fakeToolRegistry struct {
	results map[string]interface{}
}

func (f *fakeToolRegistry) SchemasFor(names []string) []providers.ToolParam {
	out := make([]providers.ToolParam, len(names))
	for i, n := range names {
		out[i] = providers.ToolParam{Name: n}
	}
	return out
}

func (f *fakeToolRegistry) Execute(name string, _ map[string]interface{}, _ ToolContext) (interface{}, error) {
	return f.results[name], nil
}

// loopProvider replays canned ChatResponses and records every ChatParams, so a
// test can inspect exactly what re-entered the loop context.
type loopProvider struct {
	params    []providers.ChatParams
	responses []*providers.ChatResponse
}

func (l *loopProvider) Chat(p providers.ChatParams) (*providers.ChatResponse, error) {
	l.params = append(l.params, p)
	idx := len(l.params) - 1
	if idx >= len(l.responses) {
		idx = len(l.responses) - 1
	}
	return l.responses[idx], nil
}

// A text-shaped tool result driven through the LOOP must be harvested into the
// evidence pool BEFORE the loop-context truncation: the model's own context gets
// a bounded view of read_document, the staged extractor sees all of it. This is
// the transcription-funnel fix end-to-end — previously the full text was
// truncated at the tool boundary and the tail never existed anywhere.
func TestAgenticLoop_TextResultReachesEvidencePool(t *testing.T) {
	var doc strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&doc, "Background paragraph %d recites the procedural history at length.\n", i+1)
	}
	marker := "The respondent transferred $781,000 to an account ending 74892."
	doc.WriteString(marker + "\n")

	prov := &loopProvider{responses: []*providers.ChatResponse{
		{StopReason: providers.StopToolUse, Content: []providers.ContentBlock{{
			Type: providers.BlockToolUse, ID: "tu1", Name: "read_document",
			Input: map[string]interface{}{"doc_id": "referral-1"},
		}}},
		{StopReason: providers.StopEndTurn, Content: []providers.ContentBlock{{Type: providers.BlockText, Text: "done"}}},
	}}
	reg := &fakeToolRegistry{results: map[string]interface{}{
		"read_document": map[string]interface{}{"text": doc.String()},
	}}

	cfg := smallCfg()
	cfg.Agents.MaxToolIterations = 4
	a := NewAgent(testDef, cfg, fakeRegistry{prov}, &cost.Store{})
	caps := capsFor(cfg, "ollama:qwen2.5:14b") // toolResultTokens = 1500 (legacy small cap)

	ctx := AgentContext{TaskID: "t1", ToolRegistry: reg}
	passages, _, err := a.runAgenticLoop("go", 500, "ollama:qwen2.5:14b", ctx, []string{"read_document"}, caps)
	if err != nil {
		t.Fatal(err)
	}
	if len(passages) < 2 {
		t.Fatalf("full document should chunk into multiple passages, got %d", len(passages))
	}
	var joined strings.Builder
	for _, p := range passages {
		if p.source != "referral-1" {
			t.Fatalf("passage source %q, want referral-1", p.source)
		}
		joined.WriteString(p.text)
		joined.WriteString("\n")
	}
	if !strings.Contains(normalizeWS(joined.String()), normalizeWS(marker)) {
		t.Errorf("tail marker never reached the evidence pool")
	}

	// The loop context, by contrast, got the truncated view: the tool_result fed
	// back to the model is bounded and does NOT carry the tail marker.
	if len(prov.params) < 2 {
		t.Fatalf("want 2 provider calls, got %d", len(prov.params))
	}
	second := prov.params[1].Messages
	blocks, ok := second[len(second)-1].Content.([]providers.ContentBlock)
	if !ok || len(blocks) == 0 || blocks[0].Type != providers.BlockToolResult {
		t.Fatalf("second call should end with tool_result blocks")
	}
	if !strings.HasSuffix(blocks[0].Content, "…[truncated]") {
		t.Errorf("loop-context tool result was not truncated at the small cap")
	}
	if strings.Contains(blocks[0].Content, marker) {
		t.Errorf("tail marker inside the truncated loop view — test premise broken")
	}
}

// ─── Context-aware caps ──────────────────────────────────────────────────────

func TestCapsFor_SmallModelKeepsConservativeDefaults(t *testing.T) {
	caps := capsFor(smallCfg(), "ollama:qwen2.5:14b")
	if caps.toolResultTokens != 1500 || caps.passagesPerCall != 8 || caps.maxEvidence != 8 {
		t.Errorf("small-context caps regressed the 14B path: %+v", caps)
	}
}

func TestCapsFor_LargeModelExpands(t *testing.T) {
	caps := capsFor(smallCfg(), "qwen-max") // bare stack ID → 128K-class
	if caps.toolResultTokens <= 1500 || caps.passagesPerCall <= 8 || caps.maxEvidence <= 8 || caps.quoteTokens <= 130 {
		t.Errorf("large-context caps did not expand: %+v", caps)
	}
	if caps.toolResultTokens != 16384 || caps.passagesPerCall != 24 || caps.maxEvidence != 48 || caps.quoteTokens != 600 {
		t.Errorf("large-context caps drifted from the derived values: %+v", caps)
	}
}

func TestCapsFor_EnvOverridesWin(t *testing.T) {
	cfg := smallCfg()
	cfg.Agents.MaxToolResultTokens = 2000
	cfg.Agents.MaxEvidencePerAgent = 4
	cfg.Agents.MaxEvidencePassages = 3
	cfg.Agents.EvidenceQuoteTokens = 200
	caps := capsFor(cfg, "qwen-max")
	if caps.toolResultTokens != 2000 || caps.maxEvidence != 4 || caps.passagesPerCall != 3 || caps.quoteTokens != 200 {
		t.Errorf("explicit overrides lost to auto: %+v", caps)
	}
	// 0 keeps its documented "uncapped" meaning for the tool-result cap.
	cfg.Agents.MaxToolResultTokens = 0
	if capsFor(cfg, "qwen-max").toolResultTokens != 0 {
		t.Errorf("MaxToolResultTokens=0 (uncapped) not honoured")
	}
}
