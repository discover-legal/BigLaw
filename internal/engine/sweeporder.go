// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
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
		slices.SortStableFunc(r, func(a, b int) int { return cmpFloat(sc[b], sc[a]) })
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

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// tocLine is a table-of-contents line: a title, then dot leaders or a tab, then a page number.
var tocLine = regexp.MustCompile(`(?:\.{3,}|…|\t| {3,})\s*\d{1,4}\s*$`)

// skipWhy says why a visit is not worth a subagent, "" when it is: "duplicate" when its text (case
// and white space folded) is inside an earlier read visit's of the same document, or nearly the same
// words (90% of its five-word runs) with exactly the same figures, or, across documents, a whole
// page of at least 40 words another document already holds; "boilerplate" when it holds no word at
// all, or is a table of contents (five or more lines, 70% of them a title and a page number). A page
// that differs from another only by a figure is read: a figure is what the joins compare.
func skipWhy(v Visit, earlier []Visit) string {
	lines := strings.FieldsFunc(v.text, func(r rune) bool { return r == '\n' })
	toc := 0
	for _, l := range lines {
		toc += btoi(tocLine.MatchString(l))
	}
	if !strings.ContainsFunc(v.text, unicode.IsLetter) || len(lines) >= 5 && float64(toc) >= 0.7*float64(len(lines)) {
		return "boilerplate"
	}
	folded, words := fold(v.text), strings.Fields(strings.ToLower(v.text))
	ds, ns := figures(v.text, true)
	for _, x := range earlier {
		same := x.DocumentID == v.DocumentID
		if (same || len(words) >= 40) && strings.Contains(fold(x.text), folded) {
			return "duplicate"
		}
		if xd, xn := figures(x.text, true); same && slices.Equal(ds, xd) && slices.Equal(ns, xn) && shingleShare(words, strings.Fields(strings.ToLower(x.text))) >= 0.9 {
			return "duplicate"
		}
	}
	return ""
}

func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// shingleShare is the share of a's five-word runs that b also has (a's words if it has fewer than five).
func shingleShare(a, b []string) float64 {
	runs := func(w []string) map[string]bool {
		m := map[string]bool{}
		for i := 0; i+5 <= len(w); i++ {
			m[strings.Join(w[i:i+5], " ")] = true
		}
		if len(w) < 5 {
			m[strings.Join(w, " ")] = true
		}
		return m
	}
	ra, rb := runs(a), runs(b)
	hit := 0
	for r := range ra {
		hit += btoi(rb[r])
	}
	return float64(hit) / float64(max(1, len(ra)))
}
