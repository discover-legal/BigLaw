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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
		routes(fakeDB{tc.err}, nil, slog.New(slog.NewTextHandler(&logs, nil))).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
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
		routes(fakeDB{}, nil, discard()).ServeHTTP(rec, req)
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
	orphan, err := st.PutFile(t.Context(), "crashed.pdf", "application/pdf", strings.NewReader("%PDF-")) // an ingest a crash interrupted
	if err != nil {
		t.Fatal(err)
	}
	st.DB().Exec(`UPDATE files SET created_at = now() - interval '2 hours'`)
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
	if _, err := st.GetFile(t.Context(), orphan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("serve did not sweep the orphan file at startup: %v", err)
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
// while another process holds the migration lock) gives a clear message,
// and the port is not open until migrations are done.
func TestServeStopsCleanlyWhileWaitingForLock(t *testing.T) {
	db := storetest.DSN(t)
	holder, err := store.Open(t.Context(), db, nil) // migrated; now hold the lock
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holdMigrationLock(t, db)
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := free.Addr().String()
	free.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stderr syncBuffer
	code := make(chan int, 1)
	go func() {
		code <- run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", db, "BIGLAW_ADDR", addr), nil, io.Discard, &stderr)
	}()
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(stderr.String(), "waiting for the migration lock"); {
		if time.Now().After(deadline) {
			t.Fatalf("serve never waited for the lock:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Errorf("%s accepts connections while migrations wait for the lock", addr)
	}
	cancel()
	got := <-code
	for _, want := range []string{"stopped by a signal before startup finished", "held by another BigLaw process"} {
		if got != 1 || !strings.Contains(stderr.String(), want) {
			t.Errorf("code %d, stderr lacks %q:\n%s", got, want, stderr.String())
		}
	}
}

// syncBuffer is a bytes.Buffer safe for one writer and one reader.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

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

// TestServeLogsModelSetup: before touching the database, serve warns that the key would cross plain
// http and logs each role's price once, or that it is unpriced (r1 L2, R1-1; r2 owner ruling: the
// host plays no part, so qwen2.5:14b on localhost is priced).
func TestServeLogsModelSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stop at store.Open: only the startup log matters here
	var stderr bytes.Buffer
	run(ctx, []string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "http://localhost:4000/v1", "BIGLAW_MODEL_KEY", "sk-SECRET",
		"BIGLAW_MODEL_LEAD", "claude-sonnet-4-6", "BIGLAW_MODEL_CRITIC", "qwen2.5:14b", "BIGLAW_MODEL_SPECIALIST", "my-finetune"), nil, io.Discard, &stderr)
	out := stderr.String()
	for _, want := range []string{"BIGLAW_MODEL_KEY will be sent over plain http",
		`role=lead model=claude-sonnet-4-6 price="$3 in / $15 out per 1M tokens"`, `role=specialist model=my-finetune price="unpriced: cost unknown`,
		`role=critic model=qwen2.5:14b price="$1.6 in / $6.4 out per 1M tokens"`} {
		if !strings.Contains(out, want) {
			t.Errorf("serve log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("serve logged the key:\n%s", out)
	}
}

// TestBinaryIsItsOwnPDFReader: the built binary answers the PDF reader child protocol, so the
// server's uploads parse PDFs out of process (docs.MaybeRunChild is wired into main).
func TestBinaryIsItsOwnPDFReader(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "biglaw")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "__biglaw-extract", strconv.Itoa(os.Getpid()), "60000", "1073741824")
	cmd.Stdin = strings.NewReader("%PDF-1.4 not really")
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), `"Err":"docs: the file could not be read: the PDF is damaged or encrypted"`) {
		t.Errorf("child answer: %s %v", out, err)
	}
}
