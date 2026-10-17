// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

func mapFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func TestMigrationFilesOrderedAndUnique(t *testing.T) {
	got, err := LoadMigrations(Migrations)
	if err != nil {
		t.Fatalf("embedded migrations invalid: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no embedded migrations")
	}
	for i, m := range got {
		if m.Version != i+1 || !strings.HasPrefix(m.Name, fmt.Sprintf("%04d_", i+1)) {
			t.Fatalf("migration %d is %q (version %d)", i, m.Name, m.Version)
		}
	}

	bad := map[string]fstest.MapFS{
		"gap":            mapFS(map[string]string{"0001_a.sql": "", "0003_c.sql": ""}),
		"duplicate":      mapFS(map[string]string{"0001_a.sql": "", "0001_b.sql": ""}),
		"starts at 0002": mapFS(map[string]string{"0002_a.sql": ""}),
		"short number":   mapFS(map[string]string{"1_a.sql": ""}),
		"no name":        mapFS(map[string]string{"0001.sql": ""}),
		"upper case":     mapFS(map[string]string{"0001_Init.sql": ""}),
		"stray file":     mapFS(map[string]string{"0001_a.sql": "", "README.md": ""}),
		"wrong ext":      mapFS(map[string]string{"0001_a.sql": "", "0002_b.SQL": ""}),
		"subdirectory":   {"0001_a.sql": {Data: nil}, "0002_dir/x.sql": {Data: nil}},
	}
	for name, fsys := range bad {
		if _, err := LoadMigrations(fsys); err == nil {
			t.Errorf("%s: LoadMigrations accepted an invalid set", name)
		}
	}
}

type migRow struct {
	Version   int
	Name, Sum string
	At        time.Time
}

func migrations(t *testing.T, db *sql.DB) []migRow {
	t.Helper()
	rows, err := db.Query(`SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []migRow
	for rows.Next() {
		var r migRow
		if err := rows.Scan(&r.Version, &r.Name, &r.Sum, &r.At); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestMigrateIdempotent(t *testing.T) {
	t.Parallel()
	dsn := storetest.DSN(t)
	s, err := Open(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := migrations(t, s.db)
	files, _ := LoadMigrations(Migrations)
	if len(first) != len(files) {
		t.Fatalf("after first migrate: %d rows, want %d", len(first), len(files))
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(t.Context(), s.db, nil); err != nil {
			t.Fatalf("re-migrate %d: %v", i, err)
		}
	}
	if again := migrations(t, s.db); !reflect.DeepEqual(first, again) {
		t.Fatalf("schema_migrations changed on re-run:\n first %v\n again %v", first, again)
	}
}

func TestConcurrentMigrateSafe(t *testing.T) {
	t.Parallel()
	dsn := storetest.DSN(t)
	// The sleep widens the race window: without the advisory lock, a second
	// runner reads an empty schema_migrations and re-applies 0001.
	fsys := mapFS(map[string]string{
		"0001_a.sql": `SELECT pg_sleep(0.3); CREATE TABLE a (x int);`,
		"0002_b.sql": `CREATE TABLE b (x int);`,
		"0003_c.sql": `INSERT INTO a VALUES (1);`,
	})
	const runners = 6
	errs := make([]error, runners)
	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		db := openDB(t, dsn) // separate pools: separate sessions
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = MigrateFS(t.Context(), db, fsys, nil)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("runner %d: %v", i, err)
		}
	}
	db := openDB(t, dsn)
	if got := migrations(t, db); len(got) != 3 {
		t.Fatalf("schema_migrations has %d rows, want 3: %v", len(got), got)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM a`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0003 applied %d times (err %v), want exactly once", n, err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	t.Parallel()
	dsn := storetest.DSN(t)
	db := openDB(t, dsn)
	good := `CREATE TABLE ok_t (x int);`
	bad := mapFS(map[string]string{
		"0001_ok.sql":  good,
		"0002_bad.sql": `CREATE TABLE partial_t (x int); CREATE INDEX partial_i ON partial_t (x); SELECT 1/0;`,
	})
	err := MigrateFS(t.Context(), db, bad, nil)
	if err == nil || !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Fatalf("err = %v, want a failure naming 0002_bad.sql", err)
	}
	if !tableExists(t, db, "ok_t") {
		t.Fatal("0001 should have been committed before 0002 failed")
	}
	if tableExists(t, db, "partial_t") || tableExists(t, db, "partial_i") {
		t.Fatal("failed migration left partial schema behind")
	}
	if got := migrations(t, db); len(got) != 1 || got[0].Version != 1 {
		t.Fatalf("schema_migrations = %v, want only version 1", got)
	}
	// Fixing the file lets the next run finish.
	fixed := mapFS(map[string]string{"0001_ok.sql": good, "0002_bad.sql": `CREATE TABLE partial_t (x int);`})
	if err := MigrateFS(t.Context(), db, fixed, nil); err != nil {
		t.Fatalf("after fix: %v", err)
	}
	if got := migrations(t, db); len(got) != 2 {
		t.Fatalf("after fix: %d rows, want 2", len(got))
	}
}

