// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package blob

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// ─── Pure-logic unit tests (no network) ────────────────────────────────────────

func TestOCITagSanitization(t *testing.T) {
	cases := map[string]string{
		"doc-uuid/att-uuid": "doc-uuid_att-uuid",
		"a/b/c":             "a_b_c",
		"plain":             "plain",
	}
	for in, want := range cases {
		if got := ociTag(in); got != want {
			t.Errorf("ociTag(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(ociTag(in), "/: ") {
			t.Errorf("ociTag(%q) = %q contains an invalid tag char", in, ociTag(in))
		}
	}
	if got := ociTag(strings.Repeat("x", 300)); len(got) > 128 {
		t.Errorf("ociTag must cap at 128 chars, got %d", len(got))
	}
}

func TestWebDAVURLBuild(t *testing.T) {
	w, err := NewWebDAVStore("https://dav.example.com/biglaw/", "u", "p")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.url("docX/attY"); got != "https://dav.example.com/biglaw/docX/attY" {
		t.Errorf("webdav url = %q", got)
	}
}

func TestSupabaseURLBuild(t *testing.T) {
	s, err := NewSupabaseStore("https://ref.supabase.co/", "attachments", "key")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://ref.supabase.co/storage/v1/object/attachments/docX/attY"
	if got := s.objectURL("docX/attY"); got != want {
		t.Errorf("supabase url = %q, want %q", got, want)
	}
}

func TestBackendsRequireConfig(t *testing.T) {
	if _, err := NewWebDAVStore("", "", ""); err == nil {
		t.Error("webdav should require a URL")
	}
	if _, err := NewSupabaseStore("", "", ""); err == nil {
		t.Error("supabase should require URL+bucket")
	}
	if _, err := NewOCIStore("", "", "", false); err == nil {
		t.Error("oci should require a ref")
	}
}

// ─── Env-gated integration tests (run against a real server when configured) ────

func TestWebDAVIntegration(t *testing.T) {
	url := os.Getenv("BLOB_WEBDAV_URL")
	if url == "" {
		t.Skip("BLOB_WEBDAV_URL not set")
	}
	s, err := NewWebDAVStore(url, os.Getenv("BLOB_WEBDAV_USER"), os.Getenv("BLOB_WEBDAV_PASS"))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, s)
}

func TestSupabaseIntegration(t *testing.T) {
	url := os.Getenv("BLOB_SUPABASE_URL")
	if url == "" {
		t.Skip("BLOB_SUPABASE_URL not set")
	}
	s, err := NewSupabaseStore(url, envOr("BLOB_SUPABASE_BUCKET", "attachments"), os.Getenv("BLOB_SUPABASE_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, s)
}

func TestOCIIntegration(t *testing.T) {
	ref := os.Getenv("BLOB_OCI_REF")
	if ref == "" {
		t.Skip("BLOB_OCI_REF not set")
	}
	s, err := NewOCIStore(ref, os.Getenv("BLOB_OCI_USER"), os.Getenv("BLOB_OCI_PASS"), os.Getenv("BLOB_OCI_PLAIN_HTTP") == "1")
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, s)
}

func roundTrip(t *testing.T, s Store) {
	t.Helper()
	key := "itest-doc/itest-att"
	data := []byte("attachment bytes \x00\x01\x02 PDF-ish")
	if err := s.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("round-trip mismatch on %s backend", s.Backend())
	}
	if err := s.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
