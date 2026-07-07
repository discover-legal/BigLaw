// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Browser OAuth login (Google / Microsoft / LinkedIn) + signed-cookie
// sessions. Routes are registered as static paths only — gin cannot mix
// :param and static segments under /auth, and /auth/clio/* may join later.
// First login auto-provisions a profile: partner when the email is in
// cfg.Auth.AdminEmails, lawyer otherwise (mirrors the TS implementation).
package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// authRateLimit / authRateWindow mirror the TS auth endpoints: 20 req/min/IP.
const (
	authRateLimit  = 20
	authRateWindow = time.Minute
)

// registerAuthRoutes mounts the browser OAuth login flow. All routes are
// rate-limited per client IP.
func (s *Server) registerAuthRoutes(r *gin.Engine) {
	limiter := auth.NewRateLimiter(authRateLimit, authRateWindow)
	rate := func(c *gin.Context) {
		if !limiter.Allow(c.ClientIP()) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		}
	}

	// Boolean map per provider — the shape the login screen (and the TS
	// implementation) expect: { google: bool, microsoft: bool, linkedin: bool }.
	r.GET("/auth/providers", rate, func(c *gin.Context) {
		out := gin.H{}
		for _, p := range auth.Providers {
			out[string(p)] = auth.IsConfigured(s.cfg, p)
		}
		c.JSON(http.StatusOK, out)
	})

	// Static paths per provider — no :param segments (they conflict with the
	// other static /auth/* routes in gin's router tree).
	for _, p := range auth.Providers {
		r.GET("/auth/"+string(p)+"/login", rate, s.handleOAuthLogin(p))
		r.GET("/auth/"+string(p)+"/callback", rate, s.handleOAuthCallback(p))
	}

	r.POST("/auth/logout", rate, s.handleLogout)
}

// handleOAuthLogin redirects the browser to the provider's authorize URL,
// stashing the anti-CSRF state in a short-lived signed cookie.
func (s *Server) handleOAuthLogin(p auth.Provider) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.IsConfigured(s.cfg, p) {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not configured"})
			return
		}
		state := uuid.New().String()
		s.setAuthCookie(c, auth.StateCookieName,
			auth.SignState(s.cfg.Auth.SessionSecret, state), int(auth.StateMaxAge.Seconds()))

		target, err := auth.AuthorizeURL(s.cfg, p, state)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Redirect(http.StatusFound, target)
	}
}

// handleOAuthCallback verifies the state, exchanges the code, fetches the
// identity, maps it to a lawyer profile (auto-provisioning on first login),
// sets the session cookie, and bounces the browser back to the UI.
func (s *Server) handleOAuthCallback(p auth.Provider) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !auth.IsConfigured(s.cfg, p) {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not configured"})
			return
		}

		code := c.Query("code")
		state := c.Query("state")
		signedState, _ := c.Cookie(auth.StateCookieName)
		expected, stateOK := auth.VerifyState(s.cfg.Auth.SessionSecret, signedState)
		s.setAuthCookie(c, auth.StateCookieName, "", -1) // single-use

		if code == "" || state == "" || !stateOK ||
			subtle.ConstantTimeCompare([]byte(state), []byte(expected)) != 1 {
			s.auditAuthFailed(p, "invalid_state")
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid OAuth state"})
			return
		}

		ctx := c.Request.Context()
		token, err := auth.ExchangeCode(ctx, s.cfg, p, code)
		if err != nil {
			s.oauthFail(c, p, err.Error())
			return
		}
		identity, err := auth.FetchUserInfo(ctx, p, token)
		if err != nil {
			s.oauthFail(c, p, err.Error())
			return
		}
		if identity.Email == "" {
			s.oauthFail(c, p, "provider returned no email")
			return
		}
		if !identity.EmailVerified {
			s.oauthFail(c, p, "unverified_email")
			return
		}

		// Map to a lawyer profile; auto-provision on first login.
		profile := s.profiles.GetByEmail(identity.Email)
		if profile == nil {
			role := "lawyer"
			if s.isAdminEmail(identity.Email) {
				role = "partner"
			}
			profile, err = s.profiles.Create(auth.CreateProfileInput{
				Name:  identity.Name,
				Email: identity.Email,
				Role:  role,
			})
			if err != nil {
				s.oauthFail(c, p, "profile provisioning failed: "+err.Error())
				return
			}
		}

		sessionUser := types.SessionUser{
			ProfileID: profile.ID,
			Name:      profile.Name,
			Email:     profile.Email,
			Role:      profile.Role,
			Mode:      auth.ResolveMode(profile.Role, profile.Mode),
		}
		s.setAuthCookie(c, auth.SessionCookieName,
			auth.MintSession(s.cfg.Auth.SessionSecret, sessionUser, auth.SessionMaxAge),
			int(auth.SessionMaxAge.Seconds()))

		slog.Info("oauth login", "provider", p, "email", identity.Email, "role", profile.Role)
		audit.Default.Write(audit.WriteRequest{
			Event:   "auth.login",
			ActorID: profile.ID,
			Data:    map[string]interface{}{"provider": string(p), "role": string(profile.Role)},
		})
		c.Redirect(http.StatusFound, s.cfg.Auth.UIURL)
	}
}

