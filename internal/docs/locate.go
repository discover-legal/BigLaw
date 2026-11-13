// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"strings"
	"unicode/utf8"
)

// The edit locator and the compare diff (track changes, README § Track changes). Both are pure
// functions over the stored text: a POST never opens the file.

// Edit statuses and methods (Outcome).
const (
	StatusLocated          = "located"
	StatusNotFound         = "not_found"
	StatusAmbiguous        = "ambiguous"
	StatusCrossesParagraph = "crosses_paragraph"
	StatusOverlaps         = "overlaps"
	StatusApplied          = "applied"
	StatusFailed           = "failed"
)

// editContext is how much text compare keeps on each side of a change to disambiguate it.
const editContext = 40

// locate finds e in text: exact occurrences of find scored by context, then tolerant ones
// (Verify's normalisation: quotes straightened, white space collapsed, case folded), then the span
// between before and after alone (how a pure insertion lands). A paragraph edit locates its
// paragraph as one whole line. More than one best candidate is ambiguous, none not found, a span
// holding a paragraph break crosses_paragraph; the offsets are bytes into text.
func locate(nt *Text, e Edit) Outcome {
	text := nt.raw
	if e.Kind == KindInsertParagraph || e.Kind == KindInsertParagraphBefore || e.Kind == KindDeleteParagraph {
		return locateLine(nt, e.Paragraph)
	}
	from, to := 0, len(text)
	if e.Paragraph != "" { // the edit names its paragraph: search inside it
		o := locateLine(nt, e.Paragraph)
		if o.Status != StatusLocated {
			return Outcome{Status: o.Status, Reason: "its paragraph: " + o.Reason}
		}
		from, to = o.Start, o.End
	}
	var o Outcome
	if e.Find != "" {
		if ps := occurrences(text, e.Find, from, to); len(ps) > 0 {
			o = pick(text, ps, len(e.Find), e.Before, e.After, "exact")
		} else {
			nf, _ := normalize(e.Find)
			lo, hi := nt.normPos(from), nt.normPos(to)
			if nf = strings.TrimSpace(nf); nf != "" {
				if ps := occurrences(nt.norm, nf, lo, hi); len(ps) > 0 {
					nb, _ := normalize(e.Before)
					na, _ := normalize(e.After)
					o = pick(nt.norm, ps, len(nf), nb, na, "tolerant")
					if o.Status == StatusLocated {
						o.Start, o.End = nt.back[o.Start], nt.back[o.End]
					}
				}
			}
		}
	}
	if o.Status == "" { // context only: the span between before and after
		o = locateBetween(nt, e, from, to)
	}
	if o.Status == StatusLocated && strings.Contains(text[o.Start:o.End], "\n") {
		return Outcome{Status: StatusCrossesParagraph, Method: o.Method, Start: o.Start, End: o.End, Reason: "the text spans a paragraph break: send one edit per paragraph, or a paragraph edit"}
	}
	return o
}

// occurrences are the positions of needle in text[from:to] (at most 1,000).
func occurrences(text, needle string, from, to int) []int {
	var ps []int
	for i := from; i < to && len(ps) < 1000; {
		j := strings.Index(text[i:to], needle)
		if j < 0 {
			break
		}
		ps = append(ps, i+j)
		i += j + 1
	}
	return ps
}

// pick chooses among the occurrences by context: the one whose surroundings share the most with
// before and after; one alone is taken as is; two with the same best score are ambiguous.
func pick(text string, ps []int, n int, before, after, method string) Outcome {
	if len(ps) == 1 {
		return Outcome{Status: StatusLocated, Method: method, Start: ps[0], End: ps[0] + n}
	}
	best, bestScore, ties := -1, -1, 0
	for _, p := range ps {
		score := commonSuffix(text[:p], before) + commonPrefix(text[p+n:], after)
		switch {
		case score > bestScore:
			best, bestScore, ties = p, score, 1
		case score == bestScore:
			ties++
		}
	}
	if ties > 1 || before == "" && after == "" {
		return Outcome{Status: StatusAmbiguous, Method: method, Reason: "the text occurs more than once: send before and after context that picks one"}
	}
	return Outcome{Status: StatusLocated, Method: method, Start: best, End: best + n}
}

