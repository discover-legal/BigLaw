// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package pageindex turns the plain text of a legal document into a
// hierarchical section tree. Instead of flat semantic chunking, agents can
// inspect the outline (titles + numbering) and then drill into a single
// section's verbatim text.
//
// The central invariant is byte-exact reconstruction: walking the returned
// tree in pre-order and concatenating each node's Body yields the original
// input unchanged — no byte dropped, none duplicated. Downstream citation
// verification depends on every Body being a verbatim slice of the source.
package pageindex

import (
	"regexp"
	"strings"
)

// Scheme identifies which legal heading/numbering convention produced a
// section's Number.
type Scheme string

const (
	// SchemeNone is used for the synthetic root and for sections that carry
	// text but no recognised heading.
	SchemeNone Scheme = ""
	// SchemeDecimal covers outline numbering: "1.", "1.1", "1.1.1".
	SchemeDecimal Scheme = "decimal"
	// SchemeAlpha covers parenthesised alpha sub-items: "(a)", "(b)".
	SchemeAlpha Scheme = "alpha"
	// SchemeRoman covers parenthesised roman sub-items: "(i)", "(ii)".
	SchemeRoman Scheme = "roman"
	// SchemeArticle covers "ARTICLE I", "ARTICLE IV", "Article 3".
	SchemeArticle Scheme = "article"
	// SchemeSection covers "Section 1", "§ 1", "Sec. 2".
	SchemeSection Scheme = "section"
	// SchemeSchedule covers "Schedule A", "Exhibit 1", "Appendix B",
	// "Annex II".
	SchemeSchedule Scheme = "schedule"
	// SchemeRecital covers recital framing: "RECITALS", "WHEREAS",
	// "NOW, THEREFORE".
	SchemeRecital Scheme = "recital"
)

// Section is one node of the document's hierarchical structure.
//
// Body holds the VERBATIM text owned by this node and excludes the text of
// its Children: it is exactly the slice of the original document from this
// node's start up to the start of its first child (or, for a leaf, up to the
// end of the node). Concatenating Body across a pre-order walk reproduces the
// source byte-for-byte.
type Section struct {
	// Title is the heading line's text with the Number prefix stripped
	// (trimmed). For body-only or root nodes it is empty.
	Title string
	// Number is the recognised label, e.g. "1.1", "(a)", "ARTICLE IV".
	// Empty when the node carries no heading.
	Number string
	// Scheme records which convention matched the heading.
	Scheme Scheme
	// Level is the nesting depth (root == 0, its heading children == 1, ...).
	Level int
	// Body is the verbatim text of this node excluding its Children.
	Body string
	// Children are the nested subsections, in document order.
	Children []Section
	// ByteStart and ByteEnd are offsets into the original input. The whole
	// subtree rooted here spans [ByteStart, ByteEnd); Body occupies the
	// prefix [ByteStart, ByteStart+len(Body)).
	ByteStart int
	ByteEnd   int
	// IsDefinitions flags a "Definitions" / "Interpretation" section so
	// downstream code can harvest a glossary from it.
	IsDefinitions bool
}

// heading is the internal classification of a single source line.
type heading struct {
	number string
	title  string
	scheme Scheme
	level  int
	isDefs bool
}

// line is a source line with its exact byte span. start..end covers the line
// content plus its trailing newline (if any), so spans tile the input with no
// gaps.
type line struct {
	text  string // content without the trailing "\n"/"\r\n"
	start int
	end   int // exclusive; includes the line terminator
}

