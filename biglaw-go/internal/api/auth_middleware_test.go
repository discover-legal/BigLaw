// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Coverage gap this file targets: authMiddleware (server.go) has NO existing
// direct test anywhere in the api package (no server_test.go / auth_test.go).
// It verifies credential-bound identity and the narrow public-route policy.

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/config"
)

// newAuthMiddlewareServer builds a Server with just enough wiring for
// authMiddleware: config (Auth.Enabled + API.APIKey) and a real,
// file-backed ProfileStore rooted at a temp dir.
func newAuthMiddlewareServer(t *testing.T) (*Server, *auth.ProfileStore) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Auth.Enabled = true
	cfg.API.APIKey = "test-bearer-key"
	cfg.Persistence.ProfilesFile = t.TempDir() + "/profiles.json"

	profiles := auth.NewProfileStore(cfg)
	if err := profiles.Init(); err != nil {
		t.Fatalf("profiles.Init: %v", err)
	}
	return &Server{cfg: cfg, profiles: profiles}, profiles
}

func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, profiles := newAuthMiddlewareServer(t)

	lawyer, err := profiles.Create(auth.CreateProfileInput{
		Name: "Alice Lawyer", Email: "alice@example.com", Role: "lawyer",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	s.cfg.API.ProfileID = lawyer.ID

	r := gin.New()
	r.Use(s.authMiddleware())
	r.GET("/probe", func(c *gin.Context) {
		u := getUser(c)
		if u == nil {
			c.JSON(http.StatusOK, gin.H{"authenticated": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{"authenticated": true, "profileId": u.ProfileID})
	})
	// Only declared login/provider endpoints are public; arbitrary /auth paths
	// remain protected.
	r.GET("/auth/providers", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/auth/probe", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	tests := []struct {
		name       string
		path       string
		authHeader string
		profileID  string
		wantStatus int
	}{
		{"no credentials at all", "/probe", "", "", http.StatusUnauthorized},
		{"wrong bearer token", "/probe", "Bearer wrong-key", lawyer.ID, http.StatusUnauthorized},
		{"X-Profile-ID alone, no bearer, must NOT authenticate", "/probe", "", lawyer.ID, http.StatusUnauthorized},
		{"correct token uses bound profile", "/probe", "Bearer test-bearer-key", "no-such-profile", http.StatusOK},
		{"declared public auth route needs no credentials", "/auth/providers", "", "", http.StatusOK},
		{"undeclared auth route remains protected", "/auth/probe", "", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			if tt.profileID != "" {
				req.Header.Set("X-Profile-ID", tt.profileID)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Errorf("%s: status = %d, want %d (body: %s)", tt.name, w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}

	// TODO: session-cookie precedence. sessionUserFromCookie() must be
	// honored ahead of the bearer-token branch (server.go: the cookie check
	// runs before the /auth/ bypass so partner-gated auth routes like the
	// Clio connect flow see the logged-in user). Mint a signed cookie with
	// auth.MintSession(cfg.Auth.SessionSecret, sessionUser, auth.SessionMaxAge),
	// set it via http.Cookie{Name: auth.SessionCookieName, ...} on the
	// request, and assert it authenticates with NO Authorization header —
	// then assert a tampered/expired cookie falls through to the bearer
	// check instead of authenticating.
}

// TestAuthMiddlewareDisabled verifies the local-dev bypass: with
// cfg.Auth.Enabled=false every request must resolve to auth.LocalPartner
// regardless of headers (no bearer token, no profile lookup at all).
func TestAuthMiddlewareDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{} // Auth.Enabled defaults false
	s := &Server{cfg: cfg}

	r := gin.New()
	r.Use(s.authMiddleware())
	r.GET("/probe", func(c *gin.Context) {
		u := getUser(c)
		if u == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "no user set"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profileId": u.ProfileID, "role": string(u.Role)})
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), auth.LocalPartner.ProfileID) {
		t.Errorf("body = %q, want the LocalPartner profile id (%s)", w.Body.String(), auth.LocalPartner.ProfileID)
	}
}
