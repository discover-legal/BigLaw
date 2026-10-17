// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/modules"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// handleListModules reports every registered feature module, its resolved
// state, and what decided it (config default or BIGLAW_MODULE_* override).
func (s *Server) handleListModules(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"modules": modules.Default.List()})
}

// Mount attaches an optional HTTP module (see modules.HTTPModule). It is a
// no-op when m is nil or the module is disabled in the registry, so a
// disabled module's routes do not exist. Call before Start.
func (s *Server) Mount(m modules.HTTPModule) {
	if m == nil || !modules.Default.Enabled(m.ModuleName()) {
		return
	}
	s.publicRoutes = append(s.publicRoutes, m.PublicRoute)
	m.Mount(s.router, moduleHost{s})
}

// isPublic is the full public-route set: the core's fixed set plus each
// mounted module's self-authenticating routes. route is the matched route
// pattern (c.FullPath), as for isPublicRoute.
func (s *Server) isPublic(method, route string) bool {
	if isPublicRoute(method, route) {
		return true
	}
	for _, pub := range s.publicRoutes {
		if pub(method, route) {
			return true
		}
	}
	return false
}

// moduleHost lends modules request identity without exposing the Server.
type moduleHost struct{ s *Server }

func (h moduleHost) User(c *gin.Context) *types.SessionUser  { return getUser(c) }
func (h moduleHost) Identity(c *gin.Context) context.Context { return reqIdentity(c) }
func (h moduleHost) ProfileName(profileID string) string {
	if profileID == "" || h.s.profiles == nil {
		return ""
	}
	if p := h.s.profiles.Get(profileID); p != nil {
		return p.Name
	}
	return ""
}
