// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package rag

import (
	"sort"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/bm25"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
)

// Ranked is one scored chunk id from a single ranker (dense / question / BM25),
// before fusion.
type Ranked struct {
	ChunkID string
	Score   float64
}

// ChunkRecord is a chunk plus the vectors indexed for it: the dense embedding of
// its text, and the embeddings of the anticipated questions it answers (doc2query).
type ChunkRecord struct {
	Chunk
	Dense     []float32
	Questions [][]float32
}

// ChunkStore is the retrieval-index seam. The in-process implementation
// (NewMemStore) backs it now; the EvidenceGraph will satisfy the same interface
// later, selected by deployment profile.
type ChunkStore interface {
	Upsert(rec ChunkRecord)
	AddQuestions(chunkID string, qs [][]float32) // attach doc2query vectors post-hoc
	DeleteDoc(docID string)
	// The three rankers fused by the service. Each returns up to topK chunk ids
	// ranked best-first; a chunk absent from a ranker is simply unranked there.
	DenseSearch(vec []float32, topK int) []Ranked    // cosine over chunk text vectors
	QuestionSearch(vec []float32, topK int) []Ranked // cosine over doc2query vectors → chunk
	LexicalSearch(query string, topK int) []Ranked   // BM25 over chunk text
	Get(chunkID string) (Chunk, bool)
	Outline(docID string) []Chunk            // a document's chunks in order
	BySection(docID, locator string) []Chunk // chunks whose locator == locator
	Len() int
}

type record struct {
	Chunk
	dense     []float32
	questions [][]float32
}

type memStore struct {
	mu    sync.RWMutex
	recs  map[string]*record
	byDoc map[string][]string // docID → chunkIDs in insertion order
	bm    *bm25.Index
}

// NewMemStore returns an in-process ChunkStore: brute-force cosine over chunk and
// question vectors for the dense rankers, and a BM25 index for the lexical ranker.
func NewMemStore() ChunkStore {
	return &memStore{recs: map[string]*record{}, byDoc: map[string][]string{}, bm: bm25.New()}
}

func (m *memStore) Upsert(rec ChunkRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.recs[rec.ID]; !exists {
		m.byDoc[rec.DocID] = append(m.byDoc[rec.DocID], rec.ID)
	}
	m.recs[rec.ID] = &record{Chunk: rec.Chunk, dense: rec.Dense, questions: rec.Questions}
	m.bm.Add(rec.ID, rec.indexText()) // BM25 over EmbedText for table rows, else Text
}

func (m *memStore) AddQuestions(chunkID string, qs [][]float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.recs[chunkID]; ok {
		r.questions = qs
	}
}

func (m *memStore) DeleteDoc(docID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.byDoc[docID] {
		delete(m.recs, id)
		m.bm.Remove(id)
	}
	delete(m.byDoc, docID)
}

func (m *memStore) DenseSearch(vec []float32, topK int) []Ranked {
	if len(vec) == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	scored := make([]Ranked, 0, len(m.recs))
	for id, r := range m.recs {
		if len(r.dense) == 0 {
			continue
		}
		scored = append(scored, Ranked{ChunkID: id, Score: embeddings.CosineSimilarity(vec, r.dense)})
	}
	return topRanked(scored, topK)
}

func (m *memStore) QuestionSearch(vec []float32, topK int) []Ranked {
	if len(vec) == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	scored := make([]Ranked, 0, len(m.recs))
	for id, r := range m.recs {
		best := -2.0
		for _, q := range r.questions {
			if s := embeddings.CosineSimilarity(vec, q); s > best {
				best = s
			}
		}
		if best > -2.0 {
			scored = append(scored, Ranked{ChunkID: id, Score: best})
		}
	}
	return topRanked(scored, topK)
}

func (m *memStore) LexicalSearch(query string, topK int) []Ranked {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hits := m.bm.Search(query, topK)
	out := make([]Ranked, len(hits))
	for i, h := range hits {
		out[i] = Ranked{ChunkID: h.ID, Score: h.Score}
	}
	return out
}

func (m *memStore) Get(chunkID string) (Chunk, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if r, ok := m.recs[chunkID]; ok {
		return r.Chunk, true
	}
	return Chunk{}, false
}

func (m *memStore) Outline(docID string) []Chunk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := m.byDoc[docID]
	out := make([]Chunk, 0, len(ids))
	for _, id := range ids {
		if r, ok := m.recs[id]; ok {
			out = append(out, r.Chunk)
		}
	}
	return out
}

func (m *memStore) BySection(docID, locator string) []Chunk {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Chunk
	for _, id := range m.byDoc[docID] {
		if r, ok := m.recs[id]; ok && r.Locator == locator {
			out = append(out, r.Chunk)
		}
	}
	return out
}

func (m *memStore) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.recs)
}

// topRanked sorts by descending score (ties by chunk id for determinism) and
// truncates to topK.
func topRanked(scored []Ranked, topK int) []Ranked {
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].ChunkID < scored[j].ChunkID
	})
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}
