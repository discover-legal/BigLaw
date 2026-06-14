// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// MemoryRepo is a non-persistent DocRepository (DB_BACKEND=memory) — the
// original Go-port behaviour, kept for tests and ephemeral runs.
type MemoryRepo struct {
	mu          sync.RWMutex
	docs        map[string]types.Document
	order       []string
	attachments map[string]types.Attachment
}

func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{docs: map[string]types.Document{}, attachments: map[string]types.Attachment{}}
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
