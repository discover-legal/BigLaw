// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// The database and its WAL/shared-memory files (which hold document text)
// are 0600 — on a fresh install and over 0644 files from an older build.
func TestSQLiteFilesAre0600(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh.db")
	old := filepath.Join(dir, "old.db")
	for _, f := range []string{old + "-wal", old + "-shm"} {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{fresh, old} {
		repo, err := openSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		ctx := storeSystemContext()
		_ = repo.Upsert(ctx, types.Document{ID: "d1", Title: "t", Content: "secret text"})
		_, _, _ = repo.GetByID(context.Background(), "d1")
		for _, f := range []string{path, path + "-wal", path + "-shm"} {
			fi, err := os.Stat(f)
			if err != nil {
				continue // WAL files may be checkpointed away
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s perm = %v", filepath.Base(f), fi.Mode().Perm())
			}
		}
		_ = repo.Close()
	}
}
