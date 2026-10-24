// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The Computed figures block (lab-r4 r9 H1, r10 H1/usability): every figure a section states that
// traces only through a worksheet over typed literals (G8 itself decides it, tracing the section a
// second time without those worksheets: lintLimited), and every figure that traces only by a reader's
// label the lawyer should check, listed once in the deliverable's Limitations in two tiers:
//   - check first: a figure resting on words the figure reader cited for a unit, a multiple or a
//     scale, on the days between two cited dates, or on a memo figure's label (a period whose words
//     differ from the record's, an identifier with a quantity's digits); a unit whose cited words are
//     not its name is marked;
//   - arithmetic: sums, differences and products of figures the cited quotes write out.
//
// Each input is written as the memo writes figures, with the finding and the quote it comes from.

// computedFig is one item of the block.
type computedFig struct {
	Key      string `json:"key"`  // the worksheet id, or the memo figure's own key: one item however many sections state it
	Text     string `json:"text"` // the item as the block lists it
	Check    bool   `json:"check"`
	Mismatch bool   `json:"mismatch"` // a unit's cited words are not its name: listed first
	Section  string `json:"section"`
}

// citedRead is a cited quote's reading with the finding and quote it is in.
type citedRead struct {
	analysis.Reading
	fid, quote string
}

// citedSources are the cited findings' quote readings (Finding.Figures), each with its finding and
// quote (a reading's span "q2.s1" is in the finding's second verified quote).
func citedSources(fids []string, usable map[string]Finding) []citedRead {
	var out []citedRead
	for _, fid := range fids {
		f := usable[fid]
		var quotes []string
		for _, c := range f.Citations {
			if c.Method != "none" {
				quotes = append(quotes, c.Quote)
			}
		}
		for _, r := range f.Figures {
			q := ""
			if p, _, ok := strings.Cut(strings.TrimPrefix(r.Span, "q"), ".s"); ok {
				if i, err := strconv.Atoi(p); err == nil && i >= 1 && i <= len(quotes) {
					q = quotes[i-1]
				}
			}
			out = append(out, citedRead{r, fid, q})
		}
	}
	return out
}

// computeSet is what a section's block items are written from.
type computeSet struct {
	src    []citedRead
	ground string
	spans  []int64
	sheets []analysis.Worksheet
	usable map[string]Finding
	fids   []string
}

// snippet is the words of quote around [start, end): about 24 bytes each side, cut at spaces.
func snippet(quote string, start, end int) string {
	if quote == "" || start < 0 || end > len(quote) || start > end {
		return ""
	}
	lo, hi := max(0, start-24), min(len(quote), end+24)
	if i := strings.IndexByte(quote[lo:start], ' '); lo > 0 && i >= 0 {
		lo += i + 1
	}
	if i := strings.LastIndexByte(quote[end:hi], ' '); hi < len(quote) && i >= 0 {
		hi = end + i
	}
	for lo > 0 && !utf8.RuneStart(quote[lo]) {
		lo--
	}
	for hi < len(quote) && !utf8.RuneStart(quote[hi]) {
		hi++
	}
	return cmpStr(lo > 0, "…", "") + strings.TrimSpace(quote[lo:hi]) + cmpStr(hi < len(quote), "…", "")
}

// at is where a cited reading is: its finding and the quote around it.
func (c citedRead) at() string {
	return "[" + c.fid + "]" + cmpStr(c.quote != "", " “"+snippet(c.quote, c.Start, c.End)+"”", "")
}

// find is the cited source of a reading (by its place and digits), if any.
func (cs computeSet) find(r analysis.Reading) (citedRead, bool) {
	i := slices.IndexFunc(cs.src, func(c citedRead) bool {
		return c.Span == r.Span && c.Text == r.Text && c.Kind == r.Kind && c.Amount == r.Amount
	})
	if i < 0 {
		return citedRead{}, false
	}
	return cs.src[i], true
}

// holding are the cited findings whose quotes hold a reading.
func (cs computeSet) holding(r analysis.Reading) []string {
	var out []string
	for _, c := range cs.src {
		if c.Span == r.Span && c.Text == r.Text && c.Kind == r.Kind && c.Amount == r.Amount && !slices.Contains(out, c.fid) {
			out = append(out, c.fid)
		}
	}
	return out
}

