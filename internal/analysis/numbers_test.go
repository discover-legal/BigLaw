// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"strings"
	"testing"
)

func ref(s string) *string { return &s }

func ledger() []Finding {
	return read([]Finding{
		{FID: "F1", Verified: true, Values: []Value{{Entity: "Oceanic Fund I LP", Metric: "excess profits", Value: "$7,800,000"}, {Metric: "rate", Value: "81.6%"}}},
		{FID: "F2", Verified: true, Values: []Value{{Entity: "Diana L. Chao", Metric: "excess profits", Value: "$438,000"}, {Metric: "trades", Value: "4,217", Unit: "trades"}}},
		{FID: "F3", Verified: false, Values: []Value{{Metric: "excess profits", Value: "$9,999,999"}}},
		{FID: "F4", Verified: true, Values: []Value{{Metric: "notice date", Value: "November 12, 2026"}, {Metric: "a", Value: "0.1"}, {Metric: "b", Value: "0.2"}, {Metric: "cite", Value: "Section 9.1"}}},
	})
}

func TestCalculateDecimalExact(t *testing.T) {
	for _, c := range []struct {
		fn, want, unit string
		ops            []string
	}{
		{"sum", "8238000", "USD", []string{"F1.v1", "F2.v1"}},
		{"diff", "7362000", "USD", []string{"F1.v1", "F2.v1"}},
		{"sum", "0.3", "", []string{"F4.v2", "F4.v3"}}, // never 0.30000000000000004
		{"pct", "5.615385", "%", []string{"F2.v1", "F1.v1"}},
		{"ratio", "0.5", "", []string{"F4.v2", "F4.v3"}},
	} {
		var ops []Operand
		for _, o := range c.ops {
			ops = append(ops, Operand{Ref: ref(o)})
		}
		w, err := Calc(ledger(), c.fn, ops, nil)
		if err != nil || w.Result != c.want || w.Unit != c.unit || w.LiteralOperands {
			t.Errorf("%s %v = %q %q (%v), want %s %s", c.fn, c.ops, w.Result, w.Unit, err, c.want, c.unit)
		}
	}
	if _, err := Calc(ledger(), "sum", []Operand{{Ref: ref("F1.v1")}, {Ref: ref("F1.v2")}}, nil); err == nil {
		t.Error("dollars were added to a percentage")
	}
}

func TestCalculateBusinessDays(t *testing.T) {
	date, days := ref("date"), ref("days")
	// 2026-11-12 is a Thursday; 10 business days later, skipping Thanksgiving (Nov 26): Nov 27.
	w, err := Calc(ledger(), "business_days", []Operand{{Ref: ref("F4.v1")}, {Literal: ref("10"), Unit: days}}, []string{"2026-11-26"})
	if err != nil || w.Result != "2026-11-27" || w.Unit != "date" || !w.LiteralOperands {
		t.Fatalf("date + 10 business days = %q %v", w.Result, err)
	}
	w, err = Calc(ledger(), "business_days", []Operand{{Literal: ref("2026-11-12"), Unit: date}, {Literal: ref("2026-11-27"), Unit: date}}, []string{"November 26, 2026"})
	if err != nil || w.Result != "10" || w.Steps[0].Value != "2026-11-12 date" {
		t.Fatalf("business days between = %q %v", w.Result, err)
	}
	w, err = Calc(ledger(), "date_add", []Operand{{Ref: ref("F4.v1")}, {Literal: ref("30"), Unit: days}}, nil)
	if err != nil || w.Result != "2026-12-12" {
		t.Fatalf("date_add = %q %v", w.Result, err)
	}
	w, err = Calc(ledger(), "date_diff", []Operand{{Literal: ref("2026-01-01"), Unit: date}, {Ref: ref("F4.v1")}}, nil)
	if err != nil || w.Result != "315" || w.Unit != "days" {
		t.Fatalf("date_diff = %q %v", w.Result, err)
	}
	if _, err := Calc(ledger(), "business_days", []Operand{{Ref: ref("F4.v1")}, {Literal: ref("1"), Unit: days}}, []string{"someday"}); err == nil {
		t.Fatal("an unreadable holiday was ignored")
	}
}

