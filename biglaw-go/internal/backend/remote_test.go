// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/timekeeping"
)

func TestRemoteMapsStatusesToErrorKinds(t *testing.T) {
	for _, c := range []struct {
		status int
		kind   error
	}{
		{http.StatusBadRequest, ErrInvalid},
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrForbidden},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusInternalServerError, ErrInternal},
		{http.StatusBadGateway, ErrUnavailable},
		{http.StatusGatewayTimeout, ErrUnavailable},
		{http.StatusTeapot, ErrInternal},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if serveMe(w, req) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		}))
		r, _ := NewRemote(srv.URL, "", 0)
		_, err := r.GetTask(context.Background(), "t")
		srv.Close()
		if !errors.Is(err, c.kind) {
			t.Errorf("status %d: want %v, got %v", c.status, c.kind, err)
		}
		if c.status != http.StatusTeapot && err.Error() != "boom" {
			t.Errorf("status %d: message %q, want the API's error text", c.status, err.Error())
		}
	}
}

func TestRemoteRequestShape(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMe(w, r) {
			return
		}
		got = r
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	r, err := NewRemote(srv.URL+"/", "s3cret", 0)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := r.ListTimeEntries(context.Background(), timekeeping.TimeFilter{MatterNumber: "M 1", From: &from}); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/time-entries" || got.URL.Query().Get("matterNumber") != "M 1" ||
		got.URL.Query().Get("from") != "2026-01-02T00:00:00Z" || got.URL.Query().Has("to") {
		t.Fatalf("request = %s", got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got.Header.Get("Authorization"))
	}

	// IDs are escaped as single path segments.
	_, _ = r.GetTask(context.Background(), "../agents")
	if got.URL.EscapedPath() != "/tasks/..%2Fagents" {
		t.Fatalf("escaped path = %s", got.URL.EscapedPath())
	}
}

// serveMe answers the attach probe like a BigLaw backend.
func serveMe(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/me" {
		return false
	}
	_, _ = w.Write([]byte(`{"user":{"profileId":"local-partner","role":"partner"},"authEnabled":false}`))
	return true
}

func TestRemoteNeverFollowsRedirects(t *testing.T) {
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"user":{"profileId":"x"}}`))
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/me" && serveMe(w, r) {
			return
		}
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	r, _ := NewRemote(srv.URL, "SECRET", 0)
	if _, err := r.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("ping through a redirect: want a redirect error, got %v", err)
	}
	if leaked != "" {
		t.Fatalf("bearer forwarded across a redirect: %q", leaked)
	}
}

func TestRemoteRedactsUserinfo(t *testing.T) {
	r, err := NewRemote("http://user:hunter2@127.0.0.1:1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.URL(), "hunter2") {
		t.Fatalf("URL() leaks the password: %s", r.URL())
	}
	if _, err := r.Ping(context.Background()); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error leaks the password: %v", err)
	}
}

// The first call waits out a backend that is still booting (within grace).
func TestRemoteAttachWaitsForBootingOwner(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r, _ := NewRemote("http://"+addr, "", 10*time.Second)
	go func() {
		time.Sleep(700 * time.Millisecond)
		l2, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		_ = http.Serve(l2, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if !serveMe(w, req) {
				_, _ = w.Write([]byte(`[]`))
			}
		}))
	}()
	if _, err := r.ListAgents(context.Background()); err != nil {
		t.Fatalf("call during boot grace: %v", err)
	}
}

func TestRemotePingUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens here now

	r, _ := NewRemote("http://"+addr, "", 0)
	_, err = r.Ping(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestNewRemoteRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "localhost:3101", "ftp://x", "http://", "http://:3101"} {
		if _, err := NewRemote(u, "", 0); err == nil {
			t.Errorf("NewRemote(%q) accepted", u)
		}
	}
}

// The 16 MiB rule measures the body the client sends, not json.Marshal's
// HTML-escaped re-encoding (which turns each <, > and & into six bytes).
func TestRequestSizeIgnoresHTMLEscaping(t *testing.T) {
	for _, ch := range []string{"<", "&", ">"} {
		in := IngestInput{Title: "markup", Content: strings.Repeat(ch, 12<<20)} // 12 MiB, 72 MiB if escaped
		if err := checkSize(in); err != nil {
			t.Fatalf("%q-heavy 12 MiB body refused: %v", ch, err)
		}
	}
	if err := checkSize(IngestInput{Content: strings.Repeat("&", MaxRequestBytes)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a body over the cap must still be refused, got %v", err)
	}

	var gotLen int64
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMe(w, r) {
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotLen, gotBody = r.ContentLength, string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"d1"}`))
	}))
	defer srv.Close()
	r, _ := NewRemote(srv.URL, "", 0)
	content := strings.Repeat("Smith & Jones <v> ", 700_000) // ~12.6 MB raw, ~30 MB escaped
	if _, err := r.IngestDocument(context.Background(), IngestInput{Title: "t", Content: content}); err != nil {
		t.Fatalf("remote ingest refused: %v", err)
	}
	if gotLen > MaxRequestBytes || !strings.Contains(gotBody, "Smith & Jones <v>") {
		t.Fatalf("body sent unescaped and under the cap: len=%d", gotLen)
	}
}

// A backend that accepts connections but never answers must not hold every
// caller for the full request timeout, nor serialize callers behind a lock.
func TestRemoteAttachBoundsAHungBackend(t *testing.T) {
	old := attachProbeTimeout
	attachProbeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { attachProbeTimeout = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	r, _ := NewRemote(srv.URL, "", 0)
	start := time.Now()
	errs := make(chan error, 4)
	for range 4 {
		go func() { _, err := r.Attach(context.Background()); errs <- err }()
	}
	for range 4 {
		if err := <-errs; !errors.Is(err, ErrUnavailable) {
			t.Fatalf("want ErrUnavailable from a hung backend, got %v", err)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("concurrent attaches took %v: probes are serialized or unbounded", d)
	}
}
