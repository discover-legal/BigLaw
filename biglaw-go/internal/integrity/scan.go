// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package integrity answers one question about an inbound document: can you
// trust it? Two detectors share the package. ScanText finds Unicode
// obfuscation — homoglyph substitutions (Cyrillic/Greek/fullwidth lookalikes
// inside Latin words), zero-width and invisible characters, bidi control
// characters, and mixed-script words — crafted to make AI extraction or human
// review misread a document. CompareVersions (compare.go) finds unmarked
// changes: edits in a returned draft that are NOT accounted for by its
// tracked changes.

package integrity

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Severity grades a finding.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// FindingKind classifies an obfuscation finding.
type FindingKind string

const (
	KindHomoglyph       FindingKind = "homoglyph"
	KindInvisibleChar   FindingKind = "invisible_char"
	KindBidiControl     FindingKind = "bidi_control"
	KindMixedScriptWord FindingKind = "mixed_script_word"
)

// Finding is one aggregated obfuscation observation. Repeats of the same kind
// and character are folded into a single finding with Count; Offset, Sample,
// and Detail describe the first occurrence. Offset is a byte offset into the
// scanned text.
type Finding struct {
	Kind     FindingKind `json:"kind"`
	Severity Severity    `json:"severity"`
	Offset   int         `json:"offset"`
	Sample   string      `json:"sample"`
	Detail   string      `json:"detail"`
	Count    int         `json:"count"`
}

// sampleContextChars is how much surrounding text a finding's Sample carries
// on each side of the affected character or word.
const sampleContextChars = 20

// ─── Character tables ─────────────────────────────────────────────────────────

// invisibleNames: zero-width and invisible characters that can split words,
// break exact-match verification, or hide content from review.
var invisibleNames = map[rune]string{
	'\u200B': "zero-width space",
	'\u200C': "zero-width non-joiner",
	'\u200D': "zero-width joiner",
	'\u2060': "word joiner",
	'\uFEFF': "zero-width no-break space (BOM)",
	'\u00AD': "soft hyphen",
}

// bidiNames: directional formatting characters that can visually reorder
// text (the classic RLO filename/amount attack).
var bidiNames = map[rune]string{
	'\u202A': "left-to-right embedding (LRE)",
	'\u202B': "right-to-left embedding (RLE)",
	'\u202C': "pop directional formatting (PDF)",
	'\u202D': "left-to-right override (LRO)",
	'\u202E': "right-to-left override (RLO)",
	'\u2066': "left-to-right isolate (LRI)",
	'\u2067': "right-to-left isolate (RLI)",
	'\u2068': "first strong isolate (FSI)",
	'\u2069': "pop directional isolate (PDI)",
}

// confusable maps a non-Latin (or fullwidth) rune to the Latin character it
// impersonates. This is a curated table of the common Cyrillic and Greek
// lookalikes plus the fullwidth forms — not the full Unicode confusables
// database, which would be overkill for catching adversarial legal drafting.
type confusable struct {
	latin  string
	script string
}

var confusables = map[rune]confusable{
	// Cyrillic lowercase
	'а': {"a", "Cyrillic"}, 'е': {"e", "Cyrillic"}, 'о': {"o", "Cyrillic"},
	'р': {"p", "Cyrillic"}, 'с': {"c", "Cyrillic"}, 'у': {"y", "Cyrillic"},
	'х': {"x", "Cyrillic"}, 'і': {"i", "Cyrillic"}, 'ѕ': {"s", "Cyrillic"},
	'ј': {"j", "Cyrillic"}, 'ԛ': {"q", "Cyrillic"}, 'ԝ': {"w", "Cyrillic"},
	'һ': {"h", "Cyrillic"}, 'ԁ': {"d", "Cyrillic"}, 'ѵ': {"v", "Cyrillic"},
	// Cyrillic uppercase
	'А': {"A", "Cyrillic"}, 'В': {"B", "Cyrillic"}, 'Е': {"E", "Cyrillic"},
	'К': {"K", "Cyrillic"}, 'М': {"M", "Cyrillic"}, 'Н': {"H", "Cyrillic"},
	'О': {"O", "Cyrillic"}, 'Р': {"P", "Cyrillic"}, 'С': {"C", "Cyrillic"},
	'Т': {"T", "Cyrillic"}, 'У': {"Y", "Cyrillic"}, 'Х': {"X", "Cyrillic"},
	'Ѕ': {"S", "Cyrillic"}, 'І': {"I", "Cyrillic"}, 'Ј': {"J", "Cyrillic"},
	// Greek lowercase
	'ο': {"o", "Greek"}, 'ν': {"v", "Greek"}, 'ρ': {"p", "Greek"},
	'ι': {"i", "Greek"}, 'κ': {"k", "Greek"}, 'υ': {"u", "Greek"},
	'χ': {"x", "Greek"}, 'α': {"a", "Greek"}, 'ω': {"w", "Greek"},
	// Greek uppercase
	'Α': {"A", "Greek"}, 'Β': {"B", "Greek"}, 'Ε': {"E", "Greek"},
	'Ζ': {"Z", "Greek"}, 'Η': {"H", "Greek"}, 'Ι': {"I", "Greek"},
	'Κ': {"K", "Greek"}, 'Μ': {"M", "Greek"}, 'Ν': {"N", "Greek"},
	'Ο': {"O", "Greek"}, 'Ρ': {"P", "Greek"}, 'Τ': {"T", "Greek"},
	'Υ': {"Y", "Greek"}, 'Χ': {"X", "Greek"},
}

