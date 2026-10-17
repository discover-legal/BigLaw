// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"bytes"
	"context"
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

	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		status string
	}{{nil, 200, "ok"}, {errors.New("dial tcp 10.0.0.5:5432: secret-host"), 503, "database unavailable"}} {
		rec := httptest.NewRecorder()
		routes(fakeDB{tc.err}).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
		var body map[string]string
		json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.code || body["status"] != tc.status || strings.Contains(rec.Body.String(), "secret-host") {
			t.Errorf("health(%v) = %d %s", tc.err, rec.Code, rec.Body.String())
		}
	}
	for _, req := range []*http.Request{httptest.NewRequest("GET", "/", nil), httptest.NewRequest("POST", "/health", nil)} {
		rec := httptest.NewRecorder()
		routes(fakeDB{}).ServeHTTP(rec, req)
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
		{[]string{"version", "--config"}, env("BIGLAW_DATABASE_URL", dsn), 0, "BIGLAW_BUDGET_CALLS        150  [default]"},
		{[]string{"version", "--config"}, env(), 2, "not valid"},
		{[]string{"version", "--bogus"}, env(), 2, "usage"},
		{[]string{"serve", "extra"}, env(), 2, "usage"},
		{[]string{"serve"}, env("BIGLAW_DATABASE_URL", dsn, "BIGLAW_MODEL_URL", "https://api.anthropic.com/v1"), 2, "vendor breaker"},
		{[]string{"help"}, env(), 0, "serve"},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		code := run(context.Background(), tc.args, tc.env, nil, &out, &errb)
		if code != tc.code || !strings.Contains(out.String()+errb.String(), tc.out) {
			t.Errorf("biglaw %v: code %d, output %q", tc.args, code, out.String()+errb.String())
		}
	}
}

// TestServeEndToEnd starts the real server against a test schema: it
// migrates on start, answers /health, and stops cleanly on cancel.
func TestServeEndToEnd(t *testing.T) {
	db := storetest.DSN(t)
	cfg, err := Load(env("BIGLAW_DATABASE_URL", db))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, ln, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	url := "http://" + ln.Addr().String() + "/health"
	var resp *http.Response
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if resp, err = http.Get(url); err == nil {
			break
		}
	}
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
