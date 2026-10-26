// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r9 verifier repros (critique/biglaw-lab-r4-r9-verifier.md H1, H2, M1), kept as regression
// tests: one wrong reader label, or a memo period with right labels, never traces a wrong figure
// unseen.

// H1: "Section 30" read as a 30-day duration with no words beside it that state a unit is no period:
// the label is unclear (ReadLabels), and a reading with no cited unit words feeds no worksheet.
func TestMislabelledSectionIsNoPeriod(t *testing.T) {
	q := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days."
	days := "days"
	l := lab("q1.s1", analysis.KindDuration, "", "1", "", "", "")
	l.Unit, l.UnitFrom = &days, &days
	rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", q)}, []analysis.Label{l})
	if r := rs["q1.s1"]; !r.Unclear {
		t.Errorf("\"Section 30\" read as 30 days citing \"days\": %+v", r)
	}
	known := []analysis.Reading{
		{Kind: analysis.KindDuration, Unit: "days", Amount: "30", Text: "30"}, // a duration no words state
		{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"},
	}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"30", "days"})
	if got := tracedSheets([]analysis.Worksheet{w}, q+"\n", map[string]bool{"F1": true}, known); len(got) == 1 {
		t.Errorf("date_add(2024-03-01, 30 days) = %s traced from \"Section 30\"", w.Result)
	}
}

// H1: "Section 12" read as a bare number is no multiplier: a number needs the sign beside it.
func TestMislabelledSectionIsNoMultiplier(t *testing.T) {
	quote := "Under Section 12 of the Agreement, the Buyer shall pay $1,000,000 at Closing."
	sec := "Section"
	l := lab("q1.s1", analysis.KindNumber, "", "1", "", "", "")
	l.UnitFrom = &sec
	if rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", quote)}, []analysis.Label{l}); !rs["q1.s1"].Unclear {
		t.Errorf("\"Section 12\" read as a multiple of \"Section\": %+v", rs["q1.s1"])
	}
	known := []analysis.Reading{{Kind: analysis.KindNumber, Amount: "12", Text: "12"}, {Kind: analysis.KindMoney, Currency: "USD", Amount: "1000000", Text: "1,000,000"}}
	cited := map[string]bool{"F1": true}
	w := sheet(t, "product", [2]string{"12", ""}, [2]string{"1,000,000", "USD"})
	memo := "The Buyer pays $12,000,000 at Closing."
	sheets := tracedSheets([]analysis.Worksheet{w}, quote+"\n", cited, known)
	if got := g8(memo, quote+"\n", 0, nil, memoRead(memo, lab("m.s1", analysis.KindMoney, "USD", "1", "", "", "")), known, nil, sheets, cited, nil); len(got) == 0 {
		t.Errorf("%q traced by %s = %s %s", memo, w.WID, w.Result, w.Unit)
	}
}

