// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"regexp"
	"strings"
	"unicode"
)

// Locators: a rule, section or clause number is where a figure is said, not a figure (G8 traces
// quantities and dates).

var (
	// refCite is a rule, section, paragraph or exhibit number ("Rule 6(a)(1)(C)", "Section 4.3",
	// "Fed. R. Civ. P. 7.1", "§ 9.2"): a locator, not a figure (G8 traces quantities and dates). At
	// most four digits a part, so no amount hides behind "Rule 214340". refList continues it over a
	// list or range (MaskLocators).
	refCite = regexp.MustCompile(`(?i)(\b(?:rules?|sections?|secs?\.|articles?|arts?\.|paragraphs?|paras?\.|clauses?|schedules?|exhibits?)\s+|§+\s*|¶+\s*|\bR\.\s*(?:Civ|Crim|App|Evid|Bankr)\.\s*P\.\s*)` + locator)
	// refList is one more locator of a list ("Sections 7.3 and 9.1", "paragraphs 12, 14 and 17") or
	// the end of a range ("§§ 4.2–4.5", "Section 4.2-4.5"); refRange is the range form alone.
	refList = regexp.MustCompile(`^(\s*(?:,|;|&|\band\b|\bor\b)\s*(?:and\s+|or\s+)?|\s*[–—-]\s*)(` + locator + `)`)
	// listWord is a word that may follow a list's bare-number item ("paragraphs 12, 14 and 17 are
	// …", "Sections 7 and 9 of the Manual"); any other word makes the number a count ("37 trades").
	listWord = regexp.MustCompile(`(?i)^\s+(?:and|or|of|to|through|in|under|is|are|was|were|be|do|does|did|not|also|each|both|which|that|as|may|must|shall|apply|applies|provide|provides|require|requires|state|states|set|sets)\b`)
	refRange = regexp.MustCompile(`^\s*[–—-]\s*` + locator)
)

// locator is one rule, section or clause number: "7.3", "206(4)", "3(b)".
const locator = `\d{1,4}(?:\.\d{1,4})*(?:(?:\([0-9A-Za-z]{1,4}\))+|\b)`

// MaskLocators is text as G8 reads it (internal/engine lint): its locators blanked byte for byte, so
// offsets hold. It blanks the locators in text (refCite), each with the rest of its list or range: a plural
// head ("Sections", "§§", "clauses") continues over ", 9.1", "and 3(b)", "or 17" and "–4.5"; a
// singular one only over a range. The list ends at its last locator item (listItem): a number that
// goes on as a figure ("2,000", "2.5%"), is followed by a noun or unit ("37 trades"), or comes after
// a "," or ";" once an "and"/"or"/"&" has closed the list ("paragraphs 12 and 14, 37 trades") is no
// item, so it stays a figure.
func MaskLocators(text string) string {
	b := []byte(text)
	for _, loc := range refCite.FindAllStringSubmatchIndex(text, -1) {
		head := strings.ToLower(strings.TrimSpace(text[loc[2]:loc[3]]))
		plural := strings.HasSuffix(head, "s") || strings.HasSuffix(head, "s.") || strings.HasPrefix(head, "§§") || strings.HasPrefix(head, "¶¶")
		end, closed := loc[1], false
		for {
			var m []int
			if plural {
				m = refList.FindStringSubmatchIndex(text[end:])
			} else if r := refRange.FindStringIndex(text[end:]); r != nil {
				m = []int{0, r[1], 0, 0, 0, r[1]} // a range's end is its own item
			}
			if m == nil || !listItem(text, end, m, closed) {
				break
			}
			sep := strings.ToLower(text[end+m[2] : end+m[3]])
			closed = closed || strings.Contains(sep, "and") || strings.Contains(sep, "or") || strings.Contains(sep, "&")
			end += m[1]
		}
		if figureAfter(text, loc[1]) { // "Section 2,000": not a locator at all
			continue
		}
		for i := loc[0]; i < end; i++ {
			b[i] = ' '
		}
	}
	return string(b)
}

// listItem reports whether refList's match m at text[at:] is one more locator of the list: not a
// figure's head, not after list-ending punctuation (a "," or ";" once the list is closed), and either
// locator-shaped itself ("9.1", "206(2)") or followed by punctuation, the text's end or a listWord.
func listItem(text string, at int, m []int, closed bool) bool {
	i := at + m[1]
	if figureAfter(text, i) {
		return false
	}
	if sep := strings.TrimSpace(text[at+m[2] : at+m[3]]); closed && (strings.HasPrefix(sep, ",") || strings.HasPrefix(sep, ";")) {
		return false
	}
	if strings.ContainsAny(text[at+m[4]:at+m[5]], ".(") {
		return true
	}
	rest := text[i:]
	if t := strings.TrimLeft(rest, " \t"); t == "" || !unicode.IsLetter([]rune(t)[0]) {
		return true // punctuation, a citation or the end
	}
	return listWord.MatchString(rest)
}

// figureAfter reports whether a number ending at i goes on as a figure: a separator then a digit.
func figureAfter(text string, i int) bool {
	return i+1 < len(text) && (text[i] == ',' || text[i] == '.') && text[i+1] >= '0' && text[i+1] <= '9'
}
