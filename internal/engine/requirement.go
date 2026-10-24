// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/docs"
)

// A requirement is verified where the document has it (requirementParts), then read in its section
// (contextOf): the leaf section of the document's own section tree (docs.Parse, byte-exact) that holds
// it, whole, with the heading path of the sections that enclose it. Structure is the tree's, never a
// guess at layout (lab-r3 r3 verifier: walking lines upward missed blank-line lists, read a name as a
// heading, trimmed around the wrong quote, never saw an override below the requirement). Whether the
// section, read as a whole, imposes the requirement as a verdict states it (a negation, an override)
// is a question for a model, not for code: check_deviation asks it (deviation.go checkSection).
// Parts joined from several passages must still be one passage in the document's order: stitching
// across a heading, a list's introduction or a section stays refused.
const (
	maxJoins   = 12      // joins a requirement is cut at; more is refused as a whole (each part is searched)
	maxPlaces  = 8       // occurrences of one part tried for one whose gap from the part before is allowed
	maxGap     = 400     // bytes of the document between two parts
	maxSkipped = 2       // whole sentences two parts may skip between them
	ctxMax     = 8 << 10 // bytes of a requirement's section it is read in, at most (its heading path aside)
)

// joins are where a model joins passages of a requirement: after a colon, semicolon, sentence end
// or ellipsis followed by white space, at a line break, and at an ellipsis it wrote.
var joins = regexp.MustCompile(`[:;.!?](?:["”’)]*)\s+|\s*(?:…|\.\.\.)\s*|\s*\n\s*`)

// listMarker is a list item's or heading's marker at the start of a line: 1. (a) ii) B. - •
var listMarker = regexp.MustCompile(`^[ \t]*(?:\(?[0-9]{1,3}[.)]|\(?[A-Za-z]{1,4}[.)]|[-•*–])[ \t]+`)

// foldCf drops format characters (zero-width spaces and joiners, bidi marks, soft hyphens): they
// render as nothing, so structure is read without them (lab-r3 r2 verifier L3: "N​OT").
func foldCf(s string) string {
	if !strings.ContainsFunc(s, isCf) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isCf(r) {
			return -1
		}
		return r
	}, s)
}

func isCf(r rune) bool { return unicode.Is(unicode.Cf, r) }

// requirementParts verifies a requirement in a document's text and returns the document's words for
// it, part by part (placeRequirement).
func requirementParts(ctx context.Context, text, req string) (parts []string, bad string, err error) {
	spans, bad, err := placeRequirement(ctx, text, req)
	for _, s := range spans {
		parts = append(parts, text[s[0]:s[1]])
	}
	return parts, bad, err
}

// placeRequirement verifies a requirement in a document's text and returns where the document has
// it: the whole, as sent or with its JSON escapes undone, else part by part (bench: a heading
// joined to its list, "Successor Trustees: … First Successor Trustee: …"; two sentences of one
// passage). The requirement is cut at its joins (at most maxJoins), and each part is the longest run
// of pieces, its ellipses and edge punctuation stripped, that the document holds (exact or tolerant,
// format characters folded) after the part before it with only a bounded gap between (partGap),
// placed by one normalised index of the document. A part of fewer than four words is a heading:
// only the first of several, and a line of its own. bad is the first part that does not verify (""
// when all do); err is ctx's.
func placeRequirement(ctx context.Context, text, req string) (spans [][2]int, bad string, err error) {
	req, idx := unescape(req), docs.NewText(text)
	if s, e, ok := place(idx, text, strings.TrimSpace(req), -1); ok {
		return [][2]int{{s, e}}, "", nil
	}
	locs := joins.FindAllStringIndex(req, maxJoins+1)
	if len(locs) > maxJoins {
		return nil, req, nil
	}
	cuts := []int{0} // piece boundaries in req: 0, each join's end, len(req); no piece is only white space or joins
	for _, loc := range locs {
		if loc[1] < len(req) && strings.TrimSpace(strings.Trim(req[cuts[len(cuts)-1]:loc[1]], ":;.!?…\"”’) \t\n")) != "" {
			cuts = append(cuts, loc[1])
		}
	}
	cuts = append(cuts, len(req))
	prev := -1 // where the last part ends in text
	for i := 0; i+1 < len(cuts); {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		j, s, e := len(cuts)-1, 0, 0
		for ; j > i; j-- {
			if q := edges(req[cuts[i]:cuts[j]]); q != "" {
				var ok bool
				if s, e, ok = place(idx, text, q, prev); ok {
					break
				}
			}
		}
		piece := cmpStr(edges(req[cuts[i]:cuts[i+1]]) != "", edges(req[cuts[i]:cuts[i+1]]), req)
		if j == i {
			return nil, piece, nil
		}
		if m := text[s:e]; len(strings.Fields(m)) < 4 && (len(spans) > 0 || j == len(cuts)-1 || !lineEnd(text, e)) {
			return nil, piece, nil // a short part only as a heading: the first of several, and a line of the document's own; never "" (that is success)
		}
		spans, prev, i = append(spans, [2]int{s, e}), e, j
	}
	return spans, "", nil
}

