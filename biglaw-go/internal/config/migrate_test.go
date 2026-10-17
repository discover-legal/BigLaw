// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// stateEnvs are every env var whose default moved into the data dir; tests
// clear them so the developer's shell cannot leak overrides in.
var stateEnvs = []string{
	"TASKS_FILE", "SETTINGS_FILE", "PROFILES_FILE", "CLIENTS_FILE", "TIME_FILE",
	"LEARNING_FILE", "OCG_FILE", "JOBS_FILE", "AUDIT_LOG_FILE", "SQLITE_PATH",
	"BLOB_DIR", "COST_LOG_FILE", "PREBILLS_FILE", "PLAYBOOKS_FILE", "CLIENT_VOICE_FILE",
	"CLIO_TOKENS_FILE", "LPM_CORPUS_FILE", "LPM_REPORT_DIR", "LPM_ROUTED_FILE",
	"LPM_PENDING_FILE", "LPM_BACKFILL_CURSOR_FILE", "DOCKETS_FILE",
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
	write(t, ".time-entries.json.migrations.json", `{}`) // one-off migration marker beside the time file

	c := Load()
	moves, err := MigrateLegacyStateFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 5 {
		t.Fatalf("moves = %+v, want 5", moves)
	}
	for _, p := range []string{".tasks.json", ".profiles.json", "audit.jsonl", ".matter-budgets.json", ".time-entries.json.migrations.json"} {
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
	t.Setenv("TASKS_FILE", "pinned/tasks.json")
	t.Setenv("CLIENTS_FILE", "custom/clients.json")
	write(t, "pinned/tasks.json", "pinned")
	write(t, ".tasks.json", "legacy")      // not the configured file: untouched
	write(t, ".matter-budgets.json", "{}") // follows CLIENTS_FILE's dir: not migrated either

	c := Load()
	if c.Persistence.TasksFile != "pinned/tasks.json" {
		t.Fatalf("override lost: %q", c.Persistence.TasksFile)
	}
	moves, err := MigrateLegacyStateFiles(c)
	if err != nil || len(moves) != 0 {
		t.Fatalf("moves=%+v err=%v", moves, err)
	}
	if read(t, "pinned/tasks.json") != "pinned" || exists("data/.tasks.json") || exists("data/.matter-budgets.json") {
		t.Fatal("overridden files must not move")
	}
}

// An env value equal to an old default (a stale .env.example line) is not an
// override: the file is migrated and the data-dir path is used.
func TestEnvEqualToLegacyDefaultCountsAsUnset(t *testing.T) {
	inScratch(t)
	t.Setenv("TASKS_FILE", ".tasks.json")
	t.Setenv("AUDIT_LOG_FILE", "./audit.jsonl")
	t.Setenv("SQLITE_PATH", "./data/biglaw.db")
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	write(t, ".tasks.json", "t")
	write(t, "audit.jsonl", "a")
	write(t, "data/biglaw.db", "db")

	c := Load()
	if c.Persistence.TasksFile != "state/.tasks.json" || c.Audit.LogFile != "state/audit.jsonl" ||
		c.Database.SQLitePath != "state/biglaw.db" {
		t.Fatalf("paths = %q %q %q", c.Persistence.TasksFile, c.Audit.LogFile, c.Database.SQLitePath)
	}
	if _, err := MigrateLegacyStateFiles(c); err != nil {
		t.Fatal(err)
	}
	if read(t, "state/.tasks.json") != "t" || read(t, "state/audit.jsonl") != "a" || read(t, "state/biglaw.db") != "db" {
		t.Fatal("legacy-valued env files were not migrated")
	}
}

// With RUVECTOR_DATA_DIR elsewhere, everything that used to sit in ./data
// (the database and its journal, attachments, ledgers) follows it.
func TestMigrateDotDataIntoCustomDataDir(t *testing.T) {
	inScratch(t)
	dir := t.TempDir()
	t.Setenv("RUVECTOR_DATA_DIR", filepath.Join(dir, "state"))
	write(t, "data/biglaw.db", "db")
	write(t, "data/biglaw.db-wal", "wal")
	write(t, "data/costs.jsonl", "c")
	write(t, "data/attachments/a/b.pdf", "pdf")
	write(t, "data/.profiles.json", "profiles") // a dotfile already in the old default data dir

	c := Load()
	if _, err := MigrateLegacyStateFiles(c); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"biglaw.db": "db", "biglaw.db-wal": "wal", "costs.jsonl": "c", "attachments/a/b.pdf": "pdf",
		".profiles.json": "profiles",
	} {
		if got := read(t, filepath.Join(dir, "state", rel)); got != want {
			t.Errorf("%s = %q", rel, got)
		}
	}
	if exists("data/biglaw.db") || exists("data/biglaw.db-wal") || exists("data/attachments") {
		t.Error("legacy copies left behind")
	}
	if err := c.CheckStateLayout(); err != nil {
		t.Fatal(err)
	}
}

