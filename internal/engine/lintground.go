// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// What G8 traces a section's figures to (lint.go): the worksheets it may trace through, the cited
// ground, the handles Go wrote into the text, and the run's worksheets and handles.

// tracedSheets are the worksheets a section's figures may trace to (lab-r4 r6 M3, r7 H1, r8 H1):
// every operand a cited finding's value (its ref), a traced worksheet's result in the same unit, or a
// literal the cited quotes state as its unit says, by the figure reader's kind: an amount in a
// currency one it read clearly as money in it, a percentage one it read as a percentage, a plain
// number (no unit, or a unit that is no currency or period) one it read as a count or a bare number
// or multiple, a date one the cited quotes and values state, and a period one it read as a duration
// in that unit (calendar days for date_add, business days for business_days), or, in days, the
// difference of two dates they state. Never a number it read as other (a section, rule or docket
// number: "Section 30" is no 30-day period, "Section 12" no multiplier). A worksheet over a literal
// the record does not state so ("sum 9000000000 and 0", "9000000000 days", "2,000,000 USD" from a
// share count) computes nothing from the evidence and traces nothing.
func tracedSheets(sheets []analysis.Worksheet, ground string, cited map[string]bool, quoted []analysis.Reading) []analysis.Worksheet {
	g := groundOf(ground)
	var out []analysis.Worksheet
	for changed := true; changed; {
		changed = false
		for _, w := range sheets {
			if slices.ContainsFunc(out, func(x analysis.Worksheet) bool { return x.WID == w.WID }) {
				continue
			}
			fn := ""
			if len(w.Steps) > 0 {
				fn = w.Steps[len(w.Steps)-1].Step
			}
			ok := true
			for _, st := range w.Steps {
				if !strings.HasPrefix(st.Step, "operand") {
					continue
				}
				ref, _, isRef := strings.Cut(st.Value, " = ")
				if isRef && refRe.MatchString(ref) {
					ok = ok && cited[strings.SplitN(ref, ".", 2)[0]]
					continue
				}
				ok = ok && literalTraced(fn, st.Value, g, quoted, out)
			}
			if ok {
				out, changed = append(out, w), true
			}
		}
	}
	return out
}

// ground is what a section's figures trace to, parsed once: its text, its dates (parsed), its
// numbers and the days between its full dates (lab-r4 r11 M).
type traceGround struct {
	text  string
	dates []analysis.Date
	nums  []analysis.Span
	spans []int64
	cited []analysis.Span // the numbers of what the section cites alone (its findings' quotes and values), never a worksheet's
}

// groundOf is text parsed as a ground, all of it cited.
func groundOf(text string) traceGround {
	g := traceGround{}.plus(text)
	g.cited = g.nums
	return g
}

// plus is g with text added: only text is parsed, and the day spans between its dates and g's added.
func (g traceGround) plus(text string) traceGround {
	if text == "" {
		return g
	}
	ks, ns := figures(text, true)
	var ds []analysis.Date
	for _, k := range ks {
		d, _ := analysis.ParseDate(k)
		ds = append(ds, d)
	}
	spans := slices.Clone(g.spans)
	for i, y := range ds { // from the date written first to the one written after it
		for _, x := range slices.Concat(g.dates, ds[:i]) {
			if x.Day != 0 && y.Day != 0 {
				spans = append(spans, analysis.DaysBetween(x, y))
			}
		}
	}
	return traceGround{g.text + text, slices.Concat(g.dates, ds), slices.Concat(g.nums, ns), spans, g.cited}
}

// literalTraced reports whether a worksheet's literal operand (its step's value: the literal as
// typed, then its unit) of a calc fn is stated by the evidence as its unit says it is (tracedSheets).
func literalTraced(fn, step string, g traceGround, quoted []analysis.Reading, traced []analysis.Worksheet) bool {
	o := literalOrigin(fn, step, g, quoted, traced)
	return o.ok
}

// origin is what states a worksheet's literal operand: a cited reading (its index in quoted), the
// days between two cited dates, a traced worksheet's result, or (a date) the cited text.
type origin struct {
	ok    bool
	read  int // index in quoted, -1 when none
	days  bool
	sheet *analysis.Worksheet
}

