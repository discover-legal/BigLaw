// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
)

// figureProperties are the parser's invariants over any text: no panic, spans in bounds and never
// overlapping (lab-r4 r3 H1: "1,000 USD 2,000 USD" overlapped and panicked), and every unambiguous
// figure renders to a form the parser reads back to the same identity (parse → render → parse).
func figureProperties(t *testing.T, s string) {
	t.Helper()
	_, fs := Figures(s)
	for i, x := range fs {
		if x.Start < 0 || x.End > len(s) || x.Start > x.End || x.Amount == nil {
			t.Fatalf("bad span [%d:%d] in %q", x.Start, x.End, s)
		}
		if i > 0 && fs[i-1].End > x.Start {
			t.Fatalf("overlapping spans %q and %q in %q", s[fs[i-1].Start:fs[i-1].End], s[x.Start:x.End], s)
		}
		if x.Amb || x.Cur != "" && !isoCodes[x.Cur] && x.Cur != "kr" {
			continue
		}
		back := valueFigures(x.Render())
		if len(back) != 1 || back[0].String() != x.String() {
			t.Fatalf("%q in %q renders as %q, which reads %v", s[x.Start:x.End], s, x.Render(), back)
		}
	}
}

func groupUS(s string) string {
	for k := len(s) - 3; k > 0; k -= 3 {
		s = s[:k] + "," + s[k:]
	}
	return s
}

// TestFigurePropertyGenerated (the lab-r4 r3 verifier's generated property test, kept): 200,000
// figures over currencies before and after the number, scale words, US and continental separators,
// "-", "−" and parentheses, percent forms and count nouns, each in a sentence or a statement's column.
// Each reads as exactly the figure written (class, sign, currency, amount), unless its form reads two
// ways by design (one continental group of three, "CHF 2.327" or "7.397 trades", is ambiguous, or a
// decimal point beside a scale or a percent); and the properties hold.
func TestFigurePropertyGenerated(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	scales := []struct {
		w   string
		mul int64
	}{{"", 1}, {" million", 1e6}, {" billion", 1e9}, {" thousand", 1e3}, {" mn", 1e6}, {" bn", 1e9}}
	n := 200000
	if testing.Short() {
		n = 20000
	}
	bad := 0
	for range n {
		cls := []FigClass{MoneyFig, MoneyFig, PercentFig, CountFig}[r.IntN(4)]
		cur := ""
		if cls == MoneyFig {
			cur = []string{"USD", "EUR", "GBP", "CHF", "CAD", "JPY", "INR"}[r.IntN(7)]
		}
		intPart := int64(r.IntN(10))*int64([]int{1, 10, 100, 1000, 10000, 1000000, 1}[r.IntN(7)]) + int64(r.IntN(999))
		frac := ""
		if r.IntN(3) == 0 {
			frac = []string{"5", "25", "50", "00", "75"}[r.IntN(5)]
		}
		sc := scales[0]
		if cls != PercentFig && r.IntN(3) == 0 {
			sc = scales[r.IntN(len(scales))]
		}
		neg := cls != CountFig && r.IntN(4) == 0
		amt, _ := new(big.Rat).SetString(fmt.Sprint(intPart) + map[bool]string{true: "." + frac}[frac != ""])
		amt.Mul(amt, new(big.Rat).SetInt64(sc.mul))
		digits := fmt.Sprint(intPart)
		num := digits
		switch r.IntN(4) {
		case 1:
			num = groupUS(digits)
		case 2:
			num = strings.ReplaceAll(groupUS(digits), ",", ".")
			if frac != "" {
				num += "," + frac
				frac = ""
			}
		}
		if frac != "" {
			num += "." + frac
		}
		num += sc.w
		var s string
		switch cls {
		case PercentFig:
			s = num + []string{"%", " %", " per cent", " percent"}[r.IntN(4)]
		case CountFig:
			s = num + []string{" shares", " days", " employees", " trades"}[r.IntN(4)]
		default:
			pre := map[string][]string{"USD": {"$", "USD ", "US$"}, "EUR": {"€", "EUR "}, "GBP": {"£", "GBP "}, "CHF": {"CHF ", "Fr. "}, "CAD": {"C$", "CAD ", "CAD$"},
				"JPY": {"¥", "JPY "}, "INR": {"₹", "INR ", "Rs. "}}[cur]
			post := map[string][]string{"USD": {" USD", " dollars", " US dollars"}, "EUR": {" EUR", " €", " euros"}, "GBP": {" GBP", " pounds"},
				"CHF": {" CHF", " Swiss francs"}, "CAD": {" CAD", " Canadian dollars"}, "JPY": {" JPY", " yen", "円"}, "INR": {" INR", " rupees"}}[cur]
			if r.IntN(2) == 0 {
				s = pre[r.IntN(len(pre))] + num
			} else {
				s = num + post[r.IntN(len(post))]
			}
		}
		ctx := []string{"The Purchaser shall pay %s on Closing.", "Total:\t%s", "%s", "Net: %s."}[r.IntN(4)]
		if neg {
			switch r.IntN(3) {
			case 0:
				s = "-" + s
			case 1:
				s = "−" + s
			default:
				s, ctx = "("+s+")", "Total:\t%s"
			}
			if ctx != "Total:\t%s" {
				ctx = "Total: %s"
			}
		}
		line := fmt.Sprintf(ctx, s)
		figureProperties(t, line)
		_, fs := Figures(line)
		want := Figure{Amount: amt, Neg: neg, Cur: cur, Class: cls}
		if len(fs) != 1 || !continentalTie(num) && (fs[0].Amb || fs[0].String() != want.String()) {
			if bad++; bad <= 20 {
				t.Errorf("%q reads %v, want %v", line, fs, want)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d generated figures misread", bad, n)
	}
}

// continentalTie reports whether a number's form reads two ways by design: one dot group of three
// digits ("2.327"), which is a decimal point or a thousands group.
func continentalTie(num string) bool {
	f := strings.Fields(num)[0]
	return strings.Count(f, ".") == 1 && !strings.Contains(f, ",") && len(f)-strings.IndexByte(f, '.')-1 == 3
}

// FuzzFigureProperties: the properties over arbitrary text (go test -fuzz=FuzzFigureProperties).
func FuzzFigureProperties(f *testing.F) {
	for _, c := range figureCorpus {
		f.Add(c.in)
	}
	f.Fuzz(figureProperties)
}
