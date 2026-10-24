// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r7 (critique/biglaw-lab-r4-r7-verifier.md H1, M1, M2, M4, L1, L2): a worksheet traces a memo
// figure only from what the cited quotes state as each operand's kind, and only a figure of its own
// kind; a scale's words are adjacent to its figure; a figure at one scale beside a cited one at another
// is disclosed and gates the citing finding, whichever side is scaled.

// sheet is analysis.Calc over literal operands (a test's worksheet).
func sheet(t *testing.T, fn string, ops ...[2]string) analysis.Worksheet {
	t.Helper()
	var os []analysis.Operand
	for _, o := range ops {
		lit, unit := o[0], o[1]
		x := analysis.Operand{Literal: &lit}
		if unit != "" {
			x.Unit = &unit
		}
		os = append(os, x)
	}
	w, err := analysis.Calc(nil, fn, os, nil)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// memoRead is the memo's readings by offset, each span labelled as given.
func memoRead(memo string, ls ...analysis.Label) map[int]analysis.Reading {
	rs, _ := analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("m", memo)}, ls)
	read := map[int]analysis.Reading{}
	for _, r := range rs {
		read[r.Start] = r
	}
	return read
}

// H1 (e2e): a worksheet of two typed "days" literals traces nothing: sum(9000000000 days, 0 days) once
// traced the memo's "$9,000,000,000" against a $9,000 quote, undisclosed.
func TestDaysLiteralWorksheetTracesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Settlement\nThe settlement payment is $9,000.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What is the settlement?", "Conduct"))), calls(extract(d1, "settlement", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "sum", "operands": []m{op("9000000000", "days"), op("0", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The settlement payment is $9,000.", d1, "The settlement payment is $9,000", val("Company", "settlement payment", "$9,000", "Money")))},
		"draft_section": {section("The settlement is $9,000,000,000 [F1].", "Settlement [F1].")},
	}), Config{})
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 9000000000 is not traced") {
		t.Errorf("a worksheet of two typed \"days\" literals traced the memo's $9,000,000,000; stop %s\n%s", stop(v), fin)
	}
}

// H1 (unit): a worksheet traces a memo figure only of its own kind, from literals the cited quotes
// state as their units say: a share count summed is no money, a day count no amount, and a share count
// typed "USD" no operand; a share count times a price times a multiple is money, and traces.
func TestWorksheetTracesOnlyItsKind(t *testing.T) {
	quote := "The Company shall issue 1,000,000 shares at each of two closings at $5.00 per share, with a 1.5x preference."
	known := []analysis.Reading{{Kind: analysis.KindCount, Amount: "1000000", Text: "1,000,000"}, {Kind: analysis.KindMoney, Currency: "USD", Amount: "5", Text: "5.00"},
		{Kind: analysis.KindCount, Amount: "1.5", Text: "1.5"}}
	cited := map[string]bool{"F1": true}
	money := lab("m.s1", analysis.KindMoney, "USD", "1", "", "", "")
	for name, c := range map[string]struct {
		memo   string
		w      analysis.Worksheet
		traced bool
	}{
		"days literals, any amount":       {"The settlement is $9,000,000,000.", sheet(t, "sum", [2]string{"9000000000", "days"}, [2]string{"0", "days"}), false},
		"share counts summed as money":    {"The Investor paid $2,000,000.", sheet(t, "sum", [2]string{"1,000,000", ""}, [2]string{"1,000,000", ""}), false},
		"a share count typed as dollars":  {"The Investor paid $2,000,000.", sheet(t, "sum", [2]string{"1,000,000", "USD"}, [2]string{"1,000,000", "USD"}), false},
		"shares times price times 1.5":    {"The preference is $7,500,000.", sheet(t, "product", [2]string{"1,000,000", ""}, [2]string{"5.00", "USD"}, [2]string{"1.5", ""}), true},
		"a literal the quotes do not say": {"The preference is $9,000,000.", sheet(t, "product", [2]string{"1,200,000", ""}, [2]string{"5.00", "USD"}, [2]string{"1.5", ""}), false},
	} {
		sheets := tracedSheets([]analysis.Worksheet{c.w}, quote+"\n", cited, known)
		got := g8(c.memo, quote+"\n", 0, nil, memoRead(c.memo, money), known, nil, sheets, cited, nil)
		if (len(got) == 0) != c.traced {
			t.Errorf("%s: %q traced %v by worksheet %s = %s %s (steps %v), want %v", name, c.memo, len(got) == 0, c.w.WID, c.w.Result, c.w.Unit, c.w.Steps, c.traced)
		}
	}
}

