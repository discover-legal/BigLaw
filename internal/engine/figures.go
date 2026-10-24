// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"slices"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The figure parser is analysis.Figures (internal/analysis/figures.go, specified by the corpus in
// its figures_test.go): the only number reader. Typed values (valueStated), claimSums, G8
// (Untraced), the committee's statesValue and coverage's addressed read text through it here;
// Canon, Join, Calc and Salience read it in analysis.

type figure = analysis.Figure

// figuresIn is the parser's reading of text: its dates (as keys) and its figures.
func figuresIn(text string) ([]string, []figure) { return analysis.Figures(text) }

// valueInQuote reports whether a typed value is stated in the quotes (valueStated).
func valueInQuote(v analysis.Value, quotes string) bool {
	ok, _, _ := valueStated(v, quotes)
	return ok
}

// valueStated checks a typed value against its verified quotes. A quantity (money, a count, a
// percentage) is read by the figure parser and needs a quote figure that admits it; cur is the
// matched figure's currency. A date or any other value needs its text, or its figures (covered).
// why says why a value is not admitted.
func valueStated(v analysis.Value, quotes string) (ok bool, cur, why string) {
	if strings.TrimSpace(v.Value) == "" {
		return false, "", "it is empty"
	}
	text := strings.Contains(strings.ToLower(norm(quotes)), strings.ToLower(norm(v.Value)))
	cl := analysis.FigureClassOf(v)
	if cl == analysis.UnknownFig { // a date, a name, an identifier
		if text || covered(v.Value, quotes) {
			return true, "", ""
		}
		return false, "", "its quotes do not state it"
	}
	want, why := analysis.ValueFigure(v)
	if why != "" {
		if text && !strings.ContainsFunc(v.Value, unicode.IsDigit) { // a written figure the parser does not read, stated as written
			return true, "", ""
		}
		return false, "", why
	}
	_, qs := figuresIn(quotes)
	named := analysis.CurrenciesNamed(quotes)
	why = "no figure of " + cmpStr(want.Neg, "-", "") + analysis.Decimal(want.Amount) + " in its quotes"
	for _, q := range qs {
		c, ok, w := analysis.Admits(want, cl, q, named)
		if ok {
			return true, c, ""
		}
		if w != "" {
			why = w
		}
	}
	return false, "", why
}

// figureStated reports whether any of fs traces m (analysis.Traces).
func figureStated(m figure, fs []figure) bool {
	return slices.ContainsFunc(fs, func(g figure) bool { return analysis.Traces(m, g) })
}