// edges strips a piece of white space, the ellipses a model joined it with, and the colon,
// semicolon or comma that ended it in the requirement.
func edges(p string) string {
	for {
		q := strings.TrimSpace(p)
		q = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(q, "…"), "..."))
		q = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(q, "…"), "..."))
		q = strings.TrimRight(q, ":;, ")
		if q == p {
			return q
		}
		p = q
	}
}

// place finds part q in text: the first occurrence (for a later part, the first after the previous
// part's end, prev, whose gap from it is allowed).
func place(idx *docs.Text, text, q string, prev int) (int, int, bool) {
	from, to := 0, len(text)
	if prev >= 0 {
		from, to = prev, prev+maxGap+2*len(q)+8 // a tolerant match may span more white space than q
	}
	for range maxPlaces {
		s, e, ok := idx.Find(q, from, to)
		if !ok {
			return 0, 0, false
		}
		if prev < 0 || partGap(text, prev, s) {
			return s, e, true
		}
		from = s + 1
	}
	return 0, 0, false
}

// closers are what may follow a sentence's last word: closing quotes and brackets.
const closers = "\"”’')]"

// lineEnd: only spaces or tabs lie between byte e of text and a line break or the end.
func lineEnd(text string, e int) bool {
	rest := strings.TrimLeft(text[e:], " \t")
	return rest == "" || rest[0] == '\n' || rest[0] == '\r'
}

// endsWith reports whether s, closing quotes and brackets aside, ends with one of chars.
func endsWith(s, chars string) bool {
	t := strings.TrimRight(s, closers)
	return t != "" && strings.ContainsRune(chars, rune(t[len(t)-1]))
}

// sentenceEnds finds the sentence ends in a line: . ! ? (and closers) before white space or its end.
var sentenceEnds = regexp.MustCompile(`[.!?]["”’')\]]*(?:\s|$)`)

// partGap: a later part follows the one before it (ending at prev) in the document's order, within
// maxGap bytes, separated only by white space, list markers and the punctuation that ended the
// earlier part, or, after a part that ends a sentence, by at most maxSkipped whole sentences of the
// same passage (each line ends a sentence: no heading, no introduction to a list, and no sentence
// head the later part would start inside). What a skip passes over is in the requirement's
// context, which the subagent reads and the finding quotes.
func partGap(text string, prev, s int) bool {
	if s < prev || s-prev > maxGap {
		return false
	}
	raw := foldCf(text[prev:s])
	gap := strings.TrimLeft(raw, closers+".!?;:,")
	var core []string
	for _, l := range strings.Split(gap, "\n") {
		if l = strings.TrimSpace(listMarker.ReplaceAllString(l, "")); l != "" {
			core = append(core, l)
		}
	}
	if len(core) == 0 {
		return true
	}
	after := strings.TrimLeft(raw, closers)
	if !(endsWith(foldCf(text[:prev]), ".!?") || after != "" && strings.ContainsRune(".!?", rune(after[0]))) {
		return false
	}
	n := 0
	for _, l := range core {
		if !endsWith(l, ".!?") {
			return false
		}
		n += len(sentenceEnds.FindAllStringIndex(l, -1))
	}
	return n <= maxSkipped
}

// span is a byte range of a document's text.
type span struct{ s, e int }