// H1 (r9, r10): "21 business days" read in calendar days (its words "days" are beside it) still
// traces a calendar deadline, so every figure of a section that traces only through a worksheet over
// typed inputs is in the Computed figures block, whatever form the memo writes it in (G8 itself
// decides: lintLimited traces the section again without those worksheets), with the words its inputs
// rest on, the unit whose words are not its name marked.
func TestComputedFigureDisclosedWithItsWords(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 21 business days."
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}},
		Figures: []analysis.Reading{{Span: "q1.s3", Text: "21", Start: 60, End: 62, Kind: analysis.KindDuration, Unit: "days", UnitFrom: "business days", Amount: "21"}}} // the wrong unit
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	use := map[string]Finding{"F1": f1}
	for _, memo := range []string{"The answer is due March 22, 2024 [F1].", "The answer is due March 22 [F1].", "The answer is due March 22nd, 2024 [F1].", "The answer is due 22 March 2024 [F1].",
		"The answer is due {" + w.WID + "} [F1]."} {
		sec := readSection(Section{Markdown: memo, Handles: []Fig{{"{" + w.WID + "}", "", sheetValue(w.Result, w.Unit)}}})
		lines, _, _, computed := lintLimited("I1", sec, use, []analysis.Worksheet{w})
		if len(lines) == 0 && (len(computed) != 1 || !computed[0].Mismatch || !strings.HasPrefix(computed[0].Text, "⚠ March 22, 2024 = March 1, 2024 [F1] “") || !strings.Contains(computed[0].Text, "” + 21 days [F1] “…2024. An answer is due within 21 business days.”") ||
			!strings.HasSuffix(computed[0].Text, ", read in days from “business days”: the cited words “business days” are not its unit (days); the findings are gated") || computed[0].Gate["F1"] != limitedUnit) {
			t.Errorf("%q: traced, but not disclosed with its words: %+v", memo, computed)
		}
	}
	if _, _, _, computed := lintLimited("I1", readSection(Section{Markdown: "The complaint was served on March 1, 2024 [F1]."}), use, []analysis.Worksheet{w}); len(computed) != 0 {
		t.Errorf("a section that states no computed figure lists one: %+v", computed)
	}
}

// H1 (unchanged): a period in the wrong unit is never an operand: 21 days feeds no 21 months.
func TestWrongUnitOperandRefused(t *testing.T) {
	ground := "Served on March 1, 2024. An answer is due within 21 days.\n"
	known := []analysis.Reading{{Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"}}
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "months"})
	if got := tracedSheets([]analysis.Worksheet{w}, ground, map[string]bool{"F1": true}, known); len(got) != 0 {
		t.Errorf("date_add 21 months traced from a 21-day reading: %s", w.Result)
	}
}

// M1: a month on from January 31, 2024 is February 29, 2024, and a year on from February 29, 2024 is
// February 28, 2025 (the last day of the shorter month), traced from the cited "one (1) month".
func TestMonthEndClampTraces(t *testing.T) {
	ground := "Notice of default was given on January 31, 2024. The Borrower may cure within one (1) month after notice.\n"
	known := []analysis.Reading{{Kind: analysis.KindDuration, Unit: "months", UnitFrom: "month", Amount: "1", Text: "1"}}
	w := sheet(t, "date_add", [2]string{"2024-01-31", "date"}, [2]string{"1", "months"})
	if got := tracedSheets([]analysis.Worksheet{w}, ground, map[string]bool{"F1": true}, known); w.Result != "2024-02-29" || len(got) != 1 {
		t.Errorf("date_add(2024-01-31, 1 month) = %s (traced %v), want 2024-02-29", w.Result, len(got) == 1)
	}
	if y := sheet(t, "date_add", [2]string{"2024-02-29", "date"}, [2]string{"1", "years"}); y.Result != "2025-02-28" {
		t.Errorf("date_add(2024-02-29, 1 year) = %s, want 2025-02-28", y.Result)
	}
}

// H2: with every label right, a memo's "within 30 days" traces only to a cited period of 30 days (or
// the days between two cited dates), never to the "30" of "Section 30"; the record's 21 days traces.
func TestMemoPeriodNeedsACitedPeriod(t *testing.T) {
	quote := "Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days. The hearing is on March 31, 2024."
	known := []analysis.Reading{{Kind: analysis.KindOther, Amount: "30", Text: "30"}, {Kind: analysis.KindDuration, Unit: "days", UnitFrom: "days", Amount: "21", Text: "21"}}
	period := func(memo string) []untraced {
		days := "days"
		l := lab("m.s1", analysis.KindDuration, "", "1", "", "", "")
		l.Unit, l.UnitFrom = &days, &days
		return g8(memo, quote+"\n", 0, nil, memoRead(memo, l), known, nil, nil, map[string]bool{"F1": true}, nil)
	}
	if got := period("An answer is due within 31 days of service."); len(got) != 1 {
		t.Errorf("31 days (no cited period, nor two cited dates 31 days apart) traced: %+v", got)
	}
	if got := period("An answer is due within 21 days of service."); len(got) != 0 {
		t.Errorf("the cited 21 days flagged: %+v", got)
	}
	if got := period("The hearing is 30 days after service."); len(got) != 0 { // March 1 to March 31: two cited dates
		t.Errorf("the days between two cited dates flagged: %+v", got)
	}
	quote = strings.Replace(quote, " The hearing is on March 31, 2024.", "", 1)
	if got := period("An answer is due within 30 days of service."); len(got) != 1 || !strings.Contains(got[0].hint, "no period of 30 days") {
		t.Errorf("30 days traced to \"Section 30\": %+v", got)
	}
}

