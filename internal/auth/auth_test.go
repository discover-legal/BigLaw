// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/discover-legal/BigLaw/internal/auth/authtest"
	"github.com/discover-legal/BigLaw/internal/store"
	"github.com/discover-legal/BigLaw/internal/store/storetest"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// rig is sign-in over a fresh schema, served at srv with /whoami answering the session's user.
type rig struct {
	idp  *authtest.IdP
	s    *Service
	srv  *httptest.Server
	db   *sql.DB
	clk  *clock
	logs *syncBuf
	seen []string // every body BigLaw answered (secrets must appear in none)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newRig(t *testing.T, admins ...string) *rig {
	t.Helper()
	st := storetest.Open(t, store.OpenPool)
	r := &rig{idp: authtest.New(t), db: st.DB(), clk: &clock{t: time.Now()}, logs: &syncBuf{}}
	r.idp.Now = r.clk.now
	r.srv = httptest.NewServer(nil)
	t.Cleanup(r.srv.Close)
	var err error
	r.s, err = New(r.db, Config{Issuer: r.idp.URL, ClientID: r.idp.ClientID, ClientSecret: r.idp.Secret, PublicURL: r.srv.URL, Admins: admins,
		Now: r.clk.now, Log: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	r.s.Routes(mux)
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, req *http.Request) {
		u, ok, err := r.s.User(req)
		if err != nil {
			w.WriteHeader(503)
			return
		}
		if !ok {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(u)
	})
	mux.HandleFunc("GET /alive", func(w http.ResponseWriter, req *http.Request) {
		if ok, _ := r.s.Alive(req); !ok {
			w.WriteHeader(401)
		}
	})
	r.srv.Config.Handler = mux
	return r
}

// whoami is the browser's user (ok false: 401).
func (r *rig) whoami(t *testing.T, c *http.Client) (User, bool) {
	t.Helper()
	resp, err := c.Get(r.srv.URL + "/whoami")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var u User
	json.NewDecoder(resp.Body).Decode(&u)
	return u, resp.StatusCode == 200
}

// signIn runs a whole sign-in and returns the callback's status and body.
func (r *rig) signIn(t *testing.T, c *http.Client) (int, string) {
	t.Helper()
	resp := r.idp.SignIn(t, c, r.srv.URL)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r.seen = append(r.seen, string(b))
	return resp.StatusCode, string(b)
}

func (r *rig) sessions(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sessionCookie(t *testing.T, c *http.Client, base string) *http.Cookie {
	t.Helper()
	u, _ := url.Parse(base)
	for _, k := range c.Jar.Cookies(u) {
		if strings.HasSuffix(k.Name, "biglaw_session") {
			return k
		}
	}
	return nil
}

// TestSignInHappyPath: the code flow with PKCE S256 and the client's basic credentials signs the
// user in; the session cookie is opaque, HttpOnly, SameSite=Lax and Path=/ (not Secure on loopback
// http); the redirect goes to next; admin by a verified email; ES256 tokens verify too.
func TestSignInHappyPath(t *testing.T) {
	t.Parallel()
	r := newRig(t, "ADA@firm.example", "u-root")
	c := authtest.Browser(t)
	resp, err := c.Get(r.srv.URL + "/auth/login?next=/%23/task/T1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	q := loc.Query()
	if resp.StatusCode != 302 || !strings.HasPrefix(loc.String(), r.idp.URL+"/authorize?") || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != r.srv.URL+"/auth/callback" ||
		len(q.Get("state")) != 43 || len(q.Get("nonce")) != 43 || q.Get("scope") != "openid email profile" {
		t.Fatalf("login redirect: %d %s", resp.StatusCode, loc)
	}
	var setCookie string
	for _, h := range resp.Header.Values("Set-Cookie") {
		setCookie += h
	}
	if !strings.Contains(setCookie, "biglaw_login=") || !strings.Contains(setCookie, "HttpOnly") || !strings.Contains(setCookie, "SameSite=Lax") || strings.Contains(setCookie, "Secure") {
		t.Fatalf("login cookie: %s", setCookie)
	}
	resp, _ = c.Get(loc.String())
	resp.Body.Close()
	resp, _ = c.Get(resp.Header.Get("Location"))
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/#/task/T1" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	sc := strings.Join(resp.Header.Values("Set-Cookie"), "\n")
	if !strings.Contains(sc, "biglaw_session=") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Lax") || !strings.Contains(sc, "Path=/") {
		t.Fatalf("session cookie: %s", sc)
	}
	form := r.idp.Tokens[0]
	if form.Get("code_verifier") == "" || form.Get("client_secret") != "" || !strings.HasPrefix(r.idp.TokenAuth[0], "Basic ") {
		t.Fatalf("token request: %v %s", form, r.idp.TokenAuth[0])
	}
	u, ok := r.whoami(t, c)
	if !ok || u.Sub != "u-ada" || u.Email != "ada@firm.example" || !u.Verified || u.Name != "Ada Lovelace" || !u.Admin {
		t.Fatalf("whoami: %v %+v", ok, u)
	}
	if k := sessionCookie(t, c, r.srv.URL); k == nil || len(k.Value) != 43 || strings.Contains(k.Value, "ada") {
		t.Fatalf("the session cookie is not an opaque random id: %+v", k)
	}
	// ES256, a member (no admin entry matches), a name from preferred_username
	r.idp.Alg = "ES256"
	r.idp.User = map[string]any{"sub": "u-bob", "email": "bob@firm.example", "email_verified": true, "preferred_username": "bob"}
	b := authtest.Browser(t)
	if code, body := r.signIn(t, b); code != 303 {
		t.Fatalf("ES256 sign-in: %d %s", code, body)
	}
	if u, ok := r.whoami(t, b); !ok || u.Admin || u.Name != "bob" || !u.Verified {
		t.Fatalf("ES256 member: %v %+v", ok, u)
	}
	// an admin named by sub
	r.idp.User = map[string]any{"sub": "u-root"}
	if code, _ := r.signIn(t, authtest.Browser(t)); code != 303 {
		t.Fatal("sub admin sign-in")
	}
}

// TestIsAdmin: an email entry matches only an address the issuer verified, case-insensitively; any
// other entry matches a sub exactly.
func TestIsAdmin(t *testing.T) {
	t.Parallel()
	admins := []string{"Ada@Firm.example", "u-root"}
	for _, c := range []struct {
		sub, email string
		verified   bool
		want       bool
	}{{"x", "ada@firm.example", true, true}, {"x", "ada@firm.example", false, false}, {"u-root", "", false, true}, {"U-ROOT", "", false, false},
		{"ada@firm.example", "", false, false}, {"x", "", true, false}, {"x", "eve@firm.example", true, false}} {
		if got := IsAdmin(admins, c.sub, c.email, c.verified); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// TestTokenRefusals: every ID token that should not sign anyone in is refused (401, no session):
// a bad signature, the wrong iss or aud, an azp of another client, several audiences without azp,
// expired, issued in the future, a nonce that is not the sign-in's, no sub, alg none, HS256 keyed
// with the RSA public key (key confusion), an EC key named for RS256, and a kid the JWKS lacks.
func TestTokenRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	other := authtest.New(t) // a key the issuer never published
	edit := func(k string, v any) func(map[string]any) { return func(c map[string]any) { c[k] = v } }
	for _, c := range []struct {
		name string
		edit func(map[string]any)
		raw  func(i *authtest.IdP, nonce string) string
		want string
	}{
		{name: "bad signature", raw: func(i *authtest.IdP, n string) string { return other.Sign("RS256", "rsa-1", i.Claims(n)) }, want: "signature does not verify"},
		{name: "tampered claims", raw: func(i *authtest.IdP, n string) string {
			p := strings.Split(i.IDToken(n), ".")
			c := i.Claims(n)
			c["sub"] = "u-root"
			return p[0] + "." + authtest.Segment(c) + "." + p[2]
		}, want: "signature does not verify"},
		{name: "wrong iss", edit: edit("iss", "https://idp.example"), want: "issuer"},
		{name: "wrong aud", edit: edit("aud", "another-client"), want: "audience"},
		{name: "azp of another client", edit: edit("azp", "another-client"), want: "azp"},
		{name: "several audiences, no azp", edit: edit("aud", []string{"biglaw-test", "another-client"}), want: "azp"},
		{name: "expired", edit: func(c map[string]any) { c["exp"] = time.Now().Add(-2 * time.Minute).Unix() }, want: "expired"},
		{name: "no exp", edit: func(c map[string]any) { delete(c, "exp") }, want: "expired"},
		{name: "issued in the future", edit: func(c map[string]any) { c["iat"] = time.Now().Add(5 * time.Minute).Unix() }, want: "future"},
		{name: "nonce mismatch", edit: edit("nonce", "not-the-nonce"), want: "nonce"},
		{name: "no nonce", edit: func(c map[string]any) { delete(c, "nonce") }, want: "nonce"},
		{name: "no sub", edit: edit("sub", ""), want: "subject"},
		{name: "alg none", raw: func(i *authtest.IdP, n string) string { return authtest.None(i.Claims(n)) }, want: "algorithm"},
		{name: "alg none, signature kept", raw: func(i *authtest.IdP, n string) string {
			p := strings.Split(i.IDToken(n), ".")
			return authtest.Segment(map[string]any{"alg": "none", "kid": "rsa-1"}) + "." + p[1] + "." + p[2]
		}, want: "algorithm"},
		{name: "HS256 with the RSA public key", raw: func(i *authtest.IdP, n string) string {
			return authtest.HS256(i.RSA.PublicKey.N.Bytes(), "rsa-1", i.Claims(n))
		}, want: "algorithm"},
		{name: "HS256 with the JWK's JSON", raw: func(i *authtest.IdP, n string) string {
			j, _ := json.Marshal(authtest.RSAJWK(&i.RSA.PublicKey, "rsa-1"))
			return authtest.HS256(j, "rsa-1", i.Claims(n))
		}, want: "algorithm"},
		{name: "EC key named for RS256", raw: func(i *authtest.IdP, n string) string { return i.Sign("RS256", "ec-1", i.Claims(n)) }, want: "key id"},
		{name: "RSA key named for ES256", raw: func(i *authtest.IdP, n string) string { return i.Sign("ES256", "rsa-1", i.Claims(n)) }, want: "key id"},
		{name: "unknown kid", raw: func(i *authtest.IdP, n string) string { return i.Sign("RS256", "nope", i.Claims(n)) }, want: "key id"},
		{name: "crit header", raw: func(i *authtest.IdP, n string) string {
			return authtest.Segment(map[string]any{"alg": "RS256", "kid": "rsa-1", "crit": []string{"exp"}}) + "." + authtest.Segment(i.Claims(n)) + ".AA"
		}, want: "critical"},
		{name: "not a JWT", raw: func(*authtest.IdP, string) string { return "abc" }, want: "not a signed JWT"},
	} {
		r.idp.Edit, r.idp.Raw = c.edit, nil
		if c.raw != nil {
			raw := c.raw
			r.idp.Raw = func(n string) string { return raw(r.idp, n) }
		}
		b := authtest.Browser(t)
		code, body := r.signIn(t, b)
		if code != 401 || !strings.Contains(body, c.want) || sessionCookie(t, b, r.srv.URL) != nil {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
		if _, ok := r.whoami(t, b); ok {
			t.Errorf("%s: signed in", c.name)
		}
	}
	if n := r.sessions(t); n != 0 {
		t.Fatalf("%d sessions after refused tokens", n)
	}
	r.idp.Edit, r.idp.Raw = nil, nil
	if code, body := r.signIn(t, authtest.Browser(t)); code != 303 { // the rig itself still signs in
		t.Fatalf("valid token after refusals: %d %s", code, body)
	}
}

// TestUnknownKidRefetch: a token naming a kid BigLaw has not seen (the issuer rotated its key)
// refetches the JWKS once and signs in; another unknown kid within a minute is refused without a
// fetch (rate limit), and after a minute it is fetched again.
func TestUnknownKidRefetch(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if code, _ := r.signIn(t, authtest.Browser(t)); code != 303 || r.idp.Fetches() != 1 {
		t.Fatalf("first sign-in: %d, %d fetches", code, r.idp.Fetches())
	}
	if code, _ := r.signIn(t, authtest.Browser(t)); code != 303 || r.idp.Fetches() != 1 {
		t.Fatalf("a known kid fetched again: %d fetches", r.idp.Fetches())
	}
	r.clk.add(2 * time.Minute)
	r.idp.Rotate(t, "rsa-2")
	if code, body := r.signIn(t, authtest.Browser(t)); code != 303 || r.idp.Fetches() != 2 {
		t.Fatalf("rotated key: %d %s, %d fetches", code, body, r.idp.Fetches())
	}
	r.idp.Kid = "rsa-3"
	if code, _ := r.signIn(t, authtest.Browser(t)); code != 401 || r.idp.Fetches() != 2 {
		t.Fatalf("an unknown kid within the minute: %d, %d fetches", code, r.idp.Fetches())
	}
	r.clk.add(2 * time.Minute)
	if code, _ := r.signIn(t, authtest.Browser(t)); code != 401 || r.idp.Fetches() != 3 {
		t.Fatalf("an unknown kid after the minute: %d, %d fetches", code, r.idp.Fetches())
	}
}

// loginCookie is c's sealed login cookie ("" when there is none).
func (r *rig) loginCookie(t *testing.T, c *http.Client) string {
	t.Helper()
	for _, k := range c.Jar.Cookies(mustURL(r.srv.URL)) {
		if k.Name == "biglaw_login" {
			return k.Value
		}
	}
	return ""
}

func (r *rig) setLogin(t *testing.T, c *http.Client, v string) {
	t.Helper()
	c.Jar.SetCookies(mustURL(r.srv.URL), []*http.Cookie{{Name: "biglaw_login", Value: v, Path: "/"}})
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

// callbackWith starts a sign-in in c and returns the provider's redirect back to BigLaw (unvisited).
func (r *rig) callbackWith(t *testing.T, c *http.Client) *url.URL {
	t.Helper()
	resp, err := c.Get(r.srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, _ = c.Get(resp.Header.Get("Location"))
	resp.Body.Close()
	u, _ := url.Parse(resp.Header.Get("Location"))
	return u
}

func (r *rig) get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r.seen = append(r.seen, string(b))
	return resp.StatusCode, string(b)
}

// TestCallbackRefusals: a state that is not the browser's (another browser's callback, a forged
// state, none), a replayed callback, a sign-in older than 10 minutes, a sign-in whose PKCE verifier
// is missing or is not the one the challenge was made from, an error from the issuer, and no code.
func TestCallbackRefusals(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a, b := authtest.Browser(t), authtest.Browser(t)
	cb := r.callbackWith(t, a)
	if code, body := r.get(t, b, cb.String()); code != 400 || !strings.Contains(body, "state mismatch") { // login CSRF: a's sign-in in b
		t.Fatalf("another browser's callback: %d %s", code, body)
	}
	forged := *cb
	q := forged.Query()
	q.Set("state", "forged-state-forged-state-forged-state-xyz")
	forged.RawQuery = q.Encode()
	if code, body := r.get(t, a, forged.String()); code != 400 || !strings.Contains(body, "state mismatch") {
		t.Fatalf("forged state: %d %s", code, body)
	}
	cb = r.callbackWith(t, a)
	sealed := r.loginCookie(t, a)
	if code, _ := r.get(t, a, cb.String()); code != 303 {
		t.Fatalf("valid callback: %d", code)
	}
	if r.loginCookie(t, a) != "" {
		t.Fatal("the login cookie outlived its callback")
	}
	r.setLogin(t, a, sealed) // replay: the same callback with its cookie put back fails at the issuer (code used, PKCE)
	if code, body := r.get(t, a, cb.String()); code != 401 || !strings.Contains(body, "refused the code (400 invalid_grant)") {
		t.Fatalf("replayed callback: %d %s", code, body)
	}
	for _, c := range []struct {
		name string
		edit func(*pending)
		want string
		code int
	}{
		{"missing verifier", func(p *pending) { p.Verifier = "" }, "expired", 400},
		{"wrong verifier", func(p *pending) { p.Verifier = "x" + p.Verifier }, "refused the code (400 invalid_grant)", 401},
		{"old sign-in", func(p *pending) { p.At -= 11 * 60 }, "expired", 400},
		{"from the future", func(p *pending) { p.At += 5 * 60 }, "expired", 400},
	} {
		c1 := authtest.Browser(t)
		cb := r.callbackWith(t, c1)
		p, ok := r.s.open(r.loginCookie(t, c1))
		if !ok {
			t.Fatal("the login cookie does not open")
		}
		c.edit(&p)
		r.setLogin(t, c1, r.s.seal(p))
		if code, body := r.get(t, c1, cb.String()); code != c.code || !strings.Contains(body, c.want) || sessionCookie(t, c1, r.srv.URL) != nil {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
	for name, v := range map[string]func(string) string{ // a cookie this process did not seal is refused
		"tampered": func(v string) string { b := []byte(v); b[len(b)/2] ^= 1; return string(b) },
		"restarted": func(string) string {
			other, _ := New(nil, Config{Issuer: r.idp.URL, ClientID: "c", PublicURL: r.srv.URL})
			return other.seal(pending{State: "s"})
		},
		"plain state": func(v string) string { return cb.Query().Get("state") },
	} {
		c1 := authtest.Browser(t)
		cb := r.callbackWith(t, c1)
		r.setLogin(t, c1, v(r.loginCookie(t, c1)))
		if code, body := r.get(t, c1, cb.String()); code != 400 || !strings.Contains(body, "state mismatch") {
			t.Errorf("%s login cookie: %d %s", name, code, body)
		}
	}
	tokens := len(r.idp.Tokens)
	for _, c := range []struct{ name, query, want string }{{"issuer error", "error=access_denied&error_description=<script>", "access_denied"}, {"no code", "", "no code"}} {
		c1 := authtest.Browser(t)
		cb := r.callbackWith(t, c1)
		cb.RawQuery = "state=" + url.QueryEscape(cb.Query().Get("state")) + "&" + c.query
		if code, body := r.get(t, c1, cb.String()); code < 400 || !strings.Contains(body, c.want) || strings.Contains(body, "<script>") {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
	if len(r.idp.Tokens) != tokens {
		t.Errorf("a refused callback reached the token endpoint")
	}
	if n := r.sessions(t); n != 1 {
		t.Fatalf("%d sessions, want the one valid sign-in", n)
	}
}

// TestSessionLifetimes: a session ends after the idle timeout without a request, and at its
// absolute age even when used throughout; an ended session's row is deleted.
func TestSessionLifetimes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.s.SetLifetimes(time.Hour, 3*time.Hour)
	c := authtest.Browser(t)
	r.signIn(t, c)
	for range 3 { // used every 50 minutes: idle never ends it (150 minutes in)
		r.clk.add(50 * time.Minute)
		if _, ok := r.whoami(t, c); !ok || r.sessions(t) != 1 {
			t.Fatalf("ended early at %s", r.clk.now())
		}
	}
	r.clk.add(50 * time.Minute) // 200 minutes: past its absolute age of 3 hours
	if _, ok := r.whoami(t, c); ok || r.sessions(t) != 0 {
		t.Fatalf("past its absolute age: signed in %v, %d rows", ok, r.sessions(t))
	}
	r.signIn(t, c)
	r.clk.add(61 * time.Minute)
	if _, ok := r.whoami(t, c); ok || r.sessions(t) != 0 {
		t.Fatalf("idle for over an hour: signed in %v, %d rows", ok, r.sessions(t))
	}
}

// TestLogoutRevokesAndLoginRotates: logout deletes the session, so its cookie replayed from
// anywhere is no session; a sign-in with a session cookie present replaces it with a new id.
func TestLogoutRevokesAndLoginRotates(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := authtest.Browser(t)
	r.signIn(t, c)
	first := sessionCookie(t, c, r.srv.URL).Value
	r.signIn(t, c)
	second := sessionCookie(t, c, r.srv.URL).Value
	if first == second || r.sessions(t) != 1 {
		t.Fatalf("sign-in did not rotate the session: same id %v, %d rows", first == second, r.sessions(t))
	}
	thief := authtest.Browser(t)
	u, _ := url.Parse(r.srv.URL)
	thief.Jar.SetCookies(u, []*http.Cookie{{Name: "biglaw_session", Value: second, Path: "/"}})
	if _, ok := r.whoami(t, thief); !ok {
		t.Fatal("a copied cookie is the session until logout")
	}
	resp, err := c.Post(r.srv.URL+"/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || sessionCookie(t, c, r.srv.URL) != nil || r.sessions(t) != 0 {
		t.Fatalf("logout: %d, rows %d", resp.StatusCode, r.sessions(t))
	}
	if _, ok := r.whoami(t, thief); ok {
		t.Fatal("a revoked session's cookie still signs in")
	}
}

// TestNextStaysOnThisServer: next is a path here, never another host.
func TestNextStaysOnThisServer(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"/#/task/1": "/#/task/1", "//evil.example": "/", "/\\evil.example": "/", "https://evil.example": "/", "": "/", "/a\nb": "/"} {
		if got := next(in); got != want {
			t.Errorf("next(%q) = %q", in, got)
		}
	}
}

// TestNewRefusesBadConfig: the public URL is https (http on loopback only) with no path; the
// issuer and client id are required; https public URLs get Secure __Host- cookies.
func TestNewRefusesBadConfig(t *testing.T) {
	t.Parallel()
	for _, pub := range []string{"http://biglaw.example", "https://biglaw.example/app", "ftp://x", "https://u@x", ""} {
		if _, err := New(nil, Config{Issuer: "https://idp.example", ClientID: "c", PublicURL: pub}); err == nil {
			t.Errorf("public URL %q accepted", pub)
		}
	}
	if _, err := New(nil, Config{ClientID: "c", PublicURL: "https://x"}); err == nil {
		t.Error("no issuer accepted")
	}
	s, err := New(nil, Config{Issuer: "https://idp.example", ClientID: "c", PublicURL: "https://biglaw.example/"})
	if err != nil || !s.secure || s.cookie != "__Host-biglaw_session" || s.redirect != "https://biglaw.example/auth/callback" || s.Host() != "biglaw.example" {
		t.Fatalf("https: %v %+v", err, s)
	}
	rec := httptest.NewRecorder()
	s.setCookie(rec, s.cookie, "v", time.Hour)
	if h := rec.Header().Get("Set-Cookie"); !strings.Contains(h, "Secure") || !strings.Contains(h, "HttpOnly") || !strings.HasPrefix(h, "__Host-") || strings.Contains(h, "Domain") {
		t.Fatalf("https cookie: %s", h)
	}
}

// TestDiscoveryChecks: the discovery document's issuer must be the configured one exactly, its
// endpoints https (plain http only beside a loopback issuer), and S256 among its PKCE methods.
func TestDiscoveryChecks(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		doc  func(base string) map[string]any
		want string
	}{
		{"issuer differs", func(b string) map[string]any {
			return map[string]any{"issuer": b + "/", "authorization_endpoint": b + "/a", "token_endpoint": b + "/t", "jwks_uri": b + "/j"}
		}, "issuer"},
		{"plain http endpoint", func(b string) map[string]any {
			return map[string]any{"issuer": b, "authorization_endpoint": b + "/a", "token_endpoint": "http://idp.example/t", "jwks_uri": b + "/j"}
		}, "https"},
		{"no S256", func(b string) map[string]any {
			return map[string]any{"issuer": b, "authorization_endpoint": b + "/a", "token_endpoint": b + "/t", "jwks_uri": b + "/j", "code_challenge_methods_supported": []string{"plain"}}
		}, "S256"},
	} {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(c.doc(srv.URL)) }))
		s, _ := New(nil, Config{Issuer: srv.URL, ClientID: "c", PublicURL: "http://127.0.0.1:1"})
		if err := s.Discover(context.Background()); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
		srv.Close()
	}
}

// TestSignInNeverLogsSecrets: no log line and no answer holds the client secret, an authorization
// code, an ID token, a PKCE verifier, a state or a session cookie, through sign-ins that pass and fail.
func TestSignInNeverLogsSecrets(t *testing.T) {
	t.Parallel()
	r := newRig(t, "ada@firm.example")
	var tokens []string
	r.idp.Raw = func(n string) string { tok := r.idp.IDToken(n); tokens = append(tokens, tok); return tok }
	c := authtest.Browser(t)
	r.signIn(t, c)
	cookie := sessionCookie(t, c, r.srv.URL).Value
	r.idp.Raw = func(n string) string {
		tok := r.idp.Sign("RS256", "rsa-1", map[string]any{"iss": "x", "nonce": n})
		tokens = append(tokens, tok)
		return tok
	}
	r.signIn(t, authtest.Browser(t))
	r.idp.Raw = nil
	b := authtest.Browser(t)
	cb := r.callbackWith(t, b)
	p, _ := r.s.open(r.loginCookie(t, b))
	login := r.loginCookie(t, b)
	p.Verifier = "x" + p.Verifier
	r.setLogin(t, b, r.s.seal(p))
	r.get(t, b, cb.String()) // the issuer's error_description quotes the code: never repeated
	secrets := append([]string{r.idp.Secret, cookie}, tokens...)
	for _, f := range r.idp.Tokens {
		secrets = append(secrets, f.Get("code"), f.Get("code_verifier"))
	}
	secrets = append(secrets, cb.Query().Get("state"), login)
	out := r.logs.String() + strings.Join(r.seen, "\n")
	if !strings.Contains(r.logs.String(), "signed in") || !strings.Contains(r.logs.String(), "sign-in refused") {
		t.Fatalf("the logs miss the sign-ins: %s", r.logs.String())
	}
	for i, s := range secrets {
		if s == "" {
			t.Fatalf("secret %d is empty", i)
		}
		for _, part := range []string{s, strings.Split(s, ".")[len(strings.Split(s, "."))-1]} {
			if len(part) >= 16 && strings.Contains(out, part) {
				t.Errorf("secret %d (%s…) appears in a log or an answer", i, fmt.Sprint(part[:6]))
			}
		}
	}
}

// signRaw signs a hand-written claims payload with the issuer's RSA key (rsa-1).
func signRaw(i *authtest.IdP, claims string) string {
	in := authtest.Segment(map[string]any{"alg": "RS256", "kid": "rsa-1", "typ": "JWT"}) + "." + base64.RawURLEncoding.EncodeToString([]byte(claims))
	d := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, i.RSA, crypto.SHA256, d[:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestAdminMatchIsExactlyWhatTheIssuerVerified (review r1 M-2, its repro): an email entry matches
// only the address the issuer signed and verified, byte for byte after ASCII lowercasing: a soft
// hyphen or zero-width space inside it, a KELVIN SIGN or LONG S that Unicode folds to k or s, or
// email_verified as the string "true" is a member; plain ASCII in other case is the admin.
func TestAdminMatchIsExactlyWhatTheIssuerVerified(t *testing.T) {
	t.Parallel()
	r := newRig(t, "ada@firm.example", "kate@firm.example", "sam@firm.example")
	for _, c := range []struct {
		email    string
		verified any
		admin    bool
	}{
		{"a\u00adda@firm.example", true, false}, {"ada@firm.example\u200b", true, false}, {"\u212Aate@firm.example", true, false},
		{"\u017Fam@firm.example", true, false}, {"ada@firm.example", "true", false}, {"ada@firm.example", 1, false},
		{"ADA@Firm.Example", true, true}, {"kate@firm.example", true, true},
	} {
		var email string
		json.Unmarshal([]byte(`"`+c.email+`"`), &email)
		r.idp.User = map[string]any{"sub": "u-x", "email": email, "email_verified": c.verified, "name": "X"}
		b := authtest.Browser(t)
		if code, body := r.signIn(t, b); code != 303 {
			t.Fatalf("%q: sign-in %d %s", c.email, code, body)
		}
		if u, ok := r.whoami(t, b); !ok || u.Admin != c.admin {
			t.Errorf("%q (email_verified %v): admin %v, want %v", c.email, c.verified, u.Admin, c.admin)
		}
	}
	for _, c := range []struct {
		admins []string
		email  string
	}{{[]string{"ada@firm.example"}, "ada@firm.example "}, {[]string{"\u212Aate@firm.example"}, "kate@firm.example"}} {
		if IsAdmin(c.admins, "u", c.email, true) {
			t.Errorf("IsAdmin(%q, %q)", c.admins, c.email)
		}
	}
}

// TestClaimsDecodedStrictly (review r1 M-2, its second repro): claims are read by their exact
// names: a claim named like a read one in other case ("Email", "EMAIL_VERIFIED", "SUB") or a
// repeated claim refuses the token instead of overriding the standard one; so does a read claim of
// the wrong type.
func TestClaimsDecodedStrictly(t *testing.T) {
	t.Parallel()
	r := newRig(t, "ada@firm.example", "u-root")
	for name, tail := range map[string]string{
		"case override":   `"sub":"u-mallory","email":"mallory@elsewhere.example","email_verified":false,"Email":"ada@firm.example","EMAIL_VERIFIED":true`,
		"sub override":    `"sub":"u-mallory","SUB":"u-root"`,
		"repeated claim":  `"sub":"u-mallory","email":"mallory@elsewhere.example","email_verified":true,"email":"ada@firm.example"`,
		"sub not string":  `"sub":7`,
		"aud not strings": `"sub":"u-x","aud":[1]`,
		"exp not number":  `"sub":"u-x","exp":"soon"`,
	} {
		tail := tail
		r.idp.Raw = func(nonce string) string {
			now := r.clk.now().Unix()
			return signRaw(r.idp, fmt.Sprintf(`{"iss":%q,"aud":%q,"exp":%d,"iat":%d,"nonce":%q,%s}`, r.idp.URL, r.idp.ClientID, now+300, now, nonce, tail))
		}
		b := authtest.Browser(t)
		if code, body := r.signIn(t, b); code != 401 {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if n := r.sessions(t); n != 0 {
		t.Fatalf("%d sessions", n)
	}
}

// TestNbfChecked (review r1 L-2): a token not valid yet (nbf beyond the skew) is refused; one valid since a minute ago is not.
func TestNbfChecked(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.idp.Edit = func(c map[string]any) { c["nbf"] = r.clk.now().Add(24 * time.Hour).Unix() }
	if code, body := r.signIn(t, authtest.Browser(t)); code != 401 || !strings.Contains(body, "nbf") {
		t.Fatalf("nbf tomorrow: %d %s", code, body)
	}
	r.idp.Edit = func(c map[string]any) { c["nbf"] = r.clk.now().Add(-time.Minute).Unix() }
	if code, body := r.signIn(t, authtest.Browser(t)); code != 303 {
		t.Fatalf("nbf a minute ago: %d %s", code, body)
	}
}

// TestUnfinishedSignInsNeverLockAnyoneOut (review r2 M-3, its lockout repro; r1 M-1's intent): a
// sign-in in flight is sealed in its own browser's cookie, so a flood of unfinished ones from one
// address (behind a proxy: everyone's) stores nothing and refuses nobody, then or nine minutes on;
// next over 512 bytes returns to "/".
func TestUnfinishedSignInsNeverLockAnyoneOut(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for range 3000 {
		resp, err := c.Get(r.srv.URL + "/auth/login?next=/" + strings.Repeat("x", 2000))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 302 {
			t.Fatalf("an anonymous sign-in: %d", resp.StatusCode)
		}
		if n := len(resp.Header.Get("Set-Cookie")); n > 1024 {
			t.Fatalf("a %d-byte login cookie: next is not bounded", n)
		}
	}
	var tables int
	r.db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'auth_logins'`).Scan(&tables)
	if tables != 0 {
		t.Fatal("sign-ins in flight have a table")
	}
	for _, wait := range []time.Duration{0, 9 * time.Minute} {
		r.clk.add(wait)
		b := authtest.Browser(t)
		resp, err := b.Get(r.srv.URL + "/auth/login?next=/" + strings.Repeat("y", 600))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 302 {
			t.Fatalf("a real user after the flood (+%s): %d", wait, resp.StatusCode)
		}
		resp, _ = b.Get(resp.Header.Get("Location"))
		resp.Body.Close()
		resp, _ = b.Get(resp.Header.Get("Location"))
		resp.Body.Close()
		if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
			t.Fatalf("a real user's sign-in after the flood (+%s): %d %s", wait, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if n := r.sessions(t); n != 2 {
		t.Fatalf("%d sessions, want the two real ones", n)
	}
}

// TestIssuerFetchesCoalesced (review r1 L-5): concurrent sign-ins against a slow issuer make one
// discovery request, a caller that gives up returns at once (no lock held across the fetch), and a
// failed discovery is answered from memory for a few seconds.
func TestIssuerFetchesCoalesced(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.idp.SetSlow(300 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	if err := r.s.Discover(ctx); err == nil || time.Since(start) > 200*time.Millisecond {
		t.Fatalf("a caller that gives up waited %s (%v)", time.Since(start), err)
	}
	cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); r.s.Discover(context.Background()) }()
	}
	wg.Wait()
	if n := r.idp.Discovered(); n != 1 {
		t.Fatalf("%d discovery requests for 9 concurrent callers, want 1", n)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	s, _ := New(nil, Config{Issuer: down.URL, ClientID: "c", PublicURL: "http://127.0.0.1:1", Now: r.clk.now})
	for range 3 {
		if err := s.Discover(context.Background()); err == nil {
			t.Fatal("a down issuer discovered")
		}
	}
	if s.p.discErr == nil {
		t.Fatal("the failure is not remembered")
	}
}

// TestAliveIsNotActivity (review r2): an open event stream's session check does not move seen_at,
// so a tab left open still idles out.
func TestAliveIsNotActivity(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.s.SetLifetimes(time.Hour, 24*time.Hour)
	c := authtest.Browser(t)
	r.signIn(t, c)
	alive := func() bool {
		resp, err := c.Get(r.srv.URL + "/alive")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	}
	for range 5 { // 50 minutes of a stream checking every 10 minutes
		r.clk.add(10 * time.Minute)
		if !alive() {
			t.Fatal("ended early")
		}
	}
	r.clk.add(11 * time.Minute)
	if alive() {
		t.Fatal("the stream's checks kept an idle session alive")
	}
}

// TestSafeName (review r3 L-8): a display name is never email-shaped.
func TestSafeName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, preferred, email, want string }{
		{"Ada Lovelace", "ada", "ada@firm.example", "Ada Lovelace"}, {"", "ada", "ada@firm.example", "ada"},
		{"ada@firm.example", "ada@firm.example", "ada@firm.example", "ada"}, {"", "", "", ""}, {"x@y", "", "", ""},
		{"ada\uff20firm.example", "ada\ufe6bfirm.example", "ada@firm.example", "ada"}, {"ada%40firm.example", "Ada", "", "Ada"},
	} {
		if got := safeName(c.name, c.preferred, c.email); got != c.want {
			t.Errorf("safeName(%q, %q, %q) = %q, want %q", c.name, c.preferred, c.email, got, c.want)
		}
	}
}
