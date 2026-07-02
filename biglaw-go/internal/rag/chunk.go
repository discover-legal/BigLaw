// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package rag implements section-aware hybrid retrieval over the matter's
// documents: PageIndex-section chunks, each carrying a dense embedding,
// anticipated-question embeddings (doc2query), and a BM25 lexical signal, fused
// at query time (dense + HyDE + question + BM25) with Reciprocal Rank Fusion.
//
// It feeds the staged evidence extractor the semantically-right sections instead
// of the document letterhead — the failure mode of the old one-embedding-per-doc
// search. Backed by an in-process ChunkStore now (RuVector-style); the same seam
// will be satisfied by the EvidenceGraph later.
package rag

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/biglaw-go/internal/pageindex"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// Chunk is one section-aware, size-capped slice of a document — the unit of
// retrieval. Text is verbatim (a byte-exact substring of the document) so a
// quote copied from it still verifies at the citation gate.
type Chunk struct {
	ID       string // "<docID>#<n>"
	DocID    string
	DocTitle string
	Locator  string // section path, e.g. "ARTICLE I > 1.2 Definitions"
	Text     string // verbatim chunk text (what an agent quotes — gate-safe)
	// EmbedText, when non-empty, is what gets dense-embedded and BM25-indexed
	// instead of Text. For table rows it pairs each cell with its column header
	// (e.g. "Summary — Excess profits … Oceanic Fund I LP: $7,800,000") so a bare
	// figure becomes findable, while Text stays the verbatim row for quoting.
	EmbedText string
	// Context is optional human/model-facing context for the chunk (the table's
	// sheet + column headers), surfaced by search_chunks so a small model can read
	// a cryptic row's meaning without it leaking into the verbatim quote.
	Context   string
	ByteStart int // offset into the document
	ByteEnd   int
}

// indexText is what the store embeds and BM25-indexes for a chunk: EmbedText when
// set (enriched table rows), else the verbatim Text.
func (c Chunk) indexText() string {
	if strings.TrimSpace(c.EmbedText) != "" {
		return c.EmbedText
	}
	return c.Text
}

// DefaultChunkTokens caps a chunk's size so no single section is "represented by
// a slice of itself"; larger sections are sub-split at sentence boundaries.
const DefaultChunkTokens = 400

// Chunkify splits a document into section-aware, size-capped chunks via
// PageIndex. Each section's own Body (excluding its children) becomes one or more
// chunks, tagged with its section path. capTokens <= 0 uses DefaultChunkTokens.
func Chunkify(docID, docTitle, content string, capTokens int) []Chunk {
	if capTokens <= 0 {
		capTokens = DefaultChunkTokens
	}
	var chunks []Chunk
	var walk func(secs []pageindex.Section, path string)
	walk = func(secs []pageindex.Section, path string) {
		for _, s := range secs {
			label := strings.TrimSpace(strings.TrimSpace(s.Number) + " " + strings.TrimSpace(s.Title))
			label = strings.TrimSpace(label)
			p := label
			switch {
			case path != "" && label != "":
				p = path + " > " + label
			case label == "":
				p = path
			}
			if body := strings.TrimSpace(s.Body); body != "" {
				if isTabularBody(s.Body) {
					// Exhibit/spreadsheet body: emit one chunk per data row so each
					// fact (amount, account #, %) is findable and quotable on its own.
					chunks = append(chunks, tableRowChunks(s.Body, s.ByteStart, docID, docTitle, p)...)
				} else {
					for _, w := range capSplit(s.Body, s.ByteStart, capTokens) {
						if strings.TrimSpace(w.text) == "" {
							continue
						}
						chunks = append(chunks, Chunk{
							DocID:     docID,
							DocTitle:  docTitle,
							Locator:   p,
							Text:      w.text,
							ByteStart: w.start,
							ByteEnd:   w.end,
						})
					}
				}
			}
			walk(s.Children, p)
		}
	}
	walk(pageindex.Parse(content), "")
	for i := range chunks {
		chunks[i].ID = fmt.Sprintf("%s#%d", docID, i)
	}
	return chunks
}

// isTableRow reports whether a line is a tab-delimited table row (3+ columns).
func isTableRow(line string) bool { return strings.Count(line, "\t") >= 2 }

// isTabularBody reports whether a section body is predominantly tabular — at least
// two table rows. Spreadsheet exhibits (converted to "## Sheet:" + tab rows) and
// docx tables (tab-joined rows) match; ordinary prose does not.
func isTabularBody(body string) bool {
	n := 0
	for _, ln := range strings.Split(body, "\n") {
		if isTableRow(ln) {
			if n++; n >= 2 {
				return true
			}
		}
	}
	return false
}

