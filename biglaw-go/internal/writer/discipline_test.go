// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Regression tests for the partner-committee work-product review (discipline v2):
// quote inviolability, meta-dialogue filtering, prose rosters, sibling-only roll-ups,
// the prose total audit, and the nested-frame guard.

package writer

import (
	"strings"
	"testing"
)

// ─── Defect 1 — handle machinery must never corrupt verbatim quotes ─────────────

// THE BUG: masking used strings.ReplaceAll with no word boundary and no quote
// protection, so a handle whose value was "1" turned the quoted instruction
// "remove the old allocation folders from Q1 2021 through Q1 2022" into
// "…from QCalliope 202Calliope through QCalliope 2022" in the drafter's prompt —
// which the drafter then copied verbatim into the deliverable.
func TestMaskValue_QuoteInviolable(t *testing.T) {
	quote := `instructs Delgado to "remove the old allocation folders from Q1 2021 through Q1 2022."`
	// (a) word boundary: a value that is a substring of a larger token never substitutes.
	if out := maskValue(quote, "1", "Calliope"); out != quote {
		t.Errorf("substring value corrupted the text:\n%q", out)
	}
	// (b) quoted spans are inviolable even for boundary-clean values.
	in := `the email says "an initiation fee of $45,000.00 was paid" on that date`
	if out := maskValue(in, "$45,000.00", "Zephyr"); out != in {
		t.Errorf("masking edited inside a quoted span:\n%q", out)
	}
	// (c) outside quotes, a boundary-clean value IS masked.
	out := maskValue("an initiation fee of $45,000.00 was paid", "$45,000.00", "Zephyr")
	if !strings.Contains(out, "Zephyr") || strings.Contains(out, "$45,000.00") {
		t.Errorf("boundary-clean value outside quotes not masked: %q", out)
	}
	// (d) an unclosed quote protects the tail (protective default).
	trunc := `references the "clean up of legacy files from Q1 2021`
	if out := maskValue(trunc, "Q1 2021", "Nimbus"); out != trunc {
		t.Errorf("unclosed quote was edited: %q", out)
	}
}

// Day-of-month fragments and bare short numbers must never become handle values (the
// mechanism behind "On 18, 2024" / "commenced on 11, 2024" — the drafter treated the
// handle as the whole date and the month vanished). Full dates ARE carried, whole.
func TestAssignHandles_NoFragmentValues(t *testing.T) {
	hits := []SpecificHit{
		{Text: "referral to the Division of Enforcement on October 18, 2024", Source: "referral.docx"},
		{Text: "the examination commenced on March 11, 2024", Source: "referral.docx"},
		{Text: "remove the old allocation folders from Q1 2021 through Q1 2022", Source: "email.docx"},
	}
	handled := assignHandles(hits)
	vals := map[string]bool{}
	for _, h := range handled {
		vals[h.Value] = true
		if isBareShortNumber(h.Value) {
			t.Errorf("bare fragment %q became a handle value", h.Value)
		}
	}
	if !vals["October 18, 2024"] {
		t.Errorf("full date not carried as the handle value: %+v", handled)
	}
	if vals["18"] || vals["11"] || vals["1"] {
		t.Errorf("day/quarter fragment leaked into handle values: %+v", handled)
	}
	// A date value must sit below the Key-figures guarantee floor: dates are carried in
	// prose but never appended to the mechanical figure tail.
	if figureSalience("October 18, 2024") >= salienceGuaranteeFloor {
		t.Error("date value would leak into the Key-figures guarantee tail")
	}
}

// Handle names can never appear in the output: mapped names are substituted (globally),
// and an unmapped pool name the drafter hallucinated is scrubbed outside quotes.
func TestNoHandleNamesInOutput(t *testing.T) {
	handled := []handledFig{{Handle: "Zephyr", Value: "$7,800,000"}}
	out := resolveFigureHandles("The scheme generated Zephyr in profits, per Peregrine analysis.", handled)
	out = scrubUnresolvedHandles(out, handled)
	for _, name := range figureHandles {
		if strings.Contains(strings.ToLower(out), strings.ToLower(name)) {
			t.Errorf("handle name %q shipped in output: %q", name, out)
		}
	}
	if !strings.Contains(out, "$7,800,000") {
		t.Errorf("mapped handle not substituted: %q", out)
	}
	// Inside a quote, scrub leaves text untouched (quotes are inviolable) — and a real
	// word containing a pool name is never damaged.
	in := `He wrote "the Peregrine file" in the margin; the peregrination continued.`
	if got := scrubUnresolvedHandles(in, nil); got != in {
		t.Errorf("scrub edited a quote or a containing word: %q", got)
	}
}