// memoValue is an amount in a unit as the memo writes it ("$212,500,000", "EUR 2,810,000,000", "24%",
// "21 days", "1 year", "October 6, 2025").
func memoValue(amount, unit string) string {
	switch k := unitKind(unit); {
	case k == analysis.KindDate:
		return sheetValue(amount, "date")
	case k == analysis.KindDuration:
		n := sheetValue(amount, "")
		if strings.TrimPrefix(n, "-") == "1" {
			unit = strings.TrimSuffix(unit, "s")
		}
		return n + " " + unit
	case k == analysis.KindMoney && unit != "USD":
		return unit + " " + sheetValue(amount, "")
	}
	return sheetValue(amount, unit)
}

// operand is one worksheet input as the block writes it, and whether it rests on a reader's words
// (check) or on words that are not its unit's name (mismatch).
func (cs computeSet) operand(fn string, st analysis.SheetStep, depth int) (text string, check, mismatch bool) {
	if ref, val, isRef := strings.Cut(st.Value, " = "); isRef && refRe.MatchString(ref) {
		fid, _, _ := strings.Cut(ref, ".")
		text = strings.TrimSuffix(strings.TrimSuffix(val, " date"), " ") + " [" + fid + "]"
		f := cs.usable[fid]
		if j, err := strconv.Atoi(strings.TrimPrefix(ref[len(fid):], ".v")); err == nil && j >= 1 && j <= len(f.Values) {
			if g := f.Values[j-1].Fig; g != nil && (g.Scale != "" || g.UnitFrom != "") {
				text += readWords(*g)
				check = true
			}
		}
		return text, check, false
	}
	lit, unit := sheetOperand(st.Value)
	amount := lit
	if _, ss := analysis.Literal(lit); len(ss) == 1 && len(ss[0].Readings()) == 1 && unit != "date" {
		amount = analysis.Decimal(ss[0].Readings()[0])
	} else if d, ok := analysis.ParseDate(lit); ok && unit == "date" {
		amount = d.Key()
	}
	text = memoValue(amount, unit)
	quoted := make([]analysis.Reading, len(cs.src))
	for i, c := range cs.src {
		quoted[i] = c.Reading
	}
	o := literalOrigin(fn, st.Value, cs.ground, quoted, cs.spans, cs.sheets)
	switch {
	case unit == "date":
		for _, fid := range cs.fids {
			for _, c := range cs.usable[fid].Citations {
				ds, _ := figures(c.Quote, true)
				if c.Method != "none" && slices.ContainsFunc(ds, func(k string) bool {
					x, _ := analysis.ParseDate(k)
					y, _ := analysis.ParseDate(amount)
					return x.Compatible(y) && x.Day != 0
				}) {
					for _, form := range []string{text, lit} { // the date as the memo writes it, or as typed
						if i := strings.Index(c.Quote, form); i >= 0 {
							return text + " [" + fid + "] “" + snippet(c.Quote, i, i+len(form)) + "”", false, false
						}
					}
					return text + " [" + fid + "]", false, false
				}
			}
		}
	case o.sheet != nil && depth < 2:
		inner, c, m := cs.expr(*o.sheet, depth+1)
		return text + " (" + inner + ")", c, m
	case o.days:
		return text + " (the days between two cited dates)", true, false
	case o.read >= 0:
		c := cs.src[o.read]
		text += " " + c.at()
		if c.Scale != "" || c.UnitFrom != "" {
			text += readWords(c.Reading)
			check = true
		}
		if c.Kind == analysis.KindDuration && unitWords(c.UnitFrom) != unitWords(unit) && !(unit == "days" && fn == "business_days") {
			return text + ": the cited words “" + c.UnitFrom + "” are not its unit (" + unit + ")", true, true
		}
	}
	return text, check, false
}

// readWords is what a reading rests on of the reader's words: its scale's or its unit's.
func readWords(r analysis.Reading) string {
	switch {
	case r.Scale != "":
		return ", scaled by “" + r.ScaleFrom + "”"
	case r.Kind == analysis.KindDuration:
		return ", read in " + r.Unit + " from “" + r.UnitFrom + "”"
	case r.UnitFrom != "":
		return ", a multiple by “" + r.UnitFrom + "”"
	}
	return ""
}

