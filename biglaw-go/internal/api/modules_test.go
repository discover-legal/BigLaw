// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/modules"
)

// fakeModule stands in for an optional module (e.g. intake): one
// self-authenticating route and one that needs a firm credential.
type fakeModule struct{ name string }

func (m fakeModule) ModuleName() string { return m.name }
func (m fakeModule) PublicRoute(method, route string) bool {
	return method == http.MethodPost && route == "/fake/hook/:id"
}
func (m fakeModule) Mount(r gin.IRouter, host modules.Host) {
	r.POST("/fake/hook/:id", func(c *gin.Context) { c.String(http.StatusOK, "hook") })
	r.GET("/fake/private", func(c *gin.Context) {
		if host.User(c) == nil {
			c.Status(http.StatusUnauthorized)
			return
		}
		c.String(http.StatusOK, "private")
	})
}

func newModuleTestServer() *Server {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Auth.Enabled = true
	s := &Server{cfg: cfg}
	r := gin.New()
	r.Use(s.authMiddleware())
	s.router = r
	return s
}

func serve(s *Server, method, path string) int {
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code
}

// A mounted module's self-authenticating routes join the public set — by
// registered route pattern, so a concrete path under a param route is
// public; its other routes still need a credential.
func TestMountPublicRoutes(t *testing.T) {
	modules.Default.Register("fake-on", "test module", true, "test")
	s := newModuleTestServer()
	s.Mount(fakeModule{name: "fake-on"})
	if code := serve(s, http.MethodPost, "/fake/hook/abc"); code != http.StatusOK {
		t.Fatalf("module public route blocked by core auth: %d", code)
	}
	if code := serve(s, http.MethodGet, "/fake/private"); code != http.StatusUnauthorized {
		t.Fatalf("module private route reachable anonymously: %d", code)
	}
}

// A disabled module mounts nothing: its routes 404 and its public
// predicate never widens the core's public set.
func TestMountDisabledModule(t *testing.T) {
	modules.Default.Register("fake-off", "test module", false, "test")
	s := newModuleTestServer()
	s.Mount(fakeModule{name: "fake-off"})
	if code := serve(s, http.MethodPost, "/fake/hook/abc"); code != http.StatusUnauthorized {
		t.Fatalf("disabled module's route not refused by auth: %d", code)
	}
	if s.isPublic(http.MethodPost, "/fake/hook/:id") {
		t.Fatal("disabled module widened the public route set")
	}
	s.Mount(nil) // nil is a no-op
}
