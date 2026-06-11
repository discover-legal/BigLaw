// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// In-process knowledge/document store with cosine-similarity semantic search.

package knowledge

import (
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type Store struct {
	mu     sync.RWMutex
	docs   []types.Document
	embedC *embeddings.Client
}

func NewStore(embedC *embeddings.Client) *Store {
	return &Store{embedC: embedC}
}

func (s *Store) Init() error { return nil }

// Ingest adds or updates a document, computing its embedding.
func (s *Store) Ingest(doc types.Document) (*types.Document, error) {
	if doc.ID == "" {
		doc.ID = uuid.New().String()
	}
	// Embed a representative chunk (title + first 2000 chars)
	text := doc.Title + " " + doc.Content
	if len(text) > 2000 {
		text = strutil.Truncate(text, 2000)
	}
	if result, err := s.embedC.Embed(text); err == nil && result != nil {
		doc.Embedding = result.Embedding
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.docs {
		if d.ID == doc.ID {
			s.docs[i] = doc
			return &doc, nil
		}
	}
	s.docs = append(s.docs, doc)
	return &doc, nil
}

type SearchOpts struct {
	OwnerID string
	TopK    int
}

func (s *Store) Search(query string, opts SearchOpts) ([]types.SearchResult, error) {
	qResult, err := s.embedC.Embed(query)
	if err != nil || qResult == nil {
		return s.fallback(query, opts), nil
	}

	s.mu.RLock()
	type scored struct {
		doc   types.Document
		score float64
	}
	var candidates []scored
	for _, d := range s.docs {
		if opts.OwnerID != "" && d.OwnerID != "" && d.OwnerID != opts.OwnerID {
			continue
		}
		if len(d.Embedding) == 0 {
			continue
		}
		score := embeddings.CosineSimilarity(qResult.Embedding, d.Embedding)
		candidates = append(candidates, scored{doc: d, score: score})
	}
	s.mu.RUnlock()

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	k := opts.TopK
	if k <= 0 {
		k = 5
	}
	if k > len(candidates) {
		k = len(candidates)
	}
	out := make([]types.SearchResult, k)
	for i, c := range candidates[:k] {
		excerpt := c.doc.Content
		if len(excerpt) > 300 {
			excerpt = strutil.Truncate(excerpt, 300) + "…"
		}
		out[i] = types.SearchResult{Document: c.doc, Score: c.score, Excerpt: excerpt}
	}
	return out, nil
}

func (s *Store) GetFullText(docID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, d := range s.docs {
		if d.ID == docID {
			return d.Content, nil
		}
	}
	return "", nil
}

func (s *Store) GetByID(docID string) *types.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, d := range s.docs {
		if d.ID == docID {
			cp := s.docs[i]
			return &cp
		}
	}
	return nil
}

func (s *Store) ListAll() []types.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.Document, len(s.docs))
	copy(out, s.docs)
	return out
}

func (s *Store) fallback(query string, opts SearchOpts) []types.SearchResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := opts.TopK
	if k <= 0 {
		k = 5
	}
	var out []types.SearchResult
	for _, d := range s.docs {
		if opts.OwnerID != "" && d.OwnerID != "" && d.OwnerID != opts.OwnerID {
			continue
		}
		excerpt := d.Content
		if len(excerpt) > 300 {
			excerpt = strutil.Truncate(excerpt, 300) + "…"
		}
		out = append(out, types.SearchResult{Document: d, Score: 0.5, Excerpt: excerpt})
		if len(out) >= k {
			break
		}
	}
	return out
}

// Adapter satisfies agents.KnowledgeStore using the flat-argument interface
// that agents/base.go expects.
type Adapter struct {
	store *Store
}

func NewAdapter(store *Store) *Adapter {
	return &Adapter{store: store}
}

func (a *Adapter) Search(query, ownerID string, topK int) ([]types.SearchResult, error) {
	return a.store.Search(query, SearchOpts{OwnerID: ownerID, TopK: topK})
}

func (a *Adapter) GetFullText(docID string) (string, error) {
	return a.store.GetFullText(docID)
}