// ─── Defect 2 — agent process-chatter must never ship as section content ────────

func TestSanitizeDraft_ProcessChatter(t *testing.T) {
	in := strings.Join([]string{
		"Let me extract the specific figures and details from the source documents:",
		"Now I have the necessary information to write the section. Let me compose the Examination Conduct Violations section based on the findings and extracted specifics.",
		"---",
		"Section 209(e) of the Advisers Act establishes a clear statutory prohibition against obstructing the Commission.",
	}, "\n")
	out := sanitizeDraft(in)
	for _, tell := range []string{"Let me extract", "Now I have", "Let me compose", "---"} {
		if strings.Contains(out, tell) {
			t.Errorf("process chatter %q survived:\n%s", tell, out)
		}
	}
	if !strings.Contains(out, "Section 209(e)") {
		t.Errorf("substantive prose lost:\n%s", out)
	}
	// A body that is ONLY a trailing lead-in must polish to empty (→ fallback fires).
	if got := polishSection("X", "The following should be noted:"); strings.TrimSpace(got) != "" {
		t.Errorf("trailing lead-in with no content shipped: %q", got)
	}
}

func TestIsRefusalDraft(t *testing.T) {
	refusal := `I appreciate the detailed correction, but I need to clarify my role here. You've asked me to revise a section titled "Recordkeeping Violations", but the grounded additions indicate otherwise.
I can instead draft one of the following:
1. A revised section with an accurate title.
Please confirm which approach aligns with the actual findings, and I will draft accordingly.`
	if !isRefusalDraft(refusal) {
		t.Error("multi-paragraph refusal not detected")
	}
	prose := "The Division alleges that WCA failed to maintain required books and records under Rule 204-2. The firm's role in the scheme was central."
	if isRefusalDraft(prose) {
		t.Errorf("substantive prose misflagged as refusal: %q", prose)
	}
	// End-to-end: a drafter that refuses yields the grounded fallback, never the dialogue.
	w := New(nil, &scriptProv{draftText: refusal}, "m", Options{MaxFindingsPerSec: 2})
	out, err := w.Write("Summarize", "roundtable", sampleFindings(3))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "clarify my role") || strings.Contains(out, "Please confirm") {
		t.Errorf("refusal shipped as section content:\n%s", out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("never-empty guarantee broken")
	}
}

// ─── Defect 3 — roster renders prose, not graph predicates ──────────────────────

func TestRespondentEntry_RendersProse(t *testing.T) {
	w := New(nil, &scriptProv{}, "m", Options{
		Respondents: []string{"Marcus T. Bellini"},
		Facts: []Fact{
			{Line: "- Directed Brokerage to Lakeshore Trading committedBy Marcus T. Bellini", Entity: "Directed Brokerage to Lakeshore Trading"},
			{Line: "- Marcus T. Bellini Vice President and Portfolio Manager at WCA", Entity: "Marcus T. Bellini"},
			{Line: "- Marcus T. Bellini is Vice President and Portfolio Manager at WCA", Entity: "Marcus T. Bellini"}, // restatement — must collapse
			{Line: "- Kevin Ostrowski ownsStakeIn Lakeshore Trading LLC (40%)", Entity: "Kevin Ostrowski"},
			{Line: "- Marcus T. Bellini receivedFrom Kevin Ostrowski ($45,000.00)", Entity: "Marcus T. Bellini"},
			{Line: "- monthly dues paid for Bellini $2,800.00", Entity: "Kevin Ostrowski"},
		},
	})
	entry := w.respondentEntry("Marcus T. Bellini")
	for _, raw := range []string{"committedBy", "ownsStakeIn", "receivedFrom", "Grounded figures on record:"} {
		if strings.Contains(entry, raw) {
			t.Errorf("internal representation %q shipped in roster entry:\n%s", raw, entry)
		}
	}
	for _, want := range []string{"committed by", "received from", "$45,000.00", "$2,800.00"} {
		if !strings.Contains(entry, want) {
			t.Errorf("roster entry missing %q:\n%s", want, entry)
		}
	}
	if n := strings.Count(entry, "Vice President and Portfolio Manager"); n != 1 {
		t.Errorf("restated clause appears %d times, want 1:\n%s", n, entry)
	}
}

// ─── Defect 6 — sibling-only roll-ups and the prose total audit ─────────────────