// lineOf is the line holding byte i of text, without its line break.
func lineOf(text string, i int) span {
	i = min(max(i, 0), len(text))
	s := strings.LastIndexByte(text[:i], '\n') + 1
	e := strings.IndexByte(text[i:], '\n')
	if e < 0 {
		return span{s, len(text)}
	}
	return span{s, i + e}
}

// colonEnd reports whether a line, closing quotes and brackets aside, ends with a colon (also a
// full-width one): it introduces what follows it.
func colonEnd(line string) bool {
	t := strings.TrimRight(strings.TrimSpace(foldCf(line)), closers)
	return strings.HasSuffix(t, ":") || strings.HasSuffix(t, "：")
}

// item is a section that is an item of a list rather than a clause of its own: lettered or roman.
func item(x docs.Section) bool { return x.Scheme == "alpha" || x.Scheme == "roman" }

// parent is section i's nearest enclosing numbered section in the pre-order list, or -1 (a preface
// or flat document's level-0 section encloses nothing).
func parent(secs []docs.Section, i int) int {
	for j := i - 1; j >= 0; j-- {
		if secs[j].Level > 0 && secs[j].Level < secs[i].Level && secs[j].End >= secs[i].End {
			return j
		}
	}
	return -1
}

// introduced reports whether the last non-blank line before byte at ends with a colon.
func introduced(text string, at int) bool {
	t := strings.TrimRight(text[:at], " \t\r\n")
	return t != "" && colonEnd(text[lineOf(text, len(t)-1).s:len(t)])
}

// sectionOf is the section a requirement at text[s:e] is read in, from the document's own section
// tree (docs.Parse, the tree retrieve and the outline show): the deepest section holding the
// requirement; for a lettered or roman item, the clause that holds its list (so a sibling such as
// "(b) Paragraph (a) shall not apply" is read with it); and for a list with no enclosing clause, or a
// clause introduced by a colon line before it, the whole run of its sibling items with the text that
// introduces them. lo..hi is that text, whole; root is the section whose heading path governs it.
func sectionOf(secs []docs.Section, text string, s, e int) (lo, hi, root int) {
	n := -1
	for i, x := range secs {
		if x.Start <= s && e <= x.End {
			n = i
		}
	}
	if n < 0 { // across the preface and a heading: the whole document
		return 0, len(text), -1
	}
	for item(secs[n]) {
		p := parent(secs, n)
		if p < 0 {
			break
		}
		n = p
	}
	x := secs[n]
	if x.Level == 0 {
		return x.Start, x.End, n
	}
	first, last := n, n
	for j := n - 1; j >= 0 && secs[j].Level >= x.Level; j-- {
		if secs[j].Level == x.Level {
			if secs[j].Scheme != x.Scheme {
				break
			}
			first = j
		}
	}
	if !item(x) && !introduced(text, secs[first].Start) {
		return x.Start, x.End, n
	}
	for j := n + 1; j < len(secs) && secs[j].Level >= x.Level; j++ {
		if secs[j].Level == x.Level {
			if secs[j].Scheme != x.Scheme {
				break
			}
			last = j
		}
	}
	lo, root = 0, -1
	if at := secs[first].Start; at > 0 { // the section whose own text the list follows: its introduction
		for i, y := range secs {
			if y.Start < at && at <= y.BodyEnd {
				lo, root = y.Start, i
			}
		}
	}
	return lo, secs[last].End, root
}