// locateBetween is the context-only rung: before then after, with at most 80 bytes more than the
// find between them; the closest-sized gap wins.
func locateBetween(nt *Text, e Edit, from, to int) Outcome {
	nb, _ := normalize(e.Before)
	na, _ := normalize(e.After)
	nf, _ := normalize(e.Find)
	nf = strings.TrimSpace(nf)
	if strings.TrimSpace(nb) == "" && strings.TrimSpace(na) == "" {
		return Outcome{Status: StatusNotFound, Reason: "the text is not in the document (neither exactly nor with quotes and spacing normalised), and no context was given to place it"}
	}
	lo, hi := nt.normPos(from), nt.normPos(to)
	const slack = 80
	bestDiff, bs, be := -1, 0, 0
	switch {
	case strings.TrimSpace(nb) == "": // after alone: the point before it
		for _, p := range occurrences(nt.norm, na, lo, hi) {
			if bestDiff >= 0 {
				return Outcome{Status: StatusAmbiguous, Reason: "the context occurs more than once"}
			}
			bestDiff, bs, be = 0, p, p
		}
	case strings.TrimSpace(na) == "": // before alone: the point after it
		for _, p := range occurrences(nt.norm, nb, lo, hi) {
			if bestDiff >= 0 {
				return Outcome{Status: StatusAmbiguous, Reason: "the context occurs more than once"}
			}
			bestDiff, bs, be = 0, p+len(nb), p+len(nb)
		}
	default:
		for _, p := range occurrences(nt.norm, nb, lo, hi) {
			gap := p + len(nb)
			window := min(hi, gap+len(nf)+slack+len(na))
			if g := strings.Index(nt.norm[gap:window], na); g >= 0 {
				if diff := abs(g - len(nf)); bestDiff < 0 || diff < bestDiff {
					bestDiff, bs, be = diff, gap, gap+g
				} else if diff == bestDiff {
					return Outcome{Status: StatusAmbiguous, Reason: "the context occurs more than once"}
				}
			}
		}
	}
	if bestDiff < 0 {
		return Outcome{Status: StatusNotFound, Reason: "neither the text nor its context is in the document"}
	}
	return Outcome{Status: StatusLocated, Method: "context", Start: nt.back[bs], End: nt.back[be]}
}

// locateLine finds paragraph as one whole line of text (exact, then tolerant).
func locateLine(nt *Text, paragraph string) Outcome {
	p := strings.TrimSpace(paragraph)
	if p == "" {
		return Outcome{Status: StatusNotFound, Reason: "the paragraph is empty"}
	}
	raw := nt.raw
	found := func(text, needle string, back func(int) int) Outcome {
		var ps [][2]int
		for _, i := range occurrences(text, needle, 0, len(text)) {
			s, e := back(i), back(i+len(needle))
			for s > 0 && raw[s-1] != '\n' && raw[s-1] == ' ' { // a tolerant match may start after the line's own spaces
				s--
			}
			if (s == 0 || raw[s-1] == '\n') && (e == len(raw) || raw[e] == '\n') {
				ps = append(ps, [2]int{s, e})
			}
		}
		switch len(ps) {
		case 0:
			return Outcome{}
		case 1:
			return Outcome{Status: StatusLocated, Method: "exact", Start: ps[0][0], End: ps[0][1]}
		}
		return Outcome{Status: StatusAmbiguous, Reason: "the paragraph occurs more than once"}
	}
	if o := found(raw, p, func(i int) int { return i }); o.Status != "" {
		return o
	}
	np, _ := normalize(p)
	if np = strings.TrimSpace(np); np != "" {
		if o := found(nt.norm, np, func(i int) int { return nt.back[i] }); o.Status != "" {
			o.Method = "tolerant"
			return o
		}
	}
	return Outcome{Status: StatusNotFound, Reason: "no paragraph of the document reads so"}
}