// A ./data with a biglaw.lock is another install's data dir (possibly a live
// owner), not legacy state: a second owner started with its own data dir
// must never migrate it out from under that owner.
func TestMigrateSkipsLockedDotData(t *testing.T) {
	inScratch(t)
	dir := t.TempDir()
	t.Setenv("RUVECTOR_DATA_DIR", filepath.Join(dir, "state"))
	write(t, "data/"+LockFileName, "4242\n")
	write(t, "data/biglaw.db", "db")
	write(t, "data/biglaw.db-wal", "wal")
	write(t, "data/biglaw.db-shm", "shm")
	write(t, "data/.clients.json", "clients")
	write(t, "data/attachments/a/b.pdf", "pdf")
	write(t, ".tasks.json", "tasks") // CWD dotfiles are still legacy state

	c := Load()
	moves, err := MigrateLegacyStateFiles(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, mv := range moves {
		if strings.HasPrefix(filepath.Clean(mv.From), "data"+string(filepath.Separator)) {
			t.Errorf("migrated from the locked ./data: %+v", mv)
		}
	}
	for _, rel := range []string{"biglaw.db", "biglaw.db-wal", "biglaw.db-shm", ".clients.json", "attachments/a/b.pdf", LockFileName} {
		if !exists(filepath.Join("data", rel)) {
			t.Errorf("data/%s moved out of the other install's data dir", rel)
		}
		if rel != LockFileName && exists(filepath.Join(dir, "state", rel)) {
			t.Errorf("state/%s appeared", rel)
		}
	}
	if got := read(t, filepath.Join(dir, "state", ".tasks.json")); got != "tasks" {
		t.Errorf(".tasks.json = %q, want migrated from the working dir", got)
	}
}

// A database that already exists in the data dir is never paired with a
// legacy journal.
func TestMigrateCompanionNeverOrphaned(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	write(t, "data/biglaw.db", "old")
	write(t, "data/biglaw.db-wal", "oldwal")
	write(t, "state/biglaw.db-wal", "stray")
	if _, err := MigrateLegacyStateFiles(Load()); err == nil {
		t.Fatal("want an error for a stray journal at the destination")
	}
	if read(t, "data/biglaw.db") != "old" || exists("state/biglaw.db") {
		t.Fatal("nothing may move when the group cannot move together")
	}
}

// failRename makes the migration's rename fail for paths ending in suffix
// (the crash/fault seam between two moves of one unit).
func failRename(t *testing.T, suffix string) {
	t.Helper()
	rename = func(from, to string) error {
		if strings.HasSuffix(from, suffix) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EIO}
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename = os.Rename })
}

// The verifier's repro: the unit is interrupted after part of it moved.
// The refusal names exactly what moved and what did not, a journal stays
// behind, and the next start resumes it — the database never comes up at
// its destination without its WAL.
func TestMigrateInterruptedUnitResumes(t *testing.T) {
	for _, failOn := range []string{"biglaw.db", "-shm"} {
		t.Run(failOn, func(t *testing.T) {
			inScratch(t)
			t.Setenv("RUVECTOR_DATA_DIR", "state")
			write(t, "data/biglaw.db", "db")
			write(t, "data/biglaw.db-wal", "wal")
			write(t, "data/biglaw.db-shm", "shm")
			c := Load()
			journal := c.Database.SQLitePath + JournalSuffix

			failRename(t, failOn)
			_, err := MigrateLegacyStateFiles(c)
			var me *MigrationError
			if !errors.As(err, &me) || me.Journal == "" {
				t.Fatalf("want a MigrationError with a journal, got %v", err)
			}
			msg := err.Error()
			legacy, _ := filepath.Abs("data/biglaw.db")
			if !strings.Contains(msg, "moved:     "+legacy+"-wal -> ") ||
				!strings.Contains(msg, "not moved: "+legacy+" (") {
				t.Fatalf("refusal must list what moved and what did not:\n%s", msg)
			}
			if exists("state/biglaw.db") || !exists("data/biglaw.db") || !exists(journal) {
				t.Fatal("the main file moves last, and the journal stays until the unit completes")
			}

			rename = os.Rename // the fault clears; next start
			if _, err := MigrateLegacyStateFiles(Load()); err != nil {
				t.Fatalf("resume: %v", err)
			}
			for rel, want := range map[string]string{"biglaw.db": "db", "biglaw.db-wal": "wal", "biglaw.db-shm": "shm"} {
				if got := read(t, filepath.Join("state", rel)); got != want {
					t.Errorf("%s = %q", rel, got)
				}
			}
			if exists(journal) || exists("data/biglaw.db") || exists("data/biglaw.db-wal") {
				t.Fatal("resume must finish the unit and drop the journal")
			}
		})
	}
}

