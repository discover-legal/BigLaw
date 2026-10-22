// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"regexp"
	"slices"
	"strings"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

var (
	numRe  = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)
	dateRe = regexp.MustCompile(`(?i)\b(?:\d{1,2}\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?(?:\s+\d{1,2},?)?\s+(?:19|20)\d\d\b|\b(?:19|20)\d\d-\d\d(?:-\d\d)?\b|\b\d{1,2}/\d{1,2}/(?:19|20)\d\d\b|(?:§\s*)?\b(?:19|20)\d\d\b`)
)

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
		out, err := analysis.Substitute(s, handlesOf(figs))
		if err != nil {
			return s
		}
		return out
	})
}

// lint is the deliverable's integrity check over one section (G5, G8): every [F-id] names a
// usable finding, every handle is known, and every figure traces to a cited finding's quotes or
// values, a handle, or a calc worksheet.
func lint(issueID string, s Section, usable map[string]Finding, sheets []string) []string {
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
		ground.WriteString(f.Value + "\n")
	}
	ground.WriteString(strings.Join(sheets, "\n"))
	body := fidRe.ReplaceAllString(substituteOutsideQuotes(s.Markdown, s.Handles), " ")
	body = statuteCite.ReplaceAllString(caseCite.ReplaceAllString(body, " "), " ") // an authority's numbers are G5's (flagged), not figures
	for _, n := range Untraced(body, ground.String(), false) {
		out = append(out, issueID+": the figure "+n+" is not a handle, a calc result or part of a cited quote (G8)")
	}
	return out
}
