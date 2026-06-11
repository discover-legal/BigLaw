// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package clientvoice stores the per-matter advocacy brief pushed by the
// client-facing agent (Remy, via CNTXT) and the notifications external
// agents post to a matter. The brief is surfaced at human gates so the
// reviewer sees the client's stated goals and concerns next to each
// finding — the client's voice travels into the firm's review loop.
package clientvoice

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const maxNotificationsPerMatter = 500

// Store holds client-voice briefs and matter notifications, persisted to a
// single JSON file with the same atomic write pattern as the other stores.
type Store struct {
	mu            sync.Mutex
	voices        map[string]types.ClientVoice          // matterNumber → brief
	notifications map[string][]types.MatterNotification // matterNumber → newest-last
	path          string
}

type persisted struct {
	Voices        map[string]types.ClientVoice          `json:"voices"`
	Notifications map[string][]types.MatterNotification `json:"notifications"`
}

// New creates a Store persisting to path. Call Init before use.
func New(path string) *Store {
	return &Store{
		voices:        map[string]types.ClientVoice{},
		notifications: map[string][]types.MatterNotification{},
		path:          path,
	}
}

// Init loads persisted state. A missing file is not an error.
func (s *Store) Init() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Voices != nil {
		s.voices = p.Voices
	}
	if p.Notifications != nil {
		s.notifications = p.Notifications
	}
	return nil
}

// SetVoice upserts the advocacy brief for a matter.
func (s *Store) SetVoice(v types.ClientVoice) types.ClientVoice {
	v.UpdatedAt = time.Now()
	if v.Source == "" {
		v.Source = "remy"
	}
	s.mu.Lock()
	s.voices[v.MatterNumber] = v
	s.mu.Unlock()
	s.persist()
	return v
}

// Voice returns the advocacy brief for a matter, or nil if none was pushed.
func (s *Store) Voice(matterNumber string) *types.ClientVoice {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.voices[matterNumber]
	if !ok {
		return nil
	}
	cp := v
	return &cp
}

// Notify appends a notification to a matter's feed.
func (s *Store) Notify(matterNumber, source, message string) types.MatterNotification {
	n := types.MatterNotification{
		ID:           uuid.New().String(),
		MatterNumber: matterNumber,
		Source:       source,
		Message:      message,
		At:           time.Now(),
	}
	s.mu.Lock()
	list := append(s.notifications[matterNumber], n)
	if len(list) > maxNotificationsPerMatter {
		list = list[len(list)-maxNotificationsPerMatter:]
	}
	s.notifications[matterNumber] = list
	s.mu.Unlock()
	s.persist()
	return n
}

// Notifications returns up to limit most recent notifications, newest-first.
func (s *Store) Notifications(matterNumber string, limit int) []types.MatterNotification {
	if limit <= 0 || limit > maxNotificationsPerMatter {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.notifications[matterNumber]
	if len(list) > limit {
		list = list[len(list)-limit:]
	}
	out := make([]types.MatterNotification, len(list))
	for i, n := range list {
		out[len(list)-1-i] = n // reverse: newest first
	}
	return out
}

// persist writes atomically (tmp + rename). Errors are swallowed so callers
// don't fail on disk issues — same policy as the other stores.
func (s *Store) persist() {
	s.mu.Lock()
	p := persisted{Voices: s.voices, Notifications: s.notifications}
	data, err := json.MarshalIndent(p, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}