// Without a journal (a crash before this release, or files moved by hand),
// a legacy WAL whose database is gone refuses the start instead of opening
// the data-dir database without it.
func TestMigrateRefusesOrphanedLegacyCompanion(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	write(t, "state/biglaw.db", "db-moved")
	write(t, "data/biglaw.db-wal", "wal-left-behind")
	_, err := MigrateLegacyStateFiles(Load())
	if err == nil || !strings.Contains(err.Error(), "data/biglaw.db-wal exists without data/biglaw.db") {
		t.Fatalf("want an orphaned-WAL refusal, got %v", err)
	}
	if read(t, "data/biglaw.db-wal") != "wal-left-behind" {
		t.Fatal("the orphan must be left untouched")
	}
}

// A stray companion at the destination is refused even when the legacy
// database has none to replace it with.
func TestMigrateRefusesStrayDestinationCompanion(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	write(t, "data/biglaw.db", "db")
	write(t, "state/biglaw.db-wal", "someone-elses")
	_, err := MigrateLegacyStateFiles(Load())
	legacy, _ := filepath.Abs("data/biglaw.db")
	if err == nil || !strings.Contains(err.Error(), "not moved: "+legacy+" (") {
		t.Fatalf("want a stray-companion refusal listing the unmoved db, got %v", err)
	}
	if exists("state/biglaw.db") || !exists("data/biglaw.db") {
		t.Fatal("nothing may move")
	}
}

