// SPDX-License-Identifier: Apache-2.0
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

	t.Run("empty name still checks the adverse parties", func(t *testing.T) {
		if res := store.CheckConflict("   ", nil); res.HasConflict {
			t.Fatalf("expected no conflict for empty name and no parties, got %+v", res)
		}
		res := store.CheckConflict("   ", []string{"Globex"})
		if !res.HasConflict || res.ConflictingClientID != "c1" {
			t.Fatalf("an empty client name must not skip the adverse-party direction, got %+v", res)
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

func TestNamesMatchVariants(t *testing.T) {
	match := [][2]string{
		{"José García", "Jose Garcia"},
		{"Mary O'Brien-Smith", "Mary OBrien-Smith"},
		{"Mary O'Brien-Smith", "Mary O Brien Smith"},
		{"Mary O’Brien", "Mary O'Brien"},
		{"Smith, Robert", "Robert Smith"},
		{"Robert J. Smith", "Robert Smith"},
		{"Rob​ert Smith", "Robert Smith"},
		{"ROBERT SMITH", "robert smith"},
		{"Acme Inc.", "Acme Corporation"},
	}
	for _, m := range match {
		if !NamesMatch(m[0], m[1]) || !NamesMatch(m[1], m[0]) {
			t.Errorf("NamesMatch(%q, %q) = false, want true", m[0], m[1])
		}
	}
	noMatch := [][2]string{
		{"Robert Smith", "Alice Jones"},
		{"", "Robert Smith"},
		{"Inc", "Acme"},
	}
	for _, m := range noMatch {
		if NamesMatch(m[0], m[1]) {
			t.Errorf("NamesMatch(%q, %q) = true, want false", m[0], m[1])
		}
	}
}

func TestConflictHitsCatchesNameVariants(t *testing.T) {
	store := &ClientStore{clients: []types.Client{
		{ID: "c1", Name: "José García", Adversaries: []string{"Smith, Robert"}},
	}}
	// Forward: the new client is on c1's adversary list under another spelling.
	if hits := store.ConflictHits("Robert J. Smith", nil, ""); len(hits) != 1 {
		t.Fatalf("forward variant missed: %+v", hits)
	}
	// Reverse: the new client's adverse party is c1 under another spelling.
	if hits := store.ConflictHits("Pat Doe", []string{"Jose Garcia"}, ""); len(hits) != 1 {
		t.Fatalf("reverse variant missed: %+v", hits)
	}
}
