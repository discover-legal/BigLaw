// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Regression tests for the matter-4 deliverable corruption (family-law run):
// wrong-constant substitution (one figure filling every slot), unfilled coined
// placeholders shipping as prose, mojibake section titles, and garbled Key-figures
// labels cut mid-clause / mid-rune.

package writer

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// ─── Defect 1 — wrong-constant substitution ─────────────────────────────────────

// THE BUG: the drafter reused ONE handle for every slot in an asset list, and blind
// global substitution rendered every asset as the same constant ("TD chequing $18,900,
// TFSA $18,900, RRSP $18,900" against true values $18,900 / $61,300 / $184,000). Each
// occurrence must be verified against the figure's own source row: a slot whose clause
// matches a DIFFERENT figure's row is flagged, never filled with the wrong constant.
func TestResolveFigureHandles_WrongConstantReuseFlagged(t *testing.T) {
	handled := []handledFig{
		{Handle: "Zephyr", Value: "$18,900", Context: "Danielle Tremblay - TD chequing/savings: $18,900", Source: "disclosure"},
		{Handle: "Quasar", Value: "$61,300", Context: "TFSA: $61,300 (her half of the joint TFSA moved by Marc)", Source: "disclosure"},
		{Handle: "Nimbus", Value: "$184,000", Context: "RRSP: $184,000", Source: "disclosure"},
	}
	in := "The disclosure identifies TD chequing and savings of Zephyr, a TFSA of Zephyr, and an RRSP of Zephyr."
	out := resolveFigureHandles(in, handled)
	if n := strings.Count(out, "$18,900"); n != 1 {
		t.Errorf("the reused constant appears %d times, want 1 (only its own slot):\n%s", n, out)
	}
	if !strings.Contains(out, figGapPrefix) {
		t.Errorf("mis-slotted handle occurrences were not flagged:\n%s", out)
	}
	if strings.Contains(out, "TFSA of $18,900") || strings.Contains(out, "RRSP of $18,900") {
		t.Errorf("a wrong constant filled a different figure's slot:\n%s", out)
	}
}

// Distinct handles used in their OWN slots must all substitute — verification must not
// suppress correct usage.
func TestResolveFigureHandles_CorrectSlotsSubstitute(t *testing.T) {
	handled := []handledFig{
		{Handle: "Zephyr", Value: "$18,900", Context: "Danielle Tremblay - TD chequing/savings: $18,900", Source: "disclosure"},
		{Handle: "Quasar", Value: "$61,300", Context: "TFSA: $61,300 (her half of the joint TFSA moved by Marc)", Source: "disclosure"},
		{Handle: "Nimbus", Value: "$184,000", Context: "RRSP: $184,000", Source: "disclosure"},
	}
	in := "TD chequing and savings of Zephyr, a TFSA of Quasar, and an RRSP of Nimbus."
	out := resolveFigureHandles(in, handled)
	for _, want := range []string{"$18,900", "$61,300", "$184,000"} {
		if !strings.Contains(out, want) {
			t.Errorf("correctly-slotted figure %q was not substituted:\n%s", want, out)
		}
	}
	if strings.Contains(out, figGapPrefix) {
		t.Errorf("correct usage was wrongly flagged:\n%s", out)
	}
}

// The single-use wrong-slot case (the "$1,640,000 inheritance" / "date of marriage as
// July 2026" corruption): a handle for the HOUSE value dropped into the INHERITANCE
// sentence must be flagged when the inheritance figure is on the section's list.
func TestResolveFigureHandles_CrossFigureMisuseFlagged(t *testing.T) {
	handled := []handledFig{
		{Handle: "Zephyr", Value: "$1,640,000", Context: "Appraised 12 June 2026 at $1,640,000 (Meridian Appraisals)", Source: "disclosure"},
		{Handle: "Quasar", Value: "$150,000", Context: "Marc received a $150,000 inheritance from his mother in March 2019", Source: "disclosure"},
	}
	out := resolveFigureHandles("Marc claims that the entire Zephyr inheritance received from his mother should be excluded.", handled)
	if strings.Contains(out, "$1,640,000") {
		t.Errorf("the house value shipped in the inheritance slot:\n%s", out)
	}
	if !strings.Contains(out, figGapPrefix) {
		t.Errorf("the mis-slotted figure was not flagged:\n%s", out)
	}
}