// normPos is the normalised position of raw offset i.
func (t *Text) normPos(i int) int {
	lo, hi := 0, len(t.back)-1
	for lo < hi {
		m := (lo + hi) / 2
		if t.back[m] < i {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

func commonSuffix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// compareTexts is the word-level difference of old to new as edits of old: paragraphs are diffed
// first (a paragraph of old with no counterpart is a delete_paragraph, one of new an
// insert_paragraph after the old paragraph before it, or before the first), then each changed
// paragraph pair word by word, every editHunk a substitution naming its paragraph with 40 bytes of
// context on each side. Nothing after a Footnotes or Endnotes line is compared (the writer leaves
// notes alone).
func compareTexts(old, new string) []Edit {
	oldLines, newLines := body(old), body(new)
	var edits []Edit
	anchor := -1 // the last old paragraph kept or changed, for insertions
	for _, h := range editScript(oldLines, newLines) {
		if h.kind == same {
			anchor = h.oldEnd - 1
			continue
		}
		insert := func(line string) {
			if anchor < 0 {
				edits = append(edits, Edit{Kind: KindInsertParagraphBefore, Paragraph: oldLines[0], Replace: line})
			} else {
				edits = append(edits, Edit{Kind: KindInsertParagraph, Paragraph: oldLines[anchor], Replace: line})
			}
		}
		// pair each old paragraph with the next new one that mostly matches it (in order); new
		// paragraphs before the pair are insertions, old ones with no pair deletions
		j := h.newStart
		for i := h.oldStart; i < h.oldEnd; i++ {
			k := j
			for k < h.newEnd && similarity(oldLines[i], newLines[k]) < 0.5 {
				k++
			}
			if k == h.newEnd { // no pair
				edits = append(edits, Edit{Kind: KindDeleteParagraph, Paragraph: oldLines[i]})
				continue
			}
			for ; j < k; j++ {
				insert(newLines[j])
			}
			edits = append(edits, words(oldLines[i], newLines[k])...)
			anchor, j = i, k+1
		}
		for ; j < h.newEnd; j++ {
			insert(newLines[j])
		}
	}
	return edits
}

// similarity is the share of a's word tokens that b holds too (0 for an empty a).
func similarity(a, b string) float64 {
	at, bt := wordTokens(a), wordTokens(b)
	if len(at) == 0 {
		return 0
	}
	have := map[string]int{}
	for _, t := range bt {
		have[strings.TrimSpace(t)]++
	}
	n := 0
	for _, t := range at {
		if have[strings.TrimSpace(t)] > 0 {
			have[strings.TrimSpace(t)]--
			n++
		}
	}
	return float64(n) / float64(len(at))
}

// body is text's body as lines: up to the first Footnotes/Endnotes section, empty lines included
// (they are paragraphs too), a trailing line break not a line.
func body(text string) []string {
	for _, marker := range []string{"\n\nFootnotes\n", "\n\nEndnotes\n"} {
		if i := strings.Index(text, marker); i >= 0 {
			text = text[:i+1]
		}
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// words diffs two paragraphs token by token (words with their following spaces) into
// substitutions of old; adjacent changed tokens form one editHunk.
func words(old, new string) []Edit {
	ot, nt := wordTokens(old), wordTokens(new)
	var edits []Edit
	at := 0 // byte offset in old of ot[i]
	offs := make([]int, len(ot)+1)
	for i, t := range ot {
		offs[i] = at
		at += len(t)
	}
	offs[len(ot)] = at
	for _, h := range editScript(ot, nt) {
		if h.kind == same {
			continue
		}
		start, end := offs[h.oldStart], offs[h.oldEnd]
		e := Edit{Find: old[start:end], Replace: strings.Join(nt[h.newStart:h.newEnd], ""), Paragraph: old}
		if start == end && e.Replace == "" {
			continue
		}
		e.Before, e.After = old[runeStartAt(old, start-editContext):start], old[end:runeStartAt(old, end+editContext)]
		edits = append(edits, e)
	}
	return edits
}

// wordTokens splits a paragraph into words, each with the white space after it.
func wordTokens(s string) []string {
	var out []string
	start := 0
	inSpace := false
	for i, r := range s {
		if r == ' ' || r == '\t' {
			inSpace = true
		} else if inSpace {
			out = append(out, s[start:i])
			start, inSpace = i, false
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// runeStartAt is i clamped into s and moved back to a rune boundary.
func runeStartAt(s string, i int) int {
	i = min(max(i, 0), len(s))
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// editHunk is one run of an edit script: equal, or a replacement of old[oldStart:oldEnd] by new[newStart:newEnd].
type editHunk struct {
	kind                               int
	oldStart, oldEnd, newStart, newEnd int
}

const (
	same = iota
	changed
)

// editScript is a shortest-edit-script diff (Myers) of two token lists as hunks, adjacent changes
// merged. Bounded: beyond diffMax tokens on both sides a whole-list replacement is returned
// (the caller's edits are then one per paragraph, still correct).
func editScript(a, b []string) []editHunk {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	if n > diffMax && m > diffMax {
		return []editHunk{{changed, 0, n, 0, m}}
	}
	// the standard O((N+M)D) forward algorithm with a trace, giving the edit script
	maxD := n + m
	v := map[int]int{1: 0}
	var trace []map[int]int
	var found bool
	for d := 0; d <= maxD && !found; d++ {
		snap := make(map[int]int, len(v))
		for k, x := range v {
			snap[k] = x
		}
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || k != d && v[k-1] < v[k+1] {
				x = v[k+1]
			} else {
				x = v[k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	// walk the trace back into moves
	type move struct{ x, y, px, py int }
	var moves []move
	x, y := n, m
	for d := len(trace) - 1; d >= 0 && (x > 0 || y > 0); d-- {
		vd := trace[d]
		k := x - y
		var pk int
		if k == -d || k != d && vd[k-1] < vd[k+1] {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := vd[pk]
		py := px - pk
		for x > px && y > py {
			moves = append(moves, move{x - 1, y - 1, x, y})
			x, y = x-1, y-1
		}
		if d > 0 {
			moves = append(moves, move{px, py, x, y})
		}
		x, y = px, py
	}
	var out []editHunk
	ox, oy := 0, 0
	push := func(kind, x1, y1 int) {
		if n := len(out); n > 0 && out[n-1].kind == kind {
			out[n-1].oldEnd, out[n-1].newEnd = x1, y1
		} else {
			out = append(out, editHunk{kind, ox, x1, oy, y1})
		}
		ox, oy = x1, y1
	}
	for i := len(moves) - 1; i >= 0; i-- {
		mv := moves[i]
		if mv.px-mv.x == 1 && mv.py-mv.y == 1 {
			push(same, mv.px, mv.py)
		} else {
			push(changed, mv.px, mv.py)
		}
	}
	if ox < n || oy < m {
		push(changed, n, m)
	}
	return out
}

// diffMax bounds the Myers search: 20,000 tokens a side is a long contract's words.
var diffMax = 20000
