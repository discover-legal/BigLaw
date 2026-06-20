// SPDX-License-Identifier: AGPL-3.0-only
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
	ID        string // "<docID>#<n>"
	DocID     string
	DocTitle  string
	Locator   string // section path, e.g. "ARTICLE I > 1.2 Definitions"
	Text      string // verbatim chunk text
	ByteStart int    // offset into the document
	ByteEnd   int
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
			walk(s.Children, p)
		}
	}
	walk(pageindex.Parse(content), "")
	for i := range chunks {
		chunks[i].ID = fmt.Sprintf("%s#%d", docID, i)
	}
	return chunks
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