// handleLogout revokes the session token (so replayed cookies are rejected)
// and clears the cookie.
func (s *Server) handleLogout(c *gin.Context) {
	actorID := audit.ActorAnonymous
	if raw, err := c.Cookie(auth.SessionCookieName); err == nil && raw != "" {
		// Resolve the actor before revoking the token.
		if u := auth.ParseSession(s.cfg.Auth.SessionSecret, raw); u != nil {
			actorID = u.ProfileID
		}
		auth.RevokeSession(s.cfg.Auth.SessionSecret, raw)
	}
	s.setAuthCookie(c, auth.SessionCookieName, "", -1)
	audit.Default.Write(audit.WriteRequest{
		Event:   "auth.logout",
		ActorID: actorID,
		Data:    map[string]interface{}{},
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// sessionUserFromCookie parses + verifies the signed session cookie and
// resolves the live profile (so role/mode changes apply immediately and
// deleted profiles lose access). Returns nil when the cookie is absent,
// invalid, expired, revoked, or orphaned. The auth middleware uses this as
// an alternative credential to the bearer token.
func (s *Server) sessionUserFromCookie(c *gin.Context) *types.SessionUser {
	raw, err := c.Cookie(auth.SessionCookieName)
	if err != nil || raw == "" {
		return nil
	}
	u := auth.ParseSession(s.cfg.Auth.SessionSecret, raw)
	if u == nil {
		return nil
	}
	p := s.profiles.Get(u.ProfileID)
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

// ─── Helpers ──────────────────────────────────────────────────────────────────

// setAuthCookie writes an httpOnly SameSite=Lax cookie; Secure when the
// public base URL is https. MaxAge < 0 clears the cookie.
func (s *Server) setAuthCookie(c *gin.Context, name, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.cfg.Auth.BaseURL, "https"),
	})
}

// isAdminEmail reports whether the email is on the ADMIN_EMAILS list
// (case-insensitive) and should be provisioned as a partner on first login.
func (s *Server) isAdminEmail(email string) bool {
	for _, admin := range s.cfg.Auth.AdminEmails {
		if strings.EqualFold(strings.TrimSpace(admin), email) {
			return true
		}
	}
	return false
}

func (s *Server) auditAuthFailed(p auth.Provider, reason string) {
	audit.Default.Write(audit.WriteRequest{
		Event:   "auth.failed",
		ActorID: audit.ActorAnonymous,
		Data:    map[string]interface{}{"provider": string(p), "reason": reason},
	})
}

// oauthFail logs + audits a callback failure and bounces the browser back to
// the UI with an error flag (mirrors the TS ?auth_error=1 contract).
func (s *Server) oauthFail(c *gin.Context, p auth.Provider, reason string) {
	slog.Warn("oauth callback failed", "provider", p, "reason", reason)
	s.auditAuthFailed(p, reason)
	c.Redirect(http.StatusFound, s.cfg.Auth.UIURL+"?auth_error=1")
}
