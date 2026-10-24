// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Section is one node of a document's section tree, from its own numbering (ported from v1
// pageindex). Sections come from Parse as a flat pre-order list. The node spans [Start, End) with
// its subsections; its own text is [Start, BodyEnd), up to its first subsection. The invariant
// citation checks rely on: joining text[Start:BodyEnd] over the list gives back the input, byte
// for byte, so every section is a verbatim slice.
type Section struct {
	Number, Title       string // "1.2", "(a)", "ARTICLE IV", "Section 5", "Schedule A"; title without the number
	Scheme              string // decimal alpha roman article section schedule recital heading, or "" (preface/flat)
	Level               int    // 0 for a preface or a flat document; deeper sections have higher levels
	Defs                bool   // a Definitions / Interpretation section
	Start, BodyEnd, End int
}

// Bounds on the tree, so storage stays linear in the input (each chunk carries its section path):
// at most MaxSections sections (later headings stay body text of the last one), a title of at most
// maxTitle bytes (a longer heading line, usually a numbered paragraph, keeps its full text in the
// body), and a number of at most maxNumber bytes and five decimal levels (deeper is body text),
// so a path has at most six levels.
const (
	MaxSections = 10_000
	maxTitle    = 120
	maxNumber   = 24
)

// Parse builds the section tree of text. A document with no recognised heading is one level-0
// section; text before the first heading is a level-0 preface section.
func Parse(text string) []Section { secs, _ := parseTree(text); return secs }

// parseTree is Parse, and whether MaxSections cut the tree (later headings are body text).
func parseTree(text string) (secs []Section, capped bool) {
	for start := 0; start < len(text); {
		end := len(text)
		if i := strings.IndexByte(text[start:], '\n'); i >= 0 {
			end = start + i + 1
		}
		s, ok := classify(strings.TrimSuffix(strings.TrimSuffix(text[start:end], "\n"), "\r"))
		if room := MaxSections - len(secs); ok && (room < 1 || room < 2 && len(secs) == 0 && start > 0) {
			ok, capped = false, true
		}
		if ok {
			if len(secs) == 0 && start > 0 {
				secs = append(secs, Section{BodyEnd: start, End: start}) // the preface
			}
			s.Start = start
			secs = append(secs, s)
		}
		start = end
	}
	if len(secs) == 0 {
		return []Section{{BodyEnd: len(text), End: len(text)}}, false
	}
	for i := range secs {
		if secs[i].Level == 0 {
			continue
		}
		secs[i].BodyEnd, secs[i].End = len(text), len(text)
		if i+1 < len(secs) {
			secs[i].BodyEnd = secs[i+1].Start
		}
		for j := i + 1; j < len(secs); j++ { // the subtree ends at the next heading as shallow or shallower
			if secs[j].Level <= secs[i].Level {
				secs[i].End = secs[j].Start
				break
			}
		}
	}
	return secs, capped
}