// literalOrigin is literalTraced with what states the literal (lab-r4 r9 H1: a duration or a number
// reading counts only with the words beside it the reader cites, checked by ReadLabels).
func literalOrigin(fn, step string, g traceGround, quoted []analysis.Reading, traced []analysis.Worksheet) origin {
	none := origin{read: -1}
	lit, unit := sheetOperand(step)
	if unit == "date" {
		return origin{ok: coveredIn(lit, g), read: -1}
	}
	_, ss := analysis.Literal(lit)
	if len(ss) != 1 || len(ss[0].Readings()) != 1 {
		return none
	}
	n := ss[0].Readings()[0]
	if i := slices.IndexFunc(traced, func(w analysis.Worksheet) bool {
		x, ok := new(big.Rat).SetString(w.Result)
		return ok && w.Unit == unit && x.Cmp(n) == 0
	}); i >= 0 {
		return origin{ok: true, read: -1, sheet: &traced[i]}
	}
	kind := unitKind(unit)
	if kind == analysis.KindDuration { // a period counted back ("-7 days") is the period the record states ("seven (7) days before")
		n = new(big.Rat).Abs(n)
	}
	i := slices.IndexFunc(quoted, func(k analysis.Reading) bool {
		x := k.Rat()
		if x != nil && kind == analysis.KindDuration {
			x.Abs(x)
		}
		if k.Unclear || x == nil || x.Cmp(n) != 0 {
			return false
		}
		switch kind {
		case analysis.KindMoney:
			return k.Kind == analysis.KindMoney && k.Currency == unit
		case analysis.KindDuration:
			want := unit
			if unit == "days" && fn == "business_days" {
				want = "business days"
			}
			return k.Kind == analysis.KindDuration && k.Unit == want && k.UnitFrom != ""
		case analysis.KindPercent:
			return k.Kind == analysis.KindPercent
		}
		return k.Kind == analysis.KindCount || k.Kind == analysis.KindNumber && k.UnitFrom != "" // a plain number: never an identifier (other)
	})
	switch {
	case i >= 0: // a cited period first: the block names its words
		return origin{ok: true, read: i}
	case kind == analysis.KindDuration && unit == "days" && fn != "business_days" && n.IsInt() && slices.ContainsFunc(g.spans, func(d int64) bool { return n.Num().IsInt64() && n.Num().Int64() == d }):
		return origin{ok: true, read: -1, days: true}
	}
	return none
}

// sheetOperand splits a worksheet's literal operand step (analysis.Calc: the literal as typed, then
// its unit) into the literal and the unit: "date", a period's ("days", "weeks", "months", "years"),
// "%", a currency code, or "" for a plain number (a unit that is none of those: "shares").
func sheetOperand(step string) (lit, unit string) {
	for _, u := range []string{" date", " business days", " days", " weeks", " months", " years", " %"} {
		if strings.HasSuffix(step, u) {
			return strings.TrimSuffix(step, u), u[1:]
		}
	}
	if i := strings.LastIndexByte(step, ' '); i >= 0 && analysis.IsCurrency(step[i+1:]) {
		return step[:i], step[i+1:]
	}
	if _, ss := analysis.Literal(step); len(ss) == 1 {
		return step[:ss[0].End], ""
	}
	return step, ""
}

// unitKind is the reading kind a worksheet unit is: money for a currency code, a percentage for
// "%", a duration for a period's unit, a date, or a count (a plain number).
func unitKind(unit string) string {
	switch {
	case unit == "%":
		return analysis.KindPercent
	case slices.Contains([]string{"days", "business days", "weeks", "months", "years"}, unit):
		return analysis.KindDuration
	case unit == "date":
		return analysis.KindDate
	case analysis.IsCurrency(unit):
		return analysis.KindMoney
	}
	return analysis.KindCount
}

// sheetKind reports whether a worksheet's result is of the memo reading's kind: money in the
// reading's currency, a percentage, or a plain number (a count or a bare number) (r7 H1: a sum of
// share counts never traces a "$2,000,000"; a count of days traces no figure by its amount).
func sheetKind(w analysis.Worksheet, r analysis.Reading) bool {
	switch k := unitKind(w.Unit); k {
	case analysis.KindCount:
		return r.Kind == analysis.KindCount || r.Kind == analysis.KindNumber
	case analysis.KindMoney:
		return r.Kind == k && w.Unit == r.Currency
	default:
		return r.Kind == k
	}
}

var refRe = regexp.MustCompile(`^F\d+\.v\d+$`)

// rawDate reports whether a handle's value is a deadline's raw date (rolledDates).
func rawDate(value string, rolled map[string]analysis.Worksheet) bool {
	ds, _ := figures(value, true)
	_, raw := rolled[strings.Join(ds, "")]
	return raw
}

// sheetAmount is a worksheet's numeric result (nil for a date or a count of days).
func sheetAmount(w analysis.Worksheet) *big.Rat {
	if k := unitKind(w.Unit); k == analysis.KindDate || k == analysis.KindDuration {
		return nil
	}
	r, ok := new(big.Rat).SetString(w.Result)
	if !ok {
		return nil
	}
	return r
}

