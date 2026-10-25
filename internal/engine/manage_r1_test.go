// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"errors"
	"slices"
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

// TestManageSaveNeverDeletes: a save with no files would delete its item; it is refused for the
// engine's workflow and what Go needs with Delete's own reasons, and for every other built-in (a
// built-in skill is deleted with Delete). Before, {"base":0} deleted the lead's prompt and every
// Manage read then panicked.
func TestManageSaveNeverDeletes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	for id, want := range map[string]string{
		"prompt.lead":          "prompt.lead is part of the engine's workflow: edit or revert it; it cannot be deleted",
		"pack.jurisdiction.us": "is part of the engine's workflow",
		"tool.retrieve":        "tool.retrieve cannot be deleted or disabled: it is one of the memo lead's tools",
		"agent.extract_facts":  "cannot be deleted or disabled",
		"skill." + aSkill(t):   "is built in: a save keeps its files",
	} {
		for _, in := range []SaveIn{{}, {Files: map[string]string{}}} {
			if _, _, err := e.Save(ctx, id, in, "Ada"); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
				t.Errorf("save of %s with no files: %v", id, err)
			}
		}
	}
	if vs, err := e.Changes(ctx, 10); err != nil || len(vs) != 0 {
		t.Fatalf("a refused save was stored: %v %v", vs, err)
	}
}

// TestManageSurvivesAMissingPrompt: a prompt whose file a version deleted (rows saved before saves
// kept built-ins' files) is listed as deleted, its detail opens, and a revert restores it: no panic.
func TestManageSurvivesAMissingPrompt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES ('prompt.lead', 1, 'save', '{}'::jsonb, 0, 't')`); err != nil {
		t.Fatal(err)
	}
	items, err := e.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "prompt.lead" })
	if i < 0 || !items[i].Deleted || items[i].Summary != deletedSummary {
		t.Fatalf("the missing prompt's item: %+v", items[i])
	}
	if d, err := e.ItemDetail(ctx, "prompt.lead"); err != nil || len(d.Content) != 0 || d.Original[leadFile] == "" {
		t.Fatalf("detail: %+v %v", d, err)
	}
	if _, err := e.Revert(ctx, "prompt.lead", 0, 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if d, err := e.ItemDetail(ctx, "prompt.lead"); err != nil || d.Deleted || d.Content[leadFile] == "" {
		t.Fatalf("after the revert: %+v %v", d.Item, err)
	}
}

// TestManageSurvivesABrokenVersion: a stored version that no longer loads (edited in the database,
// or saved under an older parser) is listed as broken with why, its detail shows what is stored,
// every other item is still listed, and a revert repairs it.
func TestManageSurvivesABrokenVersion(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	e, _ := f.engine(nil, Config{})
	ctx := context.Background()
	const rf = agentsDir + "reproduce.json"
	if _, err := e.db.ExecContext(ctx, `INSERT INTO spec_versions (item, version, action, files, from_version, author) VALUES ('agent.reproduce', 1, 'save', $1::jsonb, 0, 't')`,
		`{"`+rf+`": "{", "`+agentsDir+`reproduce.md": "x"}`); err != nil {
		t.Fatal(err)
	}
	items, err := e.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(items, func(x Item) bool { return x.ID == "agent.reproduce" })
	if i < 0 || items[i].Status != "broken" || !strings.Contains(items[i].Summary, "does not load") || len(items) != len(Builtins()) {
		t.Fatalf("the broken item (%d items): %+v", len(items), items[i])
	}
	if d, err := e.ItemDetail(ctx, "agent.reproduce"); err != nil || d.Content[rf] != "{" {
		t.Fatalf("detail: %+v %v", d.Content, err)
	}
	if _, err := e.ItemDetail(ctx, "prompt.lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Revert(ctx, "agent.reproduce", 0, 1, "", "Ada"); err != nil {
		t.Fatal(err)
	}
	if d, err := e.ItemDetail(ctx, "agent.reproduce"); err != nil || d.Status == "broken" {
		t.Fatalf("after the revert: %+v %v", d.Item, err)
	}
}
