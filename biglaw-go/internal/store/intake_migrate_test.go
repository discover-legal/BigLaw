// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/discover-legal/biglaw-go/internal/config"
)

// legacyIntakeTable is intake_submissions as the first intake builds
// created it — before practice_area / noslegal_json / held_json.
const legacyIntakeTable = `CREATE TABLE intake_submissions (
	id TEXT PRIMARY KEY, external_id TEXT NOT NULL DEFAULT '', profile_id TEXT NOT NULL DEFAULT '',
	client_id TEXT NOT NULL DEFAULT '', client_number TEXT NOT NULL DEFAULT '', matter_number TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '', document_type TEXT NOT NULL DEFAULT '', matter_type TEXT NOT NULL DEFAULT '',
	jurisdiction TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', document_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'received', note TEXT NOT NULL DEFAULT '', assigned_to TEXT NOT NULL DEFAULT '',
	task_id TEXT NOT NULL DEFAULT '', conflict %BOOL% NOT NULL DEFAULT %FALSE%, conflict_json %JSON% NOT NULL DEFAULT %EMPTY%,
	metadata_json %JSON% NOT NULL DEFAULT %EMPTY%, created_at %TS% NOT NULL %TSDEF%, updated_at %TS% NOT NULL %TSDEF%)`

func roundTripHeld(t *testing.T, repo DocRepository) {
	t.Helper()
	ir := repo.(IntakeRepository)
	ctx := WithSystem(context.Background())
	sub := IntakeSubmission{ID: "s1", ExternalID: "d1", ProfileID: "p1", Status: "conflict_hold",
		PracticeArea: "Family", NosLegalJSON: []byte(`{"areaOfLaw":"Family"}`), HeldJSON: []byte(`{"content":"x"}`),
		ConflictJSON: []byte(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := ir.PutIntakeSubmission(ctx, sub); err != nil {
		t.Fatalf("put on a migrated legacy table: %v", err)
	}
	got, ok, err := ir.GetIntakeSubmission(ctx, "s1")
	if err != nil || !ok || got.PracticeArea != "Family" || !strings.Contains(string(got.HeldJSON), "content") {
		t.Fatalf("round trip: %v %v %+v", err, ok, got)
	}
}

func TestLegacyIntakeTableMigratesSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.NewReplacer("%BOOL%", "INTEGER", "%FALSE%", "0", "%JSON%", "TEXT", "%EMPTY%", "''",
		"%TS%", "TEXT", "%TSDEF%", "DEFAULT ''").Replace(legacyIntakeTable)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	repo, err := openSQLite(path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	roundTripHeld(t, repo)
	// Re-opening an already-migrated database is a no-op.
	_ = repo.Close()
	again, err := openSQLite(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	_ = again.Close()
}

func TestLegacyIntakeTableMigratesPostgres(t *testing.T) {
	superURL := os.Getenv("PG_CRM_TEST_URL")
	if superURL == "" {
		t.Skip("PG_CRM_TEST_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, superURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	const db = "biglaw_intake_legacy_migrate"
	_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+db)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if c, e := pgx.Connect(ctx, superURL); e == nil {
			_, _ = c.Exec(ctx, `DROP DATABASE IF EXISTS `+db+` WITH (FORCE)`)
			c.Close(ctx)
		}
	})
	u := superURL
	if i := strings.LastIndex(u, "/"); i > len("postgres://") {
		rest := ""
		if q := strings.Index(u[i:], "?"); q >= 0 {
			rest = u[i+q:]
		}
		u = u[:i] + "/" + db + rest
	}
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.NewReplacer("%BOOL%", "BOOLEAN", "%FALSE%", "FALSE", "%JSON%", "JSONB", "%EMPTY%", "'{}'::jsonb",
		"%TS%", "TIMESTAMPTZ", "%TSDEF%", "DEFAULT now()").Replace(legacyIntakeTable)
	if _, err := conn.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	conn.Close(ctx)
	cfg := &config.Config{}
	cfg.Database.URL = u
	repo, err := openPostgres(cfg)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	defer repo.Close()
	roundTripHeld(t, repo)
}