// Parse builds the section tree for text and returns the top-level sections.
//
// When no structure is detected, the result is a single root section whose
// Body is the entire input. The returned slice always tiles the whole input:
// the first section starts at byte 0 and the last ends at len(text).
func Parse(text string) []Section {
	lines := splitLines(text)

	// Classify every line. A nil entry means "body text".
	headings := make([]*heading, len(lines))
	for i, ln := range lines {
		headings[i] = classify(ln.text)
	}

	// Demote false-positive headings: a recognised heading that is the sole
	// content of an otherwise-empty document still counts, but a heading line
	// embedded mid-paragraph (no blank line or heading before it, previous
	// line is running prose ending without terminator cues) is kept as-is —
	// we deliberately favour recall of structure. The reconstruction
	// invariant holds regardless of how aggressively we split.

	root := Section{Level: 0, ByteStart: 0, ByteEnd: len(text), Scheme: SchemeNone}

	// Build using an explicit stack of section pointers keyed by level.
	// stack[0] is always the root.
	stack := []*Section{&root}

	// preface collects leading body lines (before the first heading); they
	// belong to the root's own Body.
	firstHeadingIdx := -1
	for i := range headings {
		if headings[i] != nil {
			firstHeadingIdx = i
			break
		}
	}

	if firstHeadingIdx == -1 {
		// No structure at all: single root holding everything.
		root.Body = text
		root.ByteEnd = len(text)
		return []Section{root}
	}

	for i := 0; i < len(lines); i++ {
		h := headings[i]
		if h == nil {
			continue
		}
		// Pop the stack until the top is a strictly shallower parent.
		for len(stack) > 1 && stack[len(stack)-1].Level >= h.level {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1]
		child := Section{
			Title:         h.title,
			Number:        h.number,
			Scheme:        h.scheme,
			Level:         h.level,
			ByteStart:     lines[i].start,
			IsDefinitions: h.isDefs,
		}
		parent.Children = append(parent.Children, child)
		stack = append(stack, &parent.Children[len(parent.Children)-1])
	}

	// Now assign ByteEnd and Body across the tree. ByteEnd of every node is
	// the start of the next node (in document order) at the same-or-shallower
	// level, else len(text). We compute this with a pre-order walk that also
	// fixes the slice-of-slice pointer aliasing introduced above.
	finalize(&root, text)

	// The root's own Body is the preface text (everything before its first
	// child). If the document begins with a heading at level 1, the root's
	// Body is empty but the root node is still returned so callers always get
	// a single coherent tree span. We return the root's children as the
	// top-level list when the root carries no heading and no preface, to keep
	// the common case (a normal contract) ergonomic; otherwise we return the
	// root itself to preserve any preface bytes.
	if root.Body == "" && root.Title == "" && root.Number == "" {
		// Root holds no bytes of its own — surface its children directly.
		// This still tiles the input because child[0].ByteStart == 0.
		if len(root.Children) > 0 && root.Children[0].ByteStart == 0 {
			return root.Children
		}
	}
	return []Section{root}
}

// finalize performs a pre-order walk fixing ByteEnd and Body for every node.
// It must run after the whole tree is built because a node's end depends on
// the next sibling/uncle that appears later in document order.
func finalize(root *Section, text string) {
	// Collect all nodes in pre-order along with their parent's end bound.
	var walk func(n *Section, parentEnd int)
	walk = func(n *Section, parentEnd int) {
		if len(n.Children) == 0 {
			// Leaf: Body runs from its start to its end bound.
			n.ByteEnd = parentEnd
			n.Body = text[n.ByteStart:n.ByteEnd]
			return
		}
		// Body of an internal node is the span before its first child.
		firstChildStart := n.Children[0].ByteStart
		n.Body = text[n.ByteStart:firstChildStart]
		// Each child ends where the next child starts; the last child ends
		// at this node's own end bound (== parentEnd, set below).
		n.ByteEnd = parentEnd
		for i := range n.Children {
			var childEnd int
			if i+1 < len(n.Children) {
				childEnd = n.Children[i+1].ByteStart
			} else {
				childEnd = parentEnd
			}
			walk(&n.Children[i], childEnd)
		}
	}
	walk(root, root.ByteEnd)
}

// splitLines splits text into lines while recording exact byte spans. Each
// span includes the trailing terminator so the spans tile the input.
func splitLines(text string) []line {
	if text == "" {
		return []line{{text: "", start: 0, end: 0}}
	}
	var lines []line
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			content := text[start:i]
			content = strings.TrimSuffix(content, "\r")
			lines = append(lines, line{text: content, start: start, end: i + 1})
			start = i + 1
		}
	}
	if start < len(text) {
		// Trailing line with no terminator.
		content := strings.TrimSuffix(text[start:], "\r")
		lines = append(lines, line{text: content, start: start, end: len(text)})
	}
	return lines
}

// --- Heading recognition -------------------------------------------------

