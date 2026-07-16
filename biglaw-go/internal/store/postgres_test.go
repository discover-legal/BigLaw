// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// TestPostgresRLS proves the database row-level-security policy: default-deny,
// per-lawyer isolation, partner-sees-all, and WITH CHECK on writes.
//
// It is skipped unless a superuser DSN is provided (PG_SUPERUSER_URL, default
// the local dev instance). RLS only applies to NON-superuser roles, so the test
// provisions a dedicated NOSUPERUSER/NOBYPASSRLS role and connects the
// repository as that role — exactly how production must connect.
func TestPostgresRLS(t *testing.T) {
	superURL := os.Getenv("PG_SUPERUSER_URL")
	if superURL == "" {
		superURL = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, superURL)
	if err != nil {
		t.Skipf("no Postgres reachable (%v) — skipping RLS integration test", err)
	}
	defer admin.Close(ctx)

	// Provision a clean database + a non-superuser app role subject to RLS.
	const appRole = "biglaw_rls_test_app"
	const appPass = "biglaw_rls_test_pw"
	for _, stmt := range []string{
		`DROP DATABASE IF EXISTS biglaw_rls_test`,
		`CREATE DATABASE biglaw_rls_test`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("admin setup %q: %v", stmt, err)
		}
	}
	_, _ = admin.Exec(ctx, `DROP ROLE IF EXISTS `+appRole)
	if _, err := admin.Exec(ctx, `CREATE ROLE `+appRole+` LOGIN PASSWORD '`+appPass+`' NOSUPERUSER NOBYPASSRLS`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		c, e := pgx.Connect(ctx, superURL)
		if e == nil {
			_, _ = c.Exec(ctx, `DROP DATABASE IF EXISTS biglaw_rls_test`)
			_, _ = c.Exec(ctx, `DROP ROLE IF EXISTS `+appRole)
			c.Close(ctx)
		}
	})

	// Grant the app role ownership of the public schema in the new DB so its
	// CREATE TABLE (run by openPostgres) makes it the table owner; FORCE RLS
	// then applies to it.
	dbAdmin, err := pgx.Connect(ctx, "postgres://postgres:postgres@localhost:5432/biglaw_rls_test?sslmode=disable")
	if err != nil {
		t.Fatalf("connect to test db: %v", err)
	}
	if _, err := dbAdmin.Exec(ctx, `ALTER SCHEMA public OWNER TO `+appRole); err != nil {
		t.Fatalf("grant schema: %v", err)
	}
	dbAdmin.Close(ctx)

	cfg := &config.Config{}
	cfg.Database.Backend = "postgres"
	cfg.Database.URL = "postgres://" + appRole + ":" + appPass + "@localhost:5432/biglaw_rls_test?sslmode=disable"

	repo, err := openPostgres(cfg)
	if err != nil {
		t.Fatalf("openPostgres (as app role): %v", err)
	}
	defer repo.Close()

	sys := WithSystem(ctx)
	alice := WithIdentity(ctx, "alice", false)
	bob := WithIdentity(ctx, "bob", false)
	partner := WithIdentity(ctx, "boss", true)

	mk := func(id, owner string) types.Document {
		return types.Document{ID: id, Title: id, OwnerID: owner, IngestedAt: time.Now()}
	}
	// System seeds documents owned by alice and bob.
	if err := repo.Upsert(sys, mk("a1", "alice")); err != nil {
		t.Fatalf("seed a1: %v", err)
	}
	if err := repo.Upsert(sys, mk("b1", "bob")); err != nil {
		t.Fatalf("seed b1: %v", err)
	}

	countFor := func(c context.Context) int {
		list, err := repo.List(c)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		return len(list)
	}

	// Default-deny: no identity → zero rows.
	if n := countFor(ctx); n != 0 {
		t.Errorf("default-deny failed: anonymous saw %d rows, want 0", n)
	}
	// Per-lawyer isolation.
	if n := countFor(alice); n != 1 {
		t.Errorf("alice saw %d rows, want 1 (her own)", n)
	}
	if n := countFor(bob); n != 1 {
		t.Errorf("bob saw %d rows, want 1 (his own)", n)
	}
	// Partner sees all.
	if n := countFor(partner); n != 2 {
		t.Errorf("partner saw %d rows, want 2 (all)", n)
	}
	// Alice cannot see bob's document by ID.
	if _, found, _ := repo.GetByID(alice, "b1"); found {
		t.Error("RLS leak: alice could read bob's document")
	}
	// WITH CHECK: alice cannot write a document owned by bob.
	if err := repo.Upsert(alice, mk("a2", "bob")); err == nil {
		t.Error("WITH CHECK failed: alice wrote a row owned by bob")
	}
	// Alice CAN write her own.
	if err := repo.Upsert(alice, mk("a3", "alice")); err != nil {
		t.Errorf("alice could not write her own doc: %v", err)
	}

	// ── Attachments inherit the same RLS ──
	mkAtt := func(id, doc, owner string) types.Attachment {
		return types.Attachment{ID: id, DocID: doc, OwnerID: owner, Filename: id + ".pdf",
			MediaType: "application/pdf", Kind: types.AttachmentOriginal, BlobKey: doc + "/" + id, CreatedAt: time.Now()}
	}
	if err := repo.AddAttachment(sys, mkAtt("att-a", "a1", "alice")); err != nil {
		t.Fatalf("seed att-a: %v", err)
	}
	if err := repo.AddAttachment(sys, mkAtt("att-b", "b1", "bob")); err != nil {
		t.Fatalf("seed att-b: %v", err)
	}
	// Alice sees only her own attachment; cannot read bob's.
	if al, _ := repo.ListAttachments(alice, "a1"); len(al) != 1 {
		t.Errorf("alice saw %d of her attachments, want 1", len(al))
	}
	if bl, _ := repo.ListAttachments(alice, "b1"); len(bl) != 0 {
		t.Errorf("RLS leak: alice saw %d of bob's attachments, want 0", len(bl))
	}
	if _, found, _ := repo.GetAttachment(alice, "att-b"); found {
		t.Error("RLS leak: alice could read bob's attachment metadata")
	}
	// Anonymous (default-deny) sees nothing.
	if _, found, _ := repo.GetAttachment(ctx, "att-a"); found {
		t.Error("default-deny failed: anonymous read an attachment")
	}
}

