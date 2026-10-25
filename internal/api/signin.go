// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/discover-legal/BigLaw/internal/auth"
	"github.com/discover-legal/BigLaw/internal/docs"
)

type principalKey struct{}

// SessionUser is Who under sign-in: the principal RequireSignIn put on the request (none: a
// principal that is nobody and no admin).
func SessionUser(r *http.Request) Principal {
	p, _ := r.Context().Value(principalKey{}).(Principal)
	return p
}

// public is what a browser with no session may reach: the UI's files (so it can show its sign-in
// page), GET /health and sign-in itself.
func public(pattern string) bool {
	return pattern == "GET /" || pattern == "GET /health" || strings.HasPrefix(pattern, "GET /auth/") || pattern == "POST /auth/logout"
}

// RequireSignIn mounts sign-in's routes on mux and puts every other route behind a session: no
// session is 401 unauthenticated, a session's user is the request's principal (SessionUser), and
// every unsafe method, logout included, passes docs.Guard's cross-site refusal first (the session
// cookie is SameSite=Lax as well). A database failure looking the session up is 503, never 401.
func RequireSignIn(mux *http.ServeMux, s *auth.Service, log *slog.Logger) http.Handler {
	s.Routes(mux)
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
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
