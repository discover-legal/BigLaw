// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"strings"
	"testing"
)

func ref(s string) *string { return &s }

func ledger() []Finding {
	return []Finding{
		{FID: "F1", Verified: true, Values: []Value{{Entity: "Oceanic Fund I LP", Metric: "excess profits", Value: "$7,800,000"}, {Metric: "rate", Value: "81.6%"}}},
		{FID: "F2", Verified: true, Values: []Value{{Entity: "Diana L. Chao", Metric: "excess profits", Value: "$438,000"}, {Metric: "trades", Value: "4,217", Unit: "trades"}}},
		{FID: "F3", Verified: false, Values: []Value{{Metric: "excess profits", Value: "$9,999,999"}}},
		{FID: "F4", Verified: true, Values: []Value{{Metric: "notice date", Value: "November 12, 2026"}, {Metric: "a", Value: "0.1"}, {Metric: "b", Value: "0.2"}, {Metric: "cite", Value: "Section 9.1"}}},
	}
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
	for _, h := range Handles([]Finding{{FID: "F1", Verified: true, Values: vals}}, nil, 12) {
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
	if tab := KeyFigures([]Finding{{FID: "F1", Verified: true, Values: []Value{{Metric: "a|b", Value: "$1"}}}}, nil); !strings.Contains(tab, `a\|b`) {
		t.Fatalf("pipe not escaped: %s", tab)
	}
}

// Carried normaliser vectors.
func TestParseRecDates(t *testing.T) {
	toks := DatesIn("deletions in Q1 2021, amended in March 2022, and again in early 2023")
	if len(toks) != 3 {
		t.Fatalf("want 3 dates, got %+v", toks)
	}
	byRaw := map[string]Date{}
	for _, tk := range toks {
		byRaw[tk.Raw] = tk.Date
	}
	if d := byRaw["Q1 2021"]; d.Year != 2021 || d.Month != 3 {
		t.Errorf("Q1 2021 parsed as %+v (want quarter-end March 2021)", d)
	}
	if d := byRaw["March 2022"]; d.Year != 2022 || d.Month != 3 {
		t.Errorf("March 2022 parsed as %+v", d)
	}
	if d := byRaw["2023"]; d.Year != 2023 || d.Month != 0 {
		t.Errorf("bare year parsed as %+v", d)
	}
	if got := DatesIn("18 U.S.C. § 1519 and 28 U.S.C. § 2462, under § 2012"); len(got) != 0 {
		t.Errorf("statute numbers parsed as dates: %+v", got)
	}
}

func TestParseMoneyCents(t *testing.T) {
	for in, want := range map[string]int64{"$7,800,000": 780_000_000, "$438,000.00": 43_800_000, "$22.2 million": 2_220_000_000, "$1,234.56": 123_456} {
		if got, ok := MoneyCents(in); !ok || got != want {
			t.Errorf("MoneyCents(%q) = %d,%v want %d", in, got, ok, want)
		}
	}
}

func TestFigureSalienceRanking(t *testing.T) {
	for _, c := range [][2]string{{"$8,238,000", "$185"}, {"4,217", "3"}, {"$438,000", "12%"}, {"81.6%", "3"}, {"$92,290.00", "$40"}} {
		if Salience(c[0]) <= Salience(c[1]) {
			t.Errorf("salience(%q) should exceed salience(%q)", c[0], c[1])
		}
	}
}
