// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const xlsxBody = `## Sheet: Summary
Parameter	Value	Notes
Review Period	January 1, 2021 – December 31, 2023	36-month period
Excess profits from cherry-picking allocated to Oceanic Fund I LP (2021–2023)	$7,800,000	forensic estimate
Chao personal account (ending -7823) profitable allocation rate	81.6%	vs 52% firm average`

// checkChunks asserts every chunk is the verbatim slice it claims and fits the cap.
func checkChunks(t *testing.T, text string, chunks []Chunk) {
	t.Helper()
	for i, c := range chunks {
		if c.Start < 0 || c.End > len(text) || c.Start >= c.End || text[c.Start:c.End] != c.Text || c.Search == "" {
			t.Fatalf("chunk %d [%d,%d) is not its verbatim slice: %q", i, c.Start, c.End, c.Text)
		}
		if tokens(c.Text) > ChunkTokens {
			t.Fatalf("chunk %d has %d tokens > %d", i, tokens(c.Text), ChunkTokens)
		}
	}
}

// TestTableRowChunking: rows are grouped into chunks of whole lines (r2 H-1; v1 made one chunk
// per row, so per-row copies dominated a table of short rows); each data cell is indexed with its
// column name and Context names the sheet and columns.
func TestTableRowChunking(t *testing.T) {
	chunks := Chunkify(xlsxBody, Parse(xlsxBody))
	checkChunks(t, xlsxBody, chunks)
	if len(chunks) != 1 {
		t.Fatalf("%d chunks, want the 306-byte sheet in one: %+v", len(chunks), chunks)
	}
	hit := chunks[0]
	for _, want := range []string{"Value: $7,800,000", "Parameter: Excess profits", "Parameter: Chao personal account (ending -7823)", "Value: 81.6%", "Notes: 36-month period"} {
		if !strings.Contains(hit.Search, want) {
			t.Errorf("row search text pairs cells with headers: %q lacks %q", hit.Search, want)
		}
	}
	if hit.Context != "Sheet: Summary; columns: Parameter, Value, Notes" {
		t.Errorf("context: %q", hit.Context)
	}
	var rows strings.Builder
	rows.WriteString("Name\tAmount\tYear\n")
	for i := range 100 {
		fmt.Fprintf(&rows, "Party %d\t$%d,000\t20%02d\n", i, i, i)
	}
	text := rows.String()
	chunks = Chunkify(text, Parse(text))
	checkChunks(t, text, chunks)
	if len(chunks) < 2 || !strings.HasPrefix(chunks[1].Search, "columns: Name, Amount, Year\nName: Party") || chunks[1].Context != "columns: Name, Amount, Year" {
		t.Errorf("a longer table: %d chunks, the second %+v", len(chunks), chunks[min(1, len(chunks)-1)])
	}
	var wide strings.Builder // a header longer than the token cap is still the header
	for i := range 300 {
		fmt.Fprintf(&wide, "Col%03d\t", i)
	}
	wide.WriteString("\n" + strings.Repeat("x\t", 300) + "\n" + strings.Repeat("y\t", 300) + "\n")
	if wc := Chunkify(wide.String(), Parse(wide.String())); !strings.HasPrefix(wc[len(wc)-1].Context, "columns: Col000, Col001") {
		t.Errorf("a long header was lost: %+v", wc[len(wc)-1])
	}
	for _, c := range chunks[:len(chunks)-1] {
		if len(c.Text) < minTableChunk-40 || text[c.End] != '\n' {
			t.Errorf("chunk of %d bytes does not end at a row end: %q", len(c.Text), c.Text)
		}
	}
}

func TestProseStillSectionChunked(t *testing.T) {
	prose := "1. Definitions\n\"Confidential Information\" means all non-public information disclosed by either party."
	chunks := Chunkify(prose, Parse(prose))
	checkChunks(t, prose, chunks)
	if len(chunks) != 1 || chunks[0].Search != chunks[0].Text || chunks[0].Context != "" || chunks[0].Locator != "1 Definitions" {
		t.Errorf("prose chunk: %+v", chunks)
	}
}

// TestChunksCoverEveryWordOnce: long sections split under the cap at sentence or word ends, every
// non-space byte of the document is in exactly one chunk, and locators carry the section path.
func TestChunksCoverEveryWordOnce(t *testing.T) {
	var b strings.Builder
	b.WriteString("ARTICLE I Terms\n1.1 Payment\n")
	for i := range 300 {
		fmt.Fprintf(&b, "Clause %d sets the fee for the Ångström unit. ", i)
	}
	b.WriteString("\n(a) a sub-item\n" + strings.Repeat("x", 3000) + "\n")
	text := b.String()
	chunks := Chunkify(text, Parse(text))
	checkChunks(t, text, chunks)
	covered := make([]int, len(text))
	for _, c := range chunks {
		for i := c.Start; i < c.End; i++ {
			covered[i]++
		}
	}
	for i, n := range covered {
		if n > 1 || n == 0 && !strings.ContainsRune(" \n\t", rune(text[i])) {
			t.Fatalf("byte %d (%q) is in %d chunks", i, text[i], n)
		}
	}
	if chunks[1].Locator != "ARTICLE I Terms > 1.1 Payment" || chunks[len(chunks)-1].Locator != "ARTICLE I Terms > 1.1 Payment > (a) a sub-item" {
		t.Errorf("locators: %q, %q", chunks[1].Locator, chunks[len(chunks)-1].Locator)
	}
	if !strings.HasSuffix(chunks[1].Text, ".") {
		t.Errorf("a long clause is cut at a sentence end: %q", chunks[1].Text[len(chunks[1].Text)-20:])
	}
}

// TestSectionTreeBounded (r1 reproducer R1, r2 prosecutor H-1 and L-3, auditor L3): each hostile
// shape (a huge heading title over 40k items, a 1,500-deep decimal chain, 170k one-line items, and
// tables: 5,000 columns × 5,000 rows, a 60 KB column name, 250k tiny rows under a deep path,
// 50k one-row sheets, a 1.5 MB cell) keeps every chunk column (path, body, search text, context)
// within 3× the input, chunks under the token cap, and exactly MaxSections sections when the cap
// cuts the tree (with a note saying so); three ingest end to end. v1's tree copied the full path
// into every chunk (3,284× and 168 s for the first shape); r1's table rows copied the whole header
// (10,002× and 3 GB for a 120 KB table).
func TestSectionTreeBounded(t *testing.T) {
	const bound = 3 // × the input: every chunk column (locator, body, search text, context)
	var chain strings.Builder
	for n := "1"; chain.Len() < 1<<20; n += ".1" {
		chain.WriteString(n + " Clause\n")
	}
	title := func(n string) string { return n + " " + strings.Repeat("Title ", 20) + "\n" }
	deep := "ARTICLE I " + title("") + title("1.") + title("1.1") + title("1.1.1") + title("1.1.1.1") + strings.Repeat("h\t", 3) + "\n"
	shapes := map[string]string{
		"long title":          "ARTICLE I " + strings.Repeat("word ", 200_000) + "\n" + strings.Repeat("(a) x\n", 40_000),
		"deep chain":          chain.String(),
		"tiny items":          strings.Repeat("(a) x\n", 170_000),
		"preface, tiny items": "Preface.\n" + strings.Repeat("(a) x\n", 170_000),
		// r2 prosecutor H-1: every row copied the whole header (C×R) into Context and Search
		"wide table":     strings.Repeat("h\t", 5000) + "\n" + strings.Repeat("a\t\t\n", 5000),
		"long column":    strings.Repeat("H", 60_000) + "\t\t\n" + strings.Repeat("a\t\t\n", 2000),
		"named columns":  strings.Repeat("Column name number 00\t", 100) + "\n" + strings.Repeat(strings.Repeat("1\t", 100)+"\n", 2500),
		"deep tiny rows": deep + strings.Repeat("a\t\t\n", 250_000),
		"many sheets":    strings.Repeat("#\nh\th\th\na\tb\tc\n", 50_000),
		"long row":       "h\th\th\n" + strings.Repeat("word ", 300_000) + "\tb\tc\n" + "a\tb\tc\n", // r2 L-3: one 1.5 MB cell
	}
	for name, text := range shapes {
		start := time.Now()
		secs := Parse(text)
		chunks := Chunkify(text, secs)
		took, size := time.Since(start), 0
		for _, c := range chunks {
			size += len(c.Locator) + len(c.Text) + len(c.Context)
			if c.Search != c.Text {
				size += len(c.Search) // stored only when it is not the body (ingest)
			}
		}
		checkChunks(t, text, chunks)
		t.Logf("%s: %d bytes in, %d sections, %d chunks, all chunk columns %d bytes (%.2f×), %s", name, len(text), len(secs), len(chunks), size, float64(size)/float64(len(text)), took)
		if size > len(text)*bound || len(secs) > MaxSections || took > 30*time.Second || strings.Contains(name, "items") && len(secs) != MaxSections {
			t.Errorf("%s: not bounded", name)
		}
	}
	s := newService(t)
	for _, name := range []string{"long title", "deep tiny rows", "long row"} {
		start := time.Now()
		d, err := s.IngestText(t.Context(), "hostile.txt", shapes[name])
		var stored int
		if err == nil {
			err = s.db.QueryRow(`SELECT sum(octet_length(locator) + octet_length(body) + octet_length(search_text) + octet_length(context)) FROM chunks WHERE document_id = $1`, d.ID).Scan(&stored)
		}
		t.Logf("ingested the %s shape in %s: %d bytes stored in chunks", name, time.Since(start), stored)
		capped := name == "long title" == slices.ContainsFunc(d.Notes, func(n string) bool { return strings.Contains(n, "first 10000 headings only") })
		if err != nil || stored > len(shapes[name])*bound || time.Since(start) > 15*time.Second || !capped {
			t.Errorf("ingest %s: %v, %d bytes stored", name, err, stored)
		}
	}
}

// TestHeadingHeuristics (r1 reproducer R11): a figure at a line start followed by a lower-case word
// is not a heading; titles lose bidi and control characters and are cut at maxTitle bytes; a
// number of more than maxNumber bytes or six decimal levels is body text.
func TestHeadingHeuristics(t *testing.T) {
	for _, line := range []string{"100 dollars were paid", "1.5 million dollars", "3.14 is pi", "1.2.3.4.5.6 Deep", "ARTICLE " + strings.Repeat("I", 30) + " Long", "1\t2023\t$400", "## Sheet\tA\tB"} {
		if s, ok := classify(line); ok {
			t.Errorf("%q is a heading: %+v", line, s)
		}
	}
	for line, want := range map[string]string{"1. Definitions": "Definitions", "1.2.3.4.5 Five levels": "Five levels", "ARTICLE I Fees \u202e5.0 GBP\x1b[31m": "Fees 5.0 GBP[31m",
		"2. " + strings.Repeat("É", 100): strings.Repeat("É", 60) + "…"} {
		if s, ok := classify(line); !ok || s.Title != want {
			t.Errorf("%q: %+v %v, want title %q", line, s, ok, want)
		}
	}
}
