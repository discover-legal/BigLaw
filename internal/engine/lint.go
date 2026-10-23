// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

var (
	// refCite is a rule, section, paragraph or exhibit number ("Rule 6(a)(1)(C)", "Section 4.3",
	// "Fed. R. Civ. P. 7.1", "§ 9.2"): a locator, not a figure (G8 traces quantities and dates). At
	// most four digits a part, so no amount hides behind "Rule 214340". refList continues it over a
	// list or range (maskRefs).
	refCite = regexp.MustCompile(`(?i)(\b(?:rules?|sections?|secs?\.|articles?|arts?\.|paragraphs?|paras?\.|clauses?|schedules?|exhibits?)\s+|§+\s*|¶+\s*|\bR\.\s*(?:Civ|Crim|App|Evid|Bankr)\.\s*P\.\s*)` + locator)
	// refList is one more locator of a list ("Sections 7.3 and 9.1", "paragraphs 12, 14 and 17") or
	// the end of a range ("§§ 4.2–4.5", "Section 4.2-4.5"); refRange is the range form alone.
	refList  = regexp.MustCompile(`^(?:\s*(?:,|;|&|\band\b|\bor\b)\s*(?:and\s+|or\s+)?|\s*[–—-]\s*)` + locator)
	refRange = regexp.MustCompile(`^\s*[–—-]\s*` + locator)
	numRe    = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)
	dateRe   = regexp.MustCompile(`(?i)\b(?:\d{1,2}\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?(?:\s+\d{1,2},?)?\s+(?:19|20)\d\d\b|\b(?:19|20)\d\d-\d\d(?:-\d\d)?\b|\b\d{1,2}/\d{1,2}/(?:19|20)\d\d\b|(?:§\s*)?\b(?:19|20)\d\d\b`)
)

// locator is one rule, section or clause number: "7.3", "206(4)", "3(b)".
const locator = `\d{1,4}(?:\.\d{1,4})*(?:(?:\([0-9A-Za-z]{1,4}\))+|\b)`