func TestCalculateWorksheetReproducible(t *testing.T) {
	ops := []Operand{{Ref: ref("F1.v1")}, {Ref: ref("F2.v1")}}
	a, _ := Calc(ledger(), "sum", ops, nil)
	b, _ := Calc(ledger(), "sum", ops, nil)
	c, _ := Calc(ledger(), "diff", ops, nil)
	d1, _ := Calc(ledger(), "business_days", []Operand{{Ref: ref("F4.v1")}, {Literal: ref("3"), Unit: ref("days")}}, []string{"2027-01-01"})
	d2, _ := Calc(ledger(), "business_days", []Operand{{Ref: ref("F4.v1")}, {Literal: ref("3"), Unit: ref("days")}}, []string{"2027-01-02"})
	if d1.Result != d2.Result || d1.WID == d2.WID {
		t.Fatalf("the holidays are part of the worksheet's identity: %s %s", d1.WID, d2.WID)
	}
	if a.WID == "" || a.WID != b.WID || a.WID == c.WID || len(a.Steps) != 3 || !strings.Contains(a.Steps[0].Value, "F1.v1 = $7,800,000") {
		t.Fatalf("worksheets %+v / %+v / %+v", a, b, c)
	}
}

// Trap: a quarantined or unknown finding is refused, never treated as 0.
func TestCalculateOperandsMustBeVerified(t *testing.T) {
	for r, want := range map[string]string{"F3.v1": "F3 is quarantined", "F9.v1": "no finding F9", "F1.v7": "F1 has 2 values", "F1": "not a value reference"} {
		_, err := Calc(ledger(), "sum", []Operand{{Ref: ref("F1.v1")}, {Ref: ref(r)}}, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", r, err, want)
		}
	}
}

// Carried from v1 writer tests (4 handle vectors), against {Codename} handles.
func TestResolveFigureHandles_CurrencyNotEaten(t *testing.T) {
	hs := []Handle{{Handle: "{Zephyr}", Value: "$7,800,000"}, {Handle: "{Quasar}", Value: "$1,234.56"}, {Handle: "{Nimbus}", Value: "81.6%"}}
	for in, want := range map[string]string{
		"The scheme generated approximately {Zephyr} in excess profits.": "approximately $7,800,000 in excess profits",
		"a fee ({Quasar}).":              "($1,234.56).",
		"rates of {Nimbus}, and rising.": "81.6%, and rising.",
		"total:{Zephyr}.":                "total:$7,800,000.",
		"{NIMBUS} was the rate.":         "81.6% was the rate.",
		"the Zephyrus wind and Zephyr":   "the Zephyrus wind and Zephyr", // only {braced} codenames are handles
	} {
		if out, err := Substitute(in, hs); err != nil || !strings.Contains(out, want) {
			t.Errorf("Substitute(%q) = %q %v, want %q", in, out, err, want)
		}
	}
}

func TestResolveFigureHandles_KeepsParagraphBreaks(t *testing.T) {
	out, err := Substitute("First paragraph about {Zephyr}.\n\nSecond paragraph.", []Handle{{Handle: "{Zephyr}", Value: "$7,800,000"}})
	if err != nil || !strings.Contains(out, "\n\n") || !strings.Contains(out, "$7,800,000.") {
		t.Errorf("%q %v", out, err)
	}
}

func TestAssignHandles_CarriesCitationsIntact(t *testing.T) {
	hs := Handles(ledger(), []string{"F4"}, 0)
	var cite Handle
	for _, h := range hs {
		if h.Value == "Section 9.1" {
			cite = h
		}
	}
	out, err := Substitute("Pre-clearance was required by "+cite.Handle+" of the Manual.", hs)
	if cite.Handle == "" || err != nil || !strings.Contains(out, "required by Section 9.1 of the Manual") {
		t.Fatalf("citation not carried verbatim: %q %v (%+v)", out, err, hs)
	}
}

// v1's salience floor: headline figures keep their handles, bare noise falls off under the cap.
func TestAssignHandlesSurfacesSalient(t *testing.T) {
	var vals []Value
	for _, v := range []string{"3", "62%", "2", "1.5%", "81.6%", "$2.3", "$185", "$92", "$40", "20%", "6%", "15%", "5%", "$87,655.90", "$8,238,000", "$3.1", "$438,000", "12%", "1", "4,217"} {
		vals = append(vals, Value{Metric: "figure", Value: v})
	}
	got := map[string]bool{}
	for _, h := range Handles(read([]Finding{{FID: "F1", Verified: true, Values: vals}}), nil, 12) {
		got[h.Value] = true
	}
	for _, want := range []string{"$8,238,000", "$438,000", "4,217"} {
		if !got[want] {
			t.Errorf("salient figure %q dropped", want)
		}
	}
	if got["3"] || got["1"] || got["2"] {
		t.Error("bare-noise figures survived the cap ahead of salient ones")
	}
}

