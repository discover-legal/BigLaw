// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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

// TestEveryRouteNeedsASession (review r1, its route probe kept as a regression test): every route,
// method and path spelling sent raw without a session is 401, or a 404/405/400 or redirect that serves
// nothing; only the UI's files answer 200, and /auth/ is public for its three routes alone.
func TestEveryRouteNeedsASession(t *testing.T) {
	t.Parallel()
	s, ids := signedServer(t, nil, doc1)
	d := ids[0]
	routes := []string{
		"GET /clients", "POST /tasks", "GET /tasks", "GET /tasks/x", "GET /tasks/x/steps", "GET /tasks/x/cost", "GET /tasks/x/deliverable.md",
		"POST /tasks/x/cancel", "POST /tasks/x/gates/g/approve", "POST /tasks/x/gates/g/reject", "GET /tasks/x/events",
		"GET /me", "GET /manage/items", "GET /manage/items/prompt.lead", "GET /manage/items/prompt.lead/history", "GET /manage/items/prompt.lead/diff",
		"POST /manage/items/prompt.lead/versions", "POST /manage/items/prompt.lead/revert", "POST /manage/items/prompt.lead/delete",
		"GET /manage/changes", "GET /manage/export", "GET /manage/export?format=zip", "GET /manage/secrets", "POST /manage/secrets",
		"POST /manage/secrets/k/delete", "POST /manage/items/server.x/discover", "POST /manage/items/server.x/import", "POST /manage/items/tool.x/test",
		"POST /manage/sessions/revoke", "POST /documents", "GET /documents", "GET /documents/" + d, "GET /documents/" + d + "/outline",
		"GET /documents/" + d + "/sections/s1", "GET /documents/search?q=x", "DELETE /documents/" + d, "POST /documents/upload", "GET /documents/" + d + "/file",
		"HEAD /tasks", "HEAD /documents/" + d + "/file", "PUT /tasks", "PATCH /documents/" + d, "OPTIONS /tasks", "get /tasks",
		"GET /x/../tasks", "GET /./documents", "GET //tasks", "GET /tasks/x/../../documents", "GET /documents/" + d + "%2Ffile",
		"GET /Tasks", "GET /manage/secrets/", "GET /auth/../manage/secrets", "GET /auth/%2e%2e/manage/secrets", "GET /auth/other",
	}
	for _, rt := range routes {
		method, path, _ := strings.Cut(rt, " ")
		code, loc, body := rawRequest(t, s.URL, method, path)
		ui := code == 200 && method == "GET" && strings.Contains(strings.ToLower(body), "<!doctype html")
		redirect := (code == 301 || code == 307 || code == 308) && !strings.HasPrefix(loc, "//")
		if strings.Contains(body, d) || !(code == 401 || code == 404 || code == 405 || code == 400 || redirect || ui) {
			t.Errorf("%s without a session: %d %.90s", rt, code, body)
		}
	}
	for _, p := range []string{"GET /auth/other", "POST /auth/login", "GET /auth/", "GET /healthz"} { // exact patterns only
		if public(p) {
			t.Errorf("%s is public", p)
		}
	}
}

// rawRequest sends the request line exactly as written (no client-side cleaning).
func rawRequest(t *testing.T, base, method, path string) (int, string, string) {
	t.Helper()
	u, _ := url.Parse(base)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 2\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n{}", method, path, u.Host)
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0, "", ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// TestRevokeSessions (review r1 L-3): an admin ends a user's sessions by sub or email (a member may
// not), the user is signed out at once, and an open event stream of theirs ends too.
func TestRevokeSessions(t *testing.T) {
	t.Parallel()
	s, ids := signedServer(t, memo, doc1)
	member, admin := s.browser(t, bob), s.browser(t, ada)
	if code, body := member.do("POST", "/manage/sessions/revoke", m{"user": "u-ada"}); code != 403 {
		t.Fatalf("a member's revoke: %d %s", code, body)
	}
	if code, body := admin.do("POST", "/manage/sessions/revoke", m{}); code != 400 {
		t.Fatalf("a revoke naming no one: %d %s", code, body)
	}
	code, body := member.do("POST", "/tasks", m{"description": "What is claimed?", "workflowType": "memo", "documentIds": ids})
	var v engine.TaskView
	if json.Unmarshal([]byte(body), &v); code != 201 || v.SubmittedBy == nil || *v.SubmittedBy != (engine.Person{ID: "u-bob", Name: "Bob Member", Email: "bob@firm.example"}) {
		t.Fatalf("a member's task records its submitter: %d %s", code, body)
	}
	member.waitFor(v.ID, func(v engine.TaskView) bool { return v.Status == "awaiting_gate" })
	// an open stream on a task another process holds running (it would not end by itself)
	if _, err := s.st.DB().Exec(`UPDATE tasks SET status = 'running', lease_owner = 'elsewhere', lease_until = now() + interval '1 hour' WHERE id = $1`, v.ID); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", s.URL+"/tasks/"+v.ID+"/events", nil)
	resp, err := member.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ended := make(chan string, 1)
	go func() { b, _ := io.ReadAll(resp.Body); ended <- string(b) }()
	if code, body := admin.do("POST", "/manage/sessions/revoke", m{"user": "BOB@firm.example"}); code != 200 || !strings.Contains(body, `"revoked":1`) {
		t.Fatalf("revoke by email: %d %s", code, body)
	}
	if code, _ := member.do("GET", "/me", nil); code != 401 {
		t.Fatalf("a revoked member: %d", code)
	}
	select {
	case b := <-ended:
		if !strings.Contains(b, "event: signedOut") {
			t.Fatalf("the stream ended without saying why: %s", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the revoked member's stream is still open")
	}
	if code, _ := admin.do("GET", "/me", nil); code != 200 {
		t.Fatal("revoking bob signed the admin out")
	}
	if code, body := admin.do("POST", "/manage/sessions/revoke", m{"user": "u-ada"}); code != 200 || !strings.Contains(body, `"revoked":1`) {
		t.Fatalf("revoke by sub: %d %s", code, body)
	}
}

// TestMembersSeeAuthorsByName (review r1 Info): admins' emails in the change log, history and
// secrets' updatedBy are shown to admins only; a member sees the display name.
func TestMembersSeeAuthorsByName(t *testing.T) {
	t.Parallel()
	s, _ := signedServer(t, nil)
	member, admin := s.browser(t, bob), s.browser(t, ada)
	if code, body := admin.do("POST", "/manage/secrets", m{"name": "k1", "value": "a-long-value", "origin": "https://api.example.com"}); code != 201 {
		t.Fatalf("secret: %d %s", code, body)
	}
	for _, p := range []string{"/manage/secrets", "/manage/changes"} {
		if _, body := admin.do("GET", p, nil); !strings.Contains(body, `"Ada Lovelace \u003cada@firm.example\u003e"`) {
			t.Errorf("an admin's %s lacks the full author: %s", p, body)
		}
		if _, body := member.do("GET", p, nil); strings.Contains(body, "ada@firm.example") || !strings.Contains(body, "Ada Lovelace") {
			t.Errorf("a member's %s: %s", p, body)
		}
	}
}
