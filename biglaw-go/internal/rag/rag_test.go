// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package rag

import (
	"strings"
	"testing"
)

func TestChunkify(t *testing.T) {
	doc := `MASTER SERVICES AGREEMENT

1. Definitions
"Confidential Information" means all non-public information disclosed by either party. It includes trade secrets and financial data.

2. Services
The Provider shall perform the services described in Schedule A. Performance is measured monthly.

(a) Service levels are defined in the SLA.
(b) Remedies for breach are limited to fee credits.`

	chunks := Chunkify("doc-1", "MSA", doc, 400)
	if len(chunks) == 0 {
		t.Fatal("no chunks produced")
	}
	for i, c := range chunks {
		if strings.TrimSpace(c.Text) == "" {
			t.Errorf("chunk %d empty", i)
		}
		// Text must be a verbatim substring of the source (citation-gate safe).
		if !strings.Contains(doc, strings.TrimSpace(c.Text)) {
			// allow leading/trailing trim differences by checking the core
			core := strings.TrimSpace(c.Text)
			if !strings.Contains(doc, core) {
				t.Errorf("chunk %d text is not a verbatim substring: %q", i, c.Text[:min(60, len(c.Text))])
			}
		}
		if c.DocID != "doc-1" || c.DocTitle != "MSA" {
			t.Errorf("chunk %d wrong doc tag", i)
		}
		if c.ID == "" {
			t.Errorf("chunk %d missing id", i)
		}
	}
	// Some chunk should carry a Definitions-ish locator.
	hasDefs := false
	for _, c := range chunks {
		if strings.Contains(strings.ToLower(c.Locator), "definition") {
			hasDefs = true
		}
	}
	if !hasDefs {
		t.Errorf("expected a chunk located in the Definitions section; locators=%v", locators(chunks))
	}
}

func TestCapSplitBoundsAndVerbatim(t *testing.T) {
	// A long single paragraph must split into multiple capped windows, each a
	// verbatim substring, together covering the text.
	body := strings.Repeat("This sentence is part of a long clause that must be split into bounded windows. ", 40)
	ws := capSplit(body, 100, 60) // small cap forces splitting
	if len(ws) < 2 {
		t.Fatalf("expected multiple windows, got %d", len(ws))
	}
	for _, w := range ws {
		if !strings.Contains(body, strings.TrimSpace(w.text)) {
			t.Errorf("window not a verbatim substring")
		}
		if w.start < 100 || w.end > 100+len(body) {
			t.Errorf("window offsets out of range: %d-%d", w.start, w.end)
		}
	}
}

func TestRRF(t *testing.T) {
	// "b" is mid-rank in two rankers; "a" is top of one only. Fusion should rank
	// "b" above "a" because it accumulates across rankers.
	r1 := []Ranked{{"a", 0.9}, {"b", 0.5}, {"c", 0.1}}
	r2 := []Ranked{{"b", 0.8}, {"d", 0.4}}
	r3 := []Ranked{{"b", 0.7}, {"a", 0.2}}
	order := rrf([][]Ranked{r1, r2, r3}, 60)
	if order[0] != "b" {
		t.Errorf("expected b first (appears high in 3 rankers), got %v", order)
	}
	// determinism: identical input → identical order.
	if got := rrf([][]Ranked{r1, r2, r3}, 60); got[0] != order[0] || len(got) != len(order) {
		t.Errorf("rrf not deterministic")
	}
}

func TestMemStore(t *testing.T) {
	s := NewMemStore()
	s.Upsert(ChunkRecord{Chunk: Chunk{ID: "d#0", DocID: "d", Locator: "1 Intro", Text: "the confidential information clause governs trade secrets"}, Dense: []float32{1, 0, 0}})
	s.Upsert(ChunkRecord{Chunk: Chunk{ID: "d#1", DocID: "d", Locator: "2 Term", Text: "the agreement term is three years and renews annually"}, Dense: []float32{0, 1, 0}})
	if s.Len() != 2 {
		t.Fatalf("len=%d", s.Len())
	}
	// dense: query close to chunk 0's vector ranks it first.
	if r := s.DenseSearch([]float32{0.9, 0.1, 0}, 2); len(r) == 0 || r[0].ChunkID != "d#0" {
		t.Errorf("dense search wrong: %v", r)
	}
	// lexical: BM25 on "term renews" hits chunk 1.
	if r := s.LexicalSearch("term renews annually", 2); len(r) == 0 || r[0].ChunkID != "d#1" {
		t.Errorf("lexical search wrong: %v", r)
	}
	if got := s.Outline("d"); len(got) != 2 {
		t.Errorf("outline len=%d", len(got))
	}
	// delete removes from both indexes.
	s.DeleteDoc("d")
	if s.Len() != 0 || len(s.DenseSearch([]float32{1, 0, 0}, 2)) != 0 || len(s.LexicalSearch("term", 2)) != 0 {
		t.Errorf("delete left residue")
	}
}

func locators(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Locator
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
