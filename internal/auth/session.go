// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Lifetimes: a session ends after IdleTimeout without a request or MaxAge after sign-in, whichever
// is first; a sign-in must come back from the issuer within loginTTL.
const (
	IdleTimeout = 8 * time.Hour
	MaxAge      = 7 * 24 * time.Hour
	loginTTL    = 10 * time.Minute // inside the sealed login cookie
	touchEvery  = time.Minute      // a request moves seen_at at most this often (one write a minute per session)
)

// Config is sign-in's configuration (cmd/biglaw config.go validates it).
type Config struct {
	Issuer, ClientID, ClientSecret string
	PublicURL                      string   // BigLaw's external base URL: the redirect URI's base; https makes the cookies Secure and __Host-
	Admins                         []string // BIGLAW_ADMINS: a verified email (has an @) or a sub, each
	HTTP                           *http.Client
	Now                            func() time.Time // tests: a fake clock
	Log                            *slog.Logger
}

// Service is sign-in: its routes (/auth/login, /auth/callback, /auth/logout) and the session lookup.
type Service struct {
	p                     *provider
	db                    *sql.DB
	log                   *slog.Logger
	admins                []string
	redirect, home, host  string
	secure                bool
	cookie, loginCookie   string
	idle, maxAge, loginIn time.Duration
	swept                 atomic.Int64 // unix seconds of the last sweep of ended sessions
	logged                atomic.Int64 // unix seconds of the last refused-sign-in log line (logFail)
	aead                  cipher.AEAD  // seals the login cookie; its key is made at New, so a restart ends sign-ins in flight
}

// maxNext bounds next (it rides in the login cookie).
const maxNext = 512

// User is a signed-in person: the issuer's sub, their email (Verified as the issuer says), a
// display name, and whether BIGLAW_ADMINS names them.
type User struct {
	Sub, Email, Name string
	Verified, Admin  bool
}

// New builds sign-in over db. It makes no network call: the issuer is discovered at the first
// sign-in (Discover probes it at startup).
func New(db *sql.DB, c Config) (*Service, error) {
	pub, err := url.Parse(strings.TrimSuffix(c.PublicURL, "/"))
	if err != nil || pub.Host == "" || !(pub.Scheme == "https" || loopbackHTTP(pub)) || pub.Path != "" || pub.RawQuery != "" || pub.Fragment != "" || pub.User != nil {
		return nil, errors.New("auth: BIGLAW_PUBLIC_URL must be https://host[:port] (plain http only on loopback), with no path")
	}
	if c.Issuer == "" || c.ClientID == "" {
		return nil, errors.New("auth: BIGLAW_OIDC_ISSUER and BIGLAW_OIDC_CLIENT_ID are both required")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	nc := *hc
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse } // an answer must come from the URL itself
	now := c.Now
	if now == nil {
		now = time.Now
	}
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Service{p: &provider{issuer: c.Issuer, clientID: c.ClientID, secret: c.ClientSecret, http: &nc, now: now}, db: db, log: log, admins: c.Admins,
		home: pub.Scheme + "://" + pub.Host, host: pub.Hostname(), secure: pub.Scheme == "https", idle: IdleTimeout, maxAge: MaxAge, loginIn: loginTTL}
	s.redirect = s.home + "/auth/callback"
	s.cookie, s.loginCookie = "biglaw_session", "biglaw_login"
	if s.secure { // __Host-: Secure, Path=/, no Domain, so no other host or path can set or shadow it
		s.cookie, s.loginCookie = "__Host-biglaw_session", "__Host-biglaw_login"
	}
	key := make([]byte, 32)
	rand.Read(key)
	block, _ := aes.NewCipher(key) // a 32-byte key never fails
	s.aead, _ = cipher.NewGCM(block)
	return s, nil
}

// SetLifetimes replaces the idle and absolute session lifetimes (tests).
func (s *Service) SetLifetimes(idle, maxAge time.Duration) { s.idle, s.maxAge = idle, maxAge }

// Host is BIGLAW_PUBLIC_URL's host name: the one DNS name the server answers to (cmd/biglaw's bad_host rule).
func (s *Service) Host() string { return s.host }

// Discover reads the issuer's discovery document (serve's startup probe; a failure is retried at sign-in).
func (s *Service) Discover(ctx context.Context) error { _, err := s.p.discover(ctx); return err }

// IsAdmin reports whether admins names u: an entry with an @ by u's email, only when the issuer
// verified it and both are plain ASCII (plainEmail), compared byte for byte after ASCII-only
// lowercasing (no Unicode folding: U+212A is never "k"); any other entry by u's sub exactly.
func IsAdmin(admins []string, sub, email string, verified bool) bool {
	for _, a := range admins {
		if strings.Contains(a, "@") {
			if verified && plainEmail(email) && plainEmail(a) && asciiLower(a) == asciiLower(email) {
				return true
			}
		} else if a == sub {
			return true
		}
	}
	return false
}