func TestMigrateResumeHandlesBothEnds(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	c := Load()
	dst := c.Database.SQLitePath
	src := filepath.Join("data", "biglaw.db")
	writeJ := func() {
		if err := writeJournal(dst+JournalSuffix, unitJournal{Env: "SQLITE_PATH", Moves: []unitMove{
			{From: src + "-journal", To: dst + "-journal"}, {From: src, To: dst},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// A crash inside a cross-device copy leaves identical copies at both ends.
	write(t, src+"-journal", "j")
	write(t, dst+"-journal", "j")
	write(t, src, "db")
	writeJ()
	if _, err := MigrateLegacyStateFiles(c); err != nil {
		t.Fatal(err)
	}
	if exists(src) || exists(src+"-journal") || read(t, dst) != "db" || exists(dst+JournalSuffix) {
		t.Fatal("identical copies resolve to the destination")
	}
	// Differing copies are never guessed at.
	write(t, src+"-journal", "old")
	if err := os.Remove(dst); err != nil {
		t.Fatal(err)
	}
	write(t, src, "db")
	writeJ()
	if _, err := MigrateLegacyStateFiles(c); err == nil || !strings.Contains(err.Error(), "both exist and differ") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if !exists(dst + JournalSuffix) {
		t.Fatal("the journal stays while the unit is unresolved")
	}
}

// A journal torn by a crash while it was written (before any move) is
// dropped and the unit planned afresh.
func TestMigrateDiscardsTornJournal(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	write(t, "data/biglaw.db", "db")
	write(t, "data/biglaw.db-wal", "wal")
	write(t, "state/biglaw.db"+JournalSuffix, `{"env":"SQLITE_PATH","mo`)
	if _, err := MigrateLegacyStateFiles(Load()); err != nil {
		t.Fatal(err)
	}
	if read(t, "state/biglaw.db") != "db" || read(t, "state/biglaw.db-wal") != "wal" || exists("state/biglaw.db"+JournalSuffix) {
		t.Fatal("torn journal not replaced by a fresh, completed unit")
	}
}

func TestMigrateMovesRollbackJournal(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "state")
	write(t, "data/biglaw.db", "db")
	write(t, "data/biglaw.db-journal", "hot")
	if _, err := MigrateLegacyStateFiles(Load()); err != nil {
		t.Fatal(err)
	}
	if read(t, "state/biglaw.db-journal") != "hot" || exists("data/biglaw.db-journal") {
		t.Fatal("a hot rollback journal moves with its database")
	}
}

func TestMigrateSymlinkMovesAsLink(t *testing.T) {
	dir := inScratch(t)
	write(t, "real/tasks.json", "linked")
	if err := os.Symlink("real/tasks.json", ".tasks.json"); err != nil { // relative target
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "real", "profiles.json")
	write(t, abs, "p")
	if err := os.Symlink(abs, ".profiles.json"); err != nil { // absolute target
		t.Fatal(err)
	}
	c := Load()
	if _, err := MigrateLegacyStateFiles(c); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{c.Persistence.TasksFile, c.Persistence.ProfilesFile} {
		if info, err := os.Lstat(p); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s should be a symlink: %v", p, err)
		}
	}
	if read(t, c.Persistence.TasksFile) != "linked" || read(t, c.Persistence.ProfilesFile) != "p" {
		t.Fatal("moved links do not resolve to the original files")
	}
}

func TestMigrateRefusesUnmovableState(t *testing.T) {
	inScratch(t)
	if err := os.MkdirAll(".tasks.json", 0o755); err != nil { // a dir where a file belongs
		t.Fatal(err)
	}
	if _, err := MigrateLegacyStateFiles(Load()); err == nil || !strings.Contains(err.Error(), "TASKS_FILE") {
		t.Fatalf("want an actionable error naming TASKS_FILE, got %v", err)
	}
}

// Regression guard: every path-like config field must be a registered state
// path (so it defaults into the data dir and is migrated + checked), or be
// listed here as deliberately not state.
func TestEveryPathFieldIsRegisteredState(t *testing.T) {
	inScratch(t)
	t.Setenv("RUVECTOR_DATA_DIR", "/srv/biglaw")
	c := Load()
	if err := c.CheckStateLayout(); err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, p := range c.StatePaths() {
		registered[p.Path] = true
	}
	notState := map[string]bool{
		"VectorDB.DataDir": true, // the root itself
		"PDF.OutputDir":    true, // generated deliverables, not platform state
		"PDF.PythonBin":    true,
	}
	var walk func(v reflect.Value, name string)
	walk = func(v reflect.Value, name string) {
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				f := v.Type().Field(i)
				if !f.IsExported() {
					continue
				}
				n := f.Name
				if name != "" {
					n = name + "." + f.Name
				}
				walk(v.Field(i), n)
			}
		case reflect.String:
			base := name[strings.LastIndex(name, ".")+1:]
			if !(strings.HasSuffix(base, "File") || strings.HasSuffix(base, "Dir") || strings.HasSuffix(base, "Path")) {
				return
			}
			if notState[name] || v.String() == "" {
				return
			}
			if !registered[v.String()] {
				t.Errorf("%s = %q is a path but not a registered state path", name, v.String())
			}
		}
	}
	walk(reflect.ValueOf(*c), "")
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

func TestDefaultAPIURL(t *testing.T) {
	for host, want := range map[string]string{
		"127.0.0.1": "http://127.0.0.1:3101",
		"0.0.0.0":   "http://127.0.0.1:3101",
		"::":        "http://127.0.0.1:3101",
		"::1":       "http://[::1]:3101",
		"biglaw":    "http://biglaw:3101",
	} {
		if got := LocalAPIURL(host, 3101); got != want {
			t.Errorf("LocalAPIURL(%q) = %q, want %q", host, got, want)
		}
	}
}

// Defaults is the env-free baseline: neither RUVECTOR_DATA_DIR nor a state
// override may reach it, and every state path is the stock ./data default.
func TestDefaultsIgnoreStateEnv(t *testing.T) {
	t.Setenv("RUVECTOR_DATA_DIR", "/elsewhere")
	t.Setenv("TASKS_FILE", "/explicit/tasks.json")
	t.Setenv("BIGLAW_LOCK", "off")
	d := Defaults()
	if d.Persistence.TasksFile != filepath.Join("data", ".tasks.json") {
		t.Errorf("TasksFile = %q, want data/.tasks.json", d.Persistence.TasksFile)
	}
	if d.Run.LockDisabled {
		t.Error("Defaults saw BIGLAW_LOCK")
	}
	for _, p := range d.StatePaths() {
		if p.Explicit || !strings.HasPrefix(p.Path, "data"+string(filepath.Separator)) {
			t.Errorf("%s: %q (explicit=%v) not a ./data default", p.Env, p.Path, p.Explicit)
		}
	}
	if err := d.CheckStateLayout(); err != nil {
		t.Error(err)
	}
	if got := Load().Persistence.TasksFile; got != "/explicit/tasks.json" {
		t.Errorf("Load after Defaults lost the env: TasksFile = %q", got)
	}
}