// A stray {{FIG:…}} with no grounded match must render as a flagged gap, never vanish
// silently and never pull in an unrelated figure.
func TestUnmatchedPlaceholderFlaggedNotDropped(t *testing.T) {
	figs := []SpecificHit{{Text: "Excess profits allocated to Oceanic Fund I LP\t$7,800,000", Source: "x.xlsx"}}
	out := resolveFigurePlaceholders("a support payment of {{FIG: Marc proposed child support payment}} monthly", figs)
	if !strings.Contains(out, figGapPrefix+"Marc proposed child support payment]") {
		t.Errorf("unmatched placeholder not flagged: %q", out)
	}
	if strings.Contains(out, "$7,800,000") {
		t.Errorf("an unrelated figure was guessed into the slot: %q", out)
	}
}

// ─── Defect 2 — coined placeholder names left literally in prose ────────────────

// THE BUG: told to "write the NAME of the matching figure", a weak drafter coined
// descriptive names ("Marc's 2025 Income", "**Marc's Proposed Three-Year Average
// Income**") instead of pool codenames; nothing substituted or scrubbed them, so the
// literal tokens shipped where numbers belonged. They must surface as flagged gaps.
func TestFlagUnfilledFigureNames(t *testing.T) {
	in := "Harwood relies on **Marc's Proposed Three-Year Average Income** and Danielle's 2025 Income, " +
		"while Sam's Per-Session Psychological Cost remains unallocated and Marc questions the Pension Commuted-Value Estimate."
	out := flagUnfilledFigureNames(in)
	for _, want := range []string{
		figGapPrefix + "Marc's Proposed Three-Year Average Income]",
		figGapPrefix + "Danielle's 2025 Income]",
		figGapPrefix + "Sam's Per-Session Psychological Cost]",
		figGapPrefix + "Pension Commuted-Value Estimate]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("coined placeholder not flagged: want %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "**Marc's") {
		t.Errorf("bolded placeholder survived unflagged:\n%s", out)
	}
}

func TestFlagUnfilledFigureNames_LeavesLegitimateProse(t *testing.T) {
	in := strings.Join([]string{
		"The Child Support Guidelines and the Family Law Act govern the analysis as of the Valuation Date.",
		"Danielle seeks occupancy until June 2029, when Sam finishes elementary school.",
		"**Key figures:**",
		"- Appraised value of the home: $1,640,000 (disclosure)",
	}, "\n")
	if out := flagUnfilledFigureNames(in); out != in {
		t.Errorf("legitimate prose was rewritten:\nin:  %s\nout: %s", in, out)
	}
	// Idempotent: an already-flagged gap is never double-wrapped.
	flagged := "The letter relies on " + figGapPrefix + "Marc's Proposed Three-Year Average Income]."
	if out := flagUnfilledFigureNames(flagged); strings.Count(out, figGapPrefix) != 1 {
		t.Errorf("flag pass is not idempotent: %q", out)
	}
}

// ─── Defect 3a — mojibake section titles ────────────────────────────────────────

