// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/docs"
)

// A requirement is verified where the document has it (requirementParts), then read in its context
// (reqContext): the paragraph or list item that holds it, every introduction that governs it, and
// its section heading, found by the document's structure (markers, colons, blank lines), never by
// word lists. The subagent judges against that context and the finding quotes it, so a fragment can
// no longer invert what the document says: "name Sophia … as a successor trustee" is read, and
// quoted, inside "Do NOT name Sophia …" (lab-r3 verifier, rounds 1 and 2: every guard-word list left
// holes, an abbreviation, a numbered introduction, a missing exception word, a zero-width character).
// Parts joined from several passages must still be one passage in the document's order: stitching
// across a heading, a list's introduction or a section stays refused.
const (
	maxJoins   = 12   // joins a requirement is cut at; more is refused as a whole (each part is searched)
	maxPlaces  = 8    // occurrences of one part tried for one whose gap from the part before is allowed
	maxGap     = 400  // bytes of the document between two parts
	maxSkipped = 2    // whole sentences two parts may skip between them
	maxLines   = 200  // lines walked back for a requirement's governing introductions and heading
	ctxMax     = 1500 // bytes of context a requirement is quoted with (about)
	ctxTail    = 300  // bytes of a long introduction kept: its end, where it introduces
	maxHeading = 120  // a heading line's length at most
)

// joins are where a model joins passages of a requirement: after a colon, semicolon, sentence end
// or ellipsis followed by white space, at a line break, and at an ellipsis it wrote.
var joins = regexp.MustCompile(`[:;.!?](?:["”’)]*)\s+|\s*(?:…|\.\.\.)\s*|\s*\n\s*`)

// listMarker is a list item's or heading's marker at the start of a line: 1. (a) ii) B. - •
var listMarker = regexp.MustCompile(`^[ \t]*(?:\(?[0-9]{1,3}[.)]|\(?[A-Za-z]{1,4}[.)]|[-•*–])[ \t]+`)

// strongHeading opens a heading line whatever its case: Markdown, or an article, section or schedule.
var strongHeading = regexp.MustCompile(`^(?:#{1,6}\s|(?i:article|section|schedule|exhibit|appendix|annex|part)\s+[0-9IVXLCDMivxlcdmA-Z]|§)`)

// itemEnd is how a list item ends that runs on to the next one: a semicolon, perhaps "and" or "or".
var itemEnd = regexp.MustCompile(`;\s*(?:and|or)?$`)

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

// piece is one passage of a requirement's context: its range, and the anchor inside it that a
// trim keeps whole (the requirement, an introduction's end, a heading).
type piece struct{ s, e, as, ae int }

// lines over a document's text: line(i) is the line holding byte i, without its line break.
func lineOf(text string, i int) span {
	i = min(max(i, 0), len(text))
	s := strings.LastIndexByte(text[:i], '\n') + 1
	e := strings.IndexByte(text[i:], '\n')
	if e < 0 {
		return span{s, len(text)}
	}
	return span{s, i + e}
}

// above is the line before l (ok false at the start of the text).
func above(text string, l span) (span, bool) {
	if l.s == 0 {
		return span{}, false
	}
	return lineOf(text, l.s-1), true
}

// below is the line after l (ok false at the end of the text).
func below(text string, l span) (span, bool) {
	if l.e >= len(text) {
		return span{}, false
	}
	return lineOf(text, l.e+1), true
}

func clean(text string, l span) string { return strings.TrimSpace(foldCf(text[l.s:l.e])) }
func blank(text string, l span) bool   { return clean(text, l) == "" }
func marked(text string, l span) bool  { return listMarker.MatchString(clean(text, l)) }
func colon(text string, l span) bool   { return endsWith(clean(text, l), ":") }

// heading reports whether a line is a heading: short, not a clause that runs on (no , ; : at its
// end), and Markdown, an article/section/schedule line, or title-cased (every word of four letters
// or more capitalised, at most twelve words), and not the first line under an introduction
// ("…shall NOT serve:" over "Sophia Davenport-Chen" is an item, not a heading), unless it is a
// marked line over prose (headsProse); an unmarked one only after a blank line or at the start.
func heading(text string, l span) bool {
	t := clean(text, l)
	if t == "" || len(t) > maxHeading || endsWith(t, ",;:") {
		return false
	}
	if p, ok := above(text, l); ok && colon(text, p) && !headsProse(text, l) {
		return false
	}
	if strongHeading.MatchString(t) {
		return true
	}
	if p, ok := above(text, l); ok && !blank(text, p) && !marked(text, l) {
		return false // an unmarked heading stands after a blank line or at the start: else it is a run-on line or an item
	}
	words := strings.Fields(listMarker.ReplaceAllString(t, ""))
	if len(words) == 0 || len(words) > 12 {
		return false
	}
	letters := false
	for _, w := range words {
		run := []rune(w)
		if k := strings.IndexFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }); k >= 0 {
			run = []rune(w[:k])
		}
		if len(run) > 0 {
			letters = true
			if len(run) >= 4 && unicode.IsLower(run[0]) {
				return false // a sentence's word, not a title's
			}
		}
	}
	return letters
}

