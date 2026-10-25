// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r11 verifier repros (critique/biglaw-lab-r4-r11-verifier.md H, M, L1, L3, L4, L5), kept as
// regression tests.

const businessDaysQuote = "The complaint was served on March 1, 2024. An answer is due within 21 business days."

// businessDaysFinding is F1 over businessDaysQuote with its "21" read in days from "business days"
// (the one wrong unit).
func businessDaysFinding(t *testing.T) Finding {
	t.Helper()
	p := analysis.PassageOf("q1", businessDaysQuote)
	var ls []analysis.Label
	days, from := "days", "business days"
	for i, s := range p.Spans {
		l := lab(analysis.SpanID("q1", i), analysis.KindDate, "", "1", "", "", "")
		if s.Text == "21" {
			l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &from
		}
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	var figs []analysis.Reading
	for i := range p.Spans {
		figs = append(figs, rs[analysis.SpanID("q1", i)])
	}
	return Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: businessDaysQuote, Method: "exact"}}, Figures: figs}
}

// labelledSection is a section whose memo spans are all read with the labels label gives each.
func labelledSection(md string, label func(analysis.Span) analysis.Label) Section {
	sec := Section{Markdown: md}
	body, _ := memoBody(sec)
	p := analysis.PassageOf("m", body)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := label(s)
		l.Span = analysis.SpanID("m", i)
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	sec.Read = &SectionRead{Hash: textHash(body)}
	for _, r := range rs {
		sec.Read.Figs = append(sec.Read.Figs, r)
	}
	return sec
}

func asDate(analysis.Span) analysis.Label { return lab("", analysis.KindDate, "", "1", "", "", "") }

// r10 H1 held across memo forms: every form of the computed deadline March 22, 2024 is flagged or
// listed, never silent.
func TestComputedDateEveryForm(t *testing.T) {
	f1 := businessDaysFinding(t)
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	for _, memo := range []string{
		"The answer is due March 22 [F1].", "The answer is due March 22nd, 2024 [F1].", "The answer is due March 22nd [F1].",
		"The answer is due 22 March [F1].", "The answer is due 22 March 2024 [F1].", "The answer is due 22nd March 2024 [F1].",
		"The answer is due Mar. 22, 2024 [F1].", "The answer is due Mar 22 [F1].", "The answer is due 3/22/2024 [F1].",
		"The answer is due 3/22 [F1].", "The answer is due 2024-03-22 [F1].", "The answer is due on the 22nd [F1].",
		"The answer is due on the 22nd of March [F1].", "Served March 1, 2024, the answer is due on the 22nd [F1].",
		"The answer is due on March 22 (a Friday) [F1].", "The answer is due MARCH 22 [F1].", "The answer is due March 22–23 [F1].",
		"The answer is due by 22/3/2024 [F1].",
	} {
		lines, _, _, computed := lintLimited("I1", labelledSection(memo, asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
		if len(lines) == 0 && len(computed) == 0 {
			t.Errorf("%q: no lint line and no computed line", memo)
		}
	}
}

// A memo period traced through a typed worksheet's result, or the days between its dates, is listed.
func TestPeriodFromTypedSheetResultListed(t *testing.T) {
	f1 := businessDaysFinding(t)
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	days := "days"
	for _, memo := range []string{"From March 1, 2024 to March 22, 2024 is 21 days [F1].", "The answer is due 21 days after service [F1]."} {
		sec := labelledSection(memo, func(s analysis.Span) analysis.Label {
			l := asDate(s)
			if s.Text == "21" {
				l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &days
			}
			return l
		})
		if lines, _, _, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w}); len(lines) == 0 && len(computed) == 0 {
			t.Errorf("%q silent", memo)
		}
	}
}

