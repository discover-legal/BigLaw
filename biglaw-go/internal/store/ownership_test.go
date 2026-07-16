// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestArtifactOwnershipMemory(t *testing.T) {
	repo := NewMemoryRepo()
	system := WithSystem(context.Background())
	alice := WithIdentity(context.Background(), "alice", false)
	bob := WithIdentity(context.Background(), "bob", false)
	partner := WithIdentity(context.Background(), "partner", true)

	if err := repo.PutReview(system, "review-1", "alice", "M-1", time.Now(), []byte(`{"reviewId":"review-1","ownerId":"alice"}`)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := repo.GetReview(alice, "review-1"); !found {
		t.Fatal("owner cannot read review")
	}
	if _, found, _ := repo.GetReview(bob, "review-1"); found {
		t.Fatal("other lawyer can read review")
	}
	if _, found, _ := repo.GetReview(partner, "review-1"); !found {
		t.Fatal("partner cannot read review")
	}

	v := DocumentVersion{ID: "v1", LineageID: "l1", OwnerID: "alice", MatterNumber: "M-1"}
	if err := repo.PutVersion(system, v); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := repo.GetVersion(alice, v.ID); !found {
		t.Fatal("owner cannot read version")
	}
	if _, found, _ := repo.GetVersion(bob, v.ID); found {
		t.Fatal("other lawyer can read version")
	}
	if versions, _ := repo.ListLineage(bob, v.LineageID); len(versions) != 0 {
		t.Fatal("other lawyer can list lineage")
	}
}

func TestSQLiteOwnershipMigrationFromLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE reviews (id TEXT PRIMARY KEY, created_at TEXT NOT NULL DEFAULT '', payload TEXT NOT NULL DEFAULT '');
		CREATE TABLE document_versions (
			id TEXT PRIMARY KEY, lineage_id TEXT NOT NULL DEFAULT '', parent_id TEXT NOT NULL DEFAULT '',
			round INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '', content_hash TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '', decisions_json TEXT NOT NULL DEFAULT '');
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := openSQLite(path)
	if err != nil {
		t.Fatalf("ownership migration failed: %v", err)
	}
	defer opened.Close()
	reviews := opened.(ReviewRepository)
	if err := reviews.PutReview(WithSystem(context.Background()), "r1", "alice", "M-1", time.Now(), []byte(`{"reviewId":"r1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := reviews.GetReview(WithIdentity(context.Background(), "alice", false), "r1"); err != nil || !found {
		t.Fatalf("migrated owned review unavailable: found=%v err=%v", found, err)
	}
}