// tableRowChunks emits one chunk per data row of a tabular body. Each chunk's Text
// is the VERBATIM row (gate-safe to quote); EmbedText pairs each cell with its
// column header (so a bare figure is findable); Context names the sheet + columns.
// The first table row after a "## Sheet:"/heading marker is taken as the header.
// Non-table lines are flushed as ordinary prose chunks.
func tableRowChunks(body string, byteBase int, docID, docTitle, path string) []Chunk {
	var chunks []Chunk
	var header []string
	sheet := ""
	pos := byteBase
	var prose []string
	proseStart := byteBase
	flushProse := func(end int) {
		if txt := strings.TrimSpace(strings.Join(prose, "\n")); txt != "" {
			chunks = append(chunks, Chunk{DocID: docID, DocTitle: docTitle, Locator: path, Text: txt, ByteStart: proseStart, ByteEnd: end})
		}
		prose = nil
	}
	for _, ln := range strings.Split(body, "\n") {
		lineStart := pos
		pos += len(ln) + 1 // +1 for the consumed '\n'
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "#") { // "## Sheet: X" — new table context
			flushProse(lineStart)
			sheet = strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
			sheet = strings.TrimPrefix(sheet, "Sheet:")
			sheet = strings.TrimSpace(sheet)
			header = nil
			continue
		}
		if isTableRow(ln) {
			flushProse(lineStart)
			cells := strings.Split(ln, "\t")
			if header == nil {
				header = cells // first row of the block is the header
				continue
			}
			loc := joinLoc(path, sheet)
			if k := firstNonEmpty(cells); k != "" {
				loc = joinLoc(loc, k)
			}
			chunks = append(chunks, Chunk{
				DocID: docID, DocTitle: docTitle, Locator: loc,
				Text:      trimmed,
				EmbedText: enrichRow(sheet, header, cells),
				Context:   rowContext(sheet, header),
				ByteStart: lineStart, ByteEnd: lineStart + len(ln),
			})
			continue
		}
		if len(prose) == 0 {
			proseStart = lineStart
		}
		prose = append(prose, ln)
	}
	flushProse(pos)
	return chunks
}

// enrichRow renders a data row as header-paired key:value pairs (the EmbedText that
// makes a bare cell findable), e.g. "Summary | Parameter: Excess profits … | Value: $7,800,000".
func enrichRow(sheet string, header, cells []string) string {
	var parts []string
	if sheet != "" {
		parts = append(parts, sheet)
	}
	for k, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if k < len(header) {
			if h := strings.TrimSpace(header[k]); h != "" {
				parts = append(parts, h+": "+c)
				continue
			}
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, " | ")
}

func rowContext(sheet string, header []string) string {
	cols := make([]string, 0, len(header))
	for _, h := range header {
		if h = strings.TrimSpace(h); h != "" {
			cols = append(cols, h)
		}
	}
	ctx := ""
	if sheet != "" {
		ctx = "Sheet: " + sheet + "; "
	}
	return ctx + "columns: " + strings.Join(cols, ", ")
}

func firstNonEmpty(cells []string) string {
	for _, c := range cells {
		if c = strings.TrimSpace(c); c != "" {
			return c
		}
	}
	return ""
}

func joinLoc(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " > " + b
	}
}

type window struct {
	text       string
	start, end int
}

// capSplit breaks body into windows of at most capTokens, cutting at sentence
// boundaries where possible (else word boundaries). byteBase is body's offset in
// the original document; each window's start/end index the document. Text is
// TrimSpace'd but remains a contiguous verbatim substring.
func capSplit(body string, byteBase, capTokens int) []window {
	if strutil.EstimateTokens(body) <= capTokens {
		return []window{{text: strings.TrimSpace(body), start: byteBase, end: byteBase + len(body)}}
	}
	maxChars := strutil.TokenBudgetToChars(capTokens)
	if maxChars < 1 {
		maxChars = 1
	}
	var out []window
	i := 0
	for i < len(body) {
		end := i + maxChars
		if end >= len(body) {
			out = append(out, window{text: strings.TrimSpace(body[i:]), start: byteBase + i, end: byteBase + len(body)})
			break
		}
		for end < len(body) && !utf8.RuneStart(body[end]) {
			end++
		}
		seg := body[i:end]
		if j := strings.LastIndexAny(seg, ".!?"); j > maxChars/2 {
			end = i + j + 1 // include the terminator
		} else if j := strings.LastIndexAny(seg, " \n\t"); j > 0 {
			end = i + j
		}
		out = append(out, window{text: strings.TrimSpace(body[i:end]), start: byteBase + i, end: byteBase + end})
		i = end
	}
	return out
}