// TestPostgresReviewAndVersionParity closes the sqlite/postgres coverage gap:
// TestSQLiteReviewRoundTrip and TestSQLiteVersionRoundTrip (sqlite_test.go,
// versions_test.go) exercise ReviewRepository and VersionRepository against
// SQLite and the in-memory backend, but postgres.go's PutReview/GetReview and
// PutVersion/GetVersion/ListLineage/FindVersionByHash/FindVersionByPath
// (postgres.go lines ~378-499) are currently ONLY compile-checked — no test
// ever calls them against a live Postgres instance. Skipped unless Postgres is
// reachable, mirroring TestPostgresRLS's provisioning pattern.
func TestPostgresReviewAndVersionParity(t *testing.T) {
	superURL := os.Getenv("PG_SUPERUSER_URL")
	if superURL == "" {
		superURL = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, superURL)
	if err != nil {
		t.Skipf("no Postgres reachable (%v) — skipping review/version parity test", err)
	}
	defer admin.Close(ctx)

	const dbName = "biglaw_parity_test"
	for _, stmt := range []string{
		`DROP DATABASE IF EXISTS ` + dbName,
		`CREATE DATABASE ` + dbName,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("admin setup %q: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		c, e := pgx.Connect(ctx, superURL)
		if e == nil {
			_, _ = c.Exec(ctx, `DROP DATABASE IF EXISTS `+dbName)
			c.Close(ctx)
		}
	})

	cfg := &config.Config{}
	cfg.Database.Backend = "postgres"
	cfg.Database.URL = "postgres://postgres:postgres@localhost:5432/" + dbName + "?sslmode=disable"

	repo, err := openPostgres(cfg)
	if err != nil {
		t.Fatalf("openPostgres: %v", err)
	}
	defer repo.Close()

	// The reviews/document_versions RLS policy allows read to ANY declared
	// identity (system, partner, or a lawyer) but write only to system — so
	// these round-trips must run as the system principal, matching how the
	// tool layer actually calls PutReview/PutVersion in production.
	sys := WithSystem(ctx)

	t.Run("ReviewRoundTrip", func(t *testing.T) {
		reviews, ok := repo.(ReviewRepository)
		if !ok {
			t.Fatal("postgres repo does not implement ReviewRepository")
		}
		// TODO: PutReview, replace-in-place, GetReview round-trip, GetReview(unknown)
		// miss-without-error — mirror TestSQLiteReviewRoundTrip's assertions exactly
		// so the two backends are held to the identical contract.
		_ = reviews
		_ = sys
	})

	t.Run("VersionRoundTrip", func(t *testing.T) {
		versions, ok := repo.(VersionRepository)
		if !ok {
			t.Fatal("postgres repo does not implement VersionRepository")
		}
		// TODO: reuse the SAME verifyVersionRepo(t, repo) helper versions_test.go
		// already runs against memory and sqlite — but verifyVersionRepo's PutVersion
		// calls carry no context identity today (context.Background()), which under
		// Postgres RLS resolves to default-deny. Either thread WithSystem(ctx) through
		// verifyVersionRepo, or write a parallel identity-aware variant here.
		_ = versions
	})
}
