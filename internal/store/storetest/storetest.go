// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package storetest gives database tests an isolated Postgres schema.
//
// DSN(t) reads BIGLAW_TEST_DSN, creates a fresh schema t_<random>, and returns
// a DSN whose search_path is that schema, so every pooled connection opened
// from it lands there. The schema is dropped (CASCADE) when the test ends, so
// tests run in parallel without seeing each other's rows.
//
// Without BIGLAW_TEST_DSN the test is skipped, unless BIGLAW_REQUIRE_DB is
// set (CI sets it), in which case the missing database is a failure. A DSN
// that is set but unreachable is always a failure.
//
// At most Slots tests across every test process hold a schema at once (DSN
// waits for a slot), so the database's connections stay bounded whatever
// go test's -p and -parallel are.
//
// It deliberately does not import package store, so store's own tests can use it.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// T is the subset of testing.TB that DSN needs (an interface so the
// skip/fail policy itself can be tested).
type T interface {
	Helper()
	Skip(args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}

var _ T = testing.TB(nil)

// Pool is the connection pool a test store opens with (store.OpenPool): small,
// so the stores of the Slots tests running at once stay well inside the
// server's max_connections. A test that exercises the production pool asks
// for store.MaxOpenConns explicitly.
const Pool = 4

// Open opens a test store with open (store.OpenPool; storetest cannot import store, whose own tests
// import it) on the test's own schema (DSN), or on dsn when one is given, with the test pool, and
// closes it when the test ends: st := storetest.Open(t, store.OpenPool).
func Open[S interface{ Close() error }](t testing.TB, open func(context.Context, string, *slog.Logger, int) (S, error), dsn ...string) S {
	t.Helper()
	if len(dsn) == 0 {
		dsn = []string{DSN(t)}
	}
	st, err := open(context.Background(), dsn[0], nil, Pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// DSN returns a DSN scoped to a new schema that is dropped after the test.
func DSN(t testing.TB) string {
	t.Helper()
	if base := strings.TrimSpace(os.Getenv("BIGLAW_TEST_DSN")); base != "" {
		hold(t, base) // registered first, so given back after the schema is dropped
	}
	return DSNFrom(t, os.Getenv)
}

// DSNFrom is DSN with an explicit environment lookup.
func DSNFrom(t T, getenv func(string) string) string {
	t.Helper()
	base := strings.TrimSpace(getenv("BIGLAW_TEST_DSN"))
	if base == "" {
		if truthy(getenv("BIGLAW_REQUIRE_DB")) {
			t.Fatalf("BIGLAW_REQUIRE_DB is set but BIGLAW_TEST_DSN is empty: database tests must run, not skip")
			return ""
		}
		t.Skip("database test: set BIGLAW_TEST_DSN to run it")
		return ""
	}
	admin, err := sql.Open("postgres", base)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // a blackholed host must not hang the suite
		err = admin.PingContext(ctx)
		cancel()
	}
	if err != nil {
		t.Fatalf("BIGLAW_TEST_DSN is set but the database is unreachable: %v", err)
		return ""
	}
	extSchema, err := vectorOnce(admin)
	if err != nil {
		admin.Close()
		t.Fatalf("storetest: the pgvector extension (vector) is missing and could not be created (use a pgvector/pgvector image): %v", err)
		return ""
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	schema := "t_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("storetest: create schema: %v", err)
		return ""
	}
	admin.Close() // the test's own pools are its only connections until the drop
	t.Cleanup(func() {
		if admin, err := sql.Open("postgres", base); err == nil {
			_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
			admin.Close()
		}
	})
	return WithSearchPath(base, schema+","+extSchema)
}

// vectorOnce creates pgvector once per database as store.Open does, never in a test's schema (its drop
// would drop it for all), and returns its schema, which test schemas put on their search_path.
func vectorOnce(admin *sql.DB) (schema string, err error) {
	if _, err = admin.Exec(`SELECT pg_advisory_xact_lock(hashtext('biglaw.extension.vector')); CREATE EXTENSION IF NOT EXISTS vector`); err == nil {
		err = admin.QueryRow(`SELECT extnamespace::regnamespace::text FROM pg_extension WHERE extname = 'vector'`).Scan(&schema)
	}
	return schema, err
}

// WithSearchPath returns dsn with its search_path set to schema. lib/pq sends
// unrecognised DSN keys as run-time parameters on every new connection.
func WithSearchPath(dsn, schema string) string {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
