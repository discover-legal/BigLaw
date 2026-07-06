// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import (
	"strings"
	"testing"
)

// ─── Corruption regression: handles adjacent to $/digits/punctuation ───────────

// THE BUG: ReplaceAllString treats "$7" in a replacement value as a capture-group
// reference (group 7 = nonexistent = empty), so "$7,800,000" substituted as
// ",800,000" and the punct-tidy welded it into "approximately,800,000". The
// substitution must be literal — values are data, never templates.
func TestResolveFigureHandles_CurrencyNotEaten(t *testing.T) {
	handled := []handledFig{
		{Handle: "Zephyr", Value: "$7,800,000"},
		{Handle: "Quasar", Value: "$1,234.56"},
		{Handle: "Nimbus", Value: "81.6%"},
	}
	out := resolveFigureHandles("The scheme generated approximately Zephyr in excess profits.", handled)
	if !strings.Contains(out, "approximately $7,800,000 in excess profits") {
		t.Errorf("currency value corrupted by substitution: %q", out)
	}
	if strings.Contains(out, ",800,000") && !strings.Contains(out, "$7,800,000") {
		t.Errorf("the $7 was eaten (capture-group expansion): %q", out)
	}
	// Adjacent punctuation and digits around the handle.
	for in, want := range map[string]string{
		"a fee (Quasar).":              "($1,234.56).",
		"rates of Nimbus, and rising.": "81.6%, and rising.",
		"total:Zephyr.":                "total:$7,800,000.",
		"NIMBUS was the rate.":         "81.6% was the rate.", // case-insensitive
	} {
		if out := resolveFigureHandles(in, handled); !strings.Contains(out, want) {
			t.Errorf("resolveFigureHandles(%q) = %q, want it to contain %q", in, out, want)
		}
	}
	// A handle inside a longer word must NOT substitute (word boundary).
	if out := resolveFigureHandles("the Zephyrus wind", handled); !strings.Contains(out, "Zephyrus") {
		t.Errorf("substitution ignored the word boundary: %q", out)
	}
}

// Paragraph breaks must survive the substitution tidy — \s{2,} used to collapse "\n\n"
// into a single space, flattening the section into a wall of text.
func TestResolveFigureHandles_KeepsParagraphBreaks(t *testing.T) {
	in := "First paragraph about Zephyr.\n\nSecond paragraph."
	out := resolveFigureHandles(in, []handledFig{{Handle: "Zephyr", Value: "$7,800,000"}})
	if !strings.Contains(out, "\n\n") {
		t.Errorf("paragraph break flattened: %q", out)
	}
}

// ─── Citation-handle carriage: Section 9.1 / Item 6 / Rule 204A-1 ──────────────

