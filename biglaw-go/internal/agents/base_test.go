// SPDX-License-Identifier: Apache-2.0
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

// Partner-review regression: a "conclusion" that is pure meta-dialogue — the agent
// narrating its process or arguing about its role — must never enter the finding pool.
// Every downstream renderer (writer sections, fallbacks, rosters) trusts finding content.
func TestParseFindings_MetaDialogueFiltered(t *testing.T) {
	in := `FINDING:
Conclusion: Let me extract the specific figures and details from the source documents:
Confidence: 0.8
END_FINDING
FINDING:
Conclusion: I appreciate the detailed correction, but I need to clarify my role here before drafting the section.
Confidence: 0.7
END_FINDING
FINDING:
Evidence: SOURCE=referral-1 | QUOTE=excess profits of $7,800,000 were allocated to Oceanic Fund I LP
Conclusion: The Division quantifies the cherry-picking harm at $7,800,000 allocated to Oceanic Fund I LP.
Confidence: 0.9
END_FINDING`
	fs := parseFindings(in, testDef)
	if len(fs) != 1 {
		t.Fatalf("want 1 substantive finding after meta filtering, got %d: %+v", len(fs), fs)
	}
	if !strings.Contains(fs[0].Content, "$7,800,000") {
		t.Errorf("the substantive finding was dropped: %q", fs[0].Content)
	}
	// A substantive conclusion that merely QUOTES first-person source text mid-sentence
	// is NOT meta-dialogue.
	if isMetaDialogueConclusion(`Whitmore's email stating "I will handle the cleanup" evidences the instruction.`) {
		t.Error("mid-sentence quoted first person misflagged as meta-dialogue")
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

// The current Conclusion/Evidence labels parse, and the conclusion is kept
// separate from the verbatim evidence quote.
func TestParseFindings_ConclusionEvidence(t *testing.T) {
	in := `FINDING:
Conclusion: The wage level is inconsistent with the offer letter.
Evidence: SOURCE=lca-1 | QUOTE=Level II wage $95,000 | PAGE=3
Confidence: 0.8
END_FINDING`
	fs := parseFindings(in, testDef)
	if len(fs) != 1 {
		t.Fatalf("want 1 finding, got %d", len(fs))
	}
	if fs[0].Content != "The wage level is inconsistent with the offer letter." {
		t.Errorf("conclusion: %q", fs[0].Content)
	}
	if len(fs[0].Citations) != 1 || fs[0].Citations[0].Source != "lca-1" || fs[0].Citations[0].Quote != "Level II wage $95,000" {
		t.Errorf("evidence not parsed: %+v", fs[0].Citations)
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

// workedExample returns the FINDING block the processing prompt shows the model,
// taken from the live prompt so this test tracks the format agents are told to
// emit (Evidence → Conclusion → Confidence).
func workedExample(t *testing.T) string {
	t.Helper()
	p := buildProcessingPrompt(testDef, AgentContext{})
	_, after, ok := strings.Cut(p, "Worked example:\n")
	if !ok {
		t.Fatal("processing prompt has no worked example")
	}
	block, _, ok := strings.Cut(after, "END_FINDING")
	if !ok {
		t.Fatal("worked example has no END_FINDING")
	}
	return block + "END_FINDING"
}

// Regression: the strict citation used to end only at Evidence:/Citation:/
// Confidence:/END_FINDING, so in the prompt's own Evidence → Conclusion order the
// QUOTE swallowed "| PAGE=7\nConclusion: …" and failed verbatim verification, and
// the terminator consumed a following "Evidence:" label so every second
// consecutive Evidence line was dropped.
func TestParseFindings_EvidenceThenConclusion(t *testing.T) {
	const (
		wantQuote = "Employee shall not engage in any competing business for two years"
		wantConcl = "The non-compete clause is unenforceable in California under Bus. & Prof. Code §16600, which voids contracts restraining lawful trade."
	)
	type cite struct {
		source, quote string
		page          int // 0 = no page
	}
	cases := []struct {
		name  string
		in    string
		concl string
		cites []cite
	}{
		{
			name:  "prompt worked example",
			in:    workedExample(t),
			concl: wantConcl,
			cites: []cite{{"employment-agreement-2024", wantQuote, 7}},
		},
		{
			name: "no PAGE, no END_FINDING",
			in: "FINDING:\nEvidence: SOURCE=emp | QUOTE=" + wantQuote +
				"\nConclusion: Void.\nConfidence: 0.8",
			concl: "Void.",
			cites: []cite{{"emp", wantQuote, 0}},
		},
		{
			name: "consecutive Evidence lines all kept",
			in: "FINDING:\nEvidence: SOURCE=a | QUOTE=alpha beta | PAGE=1\n" +
				"Evidence: SOURCE=b | QUOTE=gamma delta | PAGE=2\n" +
				"Evidence: SOURCE=c | QUOTE=epsilon\n" +
				"Conclusion: Three sources agree.\nConfidence: 0.9\nEND_FINDING",
			concl: "Three sources agree.",
			cites: []cite{{"a", "alpha beta", 1}, {"b", "gamma delta", 2}, {"c", "epsilon", 0}},
		},
		{
			name: "markdown-decorated labels",
			in: "FINDING:\n- **Evidence:** SOURCE=emp | QUOTE=" + wantQuote + " | PAGE=7\n" +
				"- **Conclusion:** Void.\n- Confidence: 0.8\nEND_FINDING",
			concl: "Void.",
			cites: []cite{{"emp", wantQuote, 7}},
		},
		{
			name: "numbered Evidence lines",
			in: "FINDING:\n1. Evidence: SOURCE=a | QUOTE=alpha beta | PAGE=1\n" +
				"2) Evidence: SOURCE=b | QUOTE=gamma delta\nConclusion: Both agree.\nEND_FINDING",
			concl: "Both agree.",
			cites: []cite{{"a", "alpha beta", 1}, {"b", "gamma delta", 0}},
		},
		{
			name: "dropped Conclusion label after PAGE",
			in: "FINDING:\nEvidence: SOURCE=emp | QUOTE=" + wantQuote + " | PAGE=7\n" +
				"The clause is void.\nConfidence: 0.8\nEND_FINDING",
			concl: "The clause is void.",
			cites: []cite{{"emp", wantQuote, 7}},
		},
		{
			name: "Windows CRLF line endings",
			in: "FINDING:\r\nEvidence: SOURCE=a | QUOTE=alpha beta | PAGE=7\r\n" +
				"Evidence: SOURCE=b | QUOTE=gamma delta\r\nConclusion: Void.\r\nConfidence: 0.8\r\nEND_FINDING\r\n",
			concl: "Void.",
			cites: []cite{{"a", "alpha beta", 7}, {"b", "gamma delta", 0}},
		},
		{
			name: "Evidence on the FINDING line",
			in: "**FINDING 1:** Evidence: SOURCE=emp | QUOTE=" + wantQuote + " | PAGE=7\n" +
				"Conclusion: Void.\nEND_FINDING",
			concl: "Void.",
			cites: []cite{{"emp", wantQuote, 7}},
		},
		{
			name: "Evidence trailing the Conclusion inline",
			in: "FINDING:\nConclusion: Void. Evidence: SOURCE=emp | QUOTE=" + wantQuote +
				" | PAGE=7\nConfidence: 0.8\nEND_FINDING",
			concl: "Void. Evidence: SOURCE=emp | QUOTE=" + wantQuote + " | PAGE=7",
			cites: []cite{{"emp", wantQuote, 7}},
		},
		{
			name: "Evidence inline in unlabelled prose",
			in: "FINDING:\nThe clause is void. Evidence: SOURCE=emp | QUOTE=" + wantQuote +
				" Confidence: 0.8 END_FINDING",
			concl: "The clause is void. Evidence: SOURCE=emp | QUOTE=" + wantQuote + " Confidence: 0.8",
			cites: []cite{{"emp", wantQuote, 0}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := parseFindings(tc.in, testDef)
			if len(fs) != 1 {
				t.Fatalf("want 1 finding, got %d", len(fs))
			}
			if fs[0].Content != tc.concl {
				t.Errorf("conclusion: %q want %q", fs[0].Content, tc.concl)
			}
			got := fs[0].Citations
			if len(got) != len(tc.cites) {
				t.Fatalf("want %d citations, got %d: %+v", len(tc.cites), len(got), got)
			}
			for i, w := range tc.cites {
				if got[i].Source != w.source || got[i].Quote != w.quote {
					t.Errorf("cite %d: source=%q quote=%q want %q / %q", i, got[i].Source, got[i].Quote, w.source, w.quote)
				}
				switch {
				case w.page == 0 && got[i].Page != nil:
					t.Errorf("cite %d: unexpected page %d", i, *got[i].Page)
				case w.page != 0 && (got[i].Page == nil || *got[i].Page != w.page):
					t.Errorf("cite %d: page %v want %d", i, got[i].Page, w.page)
				}
			}
		})
	}
}

// A "Confidence:" that appears inside a quote (or an unparseable labelled
// Confidence line) must not override the agent's own Confidence value.
func TestParseFindings_ConfidenceFromLabelledLine(t *testing.T) {
	cases := map[string]float64{
		"FINDING:\nEvidence: SOURCE=a | QUOTE=The form reads Confidence: 0.2 here.\nConclusion: Low.\nConfidence: 0.9\nEND_FINDING":  0.9,
		"FINDING:\nEvidence: SOURCE=a | QUOTE=x y\nConfidence: high, per the witness\nConclusion: c.\nConfidence: 0.85\nEND_FINDING": 0.85,
		"FINDING:\n**Conclusion:** c.\n**Confidence:** 0.6\nEND_FINDING":                                                             0.6,
		"FINDING: Conclusion: c. Confidence: 0.4 END_FINDING":                                                                        0.4,
		"FINDING:\nConclusion: c.\nConfidence: 7\nEND_FINDING":                                                                       0.7,
	}
	for in, want := range cases {
		fs := parseFindings(in, testDef)
		if len(fs) != 1 || fs[0].Confidence != want {
			t.Errorf("%q: got %+v want confidence %v", in, fs, want)
		}
	}
}
