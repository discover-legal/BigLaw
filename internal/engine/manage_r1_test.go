// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"strings"
	"testing"
)

// TestManageDiffNilSide: either side of a diff (and Patch's built) may be nil: a file only on the
// other side is a new or a deleted file, never a panic (PR #59 r1 M1).
func TestManageDiffNilSide(t *testing.T) {
	t.Parallel()
	const p = "internal/engine/specs/x.md"
	if got := Patch(map[string]string{p: "a\n"}, nil); !strings.Contains(got, "new file mode 100644") || !strings.Contains(got, "+a") {
		t.Fatalf("Patch(now, nil): %q", got)
	}
	if ds := diffFiles(nil, map[string]string{p: "a\n"}); len(ds) != 1 || !strings.Contains(ds[0].Patch, "--- /dev/null") {
		t.Fatalf("diffFiles(nil, b): %+v", ds)
	}
	if ds := diffFiles(map[string]string{p: "a\n"}, nil); len(ds) != 1 || !strings.Contains(ds[0].Patch, "+++ /dev/null") {
		t.Fatalf("diffFiles(a, nil): %+v", ds)
	}
	if ds := diffFiles(nil, nil); ds == nil || len(ds) != 0 {
		t.Fatalf("diffFiles(nil, nil): %#v", ds)
	}
}

// TestManageJSONNullFiles: a version row whose files are a jsonb null (not SQL NULL, which no BigLaw
// write makes) reads as no files, as it did before the version-file readers were shared (PR #59 r1
// L2); SQL NULL is the built-in.
func TestManageJSONNullFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES
		('prompt.lead', 1, 'save', 'null'::jsonb, 0, 't'), ('prompt.lead', 2, 'revert', NULL, 1, 't')`); err != nil {
		t.Fatal(err)
	}
	if files, err := e.filesAt(ctx, "prompt.lead", 1); err != nil || files != nil {
		t.Fatalf("jsonb null: %v %v", files, err)
	}
	if files, err := e.filesAt(ctx, "prompt.lead", 2); err != nil || files[leadFile] == "" {
		t.Fatalf("SQL NULL is the built-in: %v %v", files, err)
	}
	if _, err := e.filesAt(ctx, "prompt.lead", 3); err == nil {
		t.Fatal("no version 3, no error")
	}
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES ('prompt.meta', 1, 'save', 'null'::jsonb, 0, 't')`); err != nil {
		t.Fatal(err)
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if files, err := lastContent(ctx, tx, "prompt.meta"); err != nil || files != nil {
		t.Fatalf("lastContent of a jsonb null: %v %v", files, err)
	}
	if files, err := lastContent(ctx, tx, "prompt.lead"); err != nil || files[leadFile] == "" {
		t.Fatalf("lastContent of SQL NULL is the built-in: %v %v", files, err)
	}
}

const leadFile = engineDir + "specs/lead.md"
