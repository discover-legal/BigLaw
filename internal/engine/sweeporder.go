// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The sweep's reading order and its skips (bench r3, SEC referral: 169 planned visits, the sweep's
// share spent on the first 60 in document order, so the statistical exhibit that held two of the
// matter's planted discrepancies was never read). Visits are read most relevant first, so the money
// reaches the pages the plan's issues need before it runs out, and a page that would only repeat
// another, or holds no text to extract, is not read at all; both are reported, never skipped silently.

// terms are the words of text a lexical score counts: lower-cased letters and digits, three or more
// long, not stop words, with a plural or verb ending folded ("allocations", "allocated" → "allocat").
func terms(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) < 3 || sweepStop[w] {
			continue
		}
		for _, suf := range []string{"ions", "ion", "ing", "ies", "ed", "es", "s"} {
			if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
				w = strings.TrimSuffix(w, suf)
				break
			}
		}
		out = append(out, w)
	}
	return out
}

var sweepStop = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the and for that with this was are its had has not from into were their about after before during each per
		been have will what which who whom whose when where why how does did any all such other than then there these those them they
		our your you his her him she its may must shall should would could can under over upon also only both more most some same very`) {
		m[w] = true
	}
	return m
}()

// bm25 scores each text for query (Okapi BM25, k1 1.2, b 0.75) over texts as the corpus.
func bm25(texts [][]string, query []string) []float64 {
	n, df, total := float64(len(texts)), map[string]int{}, 0
	for _, t := range texts {
		total += len(t)
		seen := map[string]bool{}
		for _, w := range t {
			if !seen[w] {
				seen[w], df[w] = true, df[w]+1
			}
		}
	}
	avg := max(1, float64(total)/max(1, n))
	q := slices.Compact(slices.Sorted(slices.Values(query)))
	out := make([]float64, len(texts))
	for i, t := range texts {
		tf := map[string]int{}
		for _, w := range t {
			tf[w]++
		}
		for _, w := range q {
			if f := float64(tf[w]); f > 0 {
				idf := math.Log(1 + (n-float64(df[w])+0.5)/(float64(df[w])+0.5))
				out[i] += idf * f * 2.2 / (f + 1.2*(0.25+0.75*float64(len(t))/avg))
			}
		}
	}
	return out
}

// order is the sweep's reading order over visits: each plan issue's visits ranked by the BM25 score
// of the issue's question against the visit's text, taken in turn across the issues (every issue's
// best page before any issue's second best), then the visits no issue's words reach, in document
// order. It returns indexes into visits; the same visits and questions give the same order.
func order(visits []Visit, questions []string) []int {
	texts := make([][]string, len(visits))
	for i, v := range visits {
		texts[i] = terms(v.text)
	}
	var ranked [][]int
	for _, q := range questions {
		sc := bm25(texts, terms(q))
		var r []int
		for i, x := range sc {
			if x > 0 {
				r = append(r, i)
			}
		}
		slices.SortStableFunc(r, func(a, b int) int { return cmp.Compare(sc[b], sc[a]) })
		ranked = append(ranked, r)
	}
	taken, out := make([]bool, len(visits)), make([]int, 0, len(visits))
	for k := 0; ; k++ {
		more := false
		for _, r := range ranked {
			if k < len(r) {
				more = true
				if i := r[k]; !taken[i] {
					taken[i], out = true, append(out, i)
				}
			}
		}
		if !more {
			break
		}
	}
	for i := range visits {
		if !taken[i] {
			out = append(out, i)
		}
	}
	return out
}

// tocEntry is a table-of-contents line: a title, then dot leaders, a tab or a run of spaces, then a
// page number; tocHeading is a table of contents' own heading; cells split a table row.
var (
	tocEntry   = regexp.MustCompile(`^(.*\pL.*?)\s*(\.{3,}|…+|\t| {3,})\s*(\d{1,4})\s*$`)
	tocHeading = regexp.MustCompile(`(?i)^\s*(?:table of contents|contents|index)\s*$`)
	cells      = regexp.MustCompile(`\t| {3,}`)
)

// tableRow reports whether a line is a row of a data table: a |-table's, or one of three or more
// tab- or space-aligned cells, at least two of them figures. A table is evidence, never boilerplate.
func tableRow(l string) bool {
	if strings.Count(l, "|") >= 2 {
		return true
	}
	cs, num := cells.Split(strings.TrimSpace(l), -1), 0
	for _, c := range cs {
		num += btoi(strings.ContainsFunc(c, unicode.IsDigit))
	}
	return len(cs) >= 3 && num >= 2
}

// tocTitleFigure reports whether a table of contents' entry title holds what a title never does:
// a date, a number of a hundred or more, a grouped or decimal number, or one written beside a
// currency symbol or a percent sign, as written (a TOC title names a heading, not a quantity).
func tocTitleFigure(title string) bool {
	ds, fs := analysis.Literal(title)
	return len(ds) > 0 || slices.ContainsFunc(fs, func(f analysis.Span) bool {
		before, after := strings.TrimRight(title[:f.Start], " "), strings.TrimLeft(title[f.End:], " ")
		r, _ := utf8.DecodeLastRuneInString(before)
		return f.Grouped() || f.Readings()[0].Cmp(big.NewRat(100, 1)) >= 0 || unicode.Is(unicode.Sc, r) || strings.HasPrefix(after, "%")
	})
}

// toc reports whether a page reads as a table of contents and nothing else: no table row, no
// sentence (every other line a short heading), and either its own Contents (Table of Contents,
// Index) heading over entries, or five or more lines, 70% of them entries that are all dot-leadered
// (". . ." or "…"), with heading-like titles (no amount or figure) and page numbers (at most three
// digits) that never fall down the page. A tab or space-aligned list of figures (a two-column
// allocation, a chronology of years, a damages schedule, rising numbers) is read (lab-r3 r2
// verifier M4): when in doubt a page is read.
func toc(lines []string) bool {
	if slices.ContainsFunc(lines, tableRow) {
		return false
	}
	entries, leaders, rising, last := 0, 0, true, -1
	for _, l := range lines {
		if m := tocEntry.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[3])
			dots := m[2] != "\t" && strings.TrimSpace(m[2]) != ""
			entries, leaders = entries+1, leaders+btoi(dots && !tocTitleFigure(m[1]))
			rising, last = rising && n >= last && len(m[3]) <= 3, n
		} else if t := strings.TrimSpace(l); len(strings.Fields(t)) > 8 || endsWith(t, ".!?") && len(strings.Fields(t)) > 2 {
			return false // prose: a page that holds a sentence is read, whatever else it holds
		}
	}
	if len(lines) > 1 && tocHeading.MatchString(lines[0]) && 2*entries >= len(lines)-1 && entries > 0 {
		return true
	}
	return len(lines) >= 5 && float64(entries) >= 0.7*float64(len(lines)) && leaders == entries && rising
}

// skipWhy says why a visit is not worth a subagent, "" when it is, and which earlier visit it
// repeats (src, -1 when none): "duplicate" when its text, case and white space folded, is exactly an
// earlier read visit's of the same document (or, across documents, a page of at least 40 words), or
// holds exactly the same lines (table rows) in another order; "boilerplate" when it holds no letter
// or digit at all, or is a table of contents (toc). A page that differs from another in anything, a
// figure, a name or a "not", is read (lab-r3 verifier M1).
func skipWhy(v Visit, earlier []Visit) (why string, src int) {
	lines := strings.FieldsFunc(v.text, func(r rune) bool { return r == '\n' })
	if !strings.ContainsFunc(v.text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) || toc(lines) {
		return "boilerplate", -1
	}
	folded, words, rows := fold(v.text), len(strings.Fields(v.text)), sortedLines(lines)
	table := slices.ContainsFunc(lines, tableRow) // v's own: read once, not once per earlier visit
	for i, x := range earlier {
		if x.DocumentID != v.DocumentID && words < 40 {
			continue
		}
		if fold(x.text) == folded || table && slices.Equal(sortedLines(strings.FieldsFunc(x.text, func(r rune) bool { return r == '\n' })), rows) {
			return "duplicate", i
		}
	}
	return "", -1
}

// sortedLines are a page's non-empty lines folded and sorted (a table's rows as a set).
func sortedLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l = fold(l); l != "" {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
