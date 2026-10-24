// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"regexp"
	"strings"

	"github.com/discover-legal/BigLaw/internal/docs"
)

// A requirement joined from several passages (requirementParts) is the document's words only when
// each part is a whole sentence, clause or line and the parts are one passage in the document's
// order: otherwise two real fragments stitch an instruction the document does not give (lab-r3
// verifier: "… shall serve as successor Trustee, in the following order of priority: Sophia
// Davenport-Chen as a successor trustee in any capacity", the tail of "Do NOT name Sophia …").
const (
	maxJoins   = 12  // joins a requirement is cut at; more is refused as a whole (each part is searched)
	maxPlaces  = 8   // occurrences of one part tried for one whose edges are whole
	maxGap     = 400 // bytes of the document between two parts
	maxSkipped = 2   // whole sentences two parts may skip between them
	maxLines   = 200 // lines walked back for a list item's introduction
)

// joins are where a model joins passages of a requirement: after a colon, semicolon, sentence end
// or ellipsis followed by white space, at a line break, and at an ellipsis it wrote.
var joins = regexp.MustCompile(`[:;.!?](?:["”’)]*)\s+|\s*(?:…|\.\.\.)\s*|\s*\n\s*`)

// listMarker is a list item's or heading's marker at the start of a line: 1. (a) ii) B. - •
var listMarker = regexp.MustCompile(`^[ \t]*(?:\(?[0-9]{1,3}[.)]|\(?[A-Za-z]{1,4}[.)]|[-•*–])[ \t]+`)

// guards are the words whose loss changes what a clause requires: a negation, a modal, an exception
// or a condition. A part may start inside a sentence only when the words it drops hold none.
var guards = wordSet("not no nor never neither none nothing without except excluding unless only if upon provided subject notwithstanding until shall must may will should would can cannot could might ought")

// qualifiers are the words of a sentence that qualifies its neighbour; two parts never skip one.
var qualifiers = wordSet("except exception unless notwithstanding provided however foregoing preceding above below apply applies save instead otherwise rather")

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// hasWord reports whether s holds one of set's words (case folded; for a set holding "not", a word
// ending n't too).
func hasWord(s string, set map[string]bool) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r == '\'' || r == '’') }) {
		if w = strings.Trim(w, "'’"); set[w] || set["not"] && (strings.HasSuffix(w, "n't") || strings.HasSuffix(w, "n’t")) {
			return true
		}
	}
	return false
}

// requirementParts verifies a requirement in a document's text and returns the document's words for
// it: the whole, as sent or with its JSON escapes undone, else part by part (bench: a heading
// joined to its list, "Successor Trustees: … First Successor Trustee: …"; two sentences of one
// passage). The requirement is cut at its joins (at most maxJoins), and each part is the longest run
// of pieces, its ellipses and edge punctuation stripped, that the document holds (exact or tolerant)
// where it is whole (wholeStart, wholeEnd) and, after the first, after the part before it with only
// a bounded gap between (partGap), placed by one normalised index of the document. A part of fewer
// than four words is a heading: only the first of several, and a line of its own. bad is the first part that does not verify ("" when all do); err is ctx's.
func requirementParts(ctx context.Context, text, req string) (parts []string, bad string, err error) {
	req, idx := unescape(req), docs.NewText(text)
	if s, e, ok := place(idx, text, strings.TrimSpace(req), -1); ok { // one part: whole too (lab-r3 r2)
		return []string{text[s:e]}, "", nil
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
		if m := text[s:e]; len(strings.Fields(m)) < 4 && (len(parts) > 0 || j == len(cuts)-1 || !lineEnd(text, e)) {
			return nil, piece, nil // a short part only as a heading: the first of several, and a line of the document's own; never "" (that is success)
		}
		parts, prev, i = append(parts, text[s:e]), e, j
	}
	return parts, "", nil
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

// place finds part q in text where it is a whole part: the first after the previous part's end
// (prev, -1 for the first part) whose edges are whole and whose gap from that part is allowed.
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
		if wholeEnd(text, e) && (prev < 0 && wholeStart(text, s) || prev >= 0 && partGap(text, prev, s)) {
			return s, e, true
		}
		from = s + 1
	}
	return 0, 0, false
}

// closers are what may follow a sentence's last word: closing quotes and brackets.
const closers = "\"”’')]"

// wholeEnd: the part ends a sentence, clause or line (. ! ? ; : after it or as its last character,
// before white space or the end), never inside one (bench: "the residuary estate shall" before "NOT").
func wholeEnd(text string, e int) bool {
	rest := strings.TrimLeft(text[e:], closers)
	if endsWith(text[:e], ".!?;:") && (rest == "" || isSpace(rest[0])) {
		return true
	}
	if rest != "" && strings.ContainsRune(".!?;:", rune(rest[0])) {
		rest = strings.TrimLeft(rest[1:], closers)
		return rest == "" || isSpace(rest[0])
	}
	return lineEnd(text, e)
}

