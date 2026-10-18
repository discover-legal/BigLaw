// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"regexp"
	"strings"
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

// Parse builds the section tree of text. A document with no recognised heading is one level-0
// section; text before the first heading is a level-0 preface section.
func Parse(text string) []Section {
	var secs []Section
	for start := 0; start < len(text); {
		end := len(text)
		if i := strings.IndexByte(text[start:], '\n'); i >= 0 {
			end = start + i + 1
		}
		if s, ok := classify(strings.TrimSuffix(strings.TrimSuffix(text[start:end], "\n"), "\r")); ok {
			if len(secs) == 0 && start > 0 {
				secs = append(secs, Section{BodyEnd: start, End: start}) // the preface
			}
			s.Start = start
			secs = append(secs, s)
		}
		start = end
	}
	if len(secs) == 0 {
		return []Section{{BodyEnd: len(text), End: len(text)}}
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
	return secs
}

var (
	reMarkdown     = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*$`)
	reDecimal      = regexp.MustCompile(`^(\d{1,3}(?:\.\d+)*)(?:\.|\s|$)\s*(.*)$`) // ≤ 3 digits: not a year or a figure
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
func classify(line string) (Section, bool) {
	s := strings.TrimSpace(line)
	md := reMarkdown.FindStringSubmatch(s)
	if md != nil {
		s = md[2]
	}
	s = strings.TrimSpace(strings.Trim(s, "*_")) // **1.2 Bold heading**
	sec := func(scheme, number, title string, level int) (Section, bool) {
		title = strings.Trim(strings.NewReplacer("**", "", "__", "").Replace(title), " \t*_—–-:.")
		return Section{Number: number, Title: title, Scheme: scheme, Level: level, Defs: reDefinitions.MatchString(title)}, true
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
	} else if m := reDecimal.FindStringSubmatch(s); m != nil {
		return sec("decimal", m[1], m[2], strings.Count(m[1], ".")+2)
	} else if md != nil && s != "" {
		return sec("heading", "", s, len(md[1]))
	}
	return Section{}, false
}

// isRomanItem: "(i)", "(v)", "(x)" and multi-letter roman tokens ("ii", "iv") are roman; a single
// c, d, l or m is far more likely an alpha enumerator.
func isRomanItem(tok string) bool {
	if len(tok) == 1 {
		return strings.ContainsAny(tok, "ivxIVX")
	}
	return true // reRoman already limits the letters
}