func TestFigureHandlesSubstituteExactValue(t *testing.T) {
	hs := Handles(ledger(), nil, 0)
	if len(hs) == 0 || hs[0].Value != "$7,800,000" || hs[0].Handle != "{Zephyr}" || hs[0].Ref != "F1.v1" {
		t.Fatalf("handles %+v", hs)
	}
	for _, h := range hs {
		if strings.ContainsAny(h.Handle+h.Label, "0123456789") || h.FID == "F3" || h.Value == "November 12, 2026" {
			t.Fatalf("a handle carries a digit, a quarantined value or a date: %+v", h)
		}
		if out, _ := Substitute("x "+h.Handle+" y", hs); out != "x "+h.Value+" y" {
			t.Fatalf("%s → %q", h.Handle, out)
		}
	}
}

// Trap: a codename with no figure is a lint error, never guessed.
func TestUnknownHandleRejected(t *testing.T) {
	_, err := Substitute("The loss was {Vireo} and {Zephyr}.", []Handle{{Handle: "{Zephyr}", Value: "$1"}})
	if err == nil || !strings.Contains(err.Error(), "{Vireo}") {
		t.Fatalf("unknown handle: %v", err)
	}
	if out, err := Substitute("A {curly} aside.", nil); err != nil || out != "A {curly} aside." {
		t.Fatalf("plain braces: %q %v", out, err)
	}
}

func TestKeyFiguresTableOnlyVerified(t *testing.T) {
	tab := KeyFigures(ledger(), nil)
	if !strings.Contains(tab, "| Oceanic Fund I LP excess profits | $7,800,000 | [F1] |") || strings.Contains(tab, "9,999,999") || strings.Contains(tab, "[F3]") {
		t.Fatalf("table:\n%s", tab)
	}
	if tab := KeyFigures(read([]Finding{{FID: "F1", Verified: true, Values: []Value{{Metric: "a|b", Value: "$1"}}}}), nil); !strings.Contains(tab, `a\|b`) {
		t.Fatalf("pipe not escaped: %s", tab)
	}
}

func TestCanonReadsMoney(t *testing.T) {
	for in, want := range map[string]string{"$7,800,000": "usd:7800000", "$438,000.00": "usd:438000", "$22.2 million": "usd:22200000", "$1,234.56": "usd:1234.56"} {
		if _, got, _, ok := Canon(withFig(Value{Value: in, Class: "Money"})); !ok || got != want { // the figure reader's reading (its stand-in here)
			t.Errorf("Canon(%q) = %s,%v want %s", in, got, ok, want)
		}
	}
}

func TestFigureSalienceRanking(t *testing.T) {
	for _, c := range [][2]string{{"$8,238,000", "$185"}, {"4,217", "3"}, {"$438,000", "12%"}, {"81.6%", "3"}, {"$92,290.00", "$40"}} {
		if Salience(withFig(Value{Value: c[0]})) <= Salience(withFig(Value{Value: c[1]})) {
			t.Errorf("salience(%q) should exceed salience(%q)", c[0], c[1])
		}
	}
}

// r1: a money value in another currency is not dollars: it keeps its unit, so a sum across
// currencies is refused instead of being labelled USD.
func TestCalculateKeepsCurrency(t *testing.T) {
	fs := append(ledger(), read([]Finding{{FID: "F5", Verified: true, Values: []Value{{Metric: "deposit", Value: "500,000", Unit: "GBP", Class: "Money"}}}})...)
	if w, err := Calc(fs, "sum", []Operand{{Ref: ref("F5.v1")}, {Ref: ref("F2.v1")}}, nil); err == nil {
		t.Fatalf("GBP + USD = %s %s", w.Result, w.Unit)
	}
	if w, err := Calc(fs, "sum", []Operand{{Ref: ref("F5.v1")}, {Literal: ref("1"), Unit: ref("gbp")}}, nil); err != nil || w.Result != "500001" || w.Unit != "GBP" { // a currency code, however its unit is written
		t.Fatalf("GBP + GBP = %q %q (%v)", w.Result, w.Unit, err)
	}
}

// r1: a date difference over centuries (a 999-year lease) is exact, not a saturated duration.
func TestCalculateDateDiffLongSpan(t *testing.T) {
	w, err := Calc(nil, "date_diff", []Operand{{Literal: ref("1990-01-01"), Unit: ref("date")}, {Literal: ref("2989-01-01"), Unit: ref("date")}}, nil)
	if err != nil || w.Result != "364878" {
		t.Fatalf("date_diff = %q (%v), want 364878", w.Result, err)
	}
}