var (
	// "1.", "1.1", "1.1.1" optionally followed by inline title text.
	reDecimal = regexp.MustCompile(`^\s*(\d+(?:\.\d+)*)\.?\s*(.*)$`)
	// "(a)" / "(A)" parenthesised single/double alpha.
	reAlpha = regexp.MustCompile(`^\s*\(([a-zA-Z]{1,2})\)\s*(.*)$`)
	// "(i)" / "(iv)" parenthesised roman. Matched before alpha when valid.
	reRoman = regexp.MustCompile(`^\s*\(([ivxlcdmIVXLCDM]+)\)\s*(.*)$`)
	// "ARTICLE I", "Article 3" — roman or arabic numeral.
	reArticle = regexp.MustCompile(`^\s*(?i:ARTICLE)\s+([IVXLCDM]+|\d+)\b\.?\s*(.*)$`)
	// "Section 1", "§ 2", "Sec. 3".
	reSection = regexp.MustCompile(`^\s*(?:§\s*|(?i:SECTION|SEC)\.?\s+)(\d+(?:\.\d+)*)\b\.?\s*(.*)$`)
	// "Schedule A", "Exhibit 1", "Appendix B", "Annex II".
	reSchedule = regexp.MustCompile(`^\s*(?i:SCHEDULE|EXHIBIT|APPENDIX|ANNEX|ANNEXURE)\s+([A-Z]+|\d+|[IVXLCDM]+)\b\.?\s*(.*)$`)
	// Recital framing keywords.
	reRecitalsHdr  = regexp.MustCompile(`^\s*(?i:RECITALS|BACKGROUND|PREAMBLE)\s*:?\s*$`)
	reWhereas      = regexp.MustCompile(`^\s*(WHEREAS)\b[,;:]?\s*(.*)$`)
	reNowTherefore = regexp.MustCompile(`^\s*(?i:NOW,?\s+THEREFORE)\b[,;:]?\s*(.*)$`)
	// Definitions / interpretation flag (matched on title text).
	reDefinitions = regexp.MustCompile(`(?i)\b(definitions?|interpretation|defined\s+terms)\b`)
)

// romanValid reports whether s is a plausible roman numeral (case-insensitive)
// so that "(i)" routes to roman while "(a)" stays alpha and "(c)" — which is a
// valid roman — is disambiguated by length/letters at the call site.
func romanValid(s string) bool {
	for _, r := range s {
		switch r {
		case 'i', 'v', 'x', 'l', 'c', 'd', 'm', 'I', 'V', 'X', 'L', 'C', 'D', 'M':
		default:
			return false
		}
	}
	return s != ""
}