func TestSalientCite(t *testing.T) {
	cases := map[string]string{
		"employees must pre-clear under Section 9.1 of the Manual": "Section 9.1",
		"Item 6 of WCA's Brochure addresses fees":                  "Item 6",
		"adopted pursuant to Rule 204A-1 under the Advisers Act":   "Rule 204A-1",
		"in violation of Rule 206(4)-7 thereunder":                 "Rule 206(4)-7",
		"no citation here at all":                                  "",
	}
	for in, want := range cases {
		if got := salientCite(in); got != want {
			t.Errorf("salientCite(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssignHandles_CarriesCitationsIntact(t *testing.T) {
	hits := []SpecificHit{
		{Text: "Section 9.1 of the Compliance Manual, titled Personal Trading, required pre-clearance", Source: "manual.docx"},
		{Text: "excess profits of $7,800,000 to Oceanic", Source: "referral.docx"},
	}
	handled := assignHandles(hits)
	var cite string
	for _, h := range handled {
		if h.Value == "Section 9.1" {
			cite = h.Handle
		}
	}
	if cite == "" {
		t.Fatalf("citation identifier got no handle: %+v", handled)
	}
	out := resolveFigureHandles("Pre-clearance was required by "+cite+" of the Manual.", handled)
	if !strings.Contains(out, "Section 9.1") {
		t.Errorf("citation not carried verbatim into prose: %q", out)
	}
	if strings.Contains(out, "Section 9 ") {
		t.Errorf("citation paraphrased/shortened: %q", out)
	}
}

// ─── Process-language leaks: structurally impossible to emit ────────────────────

func TestNoProcessLeaks_FallbackPath(t *testing.T) {
	// A findings pool carrying each documented leak string as its conclusion; the
	// drafter returns nothing, so the fallback path renders the section. None of the
	// tells may appear — the verbatim evidence surfaces instead.
	findings := []Finding{
		{ID: "1", Content: "Evidence on point for this matter; see the quoted source.",
			Evidence: "The Division alleges excess profits of $7,800,000 to Oceanic Fund I LP", Source: "referral.docx", Grounded: true},
		{ID: "2", Content: "These must be extracted from the full referral notice and exhibits",
			Evidence: "Bellini received undisclosed compensation of $438,000 from Ostrowski", Source: "referral.docx", Grounded: true},
		{ID: "3", Content: "The extent of coverage should be verified to determine if detail gap exists",
			Evidence: "Whitmore instructed Delgado to delete the trade allocation spreadsheets", Source: "email.docx", Grounded: true},
	}
	w := New(nil, &scriptProv{emptyDraft: true, stitchTxt: ""}, "m", Options{MaxFindingsPerSec: 3})
	out, err := w.Write("Summarize the matter", "roundtable", findings)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{
		"Evidence on point for this matter",
		"see the quoted source",
		"must be extracted from the full",
		"detail gap",
		"should be verified to determine",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("process language leaked into the deliverable: %q\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "$7,800,000") || !strings.Contains(out, "$438,000") {
		t.Errorf("substantive evidence was dropped along with the process language:\n%s", out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("never-empty guarantee broken")
	}
}

func TestStripProcessSentences_KeepsSubstance(t *testing.T) {
	in := "The $822,000 differential suggests additional alleged violations. These must be extracted from the full referral notice and exhibits."
	out := stripProcessSentences(in)
	if strings.Contains(out, "must be extracted") {
		t.Errorf("process sentence survived: %q", out)
	}
	if !strings.Contains(out, "$822,000 differential") {
		t.Errorf("substantive sentence lost: %q", out)
	}
}

// ─── Truncation, orphans, duplicate headings, ledger runs ───────────────────────

func TestPolishSection_TrimsTruncatedTail(t *testing.T) {
	in := "The allegation is supported by the record. Requires detailed comparison of the Form ADV against actual practices documented in trading logs and"
	out := polishSection("Form ADV Disclosures", in)
	if strings.HasSuffix(strings.TrimSpace(out), "and") {
		t.Errorf("truncated final sentence shipped: %q", out)
	}
	if !strings.Contains(out, "supported by the record.") {
		t.Errorf("complete sentence was lost: %q", out)
	}
	// A tail with NO complete sentence is dropped entirely.
	if got := polishSection("X", "documented in trading logs and"); strings.TrimSpace(got) != "" {
		t.Errorf("pure fragment survived: %q", got)
	}
}

func TestPolishSection_DropsOrphansAndDuplicateTitle(t *testing.T) {
	in := strings.Join([]string{
		"Failure to Maintain Required Books and Records", // duplicate of the heading
		"Chief Compliance Officer",                       // orphan fragment
		"Code of Ethics",                                 // orphan fragment
		"The Division alleges that WCA failed to maintain required books and records under Rule 204-2.",
	}, "\n")
	out := polishSection("Failure to Maintain Required Books and Records", in)
	if strings.Contains(out, "Chief Compliance Officer\n") || strings.TrimSpace(out) == "Chief Compliance Officer" {
		t.Errorf("orphan fragment survived:\n%s", out)
	}
	if strings.HasPrefix(out, "Failure to Maintain") {
		t.Errorf("duplicate heading line survived:\n%s", out)
	}
	if !strings.Contains(out, "Rule 204-2.") {
		t.Errorf("substantive sentence lost:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "Code of Ethics" {
			t.Errorf("orphan 'Code of Ethics' line survived:\n%s", out)
		}
	}
}

func TestPolishSection_LedgerRunBecomesTable(t *testing.T) {
	in := strings.Join([]string{
		"The bank records show a pattern of monthly distributions.",
		"Owner Distribution - K.Ostrowski - May 2022 $15,000.00",
		"Owner Distribution - K.Ostrowski - June 2022 $15,000.00",
		"Owner Distribution - K.Ostrowski - July 2022 $15,000.00",
		"Owner Distribution - K.Ostrowski - Aug 2022 $15,000.00",
	}, "\n")
	out := polishSection("Directed Brokerage", in)
	if !strings.Contains(out, "| Entry | Amount |") {
		t.Errorf("ledger run not rendered as a table:\n%s", out)
	}
	if !strings.Contains(out, "| $15,000.00 |") {
		t.Errorf("ledger amounts lost:\n%s", out)
	}
	if !strings.Contains(out, "monthly distributions.") {
		t.Errorf("narrative sentence lost:\n%s", out)
	}
}

// ─── Duplicate-block suppression across the document ───────────────────────────

func TestDedupeDocBlocks(t *testing.T) {
	para := "The total alleged ill-gotten gains across all violation categories are $8,622,000, comprising cherry-picking profits, undisclosed compensation, and excess commissions attributable to the scheme."
	doc := strings.Join([]string{
		"## Cherry-Picking",
		para,
		"**Key figures:**\n- Total alleged ill-gotten gains / client harm: $8,622,000 (referral.docx)",
		"## Obstruction",
		para, // duplicated wholesale
		"**Key figures:**\n- Total alleged ill-gotten gains / client harm: $8,622,000 (referral.docx)",
	}, "\n\n")
	out := dedupeDocBlocks(doc)
	if n := strings.Count(out, "$8,622,000, comprising"); n != 1 {
		t.Errorf("duplicated paragraph appears %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "- Total alleged ill-gotten gains"); n != 1 {
		t.Errorf("duplicated Key-figures bullet appears %d times, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "## Cherry-Picking") || !strings.Contains(out, "## Obstruction") {
		t.Errorf("headings must survive dedupe:\n%s", out)
	}
}

// ─── Roster enforcement: one exposure entry per respondent, gap notes for misses ─

func TestEnforceRoster_GapNoteForMissingRespondent(t *testing.T) {
	w := New(nil, &scriptProv{draftText: "prose"}, "m", Options{
		Respondents: []string{"Gerald R. Whitmore", "Diana L. Chao", "Marcus T. Bellini"},
		Facts: []Fact{
			{Line: "- Whitmore holds 62% membership interest in WCA ($22.2 million LP stake)", Key: "whitmore membership", Entity: "Gerald R. Whitmore"},
			{Line: "- Chao personal account received 81.6% profitable allocations", Key: "chao allocations", Entity: "Diana L. Chao"},
			// No facts for Bellini — his exposure paragraph was an extraction miss.
		},
	})
	board := newPagedBoard()
	secs := []section{
		{Title: "Cherry-Picking Trade Allocations"},
		{Title: "Individuals at Risk and Personal Exposure"},
	}
	board.put("Cherry-Picking Trade Allocations", "The scheme favored Oceanic Fund.", "")
	// The exposure section only mentions the FIRM (Whitmore Capital Advisors), not the
	// person — the firm mention must not count as covering Gerald Whitmore.
	board.put("Individuals at Risk and Personal Exposure", "Whitmore Capital Advisors LLC faces entity-level exposure.", "")

	secs = w.enforceRoster(secs, board)
	exp := board.get("Individuals at Risk and Personal Exposure")
	if !strings.Contains(exp, rosterHeader) {
		t.Fatalf("roster block missing:\n%s", exp)
	}
	if !strings.Contains(exp, "62%") || !strings.Contains(exp, "$22.2 million") {
		t.Errorf("Whitmore's consolidated record missing:\n%s", exp)
	}
	if !strings.Contains(exp, "81.6%") {
		t.Errorf("Chao's consolidated record missing:\n%s", exp)
	}
	if !strings.Contains(exp, "No individual-exposure findings were extracted for Marcus T. Bellini — review the source directly") {
		t.Errorf("explicit gap note for the missing respondent absent:\n%s", exp)
	}
}

func TestEnforceRoster_CreatesExposureSectionWhenAbsent(t *testing.T) {
	w := New(nil, &scriptProv{draftText: "prose"}, "m", Options{
		Respondents: []string{"Diana L. Chao"},
	})
	board := newPagedBoard()
	secs := []section{{Title: "Cherry-Picking"}}
	board.put("Cherry-Picking", "Prose.", "")
	secs = w.enforceRoster(secs, board)
	if len(secs) != 2 || secs[1].Title != "Individual Exposure" {
		t.Fatalf("exposure section not created: %+v", secs)
	}
	if !strings.Contains(board.get("Individual Exposure"), "No individual-exposure findings were extracted for Diana L. Chao") {
		t.Errorf("gap note missing:\n%s", board.get("Individual Exposure"))
	}
}

func TestNameCovered_FirmMentionDoesNotCoverPerson(t *testing.T) {
	if nameCovered("Whitmore Capital Advisors LLC faces exposure.", "Gerald R. Whitmore") {
		t.Error("the firm name wrongly counted as covering the person")
	}
	if !nameCovered("Whitmore holds a 62% membership interest.", "Gerald R. Whitmore") {
		t.Error("a genuine personal mention was not recognized")
	}
	if nameCovered("The Whitmoreville branch reported.", "Gerald R. Whitmore") {
		t.Error("substring inside a longer word wrongly matched")
	}
}

// ─── Roll-up arithmetic: components sum to a grounded aggregate, computed in Go ──

func TestComputeRollups_ComponentsSumToAggregate(t *testing.T) {
	// The aggregate's OWN fact (its Key embeds the source quote) states the decomposition —
	// only then is the sum asserted. Components scattered across other facts don't count.
	facts := []Fact{
		{Line: "- excess profits to Oceanic Fund I LP $7,800,000", Entity: "Oceanic Fund I LP"},
		{Line: "- excess profits in Chao's personal account $438,000", Entity: "Diana L. Chao"},
		{Line: "- total excess profits from cherry-picking $8,238,000", Entity: "WCA",
			Key: "wca totals excess profits $8,238,000 the total excess profits are estimated at $8,238,000 ($7,800,000 allocated to oceanic fund i lp + $438,000 in chao's personal account)"},
		{Line: "- unrelated management fee 1.50% per annum", Entity: "WCA"},
	}
	rollups := computeRollups(facts)
	if len(rollups) == 0 {
		t.Fatal("no roll-up found for a source-stated decomposition")
	}
	joined := strings.Join(rollups, "\n")
	for _, must := range []string{"$7,800,000", "$438,000", "$8,238,000", "="} {
		if !strings.Contains(joined, must) {
			t.Errorf("roll-up missing %q:\n%s", must, joined)
		}
	}
}

func TestComputeRollups_NoFalseAggregates(t *testing.T) {
	// Amounts that do NOT sum to any grounded aggregate must produce no roll-up —
	// the writer never asserts arithmetic the record doesn't support.
	facts := []Fact{
		{Line: "- amount one $7,800,000"},
		{Line: "- amount two $438,000"},
		{Line: "- unrelated total $9,999,999"},
	}
	if rollups := computeRollups(facts); len(rollups) != 0 {
		t.Errorf("asserted an aggregate the record does not support: %v", rollups)
	}
}

func TestParseMoneyCents(t *testing.T) {
	cases := map[string]int64{
		"$7,800,000":    780_000_000,
		"$438,000.00":   43_800_000,
		"$22.2 million": 2_220_000_000,
		"$1,234.56":     123_456,
	}
	for in, want := range cases {
		got, ok := parseMoneyCents(in)
		if !ok || got != want {
			t.Errorf("parseMoneyCents(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
}

// ─── Memo frame: paged path emits exec summary + conclusion around the body ─────

func TestFinalizePaged_MemoFrame(t *testing.T) {
	w := New(nil, &scriptProv{stitchTxt: "This matter concerns alleged cherry-picking with quantified harm."}, "m", Options{
		Facts: []Fact{
			{Line: "- excess profits to Oceanic Fund I LP $7,800,000"},
			{Line: "- excess profits to Chao $438,000"},
			{Line: "- total cherry-picking excess profits $8,238,000 ($7,800,000 to Oceanic Fund I LP + $438,000 to Chao)"},
		},
	})
	board := newPagedBoard()
	secs := []section{{Title: "Cherry-Picking"}, {Title: "Obstruction"}}
	board.put("Cherry-Picking", "The scheme allocated $7,800,000 to Oceanic Fund I LP.", "- $7.8M to Oceanic")
	board.put("Obstruction", "Whitmore instructed deletion of the spreadsheets.", "- deletion instruction")
	out := w.finalizePaged("Extract key allegations", secs, board)

	if !strings.HasPrefix(out, "## Executive Summary") {
		t.Errorf("executive summary not at the top:\n%s", out)
	}
	if !strings.Contains(out, "## Conclusion and Posture") {
		t.Errorf("closing posture section missing:\n%s", out)
	}
	if !strings.Contains(out, "$7,800,000 + $438,000 = $8,238,000") &&
		!strings.Contains(out, "$438,000 + $7,800,000 = $8,238,000") {
		t.Errorf("mechanical figure roll-up missing from the frame:\n%s", out)
	}
	// Body order preserved, both sections present.
	if !strings.Contains(out, "## Cherry-Picking") || !strings.Contains(out, "## Obstruction") {
		t.Errorf("section bodies lost:\n%s", out)
	}
}