// contextOf is a verified requirement's context (spans in document order, the first part's start to
// the last part's end as one requirement): the heading lines of the sections that enclose its
// section, then its section (sectionOf), whole, before and after the requirement, so introductions
// and overrides below it ("Notwithstanding the foregoing …", "(b) Paragraph (a) shall not apply") are
// in it. No layout is guessed: the structure is the document's section tree. A section over ctxMax
// bytes keeps the requirement, then the text before it up to the nearest colon-ending line above it
// (never cut), then as much of the section's text before and after as the bound allows, cut only at
// its far ends; truncated reports that anything was cut (the verdict on it is then admitted only at
// low confidence, for a lawyer's review).
func contextOf(text string, spans [][2]int) (ps []span, truncated bool) {
	s, e := spans[0][0], spans[len(spans)-1][1]
	secs := docs.Parse(text)
	lo, hi, root := sectionOf(secs, text, s, e)
	a, b := lo, hi
	if hi-lo > ctxMax {
		truncated = true
		keep := s
		for l := lineOf(text, s); l.s > lo; { // the nearest colon-ending line above the requirement
			l = lineOf(text, l.s-1)
			if colonEnd(text[l.s:l.e]) {
				keep = l.s
				break
			}
		}
		rem := max(0, ctxMax-(e-keep))
		a = keep - min(keep-lo, rem)
		b = e + min(hi-e, max(0, rem-(keep-a)))
		if a > lo { // start after white space
			if k := strings.IndexFunc(text[a:keep], unicode.IsSpace); k >= 0 {
				a += k + 1
			} else {
				a = keep
			}
		}
		if b < hi { // end before white space
			if k := strings.LastIndexFunc(text[e:b], unicode.IsSpace); k >= 0 {
				b = e + k
			} else {
				b = e
			}
		}
	}
	var path []int // the enclosing sections' headings, outermost first, and the root's own when it was cut
	if root >= 0 {
		if a > secs[root].Start {
			path = append(path, root)
		}
		for p := parent(secs, root); p >= 0; p = parent(secs, p) {
			path = append([]int{p}, path...)
		}
	}
	for _, i := range path {
		if l := lineOf(text, secs[i].Start); l.e <= a && strings.TrimSpace(text[l.s:l.e]) != "" {
			ps = append(ps, l)
		}
	}
	return merged(text, append(ps, span{a, b})), truncated
}

// merged joins pieces that only white space separates (one citation for one run of the text).
func merged(text string, ps []span) []span {
	var out []span
	for _, p := range ps {
		if n := len(out); n > 0 && p.s >= out[n-1].e && strings.TrimSpace(text[out[n-1].e:p.s]) == "" {
			out[n-1].e = p.e
			continue
		}
		out = append(out, p)
	}
	return out
}

// blankRun is a line break with any blank lines after it.
var blankRun = regexp.MustCompile(`[ \t\r]*\n(?:[ \t\r]*\n)*`)

// renderContext is a context as text: its pieces with an ellipsis where text is left out, each mark
// set in ⟦ ⟧, and line breaks written as nl (blank lines as one; lab-r3 r3 verifier L3).
func renderContext(text string, ps []span, marks []span, nl string) string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString("\n…\n")
		} else if l := lineOf(text, p.s); l.s < p.s && strings.TrimSpace(text[l.s:p.s]) != "" {
			b.WriteString("… ")
		}
		for at := p.s; at < p.e; {
			next := p.e
			for _, m := range marks {
				for _, k := range []int{m.s, m.e} {
					if k > at && k < next {
						next = k
					}
				}
			}
			for _, m := range marks {
				if m.s == at && m.e > m.s && m.s >= p.s && m.e <= p.e {
					b.WriteString("⟦")
				}
			}
			b.WriteString(text[at:next])
			at = next
			for _, m := range marks {
				if m.e == at && m.e > m.s && m.s >= p.s && m.e <= p.e {
					b.WriteString("⟧")
				}
			}
		}
		if l := lineOf(text, max(p.s, p.e-1)); p.e < l.e && strings.TrimSpace(text[p.e:l.e]) != "" {
			b.WriteString(" …")
		}
	}
	out := strings.TrimRight(b.String(), " \t\r\n")
	if nl == "\n" {
		return strings.ReplaceAll(out, "\r", "")
	}
	return blankRun.ReplaceAllString(out, nl)
}

// within reports where quote lies in the context (exact or tolerant, format characters folded,
// marks and a " … " join allowed): every part inside one piece, in order. ok is false when any
// part lies outside it (lab-r3 r2 verifier H3: a verdict's controlling quote is held to the
// requirement's context, never any substring of the document).
func within(idx *docs.Text, ps []span, quote string) (span, bool) {
	q := strings.NewReplacer("⟦", "", "⟧", "").Replace(foldCf(quote))
	at, prev := span{-1, -1}, -1
	for _, part := range strings.Split(q, strings.TrimSpace(partJoin)) {
		if part = edges(part); part == "" {
			continue
		}
		found := false
		for _, p := range ps {
			if s, e, ok := idx.Find(part, max(p.s, prev), p.e); ok {
				if at.s < 0 {
					at.s = s
				}
				at.e, prev, found = e, e, true
				break
			}
		}
		if !found {
			return span{}, false
		}
	}
	return at, prev >= 0
}
