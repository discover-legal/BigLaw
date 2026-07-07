// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/email"
)

// fakeSource returns a fixed batch of messages.
type fakeSource struct {
	msgs  []email.Message
	calls int
}

func (f *fakeSource) Recent(string, int, int) ([]email.Message, error) {
	f.calls++
	return f.msgs, nil
}

func TestRoutedStoreDedupAndCount(t *testing.T) {
	store := NewRoutedStore(filepath.Join(t.TempDir(), "routed.jsonl"))
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	must := func(r RoutedEmail) {
		if err := store.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	must(RoutedEmail{MessageID: "a", MatterNumber: "M-001", Method: RouteRegex, RoutedAt: now.Format(time.RFC3339)})
	must(RoutedEmail{MessageID: "b", MatterNumber: "M-001", Method: RouteModel, RoutedAt: now.Format(time.RFC3339)})
	must(RoutedEmail{MessageID: "c", MatterNumber: "M-001", Method: RouteUnrouted, RoutedAt: now.Format(time.RFC3339)})

	if !store.Seen("a") || store.Seen("zzz") {
		t.Error("Seen tracking wrong")
	}
	// Unrouted is excluded from the matter count.
	if n := store.CountForMatter("M-001", now.Add(-1*time.Hour)); n != 2 {
		t.Errorf("CountForMatter: want 2, got %d", n)
	}
	// Records before the cutoff are excluded.
	if n := store.CountForMatter("M-001", now.Add(1*time.Hour)); n != 0 {
		t.Errorf("CountForMatter after-cutoff: want 0, got %d", n)
	}
}

func TestRoutedStoreReloadsSeenSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routed.jsonl")
	s1 := NewRoutedStore(path)
	_ = s1.Init()
	_ = s1.Append(RoutedEmail{MessageID: "x", MatterNumber: "M-001", Method: RouteRegex})

	s2 := NewRoutedStore(path)
	if err := s2.Init(); err != nil {
		t.Fatal(err)
	}
	if !s2.Seen("x") {
		t.Error("seen-set should reload from disk after restart")
	}
}

func TestIntakePollOnceRoutesAndDedups(t *testing.T) {
	store := NewRoutedStore(filepath.Join(t.TempDir(), "routed.jsonl"))
	_ = store.Init()
	router := NewRouter(&fakeProvider{}, "m", 0.6)
	src := &fakeSource{msgs: []email.Message{
		{ID: "1", MatterRef: "M-001", Subject: "[M-001] hi", Provider: "graph"},
		{ID: "2", Subject: "no matter ref", Provider: "graph"}, // unrouted (no model match)
	}}
	intake := NewIntake(IntakeConfig{IntakeMode: "polling"}, src, router, store, func() []MatterOption { return roster })

	routed, err := intake.PollOnce()
	if err != nil {
		t.Fatal(err)
	}
	if routed != 1 {
		t.Errorf("want 1 confidently routed, got %d", routed)
	}

	// Second poll: both already seen → nothing new routed.
	routed2, _ := intake.PollOnce()
	if routed2 != 0 {
		t.Errorf("dedup failed: re-poll routed %d", routed2)
	}
	if store.CountForMatter("M-001", time.Now().Add(-time.Hour)) != 1 {
		t.Error("M-001 should have exactly one routed email")
	}
}

func TestBuildIntakeQuery(t *testing.T) {
	if q := buildIntakeQuery(IntakeConfig{IntakeMode: "shared_inbox", SharedInbox: "proj@firm.com"}); q != "to:proj@firm.com" {
		t.Errorf("shared_inbox query: %q", q)
	}
	if q := buildIntakeQuery(IntakeConfig{IntakeMode: "polling"}); q != "" {
		t.Errorf("polling query should be broad/empty, got %q", q)
	}
	if q := buildIntakeQuery(IntakeConfig{IntakeMode: "both", SharedInbox: "x@y.com", Query: "custom"}); q != "custom" {
		t.Errorf("explicit query override should win, got %q", q)
	}
}
