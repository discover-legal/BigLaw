// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/discover-legal/BigLaw/internal/auth"
	"github.com/discover-legal/BigLaw/internal/docs"
)

type (
	principalKey struct{}
	liveKey      struct{} // func() bool: whether the request's session still exists (an SSE stream ends when it does not)
)

// SessionUser is Who under sign-in: the principal RequireSignIn put on the request (none: a
// principal that is nobody and no admin).
func SessionUser(r *http.Request) Principal {
	p, _ := r.Context().Value(principalKey{}).(Principal)
	return p
}

// public is what a browser with no session may reach, by exact pattern (a new route is behind the
// session unless it is added here with its reason): the UI's files (so it can show its sign-in page),
// GET /health (probes) and sign-in's three routes.
func public(pattern string) bool {
	switch pattern {
	case "GET /", "GET /health", "GET /auth/login", "GET /auth/callback", "POST /auth/logout":
		return true
	}
	return false
}

// RequireSignIn mounts sign-in's routes on mux and puts every other route behind a session: no
// session is 401 unauthenticated, a session's user is the request's principal (SessionUser), and
// every unsafe method, logout included, passes docs.Guard's cross-site refusal first (the session
// cookie is SameSite=Lax as well). A database failure looking the session up is 503, never 401.
func RequireSignIn(mux *http.ServeMux, s *auth.Service, log *slog.Logger) http.Handler {
	s.Routes(mux)
	mux.HandleFunc("POST /manage/sessions/revoke", admin(log, SessionUser, func(w http.ResponseWriter, r *http.Request, p Principal) {
		var in struct{ User string }
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&in); err != nil || strings.TrimSpace(in.User) == "" {
			docs.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is {\"user\": a sub or an email}", "errorType": "bad_request"})
			return
		}
		n, err := s.Revoke(r.Context(), in.User)
		if err != nil {
			log.Error("api: revoke failed", "err", err)
			docs.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the sessions could not be revoked (logged)", "errorType": "unavailable"})
			return
		}
		log.Info("api: sessions revoked by an admin", "admin", p.Label(), "sessions", n)
		docs.WriteJSON(w, 200, map[string]int64{"revoked": n})
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			same := false
			docs.Guard(log, func(http.ResponseWriter, *http.Request) { same = true })(w, r)
			if !same {
				return
			}
		}
		if _, pattern := mux.Handler(r); public(pattern) {
			mux.ServeHTTP(w, r)
			return
		}
		u, ok, err := s.User(r)
		switch {
		case err != nil:
			log.Error("api: session lookup failed", "err", err)
			docs.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sign-in is unavailable right now (logged)", "errorType": "unavailable"})
			return
		case !ok:
			w.Header().Set("Cache-Control", "no-store")
			docs.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in first: open BigLaw in the browser, or your session has ended (GET /auth/login)", "errorType": "unauthenticated"})
			return
		}
		p := Principal{ID: u.Sub, Name: u.Name, Email: u.Email, Admin: u.Admin, SignedIn: true}
		if !u.Verified {
			p.Email = "" // an address the issuer did not verify is no identity
		}
		ctx := context.WithValue(context.WithValue(r.Context(), principalKey{}, p), liveKey{}, func() bool { ok, err := s.Alive(r); return ok || err != nil })
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