// maskRefs blanks the locators in text (refCite), each with the rest of its list or range: a plural
// head ("Sections", "§§", "clauses") continues over ", 9.1", "and 3(b)", "or 17" and "–4.5"; a
// singular one only over a range. A continuation is never a figure's head: "2" of "2,000" or of
// "2.5%" (a separator then a digit) ends the list, so "Sections 7.3 and 2,000 shares" keeps 2,000.
func maskRefs(text string) string {
	b := []byte(text)
	for _, loc := range refCite.FindAllStringSubmatchIndex(text, -1) {
		head := strings.ToLower(strings.TrimSpace(text[loc[2]:loc[3]]))
		plural := strings.HasSuffix(head, "s") || strings.HasSuffix(head, "s.") || strings.HasPrefix(head, "§§") || strings.HasPrefix(head, "¶¶")
		end := loc[1]
		for {
			re := map[bool]*regexp.Regexp{true: refList, false: refRange}[plural]
			m := re.FindStringIndex(text[end:])
			if m == nil || figureAfter(text, end+m[1]) {
				break
			}
			end += m[1]
		}
		if figureAfter(text, loc[1]) { // "Section 2,000": not a locator at all
			continue
		}
		for i := loc[0]; i < end; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// figureAfter reports whether a number ending at i goes on as a figure: a separator then a digit.
func figureAfter(text string, i int) bool {
	return i+1 < len(text) && (text[i] == ',' || text[i] == '.') && text[i+1] >= '0' && text[i+1] <= '9'
}

// dates finds the dates in text with their spans (month names, ISO, m/d/y and bare years; a
// statute section such as "§ 2462" is not a year).
func dates(text string) (keys []string, spans [][]int) {
	for _, loc := range dateRe.FindAllStringIndex(text, -1) {
		raw := text[loc[0]:loc[1]]
		if strings.HasPrefix(raw, "§") {
			continue
		}
		if d, ok := analysis.ParseDate(raw); ok {
			keys, spans = append(keys, d.Key()), append(spans, loc)
		}
	}
	return keys, spans
}

// figures lists the dates (as date keys) and numbers (exact decimals) text states. Dates come
// first and their digits are not read again as numbers. Single-digit integers ("Rule 6(a)(1)",
// "paragraph 3") count only when all is set.
func figures(text string, all bool) (ds, nums []string) {
	ds, spans := dates(text)
	masked := []byte(text)
	for _, loc := range spans {
		for i := loc[0]; i < loc[1]; i++ {
			masked[i] = ' '
		}
	}
	for _, m := range numRe.FindAllString(string(masked), -1) {
		m = strings.TrimRight(m, ",.")
		if !all && len(m) == 1 {
			continue
		}
		if r, ok := analysis.Number(m); ok {
			nums = append(nums, analysis.Decimal(r))
		}
	}
	return ds, nums
}

// Untraced lists what text states that ground does not: dates no ground date matches at their
// common precision, and numbers no ground number equals. derived also accepts a sum or difference
// of two ground numbers, or a sum of three (a total or delta of stated figures, not a new one).
func Untraced(text, ground string, derived bool) []string {
	gd, gn := figures(ground, true)
	td, tn := figures(text, false)
	var out []string
	for _, k := range td {
		d, _ := analysis.ParseDate(k)
		if !slices.ContainsFunc(gd, func(g string) bool { x, _ := analysis.ParseDate(g); return x.Compatible(d) }) {
			out = append(out, k)
		}
	}
	vals := make([]float64, 0, len(gn))
	for _, g := range gn {
		if r, ok := analysis.Number(g); ok && len(vals) < 60 {
			f, _ := r.Float64()
			vals = append(vals, f)
		}
	}
	for _, n := range tn {
		if slices.Contains(gn, n) {
			continue
		}
		r, _ := analysis.Number(n)
		f, _ := r.Float64()
		if !derived || !derivable(f, vals) {
			out = append(out, n)
		}
	}
	return out
}

func derivable(want float64, vals []float64) bool {
	eq := func(a float64) bool { return a-want < 1e-6 && want-a < 1e-6 }
	for i := range vals {
		for j := i + 1; j < len(vals); j++ {
			if eq(vals[i]+vals[j]) || eq(vals[i]-vals[j]) || eq(vals[j]-vals[i]) {
				return true
			}
			for k := j + 1; k < len(vals); k++ {
				if eq(vals[i] + vals[j] + vals[k]) {
					return true
				}
			}
		}
	}
	return false
}

// covered reports whether ground states every figure in value (at least one).
func covered(value, ground string) bool {
	vd, vn := figures(value, true)
	gd, gn := figures(ground, true)
	if len(vd)+len(vn) == 0 {
		return false
	}
	for _, d := range vd {
		x, _ := analysis.ParseDate(d)
		if !slices.ContainsFunc(gd, func(g string) bool { y, _ := analysis.ParseDate(g); return y.Compatible(x) }) {
			return false
		}
	}
	for _, n := range vn {
		if !slices.Contains(gn, n) {
			return false
		}
	}
	return true
}

var quotedRe = regexp.MustCompile(`“[^”]*”|"[^"\n]*"`)

// outsideQuotes applies f to the parts of text outside quotation marks: a quoted document passage
// is the source's words and is never rewritten (a {Codename} inside one stays as written).
func outsideQuotes(text string, f func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range quotedRe.FindAllStringIndex(text, -1) {
		b.WriteString(f(text[last:loc[0]]))
		b.WriteString(text[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(f(text[last:]))
	return b.String()
}

func handlesOf(figs []Fig) []analysis.Handle {
	hs := make([]analysis.Handle, len(figs))
	for i, f := range figs {
		hs[i] = analysis.Handle{Handle: f.Handle, FID: f.FID, Value: f.Value}
	}
	return hs
}

// substituteOutsideQuotes replaces the drafter's handles with their exact values outside quotes,
// from the handle set the drafter was given; an unknown handle stays as written (the lint names it).
func substituteOutsideQuotes(text string, figs []Fig) string {
	return outsideQuotes(text, func(s string) string {
		out, _ := analysis.Substitute(s, handlesOf(figs)) // an unknown handle stays as written; the rest are substituted
		return out
	})
}

// lint is the deliverable's integrity check over one section (G5, G8): every [F-id] names a
// usable finding, every handle is known, and every figure traces to a cited finding's quotes or
// values, its handle, or a calc worksheet (its result or its inputs). A rule or section number is
// a locator, not a figure. Each untraced figure says how to fix it (hint).
func lint(issueID string, s Section, usable map[string]Finding, sheets []analysis.Worksheet) []string {
	var out []string
	cited := map[string]bool{}
	for _, m := range fidRe.FindAllStringSubmatch(s.Markdown, -1) {
		if _, ok := usable[m[1]]; !ok && !cited[m[1]] {
			out = append(out, issueID+": ["+m[1]+"] is not a verified finding of this task")
		}
		cited[m[1]] = true
	}
	unknown := false
	_ = outsideQuotes(s.Markdown, func(x string) string {
		if _, err := analysis.Substitute(x, handlesOf(s.Handles)); err != nil && !unknown {
			unknown = true
			out = append(out, issueID+": "+err.Error())
		}
		return x
	})
	for _, q := range quotedRe.FindAllString(s.Markdown, -1) { // bench: "administered by {Heron} in {Indigo}" in a verbatim quote
		if i := slices.IndexFunc(s.Handles, func(f Fig) bool { return strings.Contains(q, f.Handle) }); i >= 0 {
			out = append(out, issueID+": "+s.Handles[i].Handle+" is inside a quotation, where Go never substitutes it: quote the document's own words there, or move the handle outside the quotation marks")
			break
		}
	}
	var ground strings.Builder
	for fid := range cited {
		f := usable[fid]
		for _, c := range f.Citations {
			ground.WriteString(c.Quote + "\n")
		}
		for _, v := range f.Values {
			ground.WriteString(v.Value + "\n")
		}
	}
	for _, f := range s.Handles {
		if f.FID == "" || cited[f.FID] { // a worksheet's, or a cited finding's
			ground.WriteString(f.Value + "\n")
		}
	}
	for _, w := range sheets {
		ground.WriteString(w.Result + "\n")
		for _, st := range w.Steps {
			ground.WriteString(st.Value + "\n")
		}
	}
	body := fidRe.ReplaceAllString(substituteOutsideQuotes(s.Markdown, s.Handles), " ")
	body = statuteCite.ReplaceAllString(caseCite.ReplaceAllString(body, " "), " ") // an authority's numbers are G5's (flagged), not figures
	for _, n := range Untraced(maskRefs(body), ground.String(), false) {
		out = append(out, issueID+": the figure "+n+" is not traced (G8): "+hint(n, s.Handles, usable, cited))
	}
	return out
}

// hint says how to fix one untraced figure n (a decimal, or a date key): the handle that states
// it, the finding to cite, or the calculation to run; else to remove it.
func hint(n string, hs []Fig, usable map[string]Finding, cited map[string]bool) string {
	states := func(text string) bool {
		ds, ns := figures(text, true)
		d, isDate := analysis.ParseDate(n)
		return !isDate && slices.Contains(ns, n) || isDate && strings.Contains(n, "-") && slices.ContainsFunc(ds, func(k string) bool { x, _ := analysis.ParseDate(k); return x == d })
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
