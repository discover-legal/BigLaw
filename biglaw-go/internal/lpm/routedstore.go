// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// RoutedStore is an append-only record of which inbound emails were routed to
// which matter. It deliberately persists metadata only — message id, subject,
// sender, provider, the routing decision and its confidence — never the body or
// snippet, so the corpus stays low-sensitivity while still feeding the daily
// status-report deltas and supporting dedup across polls.
package lpm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RoutingMethod records how a message was assigned to a matter.
type RoutingMethod string

const (
	RouteRegex    RoutingMethod = "regex"    // matched a matter ref in the subject
	RouteModel    RoutingMethod = "model"    // assigned by the small-model router
	RouteUnrouted RoutingMethod = "unrouted" // no confident matter assignment
)

// RoutedEmail is the metadata-only record of one routing decision.
type RoutedEmail struct {
	MessageID    string        `json:"messageId"`
	Subject      string        `json:"subject"`
	From         string        `json:"from"`
	ReceivedAt   string        `json:"receivedAt"`
	Provider     string        `json:"provider"`
	MatterNumber string        `json:"matterNumber,omitempty"`
	Confidence   float64       `json:"confidence"`
	Method       RoutingMethod `json:"method"`
	RoutedAt     string        `json:"routedAt"`
}

// RoutedStore persists RoutedEmail records as newline-delimited JSON and keeps an
// in-memory set of seen message IDs for dedup.
type RoutedStore struct {
	mu   sync.Mutex
	path string
	seen map[string]bool
}

// NewRoutedStore returns a store backed by the JSONL file at path.
func NewRoutedStore(path string) *RoutedStore {
	return &RoutedStore{path: path, seen: map[string]bool{}}
}

// Init loads existing records to repopulate the seen-set after a restart.
func (s *RoutedStore) Init() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r RoutedEmail
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		if r.MessageID != "" {
			s.seen[r.MessageID] = true
		}
	}
	return sc.Err()
}

// Seen reports whether a message ID has already been routed.
func (s *RoutedStore) Seen(messageID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[messageID]
}

// Append records a routing decision and marks the message seen.
func (s *RoutedStore) Append(r RoutedEmail) error {
	if r.RoutedAt == "" {
		r.RoutedAt = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	if r.MessageID != "" {
		s.seen[r.MessageID] = true
	}
	return nil
}

// CountForMatter returns how many emails were routed to a matter since the cutoff.
// It powers the EmailsRouted delta in the daily status report.
func (s *RoutedStore) CountForMatter(matter string, since time.Time) int {
	all := s.all()
	n := 0
	for _, r := range all {
		if r.MatterNumber != matter || r.Method == RouteUnrouted {
			continue
		}
		if t, err := time.Parse(time.RFC3339, r.RoutedAt); err == nil && !t.After(since) {
			continue
		}
		n++
	}
	return n
}

func (s *RoutedStore) all() []RoutedEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []RoutedEmail
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r RoutedEmail
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}
