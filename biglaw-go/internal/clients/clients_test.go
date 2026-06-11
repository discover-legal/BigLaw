// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package clients

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestNormName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Acme Corp.", "acme"},
		{"ACME Corporation", "acme"},
		{"Acme, Inc.", "acme"},
		{"Globex GmbH", "globex"},
		{"Initech LLC", "initech"},
		{"Wayne Enterprises Ltd", "wayne enterprises"},
		{"Stark & Co", "stark"},
		{"  Umbrella   Holdings  ", "umbrella"},
		{"Cyberdyne Systems", "cyberdyne systems"},
		{"", ""},
		{"Inc", ""},
		// "co" stripped only as a whole word — not inside other words.
		{"Costco Company", "costco"},
	}
	for _, tt := range tests {
		if got := normName(tt.in); got != tt.want {
			t.Errorf("normName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCheckConflict(t *testing.T) {
	store := &ClientStore{
		clients: []types.Client{
			{ID: "c1", Name: "Globex Corporation", Adversaries: []string{"Acme Corp."}},
			{ID: "c2", Name: "Initech LLC", Adversaries: []string{}},
		},
	}

	t.Run("forward: new name matches existing adversary list after normalization", func(t *testing.T) {
		res := store.CheckConflict("ACME Corporation", nil)
		if !res.HasConflict || res.ConflictingClientID != "c1" || res.MatchedAdversary != "Acme Corp." {
			t.Fatalf("expected conflict with c1 via 'Acme Corp.', got %+v", res)
		}
	})

	t.Run("reverse: new client's adversary is an existing client", func(t *testing.T) {
		res := store.CheckConflict("Newco Ventures", []string{"Initech, Inc."})
		if !res.HasConflict || res.ConflictingClientID != "c2" || res.MatchedAdversary != "Initech, Inc." {
			t.Fatalf("expected reverse conflict with c2 via 'Initech, Inc.', got %+v", res)
		}
	})

	t.Run("no conflict", func(t *testing.T) {
		res := store.CheckConflict("Cyberdyne Systems", []string{"Tyrell Corp"})
		if res.HasConflict {
			t.Fatalf("expected no conflict, got %+v", res)
		}
	})

	t.Run("empty name never conflicts", func(t *testing.T) {
		res := store.CheckConflict("   ", []string{"Globex"})
		if res.HasConflict {
			t.Fatalf("expected no conflict for empty name, got %+v", res)
		}
	})

	t.Run("short normalized adversaries are skipped", func(t *testing.T) {
		store2 := &ClientStore{clients: []types.Client{
			{ID: "c3", Name: "Oscorp", Adversaries: []string{"Co"}}, // norms to "" → skipped
		}}
		res := store2.CheckConflict("Some Client", nil)
		if res.HasConflict {
			t.Fatalf("expected short adversary to be skipped, got %+v", res)
		}
	})
}
