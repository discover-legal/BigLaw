// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/auth"
	"github.com/discover-legal/BigLaw/internal/auth/authtest"
	"github.com/discover-legal/BigLaw/internal/docs"
	"github.com/discover-legal/BigLaw/internal/engine"
	"github.com/discover-legal/BigLaw/internal/model"
	"github.com/discover-legal/BigLaw/internal/store"
)

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

// signed is the API behind sign-in (RequireSignIn, as serve mounts it) with the fake provider;
// ada@firm.example is an admin (BIGLAW_ADMINS), anyone else who signs in a member. Secrets and
// custom calls are on (RemotePolicy zero), as serve sets them under sign-in.
type signed struct {
	*server
	idp  *authtest.IdP
	logs *bytes.Buffer
}

func signedServer(t *testing.T, scripts func([]string) map[string][]model.Reply, texts ...string) (*signed, []string) {
	t.Helper()
	idp, logs := authtest.New(t), &bytes.Buffer{}
	s, ids := newServerWith(t, func(url string, st *store.Store, d *docs.Service, e *engine.Engine) http.Handler {
		log := slog.New(slog.NewTextHandler(logs, nil))
		svc, err := auth.New(st.DB(), auth.Config{Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.Secret, PublicURL: url, Admins: []string{"ada@firm.example"}, Log: log})
		if err != nil {
			t.Fatal(err)
		}
		return RequireSignIn(HandlerFor(d, e, nil, log, SessionUser), svc, log)
	}, scripts, roles, texts...)
	return &signed{s, idp, logs}, ids
}

