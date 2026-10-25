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

// The Computed figures block (lab-r4 r9 H1, r10 H1/usability, r11 H): every figure a section states
// that traces only through a worksheet (G8 itself decides it, tracing the section a second time
// without the worksheets: lintLimited), whatever its operands are (a ref's value is the figure
// reader's reading of it, its scale and kind; a literal is a cited reading), and every figure that
// traces only by a reader's label the lawyer should check, listed once in the deliverable's
// Limitations in two tiers:
//   - check first: a figure resting on words the figure reader cited for a unit, a multiple or a
//     scale, on a count or number's kind alone (no unit words: it may be an identifier), on the days
//     between two cited dates, or on a memo figure's label (a period whose words differ from the
//     record's, an identifier with a quantity's digits); ⚠ marks an input whose words or kind are not
//     its unit, or a result the memo writes out over inputs read at a scale, and gates its findings;
//   - arithmetic: sums, differences, products and date arithmetic over money, percentages and dates
//     the cited quotes write out.
//
// Each input is written as the memo writes figures, with the finding and the quote it comes from.

// computedFig is one item of the block.
type computedFig struct {
	Key      string            `json:"key"`  // the worksheet id, or the memo figure's own key: one item however many sections state it
	Text     string            `json:"text"` // the item as the block lists it
	Check    bool              `json:"check"`
	Mismatch bool              `json:"mismatch"` // a unit's cited words are not its name: listed first
	Section  string            `json:"section"`
	Gate     map[string]string `json:"-"` // the findings a ⚠ input rests on, gated (by why)
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
	ground traceGround // the cited quotes and values, parsed once
	dates  map[string]quoteDates
	sheets []analysis.Worksheet
	usable map[string]Finding
	fids   []string
}

// quoteDates are a quote's dates and where it writes each (analysis.DateSpans), found once.
type quoteDates struct {
	keys  []string
	spans [][]int
}

// snippet is the words of quote around [start, end): about 24 bytes each side, cut where a token
// ends (a space, a comma or a pipe), never inside one; in a table or CSV row, from the row's first
// cell, so the party it is about stays (r11 L4: "Veltris Medical B.V.,Turnover,Netherlands,48").
func snippet(quote string, start, end int) string {
	if quote == "" || start < 0 || end > len(quote) || start > end {
		return ""
	}
	cut := func(b byte) bool { return b == ' ' || b == ',' || b == '|' || b == '\n' || b == '\t' }
	lo, hi := max(0, start-24), min(len(quote), end+24)
	if row := strings.LastIndexByte(quote[:start], '\n') + 1; start-row <= 64 && isRow(quote[row:start]) {
		lo = row
	} else if i := strings.IndexFunc(quote[lo:start], func(r rune) bool { return r < utf8.RuneSelf && cut(byte(r)) }); lo > 0 && i >= 0 {
		lo += i + 1
	}
	if i := strings.LastIndexFunc(quote[end:hi], func(r rune) bool { return r < utf8.RuneSelf && cut(byte(r)) }); hi < len(quote) && i >= 0 {
		hi = end + i
	}
	for lo > 0 && !utf8.RuneStart(quote[lo]) {
		lo--
	}
	for hi < len(quote) && !utf8.RuneStart(quote[hi]) {
		hi++
	}
	head, tail := lo > 0, hi < len(quote)
	for lo < start && cut(quote[lo]) {
		lo++
	}
	for hi > end && cut(quote[hi-1]) {
		hi--
	}
	return cmpStr(head, "…", "") + quote[lo:hi] + cmpStr(tail, "…", "")
}

