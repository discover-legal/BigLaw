// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Routing on the raw path (so %2F stays inside one :param) must not change
// how static routes match, nor which requests count as public.
func TestRawPathRoutingAndPublicRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _ := newAuthMiddlewareServer(t)
	r := gin.New()
	r.UseRawPath = true
	r.Use(s.authMiddleware())
	ok := func(c *gin.Context) { c.String(http.StatusOK, c.FullPath()+" "+c.Param("id")) }
	r.GET("/health", ok)
	r.GET("/auth/providers", ok)
	r.GET("/tasks/:id", ok)
	// A param route that a decoded path could mistake for a public one.
	r.POST("/bots/:kind/events", ok)
	h := canonicalRawPath(r)

	for _, c := range []struct {
		method, path string
		want         int
		body         string
	}{
		{"GET", "/heal%74h", http.StatusUnauthorized, ""},               // static, gratuitous escape: same route as /health
		{"GET", "/auth/%70roviders", http.StatusOK, "/auth/providers "}, // public static route, escaped
		{"GET", "/auth/providers", http.StatusOK, "/auth/providers "},   //
		{"POST", "/bots/slack/events", http.StatusUnauthorized, ""},     // matched /bots/:kind/events: not public
		{"GET", "/tasks/a%2Fb", http.StatusUnauthorized, ""},            // protected
		{"GET", "/no-such-route", http.StatusUnauthorized, ""},          // unmatched: not public
		{"GET", "/auth/providers%2Fx", http.StatusUnauthorized, ""},     // not the public route
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.want || (c.body != "" && w.Body.String() != c.body) {
			t.Errorf("%s %s = %d %q, want %d %q", c.method, c.path, w.Code, w.Body.String(), c.want, c.body)
		}
	}

	// With auth off, the routed result itself: static routes match through
	// gratuitous escapes, and an escaped "/" stays inside the ID.
	s.cfg.Auth.Enabled = false
	for path, want := range map[string]string{
		"/heal%74h":        "/health ",
		"/tasks/a%2Fb":     "/tasks/:id a/b",
		"/tasks/%61%2F%62": "/tasks/:id a/b",
		"/tasks/50%25":     "/tasks/:id 50%",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", path, w.Code, w.Body.String(), want)
		}
	}
}
