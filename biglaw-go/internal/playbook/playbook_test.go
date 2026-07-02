// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package playbook

import (
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestNormalizeClauseType(t *testing.T) {
	cases := map[string]string{
		"governing_law":           "governing_law",
		"Governing Law":           "governing_law",
		"  Governing  Law  ":      "governing_law",
		"Limitation of Liability": "limitation_of_liability",
		"limitation_of_liability": "limitation_of_liability",
		"MAC/MAE definition":      "mac_mae_definition",
		"Indemnification (cap)":   "indemnification_cap",
		"":                        "",
	}
	for in, want := range cases {
		if got := NormalizeClauseType(in); got != want {
			t.Errorf("NormalizeClauseType(%q) = %q, want %q", in, got, want)
		}
	}
}

// The redline engine looks up free-form extracted clause names ("Governing
// Law") against snake_case playbook keys ("governing_law") — Resolve must
// match across the two forms or the playbook never engages.
func TestResolveMatchesFreeFormClauseNames(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "playbooks.json"))
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	store.Upsert(types.Playbook{
		ID:           "pb1",
		Name:         "Test",
		Scope:        types.PlaybookScopeFirm,
		PracticeArea: "Corporate & M&A",
		Entries: []types.PlaybookEntry{
			{ClauseType: "governing_law", StandardPosition: "England and Wales"},
			{ClauseType: "limitation_of_liability", StandardPosition: "Cap at fees paid"},
		},
	})

	for _, name := range []string{"governing_law", "Governing Law", "GOVERNING LAW"} {
		r := store.Resolve(name, ResolveOpts{PracticeArea: "Corporate & M&A"})
		if r == nil {
			t.Fatalf("Resolve(%q) = nil, want firm position", name)
		}
		if r.EffectiveEntry.StandardPosition != "England and Wales" {
			t.Errorf("Resolve(%q) position = %q", name, r.EffectiveEntry.StandardPosition)
		}
	}

	if r := store.Resolve("Limitation of Liability", ResolveOpts{PracticeArea: "Corporate & M&A"}); r == nil {
		t.Fatal(`Resolve("Limitation of Liability") = nil, want match against limitation_of_liability`)
	}
}