// THE BUG: titleCase sliced the first BYTE of a token, splitting the two-byte "É" of
// "élodie" into invalid UTF-8 ("## Marc Preference ��lodie Alternating"). Labels must
// be valid UTF-8 with accents preserved, and surface casing (TFSA) kept.
func TestClusterLabels_RuneSafeAndAccentPreserving(t *testing.T) {
	if got := titleCase("élodie"); got != "Élodie" {
		t.Errorf("titleCase(élodie) = %q, want Élodie", got)
	}
	if got := titleCase("tfsa"); got != "Tfsa" {
		t.Errorf("titleCase(tfsa) = %q", got)
	}
	clusters := [][]Finding{
		{{Content: "Élodie prefers alternating weeks with Marc; Élodie schedule flexibility near school"},
			{Content: "Élodie alternating weeks preference stated to both parents"}},
		{{Content: "TFSA transfer of $40,000 by Marc before separation from the joint TFSA"},
			{Content: "the joint TFSA transfer requires tracing and accounting"}},
	}
	labels := labelClusters(clusters)
	for _, l := range labels {
		if !utf8.ValidString(l) || strings.ContainsRune(l, utf8.RuneError) {
			t.Errorf("label is not valid UTF-8: %q", l)
		}
	}
	if !strings.Contains(labels[0], "Élodie") {
		t.Errorf("accented name mangled in label: %q", labels[0])
	}
	if !strings.Contains(labels[1], "TFSA") {
		t.Errorf("acronym surface form lost in label: %q", labels[1])
	}
}

// Function words that survive TF-IDF must not become heading words ("… Harwood Does").
func TestClusterLabels_NoFunctionWords(t *testing.T) {
	clusters := [][]Finding{
		{{Content: "Harwood letter disclosure does does does not provide a worksheet"},
			{Content: "Harwood disclosure letter does does not explain the calculation"}},
		{{Content: "pension valuation actuarial estimate commuted value"}},
	}
	labels := labelClusters(clusters)
	if strings.Contains(strings.ToLower(labels[0]), "does") {
		t.Errorf("function word in heading: %q", labels[0])
	}
}

// ─── Defect 3b — Key-figures labels: clause-derived, figure-free, rune-safe ─────

// THE BUG: figureLabel took a raw 64-BYTE window of the row minus the value, shipping
// garbled mid-clause concatenations that still carried OTHER figures
// ("TD chequing/savings: - TFSA: $61,300 (her: $18,900"). The label must be the clause
// captioning the figure, with no residual figures, cut rune-safely.
func TestFigureLabel_CleanClause(t *testing.T) {
	row := "Danielle Tremblay - TD chequing/savings: $18,900 - TFSA: $61,300 (her half of the joint TFSA)"
	label := figureLabel(row, "$18,900")
	if label != "TD chequing/savings" {
		t.Errorf("figureLabel = %q, want the captioning clause \"TD chequing/savings\"", label)
	}
	if strings.Contains(label, "$61,300") || strings.Contains(label, "$18,900") {
		t.Errorf("label carries a figure: %q", label)
	}
	// The year-range hop: a figure whose immediate clause is figure-only backs up to the
	// captioning prose.
	l2 := figureLabel("Excess profits to Oceanic Fund I LP (2021-2023) $7,800,000", "$7,800,000")
	if !strings.Contains(l2, "Oceanic Fund") {
		t.Errorf("caption clause not recovered past the year range: %q", l2)
	}
}

func TestFigureLabel_RuneSafeTruncation(t *testing.T) {
	row := strings.Repeat("é", 100) + " $220"
	label := figureLabel(row, "$220")
	if !utf8.ValidString(label) || strings.ContainsRune(label, utf8.RuneError) {
		t.Errorf("label truncation split a multibyte rune: %q", label)
	}
	long := "Élodie " + strings.Repeat("wordy caption text ", 8) + "$220"
	l := figureLabel(long, "$220")
	if !utf8.ValidString(l) {
		t.Errorf("invalid UTF-8 label: %q", l)
	}
	if utf8.RuneCountInString(l) > 64 {
		t.Errorf("label over the rune cap: %d runes", utf8.RuneCountInString(l))
	}
}

// End-to-end through attachKeyFigures: the appended bullet reads "clause: figure (src)".
func TestAttachKeyFigures_CleanLabels(t *testing.T) {
	hits := []SpecificHit{
		{Text: "Danielle Tremblay - TFSA: $61,300 (her half of the joint TFSA)", Source: "disclosure"},
	}
	out := attachKeyFigures("Prose without the figure.", hits)
	if !strings.Contains(out, "- TFSA: $61,300 (disclosure)") {
		t.Errorf("Key-figures bullet not clause-labelled:\n%s", out)
	}
}