var (
	reMarkdown     = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*$`)
	reDecimal      = regexp.MustCompile(`^(\d{1,3}(?:\.\d+)*)(?:\.|\s|$)\s*(.*)$`) // ≤ 3 digits: not a year; a lower-case title is a figure
	reAlpha        = regexp.MustCompile(`^\(([a-zA-Z]{1,2})\)\s*(.*)$`)
	reRoman        = regexp.MustCompile(`^\(([ivxlcdmIVXLCDM]+)\)\s*(.*)$`)
	reArticle      = regexp.MustCompile(`^(?i:ARTICLE)\s+([IVXLCDM]+|\d+)\b\.?\s*(.*)$`)
	reSection      = regexp.MustCompile(`^(?:§\s*|(?i:SECTION|SEC)\.?\s+)(\d+(?:\.\d+)*)\b\.?\s*(.*)$`)
	reSchedule     = regexp.MustCompile(`^(?i:(SCHEDULE|EXHIBIT|APPENDIX|ANNEX|ANNEXURE))\s+([A-Z]+|\d+|[IVXLCDM]+)\b\.?\s*(.*)$`)
	reRecitals     = regexp.MustCompile(`^(?i:RECITALS|BACKGROUND|PREAMBLE)\s*:?$`)
	reWhereas      = regexp.MustCompile(`^WHEREAS\b[,;:]?\s*(.*)$`)
	reNowTherefore = regexp.MustCompile(`^(?i:NOW,?\s+THEREFORE)\b[,;:]?\s*(.*)$`)
	reDefinitions  = regexp.MustCompile(`(?i)\b(definitions?|interpretation|defined\s+terms)\b`)
)

// classify reports whether a line is a heading. Levels keep the legal hierarchy in order:
// ARTICLE/SCHEDULE/RECITALS 1, Section/§/WHEREAS 2, decimal "1" 2, "1.1" 3, …, (a) 4, (i) 5.
// A Markdown "#" heading is classified by its text; an unnumbered one is a heading at its depth.
// A line with two or more tabs is a table row, not a heading.
func classify(line string) (Section, bool) {
	if isTableRow(line) {
		return Section{}, false // a table row ("1\t2023\t$400") is never a heading
	}
	s := strings.TrimSpace(line)
	md := reMarkdown.FindStringSubmatch(s)
	if md != nil {
		s = md[2]
	}
	s = strings.TrimSpace(strings.Trim(s, "*_")) // **1.2 Bold heading**
	sec := func(scheme, number, title string, level int) (Section, bool) {
		raw := strings.TrimLeft(strings.NewReplacer("**", "", "__", "").Replace(title), " \t*_") // the title as it follows the number
		title = strings.Map(func(r rune) rune {                                                  // no bidi overrides or other invisible or control characters in a title
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, strings.Trim(strings.NewReplacer("**", "", "__", "").Replace(title), " \t*_—–-:."))
		if len(number) > maxNumber || scheme == "decimal" && (level > 6 || startsLower(title)) ||
			(scheme == "article" || scheme == "section" || scheme == "schedule") && (startsLower(raw) || startsPunct(raw)) {
			return Section{}, false // "1.2.3.4.5.6", "100 dollars were paid", "1.5 million"; a wrapped cross-reference: "Article 4 of the Trust, …", "Section 9, …" (lab-r3 r4 H2)
		}
		return Section{Number: number, Title: clip(title, maxTitle), Scheme: scheme, Level: level, Defs: reDefinitions.MatchString(clip(title, maxTitle))}, true
	}
	if m := reRecitals.FindStringSubmatch(s); m != nil {
		return sec("recital", "", s, 1)
	} else if m := reNowTherefore.FindStringSubmatch(s); m != nil {
		return sec("recital", "NOW, THEREFORE", "", 2)
	} else if m := reWhereas.FindStringSubmatch(s); m != nil {
		return sec("recital", "WHEREAS", m[1], 2)
	} else if m := reArticle.FindStringSubmatch(s); m != nil {
		return sec("article", "ARTICLE "+m[1], m[2], 1)
	} else if m := reSchedule.FindStringSubmatch(s); m != nil {
		return sec("schedule", strings.ToUpper(m[1][:1])+strings.ToLower(m[1][1:])+" "+m[2], m[3], 1)
	} else if m := reSection.FindStringSubmatch(s); m != nil {
		return sec("section", "Section "+m[1], m[2], 2)
	} else if m := reRoman.FindStringSubmatch(s); m != nil && isRomanItem(m[1]) {
		return sec("roman", "("+m[1]+")", m[2], 5)
	} else if m := reAlpha.FindStringSubmatch(s); m != nil {
		return sec("alpha", "("+m[1]+")", m[2], 4)
	} else if m := reDecimal.FindStringSubmatch(s); m != nil && !(len(s) > len(m[1]) && s[len(m[1])] != '.' && !strings.Contains(m[1], ".") && address(m[2])) {
		return sec("decimal", m[1], m[2], strings.Count(m[1], ".")+2)
	} else if md != nil && s != "" {
		return sec("heading", "", s, len(md[1]))
	}
	return Section{}, false
}

func startsLower(s string) bool { r, _ := utf8.DecodeRuneInString(s); return unicode.IsLower(r) }

// startsPunct: a title that runs on from its number with punctuation (", name …"), not a separator
// ("—", ":", ".") or an opening bracket or quote.
func startsPunct(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsPunct(r) && !strings.ContainsRune("([\"“‘'—–-:.", r)
}

// reStreet is a street word: "12 Elm Street, …" after a bare number is an address on a wrapped line,
// not a heading (lab-r3 r4 H2).
var reStreet = regexp.MustCompile(`^(?:\S+\s+){1,3}(?i:street|st|avenue|ave|road|rd|lane|ln|drive|dr|boulevard|blvd|way|place|pl|court|ct|terrace|highway|hwy|parkway|square|suite|floor)\b\.?(?:,|\s|$)`)

func address(title string) bool { return reStreet.MatchString(title) }

// isRomanItem: "(i)", "(v)", "(x)" and multi-letter roman tokens ("ii", "iv") are roman; a single
// c, d, l or m is far more likely an alpha enumerator.
func isRomanItem(tok string) bool {
	if len(tok) == 1 {
		return strings.ContainsAny(tok, "ivxIVX")
	}
	return true // reRoman already limits the letters
}