// confusableInfo resolves a rune against the curated table, covering the
// fullwidth Latin/digit block by arithmetic (U+FF01–U+FF5E ↔ ASCII).
func confusableInfo(r rune) (confusable, bool) {
	if c, ok := confusables[r]; ok {
		return c, true
	}
	if r >= 0xFF01 && r <= 0xFF5E {
		return confusable{latin: string(rune(r - 0xFEE0)), script: "fullwidth"}, true
	}
	return confusable{}, false
}

// ─── ScanText ─────────────────────────────────────────────────────────────────

// ScanText scans text for Unicode obfuscation and returns aggregated
// findings sorted by first occurrence. Clean text returns nil.
func ScanText(text string) []Finding {
	type key struct {
		kind FindingKind
		r    rune
	}
	agg := map[key]*Finding{}
	record := func(kind FindingKind, r rune, sev Severity, offset int, sample, detail string) {
		k := key{kind, r}
		if f, ok := agg[k]; ok {
			f.Count++
			if severityRank(sev) > severityRank(f.Severity) {
				f.Severity = sev
			}
			return
		}
		agg[k] = &Finding{Kind: kind, Severity: sev, Offset: offset, Sample: sample, Detail: detail, Count: 1}
	}

	// Pass 1 — character-level: invisibles and bidi controls.
	for i, r := range text {
		if name, ok := invisibleNames[r]; ok {
			record(KindInvisibleChar, r, SeverityWarning, i,
				sampleAround(text, i, i+utf8.RuneLen(r)),
				fmt.Sprintf("%s (U+%04X)", name, r))
		}
		if name, ok := bidiNames[r]; ok {
			record(KindBidiControl, r, SeverityCritical, i,
				sampleAround(text, i, i+utf8.RuneLen(r)),
				fmt.Sprintf("bidirectional control: %s (U+%04X) can visually reorder text", name, r))
		}
	}

	// Pass 2 — word-level: homoglyphs and mixed scripts.
	forEachWord(text, func(word string, start int) {
		hasLatin := false
		type hit struct {
			r   rune
			off int
			c   confusable
		}
		var hits []hit
		scripts := map[string]rune{} // script name → first rune of that script
		for i, r := range word {
			if isBaseLatin(r) {
				hasLatin = true
				scripts["Latin"] = firstOr(scripts["Latin"], r)
			}
			if c, ok := confusableInfo(r); ok {
				hits = append(hits, hit{r: r, off: start + i, c: c})
			}
			switch {
			case unicode.Is(unicode.Cyrillic, r):
				scripts["Cyrillic"] = firstOr(scripts["Cyrillic"], r)
			case unicode.Is(unicode.Greek, r):
				scripts["Greek"] = firstOr(scripts["Greek"], r)
			}
		}

		if len(hits) > 0 && (hasLatin || hits[0].c.script == "fullwidth") {
			// A lookalike sitting inside a Latin word is the classic
			// substitution attack — critical. Fullwidth forms are flagged even
			// standalone (they normalise to Latin and have no honest place in
			// a legal draft) but only as a warning without Latin neighbours.
			sev := SeverityCritical
			if !hasLatin {
				sev = SeverityWarning
			}
			for _, h := range hits {
				record(KindHomoglyph, h.r, sev, h.off,
					sampleAround(text, start, start+len(word)),
					fmt.Sprintf("%s '%c' (U+%04X) in Latin word %q — looks like '%s'",
						h.c.script, h.r, h.r, word, h.c.latin))
			}
			return // don't double-report the same word as mixed-script
		}

		if len(scripts) > 1 {
			// Genuinely mixed scripts without a curated lookalike — could be
			// legitimate (foreign names) but is worth a human glance.
			for name, r := range scripts {
				if name == "Latin" {
					continue
				}
				record(KindMixedScriptWord, r, SeverityWarning, start,
					sampleAround(text, start, start+len(word)),
					fmt.Sprintf("%s '%c' (U+%04X) mixed with Latin in word %q", name, r, r, word))
			}
		}
	})

	if len(agg) == 0 {
		return nil
	}
	out := make([]Finding, 0, len(agg))
	for _, f := range agg {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Offset < out[j].Offset })
	return out
}

