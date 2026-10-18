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
// reads: Text, or for table rows their cells paired with their column names (tableRows). Context
// names a table chunk's sheet and columns.
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
		if isTabular(text[s.Start:s.BodyEnd]) {
			tableRows(text, s.Start, s.BodyEnd, add)
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

// Bounds on what a table adds to its own text (r2 H-1: every row used to copy the whole header,
// C×R bytes): rows are grouped into chunks of at least minTableChunk bytes, a row gets column
// names of at most half its own length, and a chunk's Context (also indexed, once per chunk) is
// at most maxContext bytes.
const (
	minTableChunk = 1024
	maxContext    = 256
)

// tableRows chunks the tabular text[from:to]: whole lines (rows, "#"/"## Sheet:" markers, prose)
// grouped until a chunk holds minTableChunk bytes, never over ChunkTokens (a longer line is split
// like prose). The first row after the start or a marker is the header; a data row is indexed as
// its cells paired with their column names, so a bare figure is findable by what it means.
// Context names the sheet and columns of the chunk's first data row.
func tableRows(text string, from, to int, add func(start, end int, search, context string)) {
	var header []string
	sheet, columns, start, runes, context := "", "", -1, 0, ""
	var search strings.Builder
	flush := func(at int) {
		if start >= 0 {
			add(start, at, strings.TrimPrefix(context+"\n", "\n")+search.String(), context)
		}
		start, runes, context = -1, 0, ""
		search.Reset()
	}
	for pos := from; pos < to; {
		end := to
		if i := strings.IndexByte(text[pos:to], '\n'); i >= 0 {
			end = pos + i + 1
		}
		line, n := strings.TrimRight(text[pos:end], "\r\n"), utf8.RuneCountInString(text[pos:end])
		row := isTableRow(line)
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			sheet, header = strings.TrimSpace(strings.TrimPrefix(strings.TrimLeft(t, "# "), "Sheet:")), nil
		} else if row && header == nil { // the header, however long
			header, row = strings.Split(line, "\t"), false
			var cols []string
			for _, h := range header {
				if h = strings.TrimSpace(h); h != "" {
					cols = append(cols, h)
				}
			}
			if columns = "columns: " + strings.Join(cols, ", "); sheet != "" {
				columns = "Sheet: " + sheet + "; " + columns
			}
			columns = clip(columns, maxContext)
		}
		if n > ChunkTokens*7/2 {
			flush(pos)
			for s := pos; s < end; {
				e := capEnd(text, s, end)
				add(s, e, "", "")
				s = e
			}
			pos = end
			continue
		}
		if start >= 0 && (pos-start >= minTableChunk || runes+n > ChunkTokens*7/2) {
			flush(pos)
		}
		if start < 0 {
			start = pos
		}
		runes += n
		indexed := line
		if row {
			indexed = pairCells(line, header)
			if context == "" {
				context = columns
			}
		}
		search.WriteString(indexed + "\n")
		pos = end
	}
	flush(to)
}

// pairCells is a data row's search text: "Column: cell" for each cell while the column names
// added stay within half the row's length, bare cells after that.
func pairCells(line string, header []string) string {
	var cells []string
	budget := len(line) / 2
	for k, c := range strings.Split(line, "\t") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if k < len(header) {
			if h := strings.TrimSpace(header[k]); h != "" && len(h)+2 <= budget {
				budget -= len(h) + 2
				c = h + ": " + c
			}
		}
		cells = append(cells, c)
	}
	return strings.Join(cells, "\t")
}

// clip cuts s to at most n bytes (at a rune boundary) and marks the cut with "…".
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
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