// lineEnd: only spaces or tabs lie between byte e of text and a line break or the end.
func lineEnd(text string, e int) bool {
	rest := strings.TrimLeft(text[e:], " \t")
	return rest == "" || rest[0] == '\n' || rest[0] == '\r'
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// endsWith reports whether s, closing quotes and brackets aside, ends with one of chars.
func endsWith(s, chars string) bool {
	t := strings.TrimRight(s, closers)
	return t != "" && strings.ContainsRune(chars, rune(t[len(t)-1]))
}

// sentenceEnds finds the sentence ends in a line: . ! ? (and closers) before white space or its end.
var sentenceEnds = regexp.MustCompile(`[.!?]["”’')\]]*(?:\s|$)`)

// wholeStart: the first part (or a one-part requirement) starts a line (after a list marker) that
// no list introduction governs, a sentence, or a clause (after , ; : or a dash) whose sentence's
// earlier words hold no guard: never a tail that drops "Do NOT", "shall not", "except" or a
// condition, nor an item that drops "The following shall NOT serve:".
func wholeStart(text string, s int) bool {
	before := text[:s]
	line := before[strings.LastIndexByte(before, '\n')+1:]
	if strings.TrimSpace(line) == "" || listMarker.FindString(line) == line {
		return governing(text, s) < 0 // a line: unless it is an item of a list whose introduction governs it
	}
	t := strings.TrimRight(line, " \t")
	if t != line && endsWith(t, ".!?") {
		return true // a sentence start
	}
	clause := strings.HasSuffix(t, "—") || strings.HasSuffix(t, "–") || endsWith(t, ",;:")
	return clause && !hasWord(line[sentenceIn(line):], guards)
}

// sentenceIn is where the last sentence of line begins (after its last . ! ? and white space).
func sentenceIn(line string) int {
	for j := len(line) - 1; j >= 2; j-- {
		if isSpace(line[j-1]) && endsWith(line[:j-1], ".!?") {
			return j
		}
	}
	return 0
}

// partGap: a later part follows the one before it (ending at prev) in the document's order, within
// maxGap bytes, separated only by white space, list markers and the punctuation that ended the
// earlier part, or, after a part that ends a sentence, by at most maxSkipped whole sentences of the
// same passage (each line ends a sentence: no heading, no introduction to a list) that qualify
// nothing (no qualifiers); a part after a skip starts a sentence or line itself.
func partGap(text string, prev, s int) bool {
	if s < prev || s-prev > maxGap {
		return false
	}
	gap := strings.TrimLeft(text[prev:s], closers+".!?;:,")
	var core []string
	for _, l := range strings.Split(gap, "\n") {
		if l = strings.TrimSpace(listMarker.ReplaceAllString(l, "")); l != "" {
			core = append(core, l)
		}
	}
	if len(core) == 0 {
		return true
	}
	after := strings.TrimLeft(text[prev:s], closers)
	if !(endsWith(text[:prev], ".!?") || after != "" && strings.ContainsRune(".!?", rune(after[0]))) || hasWord(strings.Join(core, " "), qualifiers) {
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

// governing is where the list introduction that governs the line starting at byte s begins, -1 when
// none does: walking back over the list's earlier items (marked lines; an unmarked line only right
// after its introduction), the first other non-empty
// line, when it ends with a colon and holds a guard (a negation, modal, exception or condition:
// "The following shall NOT serve as Trustee:"). When a marked line on the way (or the line itself)
// has its predecessor marker (B. for C.) on the nearest marked line above that introduction, it is
// a sibling heading, not an item of it (bench: "C. Tommy's Share" after
// Sophia's "…with the following terms and conditions:").
func governing(text string, s int) int {
	ls := strings.LastIndexByte(text[:s], '\n') + 1
	markers := []string{listMarker.FindString(text[ls:min(len(text), ls+16)])} // of the line and the items between it and the introduction
	for end, n := ls-1, 0; end > 0 && n < maxLines; n++ {
		start := strings.LastIndexByte(text[:end], '\n') + 1
		l := strings.TrimSpace(text[start:end])
		switch {
		case l == "":
		case listMarker.MatchString(l):
			if markers[0] == "" {
				return -1 // an unmarked line after a list's items is past the list
			}
			markers = append(markers, listMarker.FindString(l))
		case endsWith(l, ":") && hasWord(l, guards):
			above := lastMarker(text[:start])
			for _, m := range markers {
				if p := predecessor(m); p != "" && strings.TrimSpace(above) == p {
					return -1
				}
			}
			return start
		default:
			return -1
		}
		end = start - 1
	}
	return -1
}

// lastMarker is the list marker of the last marked line of text ("" when none).
func lastMarker(text string) string {
	for end, n := len(text), 0; end > 0 && n < maxLines; n++ {
		start := strings.LastIndexByte(text[:end-1], '\n') + 1
		if m := listMarker.FindString(strings.TrimSpace(text[start:end])); m != "" {
			return m
		}
		end = start
	}
	return ""
}

// predecessor is the marker before a one-letter or one-number marker in its style ("B. " for
// "C. ", "(1)" for "(2)"), "" when there is none.
func predecessor(marker string) string {
	m := strings.TrimSpace(marker)
	for i, r := range m {
		if r >= 'b' && r <= 'z' || r >= 'B' && r <= 'Z' || r >= '2' && r <= '9' {
			if p := m[:i] + string(r-1) + m[i+1:]; len(m) <= 4 && !strings.ContainsAny(m[i+1:], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
				return p
			}
			return ""
		}
	}
	return ""
}

// whole reports whether text[s:e] is a whole passage: wholeStart and wholeEnd.
func whole(text string, s, e int) bool { return wholeStart(text, s) && wholeEnd(text, e) }