// plainEmail is an address of printable ASCII only (no space, control, format or non-ASCII
// character), 3 to 320 bytes, with an @: the only kind an admin entry matches.
func plainEmail(e string) bool {
	if len(e) < 3 || len(e) > 320 || !strings.Contains(e, "@") {
		return false
	}
	for i := 0; i < len(e); i++ {
		if e[i] <= ' ' || e[i] >= 0x7f {
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

// hash is what the database holds of a session id or a sign-in's state: a database reader cannot
// present it as a cookie.
func hash(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

// clean is a display name or an address as BigLaw shows and logs it: no control or format characters, at most 200 bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	for len(s) > 200 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

func (s *Service) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge / time.Second)}
	if maxAge < 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// User is the request's signed-in user: its cookie names a session that has neither idled out nor
// passed its absolute age (an ended one is deleted). An error is the database's (503, never 401).
func (s *Service) User(r *http.Request) (User, bool, error) { return s.lookup(r, true) }

// Alive is User without counting as activity (seen_at is not moved): an open event stream asks it,
// so an idle tab's session still ends after IdleTimeout.
func (s *Service) Alive(r *http.Request) (bool, error) {
	_, ok, err := s.lookup(r, false)
	return ok, err
}

func (s *Service) lookup(r *http.Request, touch bool) (User, bool, error) {
	c, err := r.Cookie(s.cookie)
	if err != nil || len(c.Value) != 43 {
		return User{}, false, nil
	}
	ctx, id := r.Context(), hash(c.Value)
	var u User
	var seen, expires time.Time
	err = s.db.QueryRowContext(ctx, `SELECT sub, email, email_verified, name, seen_at, expires_at FROM sessions WHERE id_hash = $1`, id).
		Scan(&u.Sub, &u.Email, &u.Verified, &u.Name, &seen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	} else if err != nil {
		return User{}, false, fmt.Errorf("auth: session lookup: %w", err)
	}
	now := s.p.now()
	if !now.Before(expires) || now.Sub(seen) >= s.idle {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = $1`, id)
		return User{}, false, err
	}
	if touch && now.Sub(seen) >= touchEvery {
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET seen_at = $2 WHERE id_hash = $1`, id, now); err != nil {
			return User{}, false, fmt.Errorf("auth: session touch: %w", err)
		}
	}
	u.Admin = IsAdmin(s.admins, u.Sub, u.Email, u.Verified)
	return u, true, nil
}

// Routes mounts sign-in. POST /auth/logout must sit behind the caller's cross-site guard (api's
// RequireSignIn guards every unsafe method).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/logout", s.logout)
}

// next is where a sign-in returns: a path on this server only (never //host or /\host), else "/".
func next(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") || len(v) > maxNext ||
		strings.ContainsFunc(v, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\\' }) {
		return "/"
	}
	return v
}

// pending is a sign-in in flight, sealed (AES-GCM, sealKey) into the login cookie: the state the
// callback must name, the nonce and PKCE verifier, where to return, and when it began. No server
// state: anonymous sign-ins cost no row, and no number of them can refuse another browser's.
type pending struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	At       int64  `json:"t"` // unix seconds
}

const sealAAD = "biglaw login v1"

func (s *Service) seal(p pending) string {
	b, _ := json.Marshal(p)
	n := make([]byte, s.aead.NonceSize())
	rand.Read(n)
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(n, n, b, []byte(sealAAD)))
}

// open is the sealed sign-in, if c was sealed by this process (a restart's new key refuses it) and
// has not been tampered with.
func (s *Service) open(c string) (pending, bool) {
	var p pending
	b, err := base64.RawURLEncoding.DecodeString(c)
	ns := s.aead.NonceSize()
	if err != nil || len(b) < ns {
		return p, false
	}
	plain, err := s.aead.Open(nil, b[:ns], b[ns:], []byte(sealAAD))
	return p, err == nil && json.Unmarshal(plain, &p) == nil
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	m, err := s.p.discover(r.Context())
	if err != nil {
		s.fail(w, http.StatusBadGateway, err)
		return
	}
	now := s.p.now()
	s.sweep(r.Context(), now)
	p := pending{State: random(), Nonce: random(), Verifier: random(), Next: next(r.URL.Query().Get("next")), At: now.Unix()}
	s.setCookie(w, s.loginCookie, s.seal(p), s.loginIn) // binds the sign-in to this browser (login CSRF)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.p.authURL(m, s.redirect, p.State, p.Nonce, p.Verifier), http.StatusFound)
}

