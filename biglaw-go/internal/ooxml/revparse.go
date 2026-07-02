// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tracked-change parsing — the reverse direction of revisions.go. Where
// ApplyTracked writes <w:ins>/<w:del> surgery into a document, ParseRevisions
// reads an opposing-counsel markup back out: every insertion, deletion, and
// substitution with its author, date, texts, and enough surrounding untouched
// context to anchor a counter-edit. BaselineText reconstructs the document as
// it read before any change (insertions dropped, deletions restored).

package ooxml

import (
	"strings"
	"unicode/utf8"
)

// RevisionKind classifies a parsed tracked change.
type RevisionKind string

const (
	RevInsertion    RevisionKind = "insertion"
	RevDeletion     RevisionKind = "deletion"
	RevSubstitution RevisionKind = "substitution"
)

// revisionContextChars is how much untouched baseline text is captured on
// each side of a change so a counter-edit can be anchored unambiguously.
const revisionContextChars = 40

// Revision is one parsed tracked change. Offsets come in two coordinate
// systems: BaselineStart/End index into BaselineText() (the pre-change text —
// the span the change replaces; a zero-width span for pure insertions), and
// VisibleStart/End index into Text() (the span the inserted text occupies; a
// zero-width span marking the deletion point for pure deletions). Visible
// offsets are valid until the document is next mutated.
type Revision struct {
	Kind          RevisionKind `json:"kind"`
	Author        string       `json:"author"`
	Date          string       `json:"date,omitempty"`
	InsertedText  string       `json:"insertedText,omitempty"`
	DeletedText   string       `json:"deletedText,omitempty"`
	BaselineStart int          `json:"baselineStart"`
	BaselineEnd   int          `json:"baselineEnd"`
	VisibleStart  int          `json:"visibleStart"`
	VisibleEnd    int          `json:"visibleEnd"`
	ContextBefore string       `json:"contextBefore"`
	ContextAfter  string       `json:"contextAfter"`
}

// ParseRevisions extracts every tracked change from the document in document
// order. A <w:del> immediately adjacent to a <w:ins> by the same author (no
// untouched text or paragraph boundary between them, in either order) is
// reported as one substitution; adjacent same-author fragments of the same
// kind (e.g. a deletion Word split across runs) are coalesced.
func (d *Document) ParseRevisions() []Revision {
	_, revs := d.parseRevisions()
	return revs
}

// BaselineText returns the document text as it would read with every tracked
// change rejected: insertions dropped, deletions restored. Paragraphs are
// separated by "\n", matching Text().
func (d *Document) BaselineText() string {
	base, _ := d.parseRevisions()
	return base
}

// ─── Walk ─────────────────────────────────────────────────────────────────────

type revToken int

const (
	tokNone revToken = iota
	tokParaClose
	tokInsOpen
	tokInsClose
	tokDelOpen
	tokDelClose
	tokRunOpen
)

// revFragment is one raw <w:ins>/<w:del> text contribution before coalescing.
type revFragment struct {
	kind          RevisionKind
	author, date  string
	inserted      string
	deleted       string
	baselineStart int
	baselineEnd   int
	visibleStart  int
	visibleEnd    int
	// barrier counts the untouched content (plain-run text, paragraph
	// boundaries) seen so far; two fragments with equal barriers are
	// adjacent — nothing untouched sits between them.
	barrier int
}

