// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The literal half of figure reading (lab-r4 r4, owner decision): code finds the number-looking spans
// of a text and reads their digits exactly as written (digits, separators, a decimal mark, a minus
// sign written right before them); what a span means (money, a count, a percentage; its currency,
// scale, sign and party) is the figure-reading agent's labelling (the engine's read_figures, checked
// by ReadLabels in figread.go). Code holds the invariant that every figure a finding or the memo
// asserts traces to a span that literally exists in a source quote; no word list, currency map or
// header rule decides a meaning here.
//
//	span      = [minus] digits { separator digits } | [minus] "." digits
//	minus     = "-" | "−" | "–" | "—" written right before the digits, after no letter or digit
//	separator = "," | "." | "'" | "’" | NBSP | a narrow or thin space, each between two digits
//
// A span's value depends on which mark, if any, is its decimal point (Value): its form alone decides
// it for "1,200,000", "3,5" or "1.234,56"; "1,200" and "1.200" read two ways (Readings).

// Span is one number as written.
type Span struct {
	Start, End int    // its bytes in the text, a minus sign included
	Text       string // as written
}

// groupSeps are the separators that only ever group ("1'200'000", "1 200 000" with NBSP or thin spaces).
var groupSeps = []string{"'", "’", " ", " ", " "}

// minusSigns are the signs read as part of a span when written right before its digits.
var minusSigns = []string{"-", "−", "–", "—"}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }

// sepAt is the length of a separator at s's start that a digit follows (0: none).
func sepAt(s string) int {
	for _, p := range append([]string{",", "."}, groupSeps...) {
		if strings.HasPrefix(s, p) && len(s) > len(p) && isDigit(s[len(p)]) {
			return len(p)
		}
	}
	return 0
}

// Spans lists the numbers text states, as written: other scripts' digits ("١٠٠", "１００") are read as
// their digits, spans never overlap, and digits glued after a letter ("F12", "10b5") are no number.
func Spans(text string) []Span {
	t, pos := asciiDigits(text)
	var out []Span
	for i := 0; i < len(t); {
		lead := t[i] == '.' && i+1 < len(t) && isDigit(t[i+1]) && (i == 0 || !isWordRune(lastRune(t[:i])) && !strings.ContainsRune(".,'’", lastRune(t[:i])))
		if isDigit(t[i]) && i > 0 && unicode.IsLetter(lastRune(t[:i])) { // an identifier's digits ("F12", "10b5")
			for i < len(t) && isDigit(t[i]) {
				i++
			}
			continue
		}
		if !isDigit(t[i]) && !lead {
			_, n := utf8.DecodeRuneInString(t[i:])
			i += n
			continue
		}
		s := i
		for _, m := range minusSigns {
			if strings.HasSuffix(t[:s], m) && (s == len(m) || !isWordRune(lastRune(t[:s-len(m)]))) {
				s -= len(m)
				break
			}
		}
		if lead {
			i++
		}
		for {
			for i < len(t) && isDigit(t[i]) {
				i++
			}
			if n := sepAt(t[i:]); n > 0 {
				i += n
				continue
			}
			break
		}
		start, end := s, i
		if pos != nil {
			start, end = pos[s], pos[i]
		}
		out = append(out, Span{Start: start, End: end, Text: text[start:end]})
	}
	return out
}

// digitsOf is a span's text without its sign, its other scripts' digits read as ASCII.
func (s Span) digitsOf() (string, bool) {
	t, _ := asciiDigits(s.Text)
	for _, m := range minusSigns {
		if strings.HasPrefix(t, m) {
			return t[len(m):], true
		}
	}
	return t, false
}

// Neg reports whether a minus sign is written as part of the span.
func (s Span) Neg() bool { _, neg := s.digitsOf(); return neg }

// Single reports whether the span is one bare digit ("paragraph 3"): a locator more often than a figure.
func (s Span) Single() bool { d, neg := s.digitsOf(); return len(d) == 1 && !neg }

// Grouped reports whether the span writes separators between its digits.
func (s Span) Grouped() bool {
	d, _ := s.digitsOf()
	return strings.ContainsFunc(d, func(r rune) bool { return r < '0' || r > '9' })
}

// Value is the span's number, signed as written, when decimal ('.' or ',') is its decimal point or
// (0) every mark groups. ok is false when the marks do not fit that reading: grouping in threes
// (or the Indian 2-digit groups, "1,00,000") by one separator, one decimal mark after the last group.
func (s Span) Value(decimal byte) (*big.Rat, bool) {
	d, neg := s.digitsOf()
	intPart, frac := d, ""
	if decimal != 0 {
		k := strings.LastIndexByte(d, decimal)
		if k < 0 || strings.Count(d, string(decimal)) != 1 {
			return nil, false
		}
		intPart, frac = d[:k], d[k+1:]
		if frac == "" || strings.ContainsFunc(frac, func(r rune) bool { return r < '0' || r > '9' }) {
			return nil, false
		}
	}
	plain, ok := ungroup(intPart, decimal)
	if !ok {
		return nil, false
	}
	if frac != "" {
		plain += "." + frac
	}
	r, ok := new(big.Rat).SetString(plain)
	if ok && neg {
		r.Neg(r)
	}
	return r, ok
}