// M1 (e2e): a typed day count is no period the record states: date_add(a cited date, 3000 days) traces
// no date (eval's oracle once reached any date so).
func TestTypedDayCountTracesNoDate(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Service\nThe complaint was served on March 1, 2024.\n")
	d1 := f.docs[0].ID
	op := func(lit, unit string) m { return m{"ref": nil, "literal": lit, "unit": unit} }
	v := f.run(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "When is the answer due?", "Conduct"))), calls(extract(d1, "served", "I1")),
			calls(tc("numbers", m{"op": "calc", "fn": "date_add", "operands": []m{op("2024-03-01", "date"), op("3000", "days")}, "holidays": nil, "fids": nil, "salientTop": nil})),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The complaint was served on March 1, 2024.", d1, "The complaint was served on March 1, 2024"))},
		"draft_section": {section("The answer is due May 18, 2032 [F1].", "Due [F1].")},
	}), Config{})
	if fin := strings.Join(f.outputs(v.ID, "finish"), "\n"); !strings.Contains(fin, "the figure 2032-05-18 is not traced") {
		t.Errorf("a typed 3000-day count traced the memo's May 18, 2032; stop %s\n%s", stop(v), fin)
	}
}

// M1: a day count traces when the cited quotes state it (a rule's period) or it is the days between
// two dates they state.
func TestDayCountFromTheRecord(t *testing.T) {
	ground := "The complaint was served on March 1, 2024. An answer is due within 21 days. The hearing is on March 29, 2024.\n"
	known := []analysis.Reading{{Kind: analysis.KindOther, Amount: "21", Text: "21"}}
	for days, want := range map[string]bool{"21": true, "28": true, "-21": true, "30": false} {
		w := sheet(t, "date_add", [2]string{"2024-03-01", "date"}, [2]string{days, "days"})
		if got := len(tracedSheets([]analysis.Worksheet{w}, ground, nil, known)) == 1; got != want {
			t.Errorf("%s days: traced %v, want %v", days, got, want)
		}
	}
}

// M2 (unit): a scale's words from another sentence are never applied: "Program costs (in thousands):
// 1,200." does not scale the next sentence's "$500", so a typed "$500 thousand" is not verified.
func TestEarlierSentenceScaleNotApplied(t *testing.T) {
	q := "Program costs (in thousands): 1,200. The filing fee is $500 per application."
	ps := []analysis.Passage{analysis.PassageOf("q1", q), analysis.PassageOf("v1", "$500 thousand")}
	rs, _ := analysis.ReadLabels(ps, []analysis.Label{
		lab("q1.s1", analysis.KindMoney, "USD", "1000", "in thousands", "", ""),
		lab("q1.s2", analysis.KindMoney, "USD", "1000", "in thousands", "", ""),
		lab("v1.s1", analysis.KindMoney, "USD", "1000", "thousand", "", ""),
	})
	if r := rs["q1.s1"]; r.Scale != "1000" || r.Unclear {
		t.Errorf("the lead-in's own figure lost its scale: %+v", r)
	}
	if r := rs["q1.s2"]; r.Scale != "" || !r.Unclear {
		t.Errorf("another sentence's scale applied: %+v", r)
	}
	if _, ok, low, _ := valueStated(mv3("$500 thousand", "Money"), 0, rs, q); ok && !low {
		t.Errorf("typed \"$500 thousand\" verified from %q", q)
	}
}

