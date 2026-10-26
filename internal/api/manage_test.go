// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

// manageServer is the API over a fresh schema with who deciding each request's principal.
func manageServer(t *testing.T, who Who) *server {
	t.Helper()
	st, err := store.OpenPool(context.Background(), storetest.DSN(t), nil, storetest.Pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := docs.New(st)
	e := engine.New(st, d, nil, roles, nil, engine.Config{BudgetCalls: 150, BudgetUSD: 5, CollapseThreshold: 0.5, CollapseAction: "strict"}, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(handler(d, e, nil, slog.New(slog.DiscardHandler), who))
	t.Cleanup(srv.Close)
	return &server{srv, t, e, nil, st}
}

const leadPath = "internal/engine/specs/lead.md"

func leadFiles(t *testing.T, s *server, tail string) map[string]string {
	t.Helper()
	code, body := s.do("GET", "/manage/items/prompt.lead", nil)
	var d engine.ItemDetail
	if code != 200 || json.Unmarshal([]byte(body), &d) != nil {
		t.Fatalf("detail: %d %s", code, body)
	}
	return map[string]string{leadPath: d.Original[leadPath] + tail}
}

// TestManageAdminGuard: everyone may read Manage; only an admin writes it (403 forbidden
// otherwise, nothing stored), and a cross-site write is refused before the principal is asked.
func TestManageAdminGuard(t *testing.T) {
	t.Parallel()
	viewer := manageServer(t, func(*http.Request) Principal { return Principal{ID: "v", Name: "Viewer"} })
	for _, path := range []string{"/manage/items", "/manage/items/prompt.lead", "/manage/items/prompt.lead/history", "/manage/items/prompt.lead/diff", "/manage/changes", "/manage/export", "/me"} {
		if code, body := viewer.do("GET", path, nil); code != 200 {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
	files := leadFiles(t, viewer, "\nBe brief.")
	for _, w := range []struct {
		path string
		body any
	}{{"/manage/items/prompt.lead/versions", m{"base": 0, "files": files, "note": ""}}, {"/manage/items/prompt.lead/revert", m{"to": 0, "base": 0, "note": ""}},
		{"/manage/items/skill.deadlines/delete", m{"base": 0, "note": ""}}} {
		code, body := viewer.do("POST", w.path, w.body)
		if code != 403 || !strings.Contains(body, `"errorType":"forbidden"`) {
			t.Errorf("POST %s as a viewer: %d %s", w.path, code, body)
		}
	}
	if _, body := viewer.do("GET", "/manage/changes", nil); body != "[]\n" {
		t.Fatalf("a refused write was stored: %s", body)
	}
	if _, body := viewer.do("GET", "/me", nil); !strings.Contains(body, `"admin":false`) {
		t.Fatalf("/me: %s", body)
	}

	s := manageServer(t, local)
	if _, body := s.do("GET", "/me", nil); !strings.Contains(body, `"id":"local"`) || !strings.Contains(body, `"admin":true`) || !strings.Contains(body, `"authEnabled":false`) || !strings.Contains(body, `"model":false`) {
		t.Fatalf("with auth off the local user is an admin: %s", body)
	}
	req, _ := http.NewRequest("POST", s.URL+"/manage/items/prompt.lead/versions", strings.NewReader(`{"base":0,"files":{},"note":""}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("a cross-site write: %d", resp.StatusCode)
	}
	code, body := s.do("POST", "/manage/items/prompt.lead/versions", m{"base": 0, "files": files, "note": "brief"})
	if code != 201 || !strings.Contains(body, `"author":"Local user"`) || !strings.Contains(body, `"version":1`) {
		t.Fatalf("an admin's save: %d %s", code, body)
	}
	if code, body := s.do("POST", "/manage/items/prompt.lead/versions", m{"base": 0, "files": files, "note": ""}); code != 409 {
		t.Fatalf("a stale base: %d %s", code, body)
	}
	if code, body := s.do("POST", "/manage/items/prompt.lead/versions", m{"base": 1, "files": files, "extra": 1}); code != 400 {
		t.Fatalf("an unknown body field: %d %s", code, body)
	}
}

// TestManageRoutes: a refused save answers 422 with every problem by file; history, diff, revert,
// the change log and both export formats.
func TestManageRoutes(t *testing.T) {
	t.Parallel()
	s := manageServer(t, local)
	bad := map[string]string{"internal/engine/packs/jurisdiction/us.md": "---\ncode: uk\nstatus: candidate\nreviewedBy:\n---\nBody."}
	code, body := s.do("POST", "/manage/items/pack.jurisdiction.us/versions", m{"base": 0, "files": bad, "note": ""})
	var inv struct {
		ErrorType string
		Problems  []engine.Problem
	}
	if json.Unmarshal([]byte(body), &inv); code != 422 || inv.ErrorType != "invalid" || len(inv.Problems) == 0 || inv.Problems[0].File != "internal/engine/packs/jurisdiction/us.md" {
		t.Fatalf("a refused save: %d %s", code, body)
	}
	if code, body := s.do("POST", "/manage/items/prompt.lead/versions", m{"base": 0, "files": leadFiles(t, s, "\nOne."), "note": ""}); code != 201 {
		t.Fatalf("save: %d %s", code, body)
	}
	if code, body := s.do("GET", "/manage/items/prompt.lead/diff?from=builtin&to=1", nil); code != 200 || !strings.Contains(body, `+One.`) {
		t.Fatalf("diff: %d %s", code, body)
	}
	if code, _ := s.do("GET", "/manage/items/prompt.lead/diff?from=x", nil); code != 400 {
		t.Fatalf("a bad diff version: %d", code)
	}
	if code, body := s.do("GET", "/manage/items/prompt.lead/history", nil); code != 200 || !strings.Contains(body, `"action":"save"`) {
		t.Fatalf("history: %d %s", code, body)
	}
	resp, err := http.Get(s.URL + "/manage/export")
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Disposition") == "" || !strings.Contains(string(patch), "+++ b/"+leadPath) || !strings.Contains(string(patch), "+One.") {
		t.Fatalf("patch export: %s", patch)
	}
	resp, err = http.Get(s.URL + "/manage/export?format=zip")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(z.File) != 1 || z.File[0].Name != leadPath {
		t.Fatalf("zip export: %v", err)
	}
	if code, body := s.do("POST", "/manage/items/prompt.lead/revert", m{"to": 0, "base": 1, "note": "undo"}); code != 201 || !strings.Contains(body, `"builtin":true`) {
		t.Fatalf("revert: %d %s", code, body)
	}
	if code, body := s.do("GET", "/manage/changes", nil); code != 200 || strings.Count(body, `"item":"prompt.lead"`) != 2 {
		t.Fatalf("change log: %d %s", code, body)
	}
	if code, _ := s.do("GET", "/manage/items/nope.x", nil); code != 404 {
		t.Fatalf("unknown item: %d", code)
	}
	if code, _ := s.do("GET", "/manage/export?format=tar", nil); code != 400 {
		t.Fatalf("unknown format: %d", code)
	}
}

// TestManageSecretsNeverShown: a secret is set by an admin only, listed by name only (never its
// value, in any route), and its change is in the change log; Discover, Import and Test are an
// admin's (they connect out, or spend).
func TestManageSecretsNeverShown(t *testing.T) {
	t.Parallel()
	const value = "sk-test-NEVER-SHOWN-1234"
	viewer := manageServer(t, func(*http.Request) Principal { return Principal{ID: "v", Name: "Viewer"} })
	for _, w := range []struct {
		path string
		body any
	}{{"/manage/secrets", m{"name": "k1", "value": value}}, {"/manage/secrets/k1/delete", nil}, {"/manage/items/server.x/discover", nil},
		{"/manage/items/server.x/import", m{"tools": nil, "offerToLead": false}}, {"/manage/items/tool.numbers/test", m{"documentIds": []string{"d"}, "input": m{}}}} {
		if code, body := viewer.do("POST", w.path, w.body); code != 403 {
			t.Errorf("POST %s as a viewer: %d %s", w.path, code, body)
		}
	}
	s := manageServer(t, local)
	if code, body := s.do("POST", "/manage/secrets", m{"name": "k1", "value": value}); code != 422 || !strings.Contains(body, "origin") {
		t.Fatalf("a secret with no origin: %d %s", code, body)
	}
	code, body := s.do("POST", "/manage/secrets", m{"name": "k1", "value": value, "origin": "https://api.example.com"})
	if code != 201 || strings.Contains(body, value) {
		t.Fatalf("set: %d %s", code, body)
	}
	if code, body := s.do("POST", "/manage/secrets", m{"name": "Bad Name", "value": "x"}); code != 422 || strings.Contains(body, "x\"") {
		t.Fatalf("a bad name: %d %s", code, body)
	}
	for _, path := range []string{"/manage/secrets", "/manage/changes", "/manage/items", "/manage/export"} {
		code, body := s.do("GET", path, nil)
		if code != 200 || strings.Contains(body, value) {
			t.Errorf("GET %s: %d, value shown: %v", path, code, strings.Contains(body, value))
		}
	}
	if _, body := s.do("GET", "/manage/secrets", nil); !strings.Contains(body, `"name":"k1"`) || !strings.Contains(body, `"updatedBy":"Local user"`) || !strings.Contains(body, `"origin":"https://api.example.com:443"`) {
		t.Fatalf("list: %s", body)
	}
	if _, body := s.do("GET", "/manage/changes", nil); !strings.Contains(body, `"secret set"`) {
		t.Fatalf("change log: %s", body)
	}
	if code, _ := s.do("POST", "/manage/secrets/k1/delete", nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := s.do("POST", "/manage/secrets/k1/delete", nil); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}
	// a server saved at a metadata address is refused at save (one text whatever the reason: no oracle)
	srv := `{"name":"x","url":"https://169.254.169.254/mcp","enabled":true}`
	if code, body := s.do("POST", "/manage/items/server.x/versions", m{"base": 0, "files": m{"internal/engine/specs/servers/x.json": srv}, "note": ""}); code != 422 || !strings.Contains(body, "not one BigLaw may reach") {
		t.Fatalf("a metadata-address server: %d %s", code, body)
	}
}

// TestManageAdminModes (review M3): a read-only server (--allow-no-auth) refuses every write and
// says how to open it; an open one (--admin-without-auth) says so on /me for the UI's banner.
func TestManageAdminModes(t *testing.T) {
	t.Parallel()
	ro := manageServer(t, LocalUser(false, false))
	if _, body := ro.do("GET", "/me", nil); !strings.Contains(body, `"admin":false`) || !strings.Contains(body, `"adminWithoutAuth":false`) {
		t.Fatalf("read-only /me: %s", body)
	}
	code, body := ro.do("POST", "/manage/items/prompt.lead/versions", m{"base": 0, "files": leadFiles(t, ro, "\nx"), "note": ""})
	if code != 403 || !strings.Contains(body, "--admin-without-auth") {
		t.Fatalf("a read-only server's write: %d %s", code, body)
	}
	open := manageServer(t, LocalUser(true, true))
	if _, body := open.do("GET", "/me", nil); !strings.Contains(body, `"admin":true`) || !strings.Contains(body, `"adminWithoutAuth":true`) {
		t.Fatalf("open /me: %s", body)
	}
	if code, body := open.do("POST", "/manage/items/prompt.lead/versions", m{"base": 0, "files": leadFiles(t, open, "\nx"), "note": ""}); code != 201 {
		t.Fatalf("an open server's write: %d %s", code, body)
	}
}