// headsProse: a marked line followed by an unmarked, non-blank line (a lettered heading over its
// body, "C. Tommy's Share" over its paragraph), not an item of a list of short items.
func headsProse(text string, l span) bool {
	n, ok := below(text, l)
	return ok && marked(text, l) && !blank(text, n) && !marked(text, n)
}

// shape is a list marker's style (its letters, numbers and roman numerals generalised, with its
// indentation), "" for an unmarked line: two items of one list have one shape.
func shape(text string, l span) string {
	raw := foldCf(text[l.s:l.e])
	m := listMarker.FindString(raw)
	if m == "" {
		return ""
	}
	indent := len(m) - len(strings.TrimLeft(m, " \t"))
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", indent))
	tok := strings.TrimSpace(m)
	for i := 0; i < len(tok); {
		j := i
		for j < len(tok) && (tok[j] >= 'a' && tok[j] <= 'z' || tok[j] >= 'A' && tok[j] <= 'Z') {
			j++
		}
		if j > i {
			run := tok[i:j]
			switch {
			case strings.Trim(run, "ivxlcdm") == "" && (len(run) > 1 || strings.Contains("ivx", run)):
				b.WriteByte('i')
			case strings.Trim(run, "IVXLCDM") == "" && (len(run) > 1 || strings.Contains("IVX", run)):
				b.WriteByte('I')
			case run == strings.ToLower(run):
				b.WriteByte('a')
			default:
				b.WriteByte('A')
			}
			i = j
			continue
		}
		for j < len(tok) && tok[j] >= '0' && tok[j] <= '9' {
			j++
		}
		if j > i {
			b.WriteByte('1')
			i = j
			continue
		}
		b.WriteByte(tok[i])
		i++
	}
	return b.String()
}

// unit is the paragraph or list item holding lines first..last: up to a blank line, a marked line
// (an item starts there), a heading (kept as its top) or a line that introduces it (ends with a
// colon); down to a blank line, a marked line or a strong heading.
func unit(text string, first, last span) span {
	x := first
	for !marked(text, x) && !heading(text, x) {
		p, ok := above(text, x)
		if !ok || blank(text, p) || colon(text, p) {
			break
		}
		x = p
	}
	y := last
	for {
		n, ok := below(text, y)
		if !ok || blank(text, n) || marked(text, n) || strongHeading.MatchString(clean(text, n)) {
			break
		}
		y = n
	}
	return span{x.s, y.e}
}

// reqContext is the context of the requirement at text[s:e], in document order: the unit holding it
// (a paragraph or list item), each introduction that governs it (a unit above that ends with a
// colon, or the unmarked line that opens its list), and its heading. Walking up a unit at a time:
// an item of the same list (same marker shape, or a run-on item ending with a semicolon) is passed
// over, as are a sibling's nested items; an introduction is taken and the walk goes on from its
// level; a heading is taken and ends the walk; a plain paragraph ends the governing chain, and only
// a heading is looked for above it. Bounded by maxLines.
func reqContext(text string, s, e int) []piece {
	own := unit(text, lineOf(text, s), lineOf(text, max(s, e-1)))
	first := lineOf(text, own.s)
	if heading(text, first) && first.e < own.e { // the unit opens with its heading: a piece of its own unless the requirement holds it
		if s < first.e {
			return []piece{{own.s, own.e, s, e}}
		}
		return []piece{{first.s, first.e, first.s, first.e}, {first.e + 1, own.e, s, e}}
	}
	out := []piece{{own.s, own.e, s, e}}
	cur, plain := shape(text, first), false
	x := first
	for n := 0; n < maxLines; n++ {
		p, ok := above(text, x)
		if !ok {
			break
		}
		if x = p; blank(text, p) {
			continue
		}
		u := span{unit(text, p, p).s, p.e} // a unit above ends where the walk came from
		top := lineOf(text, u.s)
		x = top
		n += strings.Count(text[u.s:u.e], "\n")
		switch sh := shape(text, top); {
		case plain:
			if heading(text, top) {
				return append(out, piece{top.s, top.e, top.s, top.e})
			}
		case colon(text, p): // an introduction: governs what follows it
			if heading(text, top) && top.e < u.e { // under its own heading
				return append(out, piece{top.e + 1, u.e, max(top.e+1, u.e-ctxTail), u.e}, piece{top.s, top.e, top.s, top.e})
			}
			out = append(out, piece{u.s, u.e, max(u.s, u.e-ctxTail), u.e})
			if heading(text, top) {
				return out
			}
			cur = sh
		case cur != "" && sh == cur, itemEnd.MatchString(clean(text, p)): // an earlier item of the same list
		case heading(text, top):
			return append(out, piece{top.s, top.e, top.s, top.e})
		case cur != "" && sh != "": // a nested item of an earlier sibling
		case cur != "": // the line that opens the list
			out = append(out, piece{u.s, u.e, max(u.s, u.e-ctxTail), u.e})
			cur = ""
		default:
			plain = true
		}
	}
	return out
}

