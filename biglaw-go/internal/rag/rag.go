// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package rag

import (
	"fmt"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
)

// Generator runs a single LLM completion. The caller wires it from the routing/
// provider layer (the same local model the agents use). Used for doc2query at
// ingest and HyDE at query.
type Generator interface {
	Generate(system, user string, maxTokens int) (string, error)
}

// GeneratorFunc adapts a plain function to Generator, so the caller can wire a
// provider closure without declaring a type.
type GeneratorFunc func(system, user string, maxTokens int) (string, error)

func (f GeneratorFunc) Generate(system, user string, maxTokens int) (string, error) {
	return f(system, user, maxTokens)
}

// Service is the hybrid retriever: it ingests documents into the ChunkStore and
// answers queries by fusing dense, HyDE, anticipated-question, and BM25 rankings.
type Service struct {
	store     ChunkStore
	embed     *embeddings.Client
	gen       Generator
	capTokens int
	docQ      int // doc2query questions generated per chunk
}

// New builds a Service over a ChunkStore. gen may be nil — doc2query and HyDE then
// degrade to no-ops (dense + BM25 still work), so retrieval never depends on a
// reachable model.
func New(store ChunkStore, embed *embeddings.Client, gen Generator) *Service {
	return &Service{store: store, embed: embed, gen: gen, capTokens: DefaultChunkTokens, docQ: 4}
}

const (
	doc2querySystem = "You generate the questions a passage answers. You output only short, specific questions, one per line, no numbering, no preamble."
	hydeSystem      = "You write a short, plausible passage from a legal document that would directly answer a question, in the register of the source. Output only the passage."
	rrfK            = 60.0 // Reciprocal Rank Fusion damping (standard value)
)

// IngestDoc chunks a document (section-aware, size-capped), embeds each chunk and
// its doc2query questions, indexes the chunk for BM25, and stores it. Replaces any
// prior chunks for docID (clean re-ingest).
func (s *Service) IngestDoc(docID, title, content string) {
	chunks := Chunkify(docID, title, content, s.capTokens)
	s.store.DeleteDoc(docID)
	if len(chunks) == 0 {
		return
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	dense := s.embedBatch(texts)
	for i, c := range chunks {
		s.store.Upsert(ChunkRecord{Chunk: c, Dense: dense[i]})
	}
	// doc2query enrichment runs in the BACKGROUND: it makes an LLM call per chunk
	// and must not block the upload. Dense + BM25 retrieval already works the moment
	// this returns; the anticipated-question vectors attach as they complete.
	if s.gen != nil {
		go s.enrichQuestions(chunks)
	}
}

// enrichQuestions generates + embeds doc2query questions per chunk and attaches
// them to the already-indexed chunk records (background pass).
func (s *Service) enrichQuestions(chunks []Chunk) {
	for _, c := range chunks {
		if qs := s.doc2query(c.Text); len(qs) > 0 {
			s.store.AddQuestions(c.ID, s.embedBatch(qs))
		}
	}
}

// Search runs the hybrid retrieval and returns the top-K chunks (verbatim text +
// section locator), ready to feed the staged evidence extractor.
func (s *Service) Search(query string, topK int) []Chunk {
	if topK <= 0 {
		topK = 6
	}
	pool := topK * 4 // gather more per ranker than we keep, so fusion has signal

	// HyDE is intentionally OFF the hot query path: on a single local GPU it adds
	// an LLM call per search and pushes agents past the round timeout. dense(query)
	// + anticipated-question (doc2query) + BM25 already give a strong hybrid; the
	// hyde() method is retained for hardware that can afford it.
	var rankings [][]Ranked
	if qVec := s.embedOne(query); len(qVec) > 0 {
		rankings = append(rankings, s.store.DenseSearch(qVec, pool))    // dense(query)
		rankings = append(rankings, s.store.QuestionSearch(qVec, pool)) // anticipated-question (doc2query)
	}
	rankings = append(rankings, s.store.LexicalSearch(query, pool)) // BM25

	out := make([]Chunk, 0, topK)
	for _, id := range rrf(rankings, rrfK) {
		if c, ok := s.store.Get(id); ok {
			out = append(out, c)
			if len(out) >= topK {
				break
			}
		}
	}
	return out
}

// doc2query asks the model what questions a chunk answers (ingest-side vocabulary
// bridge). Returns nil when no generator is wired.
func (s *Service) doc2query(text string) []string {
	if s.gen == nil {
		return nil
	}
	user := fmt.Sprintf("PASSAGE:\n%s\n\nList %d short, specific questions this passage directly answers.", text, s.docQ)
	out, err := s.gen.Generate(doc2querySystem, user, 220)
	if err != nil {
		return nil
	}
	return splitLines(out, s.docQ)
}

// hyde drafts a hypothetical answering passage and returns it for embedding
// (query-side vocabulary bridge). Falls back to the raw query on any failure.
func (s *Service) hyde(query string) string {
	if s.gen == nil {
		return query
	}
	user := fmt.Sprintf("Question: %s\n\nWrite a short (2-3 sentence) hypothetical passage from a legal document that would directly answer it.", query)
	out, err := s.gen.Generate(hydeSystem, user, 200)
	if err != nil || strings.TrimSpace(out) == "" {
		return query
	}
	return out
}

// ── Outline / read-section accessors for the navigation tools ──────────────────

func (s *Service) Outline(docID string) []Chunk          { return s.store.Outline(docID) }
func (s *Service) Section(docID, locator string) []Chunk { return s.store.BySection(docID, locator) }
func (s *Service) Chunk(chunkID string) (Chunk, bool)    { return s.store.Get(chunkID) }

// ── internals ──────────────────────────────────────────────────────────────────

func (s *Service) embedBatch(texts []string) [][]float32 {
	out := make([][]float32, len(texts))
	if res, err := s.embed.EmbedBatch(texts); err == nil && len(res) == len(texts) {
		for i := range res {
			out[i] = res[i].Embedding
		}
		return out
	}
	for i, t := range texts {
		out[i] = s.embedOne(t)
	}
	return out
}

func (s *Service) embedOne(text string) []float32 {
	if r, err := s.embed.Embed(text); err == nil && r != nil {
		return r.Embedding
	}
	return nil
}

// rrf fuses several ranked lists by Reciprocal Rank Fusion: a chunk's score is the
// sum over rankers of 1/(k + rank). Rank-based, so heterogeneous scores (cosine vs
// BM25) combine soundly. Returns chunk ids best-first (ties by id, deterministic).
func rrf(rankings [][]Ranked, k float64) []string {
	score := map[string]float64{}
	for _, r := range rankings {
		for rank, item := range r {
			score[item.ChunkID] += 1.0 / (k + float64(rank+1))
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

// splitLines extracts up to max non-empty lines, stripping bullets/numbering.
func splitLines(s string, max int) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		ln = strings.TrimSpace(strings.TrimLeft(ln, "-*•0123456789.) \t"))
		if ln != "" {
			out = append(out, ln)
			if len(out) >= max {
				break
			}
		}
	}
	return out
}