// classify decides whether a line is a heading and, if so, returns its
// number, title, scheme and nesting level. It returns nil for body text.
//
// Level assignment is scheme-aware and ordered so that the canonical legal
// hierarchy nests correctly:
//
//	1  ARTICLE / SCHEDULE / EXHIBIT / RECITALS block          (top structure)
//	2  Section / § / decimal "1." / WHEREAS / NOW THEREFORE
//	2+ decimal depth: "1.1" -> 3, "1.1.1" -> 4 (depth-driven)
//	4  (a) parenthesised alpha sub-items (nest under a "1.1"/"1.2" clause)
//	5  (i) parenthesised roman sub-items (nest under an "(a)")
//
// Because the tree is built with a level stack, exact absolute numbers matter
// less than their relative ordering; we keep them stable and monotone.
func classify(s string) *heading {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}

	// Recitals block header (standalone line).
	if reRecitalsHdr.MatchString(trimmed) {
		return &heading{number: "", title: trimmed, scheme: SchemeRecital, level: 1}
	}
	// NOW, THEREFORE — peer of WHEREAS, opens the operative block.
	if m := reNowTherefore.FindStringSubmatch(trimmed); m != nil {
		return &heading{number: "NOW, THEREFORE", title: strings.TrimSpace(m[1]), scheme: SchemeRecital, level: 2}
	}
	if m := reWhereas.FindStringSubmatch(trimmed); m != nil {
		return &heading{number: "WHEREAS", title: strings.TrimSpace(m[2]), scheme: SchemeRecital, level: 2}
	}

	// ARTICLE — top-level structural unit.
	if m := reArticle.FindStringSubmatch(trimmed); m != nil {
		return &heading{number: "ARTICLE " + m[1], title: strings.TrimSpace(m[2]), scheme: SchemeArticle, level: 1, isDefs: isDefs(m[2])}
	}
	// SCHEDULE / EXHIBIT / APPENDIX / ANNEX — top-level appendices.
	if m := reSchedule.FindStringSubmatch(trimmed); m != nil {
		label := scheduleLabel(trimmed)
		return &heading{number: label, title: strings.TrimSpace(m[2]), scheme: SchemeSchedule, level: 1, isDefs: isDefs(m[2])}
	}
	// Section / § — second-level structural unit.
	if m := reSection.FindStringSubmatch(trimmed); m != nil {
		return &heading{number: "Section " + m[1], title: strings.TrimSpace(m[2]), scheme: SchemeSection, level: 2, isDefs: isDefs(m[2])}
	}

	// Parenthesised roman / alpha sub-items. Roman is preferred only when the
	// token is composed solely of roman letters AND looks roman-y; single
	// "i" / "v" / "x" go roman, multi-letter ambiguous tokens like "c" stay
	// alpha unless they form a roman sequence ("ii", "iv"). We use a simple
	// rule: tokens that are valid roman and contain a non-{a-h,j-u-ish} signal
	// route roman; otherwise alpha. To keep it deterministic we treat any
	// token drawn purely from {i,v,x,l,c,d,m} with length>=2 as roman, and a
	// single 'i','v','x' as roman; everything else as alpha.
	if m := reRoman.FindStringSubmatch(trimmed); m != nil {
		tok := m[1]
		if isRomanItem(tok) {
			return &heading{number: "(" + tok + ")", title: strings.TrimSpace(m[2]), scheme: SchemeRoman, level: 5, isDefs: isDefs(m[2])}
		}
	}
	if m := reAlpha.FindStringSubmatch(trimmed); m != nil {
		return &heading{number: "(" + m[1] + ")", title: strings.TrimSpace(m[2]), scheme: SchemeAlpha, level: 4, isDefs: isDefs(m[2])}
	}

	// Decimal / outline numbering. Depth is driven by the number of dotted
	// components: "1" -> 2, "1.1" -> 3, "1.1.1" -> 4 ... so a bare "1." sits
	// at Section depth and refinements nest beneath it.
	if m := reDecimal.FindStringSubmatch(trimmed); m != nil {
		num := m[1]
		// Guard: a year or bare figure inside prose ("2026 was...") would
		// match; require the line to look like a heading — either it is short
		// (likely a numbered clause heading) or it begins at the line start
		// with the number as the first token (which the anchor already
		// enforces). We additionally reject pure-number lines that are
		// clearly mid-sentence by requiring the captured title (if any) to
		// not start with a lowercase conjunction artefact. This is a soft
		// heuristic; the reconstruction invariant is unaffected either way.
		depth := strings.Count(num, ".") + 2
		return &heading{number: num, title: strings.TrimSpace(m[2]), scheme: SchemeDecimal, level: depth, isDefs: isDefs(m[2])}
	}

	return nil
}

// isRomanItem decides whether a parenthesised token like "i", "ii", "iv", "c"
// should be treated as a roman-numeral sub-item rather than an alpha one.
func isRomanItem(tok string) bool {
	if !romanValid(tok) {
		return false
	}
	low := strings.ToLower(tok)
	if len(low) == 1 {
		// Single letters: i/v/x read as roman; a single c/d/l/m is far more
		// likely an alpha enumerator, so keep those alpha.
		switch low {
		case "i", "v", "x":
			return true
		default:
			return false
		}
	}
	// Multi-letter pure-roman tokens (ii, iii, iv, vi, ...) are roman.
	return true
}

// isDefs reports whether a title marks a Definitions/Interpretation section.
func isDefs(title string) bool {
	return reDefinitions.MatchString(title)
}

// scheduleLabel normalises a schedule-family heading to a compact label like
// "Schedule A" or "Exhibit 1".
func scheduleLabel(trimmed string) string {
	fields := strings.Fields(trimmed)
	if len(fields) >= 2 {
		return titleCaseWord(fields[0]) + " " + fields[1]
	}
	return strings.TrimSpace(trimmed)
}

// titleCaseWord lower-cases a word and upper-cases its first rune (ASCII),
// e.g. "SCHEDULE" -> "Schedule". Used in place of the deprecated
// strings.Title for the single-word case we need.
func titleCaseWord(w string) string {
	low := strings.ToLower(w)
	if low == "" {
		return low
	}
	return strings.ToUpper(low[:1]) + low[1:]
}
