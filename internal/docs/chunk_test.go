// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"fmt"
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

func TestTableRowChunking(t *testing.T) {
	chunks := Chunkify(xlsxBody, Parse(xlsxBody))
	checkChunks(t, xlsxBody, chunks)
	if len(chunks) != 3 {
		t.Fatalf("%d chunks, want one per data row: %+v", len(chunks), chunks)
	}
	hit := chunks[1]
	if !strings.Contains(hit.Text, "$7,800,000") || !strings.Contains(hit.Search, "Value: $7,800,000") || !strings.Contains(hit.Search, "Parameter: Excess profits") ||
		!strings.HasPrefix(hit.Search, "Summary | ") {
		t.Errorf("row search text pairs cells with headers: %q", hit.Search)
	}
	if hit.Context != "Sheet: Summary; columns: Parameter, Value, Notes" {
		t.Errorf("context: %q", hit.Context)
	}
	for _, c := range chunks {
		if strings.Contains(c.Text, "Parameter\tValue") {
			t.Error("the header row leaked in as a data chunk")
		}
	}
	if !strings.Contains(chunks[2].Text, "-7823") || !strings.Contains(chunks[2].Text, "81.6%") {
		t.Error("the -7823 / 81.6% account row was not chunked")
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

// TestSectionTreeBounded (r1 reproducer R1): about 1 MB of each hostile shape (a huge heading
// title over 40k items, a 1,500-deep decimal chain, 170k one-line items) keeps chunk text plus
// section paths within 2.5× the input and at most MaxSections sections; one ingests end to end. v1's tree copied the full path into every chunk: 3,284× and
// 168 s for the first shape.
func TestSectionTreeBounded(t *testing.T) {
	var chain strings.Builder
	for n := "1"; chain.Len() < 1<<20; n += ".1" {
		chain.WriteString(n + " Clause\n")
	}
	shapes := map[string]string{
		"long title": "ARTICLE I " + strings.Repeat("word ", 200_000) + "\n" + strings.Repeat("(a) x\n", 40_000),
		"deep chain": chain.String(),
		"tiny items": strings.Repeat("(a) x\n", 170_000),
	}
	for name, text := range shapes {
		start := time.Now()
		secs := Parse(text)
		chunks := Chunkify(text, secs)
		took, size := time.Since(start), 0
		for _, c := range chunks {
			size += len(c.Locator) + len(c.Text)
		}
		t.Logf("%s: %d bytes in, %d sections, %d chunks, chunk text + paths %d bytes (%.2f×), %s", name, len(text), len(secs), len(chunks), size, float64(size)/float64(len(text)), took)
		if size > len(text)*5/2 || len(secs) > MaxSections {
			t.Errorf("%s: not bounded", name)
		}
	}
	s := newService(t)
	start := time.Now()
	d, err := s.IngestText(t.Context(), "hostile.txt", shapes["long title"])
	var stored int
	if err == nil {
		err = s.db.QueryRow(`SELECT sum(octet_length(locator) + octet_length(body) + octet_length(search_text)) FROM chunks WHERE document_id = $1`, d.ID).Scan(&stored)
	}
	t.Logf("ingested the long-title shape in %s: %d bytes stored in chunks", time.Since(start), stored)
	if err != nil || stored > len(shapes["long title"])*5/2 || time.Since(start) > 15*time.Second {
		t.Errorf("ingest: %v, %d bytes stored", err, stored)
	}
}

// TestHeadingHeuristics (r1 reproducer R11): a figure at a line start followed by a lower-case word
// is not a heading; titles lose bidi and control characters and are cut at maxTitle bytes; a
// number of more than maxNumber bytes or six decimal levels is body text.
func TestHeadingHeuristics(t *testing.T) {
	for _, line := range []string{"100 dollars were paid", "1.5 million dollars", "3.14 is pi", "1.2.3.4.5.6 Deep", "ARTICLE " + strings.Repeat("I", 30) + " Long"} {
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