func TestMigrateRefusesDrift(t *testing.T) {
	t.Parallel()
	db := openDB(t, storetest.DSN(t))
	two := mapFS(map[string]string{"0001_a.sql": `CREATE TABLE a (x int);`, "0002_b.sql": `CREATE TABLE b (x int);`})
	if err := MigrateFS(t.Context(), db, two, nil); err != nil {
		t.Fatal(err)
	}
	edited := mapFS(map[string]string{"0001_a.sql": `CREATE TABLE a (x bigint);`, "0002_b.sql": `CREATE TABLE b (x int);`})
	if err := MigrateFS(t.Context(), db, edited, nil); err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Errorf("edited applied migration: err = %v", err)
	}
	older := mapFS(map[string]string{"0001_a.sql": `CREATE TABLE a (x int);`})
	if err := MigrateFS(t.Context(), db, older, nil); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Errorf("binary older than database: err = %v", err)
	}
}

func TestMigrateNeedsExistingSchema(t *testing.T) {
	t.Parallel()
	dsn := storetest.WithSearchPath(storetest.DSN(t), "no_such_schema_x")
	if err := Migrate(t.Context(), openDB(t, dsn), nil); err == nil || !strings.Contains(err.Error(), "no schema") {
		t.Fatalf("err = %v, want a missing-schema error", err)
	}
}

// TestMigrationRowFailureRollsBackFile pins the per-file transaction: the
// file's SQL succeeds, then recording its schema_migrations row fails (a
// trigger the file itself installs raises). Nothing from the file may
// persist. Without the transaction, table a and the trigger would remain and
// every later start would fail on "relation already exists".
func TestMigrationRowFailureRollsBackFile(t *testing.T) {
	t.Parallel()
	db := openDB(t, storetest.DSN(t))
	fsys := mapFS(map[string]string{"0001_a.sql": `
		CREATE TABLE a (x int);
		CREATE FUNCTION refuse_row() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'row refused'; END $$;
		CREATE TRIGGER refuse BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION refuse_row();`})
	err := MigrateFS(t.Context(), db, fsys, nil)
	if err == nil || !strings.Contains(err.Error(), "row refused") {
		t.Fatalf("err = %v, want the row insert's failure", err)
	}
	if tableExists(t, db, "a") {
		t.Fatal("the file's table persisted although its schema_migrations row was not written")
	}
	var triggers int
	if err := db.QueryRow(`SELECT count(*) FROM pg_trigger WHERE tgname = 'refuse'`).Scan(&triggers); err != nil || triggers != 0 {
		t.Fatalf("the file's trigger persisted (%d, %v)", triggers, err)
	}
	if got := migrations(t, db); len(got) != 0 {
		t.Fatalf("schema_migrations = %v, want empty", got)
	}
}

// TestMigrationMayNotEndTransaction: a file that COMMITs or ROLLBACKs the
// runner's transaction is refused and not recorded as applied.
func TestMigrationMayNotEndTransaction(t *testing.T) {
	t.Parallel()
	for _, stmt := range []string{"COMMIT", "ROLLBACK", "END"} {
		db := openDB(t, storetest.DSN(t))
		err := MigrateFS(t.Context(), db, mapFS(map[string]string{"0001_a.sql": "CREATE TABLE a (x int); " + stmt + "; CREATE TABLE b (x int);"}), nil)
		if err == nil || !strings.Contains(err.Error(), "ended the migration's transaction") {
			t.Errorf("%s: err = %v", stmt, err)
		}
		if got := migrations(t, db); len(got) != 0 {
			t.Errorf("%s: recorded as applied: %v", stmt, got)
		}
	}
}

// TestMigrateWaitsForLockAndSaysSo: a runner that finds the lock taken logs
// that it is waiting, gives a clear error when its context ends, and
// proceeds once the lock is released.
func TestMigrateWaitsForLockAndSaysSo(t *testing.T) {
	t.Parallel()
	dsn := storetest.DSN(t)
	holder := openDB(t, dsn)
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	lock := `SELECT pg_advisory_lock(hashtext('biglaw.migrate.' || current_schema()))`
	if _, err := conn.ExecContext(t.Context(), lock); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	err = Migrate(ctx, openDB(t, dsn), log)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "waiting for the migration lock held by another BigLaw process") ||
		!strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(logs.String(), "waiting for the migration lock") {
		t.Fatalf("no waiting log line: %q", logs.String())
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock_all()`)
	}()
	logs.Reset()
	if err := Migrate(t.Context(), openDB(t, dsn), log); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"waiting for the migration lock", "migration lock acquired", "name=0001_init.sql"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q: %q", want, logs.String())
		}
	}
}