// region is where Go wrote a handle's value into a section's text, and whether that value traces
// (ok: a cited finding's value, or a traced worksheet's result; lint sets it).
type region struct {
	start, end int
	fig        Fig
	ok         bool
}

var handleTok = regexp.MustCompile(`\{([A-Za-z]+|W[0-9a-f]{10})\}`)

// memoBody is a section as G8 reads it: its handles substituted outside quotations (and where each
// value went), its citations and authorities blanked byte for byte (offsets hold, and an
// authority's numbers are G5's, flagged, not figures).
func memoBody(s Section) (string, []region) {
	var b strings.Builder
	var regs []region
	sub := func(x string) {
		last := 0
		for _, m := range handleTok.FindAllStringIndex(x, -1) {
			i := slices.IndexFunc(s.Handles, func(f Fig) bool { return strings.EqualFold(f.Handle, x[m[0]:m[1]]) })
			if i < 0 {
				continue // an unknown handle stays as written (the lint names it)
			}
			b.WriteString(x[last:m[0]])
			regs = append(regs, region{b.Len(), b.Len() + len(s.Handles[i].Value), s.Handles[i], false})
			b.WriteString(s.Handles[i].Value)
			last = m[1]
		}
		b.WriteString(x[last:])
	}
	last := 0
	for _, loc := range quotedRe.FindAllStringIndex(s.Markdown, -1) {
		sub(s.Markdown[last:loc[0]])
		b.WriteString(s.Markdown[loc[0]:loc[1]])
		last = loc[1]
	}
	sub(s.Markdown[last:])
	return blank(statuteCite, blank(caseCite, blank(fidRe, b.String()))), regs
}

func blank(re *regexp.Regexp, s string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
}

func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// memoSpans are the numbers of a section's body G8 reads (each paragraph's, its locators masked,
// single digits included: "$3 million"), those Go did not write for a handle: the figures the
// drafter wrote, which the figure reader reads.
func memoSpans(body string, regs []region) []analysis.Span {
	var out []analysis.Span
	off := 0
	for _, para := range strings.Split(body, "\n\n") {
		_, ns := figures(analysis.MaskLocators(para), true)
		for _, n := range ns {
			n.Start, n.End = n.Start+off, n.End+off
			n.Text = body[n.Start:n.End]
			if !slices.ContainsFunc(regs, func(r region) bool { return r.start <= n.Start && n.Start < r.end }) {
				out = append(out, n)
			}
		}
		off += len(para) + 2
	}
	return out
}

// rolledDates maps each raw date a date_add worksheet rolled past a weekend or holiday to that
// worksheet (bench r3, gate A: a drafter computed the answer date with no holidays, got the raw
// November 11 (Veterans Day), and G8 traced it to that worksheet and to the rolled one's raw-date
// step, so the memo stated it as the deadline). A raw date and any worksheet result equal to it
// trace only beside the rolled deadline; the first worksheet to roll a date names it.
func rolledDates(sheets []analysis.Worksheet) map[string]analysis.Worksheet {
	out := map[string]analysis.Worksheet{}
	for _, w := range sheets {
		for _, st := range w.Steps {
			if f := strings.Fields(st.Value); st.Step == "raw date" && len(f) > 0 {
				if _, ok := out[f[0]]; !ok {
					out[f[0]] = w
				}
			}
		}
	}
	return out
}

// hint says how to fix one untraced figure n (a decimal, or a date key): the handle that states
// it, the finding to cite, or the calculation to run; else to remove it.
func hint(n string, hs []Fig, usable map[string]Finding, cited map[string]bool) string {
	states := func(text string) bool {
		ds, ns := figures(text, true)
		d, isDate := analysis.ParseDate(n)
		return !isDate && slices.ContainsFunc(ns, func(f analysis.Span) bool { return decimalOf(f) == n }) || isDate && strings.Contains(n, "-") && slices.ContainsFunc(ds, func(k string) bool { x, _ := analysis.ParseDate(k); return x == d })
	}
	for _, h := range hs { // a worksheet's value is traced already, so this is a finding's handle
		if h.FID != "" && states(h.Value) {
			return "write its handle " + h.Handle + " and cite [" + h.FID + "], the verified finding that states it"
		}
	}
	fids := make([]string, 0, len(usable))
	for fid := range usable {
		fids = append(fids, fid)
	}
	slices.SortFunc(fids, func(a, b string) int { return cmp.Or(len(a)-len(b), strings.Compare(a, b)) }) // F2 before F10
	for _, fid := range fids {
		f := usable[fid]
		var t strings.Builder
		for _, c := range f.Citations {
			t.WriteString(c.Quote + "\n")
		}
		if !cited[fid] && states(t.String()) {
			return "it is stated by [" + fid + "]: cite [" + fid + "] in the sentence that states it"
		}
	}
	if strings.Contains(n, "-") {
		return "no cited quote states this date: if it is computed (a deadline), compute it with numbers op calc fn date_add from the trigger date's value ref, with the court's holidays set to roll it, and write the {W…} handle it returns; otherwise remove it"
	}
	return "no cited finding, handle or worksheet states it: if it is computed (a total, a difference, a percentage of a figure), compute it with numbers op calc over the findings' value refs (e.g. fn product of F3.v1 and a literal 35 with unit %) and write the {W…} handle it returns; otherwise remove it"
}

