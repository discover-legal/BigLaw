// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations is the embedded migration directory (files at its root).
var Migrations, _ = fs.Sub(embedded, "migrations")

// Migration is one validated NNNN_name.sql file.
type Migration struct {
	Version  int
	Name     string // full file name
	SQL      string
	Checksum string // sha256 hex of SQL
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// LoadMigrations reads every file at the root of fsys and checks that each
// is named NNNN_name.sql and that versions run 0001, 0002, … with no gaps or
// duplicates. Any other file is an error, so a misnamed migration can never
// be silently skipped.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var out []Migration
	for i, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			return nil, fmt.Errorf("migrations: %q is not named NNNN_name.sql", e.Name())
		}
		v, _ := strconv.Atoi(m[1])
		if v != i+1 {
			return nil, fmt.Errorf("migrations: %q has version %04d, want %04d (versions must start at 0001 and increase by one)", e.Name(), v, i+1)
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrations: %w", err)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{Version: v, Name: e.Name(), SQL: string(body), Checksum: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// Migrate applies the embedded migrations. See MigrateFS.
func Migrate(ctx context.Context, db *sql.DB) error { return MigrateFS(ctx, db, Migrations) }

// MigrateFS applies every pending migration in fsys to the schema named by
// the connection's search_path. It holds a session advisory lock keyed on
// that schema for the whole run, so concurrent runners serialise and each
// file is applied exactly once. Each file runs in its own transaction
// together with its schema_migrations row: a failing file leaves neither.
// It refuses to run when the database records a migration this binary does
// not have (a newer build migrated it) or one whose contents changed.
func MigrateFS(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	files, err := LoadMigrations(fsys)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Close()

	var schema sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if !schema.Valid {
		return fmt.Errorf("migrate: no schema on the search_path exists")
	}
	lockKey := "biglaw.migrate." + schema.String
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, lockKey); err != nil {
			// Never hand a connection that may still hold the lock back to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		name       text        NOT NULL,
		checksum   text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	applied := map[int][2]string{}
	rows, err := conn.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for rows.Next() {
		var v int
		var name, sum string
		if err := rows.Scan(&v, &name, &sum); err != nil {
			rows.Close()
			return fmt.Errorf("migrate: %w", err)
		}
		applied[v] = [2]string{name, sum}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for v, rec := range applied {
		if v < 1 || v > len(files) {
			return fmt.Errorf("migrate: database has migration %s, which this binary does not know (was it migrated by a newer build?)", rec[0])
		}
		if f := files[v-1]; f.Name != rec[0] || f.Checksum != rec[1] {
			return fmt.Errorf("migrate: migration %04d was changed after it was applied (database has %s sha256 %.12s, binary has %s sha256 %.12s); add a new migration instead of editing an applied one", v, rec[0], rec[1], f.Name, f.Checksum)
		}
	}

	for _, f := range files {
		if _, ok := applied[f.Version]; ok {
			continue
		}
		if err := applyOne(ctx, conn, f); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, f Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", f.Name, err)
	}
	if _, err := tx.ExecContext(ctx, f.SQL); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migrate %s: %w", f.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		f.Version, f.Name, f.Checksum); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("migrate %s: %w", f.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate %s: %w", f.Name, err)
	}
	return nil
}