// callback finishes a sign-in. The login cookie is cleared at once (single use in this browser);
// a replay elsewhere would also need that cookie, and then still fails at the issuer: the code is
// single use and bound to the PKCE verifier, and the ID token to the nonce (README § Sign-in).
func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	q, ctx := r.URL.Query(), r.Context()
	w.Header().Set("Cache-Control", "no-store")
	s.setCookie(w, s.loginCookie, "", -1)
	state := q.Get("state")
	c, cerr := r.Cookie(s.loginCookie)
	var p pending
	ok := cerr == nil
	if ok {
		p, ok = s.open(c.Value)
	}
	if state == "" || !ok || subtle.ConstantTimeCompare([]byte(p.State), []byte(state)) != 1 {
		s.fail(w, http.StatusBadRequest, errors.New("auth: this sign-in was not started in this browser, or BigLaw restarted since (state mismatch)"))
		return
	}
	if age := s.p.now().Sub(time.Unix(p.At, 0)); age > s.loginIn || age < -clockSkew || p.Verifier == "" || p.Nonce == "" {
		s.fail(w, http.StatusBadRequest, errors.New("auth: this sign-in has expired; start again"))
		return
	}
	nonce, verifier, to := p.Nonce, p.Verifier, next(p.Next)
	if e := q.Get("error"); e != "" {
		s.fail(w, http.StatusUnauthorized, fmt.Errorf("auth: the identity provider did not sign you in (%s)", oauthCode(e)))
		return
	}
	code := q.Get("code")
	if code == "" {
		s.fail(w, http.StatusBadRequest, errors.New("auth: the identity provider sent no code"))
		return
	}
	m, err := s.p.discover(ctx)
	var idt string
	if err == nil {
		idt, err = s.p.exchange(ctx, m, code, verifier, s.redirect)
	}
	var cl Claims
	if err == nil {
		cl, err = s.p.verify(ctx, m, idt, nonce)
	}
	if err != nil {
		s.fail(w, http.StatusUnauthorized, err)
		return
	}
	name := clean(cl.Name)
	for _, alt := range []string{cl.PreferredUsername, cl.Email, cl.Sub} {
		if name == "" {
			name = clean(alt)
		}
	}
	email, verified := cl.Email, cl.EmailVerified && plainEmail(cl.Email) // an address with anything but plain ASCII is shown, never matched
	if !plainEmail(email) {
		email = clean(email)
	}
	id, now := random(), s.p.now()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if old, err := r.Cookie(s.cookie); err == nil { // a new id at every sign-in: a planted or old session never carries over
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = $1`, hash(old.Value)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions (id_hash, sub, email, email_verified, name, created_at, seen_at, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $6, $7)`,
			hash(id), cl.Sub, email, verified, name, now, now.Add(s.maxAge))
		return err
	})
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, fmt.Errorf("auth: session could not be stored: %w", err))
		return
	}
	s.setCookie(w, s.cookie, id, s.maxAge)
	s.log.Info("auth: signed in", "sub", clean(cl.Sub), "email", email, "emailVerified", verified, "admin", IsAdmin(s.admins, cl.Sub, email, verified))
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// sweep deletes ended sessions, at most once a minute (so at most one log line a minute too).
func (s *Service) sweep(ctx context.Context, now time.Time) {
	if last := s.swept.Load(); now.Unix()-last < 60 || !s.swept.CompareAndSwap(last, now.Unix()) {
		return
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= $1 OR seen_at <= $2`, now, now.Add(-s.idle)); err != nil {
		s.log.Warn("auth: sweep of ended sessions failed", "err", err)
	}
}

// Revoke ends every session of user: an issuer subject exactly, or (with an @) an email, case-insensitively.
// It is how an admin signs out someone who left (README § Sign-in): the issuer is not asked again before a session ends.
func (s *Service) Revoke(ctx context.Context, user string) (int64, error) {
	if user = strings.TrimSpace(user); user == "" {
		return 0, errors.New("auth: name a user: their sub, or their email")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE sub = $1 OR (strpos($1, '@') > 0 AND lower(email) = lower($1))`, user)
	if err != nil {
		return 0, fmt.Errorf("auth: revoke: %w", err)
	}
	n, _ := res.RowsAffected()
	s.log.Info("auth: sessions revoked", "user", clean(user), "sessions", n)
	return n, nil
}

func (s *Service) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// logout revokes the request's session (the row is deleted: the cookie is dead wherever it was copied) and clears the cookie.
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookie); err == nil {
		if _, err := s.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id_hash = $1`, hash(c.Value)); err != nil {
			s.fail(w, http.StatusServiceUnavailable, fmt.Errorf("auth: sign-out: %w", err))
			return
		}
	}
	s.setCookie(w, s.cookie, "", -1)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(`{"signedOut":true}` + "\n"))
}

// fail answers a sign-in that did not complete with a small page in BigLaw's words (never a token,
// a code or the issuer's own text) and logs why.
func (s *Service) fail(w http.ResponseWriter, code int, err error) {
	// anonymous requests reach here: at most one line a second, so a flood cannot flood the log
	if now := time.Now().Unix(); s.logged.Load() != now && s.logged.Swap(now) != now {
		if code >= 500 {
			s.log.Warn("auth: sign-in failed (one line a second at most)", "err", err)
		} else {
			s.log.Info("auth: sign-in refused (one line a second at most)", "err", err)
		}
	}
	msg := strings.TrimPrefix(err.Error(), "auth: ")
	if code >= 500 && !strings.Contains(msg, "issuer") {
		msg = "sign-in is unavailable right now (logged)"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>BigLaw sign-in</title><body style="font-family:sans-serif;max-width:40em;margin:3em auto">
<h1>Sign-in did not complete</h1><p>%s.</p><p><a href="/auth/login">Try again</a></p>`, html.EscapeString(msg))
}