// contextOf is a verified requirement's context (spans in document order, the first part's start to
// the last part's end as one requirement), its pieces in document order.
func contextOf(text string, spans [][2]int) []piece {
	ps := reqContext(text, spans[0][0], spans[len(spans)-1][1])
	slices.SortFunc(ps, func(a, b piece) int { return a.s - b.s })
	return ps
}

// trimContext cuts a context to about ctxMax bytes: each piece keeps its anchor whole (widened to
// focus where focus lies in it), then context around the anchors while the budget lasts, the pieces
// that hold focus first; cuts fall at white space.
func trimContext(text string, ps []piece, focus span) []piece {
	out := slices.Clone(ps)
	total, anchors := 0, 0
	for i, p := range out {
		if focus.s >= p.s && focus.e <= p.e {
			out[i].as, out[i].ae = min(p.as, focus.s), max(p.ae, focus.e)
		}
		total, anchors = total+p.e-p.s, anchors+out[i].ae-out[i].as
	}
	if total <= ctxMax {
		return merged(text, out)
	}
	rem := max(0, ctxMax-anchors)
	var order []int // the piece holding focus first, then the rest from the requirement up
	for i := len(out) - 1; i >= 0; i-- {
		if focus.s >= out[i].s && focus.e <= out[i].e {
			order = append([]int{i}, order...)
		} else {
			order = append(order, i)
		}
	}
	for _, i := range order {
		p := out[i]
		before, after := p.as-p.s, p.e-p.ae
		take := min(rem, before+after)
		rem -= take
		b := min(before, take/2)
		a := min(after, take-b)
		b = min(before, take-a)
		lo, hi := p.as-b, p.ae+a
		if lo > p.s { // start after white space
			if k := strings.IndexFunc(text[lo:p.as], unicode.IsSpace); k >= 0 {
				lo += k + 1
			} else {
				lo = p.as
			}
		}
		if hi < p.e { // end before white space
			if k := strings.LastIndexFunc(text[p.ae:hi], unicode.IsSpace); k >= 0 {
				hi = p.ae + k
			} else {
				hi = p.ae
			}
		}
		out[i].s, out[i].e = lo, hi
	}
	return merged(text, out)
}

// merged joins pieces that only white space separates (one citation for one run of the text).
func merged(text string, ps []piece) []piece {
	var out []piece
	for _, p := range ps {
		if n := len(out); n > 0 && p.s >= out[n-1].e && strings.TrimSpace(text[out[n-1].e:p.s]) == "" {
			out[n-1].e, out[n-1].ae = p.e, p.ae
			continue
		}
		out = append(out, p)
	}
	return out
}

// renderContext is a context as text: its pieces with an ellipsis where text is left out, each mark
// set in ⟦ ⟧, and line breaks written as nl.
func renderContext(text string, ps []piece, marks []span, nl string) string {
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteString(nl + "…" + nl)
		} else if lineOf(text, p.s).s < p.s && strings.TrimSpace(text[lineOf(text, p.s).s:p.s]) != "" {
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
			b.WriteString(strings.ReplaceAll(strings.TrimRight(text[at:next], "\r"), "\n", nl))
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
	return b.String()
}

// within reports where quote lies in the context (exact or tolerant, format characters folded,
// marks and a " … " join allowed): every part inside one piece, in order. ok is false when any
// part lies outside it (lab-r3 r2 verifier H3: a verdict's controlling quote is held to the
// requirement's context, never any substring of the document).
func within(idx *docs.Text, ps []piece, quote string) (span, bool) {
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
