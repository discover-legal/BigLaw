// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package knowledge

import (
	"context"
	"fmt"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Without an embedder, search used to return the first TopK documents in
// insertion order with a constant 0.5 score, whatever the query.
func TestSearchWithoutEmbeddingsRanksLexically(t *testing.T) {
	s := newLexicalStore(t)
	ctx := store.WithSystem(context.Background())
	for _, d := range []types.Document{
		{ID: "lease", Title: "Commercial lease", Content: "The tenant shall pay rent monthly. Rent review every five years.", OwnerID: "alice"},
		{ID: "nda", Title: "Mutual NDA", Content: "Confidential information must not be disclosed to third parties.", OwnerID: "alice"},
		{ID: "emp", Title: "Employment contract", Content: "The employee is entitled to holiday pay and a notice period.", OwnerID: "bob"},
		{ID: "rent2", Title: "Rent arrears letter", Content: "Rent rent rent is overdue; the landlord demands rent.", OwnerID: "bob"},
	} {
		if _, err := s.Ingest(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.Search("rent review", SearchOpts{TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want only the 2 documents mentioning the query terms, got %d: %+v", len(res), res)
	}
	if res[0].Document.ID != "lease" {
		t.Errorf("lease mentions both terms and should rank first, got %s", res[0].Document.ID)
	}
	for _, r := range res {
		if r.Score <= 0 || r.Score >= 1 {
			t.Errorf("%s: score %v outside (0,1)", r.Document.ID, r.Score)
		}
	}
	if res[0].Score <= res[1].Score {
		t.Errorf("scores must reflect rank: %v <= %v", res[0].Score, res[1].Score)
	}

	// Owner filter applies after ranking, so bob still gets his match.
	res, _ = s.Search("rent", SearchOpts{OwnerID: "bob", TopK: 1})
	if len(res) != 1 || res[0].Document.ID != "rent2" {
		t.Fatalf("owner-filtered search = %+v", res)
	}

	res, _ = s.Search("indemnity", SearchOpts{TopK: 5})
	if res == nil || len(res) != 0 {
		t.Fatalf("no lexical match must return an empty list, got %+v", res)
	}

	// Re-ingesting a document replaces its index entry.
	if _, err := s.Ingest(ctx, types.Document{ID: "nda", Title: "Mutual NDA", Content: "Rent review clause.", OwnerID: "alice"}); err != nil {
		t.Fatal(err)
	}
	res, _ = s.Search("confidential", SearchOpts{TopK: 5})
	if len(res) != 0 {
		t.Fatalf("stale index entry still matched: %+v", res)
	}
}

// Owner-scoped scores depend only on the owner's own documents: adding other
// owners' documents must not change them (no cross-owner statistics leak).
func TestOwnerScopedScoresIgnoreOtherOwnersDocs(t *testing.T) {
	s := newLexicalStore(t)
	ctx := store.WithSystem(context.Background())
	ingest := func(id, owner, content string) {
		if _, err := s.Ingest(ctx, types.Document{ID: id, Title: id, Content: content, OwnerID: owner}); err != nil {
			t.Fatal(err)
		}
	}
	ingest("b1", "bob", "rent arrears notice")
	ingest("b2", "bob", "holiday pay")
	before, _ := s.Search("rent", SearchOpts{OwnerID: "bob"})
	for i := range 20 {
		ingest(fmt.Sprintf("a%d", i), "alice", "rent rent review")
	}
	after, _ := s.Search("rent", SearchOpts{OwnerID: "bob"})
	if len(before) != 1 || len(after) != 1 || before[0].Score != after[0].Score {
		t.Fatalf("bob's score moved with alice's corpus: %+v → %+v", before, after)
	}
	// Moving a document to another owner moves it between owner indexes.
	ingest("b1", "carol", "rent arrears notice")
	if res, _ := s.Search("arrears", SearchOpts{OwnerID: "bob"}); len(res) != 0 {
		t.Fatalf("moved document still in old owner's results: %+v", res)
	}
	if res, _ := s.Search("arrears", SearchOpts{OwnerID: "carol"}); len(res) != 1 {
		t.Fatalf("moved document missing for new owner: %+v", res)
	}
}

// newLexicalStore returns a store whose embedder fails fast (connection
// refused on a closed local port), so Search takes the lexical fallback.
func newLexicalStore(t *testing.T) *Store {
	t.Helper()
	cfg := &config.Config{}
	cfg.Local.LocalEmbeddings = true
	cfg.Local.OllamaURL = "http://127.0.0.1:1"
	s := NewStore(embeddings.NewClient(cfg))
	if r, err := s.embedC.Embed("probe"); err == nil && r != nil {
		t.Skip("embedder unexpectedly reachable")
	}
	return s
}