// r1: a figure's codename does not depend on which findings a call named: {Zephyr} in one draft
// section names the same figure as {Zephyr} in another.
func TestHandlesStableAcrossCalls(t *testing.T) {
	all := map[string]string{}
	for _, h := range Handles(ledger(), nil, 0) {
		all[h.Ref] = h.Handle
	}
	for _, sub := range [][]string{{"F2"}, {"F4"}, {"F4", "F2"}} {
		for _, h := range Handles(ledger(), sub, 1) {
			if all[h.Ref] != h.Handle {
				t.Fatalf("fids %v: %s is %s, but %s over the whole ledger", sub, h.Ref, h.Handle, all[h.Ref])
			}
		}
	}
}

// r2 verifier N2: business_days with the dates reversed is refused (it once counted 0 days).
func TestCalculateBusinessDaysRefusesReversedDates(t *testing.T) {
	date := ref("date")
	_, err := Calc(ledger(), "business_days", []Operand{{Literal: ref("2026-11-27"), Unit: date}, {Literal: ref("2026-11-12"), Unit: date}}, nil)
	if err == nil || !strings.Contains(err.Error(), "earlier date first") {
		t.Fatalf("reversed dates: %v", err)
	}
}

// TestCalcAcceptsWhatModelsSend (bench: the lead sent "F30.d1"-style refs, a negative day count
// and a deadline that lands on a holiday, and every call failed or was wrong): unambiguous forms
// are read, a court deadline rolls off weekends and holidays (forward after an event, back before
// one), "35% of" multiplies, and what cannot be read says how to send it.
func TestCalcAcceptsWhatModelsSend(t *testing.T) {
	fs := read([]Finding{
		{FID: "F30", Verified: true, Values: []Value{{Metric: "served", Value: "10/21/2026"}}},
		{FID: "F31", Verified: true, Values: []Value{{Metric: "hearing", Value: "December 3, 2026"}, {Metric: "report lead time", Value: "7 days", Fig: fig(KindOther, "", "7", "")}}},
		{FID: "F32", Verified: true, Values: []Value{{Metric: "shortfall", Value: "$612,400"}, {Metric: "cap", Value: "35%"}}},
	})
	holidays := []string{"2026-11-11", "2026-11-26"}
	days, date := ref("days"), ref("date")
	for _, c := range []struct {
		name, fn string
		ops      []Operand
		hol      []string
		want     string
	}{
		{"F30.d1 is the date", "date_add", []Operand{{Ref: ref("F30.d1")}, {Literal: ref("21"), Unit: days}}, nil, "2026-11-11"},
		{"[F30].v1, a literal \"21 days\"", "date_add", []Operand{{Ref: ref("[F30].v1")}, {Literal: ref("21 days")}}, nil, "2026-11-11"},
		{"a bare ref names a finding's only value", "date_diff", []Operand{{Literal: ref("2026-10-09")}, {Ref: ref("f30")}}, nil, "12"},
		{"answer: 21 days lands on Veterans Day, rolls to Thursday", "date_add", []Operand{{Ref: ref("F30.v1")}, {Literal: ref("21"), Unit: days}}, holidays, "2026-11-12"},
		{"count then date", "date_add", []Operand{{Literal: ref("21"), Unit: days}, {Ref: ref("F30.v1")}}, holidays, "2026-11-12"},
		{"report: 7 days before the hearing is Thanksgiving, rolls back", "date_add", []Operand{{Ref: ref("F31.v1")}, {Literal: ref("-7"), Unit: days}}, holidays, "2026-11-25"},
		{"a value of days by ref", "business_days", []Operand{{Ref: ref("F31.d1")}, {Ref: ref("F31.v2")}}, holidays, "2026-12-14"},
		{"35% of the shortfall", "product", []Operand{{Ref: ref("F32.v1")}, {Ref: ref("F32 v2")}}, nil, "214340"},
		{"a typed date without its unit", "date_add", []Operand{{Literal: ref("2026-09-25")}, {Literal: ref("30"), Unit: ref("calendar days")}}, nil, "2026-10-25"},
		{"a percentage of a percentage", "product", []Operand{{Ref: ref("F32.v2")}, {Literal: ref("50"), Unit: ref("%")}}, nil, "17.5"},
	} {
		w, err := Calc(fs, c.fn, c.ops, c.hol)
		if err != nil || w.Result != c.want {
			t.Errorf("%s: %q %v, want %s", c.name, w.Result, err, c.want)
		}
	}
	w, _ := Calc(fs, "date_add", []Operand{{Ref: ref("F30.v1")}, {Literal: ref("21"), Unit: days}}, holidays)
	if len(w.Steps) != 5 || w.Steps[2] != (SheetStep{"raw date", "2026-11-11 (Wednesday, a holiday)"}) || w.Steps[3].Step != "rolled forward" {
		t.Errorf("the worksheet shows the raw date and the roll: %+v", w.Steps)
	}
	if w, _ := Calc(fs, "product", []Operand{{Ref: ref("F32.v1")}, {Ref: ref("F32.v2")}}, nil); w.Unit != "USD" {
		t.Errorf("35%% of dollars is dollars: %+v", w)
	}
	for _, c := range []struct {
		fn   string
		ops  []Operand
		want []string
	}{
		{"date_add", []Operand{{Ref: ref("F32.d1")}, {Literal: ref("1"), Unit: days}}, []string{"F32 has 2 values", "F32.v1 = $612,400", `{"ref": "F32.v1"`}},
		{"date_add", []Operand{{Literal: ref("2026-11-12"), Unit: date}, {Literal: ref("2026-11-13"), Unit: date}}, []string{"it got a date, a date", `"fn": "date_add"`, `"unit": "days"`}},
		{"product", []Operand{{Ref: ref("F32.v1")}, {Ref: ref("F32.v1")}}, []string{"at most one of them an amount of money"}},
		{"sum", []Operand{{}}, []string{`{"ref": "F12.v1"`}},
	} {
		if _, err := Calc(fs, c.fn, c.ops, nil); err == nil || !containsAll(err.Error(), c.want) {
			t.Errorf("%s %+v: %v, want %q", c.fn, c.ops, err, c.want)
		}
	}
}