// expr is a worksheet as an expression over its inputs, and whether it rests on a reader's words.
func (cs computeSet) expr(w analysis.Worksheet, depth int) (string, bool, bool) {
	fn := w.Steps[len(w.Steps)-1].Step
	var ops []string
	check, mismatch := false, false
	for _, st := range w.Steps {
		if strings.HasPrefix(st.Step, "operand") {
			t, c, m := cs.operand(fn, st, depth)
			ops, check, mismatch = append(ops, t), check || c, mismatch || m
		}
	}
	join := map[string]string{"sum": " + ", "diff": " − ", "product": " × ", "ratio": " ÷ "}[fn]
	var e string
	switch {
	case join != "":
		e = strings.Join(ops, join)
	case fn == "pct" && len(ops) == 2:
		e = ops[0] + " as a percentage of " + ops[1]
	case (fn == "date_add" || fn == "business_days") && len(ops) == 2 && !strings.HasSuffix(w.Unit, "days"):
		e = ops[0] + cmpStr(strings.HasPrefix(ops[1], "-"), " − "+strings.TrimPrefix(ops[1], "-"), " + "+ops[1]) + cmpStr(fn == "business_days", " (business days)", "")
	case len(ops) == 2:
		e = map[string]string{"date_diff": "days", "business_days": "business days"}[fn] + " from " + ops[0] + " to " + ops[1]
	default:
		e = fn + "(" + strings.Join(ops, "; ") + ")"
	}
	if slices.ContainsFunc(w.Steps, func(st analysis.SheetStep) bool { return strings.HasPrefix(st.Step, "rolled") }) {
		e += ", rolled past a weekend or holiday"
	}
	return e, check, mismatch
}

// sheet is a typed worksheet's item.
func (cs computeSet) sheet(w analysis.Worksheet) computedFig {
	e, check, mismatch := cs.expr(w, 0)
	return computedFig{Key: w.WID, Text: cmpStr(mismatch, "⚠ ", "") + memoValue(w.Result, w.Unit) + " = " + e, Check: check || mismatch, Mismatch: mismatch}
}

// labelled is the item of a memo figure that traces only by a reader's label (limitedUnit,
// limitedOther).
func (cs computeSet) labelled(n untraced) computedFig {
	where := "the cited reading"
	if c, ok := cs.find(n.known); ok {
		where = c.at()
	}
	if n.limited == limitedUnit {
		return computedFig{Key: "unit:" + n.figure + "|" + n.known.UnitFrom, Check: true, Mismatch: true,
			Text: fmt.Sprintf("⚠ %s in the memo (its words “%s”) is the cited period %s only by the figure reader's unit label: the cited words are “%s”; the citing findings are gated",
				n.figure, n.read.UnitFrom, where, n.known.UnitFrom)}
	}
	return computedFig{Key: "other:" + n.figure + "|" + n.known.Kind, Check: true,
		Text: fmt.Sprintf("“%s” in the memo is read as an identifier and traces as written, but %s states %s as %s",
			n.figure, where, n.known.Text, article(n.known.Kind)+cmpStr(n.known.Unit != "", " in "+n.known.Unit, ""))}
}

// computedBlock is the deliverable's Computed figures block: its items once each, the sections that
// state each, check-first items (mismatches at their top) before the arithmetic.
type computedBlock struct {
	Check, Arithmetic []string
}

func blockOf(items []computedFig) computedBlock {
	type item struct {
		computedFig
		sections []string
	}
	var all []item
	for _, c := range items {
		if i := slices.IndexFunc(all, func(x item) bool { return x.Key == c.Key }); i >= 0 {
			if !slices.Contains(all[i].sections, c.Section) {
				all[i].sections = append(all[i].sections, c.Section)
			}
			continue
		}
		all = append(all, item{c, []string{c.Section}})
	}
	slices.SortStableFunc(all, func(a, b item) int { return btoi(b.Mismatch) - btoi(a.Mismatch) })
	var b computedBlock
	for _, x := range all {
		line := x.Text + " (" + strings.Join(x.sections, ", ") + ")"
		if x.Check {
			b.Check = append(b.Check, line)
		} else {
			b.Arithmetic = append(b.Arithmetic, line)
		}
	}
	return b
}

// markdown is the block as the deliverable's Limitations end with it.
func (b computedBlock) markdown() string {
	if len(b.Check)+len(b.Arithmetic) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\n### Computed figures (from typed inputs)\n\nEvery figure below is stated in the memo but computed, or traced only by the figure reader's label; each input names the finding and the quoted words it comes from.\n")
	if len(b.Check) > 0 {
		s.WriteString("\nCheck first (resting on words the figure reader cited for a unit, a multiple or a scale, on the days between two dates, or on a memo figure's label):\n\n")
		for _, l := range b.Check {
			s.WriteString("- " + mdSafe(l, true) + "\n")
		}
	}
	if len(b.Arithmetic) > 0 {
		s.WriteString("\nArithmetic over figures the cited quotes write out:\n\n")
		for _, l := range b.Arithmetic {
			s.WriteString("- " + mdSafe(l, true) + "\n")
		}
	}
	return s.String()
}
