// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Middleware ───────────────────────────────────────────────────────────────

func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.cfg.Auth.Enabled {
			u := auth.LocalPartner
			c.Set(ctxUserKey, &u)
			c.Next()
			return
		}

		// A valid signed session cookie (browser OAuth login) is an
		// alternative credential to the bearer token. Checked before the
		// /auth/ bypass so partner-gated auth routes (e.g. the Clio connect
		// flow) see the logged-in user.
		if u := s.sessionUserFromCookie(c); u != nil {
			c.Set(ctxUserKey, u)
			c.Next()
			return
		}

		// These endpoints authenticate themselves (OAuth state or provider
		// signatures) and therefore form the explicit public route set.
		if isPublicRoute(c.Request.Method, c.Request.URL.Path) {
			c.Next()
			return
		}

		u := s.bearerUser(c.GetHeader("Authorization"))
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "valid bearer token required"})
			c.Abort()
			return
		}
		c.Set(ctxUserKey, u)
		c.Next()
	}
}

func (s *Server) bearerUser(authorization string) *types.SessionUser {
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || s.cfg.API.APIKey == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.API.APIKey)) != 1 {
		return nil
	}
	p := s.profiles.Get(s.cfg.API.ProfileID)
	if p == nil {
		return nil
	}
	return &types.SessionUser{
		ProfileID: p.ID,
		Name:      p.Name,
		Email:     p.Email,
		Role:      p.Role,
		Mode:      auth.ResolveMode(p.Role, p.Mode),
	}
}

func isPublicRoute(method, path string) bool {
	if method == http.MethodPost && (path == "/bots/slack/events" || path == "/bots/teams/webhook") {
		return true
	}
	if method == http.MethodGet {
		switch path {
		case "/auth/providers", "/auth/google/login", "/auth/google/callback",
			"/auth/microsoft/login", "/auth/microsoft/callback",
			"/auth/linkedin/login", "/auth/linkedin/callback", "/auth/clio/callback":
			return true
		}
	}
	return false
}

// ─── Auth helpers ─────────────────────────────────────────────────────────────

func getUser(c *gin.Context) *types.SessionUser {
	if v, ok := c.Get(ctxUserKey); ok {
		if u, ok := v.(*types.SessionUser); ok {
			return u
		}
	}
	return nil
}

// reqIdentity derives the durable-store identity (drives database RLS) from the
// request's session user. A request with no user is anonymous and, under the
// default-deny policy, sees/writes nothing.
func reqIdentity(c *gin.Context) context.Context {
	u := getUser(c)
	if u == nil {
		return c.Request.Context() // no identity → default-deny
	}
	return store.WithIdentity(c.Request.Context(), u.ProfileID, auth.IsPartner(u))
}

// requirePartner writes 403 and returns false if the caller is not a partner.
func requirePartner(c *gin.Context) bool {
	u := getUser(c)
	if !auth.IsPartner(u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "partner access required"})
		return false
	}
	return true
}
