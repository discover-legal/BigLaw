// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// seedLineage writes a three-round lineage into repo and returns its versions.
func seedLineage(t *testing.T, repo VersionRepository) []DocumentVersion {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	vs := []DocumentVersion{
		{ID: "v1", LineageID: "lin-1", Round: 1, Source: "ours", Author: "Big Michael",
			CreatedAt: base, Path: "/docs/msa.docx", ContentHash: "hash-1", Text: "twelve (12) months"},
		{ID: "v2", LineageID: "lin-1", ParentID: "v1", Round: 2, Source: "theirs", Author: "Opposing Counsel",
			CreatedAt: base.Add(time.Hour), Path: "/docs/msa.v2.docx", ContentHash: "hash-2", Text: "thirty-six (36) months"},
		{ID: "v3", LineageID: "lin-1", ParentID: "v2", Round: 3, Source: "ours", Author: "Big Michael",
			CreatedAt: base.Add(2 * time.Hour), Path: "/docs/msa.response.docx", ContentHash: "hash-3",
			Text: "twenty-four (24) months", Decisions: []byte(`[{"disposition":"counter"}]`)},
	}
	// Insert out of order to prove ListLineage sorts by round.
	for _, i := range []int{2, 0, 1} {
		if err := repo.PutVersion(ctx, vs[i]); err != nil {
			t.Fatalf("PutVersion %s: %v", vs[i].ID, err)
		}
	}
	return vs
}

// verifyVersionRepo exercises the full VersionRepository contract against any
// backend.
func verifyVersionRepo(t *testing.T, repo VersionRepository) {
	t.Helper()
	ctx := context.Background()
	seedLineage(t, repo)

	got, found, err := repo.GetVersion(ctx, "v2")
	if err != nil || !found {
		t.Fatalf("GetVersion: found=%v err=%v", found, err)
	}
	if got.LineageID != "lin-1" || got.ParentID != "v1" || got.Round != 2 ||
		got.Source != "theirs" || got.Author != "Opposing Counsel" ||
		got.ContentHash != "hash-2" || got.Text != "thirty-six (36) months" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if _, found, err := repo.GetVersion(ctx, "v-none"); err != nil || found {
		t.Errorf("GetVersion(unknown) = found=%v err=%v, want miss without error", found, err)
	}

	// Lineage comes back ordered by round despite out-of-order inserts.
	lineage, err := repo.ListLineage(ctx, "lin-1")
	if err != nil {
		t.Fatalf("ListLineage: %v", err)
	}
	if len(lineage) != 3 || lineage[0].ID != "v1" || lineage[1].ID != "v2" || lineage[2].ID != "v3" {
		t.Fatalf("lineage order wrong: %+v", lineage)
	}
	if string(lineage[2].Decisions) != `[{"disposition":"counter"}]` {
		t.Errorf("decisions payload lost: %q", lineage[2].Decisions)
	}
	if empty, err := repo.ListLineage(ctx, "lin-none"); err != nil || len(empty) != 0 {
		t.Errorf("ListLineage(unknown) = %d versions, err=%v", len(empty), err)
	}

	// Idempotent-registration lookups.
	byHash, found, err := repo.FindVersionByHash(ctx, "hash-2")
	if err != nil || !found || byHash.ID != "v2" {
		t.Errorf("FindVersionByHash = %+v found=%v err=%v, want v2", byHash, found, err)
	}
	byPath, found, err := repo.FindVersionByPath(ctx, "/docs/msa.response.docx")
	if err != nil || !found || byPath.ID != "v3" {
		t.Errorf("FindVersionByPath = %+v found=%v err=%v, want v3", byPath, found, err)
	}
	if _, found, _ := repo.FindVersionByHash(ctx, "hash-none"); found {
		t.Error("FindVersionByHash(unknown) should miss")
	}

	// Replace in place: attaching a decision summary after the fact.
	updated := *byHash
	updated.Decisions = []byte(`[{"disposition":"accept"}]`)
	if err := repo.PutVersion(ctx, updated); err != nil {
		t.Fatalf("PutVersion replace: %v", err)
	}
	got, _, _ = repo.GetVersion(ctx, "v2")
	if string(got.Decisions) != `[{"disposition":"accept"}]` {
		t.Errorf("replaced decisions = %q", got.Decisions)
	}
	if lineage, _ := repo.ListLineage(ctx, "lin-1"); len(lineage) != 3 {
		t.Errorf("replace grew the lineage to %d versions", len(lineage))
	}
}

func TestMemoryVersionRoundTrip(t *testing.T) {
	verifyVersionRepo(t, NewMemoryRepo())
}

func TestSQLiteVersionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "versions.db")
	repo, err := openSQLite(path)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	versions, ok := repo.(VersionRepository)
	if !ok {
		t.Fatal("sqlite repo does not implement VersionRepository")
	}
	verifyVersionRepo(t, versions)

	// Survives restart.
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	repo2, err := openSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer repo2.Close()
	lineage, err := repo2.(VersionRepository).ListLineage(context.Background(), "lin-1")
	if err != nil || len(lineage) != 3 {
		t.Fatalf("after reopen: len=%d err=%v", len(lineage), err)
	}
	if lineage[1].Text != "thirty-six (36) months" || lineage[1].CreatedAt.IsZero() {
		t.Errorf("persisted version lost fields: %+v", lineage[1])
	}
}

// TestMemoryVersionRepoInterface pins the concrete stores to the interface at
// compile time — the tool/API layers recover VersionRepository by asserting
// the review-repo handle, so all backends must implement both.
func TestMemoryVersionRepoInterface(t *testing.T) {
	var repo interface{} = NewMemoryRepo()
	if _, ok := repo.(VersionRepository); !ok {
		t.Fatal("MemoryRepo does not implement VersionRepository")
	}
	if _, ok := repo.(ReviewRepository); !ok {
		t.Fatal("MemoryRepo does not implement ReviewRepository")
	}
}
