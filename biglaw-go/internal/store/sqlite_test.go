// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestSQLiteRoundTripAndPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	ctx := context.Background()

	repo, err := openSQLite(path)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}

	doc := types.Document{
		ID: "doc-1", Title: "MSA", Content: "Governing law: Delaware.",
		Source: "upload", DocumentType: "pdf", OwnerID: "lawyer-7",
		PracticeArea: "Corporate & M&A", DetectedClientNumber: "C-001",
		Metadata:   map[string]interface{}{"extractionMethod": "hybrid-reconciled"},
		IngestedAt: time.Now(),
	}
	if err := repo.Upsert(ctx, doc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Update in place.
	doc.Title = "Master Services Agreement"
	if err := repo.Upsert(ctx, doc); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}

	got, found, err := repo.GetByID(ctx, "doc-1")
	if err != nil || !found {
		t.Fatalf("GetByID: found=%v err=%v", found, err)
	}
	if got.Title != "Master Services Agreement" || got.OwnerID != "lawyer-7" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Metadata["extractionMethod"] != "hybrid-reconciled" {
		t.Errorf("metadata not persisted: %v", got.Metadata)
	}
	if got.Embedding != nil {
		t.Errorf("embedding must not be persisted, got %d floats", len(got.Embedding))
	}
	repo.Close()

	// Reopen the same file — the document must survive (the whole point).
	repo2, err := openSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer repo2.Close()
	list, err := repo2.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("after reopen: len=%d err=%v", len(list), err)
	}
	if list[0].ID != "doc-1" {
		t.Errorf("persisted doc lost: %+v", list[0])
	}

	if err := repo2.Delete(ctx, "doc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := repo2.GetByID(ctx, "doc-1"); found {
		t.Error("doc still present after delete")
	}
}