// browser signs claims' person in and returns the server as their browser.
func (s *signed) browser(t *testing.T, claims map[string]any) *server {
	t.Helper()
	s.idp.User = claims
	c := authtest.Browser(t)
	if resp := s.idp.SignIn(t, c, s.URL); resp.StatusCode != 303 {
		t.Fatalf("sign-in: %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	return s.as(c)
}

var (
	ada = map[string]any{"sub": "u-ada", "email": "ada@firm.example", "email_verified": true, "name": "Ada Lovelace"}
	bob = map[string]any{"sub": "u-bob", "email": "bob@firm.example", "email_verified": true, "name": "Bob Member"}
	// eve claims the admin's address, unverified: a member
	eve = map[string]any{"sub": "u-eve", "email": "ada@firm.example", "email_verified": false, "name": "Eve"}
)

// TestSignInGuardsTheAPI: with sign-in every API route is 401 unauthenticated without a session
// (reads and writes, documents, tasks, gates, Manage, /me), the UI's files and sign-in itself are
// not; a session opens them; a cross-site write is 403 cross_origin even with the session's cookie
// (DELETE included); logout ends it for every copy of the cookie.
func TestSignInGuardsTheAPI(t *testing.T) {
	t.Parallel()
	s, ids := signedServer(t, nil, doc1)
	anon := s.as(authtest.Browser(t))
	for _, r := range []struct{ method, path string }{
		{"GET", "/me"}, {"GET", "/tasks"}, {"GET", "/documents"}, {"GET", "/documents/" + ids[0]}, {"GET", "/documents/" + ids[0] + "/file"}, {"GET", "/clients"},
		{"GET", "/manage/items"}, {"GET", "/manage/secrets"}, {"GET", "/manage/export"}, {"POST", "/tasks"}, {"POST", "/documents"}, {"DELETE", "/documents/" + ids[0]},
		{"POST", "/tasks/x/gates/g/approve"}, {"POST", "/manage/items/prompt.lead/versions"}, {"GET", "/tasks/x/events"},
	} {
		if code, body := anon.do(r.method, r.path, nil); code != 401 || !strings.Contains(body, `"errorType":"unauthenticated"`) {
			t.Errorf("%s %s without a session: %d %s", r.method, r.path, code, body)
		}
	}
	if code, body := anon.do("GET", "/", nil); code != 200 {
		t.Errorf("the UI without a session: %d %s", code, body)
	}
	if code, _ := anon.do("GET", "/auth/login", nil); code != 302 {
		t.Errorf("/auth/login: %d", code)
	}
	a := s.browser(t, ada)
	if code, body := a.do("GET", "/documents/"+ids[0], nil); code != 200 {
		t.Fatalf("signed in: %d %s", code, body)
	}
	for _, h := range [][]string{{"Origin", "https://evil.example"}, {"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"}} {
		for _, r := range []struct{ method, path string }{{"POST", "/tasks"}, {"DELETE", "/documents/" + ids[0]}, {"POST", "/manage/secrets"}, {"POST", "/auth/logout"}, {"POST", "/tasks/x/cancel"}} {
			req, _ := http.NewRequest(r.method, s.URL+r.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "text/plain")
			req.Header.Set(h[0], h[1])
			resp, err := a.client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 403 || !strings.Contains(string(b), `"errorType":"cross_origin"`) {
				t.Errorf("%v %s %s with the cookie: %d %s", h, r.method, r.path, resp.StatusCode, b)
			}
		}
	}
	if code, _ := a.do("GET", "/documents/"+ids[0], nil); code != 200 {
		t.Fatal("a cross-site write deleted the document or ended the session")
	}
	stolen := s.as(authtest.Browser(t))
	stolen.c.Jar.SetCookies(mustURL(s.URL), a.c.Jar.Cookies(mustURL(s.URL)))
	if code, _ := stolen.do("GET", "/me", nil); code != 200 {
		t.Fatal("a copied cookie is the session until logout")
	}
	if code, _ := a.do("POST", "/auth/logout", nil); code != 200 {
		t.Fatal("logout")
	}
	for _, b := range []*server{a, stolen} {
		if code, _ := b.do("GET", "/me", nil); code != 401 {
			t.Errorf("after logout: %d", code)
		}
	}
}

// TestSignedInMe: /me is the session's user; admin only for BIGLAW_ADMINS' verified address,
// never for an unverified claim of it (whose address is not shown).
func TestSignedInMe(t *testing.T) {
	t.Parallel()
	s, _ := signedServer(t, nil)
	for _, c := range []struct {
		who  map[string]any
		want string
	}{
		{ada, `{"admin":true,"adminWithoutAuth":false,"authEnabled":true,"email":"ada@firm.example","id":"u-ada","name":"Ada Lovelace"}`},
		{bob, `{"admin":false,"adminWithoutAuth":false,"authEnabled":true,"email":"bob@firm.example","id":"u-bob","name":"Bob Member"}`},
		{eve, `{"admin":false,"adminWithoutAuth":false,"authEnabled":true,"email":"","id":"u-eve","name":"Eve"}`},
	} {
		if _, body := s.browser(t, c.who).do("GET", "/me", nil); strings.TrimSpace(body) != c.want {
			t.Errorf("/me: %s, want %s", body, c.want)
		}
	}
}

// TestSignedInManageRoles: TestManageWriteRoutes with real sessions: a member is refused every
// Manage write 403 forbidden (saying they are a member), the admin's cross-site writes are refused
// with the cookie, the admin's writes go through and the change log names the admin.
func TestSignedInManageRoles(t *testing.T) {
	t.Parallel()
	s, _ := signedServer(t, nil)
	member, admin := s.browser(t, bob), s.browser(t, ada)
	vs := manageWrites(t, member, admin)
	for _, v := range vs {
		if v.Author != "Ada Lovelace <ada@firm.example>" {
			t.Errorf("change log author %q, want the signed-in admin", v.Author)
		}
	}
	if _, body := member.do("POST", "/manage/secrets", m{"name": "k2", "value": "v", "origin": "https://api.example.com"}); !strings.Contains(body, "signed in as a member") {
		t.Errorf("a member's refusal: %s", body)
	}
	if _, body := admin.do("GET", "/manage/secrets", nil); strings.Contains(body, "a-long-value") {
		t.Fatal("a secret's value was shown")
	}
}

// TestSignedInRemotePolicy: under sign-in secrets and custom calls are on for a real admin (no
// RemotePolicy.Off refusal) and refused to members by the admin guard.
func TestSignedInRemotePolicy(t *testing.T) {
	t.Parallel()
	s, _ := signedServer(t, nil)
	member, admin := s.browser(t, bob), s.browser(t, ada)
	if code, body := admin.do("POST", "/manage/secrets", m{"name": "k1", "value": "a-long-value", "origin": "https://api.example.com"}); code != 201 {
		t.Fatalf("an admin's secret: %d %s", code, body)
	}
	for _, p := range []string{"/manage/secrets", "/manage/items/server.x/discover", "/manage/items/tool.x/test", "/manage/items/server.x/import"} {
		if code, body := member.do("POST", p, m{}); code != 403 || !strings.Contains(body, `"errorType":"forbidden"`) {
			t.Errorf("a member's %s: %d %s", p, code, body)
		}
	}
	if code, body := admin.do("POST", "/manage/items/server.x/discover", nil); code == 403 || strings.Contains(body, "need a real admin") {
		t.Fatalf("an admin's Discover was refused as no real admin: %d %s", code, body)
	}
}

// TestGateDecisionRecordsUser: a member (lawyer review) rejects the deliverable's gate and an
// admin approves the next; each decision records who decided (sub, name, email) in the task.
func TestGateDecisionRecordsUser(t *testing.T) {
	t.Parallel()
	s, ids := signedServer(t, memo, doc1)
	member, admin := s.browser(t, bob), s.browser(t, ada)
	code, body := member.do("POST", "/tasks", m{"description": "What is claimed?", "workflowType": "memo", "documentIds": ids})
	var v engine.TaskView
	if json.Unmarshal([]byte(body), &v); code != 201 {
		t.Fatalf("a member's task: %d %s", code, body)
	}
	v = member.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" })
	if code, body := member.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/reject", m{"reason": "Cite the invoice dates."}); code != 200 {
		t.Fatalf("a member's rejection: %d %s", code, body)
	}
	v = member.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" && len(v.DecidedGates) == 1 })
	if code, body := admin.do("POST", "/tasks/"+v.ID+"/gates/"+v.PendingGates[0].GateID+"/approve", nil); code != 200 {
		t.Fatalf("an admin's approval: %d %s", code, body)
	}
	_, body = member.do("GET", "/tasks/"+v.ID, nil)
	json.Unmarshal([]byte(body), &v)
	if len(v.DecidedGates) != 2 {
		t.Fatalf("decided gates: %s", body)
	}
	for i, want := range []engine.Person{{ID: "u-bob", Name: "Bob Member", Email: "bob@firm.example"}, {ID: "u-ada", Name: "Ada Lovelace", Email: "ada@firm.example"}} {
		if g := v.DecidedGates[i]; g.DecidedBy == nil || *g.DecidedBy != want {
			t.Errorf("gate %d decided by %+v, want %+v", i, g.DecidedBy, want)
		}
	}
	if strings.Contains(body, "Local user") {
		t.Errorf("a signed-in decision names the local user: %s", body)
	}
}