// M2 (e2e): the same through a run: the typed value is not verified clearly, and the memo's "$500
// thousand" is flagged or disclosed.
func TestEarlierSentenceScaleReachesNoMemo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nProgram costs (in thousands): 1,200. The filing fee is $500 per application.\n")
	f.reads = map[string]analysis.Label{"quote:500": lab("", analysis.KindMoney, "USD", "1000", "in thousands", "", "")}
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The filing fee is $500 thousand.", d1, "Program costs (in thousands): 1,200. The filing fee is $500 per application", val("Company", "filing fee", "$500 thousand", "Money")))},
		"draft_section": {section("The filing fee is $500 thousand per application [F1].", "Fee [F1].")},
	}), Config{})
	if fs := ledgerFindings(t, f, v.ID); len(fs) == 1 && len(fs[0].Values) == 1 && !fs[0].Low {
		t.Errorf("typed $500 thousand verified (not gated) from a quote stating $500: %+v", fs[0].Values[0].Fig)
	}
	fin := strings.Join(f.outputs(v.ID, "finish"), "\n")
	if strings.Contains(deref(v.Output), "$500 thousand") && !strings.Contains(fin, "not traced") && !strings.Contains(deref(v.Output), "written at another scale") {
		t.Errorf("memo states $500 thousand from a $500 quote; stop %s, no G8 line, no Limitations line", stop(v))
	}
}

// L1: a bare "$" is no scale's words; a "($000)" is.
func TestDollarSignIsNoScaleSource(t *testing.T) {
	q := "The filing fee is $500."
	ps := []analysis.Passage{analysis.PassageOf("q1", q), analysis.PassageOf("v1", "$500")}
	rs, _ := analysis.ReadLabels(ps, []analysis.Label{
		lab("q1.s1", analysis.KindMoney, "USD", "1000", "$", "", ""),
		lab("v1.s1", analysis.KindMoney, "USD", "1000", "$", "", ""),
	})
	if fig, ok, low, _ := valueStated(mv3("$500", "Money"), 0, rs, q); ok && !low && fig != nil && fig.Amount == "500000" {
		t.Errorf("typed \"$500\" verified as %s (scale from %q)", fig.Amount, fig.ScaleFrom)
	}
	rs, _ = analysis.ReadLabels([]analysis.Passage{analysis.PassageOf("q1", "Revenue ($000)\n4,200")}, []analysis.Label{lab("q1.s2", analysis.KindMoney, "USD", "1000", "($000)", "", "")})
	if r := rs["q1.s2"]; r.Amount != "4200000" || r.Unclear {
		t.Errorf("a header's ($000) is a scale's words: %+v", r)
	}
}

// L2, M4: a memo figure at one scale beside a cited one at another is limited (disclosed), and the
// citing finding is gated for a lawyer, whichever side is scaled: "$5 per share" (scaled from "per
// share") beside a cited $5,000,000, and a memo's "$5,000,000" beside a cited "$5 million".
func TestScaledFigureGatesItsFinding(t *testing.T) {
	for name, c := range map[string]struct {
		memo, quote string
		memoLab     analysis.Label
		cited       analysis.Reading
	}{
		"the memo scaled": {"Each holder received $5 per share [F1].", "The aggregate consideration is $5,000,000.", lab("m.s1", analysis.KindMoney, "USD", "1000000", "per share", "", ""),
			analysis.Reading{Span: "q1.s1", Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5,000,000"}},
		"the cited figure scaled": {"Each holder received $5,000,000 in all [F1].", "The aggregate consideration is $5 million.", lab("m.s1", analysis.KindMoney, "USD", "1", "", "", ""),
			analysis.Reading{Span: "q1.s1", Kind: analysis.KindMoney, Currency: "USD", Amount: "5000000", Text: "5", Scale: "1000000", ScaleFrom: "million"}},
	} {
		use := map[string]Finding{"F1": {Finding: analysis.Finding{FID: "F1", Verified: true}, Citations: []Citation{{Quote: c.quote, Method: "exact"}}, Figures: []analysis.Reading{c.cited}}}
		sec := Section{Markdown: c.memo}
		body, _ := memoBody(sec)
		sec.Read = &SectionRead{Hash: textHash(body), Figs: []analysis.Reading{memoRead(body, c.memoLab)[strings.Index(body, "5")]}}
		lines, limited, gate := lintLimited("I1", sec, use, nil)
		if len(lines) != 0 || len(limited[limitedScale]) != 1 || gate["F1"] != limitedScale {
			t.Errorf("%s: lint %q, limited %v, gate %v (want limited and F1 gated)", name, lines, limited, gate)
		}
	}
}