// parseRevisions walks docXML once, building the baseline text (deletions
// restored, insertions dropped) alongside the visible text (mirroring
// index()) and collecting revision fragments in document order.
func (d *Document) parseRevisions() (string, []Revision) {
	xml := d.docXML
	var baseline, visible strings.Builder
	var frags []revFragment
	barrier := 0

	// One level of wrapper state each. Runs inside BOTH an open <w:ins> and
	// an open <w:del> are inserted-then-deleted content: already resolved,
	// present in neither text stream, reported as no revision.
	var insOpen, delOpen bool
	var insAuthor, insDate, delAuthor, delDate string

	i := 0
	for i < len(xml) {
		pos, tok := nextRevToken(xml, i)
		if pos < 0 {
			break
		}
		switch tok {
		case tokParaClose:
			baseline.WriteByte('\n')
			visible.WriteByte('\n')
			barrier++
			i = pos + len("</w:p>")

		case tokInsClose:
			insOpen = false
			i = pos + len("</w:ins>")

		case tokDelClose:
			delOpen = false
			i = pos + len("</w:del>")

		case tokInsOpen, tokDelOpen:
			gt := strings.IndexByte(xml[pos:], '>')
			if gt < 0 {
				return baseline.String(), coalesce(frags, baseline.String())
			}
			tag := xml[pos : pos+gt+1]
			i = pos + gt + 1
			if strings.HasSuffix(tag, "/>") {
				// Self-closing markers (paragraph-mark and table-row
				// insert/delete marks inside <w:rPr>/<w:trPr>) carry no run
				// content — skip without opening a wrapper.
				continue
			}
			if tok == tokInsOpen {
				insOpen, insAuthor, insDate = true, attrVal(tag, "w:author"), attrVal(tag, "w:date")
			} else {
				delOpen, delAuthor, delDate = true, attrVal(tag, "w:author"), attrVal(tag, "w:date")
			}

		case tokRunOpen:
			rClose := strings.Index(xml[pos:], "</w:r>")
			if rClose < 0 {
				return baseline.String(), coalesce(frags, baseline.String())
			}
			rEnd := pos + rClose + len("</w:r>")
			runXML := xml[pos:rEnd]
			i = rEnd
			switch {
			case insOpen && delOpen:
				// inserted-then-deleted: in neither stream
			case delOpen:
				if t := taggedRunText(runXML, "<w:delText"); t != "" {
					frags = append(frags, revFragment{
						kind: RevDeletion, author: delAuthor, date: delDate,
						deleted:       t,
						baselineStart: baseline.Len(), baselineEnd: baseline.Len() + len(t),
						visibleStart: visible.Len(), visibleEnd: visible.Len(),
						barrier: barrier,
					})
					baseline.WriteString(t)
				}
			case insOpen:
				if t := taggedRunText(runXML, "<w:t"); t != "" {
					frags = append(frags, revFragment{
						kind: RevInsertion, author: insAuthor, date: insDate,
						inserted:      t,
						baselineStart: baseline.Len(), baselineEnd: baseline.Len(),
						visibleStart: visible.Len(), visibleEnd: visible.Len() + len(t),
						barrier: barrier,
					})
					visible.WriteString(t)
				}
			default:
				if t := taggedRunText(runXML, "<w:t"); t != "" {
					baseline.WriteString(t)
					visible.WriteString(t)
					barrier++
				}
			}
		}
	}
	base := baseline.String()
	return base, coalesce(frags, base)
}

// coalesce merges adjacent same-author fragments — same-kind fragments concat
// (a change Word split across runs or elements) and a deletion/insertion pair
// becomes a substitution — then fills in the baseline context windows.
func coalesce(frags []revFragment, baseline string) []Revision {
	merged := make([]revFragment, 0, len(frags))
	for _, f := range frags {
		if n := len(merged); n > 0 {
			m := &merged[n-1]
			if m.author == f.author && m.barrier == f.barrier {
				switch {
				case m.kind == RevDeletion && f.kind == RevDeletion:
					m.deleted += f.deleted
					m.baselineEnd = f.baselineEnd
					continue
				case m.kind == RevInsertion && f.kind == RevInsertion:
					m.inserted += f.inserted
					m.visibleEnd = f.visibleEnd
					continue
				case m.kind == RevDeletion && f.kind == RevInsertion:
					m.kind = RevSubstitution
					m.inserted = f.inserted
					m.visibleStart, m.visibleEnd = f.visibleStart, f.visibleEnd
					continue
				case m.kind == RevInsertion && f.kind == RevDeletion:
					m.kind = RevSubstitution
					m.deleted = f.deleted
					m.baselineStart, m.baselineEnd = f.baselineStart, f.baselineEnd
					continue
				// A substitution keeps absorbing adjacent same-author
				// fragments — Word (and ApplyTracked's multi-run spans) can
				// split one side of a replace around the other.
				case m.kind == RevSubstitution && f.kind == RevDeletion:
					m.deleted += f.deleted
					m.baselineEnd = f.baselineEnd
					continue
				case m.kind == RevSubstitution && f.kind == RevInsertion:
					m.inserted += f.inserted
					m.visibleEnd = f.visibleEnd
					continue
				}
			}
		}
		merged = append(merged, f)
	}

	revs := make([]Revision, 0, len(merged))
	for _, m := range merged {
		before, after := contextAround(baseline, m.baselineStart, m.baselineEnd, revisionContextChars)
		revs = append(revs, Revision{
			Kind:          m.kind,
			Author:        m.author,
			Date:          m.date,
			InsertedText:  m.inserted,
			DeletedText:   m.deleted,
			BaselineStart: m.baselineStart,
			BaselineEnd:   m.baselineEnd,
			VisibleStart:  m.visibleStart,
			VisibleEnd:    m.visibleEnd,
			ContextBefore: before,
			ContextAfter:  after,
		})
	}
	return revs
}

