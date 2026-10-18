// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"fmt"
	"strings"
	"testing"
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