// isRow reports whether a line's text is cells: a pipe, or a comma with a word right after it (prose
// writes ", ", a figure "1,000").
func isRow(line string) bool {
	for i := range len(line) {
		if line[i] == '|' || line[i] == ',' && i+1 < len(line) && line[i+1] != ' ' && (line[i+1] < '0' || line[i+1] > '9') {
			return true
		}
	}
	return false
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

// input is one worksheet input as the block writes it: whether it rests on a reader's words or label
// (check), on words or a label that are not its unit (mismatch: its findings gated, why), and the
// findings an input read at a scale rests on (scaled).
type input struct {
	text            string
	check, mismatch bool
	gate            []string
	scaled          []string
}

// plainKind reports whether a reading is a count or a bare number with no unit words: the kind
// label alone says it is not an identifier (r11 L3: "8 = 10.1 − 2.1" from section numbers).
func plainKind(r analysis.Reading) bool {
	return (r.Kind == analysis.KindCount || r.Kind == analysis.KindNumber) && r.UnitFrom == ""
}

// operand is one worksheet input as the block writes it (input).
func (cs computeSet) operand(fn string, st analysis.SheetStep, depth int) input {
	if ref, val, isRef := strings.Cut(st.Value, " = "); isRef && refRe.MatchString(ref) {
		return cs.refOperand(fn, ref, val)
	}
	lit, unit := sheetOperand(st.Value)
	amount := lit
	if _, ss := analysis.Literal(lit); len(ss) == 1 && len(ss[0].Readings()) == 1 && unit != "date" {
		amount = analysis.Decimal(ss[0].Readings()[0])
	} else if d, ok := analysis.ParseDate(lit); ok && unit == "date" {
		amount = d.Key()
	}
	in := input{text: memoValue(amount, unit)}
	if unit == "date" {
		return input{text: cs.dateAt(in.text, amount)}
	}
	quoted := make([]analysis.Reading, len(cs.src))
	for i, c := range cs.src {
		quoted[i] = c.Reading
	}
	o := literalOrigin(fn, st.Value, cs.ground, quoted, cs.sheets)
	switch {
	case o.sheet != nil && depth < 2:
		inner := cs.expr(*o.sheet, depth+1)
		inner.text = in.text + " (" + inner.text + ")"
		return inner
	case o.days:
		in.text += " (the days between two cited dates)"
		in.check = true
	case o.read >= 0:
		c := cs.src[o.read]
		in.text += " " + c.at()
		if c.Scale != "" || c.UnitFrom != "" || plainKind(c.Reading) {
			in.text += readWords(c.Reading)
			in.check = true
		}
		if c.Scale != "" {
			in.scaled = []string{c.fid}
		}
		if c.Kind == analysis.KindDuration && unitWords(c.UnitFrom) != unitWords(unit) && !(unit == "days" && fn == "business_days") {
			in.text += ": the cited words “" + c.UnitFrom + "” are not its unit (" + unit + ")"
			in.check, in.mismatch, in.gate = true, true, cs.holding(c.Reading)
		}
	}
	return in
}

// refOperand is a worksheet input that is a cited finding's value (ref), as the figure reader read
// it: its amount is that reading's, so its scale and kind are the reader's labels (lab-r4 r11 H).
func (cs computeSet) refOperand(fn, ref, val string) input {
	fid, _, _ := strings.Cut(ref, ".")
	lit, unit := sheetOperand(val)
	var g *analysis.Reading
	f := cs.usable[fid]
	if j, err := strconv.Atoi(strings.TrimPrefix(ref[len(fid):], ".v")); err == nil && j >= 1 && j <= len(f.Values) {
		g = f.Values[j-1].Fig
	}
	if unit == "date" || g == nil || g.Rat() == nil {
		d, ok := analysis.ParseDate(lit)
		if unit == "date" && ok {
			return input{text: cs.dateAt(memoValue(d.Key(), "date"), d.Key())}
		}
		return input{text: lit + " [" + fid + "]"}
	}
	in := input{text: memoValue(analysis.Decimal(g.Rat()), unit)}
	if i := slices.IndexFunc(cs.src, func(c citedRead) bool {
		return c.fid == fid && !c.Unclear && c.Kind == g.Kind && c.Amount == g.Amount && c.Scale == g.Scale
	}); i >= 0 {
		in.text += " " + cs.src[i].at()
	} else {
		in.text += " [" + fid + "]"
	}
	if g.Scale != "" || g.UnitFrom != "" || plainKind(*g) {
		in.text += readWords(*g)
		in.check = true
	}
	if g.Scale != "" {
		in.scaled = []string{fid}
	}
	period := unitKind(unit) == analysis.KindDuration
	switch {
	case period && g.Kind != analysis.KindDuration: // a count used as a period: only the value's unit, never the reader, says it is days
		in.text += ": the figure reader read it as " + article(g.Kind) + ", not a period in " + unit
		in.check, in.mismatch, in.gate = true, true, []string{fid}
	case period && unitWords(g.UnitFrom) != unitWords(unit) && !(unit == "days" && fn == "business_days"):
		in.text += ": the cited words “" + g.UnitFrom + "” are not its unit (" + unit + ")"
		in.check, in.mismatch, in.gate = true, true, []string{fid}
	}
	return in
}

// dateAt is a date input with the finding and the words of the quote that state it, found by where
// the quote writes the date, however it writes it (r11 L4: "10/21/2026" for October 21, 2026).
func (cs computeSet) dateAt(text, key string) string {
	want, _ := analysis.ParseDate(key)
	for _, fid := range cs.fids {
		for _, c := range cs.usable[fid].Citations {
			if c.Method == "none" {
				continue
			}
			ds, ok := cs.dates[c.Quote]
			if !ok {
				ds.keys, ds.spans = analysis.DateSpans(c.Quote)
				cs.dates[c.Quote] = ds
			}
			ks, spans := ds.keys, ds.spans
			for i, k := range ks {
				if x, _ := analysis.ParseDate(k); x.Compatible(want) && x.Day != 0 {
					return text + " [" + fid + "] “" + snippet(c.Quote, spans[i][0], spans[i][1]) + "”"
				}
			}
		}
	}
	return text
}

// readWords is what a reading rests on of the reader's words: its scale's or its unit's, or its kind
// alone (a count or a number with no unit words).
func readWords(r analysis.Reading) string {
	switch {
	case r.Scale != "":
		return ", scaled by “" + r.ScaleFrom + "”"
	case r.Kind == analysis.KindDuration:
		if unitWords(r.UnitFrom) == unitWords(r.Unit) {
			return ""
		}
		return ", read in " + r.Unit + " from “" + r.UnitFrom + "”"
	case r.UnitFrom != "":
		return ", a multiple by “" + r.UnitFrom + "”"
	case plainKind(r):
		return ", read as " + article(r.Kind)
	}
	return ""
}

// expr is a worksheet as an expression over its inputs, with what its inputs rest on.
func (cs computeSet) expr(w analysis.Worksheet, depth int) input {
	fn := w.Steps[len(w.Steps)-1].Step
	var ops []string
	var out input
	for _, st := range w.Steps {
		if strings.HasPrefix(st.Step, "operand") {
			in := cs.operand(fn, st, depth)
			ops = append(ops, in.text)
			out.check, out.mismatch = out.check || in.check, out.mismatch || in.mismatch
			out.gate, out.scaled = append(out.gate, in.gate...), append(out.scaled, in.scaled...)
		}
	}
	neg := func(i int, o string) string { // a negative input: "45 − (−45)", never "45 − -45" (r11 L3)
		if !strings.HasPrefix(o, "-") {
			return o
		}
		amount, rest, _ := strings.Cut(o[1:], " ")
		return cmpStr(i == 0, "−"+amount, "(−"+amount+")") + cmpStr(rest != "", " "+rest, "")
	}
	join := map[string]string{"sum": " + ", "diff": " − ", "product": " × ", "ratio": " ÷ "}[fn]
	switch {
	case join != "":
		for i := range ops {
			ops[i] = neg(i, ops[i])
		}
		out.text = strings.Join(ops, join)
	case fn == "pct" && len(ops) == 2:
		out.text = ops[0] + " as a percentage of " + ops[1]
	case (fn == "date_add" || fn == "business_days") && len(ops) == 2 && !strings.HasSuffix(w.Unit, "days"):
		out.text = ops[0] + cmpStr(strings.HasPrefix(ops[1], "-"), " − "+strings.TrimPrefix(ops[1], "-"), " + "+ops[1]) + cmpStr(fn == "business_days" && !strings.Contains(ops[1], "business days"), " (business days)", "")
	case len(ops) == 2:
		out.text = map[string]string{"date_diff": "days", "business_days": "business days"}[fn] + " from " + ops[0] + " to " + ops[1]
	default:
		out.text = fn + "(" + strings.Join(ops, "; ") + ")"
	}
	if slices.ContainsFunc(w.Steps, func(st analysis.SheetStep) bool { return strings.HasPrefix(st.Step, "rolled") }) {
		out.text += ", rolled past a weekend or holiday"
	}
	return out
}

// sheet is a worksheet's item, for the memo figures it is listed for (memo: G8 traces them only with
// it) and the memo's figures at a scale that are its result (as). A memo figure written digit for
// digit whose inputs are read at a scale is the cited figures' only by the reader's scale of their
// words, as limitedScale's is (r11 H): ⚠, and the findings are gated.
func (cs computeSet) sheet(w analysis.Worksheet, memo []untraced, as []string) computedFig {
	in := cs.expr(w, 0)
	outScale := len(in.scaled) > 0 && slices.ContainsFunc(memo, func(u untraced) bool {
		return u.read.Clear() && u.read.Scale == "" && u.read.Rat() != nil && u.read.Kind != analysis.KindDate && u.read.Kind != analysis.KindOther
	})
	text := memoValue(w.Result, w.Unit) + " = " + in.text
	if len(as) > 0 {
		text += "; written in the memo as " + strings.Join(slices.Compact(slices.Sorted(slices.Values(as))), ", ") +
			cmpStr(len(in.scaled) == 0, ", at a scale the cited inputs write out: the same figure only by the figure reader's scale of the memo's words", "")
	}
	gate := map[string]string{}
	for _, fid := range in.gate {
		gate[fid] = limitedUnit
	}
	if outScale {
		text += ": the memo writes it digit for digit, and its inputs are the cited figures only by the figure reader's scale of their words; the findings are gated"
		for _, fid := range in.scaled {
			if gate[fid] == "" {
				gate[fid] = limitedScale
			}
		}
	}
	mismatch := in.mismatch || outScale
	if in.mismatch {
		text += "; the findings are gated"
	}
	return computedFig{Key: w.WID, Text: cmpStr(mismatch, "⚠ ", "") + text, Check: in.check || mismatch || len(as) > 0, Mismatch: mismatch, Gate: gate}
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
	s.WriteString("\n### Computed figures\n\nEvery figure below is stated in the memo but traces only through a calculation Go made (over cited values or typed inputs, each as the figure reader read it), or only by the figure reader's label; each input names the finding and the quoted words it comes from.\n")
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
