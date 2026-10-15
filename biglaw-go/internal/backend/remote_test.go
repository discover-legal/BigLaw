// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package backend

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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
		{http.StatusForbidden, ErrUnauthorized},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusServiceUnavailable, ErrUnavailable},
		{http.StatusInternalServerError, ErrInternal},
		{http.StatusTeapot, ErrInternal},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		}))
		r, _ := NewRemote(srv.URL, "")
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
		got = r
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	r, err := NewRemote(srv.URL+"/", "s3cret")
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

func TestRemotePingUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens here now

	r, _ := NewRemote("http://"+addr, "")
	_, err = r.Ping(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestNewRemoteRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "localhost:3101", "ftp://x", "http://"} {
		if _, err := NewRemote(u, ""); err == nil {
			t.Errorf("NewRemote(%q) accepted", u)
		}
	}
}