// refFinding is F1 over quote with values (each read by its label: a ref's amount is the figure
// reader's reading of it), its quote's spans read by quoteLabel, and the worksheet fn over refs.
func refFinding(t *testing.T, quote string, quoteLabel func(analysis.Span) analysis.Label, fn string, values ...analysis.Value) (Finding, analysis.Worksheet) {
	t.Helper()
	p := analysis.PassageOf("q1", quote)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := quoteLabel(s)
		l.Span = analysis.SpanID("q1", i)
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	f := Finding{Finding: analysis.Finding{FID: "F1", Verified: true, Values: values}, Citations: []Citation{{Quote: quote, Method: "exact"}}}
	for i := range p.Spans {
		f.Figures = append(f.Figures, rs[analysis.SpanID("q1", i)])
	}
	var ops []analysis.Operand
	for i := range values {
		r := fmt.Sprintf("F1.v%d", i+1)
		ops = append(ops, analysis.Operand{Ref: &r})
	}
	w, err := analysis.Calc([]analysis.Finding{f.Finding}, fn, ops, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f, w
}

// H (one wrong scale): two values read ×10⁶ from "billion" summed by ref; the memo writes the total
// out ("$8,000,000"). It traces only through the worksheet, whose inputs rest on the reader's scale:
// ⚠, and the finding is gated, as limitedScale's would be.
func TestRefScaledSumWrittenOutGated(t *testing.T) {
	quote := "The first instalment is $5 billion. The second instalment is $3 billion."
	wrong := func(analysis.Span) analysis.Label {
		return lab("", analysis.KindMoney, "USD", "1000000", "billion", "", "")
	}
	million := func(n string) *analysis.Reading {
		return &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: n, Scale: "1000000", ScaleFrom: "billion", Text: n}
	}
	f1, w := refFinding(t, quote, wrong, "sum",
		analysis.Value{Entity: "Company", Metric: "first instalment", Value: "$5 billion", Class: "Money", Fig: million("5")},
		analysis.Value{Entity: "Company", Metric: "second instalment", Value: "$3 billion", Class: "Money", Fig: million("3")})
	if w.LiteralOperands {
		t.Fatal("a sum over refs has literal operands")
	}
	sec := labelledSection("The instalments total $8,000,000 [F1].", func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") })
	lines, _, gate, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) == 0 && (len(computed) != 1 || !computed[0].Mismatch || !strings.HasPrefix(computed[0].Text, "⚠ $8,000,000 = $5,000,000 [F1] “") ||
		!strings.Contains(computed[0].Text, ", scaled by “billion”") || gate["F1"] != limitedScale) {
		t.Errorf("$8,000,000 (the record: $8 billion) by a ref-only sum: lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// H: the same sum written out by refs the reader read at scale 1 is plain arithmetic: listed, not
// gated.
func TestRefSumOfWrittenOutAmountsIsArithmetic(t *testing.T) {
	quote := "The first instalment is $5,000,000. The second instalment is $3,000,000."
	money := func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }
	f1, w := refFinding(t, quote, money, "sum",
		analysis.Value{Entity: "Company", Metric: "first instalment", Value: "$5,000,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000"}},
		analysis.Value{Entity: "Company", Metric: "second instalment", Value: "$3,000,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "3000000", Text: "3,000,000"}})
	sec := labelledSection("The instalments total $8,000,000 [F1].", money)
	lines, _, gate, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(gate) != 0 || len(computed) != 1 || computed[0].Check || !strings.HasPrefix(computed[0].Text, "$8,000,000 = $5,000,000 [F1] “…first instalment is $5,000,000.") ||
		!strings.HasSuffix(computed[0].Text, " + $3,000,000 [F1] “…second instalment is $3,000,000.”") {
		t.Errorf("lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// H (one wrong kind): a value the reader read as a count, used by ref as a period of days, is the
// period only by the value's unit: ⚠, and the finding is gated.
func TestRefCountUsedAsPeriodGated(t *testing.T) {
	quote := "The complaint was served on March 1, 2024. An answer is due within 21 days."
	label := func(s analysis.Span) analysis.Label {
		return lab("", cmpStr(s.Text == "21", analysis.KindCount, analysis.KindDate), "", "1", "", "", "")
	}
	f1, w := refFinding(t, quote, label, "date_add",
		analysis.Value{Entity: "Plaintiff", Metric: "service date", Value: "March 1, 2024", Class: "Date"},
		analysis.Value{Entity: "Defendant", Metric: "answer period", Value: "21", Unit: "days", Class: "Count", Fig: &analysis.Reading{Kind: analysis.KindCount, Amount: "21", Text: "21"}})
	lines, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) == 0 && (len(computed) != 1 || !computed[0].Mismatch || !strings.Contains(computed[0].Text, "21 days [F1] “") ||
		!strings.Contains(computed[0].Text, ": the figure reader read it as a count figure, not a period in days") || gate["F1"] != limitedUnit) {
		t.Errorf("lint %q, gate %v, computed %+v", lines, gate, computed)
	}
}

// H: a value whose unit is business days never feeds date_add's calendar days by ref (resolve keeps
// the unit); business_days counts it.
func TestBusinessDaysRefIsNoCalendarPeriod(t *testing.T) {
	f := analysis.Finding{FID: "F1", Verified: true, Values: []analysis.Value{
		{Entity: "Plaintiff", Metric: "service date", Value: "March 1, 2024", Class: "Date"},
		{Entity: "Defendant", Metric: "answer period", Value: "21", Unit: "business days", Class: "Count", Fig: &analysis.Reading{Kind: analysis.KindCount, Amount: "21", Text: "21"}}}}
	r1, r2 := "F1.v1", "F1.v2"
	ops := []analysis.Operand{{Ref: &r1}, {Ref: &r2}}
	if w, err := analysis.Calc([]analysis.Finding{f}, "date_add", ops, nil); err == nil {
		t.Errorf("date_add over 21 business days: %+v", w)
	}
	if w, err := analysis.Calc([]analysis.Finding{f}, "business_days", ops, nil); err != nil || w.Result != "2024-04-01" {
		t.Errorf("business_days over 21 business days: %+v, %v", w, err)
	}
}

// L1: a deadline computed from a literal whose cited words are not its unit gates its finding, as a
// memo period with those words does.
func TestMismatchedLiteralGatesItsFinding(t *testing.T) {
	f1 := businessDaysFinding(t)
	w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{"21", "days"})
	_, _, gate, computed := lintLimited("I1", labelledSection("The answer is due March 22, 2024 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(computed) != 1 || !computed[0].Mismatch || gate["F1"] != limitedUnit {
		t.Errorf("gate %v, computed %+v", gate, computed)
	}
}

// L3: a difference of counts read with no unit words (section numbers, perhaps) is checked first,
// and a negative input is written "− (−45)".
func TestCountInputsCheckedFirst(t *testing.T) {
	quote := "Sections 10.1 and 2.1 apply; notice is due forty-five (45) days before, or -45 days."
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}}}
	p := analysis.PassageOf("q1", quote)
	var ls []analysis.Label
	for i, sp := range p.Spans {
		l := lab(analysis.SpanID("q1", i), analysis.KindCount, "", "1", "", "", cmpStr(strings.HasPrefix(sp.Text, "-") || sp.Start > 0 && quote[sp.Start-1] == '-', "negative", ""))
		if strings.Contains(sp.Text, ".") {
			l.Decimal = "."
		}
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	for i := range p.Spans {
		if r := rs[analysis.SpanID("q1", i)]; !r.Unclear {
			f1.Figures = append(f1.Figures, r)
		}
	}
	number := func(analysis.Span) analysis.Label { return lab("", analysis.KindCount, "", "1", "", "", "") }
	for _, c := range []struct {
		w    analysis.Worksheet
		memo string
		want string
	}{
		{sheet(t, "diff", [2]string{"10.1", ""}, [2]string{"2.1", ""}), "The difference is 8 [F1].", "8 = 10.1 [F1] “"},
		{sheet(t, "diff", [2]string{"45", ""}, [2]string{"-45", ""}), "The difference is 90 [F1].", " − (−45) [F1] “"},
	} {
		_, _, _, computed := lintLimited("I1", labelledSection(c.memo, number), map[string]Finding{"F1": f1}, []analysis.Worksheet{c.w})
		if len(computed) != 1 || !computed[0].Check || !strings.Contains(computed[0].Text, c.want) || !strings.Contains(computed[0].Text, ", read as a count figure") {
			t.Errorf("%q: %+v", c.memo, computed)
		}
	}
}

// L4: a snippet of a CSV or table row starts at the row's first cell, so its party stays.
func TestSnippetKeepsTheRowsFirstCell(t *testing.T) {
	q := "Party,Measure,Country,Amount,Unit\nVeltris Medical B.V.,Turnover,Netherlands,48,EUR million\nHeron Inc.,Turnover,Netherlands,90,EUR million"
	for _, c := range []struct{ num, party string }{{"48", "Veltris Medical B.V."}, {"90", "Heron Inc."}} {
		i := strings.Index(q, ","+c.num+",") + 1
		if s := snippet(q, i, i+len(c.num)); !strings.Contains(s, c.party) {
			t.Errorf("%s: %q drops %q", c.num, s, c.party)
		}
	}
	prose := "The purchase price of $212,500,000 is payable at closing, and the existing 8% stake is valued at $16,000,000 as of today."
	i := strings.Index(prose, "16,000,000")
	if s := snippet(prose, i, i+10); strings.HasPrefix(s, "The purchase") || !strings.Contains(s, "16,000,000") {
		t.Errorf("prose snippet %q", s)
	}
}

// L4: a date input the quote writes another way ("10/21/2026") still gets the quote's words.
func TestDateInputQuotedWhereverWritten(t *testing.T) {
	quote := "The summons was served on 10/21/2026 and an answer is due within 21 days."
	days := "days"
	p := analysis.PassageOf("q1", quote)
	var ls []analysis.Label
	for i, s := range p.Spans {
		l := lab(analysis.SpanID("q1", i), analysis.KindDate, "", "1", "", "", "")
		if s.Text == "21" {
			l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &days
		}
		ls = append(ls, l)
	}
	rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: quote, Method: "exact"}}}
	for i := range p.Spans {
		f1.Figures = append(f1.Figures, rs[analysis.SpanID("q1", i)])
	}
	w := sheet(t, "date_add", [2]string{"2026-10-21", "date"}, [2]string{"21", "days"})
	_, _, _, computed := lintLimited("I1", labelledSection("The answer is due November 11, 2026 [F1].", asDate), map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(computed) != 1 || !strings.Contains(computed[0].Text, "= October 21, 2026 [F1] “…summons was served on 10/21/2026") {
		t.Errorf("%+v", computed)
	}
}

// L5: a worksheet's result the memo writes at a scale ("$228.5 million") is one item of the block,
// with how the memo writes it, not also a separate scale line.
func TestScaledMemoFigureListedWithItsWorksheet(t *testing.T) {
	quote := "The purchase price is $212,500,000. The existing stake is valued at $16,000,000."
	money := func(analysis.Span) analysis.Label { return lab("", analysis.KindMoney, "USD", "1", "", "", "") }
	f1, w := refFinding(t, quote, money, "sum",
		analysis.Value{Entity: "Buyer", Metric: "price", Value: "$212,500,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "212500000", Text: "212,500,000"}},
		analysis.Value{Entity: "Buyer", Metric: "stake", Value: "$16,000,000", Class: "Money", Fig: &analysis.Reading{Kind: analysis.KindMoney, Currency: "USD", Amount: "16000000", Text: "16,000,000"}})
	sec := labelledSection("The total consideration is $228.5 million [F1].", func(analysis.Span) analysis.Label {
		l := lab("", analysis.KindMoney, "USD", "1000000", "million", "", "")
		l.Decimal = "."
		return l
	})
	lines, limited, _, computed := lintLimited("I1", sec, map[string]Finding{"F1": f1}, []analysis.Worksheet{w})
	if len(lines) != 0 || len(limited[limitedScale]) != 0 || len(computed) != 1 || !computed[0].Check ||
		!strings.Contains(computed[0].Text, "$228,500,000 = $212,500,000 [F1]") || !strings.Contains(computed[0].Text, "; written in the memo as $228.5 million") {
		t.Errorf("lint %q, limited %v, computed %+v", lines, limited, computed)
	}
}

// M: the double trace on a large section (n paragraphs each stating a computed deadline and a fee,
// n typed worksheets, n cited quotes). At 60c5bc8, n=80 took 2.3 s on the verifier's machine (1.5 s
// here); at 1c50231, 24.6 s. lintLimited parses each ground once and attributes in one pass.
func largeSection(t testing.TB, n int) (Section, map[string]Finding, []analysis.Worksheet) {
	f1 := Finding{Finding: analysis.Finding{FID: "F1", Verified: true}}
	var sheets []analysis.Worksheet
	var paras []string
	days := "days"
	for i := range n {
		d := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i*3)
		q := fmt.Sprintf("Notice %d was served on %s. A response is due within %d days of notice %d, and the fee is $%d,000.", i, d.Format("January 2, 2006"), 10+i, i, 100+i)
		f1.Citations = append(f1.Citations, Citation{Quote: q, Method: "exact"})
		id := fmt.Sprintf("q%d", i+1)
		p := analysis.PassageOf(id, q)
		var ls []analysis.Label
		for k, s := range p.Spans {
			l := lab(analysis.SpanID(id, k), analysis.KindDate, "", "1", "", "", "")
			switch {
			case strings.HasPrefix(q[s.End:], " days"):
				l.Kind, l.Unit, l.UnitFrom = analysis.KindDuration, &days, &days
			case s.Start > 0 && q[s.Start-1] == '$':
				l = lab(analysis.SpanID(id, k), analysis.KindMoney, "USD", "1", "", "", "")
			case s.Text == fmt.Sprint(i):
				l.Kind = analysis.KindOther
			}
			ls = append(ls, l)
		}
		rs, _ := analysis.ReadLabels([]analysis.Passage{p}, ls)
		for k := range p.Spans {
			f1.Figures = append(f1.Figures, rs[analysis.SpanID(id, k)])
		}
		var os []analysis.Operand
		for _, o := range [][2]string{{d.Format("2006-01-02"), "date"}, {fmt.Sprint(10 + i), "days"}} {
			os = append(os, analysis.Operand{Literal: &o[0], Unit: &o[1]})
		}
		w, err := analysis.Calc(nil, "date_add", os, nil)
		if err != nil {
			t.Fatal(err)
		}
		sheets = append(sheets, w)
		r, _ := analysis.ParseDate(w.Result)
		paras = append(paras, fmt.Sprintf("Response %d is due %s, and response %d's fee is $%d,000 [F1].", i, time.Date(r.Year, time.Month(r.Month), r.Day, 0, 0, 0, 0, time.UTC).Format("January 2, 2006"), i, 100+i))
	}
	return labelledSection(strings.Join(paras, "\n\n"), asDate), map[string]Finding{"F1": f1}, sheets
}

func TestDoubleTraceLargeSection(t *testing.T) {
	sec, use, sheets := largeSection(t, 80)
	start := time.Now()
	_, _, _, computed := lintLimited("I1", sec, use, sheets)
	el := time.Since(start)
	t.Logf("80 paragraphs × 80 worksheets: %v, %d computed", el, len(computed))
	if len(computed) == 0 {
		t.Error("no computed deadline listed")
	}
	if !raceOn && el > 2300*time.Millisecond {
		t.Errorf("80 paragraphs × 80 worksheets took %v (60c5bc8: 2.3 s)", el)
	}
}

func BenchmarkLintLimitedLargeSection(b *testing.B) {
	sec, use, sheets := largeSection(b, 80)
	for b.Loop() {
		lintLimited("I1", sec, use, sheets)
	}
}

// L4: a snippet never cuts a character, always holds its span, and stays short.
func FuzzSnippetBounds(f *testing.F) {
	f.Add("…Inc.,Turnover,Netherlands,90,EUR million", 30, 32)
	f.Add("Kaufpreis von 5 Mrd. € für die Übernahme durch die Gesellschaft", 15, 16)
	f.Add("a,b\nVeltris Medical B.V.,Turnover,Netherlands,48,EUR million", 48, 50)
	f.Fuzz(func(t *testing.T, q string, a, b int) {
		if q == "" || !utf8.ValidString(q) || a < 0 || b > len(q) || a > b || a < len(q) && !utf8.RuneStart(q[a]) || b < len(q) && !utf8.RuneStart(q[b]) {
			return
		}
		s := snippet(q, a, b)
		if !utf8.ValidString(s) {
			t.Fatalf("snippet(%q,%d,%d) = %q: invalid UTF-8", q, a, b, s)
		}
		if !strings.Contains(s, q[a:b]) {
			t.Fatalf("snippet(%q,%d,%d) = %q: lost the span %q", q, a, b, s, q[a:b])
		}
		if len(s) > (b-a)+64+24+2*len("…")+2*utf8.UTFMax {
			t.Fatalf("snippet(%q,%d,%d) = %q: %d bytes", q, a, b, s, len(s))
		}
	})
}

// H e2e (one wrong kind): the "21" of "21 business days" read as a count feeds date_add by ref; the
// value's unit keeps "business days", so date_add refuses it (the scripted lead then runs out), and
// the memo's calendar "March 22, 2024" (the record: April 1) is never delivered silently.
func TestRefOnlyBusinessDaysDeadlineNeverSilent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024. An answer is due within 21 business days.\n")
	f.reads = map[string]analysis.Label{"quote:21": lab("", analysis.KindCount, "", "1", "", "", ""), "value:21": lab("", analysis.KindCount, "", "1", "", "", "")}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	period := m{"entity": "Defendant", "metric": "answer period", "value": "21", "unit": "business days", "date": nil, "class": "Count"}
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024; an answer is due within 21 business days.", d1,
			"The complaint was served on March 1, 2024. An answer is due within 21 business days",
			val("Defendant", "service date", "March 1, 2024", "Date"), period))},
		"draft_section": {section("The answer is due March 22, 2024 [F1].", "Due [F1].")},
	}), Config{})
	if ns := f.outputs(v.ID, "numbers"); len(ns) != 1 || !strings.Contains(ns[0], "date_add counts calendar days, and this period is in business days") {
		t.Errorf("date_add over a value in business days was not refused: %q", ns)
	}
	if out := deref(v.Output); strings.Contains(out, "The answer is due March 22, 2024") && !strings.Contains(out, "March 22, 2024 is not traced") && !strings.Contains(out, "⚠ March 22, 2024 =") {
		t.Errorf("a calendar deadline from \"21 business days\" (the record: April 1) delivered neither flagged nor listed ⚠; stop %s\n%s", stop(v), out)
	}
}

