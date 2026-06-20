// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package writer turns a task's findings into the final client-ready deliverable
// without ever holding all findings in one context window. It is the writing-stage
// analogue of the staged evidence extractor: findings are indexed (dense + BM25),
// clustered into topic sections, and drafted by scoped passes that each pull only
// their section's findings via the index — so a small local model (e.g. an 8K
// window) can synthesise an arbitrary number of findings. Reuses the hybrid-RAG
// primitives (embeddings + bm25 + RRF) over findings; the EvidenceGraph will back
// the same FindingStore seam later.
package writer

import (
	"sort"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/bm25"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
)

// Finding is the writer's view of one finding: a grounded conclusion plus the
// verbatim evidence and citation that support it.
type Finding struct {
	ID       string
	Content  string // the analytical conclusion
	Evidence string // a representative verbatim quote (locked)
	Source   string // citation source id
	Agent    string
	Round    int
	Grounded bool   // false → must be caveated as unverified in the draft
	Note     string // evidence note for unverified findings
}

// indexText is what we embed / BM25-index for a finding: its conclusion plus the
// evidence, so a section query matches on both the claim and the quoted facts.
func (f Finding) indexText() string {
	if f.Evidence == "" {
		return f.Content
	}
	return f.Content + " " + f.Evidence
}

// FindingIndex is an in-process dense+lexical index over a task's findings. It is
// the FindingStore seam the section drafters query; NewFindingIndex backs it with
// brute-force cosine + BM25 now, the EvidenceGraph later.
type FindingIndex struct {
	mu    sync.RWMutex
	items map[string]*Finding
	dense map[string][]float32
	order []string // insertion order, stable
	bm    *bm25.Index
	embed *embeddings.Client
}

// NewFindingIndex builds and populates the index from a finding set, embedding
// each finding's text in one batch. embed may be nil — search then degrades to
// BM25-only (deterministic, no model dependency).
func NewFindingIndex(embed *embeddings.Client, findings []Finding) *FindingIndex {
	ix := &FindingIndex{
		items: make(map[string]*Finding, len(findings)),
		dense: make(map[string][]float32, len(findings)),
		bm:    bm25.New(),
		embed: embed,
	}
	texts := make([]string, len(findings))
	for i := range findings {
		texts[i] = findings[i].indexText()
	}
	var vecs []embeddings.EmbeddingResult
	if embed != nil && len(texts) > 0 {
		if res, err := embed.EmbedBatch(texts); err == nil && len(res) == len(texts) {
			vecs = res
		}
	}
	for i := range findings {
		f := findings[i]
		ix.items[f.ID] = &f
		ix.order = append(ix.order, f.ID)
		ix.bm.Add(f.ID, f.indexText())
		if vecs != nil {
			ix.dense[f.ID] = vecs[i].Embedding
		}
	}
	return ix
}

// All returns every finding in insertion order.
func (ix *FindingIndex) All() []Finding {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]Finding, 0, len(ix.order))
	for _, id := range ix.order {
		out = append(out, *ix.items[id])
	}
	return out
}

// Get returns one finding by id.
func (ix *FindingIndex) Get(id string) (Finding, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if f, ok := ix.items[id]; ok {
		return *f, true
	}
	return Finding{}, false
}

func (ix *FindingIndex) Len() int { ix.mu.RLock(); defer ix.mu.RUnlock(); return len(ix.order) }

// vec returns the stored embedding for a finding (nil if none).
func (ix *FindingIndex) vec(id string) []float32 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.dense[id]
}

// Search returns up to topK findings most relevant to query, fusing dense
// (cosine over finding embeddings) and lexical (BM25) rankings via RRF — the same
// hybrid as search_chunks, over findings. Falls back to BM25-only when no embedder.
func (ix *FindingIndex) Search(query string, topK int) []Finding {
	if strings.TrimSpace(query) == "" || ix.Len() == 0 {
		return nil
	}
	var rankings [][]ranked

	if ix.embed != nil {
		if r, err := ix.embed.Embed(query); err == nil && r != nil && len(r.Embedding) > 0 {
			rankings = append(rankings, ix.denseRank(r.Embedding, topK*4))
		}
	}
	rankings = append(rankings, ix.lexRank(query, topK*4))

	out := make([]Finding, 0, topK)
	for _, id := range rrf(rankings, rrfK) {
		if f, ok := ix.Get(id); ok {
			out = append(out, f)
			if len(out) >= topK {
				break
			}
		}
	}
	return out
}

// SearchScoped is Search restricted to a set of finding ids (a drafter's section
// partition): the agent still tool-calls and re-ranks, but only within its
// assigned findings — guaranteeing exactly-once coverage across drafters.
func (ix *FindingIndex) SearchScoped(query string, topK int, allow map[string]bool) []Finding {
	if len(allow) == 0 {
		return nil
	}
	// Small scopes (tight agents): if no query or no embedder, just return the
	// allowed findings in order — ranking adds nothing for a handful of items.
	full := ix.Search(query, ix.Len()) // ranked over everything
	out := make([]Finding, 0, topK)
	for _, f := range full {
		if allow[f.ID] {
			out = append(out, f)
			if topK > 0 && len(out) >= topK {
				return out
			}
		}
	}
	// Append any allowed findings the search didn't surface (e.g. empty query),
	// preserving coverage.
	if topK <= 0 || len(out) < topK {
		seen := map[string]bool{}
		for _, f := range out {
			seen[f.ID] = true
		}
		for _, f := range ix.All() {
			if allow[f.ID] && !seen[f.ID] {
				out = append(out, f)
				if topK > 0 && len(out) >= topK {
					break
				}
			}
		}
	}
	return out
}

func (ix *FindingIndex) denseRank(q []float32, topK int) []ranked {
	ix.mu.RLock()
	scored := make([]ranked, 0, len(ix.dense))
	for id, v := range ix.dense {
		if len(v) == 0 {
			continue
		}
		scored = append(scored, ranked{id, embeddings.CosineSimilarity(q, v)})
	}
	ix.mu.RUnlock()
	return topRanked(scored, topK)
}

func (ix *FindingIndex) lexRank(query string, topK int) []ranked {
	hits := ix.bm.Search(query, topK)
	out := make([]ranked, len(hits))
	for i, h := range hits {
		out[i] = ranked{h.ID, h.Score}
	}
	return out
}

// ── ranking helpers (RRF fuses dense + lexical; identical shape to rag) ─────────

type ranked struct {
	id    string
	score float64
}

const rrfK = 60.0

func topRanked(s []ranked, topK int) []ranked {
	sort.Slice(s, func(i, j int) bool {
		if s[i].score != s[j].score {
			return s[i].score > s[j].score
		}
		return s[i].id < s[j].id
	})
	if topK > 0 && len(s) > topK {
		s = s[:topK]
	}
	return s
}

func rrf(rankings [][]ranked, k float64) []string {
	score := map[string]float64{}
	for _, r := range rankings {
		for rank, item := range r {
			score[item.id] += 1.0 / (k + float64(rank+1))
		}
	}
	ids := make([]string, 0, len(score))
	for id := range score {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if score[ids[i]] != score[ids[j]] {
			return score[ids[i]] > score[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}