// H1 e2e: one wrong label ("Section 30" read as 30 days) delivers no traced, undisclosed deadline.
func TestMislabelledSectionDeadlineNotDelivered(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nUnder Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days.\n")
	days := "days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &days
	f.reads = map[string]analysis.Label{"quote:30": wrong}
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("30", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 days.", d1,
			"Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days"))},
		"draft_section": {section("The answer is due March 31, 2024 [F1].", "Due [F1].")},
	}), Config{})
	if out, fin := deref(v.Output), strings.Join(f.outputs(v.ID, "finish"), "\n"); strings.Contains(out, "March 31, 2024") && !strings.Contains(fin, "not traced") && !strings.Contains(out, "### Computed figures") {
		t.Errorf("\"March 31, 2024\" delivered traced and undisclosed; stop %s", stop(v))
	}
}

// H1 e2e: "21 business days" read in calendar days: the computed deadline the drafter writes out is
// delivered only with its computation and the words it rests on in Limitations.
func TestBusinessDaysReadAsDaysDisclosed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	days, from := "days", "business days"
	wrong := lab("", analysis.KindDuration, "", "1", "", "", "")
	wrong.Unit, wrong.UnitFrom = &days, &from
	f.reads = map[string]analysis.Label{"quote:21": wrong}
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("21", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days"))},
		"draft_section": {section("The answer is due March 22, 2024 [F1].", "Due [F1].")},
	}), Config{})
	if out := deref(v.Output); !strings.Contains(out, "March 22, 2024") || !strings.Contains(out, "⚠ March 22, 2024 = March 1, 2024 [F1]") || !strings.Contains(out, "the cited words “business days” are not its unit (days)") {
		t.Errorf("the calendar deadline from \"21 business days\" is delivered without its computation; stop %s\n%s", stop(v), out)
	}
}

// M1 e2e: a month on from January 31, 2024 is February 29; the memo's overflowed "March 2, 2024" is flagged.
func TestMonthEndDeadlineFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Default\nNotice of default was given on January 31, 2024. The Borrower may cure within one (1) month after notice.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When does the cure period end?", "Conduct"))), calls(extract(d1, "cure", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-01-31", "date"), op("1", "months")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("Notice of default was given on January 31, 2024; the cure period is one month.", d1,
			"Notice of default was given on January 31, 2024. The Borrower may cure within one (1) month after notice"))},
		"draft_section": {section("The cure period ends March 2, 2024 [F1].", "Cure [F1].")},
	}), Config{})
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 2024-03-02 is not traced") {
		t.Errorf("\"March 2, 2024\" (a month on from Jan 31 is Feb 29) not flagged; stop %s\n%s", stop(v), fin)
	}
}

// H2 e2e: with the stand-in's own labels, the memo's "within 30 days" (the record: 21 days) is flagged.
func TestMemoPeriodFromSectionNumberFlagged(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nUnder Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 days.", d1,
			"Under Section 30 of the Agreement, the complaint was served on March 1, 2024. An answer is due within 21 days"))},
		"draft_section": {section("The answer is due within 30 days of service [F1].", "Due [F1].")},
	}), Config{})
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 30 days is not traced") {
		t.Errorf("\"within 30 days\" (the record: 21 days) not flagged; stop %s\n%s", stop(v), fin)
	}
}
