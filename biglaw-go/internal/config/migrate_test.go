// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// stateEnvs are every env var whose default moved into the data dir; tests
// clear them so the developer's shell cannot leak overrides in.
var stateEnvs = []string{
	"TASKS_FILE", "SETTINGS_FILE", "PROFILES_FILE", "CLIENTS_FILE", "TIME_FILE",
	"LEARNING_FILE", "OCG_FILE", "JOBS_FILE", "AUDIT_LOG_FILE",
}

// inScratch chdirs into a fresh dir with a clean state-file environment and
// RUVECTOR_DATA_DIR=./data, returning the dir.
func inScratch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	for _, k := range stateEnvs {
		t.Setenv(k, "")
	}
	t.Setenv("RUVECTOR_DATA_DIR", "./data")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestStateFilesDefaultIntoDataDir(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "/srv/biglaw")
	c := Load()
	for name, got := range map[string]string{
		"tasks": c.Persistence.TasksFile, "settings": c.Persistence.SettingsFile,
		"profiles": c.Persistence.ProfilesFile, "clients": c.Persistence.ClientsFile,
		"time": c.Persistence.TimeFile, "learning": c.Persistence.LearningFile,
		"ocg": c.Persistence.OcgFile, "jobs": c.Persistence.JobsFile, "audit": c.Audit.LogFile,
	} {
		if filepath.Dir(got) != "/srv/biglaw" {
			t.Errorf("%s file = %q, want it under the data dir", name, got)
		}
	}
	if c.Persistence.TasksFile != "/srv/biglaw/.tasks.json" || c.Audit.LogFile != "/srv/biglaw/audit.jsonl" {
		t.Errorf("basenames changed: %q %q", c.Persistence.TasksFile, c.Audit.LogFile)
	}
}

func TestMigrateMovesLegacyDotfiles(t *testing.T) {
	inScratch(t)
	write(t, ".tasks.json", `[{"id":"t1"}]`)
	write(t, ".profiles.json", `[]`)
	write(t, "audit.jsonl", "{}\n")
	write(t, ".matter-budgets.json", `{}`)

	c := Load()
	moves, err := MigrateLegacyStateFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 4 {
		t.Fatalf("moves = %+v, want 4", moves)
	}
	for _, p := range []string{".tasks.json", ".profiles.json", "audit.jsonl", ".matter-budgets.json"} {
		if exists(p) {
			t.Errorf("legacy %s still present", p)
		}
		if !exists(filepath.Join("data", p)) {
			t.Errorf("data/%s missing", p)
		}
	}
	if got := read(t, c.Persistence.TasksFile); got != `[{"id":"t1"}]` {
		t.Errorf("tasks content = %q", got)
	}
	if info, err := os.Stat(c.Persistence.TasksFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v %v", info.Mode(), err)
	}

	// Idempotent: a second start moves nothing and reports no error.
	moves, err = MigrateLegacyStateFiles(Load())
	if err != nil || len(moves) != 0 {
		t.Fatalf("second run: moves=%+v err=%v", moves, err)
	}
}

func TestMigrateNeverClobbersExistingDataDirFile(t *testing.T) {
	inScratch(t)
	write(t, ".tasks.json", "legacy")
	write(t, "data/.tasks.json", "current")

	moves, err := MigrateLegacyStateFiles(Load())
	if err != nil || len(moves) != 0 {
		t.Fatalf("moves=%+v err=%v", moves, err)
	}
	if read(t, "data/.tasks.json") != "current" || read(t, ".tasks.json") != "legacy" {
		t.Fatal("both files must be left exactly as they were")
	}
}

func TestMigrateLeavesEnvOverridesAlone(t *testing.T) {
	inScratch(t)
	t.Setenv("TASKS_FILE", ".tasks.json") // explicit, even though it is the old default
	t.Setenv("CLIENTS_FILE", "custom/clients.json")
	write(t, ".tasks.json", "pinned")
	write(t, ".matter-budgets.json", "{}") // follows CLIENTS_FILE's dir: not migrated either

	c := Load()
	if c.Persistence.TasksFile != ".tasks.json" {
		t.Fatalf("override lost: %q", c.Persistence.TasksFile)
	}
	moves, err := MigrateLegacyStateFiles(c)
	if err != nil || len(moves) != 0 {
		t.Fatalf("moves=%+v err=%v", moves, err)
	}
	if read(t, ".tasks.json") != "pinned" || exists("data/.tasks.json") || exists("data/.matter-budgets.json") {
		t.Fatal("overridden files must not move")
	}
}

func TestMigrateNoOpWhenDataDirIsWorkingDir(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", ".")
	write(t, ".tasks.json", "x")
	moves, err := MigrateLegacyStateFiles(Load())
	if err != nil || len(moves) != 0 || read(t, ".tasks.json") != "x" {
		t.Fatalf("moves=%+v err=%v", moves, err)
	}
}

func TestMigrateCopyFallbackPreservesContent(t *testing.T) {
	// The EXDEV path cannot be forced portably in a unit test; exercise the
	// copy+remove helper directly.
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a"), filepath.Join(dir, "sub", "b")
	write(t, src, "payload")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyThenRemove(src, dst, 0o640); err != nil {
		t.Fatal(err)
	}
	if exists(src) || read(t, dst) != "payload" {
		t.Fatal("copy fallback did not move the file")
	}
	if info, _ := os.Stat(dst); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode())
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "sub", "*.migrating-*")); len(m) != 0 {
		t.Fatalf("temp files left behind: %v", m)
	}
}
