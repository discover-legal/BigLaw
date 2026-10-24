// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	ctxMax     = 8 << 10 // bytes of the text a requirement is read in, at most (its heading-path label aside)
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

// sentenceGap is where a sentence or clause ends and the next begins (an end mark, closers, white
// space), or a line break; sentenceStop is a sentence end.
var (
	sentenceGap  = regexp.MustCompile(`[.!?;:：](?:["”’)\]]*)[ \t\r]*\n?[ \t]*|\n[ \t]*`)
	sentenceStop = regexp.MustCompile(`[.!?](?:["”’)\]]*)\s`)
	// paraBreak is a paragraph start: a blank line, or a line break after a sentence or clause end
	// before a capital, an opening quote or bracket, or a list marker. A .docx reader (and any reader
	// whose lines are paragraphs) ends a paragraph with one line break (lab-r3 r5 verifier M2).
	paraBreak = regexp.MustCompile(`\n[ \t\r]*\n|[.!?:;：]["”’)\]]*[ \t\r]*\n[ \t]*(?:\p{Lu}|["“‘(\[]|[0-9]{1,3}[.)]|[•*–-][ \t])`)
)

// Window bounds (lab-r3 r4, coordinator decision): a requirement is read in the document's text around
// it, not in a section of docs.Parse's tree, whose heading guesses are not reliable enough for a safety
// check (a wrapped cross-reference read as a heading split the requirement's sentence, verifier H2).
const (
	ctxSide  = 3 << 10 // bytes of text read before the requirement and after it
	ctxReach = 256     // how far an edge may move outward to a sentence or line end
	maxPath  = 512     // bytes of the heading-path label
)

// contextOf is a verified requirement's context (spans in document order, the first part's start to
// the last part's end as one requirement): the requirement with about ctxSide bytes of the document
// before and after it, each edge moved to a sentence or line end (outward within ctxReach, else
// inward), at most ctxMax bytes whatever docs.Parse makes of the document (M2). An introduction or
// lead-in without a colon and a sibling's override within that reach are in it (M1); an override
// farther away, or in another clause or definition, is not (README discloses it). truncated reports
// that the requirement's sentence or paragraph starts before the window, or an edge cut a sentence:
// the verdict on it is then admitted only at low confidence, for a lawyer's review. path is the
// heading path above the requirement, from docs.Parse: a label only, never a boundary.
func contextOf(text string, spans [][2]int) (ps []span, path string, truncated bool) {
	s, e := spans[0][0], spans[len(spans)-1][1]
	side := max(0, min(ctxSide, (ctxMax-2*ctxReach-(e-s))/2))
	a, b := 0, len(text)
	if s-side > 0 {
		a = edgeBefore(text, s-side, s)
		if a < 0 {
			a, truncated = -a, true
		}
		at := text[max(0, a-8):min(len(text), s+1)]                                                                // a break just before the edge counts
		truncated = truncated || !paraBreak.MatchString(at) || !sentenceStop.MatchString(at) || listStart(text, a) // the requirement's paragraph or sentence, or its list, starts before the window
	}
	if e+side < len(text) {
		b = edgeAfter(text, e+side, e)
		if b < 0 {
			b, truncated = -b, true
		}
	}
	return []span{{a, b}}, headingPath(text, a, s), truncated
}

// listStart reports whether the window's first line, at a, is a list item: the list's introduction (a
// "shall not:" chapeau) may then be above the window (lab-r3 r5 verifier L1). Whether it is the list's
// first item cannot be told ("(i)" after "(h)"), so any item counts.
func listStart(text string, a int) bool {
	l := lineOf(text, a)
	return strings.TrimSpace(text[l.s:a]) == "" && listMarker.MatchString(text[a:l.e])
}

// edgeBefore moves a window's start at i to the nearest sentence or line end: outward within ctxReach,
// else inward (never past limit). Negative: no such end, so the edge (at white space) cuts a sentence.
func edgeBefore(text string, i, limit int) int {
	if ms := sentenceGap.FindAllStringIndex(text[max(0, i-ctxReach):i], -1); len(ms) > 0 {
		return max(0, i-ctxReach) + ms[len(ms)-1][1]
	}
	if m := sentenceGap.FindStringIndex(text[i:limit]); m != nil {
		return i + m[1]
	}
	for i < limit && !utf8.RuneStart(text[i]) {
		i++
	}
	if k := strings.IndexFunc(text[i:limit], unicode.IsSpace); k >= 0 {
		i += k + 1
	}
	return -i
}

// edgeAfter moves a window's end at i to the nearest sentence or line end: outward within ctxReach,
// else inward (never before limit). Negative: no such end, so the edge cuts a sentence.
func edgeAfter(text string, i, limit int) int {
	end := func(m []int, at int) int { // after the end mark and its closers, before the white space
		return at + m[0] + len(strings.TrimRight(text[at+m[0]:at+m[1]], " \t\r\n"))
	}
	if m := sentenceGap.FindStringIndex(text[i:min(len(text), i+ctxReach)]); m != nil {
		return end(m, i)
	}
	if ms := sentenceGap.FindAllStringIndex(text[limit:i], -1); len(ms) > 0 {
		return end(ms[len(ms)-1], limit)
	}
	for i > limit && !utf8.RuneStart(text[i]) {
		i--
	}
	if k := strings.LastIndexFunc(text[limit:i], unicode.IsSpace); k >= 0 {
		i = limit + k
	}
	return -i
}

// headingPath is the heading lines of docs.Parse's sections that hold byte at and start before the
// window's start a (the window shows the others), outermost first, joined by " › " (each at most 120
// bytes, all at most maxPath): a label for where the text is, never a boundary of what is read.
func headingPath(text string, a, at int) string {
	var hs []string
	n := 0
	for _, x := range docs.Parse(text) {
		l := lineOf(text, x.Start)
		if x.Level == 0 || x.Start > at || at >= x.End || l.e > a {
			continue
		}
		h := truncate(strings.Join(strings.Fields(text[l.s:l.e]), " "), 120)
		if n += len(h) + 5; h == "" || n > maxPath {
			break
		}
		hs = append(hs, h)
	}
	return strings.Join(hs, " › ")
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