// lintRounds is how many finish calls have already reported lint problems: past maxLintRounds the
// lint discloses instead of refusing, so redrafting for it can never consume the budget.
func (e *Engine) lintRounds(ctx context.Context, taskID string) (int, error) {
	var n int
	err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM task_steps WHERE task_id = $1 AND tool = 'finish' AND status = 'committed'
		AND jsonb_typeof(output->'lint') = 'array' AND output->'lint' <> '[]'::jsonb`, taskID).Scan(&n)
	return n, err
}

const maxLintRounds = 2

// worksheets is the run's numbers{op:"calc"} worksheets, in the order they were made.
func (e *Engine) worksheets(ctx context.Context, taskID string) ([]analysis.Worksheet, error) {
	rs, err := e.rows(ctx, taskID, kSheet)
	out := make([]analysis.Worksheet, 0, len(rs))
	for _, r := range rs {
		var w analysis.Worksheet
		_ = json.Unmarshal(r.Data, &w)
		w.WID = r.Key
		out = append(out, w)
	}
	return out, err
}

// handleSet is the run's figure handles: each verified figure's codename (kept in the ledger once
// given, so it never changes meaning, also in a summary a later drafter reads) and each
// worksheet's {W…}, with its label and exact value. Called within a step (its writes commit with it).
func (e *Engine) handleSet(ctx context.Context, taskID string, use []Finding) ([]analysis.Handle, error) {
	kept, err := load[Fig](ctx, e, taskID, kHandle)
	ws, werr := e.worksheets(ctx, taskID)
	if err = cmpErr(err, werr); err != nil {
		return nil, err
	}
	an := make([]analysis.Finding, 0, len(use))
	for _, f := range use {
		an = append(an, f.Finding)
	}
	hs := analysis.Handles(an, nil, 0, handlesOf(kept)...)
	for _, h := range hs {
		if !slices.ContainsFunc(kept, func(k Fig) bool { return k.Handle == h.Handle }) {
			if err := e.write(ctx, taskID, kHandle, h.Handle, Fig{h.Handle, h.FID, h.Value}, false); err != nil {
				return nil, err
			}
		}
	}
	for _, w := range ws {
		hs = append(hs, analysis.Handle{Handle: "{" + w.WID + "}", Ref: w.WID, Label: sheetLabel(w), Value: sheetValue(w.Result, w.Unit)})
	}
	return hs, nil
}

// sheetLabel says what a worksheet computes from what: "date_add: 1 F1.v1 = … date; 2 21 days".
func sheetLabel(w analysis.Worksheet) string {
	if len(w.Steps) == 0 {
		return ""
	}
	var parts []string
	for _, st := range w.Steps[:len(w.Steps)-1] {
		parts = append(parts, strings.TrimPrefix(st.Step+" ", "operand ")+st.Value)
	}
	return w.Steps[len(w.Steps)-1].Step + ": " + strings.Join(parts, "; ")
}

// sheetValue is a worksheet result as the memo states it: a date in words, money with its sign
// and thousands separators, a percentage, or a count with its unit.
func sheetValue(result, unit string) string {
	if t, err := time.Parse(time.DateOnly, result); err == nil && unit == "date" {
		return t.Format("January 2, 2006")
	}
	digits, neg := strings.CutPrefix(result, "-")
	whole, frac, _ := strings.Cut(digits, ".")
	for k := len(whole) - 3; k > 0; k -= 3 {
		whole = whole[:k] + "," + whole[k:]
	}
	n := whole + map[bool]string{true: "." + frac}[frac != ""]
	switch unit {
	case "USD":
		n = "$" + n
	case "%":
		n += "%"
	case "":
	default:
		n += " " + unit
	}
	return map[bool]string{true: "-"}[neg] + n
}