// H e2e (one wrong scale): "$5 billion" and "$3 billion" read ×10⁶ from "billion", summed by ref;
// the memo's "$8,000,000" is listed ⚠ and the finding gated, never traced silently.
func TestRefOnlyScaledSumDisclosedAndGated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nThe first instalment is $5 billion. The second instalment is $3 billion.\n")
	wrong := lab("", analysis.KindMoney, "USD", "1000000", "billion", "", "")
	f.reads = map[string]analysis.Label{"quote:5": wrong, "quote:3": wrong, "value:5": wrong, "value:3": wrong}
	d1 := f.docs[0].ID
	ref := func(r string) m { return m{"ref": r, "literal": nil, "unit": nil} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What are the fees?", "Conduct"))), calls(extract(d1, "instalment", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{ref("F1.v1"), ref("F1.v2")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The instalments are $5 billion and $3 billion.", d1,
			"The first instalment is $5 billion. The second instalment is $3 billion",
			val("Company", "first instalment", "$5 billion", "Money"), val("Company", "second instalment", "$3 billion", "Money")))},
		"draft_section": {section("The instalments total $8,000,000 [F1].", "Fees [F1].")},
	}), Config{})
	gated := false
	for _, x := range ledgerFindings(t, f, v.ID) {
		gated = gated || x.FID == "F1" && x.Low
	}
	if out := deref(v.Output); strings.Contains(out, "total $8,000,000") && (!strings.Contains(out, "⚠ $8,000,000 = $5,000,000 [F1]") || !gated) {
		t.Errorf("$8,000,000 (the record: $8 billion) by a ref-only sum, not listed ⚠ or not gated (gated %v); stop %s\n%s", gated, stop(v), out)
	}
}