func TestComputeRollups_SiblingOnly(t *testing.T) {
	// The partner-review false aggregate: 2021 firm AUM + one client's AUM + one fund's
	// NAV happen to sum to the current RAUM. Separate facts, separate metrics — the sum
	// must NOT be asserted, however exactly it adds up.
	facts := []Fact{
		{Line: "- WCA regulatory assets under management $2.3 billion", Entity: "WCA"},
		{Line: "- WCA assets under management (2021) $1.95 billion", Entity: "WCA"},
		{Line: "- largest client account assets $310 million", Entity: "Crescent Bay"},
		{Line: "- Oceanic Offshore Fund net asset value $40 million", Entity: "Oceanic Offshore Fund Ltd."},
	}
	if rollups := computeRollups(facts); len(rollups) != 0 {
		t.Errorf("cross-fact subset-sum coincidence asserted as an aggregate:\n%s", strings.Join(rollups, "\n"))
	}
	// With no grounded decomposition, the itemization presents figures NOT totaled.
	items := computeItemization(facts)
	if len(items) < 2 {
		t.Fatalf("expected an itemization of headline amounts, got %v", items)
	}
	for _, it := range items {
		if strings.Contains(it, "=") || strings.Contains(it, "+") {
			t.Errorf("itemization must not compute: %q", it)
		}
	}
}

func TestStripUngroundedTotals(t *testing.T) {
	grounded := map[int64]bool{}
	for _, m := range []string{"$45,000.00", "$2,800.00", "$92,600", "$8,238,000"} {
		c, ok := parseMoneyCents(m)
		if !ok {
			t.Fatalf("fixture money %q unparsable", m)
		}
		grounded[c] = true
	}
	doc := strings.Join([]string{
		"Ostrowski paid an initiation fee of $45,000.00 on behalf of Bellini. The Division's analysis indicates that the total undisclosed compensation received by Bellini through these club payments was approximately $47,800.00, comprising the initiation fee and documented monthly dues. The payments were not disclosed to WCA.",
		"",
		"The total excess profits attributable to the scheme are estimated at $8,238,000.",
	}, "\n")
	out := stripUngroundedTotals(doc, grounded)
	if strings.Contains(out, "$47,800") {
		t.Errorf("model-computed total shipped:\n%s", out)
	}
	if !strings.Contains(out, "$45,000.00") || !strings.Contains(out, "not disclosed to WCA") {
		t.Errorf("grounded sentences around the bad total were lost:\n%s", out)
	}
	if !strings.Contains(out, "$8,238,000") {
		t.Errorf("a GROUNDED total was wrongly stripped:\n%s", out)
	}
}

// ─── Nested-structure defect — frames never wrap a frame ────────────────────────

func TestFrameScaffoldingStripped(t *testing.T) {
	in := strings.Join([]string{
		"# ALLEGATION-EXTRACTION-SUMMARY.DOCX",
		"EXECUTIVE SUMMARY",
		"",
		"This matter concerns Whitmore Capital Advisors LLC and its principal officers.",
		"---",
		"Document prepared as client-ready work product. No extraneous facts introduced.",
	}, "\n")
	out := stripFrameScaffolding(sanitizeDraft(in))
	for _, bad := range []string{"#", "EXECUTIVE SUMMARY", "ALLEGATION-EXTRACTION", "---", "Document prepared as"} {
		if strings.Contains(out, bad) {
			t.Errorf("frame scaffolding %q survived:\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "This matter concerns Whitmore Capital Advisors") {
		t.Errorf("frame prose lost:\n%s", out)
	}
}

func TestFinalizePaged_NoNestedFrames(t *testing.T) {
	// The frame model returns a full document skeleton of its own; the assembled doc must
	// still carry exactly ONE executive summary and ONE conclusion heading.
	skeleton := "# ALLEGATION EXTRACTION SUMMARY\n## EXECUTIVE SUMMARY\nThis matter concerns alleged cherry-picking."
	w := New(nil, &scriptProv{stitchTxt: skeleton}, "m", Options{})
	board := newPagedBoard()
	secs := []section{{Title: "Cherry-Picking"}}
	board.put("Cherry-Picking", "The scheme allocated profits to Oceanic Fund I LP.", "- cherry-picking")
	out := w.finalizePaged("Extract key allegations", secs, board)

	if n := strings.Count(strings.ToLower(out), "executive summary"); n != 1 {
		t.Errorf("executive summary appears %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(strings.ToLower(out), "conclusion"); n > 1 {
		t.Errorf("conclusion appears %d times, want ≤1:\n%s", n, out)
	}
	if strings.Contains(out, "ALLEGATION EXTRACTION SUMMARY") {
		t.Errorf("model title block nested into the document:\n%s", out)
	}
	if !strings.Contains(out, "## Cherry-Picking") {
		t.Errorf("section body lost:\n%s", out)
	}
}