// contextAround returns up to n bytes of s on each side of [start, end),
// snapped outward-safe to rune boundaries so a window never splits a
// multibyte character.
func contextAround(s string, start, end, n int) (before, after string) {
	b := start - n
	if b < 0 {
		b = 0
	}
	for b < start && !utf8.RuneStart(s[b]) {
		b++
	}
	a := end + n
	if a > len(s) {
		a = len(s)
	}
	for a > end && a < len(s) && !utf8.RuneStart(s[a]) {
		a--
	}
	return s[b:start], s[end:a]
}

// nextRevToken finds the earliest structural token at or after from:
// paragraph closes, <w:ins>/<w:del> open and close tags, and text runs.
func nextRevToken(xml string, from int) (int, revToken) {
	best, kind := -1, tokNone
	consider := func(pos int, k revToken) {
		if pos >= 0 && (best < 0 || pos < best) {
			best, kind = pos, k
		}
	}
	consider(indexFrom(xml, from, "</w:p>"), tokParaClose)
	consider(indexFrom(xml, from, "</w:ins>"), tokInsClose)
	consider(indexFrom(xml, from, "</w:del>"), tokDelClose)
	consider(findOpenTag(xml, from, "<w:ins"), tokInsOpen)
	consider(findOpenTag(xml, from, "<w:del"), tokDelOpen)
	consider(nextRunOpen(xml, from), tokRunOpen)
	return best, kind
}

func indexFrom(xml string, from int, sub string) int {
	j := strings.Index(xml[from:], sub)
	if j < 0 {
		return -1
	}
	return from + j
}

// findOpenTag finds the next occurrence of tag that is a genuine open tag —
// followed by an attribute space, '>', or a self-close — rejecting longer
// element names sharing the prefix (<w:delText>, <w:instrText>).
func findOpenTag(xml string, from int, tag string) int {
	for i := from; ; {
		j := strings.Index(xml[i:], tag)
		if j < 0 {
			return -1
		}
		j += i
		k := j + len(tag)
		if k < len(xml) && (xml[k] == ' ' || xml[k] == '>' || xml[k] == '/') {
			return j
		}
		i = j + 1
	}
}

// taggedRunText concatenates and decodes the content of every element with
// the given open-tag prefix (e.g. "<w:t" or "<w:delText") in one run's XML.
// Mirrors runText but parameterised over the element name so deleted text
// (<w:delText>) can be read with the same tolerant tag handling.
func taggedRunText(runXML, open string) string {
	closeTag := "</" + open[1:] + ">"
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(runXML[i:], open)
		if j < 0 {
			break
		}
		j += i
		k := j + len(open)
		if k >= len(runXML) {
			break
		}
		switch runXML[k] {
		case '>': // <tag>…</tag>
			e := strings.Index(runXML[k+1:], closeTag)
			if e < 0 {
				return b.String()
			}
			b.WriteString(Unescape(runXML[k+1 : k+1+e]))
			i = k + 1 + e + len(closeTag)
		case ' ': // <tag attrs>…</tag> or <tag attrs/>
			c := strings.Index(runXML[k:], ">")
			if c < 0 {
				return b.String()
			}
			c += k
			if runXML[c-1] == '/' {
				i = c + 1
				continue
			}
			e := strings.Index(runXML[c+1:], closeTag)
			if e < 0 {
				return b.String()
			}
			b.WriteString(Unescape(runXML[c+1 : c+1+e]))
			i = c + 1 + e + len(closeTag)
		case '/': // <tag/>
			i = k + 2
		default: // a longer element name sharing the prefix
			i = k
		}
	}
	return b.String()
}

// attrVal extracts and decodes a double-quoted attribute value from an open
// tag, or "" when absent.
func attrVal(tag, name string) string {
	key := name + `="`
	i := strings.Index(tag, key)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return Unescape(rest[:j])
}
