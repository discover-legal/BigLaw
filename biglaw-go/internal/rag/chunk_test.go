// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package rag

import (
	"strings"
	"testing"
)

const xlsxBody = `## Sheet: Summary
Parameter	Value	Notes
Review Period	January 1, 2021 – December 31, 2023	36-month period
Excess profits from cherry-picking allocated to Oceanic Fund I LP (2021–2023)	$7,800,000	forensic estimate
Chao personal account (ending -7823) profitable allocation rate	81.6%	vs 52% firm average`

func TestTableRowChunking(t *testing.T) {
	if !isTabularBody(xlsxBody) {
		t.Fatal("xlsx body not detected as tabular")
	}
	chunks := Chunkify("doc-x", "sec-quantitative-analysis-summary.xlsx", xlsxBody, 400)
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 row chunks, got %d", len(chunks))
	}
	// Find the $7.8M chunk and check the invariants the design depends on.
	var hit *Chunk
	for i := range chunks {
		if strings.Contains(chunks[i].Text, "$7,800,000") {
			hit = &chunks[i]
		}
	}
	if hit == nil {
		t.Fatal("no chunk carries the $7,800,000 row")
	}
	// 1) Text is a VERBATIM substring of the source (gate-safe to quote).
	if !strings.Contains(xlsxBody, hit.Text) {
		t.Errorf("row Text is not a verbatim substring of the source: %q", hit.Text)
	}
	// 2) EmbedText pairs the value with its description/headers (findability).
	if !strings.Contains(hit.EmbedText, "Oceanic Fund") || !strings.Contains(hit.EmbedText, "$7,800,000") {
		t.Errorf("EmbedText should pair description + value, got %q", hit.EmbedText)
	}
	// 3) Context names the sheet + columns.
	if !strings.Contains(hit.Context, "Summary") || !strings.Contains(strings.ToLower(hit.Context), "value") {
		t.Errorf("Context should name sheet + columns, got %q", hit.Context)
	}
	// The header row itself must NOT become a data chunk.
	for _, c := range chunks {
		if strings.TrimSpace(c.Text) == "Parameter\tValue\tNotes" {
			t.Error("the header row leaked in as a data chunk")
		}
	}
	// The cryptic account fact is findable via its row.
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Text, "-7823") && strings.Contains(c.Text, "81.6%") {
			found = true
		}
	}
	if !found {
		t.Error("the -7823 / 81.6% account row was not chunked")
	}
}

func TestProseStillSectionChunked(t *testing.T) {
	// A non-tabular body must still go through normal section chunking, untouched.
	prose := "1. Definitions\n\"Confidential Information\" means all non-public information disclosed by either party."
	chunks := Chunkify("doc-p", "MSA", prose, 400)
	if len(chunks) == 0 {
		t.Fatal("prose produced no chunks")
	}
	for _, c := range chunks {
		if c.EmbedText != "" {
			t.Errorf("prose chunk should have no EmbedText, got %q", c.EmbedText)
		}
	}
}
