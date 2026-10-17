// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		status string
	}{{nil, 200, "ok"}, {errors.New("dial tcp 10.0.0.5:5432: secret-host"), 503, "database unavailable"}} {
		var logs bytes.Buffer
		rec := httptest.NewRecorder()
		routes(fakeDB{tc.err}, slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
		var body map[string]string
		json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.code || body["status"] != tc.status || strings.Contains(rec.Body.String(), "secret-host") {
			t.Errorf("health(%v) = %d %s", tc.err, rec.Code, rec.Body.String())
		}
		if tc.err != nil && !strings.Contains(logs.String(), "secret-host") {
			t.Errorf("health(%v): cause not logged for the operator: %q", tc.err, logs.String())
		}
	}
	for _, req := range []*http.Request{httptest.NewRequest("GET", "/", nil), httptest.NewRequest("POST", "/health", nil)} {
		rec := httptest.NewRecorder()
		routes(fakeDB{}, discard()).ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("%s %s = 200, want an error", req.Method, req.URL.Path)
		}
	}
}

func TestRunCommands(t *testing.T) {
	cases := []struct {
		args []string
		env  func(string) string
		code int
		out  string
	}{
		{nil, env(), 2, "usage"},
		{[]string{"bogus"}, env(), 2, "unknown command"},
		{[]string{"version"}, env(), 0, "biglaw 2.0.0-dev"},
		{[]string{"version", "--config"}, env("BIGLAW_DATABASE_URL", dsn), 0, "BIGLAW_ADDR                127.0.0.1:3101  [default]"},
		{[]string{"version", "--config"}, env(), 2, "not valid"},
		{[]string{"version", "--bogus"}, env(), 2, "usage"},
		{[]string{"version", "--config", "x"}, env(), 2, "usage"},
		{[]string{"serve", "extra"}, env(), 2, "usage"},
		{[]string{"serve", "--allow-no-auth", "x"}, env(), 2, "usage"},
		{[]string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "https://api.anthropic.com/v1"), 2, "vendor breaker"},
		{[]string{"help"}, env(), 0, "--allow-no-auth"},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		code := run(context.Background(), tc.args, tc.env, nil, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.out) {
			t.Errorf("biglaw %v: code %d, output %q", tc.args, code, out.String()+errb.String())
		}
	}
}

// TestServeEndToEnd opens (and migrates) a real store, serves /health, and
// stops cleanly on cancel.
func TestServeEndToEnd(t *testing.T) {
	st, err := store.Open(t.Context(), storetest.DSN(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, st, ln, discard()) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/health") // ln is already accepting
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("/health = %d %s", resp.StatusCode, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v after cancel", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}

// TestServeStopsCleanlyWhileWaitingForLock: a signal during startup (here,
// while another process holds the migration lock) gives a clear message.
func TestServeStopsCleanlyWhileWaitingForLock(t *testing.T) {
	db := storetest.DSN(t)
	holder, err := store.Open(t.Context(), db, nil) // migrated; now hold the lock
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holdMigrationLock(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", db, "BIGLAW_ADDR", "127.0.0.1:0"), nil, &stdout, &stderr)
	for _, want := range []string{"waiting for the migration lock", "stopped by a signal before startup finished", "held by another BigLaw process"} {
		if code != 1 || !strings.Contains(stderr.String(), want) {
			t.Errorf("code %d, stderr lacks %q:\n%s", code, want, stderr.String())
		}
	}
}

// holdMigrationLock takes the advisory lock store.MigrateFS uses for the
// DSN's schema on its own session until the test ends.
func holdMigrationLock(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `SELECT pg_advisory_lock(hashtext('biglaw.migrate.' || current_schema()))`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); db.Close() })
}
