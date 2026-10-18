// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkTokens caps one search chunk, so a long section is found by its parts, not by its start.
const ChunkTokens = 400

// Chunk is one search unit (ported from v1 rag.Chunkify): a verbatim slice text[Start:End] of one
// section's own text, so a quote copied from it verifies. Search is what the full-text index
// reads: Text, or for a table row its cells paired with their column headers, so a bare figure is
// findable by what it means. Context names a row's sheet and columns.
type Chunk struct {
	Section      int    // index into Parse's list
	Locator      string // the section path, e.g. "ARTICLE I > 1.2 Fees"
	Text, Search string
	Context      string
	Start, End   int
}

// tokens is the conservative estimate used everywhere in docs: 3.5 characters per token.
func tokens(s string) int { return (utf8.RuneCountInString(s)*2 + 6) / 7 }

// Chunkify splits text into chunks along the section tree secs (from Parse).
func Chunkify(text string, secs []Section) []Chunk {
	var out []Chunk
	type open struct {
		level int
		path  string
	}
	var stack []open
	for i, s := range secs {
		for len(stack) > 0 && stack[len(stack)-1].level >= s.Level {
			stack = stack[:len(stack)-1]
		}
		path := strings.TrimSpace(s.Number + " " + s.Title)
		if len(stack) > 0 && path != "" {
			path = stack[len(stack)-1].path + " > " + path
		} else if len(stack) > 0 {
			path = stack[len(stack)-1].path
		}
		if s.Level > 0 {
			stack = append(stack, open{s.Level, path})
		}
		add := func(start, end int, search, context string) {
			for start < end && isSpaceAt(text, start) {
				start += runeLen(text, start)
			}
			for end > start && isSpaceBefore(text, end) {
				_, n := utf8.DecodeLastRuneInString(text[:end])
				end -= n
			}
			if start < end {
				c := Chunk{Section: i, Locator: path, Text: text[start:end], Search: search, Context: context, Start: start, End: end}
				if c.Search == "" {
					c.Search = c.Text
				}
				out = append(out, c)
			}
		}
		body := text[s.Start:s.BodyEnd]
		if isTabular(body) {
			tableRows(body, s.Start, add)
			continue
		}
		for start := s.Start; start < s.BodyEnd; {
			end := capEnd(text, start, s.BodyEnd)
			add(start, end, "", "")
			start = end
		}
	}
	return out
}

// capEnd is where a chunk starting at start ends: within ChunkTokens, at a sentence end in its
// second half, else at white space, else at a rune boundary.
func capEnd(text string, start, limit int) int {
	maxBytes, n := 0, 0
	for maxBytes = start; maxBytes < limit && n < ChunkTokens*7/2; n++ {
		maxBytes += runeLen(text, maxBytes)
	}
	if maxBytes >= limit {
		return limit
	}
	seg := text[start:maxBytes]
	if j := strings.LastIndexAny(seg, ".!?"); j > len(seg)/2 {
		return start + j + 1
	}
	if j := strings.LastIndexAny(seg, " \n\t"); j > 0 {
		return start + j
	}
	return maxBytes
}

func isTableRow(line string) bool { return strings.Count(line, "\t") >= 2 }

// isTabular: at least two tab-delimited rows of 3+ columns (spreadsheet exhibits, docx tables).
func isTabular(body string) bool {
	n := 0
	for _, ln := range strings.Split(body, "\n") {
		if isTableRow(ln) {
			if n++; n == 2 {
				return true
			}
		}
	}
	return false
}

// tableRows emits one chunk per data row; the first row after a "#"/"## Sheet:" marker is the
// header and is not a chunk; other lines are emitted as prose runs.
func tableRows(body string, base int, add func(start, end int, search, context string)) {
	var header []string
	sheet, prose := "", -1
	flush := func(at int) {
		if prose >= 0 {
			add(prose, at, "", "")
			prose = -1
		}
	}
	pos := base
	for _, ln := range strings.SplitAfter(body, "\n") {
		start := pos
		pos += len(ln)
		line := strings.TrimRight(ln, "\r\n")
		switch t := strings.TrimSpace(line); {
		case strings.HasPrefix(t, "#"):
			flush(start)
			sheet, header = strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(t, "# "), "Sheet:")), nil
		case isTableRow(line):
			flush(start)
			cells := strings.Split(line, "\t")
			if header == nil {
				header = cells
				continue
			}
			var pairs, cols []string
			if sheet != "" {
				pairs = append(pairs, sheet)
			}
			for k, c := range cells {
				if c = strings.TrimSpace(c); c != "" && k < len(header) && strings.TrimSpace(header[k]) != "" {
					pairs = append(pairs, strings.TrimSpace(header[k])+": "+c)
				} else if c != "" {
					pairs = append(pairs, c)
				}
			}
			for _, h := range header {
				if h = strings.TrimSpace(h); h != "" {
					cols = append(cols, h)
				}
			}
			context := "columns: " + strings.Join(cols, ", ")
			if sheet != "" {
				context = "Sheet: " + sheet + "; " + context
			}
			add(start, start+len(line), strings.Join(pairs, " | "), context)
		default:
			if prose < 0 {
				prose = start
			}
		}
	}
	flush(pos)
}

func runeLen(s string, i int) int { _, n := utf8.DecodeRuneInString(s[i:]); return n }
func isSpaceAt(s string, i int) bool {
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsSpace(r)
}
func isSpaceBefore(s string, i int) bool {
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return unicode.IsSpace(r)
}
