// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Pending-drafts store for the send_gate write-mode. A draft awaiting human
// approval is persisted here with an ID so it can be listed and approved/cancelled
// by reference, rather than re-submitting the full body. Backed by a single JSON
// file (atomic write), like the other small stores in the port.
package lpm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PendingStatus is the lifecycle state of a pending draft.
type PendingStatus string

const (
	PendingOpen      PendingStatus = "pending"
	PendingSent      PendingStatus = "sent"
	PendingCancelled PendingStatus = "cancelled"
)

// PendingDraft is a send_gate draft awaiting human approval.
type PendingDraft struct {
	ID           string        `json:"id"`
	MatterNumber string        `json:"matterNumber"`
	To           []string      `json:"to"`
	Subject      string        `json:"subject"`
	Body         string        `json:"body"`
	CreatedAt    string        `json:"createdAt"`
	CreatedBy    string        `json:"createdBy"`
	Status       PendingStatus `json:"status"`
	ResolvedAt   string        `json:"resolvedAt,omitempty"`
	ResolvedBy   string        `json:"resolvedBy,omitempty"`
}

// PendingStore persists pending drafts to a JSON file.
type PendingStore struct {
	mu     sync.Mutex
	path   string
	drafts []PendingDraft
}

// NewPendingStore returns a store backed by path.
func NewPendingStore(path string) *PendingStore {
	return &PendingStore{path: path}
}

// Init loads any persisted drafts (missing file is not an error).
func (s *PendingStore) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &s.drafts)
}

// Add records a new pending draft and returns its ID.
func (s *PendingStore) Add(d Draft, createdBy string) (string, error) {
	pd := PendingDraft{
		ID:           uuid.New().String(),
		MatterNumber: d.MatterNumber,
		To:           d.To,
		Subject:      d.Subject,
		Body:         d.Body,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		CreatedBy:    createdBy,
		Status:       PendingOpen,
	}
	s.mu.Lock()
	s.drafts = append(s.drafts, pd)
	err := s.persistLocked()
	s.mu.Unlock()
	return pd.ID, err
}

// ListOpen returns drafts still awaiting approval, newest first.
func (s *PendingStore) ListOpen() []PendingDraft {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingDraft
	for i := len(s.drafts) - 1; i >= 0; i-- {
		if s.drafts[i].Status == PendingOpen {
			out = append(out, s.drafts[i])
		}
	}
	return out
}

// Get returns a copy of the draft with the given ID, or false.
func (s *PendingStore) Get(id string) (PendingDraft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.drafts {
		if d.ID == id {
			return d, true
		}
	}
	return PendingDraft{}, false
}

// Resolve marks an open draft as sent or cancelled. Returns false if it is not
// found or is already resolved.
func (s *PendingStore) Resolve(id string, status PendingStatus, by string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.drafts {
		if s.drafts[i].ID == id {
			if s.drafts[i].Status != PendingOpen {
				return false
			}
			s.drafts[i].Status = status
			s.drafts[i].ResolvedAt = time.Now().UTC().Format(time.RFC3339)
			s.drafts[i].ResolvedBy = by
			_ = s.persistLocked()
			return true
		}
	}
	return false
}

func (s *PendingStore) persistLocked() error {
	data, err := json.MarshalIndent(s.drafts, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
