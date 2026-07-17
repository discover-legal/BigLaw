// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// MemoryRepo's DocRepository methods (Upsert/GetByID/List/Delete/*Attachment)
// have no direct contract test in this package — unlike sqlite, which has
// TestSQLiteRoundTripAndPersistence. MemoryRepo IS exercised as a test double
// in internal/api, internal/redtime, and internal/tools, but only through
// those packages' own call patterns, never against the full DocRepository
// contract in isolation the way sqlite_test.go does. This test mirrors that
// sqlite test's shape directly against MemoryRepo.

func TestMemoryRepo_DocCRUD(t *testing.T) {
	ctx := WithSystem(context.Background())
	repo := NewMemoryRepo()

	doc := types.Document{
		ID: "doc-1", Title: "MSA", Content: "Governing law: Delaware.",
		Source: "upload", OwnerID: "lawyer-7", IngestedAt: time.Now(),
		Embedding: []float32{1, 2, 3}, // must NOT survive Upsert (memory.go: "never persist the vector")
	}
	if err := repo.Upsert(ctx, doc); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, found, err := repo.GetByID(ctx, "doc-1")
	if err != nil || !found {
		t.Fatalf("GetByID: found=%v err=%v", found, err)
	}
	if got.Embedding != nil {
		t.Errorf("MemoryRepo.Upsert must strip Embedding, got %v", got.Embedding)
	}

	// Update in place must not duplicate the insertion-order slice.
	doc.Title = "Master Services Agreement"
	if err := repo.Upsert(ctx, doc); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List after update = %d docs, want 1: err=%v", len(list), err)
	}
	if list[0].Title != "Master Services Agreement" {
		t.Errorf("List[0].Title = %q, want the updated title", list[0].Title)
	}

	// TODO: Upsert a second document and assert List preserves insertion order
	// (memory.go's `order []string` — GetByID/List round-trip semantics).

	if err := repo.Delete(ctx, "doc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := repo.GetByID(ctx, "doc-1"); found {
		t.Error("doc still present after delete")
	}
	// TODO: assert Delete also removed doc-1 from the `order` slice specifically
	// (Delete's linear scan-and-splice, memory.go lines ~79-92) — a bug there
	// would leave a dangling id that List then tries to look up and silently
	// skips, rather than a clean removal. Cover a delete from the MIDDLE of a
	// 3+ element order slice, not just the only element.

	// Delete of an unknown id must be a no-op, not an error.
	if err := repo.Delete(ctx, "doc-none"); err != nil {
		t.Errorf("Delete(unknown) = %v, want nil (no-op)", err)
	}
}

func TestMemoryRepo_DeleteFromMiddleOfOrderSlice(t *testing.T) {
	ctx := WithSystem(context.Background())
	repo := NewMemoryRepo()
	for _, id := range []string{"a", "b", "c"} {
		if err := repo.Upsert(ctx, types.Document{ID: id, IngestedAt: time.Now()}); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}
	if err := repo.Delete(ctx, "b"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ := repo.List(ctx)
	// TODO: assert list == [a, c] in that order (order slice splice must not
	// reorder or drop the wrong neighbor).
	if len(list) != 2 {
		t.Fatalf("List after deleting middle element = %d docs, want 2", len(list))
	}
}

func TestMemoryRepo_AttachmentCRUD(t *testing.T) {
	ctx := WithSystem(context.Background())
	repo := NewMemoryRepo()
	if err := repo.Upsert(ctx, types.Document{ID: "doc-1", OwnerID: "lawyer-7"}); err != nil {
		t.Fatalf("Upsert parent: %v", err)
	}
	att := types.Attachment{ID: "att-1", DocID: "doc-1", OwnerID: "lawyer-7",
		Filename: "exhibit.pdf", MediaType: "application/pdf", CreatedAt: time.Now()}
	if err := repo.AddAttachment(ctx, att); err != nil {
		t.Fatalf("AddAttachment: %v", err)
	}
	// TODO: ListAttachments filters by DocID via a linear scan of the WHOLE
	// attachments map (memory.go ListAttachments) — add a second attachment
	// under a DIFFERENT doc-id and assert it is excluded.
	list, err := repo.ListAttachments(ctx, "doc-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListAttachments = %d, want 1: err=%v", len(list), err)
	}
	if _, found, _ := repo.GetAttachment(ctx, "att-1"); !found {
		t.Error("GetAttachment should find att-1")
	}
	if err := repo.DeleteAttachment(ctx, "att-1"); err != nil {
		t.Fatalf("DeleteAttachment: %v", err)
	}
	if _, found, _ := repo.GetAttachment(ctx, "att-1"); found {
		t.Error("attachment still present after delete")
	}
}

func TestMemoryRepo_BackendAndClose(t *testing.T) {
	repo := NewMemoryRepo()
	if repo.Backend() != "memory" {
		t.Errorf("Backend() = %q, want %q", repo.Backend(), "memory")
	}
	if err := repo.Close(); err != nil {
		t.Errorf("Close() = %v, want nil (memory backend has nothing to release)", err)
	}
}
