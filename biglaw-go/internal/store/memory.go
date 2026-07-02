// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// MemoryRepo is a non-persistent DocRepository (DB_BACKEND=memory) — the
// original Go-port behaviour, kept for tests and ephemeral runs.
type MemoryRepo struct {
	mu          sync.RWMutex
	docs        map[string]types.Document
	order       []string
	attachments map[string]types.Attachment
	reviews     map[string]memReview
	versions    map[string]DocumentVersion
	verOrder    []string // insertion order — the tie-break FindVersionBy* recency needs
}

// memReview is one stored tabular-review payload.
type memReview struct {
	createdAt time.Time
	payload   []byte
}

func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{
		docs:        map[string]types.Document{},
		attachments: map[string]types.Attachment{},
		reviews:     map[string]memReview{},
		versions:    map[string]DocumentVersion{},
	}
}

func (m *MemoryRepo) Backend() string { return "memory" }
func (m *MemoryRepo) Close() error    { return nil }

// The memory backend is local single-tenant; it ignores Identity (the
// application layer enforces access). Signatures match the interface.
func (m *MemoryRepo) Upsert(_ context.Context, doc types.Document) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[doc.ID]; !ok {
		m.order = append(m.order, doc.ID)
	}
	doc.Embedding = nil // never persist the vector
	m.docs[doc.ID] = doc
	return nil
}

func (m *MemoryRepo) GetByID(_ context.Context, id string) (*types.Document, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.docs[id]
	if !ok {
		return nil, false, nil
	}
	cp := d
	return &cp, true, nil
}

func (m *MemoryRepo) List(_ context.Context) ([]types.Document, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]types.Document, 0, len(m.order))
	for _, id := range m.order {
		out = append(out, m.docs[id])
	}
	return out, nil
}

func (m *MemoryRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.docs[id]; ok {
		delete(m.docs, id)
		for i, oid := range m.order {
			if oid == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	return nil
}

func (m *MemoryRepo) AddAttachment(_ context.Context, att types.Attachment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachments[att.ID] = att
	return nil
}

func (m *MemoryRepo) ListAttachments(_ context.Context, docID string) ([]types.Attachment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []types.Attachment
	for _, a := range m.attachments {
		if a.DocID == docID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *MemoryRepo) GetAttachment(_ context.Context, id string) (*types.Attachment, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.attachments[id]
	if !ok {
		return nil, false, nil
	}
	cp := a
	return &cp, true, nil
}

func (m *MemoryRepo) DeleteAttachment(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attachments, id)
	return nil
}

// ─── ReviewRepository ────────────────────────────────────────────────────────────

func (m *MemoryRepo) PutReview(_ context.Context, id string, createdAt time.Time, payload []byte) error {
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviews[id] = memReview{createdAt: createdAt, payload: cp}
	return nil
}

func (m *MemoryRepo) GetReview(_ context.Context, id string) ([]byte, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rev, ok := m.reviews[id]
	if !ok {
		return nil, false, nil
	}
	cp := make([]byte, len(rev.payload))
	copy(cp, rev.payload)
	return cp, true, nil
}

// ─── VersionRepository ───────────────────────────────────────────────────────────

func (m *MemoryRepo) PutVersion(_ context.Context, v DocumentVersion) error {
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now()
	}
	v.Decisions = append([]byte(nil), v.Decisions...)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.versions[v.ID]; !ok {
		m.verOrder = append(m.verOrder, v.ID)
	}
	m.versions[v.ID] = v
	return nil
}

func (m *MemoryRepo) GetVersion(_ context.Context, id string) (*DocumentVersion, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.versions[id]
	if !ok {
		return nil, false, nil
	}
	cp := v
	cp.Decisions = append([]byte(nil), v.Decisions...)
	return &cp, true, nil
}

func (m *MemoryRepo) ListLineage(_ context.Context, lineageID string) ([]DocumentVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []DocumentVersion
	for _, id := range m.verOrder { // insertion order breaks round ties
		if v := m.versions[id]; v.LineageID == lineageID {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Round < out[b].Round })
	return out, nil
}

func (m *MemoryRepo) FindVersionByHash(_ context.Context, contentHash string) (*DocumentVersion, bool, error) {
	return m.findVersion(func(v DocumentVersion) bool { return v.ContentHash == contentHash })
}

func (m *MemoryRepo) FindVersionByPath(_ context.Context, path string) (*DocumentVersion, bool, error) {
	return m.findVersion(func(v DocumentVersion) bool { return v.Path == path })
}

// findVersion returns the most recently registered version matching the
// predicate (walks the insertion order backwards).
func (m *MemoryRepo) findVersion(match func(DocumentVersion) bool) (*DocumentVersion, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := len(m.verOrder) - 1; i >= 0; i-- {
		if v := m.versions[m.verOrder[i]]; match(v) {
			cp := v
			cp.Decisions = append([]byte(nil), v.Decisions...)
			return &cp, true, nil
		}
	}
	return nil, false, nil
}
