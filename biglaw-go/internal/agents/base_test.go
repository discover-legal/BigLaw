// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

var testDef = types.AgentDefinition{ID: "test-agent", Name: "Test Agent"}

// The frontier-model path: exact format with the verifiable SOURCE=/QUOTE= form.
func TestParseFindings_StrictFormat(t *testing.T) {
	in := `Here is my analysis.
FINDING:
Content: The non-compete is void under §16600.
Citation: SOURCE=agreement-1 | QUOTE=shall not compete for two years | PAGE=7
Confidence: 0.9
END_FINDING`
	fs := parseFindings(in, testDef)
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if fs[0].Content != "The non-compete is void under §16600." {
		t.Errorf("content: %q", fs[0].Content)
	}
	if len(fs[0].Citations) != 1 || fs[0].Citations[0].Source != "agreement-1" {
		t.Fatalf("citation not parsed: %+v", fs[0].Citations)
	}
	if fs[0].Citations[0].Quote != "shall not compete for two years" {
		t.Errorf("quote: %q", fs[0].Citations[0].Quote)
	}
	if fs[0].Citations[0].Page == nil || *fs[0].Citations[0].Page != 7 {
		t.Errorf("page not parsed: %+v", fs[0].Citations[0].Page)
	}
	if fs[0].Confidence != 0.9 {
		t.Errorf("confidence: %v", fs[0].Confidence)
	}
}

// Cheap-model path #1: the model drops the END_FINDING terminator entirely.
// Pre-fix this yielded zero findings; now the block runs to end of text.
func TestParseFindings_MissingEndMarker(t *testing.T) {
	in := `FINDING:
Content: The LCA wage level is inconsistent with the offer letter.
Citation: SOURCE=lca-1 | QUOTE=Level II wage $95,000
Confidence: 0.8`
	fs := parseFindings(in, testDef)
	if len(fs) != 1 {
		t.Fatalf("want 1 finding without END_FINDING, got %d", len(fs))
	}
	if fs[0].Content != "The LCA wage level is inconsistent with the offer letter." {
		t.Errorf("content: %q", fs[0].Content)
	}
	if len(fs[0].Citations) != 1 || fs[0].Citations[0].Source != "lca-1" {
		t.Errorf("citation: %+v", fs[0].Citations)
	}
}

// Cheap-model path #2: no "Content:" label — the finding is written as prose
// directly after the FINDING: header. The body (minus citation/confidence
// lines) becomes the content.
func TestParseFindings_NoContentLabel(t *testing.T) {
	in := `FINDING:
The organizational chart contradicts the roles described in the offer letters.
Citation: SOURCE=org-chart | QUOTE=Director of Engineering | PAGE=2
Confidence: 0.75
END_FINDING`
	fs := parseFindings(in, testDef)
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	want := "The organizational chart contradicts the roles described in the offer letters."
	if fs[0].Content != want {
		t.Errorf("content: %q want %q", fs[0].Content, want)
	}
	if len(fs[0].Citations) != 1 {
		t.Errorf("citations: %+v", fs[0].Citations)
	}
}

// Cheap-model path #3: markdown-decorated header and a natural-language
// citation with no SOURCE=/QUOTE=. Content is recovered; the loose citation
// yields a source (and a pulled quote + page) but no verbatim anchor — the gate
// will later flag it.
func TestParseFindings_MarkdownAndLooseCitation(t *testing.T) {
	in := `**FINDING:**
Content: The credential evaluation report omits the required degree equivalency.
Citation: per the "Credential Evaluation Report", page 3
Confidence: 0.6`
	fs := parseFindings(in, testDef)
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if len(fs[0].Citations) != 1 {
		t.Fatalf("loose citation not captured: %+v", fs[0].Citations)
	}
	c := fs[0].Citations[0]
	if c.Quote != "Credential Evaluation Report" {
		t.Errorf("pulled quote: %q", c.Quote)
	}
	if c.Page == nil || *c.Page != 3 {
		t.Errorf("loose page: %+v", c.Page)
	}
}

// Multiple findings, the second missing its terminator — both recovered, and
// the per-agent cap is respected.
func TestParseFindings_MultipleAndCap(t *testing.T) {
	in := `FINDING:
Content: First issue.
Citation: SOURCE=a | QUOTE=alpha
END_FINDING
FINDING:
Content: Second issue.
Citation: SOURCE=b | QUOTE=beta
FINDING:
Content: Third issue.
Citation: SOURCE=c | QUOTE=gamma
FINDING:
Content: Fourth issue (beyond the cap).
Citation: SOURCE=d | QUOTE=delta`
	fs := parseFindings(in, testDef)
	if len(fs) != maxFindingsPerAgentRound {
		t.Fatalf("want %d findings (capped), got %d", maxFindingsPerAgentRound, len(fs))
	}
	if fs[1].Content != "Second issue." {
		t.Errorf("second content: %q", fs[1].Content)
	}
}

func TestParseFindings_NoFindings(t *testing.T) {
	if fs := parseFindings("I considered the documents. NO_FINDINGS", testDef); fs != nil {
		t.Errorf("want nil for NO_FINDINGS, got %+v", fs)
	}
}

// A block whose content is empty (header + blank) must not produce a finding.
func TestParseFindings_EmptyBlockSkipped(t *testing.T) {
	in := "FINDING:\nConfidence: 0.5\nEND_FINDING"
	if fs := parseFindings(in, testDef); len(fs) != 0 {
		t.Errorf("want 0 findings for empty content, got %d", len(fs))
	}
}

// Confidence out of range is clamped to [0,1].
func TestParseFindings_ConfidenceClamp(t *testing.T) {
	in := "FINDING:\nContent: x.\nConfidence: 1.9\nEND_FINDING"
	fs := parseFindings(in, testDef)
	if len(fs) != 1 || fs[0].Confidence != 1.0 {
		t.Errorf("clamp failed: %+v", fs)
	}
}

// Regression test grounded in REAL qwen2.5:14b output (captured via Ollama).
// The model wraps quotes in "…" and repeats QUOTE= on one line; the parser must
// recover the finding AND strip the wrapping quotes so the verbatim text is a
// substring of the source (otherwise the gate falsely flags it as unverified).
func TestParseFindings_RealQwenOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "qwen25-14b-findings.txt"))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	// The verbatim sentences the model quoted, exactly as they appear in source.
	source := "The Commission alleges that on March 3, 2024, Daniel Hargrove purchased 12,000 shares of Veltrix Corp two days before the public announcement of its acquisition by Orona Holdings. The staff further alleges that Hargrove tipped his brother-in-law, who purchased 4,000 shares the same afternoon. The referral cites Section 10(b) and Rule 10b-5."

	fs := parseFindings(string(raw), testDef)
	if len(fs) == 0 {
		t.Fatalf("real qwen output produced zero findings (the original defect)")
	}
	if len(fs[0].Citations) == 0 {
		t.Fatalf("no citation parsed from real qwen output")
	}
	verifiedAny := false
	for _, c := range fs[0].Citations {
		if c.Quote != "" && strings.Contains(source, c.Quote) {
			verifiedAny = true
		}
	}
	if !verifiedAny {
		t.Errorf("no parsed quote was a verbatim substring of the source — wrapping quotes not stripped; citations=%+v", fs[0].Citations)
	}
}