// ungroup reads an integer part's digits: plain, or grouped by one separator that is not the decimal
// mark, its groups after the first of three digits (or of two before a last group of three).
func ungroup(s string, decimal byte) (string, bool) {
	if s == "" {
		return "0", decimal != 0 // ".5"
	}
	var sep string
	for _, p := range append([]string{",", "."}, groupSeps...) {
		if strings.Contains(s, p) {
			if sep != "" || p == string(decimal) {
				return "", false
			}
			sep = p
		}
	}
	if sep == "" {
		return s, true
	}
	parts := strings.Split(s, sep)
	if len(parts[0]) == 0 || len(parts[0]) > 3 || len(parts[len(parts)-1]) != 3 {
		return "", false
	}
	for _, p := range parts[1 : len(parts)-1] {
		if len(p) != 3 && len(p) != 2 {
			return "", false
		}
	}
	return strings.Join(parts, ""), true
}

// Readings are the distinct values the span's form allows, in the order none, '.', ','.
func (s Span) Readings() []*big.Rat {
	var out []*big.Rat
	for _, d := range []byte{0, '.', ','} {
		if r, ok := s.Value(d); ok && !slices.ContainsFunc(out, func(x *big.Rat) bool { return x.Cmp(r) == 0 }) {
			out = append(out, r)
		}
	}
	return out
}

// Literal is what text states as written: its dates (as keys; their digits are no numbers) and its
// number spans that read as a number at all ("7.3.1" does not).
func Literal(text string) (dateKeys []string, spans []Span) {
	keys, ds := dateSpans(text)
	b := []byte(text)
	for _, loc := range ds {
		for i := loc[0]; i < loc[1]; i++ {
			b[i] = ' '
		}
	}
	for _, s := range Spans(string(b)) {
		if len(s.Readings()) > 0 {
			s.Text = text[s.Start:s.End]
			spans = append(spans, s)
		}
	}
	return keys, spans
}

// asciiDigits rewrites the decimal digits of other scripts ("١٠٠", "１００") as ASCII, with the
// byte offset in text of each byte of the result and one past its end (nil: text is unchanged).
func asciiDigits(text string) (string, []int) {
	if !strings.ContainsFunc(text, func(r rune) bool { return r > unicode.MaxASCII && unicode.IsDigit(r) }) {
		return text, nil
	}
	var b strings.Builder
	pos := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if d, ok := digitValue(r); ok && r > unicode.MaxASCII {
			b.WriteByte('0' + d)
			pos = append(pos, i)
		} else {
			b.WriteString(text[i : i+n])
			for k := range n {
				pos = append(pos, i+k)
			}
		}
		i += n
	}
	return b.String(), append(pos, len(text))
}

// digitValue is a decimal digit's value (Unicode's Nd ranges run in tens from a zero).
func digitValue(r rune) (byte, bool) {
	for _, g := range unicode.Nd.R16 {
		if rune(g.Lo) <= r && r <= rune(g.Hi) && g.Stride == 1 {
			return byte((r - rune(g.Lo)) % 10), true
		}
	}
	for _, g := range unicode.Nd.R32 {
		if rune(g.Lo) <= r && r <= rune(g.Hi) && g.Stride == 1 {
			return byte((r - rune(g.Lo)) % 10), true
		}
	}
	return 0, false
}

// dateRe finds the dates G8 traces as dates: month names with a year, ISO, m/d/y and continental
// d.m.y. A bare year ("2024") is a number span like any other.
var dateRe = regexp.MustCompile(`(?i)\b(?:\d{1,2}\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?(?:\s+\d{1,2},?)?\s+(?:19|20)\d\d\b|\b(?:19|20)\d\d-\d\d(?:-\d\d)?\b|\b\d{1,2}/\d{1,2}/(?:19|20)\d\d\b|\b\d{1,2}\.\d{1,2}\.(?:19|20)\d\d\b`)

var euroDate = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.((?:19|20)\d\d)$`)

// dateSpans finds the dates in text with their spans; a continental "1.5.2024" is a date only when
// its day and month are a day and a month, and it is not part of a longer number.
func dateSpans(text string) (keys []string, spans [][]int) {
	for _, loc := range dateRe.FindAllStringIndex(text, -1) {
		raw := text[loc[0]:loc[1]]
		if m := euroDate.FindStringSubmatch(raw); m != nil {
			d, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			y, _ := strconv.Atoi(m[3])
			if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 && (loc[0] == 0 || !strings.ContainsRune(".,", lastRune(text[:loc[0]]))) {
				keys, spans = append(keys, Date{y, mo, d}.Key()), append(spans, loc)
			}
			continue
		}
		if d, ok := ParseDate(raw); ok {
			keys, spans = append(keys, d.Key()), append(spans, loc)
		}
	}
	return keys, spans
}

// DateSpans is the dates text states as G8 reads them (Literal's), each with where it is written.
func DateSpans(text string) (keys []string, spans [][]int) { return dateSpans(text) }
