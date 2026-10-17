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
	"log/slog"
	"regexp"
	"strconv"
	"time"
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
	entries, err := fs.ReadDir(fsys, ".") // sorted by name
	if err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	var out []Migration
	for i, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			return nil, fmt.Errorf("migrations: %q is not named NNNN_name.sql", e.Name())
		}
		if v, _ := strconv.Atoi(m[1]); v != i+1 {
			return nil, fmt.Errorf("migrations: %q has version %04d, want %04d (versions start at 0001 and increase by one)", e.Name(), v, i+1)
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrations: %w", err)
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{Version: i + 1, Name: e.Name(), SQL: string(body), Checksum: hex.EncodeToString(sum[:])})
	}
	return out, nil
}

// Migrate applies the embedded migrations. See MigrateFS.
func Migrate(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	return MigrateFS(ctx, db, Migrations, log)
}

// MigrateFS applies every pending migration in fsys to the schema named by
// the connection's search_path. Migrations are forward-only.
//
// It holds a session advisory lock keyed on that schema for the whole run,
// so concurrent runners serialise and each file is applied exactly once.
// Waiting for another process's lock is logged and lasts until ctx ends.
// Each file runs in one transaction together with its schema_migrations
// row: if either fails, neither persists. A file must not end that
// transaction itself (COMMIT/ROLLBACK), and so cannot use statements that
// refuse to run in one (CREATE INDEX CONCURRENTLY).
//
// It refuses to run when the database records a migration this binary does
// not have (a newer build migrated it) or one whose contents changed.
func MigrateFS(ctx context.Context, db *sql.DB, fsys fs.FS, log *slog.Logger) error {
	log = orDiscard(log)
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
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, lockKey).Scan(&got); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	if !got {
		start := time.Now()
		log.Info("waiting for the migration lock: another BigLaw process is migrating this database", "schema", schema.String)
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, lockKey); err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("migrate: stopped after %s waiting for the migration lock held by another BigLaw process: %w",
					time.Since(start).Round(time.Second), context.Cause(ctx))
			}
			return fmt.Errorf("migrate: lock: %w", err)
		}
		log.Info("migration lock acquired", "waited", time.Since(start).Round(time.Millisecond).String())
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
	applied := map[int]bool{}
	rows, err := conn.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var name, sum string
		if err := rows.Scan(&v, &name, &sum); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if v < 1 || v > len(files) {
			return fmt.Errorf("migrate: database has migration %s, which this binary does not know (was it migrated by a newer build? migrations are forward-only: restore a backup or run the newer build)", name)
		}
		if f := files[v-1]; f.Name != name || f.Checksum != sum {
			return fmt.Errorf("migrate: migration %04d was changed after it was applied (database has %s sha256 %.12s, binary has %s sha256 %.12s); add a new migration instead of editing an applied one", v, name, sum, f.Name, f.Checksum)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	rows.Close()

	for _, f := range files {
		if applied[f.Version] {
			continue
		}
		if err := applyOne(ctx, conn, f); err != nil {
			return fmt.Errorf("migrate %s: %w", f.Name, err)
		}
		log.Info("applied migration", "name", f.Name)
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.Conn, f Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	// A transaction-local marker: if the file ends the transaction, it is gone.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('biglaw.migration', $1, true)`, f.Name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, f.SQL); err != nil {
		return err
	}
	var marker sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('biglaw.migration', true)`).Scan(&marker); err != nil {
		return err
	}
	if marker.String != f.Name {
		return fmt.Errorf("the file ended the migration's transaction itself (COMMIT or ROLLBACK is not allowed in a migration); " +
			"statements before that point may have been committed, so check the schema before re-running")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		f.Version, f.Name, f.Checksum); err != nil {
		return err
	}
	return tx.Commit()
}