func containsAll(s string, subs []string) bool {
	for _, x := range subs {
		if !strings.Contains(s, x) {
			return false
		}
	}
	return true
}

// r7 M4: product and ratio take any numbers with at most one amount of money (a ratio: one
// currency), exact: shares × price × multiple is money, two amounts of money multiplied or two
// currencies divided are refused, and an amount over a count is per unit.
func TestCalcProductsAndRatiosOfOneCurrency(t *testing.T) {
	lit := func(x, unit string) Operand {
		o := Operand{Literal: &x}
		if unit != "" {
			o.Unit = &unit
		}
		return o
	}
	for _, c := range []struct {
		fn           string
		ops          []Operand
		result, unit string
	}{
		{"product", []Operand{lit("1,200,000", ""), lit("5.00", "USD"), lit("1.5", "")}, "9000000", "USD"},
		{"product", []Operand{lit("2.0", ""), lit("385000", "USD")}, "770000", "USD"},
		{"product", []Operand{lit("10", "%"), lit("84000000", "USD")}, "8400000", "USD"},
		{"product", []Operand{lit("1,200,000", "shares"), lit("2", "")}, "2400000", ""},
		{"product", []Operand{lit("1,200,000", "shares"), lit("50", "%")}, "600000", "shares"},
		{"ratio", []Operand{lit("9,000,000", "USD"), lit("1,200,000", "")}, "7.5", "USD"},
		{"ratio", []Operand{lit("9,000,000", "USD"), lit("3,000,000", "USD")}, "3", ""},
	} {
		w, err := Calc(nil, c.fn, c.ops, nil)
		if err != nil || w.Result != c.result || w.Unit != c.unit {
			t.Errorf("%s %v: %s %q (%v), want %s %q", c.fn, w.Steps, w.Result, w.Unit, err, c.result, c.unit)
		}
	}
	for _, c := range []struct {
		fn  string
		ops []Operand
	}{
		{"product", []Operand{lit("5", "USD"), lit("2", "USD")}},
		{"product", []Operand{lit("5", "USD"), lit("2", "EUR"), lit("3", "")}},
		{"ratio", []Operand{lit("5", "USD"), lit("2", "EUR")}},
	} {
		if _, err := Calc(nil, c.fn, c.ops, nil); err == nil || !strings.Contains(err.Error(), "money") && !strings.Contains(err.Error(), "currency") {
			t.Errorf("%s %+v: %v, want a refusal naming the money rule", c.fn, c.ops, err)
		}
	}
}
