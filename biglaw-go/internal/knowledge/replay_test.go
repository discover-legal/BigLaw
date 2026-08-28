// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package knowledge

import (
	"context"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// TestSetOnIngestReplaysExistingDocs pins the restart-survival contract: a
// callback registered AFTER documents are already in the store (the Load()-
// before-SetOnIngest startup ordering) still sees every one of them, so the
// RAG chunk index is rebuilt across restarts instead of silently starving
// retrieval.
func TestSetOnIngestReplaysExistingDocs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.Ingest(ctx, types.Document{ID: "d1", Title: "One", Content: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, types.Document{ID: "d2", Title: "Two", Content: "beta"}); err != nil {
		t.Fatal(err)
	}

	seen := map[string]string{}
	s.SetOnIngest(func(id, title, content string) { seen[id] = content })
	if len(seen) != 2 || seen["d1"] != "alpha" || seen["d2"] != "beta" {
		t.Fatalf("replay should cover pre-registration docs, got %v", seen)
	}

	// New ingests still fire the callback exactly as before.
	if _, err := s.Ingest(ctx, types.Document{ID: "d3", Title: "Three", Content: "gamma"}); err != nil {
		t.Fatal(err)
	}
	if seen["d3"] != "gamma" {
		t.Fatalf("post-registration ingest should fire callback, got %v", seen)
	}

	// nil registration is a no-op, not a panic.
	s.SetOnIngest(nil)
}
