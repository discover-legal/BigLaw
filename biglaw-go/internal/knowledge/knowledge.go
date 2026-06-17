// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// In-process knowledge/document store with cosine-similarity semantic search.

package knowledge

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type Store struct {
	mu     sync.RWMutex
	docs   []types.Document
	embedC *embeddings.Client
	// repo is the durable backend (SQLite/Postgres). nil keeps the legacy
	// in-memory-only behaviour (used by tests and DB_BACKEND=memory).
	repo store.DocRepository
}

func NewStore(embedC *embeddings.Client) *Store {
	return &Store{embedC: embedC}
}

// NewStoreWithRepo wires a durable repository. Call Load after construction to
// hydrate the in-memory vector index from persisted documents.
func NewStoreWithRepo(embedC *embeddings.Client, repo store.DocRepository) *Store {
	return &Store{embedC: embedC, repo: repo}
}

func (s *Store) Init() error { return s.Load() }

// Load hydrates the in-memory index from the durable repository, recomputing
// embeddings where an embedder is available (the vector is never persisted).
// A no-op when no repository is wired. Runs as the system principal (boot-time
// trusted load) so RLS does not filter the hydrate.
func (s *Store) Load() error {
	if s.repo == nil {
		return nil
	}
	docs, err := s.repo.List(store.WithSystem(context.Background()))
	if err != nil {
		return err
	}
	for i := range docs {
		if s.embedC != nil {
			text := docs[i].Title + " " + docs[i].Content
			if len(text) > 2000 {
				text = strutil.Truncate(text, 2000)
			}
			if res, eerr := s.embedC.Embed(text); eerr == nil && res != nil {
				docs[i].Embedding = res.Embedding
			}
		}
	}
	s.mu.Lock()
	s.docs = docs
	s.mu.Unlock()
	slog.Info("knowledge: loaded documents from store", "backend", s.repo.Backend(), "count", len(docs))
	return nil
}

// Ingest adds or updates a document, computing its embedding. The ctx carries
// the request identity for the durable write (RLS default-deny on Postgres: a
// lawyer may only write rows they own; system/partner may write any). Use
// store.WithIdentity for user uploads and store.WithSystem for internal callers.
func (s *Store) Ingest(ctx context.Context, doc types.Document) (*types.Document, error) {
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

	// Write through to the durable repository first; on failure, surface the
	// error rather than silently keeping a memory-only document.
	if s.repo != nil {
		if err := s.repo.Upsert(ctx, doc); err != nil {
			return nil, err
		}
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

// maxTopK caps how large a result slice a caller-supplied TopK can allocate,
// independent of how many candidates exist — a hard upper bound against a
// crafted request driving an excessive allocation.
const maxTopK = 1000

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
	if k > maxTopK { // explicit upper bound on a caller-supplied size (anti-DoS)
		k = maxTopK
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

// ─── Attachments (binary artifacts; bytes live in the blob store) ───────────────

// AddAttachment persists attachment metadata under the ctx identity (RLS).
func (s *Store) AddAttachment(ctx context.Context, att types.Attachment) error {
	if s.repo == nil {
		return nil
	}
	return s.repo.AddAttachment(ctx, att)
}

// ListAttachments returns a document's attachments visible to the ctx identity.
func (s *Store) ListAttachments(ctx context.Context, docID string) ([]types.Attachment, error) {
	if s.repo == nil {
		return nil, nil
	}
	return s.repo.ListAttachments(ctx, docID)
}

// GetAttachment returns one attachment by ID if visible to the ctx identity.
func (s *Store) GetAttachment(ctx context.Context, id string) (*types.Attachment, bool, error) {
	if s.repo == nil {
		return nil, false, nil
	}
	return s.repo.GetAttachment(ctx, id)
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

// GetVisible returns a document by ID if the ctx identity may see it (database
// RLS on Postgres; falls back to the in-memory copy when no repository).
func (s *Store) GetVisible(ctx context.Context, docID string) (*types.Document, bool, error) {
	if s.repo == nil {
		d := s.GetByID(docID)
		return d, d != nil, nil
	}
	return s.repo.GetByID(ctx, docID)
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

// ListAll returns every in-memory document, unfiltered. Internal/system use
// only (e.g. analytics, briefing) — user-facing listing must use ListVisible so
// the database row-level-security layer applies.
func (s *Store) ListAll() []types.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.Document, len(s.docs))
	copy(out, s.docs)
	return out
}

// ListVisible returns documents the ctx identity may see, sourced from the
// durable repository so database RLS (default-deny on Postgres) applies as a
// layer beneath the caller's own access checks. Falls back to the in-memory
// list when no repository is wired.
//
// Note: semantic Search runs on the in-process vector index and is governed by
// the application-layer owner filter (SearchOpts.OwnerID), not database RLS —
// the vector index cannot be RLS-scoped at the database. Direct listing/reads
// (this method) and all writes are the DB-RLS-enforced paths.
func (s *Store) ListVisible(ctx context.Context) ([]types.Document, error) {
	if s.repo == nil {
		return s.ListAll(), nil
	}
	return s.repo.List(ctx)
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