// forEachWord calls fn for every maximal run of letters/digits in text with
// its byte offset.
func forEachWord(text string, fn func(word string, start int)) {
	start := -1
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			fn(text[start:i], start)
			start = -1
		}
	}
	if start >= 0 {
		fn(text[start:], start)
	}
}

// isBaseLatin reports whether r is an ordinary Latin letter (ASCII through
// Latin Extended-B) — deliberately excluding the fullwidth block, which is
// Latin-script by Unicode but treated as a lookalike here.
func isBaseLatin(r rune) bool {
	return r <= 0x024F && unicode.IsLetter(r)
}

func firstOr(existing, r rune) rune {
	if existing != 0 {
		return existing
	}
	return r
}

// sampleAround extracts the affected span with ~sampleContextChars of context
// on each side, invisible and bidi characters rendered visibly as <U+XXXX> so
// the sample can be read in a log or report.
func sampleAround(text string, start, end int) string {
	b := start - sampleContextChars
	if b < 0 {
		b = 0
	}
	for b < start && !utf8.RuneStart(text[b]) {
		b++
	}
	a := end + sampleContextChars
	if a > len(text) {
		a = len(text)
	}
	for a > end && a < len(text) && !utf8.RuneStart(text[a]) {
		a--
	}
	var sb strings.Builder
	for _, r := range text[b:a] {
		if _, inv := invisibleNames[r]; inv {
			fmt.Fprintf(&sb, "<U+%04X>", r)
			continue
		}
		if _, bd := bidiNames[r]; bd {
			fmt.Fprintf(&sb, "<U+%04X>", r)
			continue
		}
		sb.WriteRune(r)
	}
	return strings.TrimSpace(sb.String())
}

// ─── Severity helpers ─────────────────────────────────────────────────────────

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}

// WorstSeverity returns the highest severity present, or "" for no findings.
func WorstSeverity(fs []Finding) Severity {
	worst := Severity("")
	for _, f := range fs {
		if severityRank(f.Severity) > severityRank(worst) {
			worst = f.Severity
		}
	}
	return worst
}

// Clean reports whether findings contain nothing at warning severity or above.
func Clean(fs []Finding) bool {
	return severityRank(WorstSeverity(fs)) < severityRank(SeverityWarning)
}

// Summarize renders findings as one compact human sentence, e.g.
// "3 obfuscation findings (2 critical, 1 warning): homoglyph, bidi_control".
func Summarize(fs []Finding) string {
	if len(fs) == 0 {
		return "no obfuscation findings"
	}
	bySev := map[Severity]int{}
	kindSeen := map[FindingKind]bool{}
	var kindOrder []FindingKind
	total := 0
	for _, f := range fs {
		bySev[f.Severity] += f.Count
		total += f.Count
		if !kindSeen[f.Kind] {
			kindSeen[f.Kind] = true
			kindOrder = append(kindOrder, f.Kind)
		}
	}
	var sevParts []string
	for _, s := range []Severity{SeverityCritical, SeverityWarning, SeverityInfo} {
		if bySev[s] > 0 {
			sevParts = append(sevParts, fmt.Sprintf("%d %s", bySev[s], s))
		}
	}
	kindNames := make([]string, len(kindOrder))
	for i, k := range kindOrder {
		kindNames[i] = string(k)
	}
	plural := "s"
	if total == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d obfuscation finding%s (%s): %s",
		total, plural, strings.Join(sevParts, ", "), strings.Join(kindNames, ", "))
}
