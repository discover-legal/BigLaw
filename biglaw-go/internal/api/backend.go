// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/backend"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// The REST handlers for the operations the MCP server also exposes (tasks,
// gates, templates, rounds, documents, search, agents, audit, plugins, time
// entries) are thin transports over backend.Local.As(principal): request
// parsing in, status mapping out. The operations and their access rules live
// in one place, so the REST API, the in-process MCP server and the MCP thin
// client (which calls these very routes) cannot drift apart.

// Backend is the owner-side operations this server exposes; the in-process
// MCP server takes its principal's view of the same instance.
func (s *Server) Backend() *backend.Local { return s.svc }

// AttachPlugins hands the server the plugin registry loaded at startup
// (GET /plugins and the MCP list_plugins tool report it).
func (s *Server) AttachPlugins(reg *adapters.Registry) { s.svc.Plugins = reg }

// as is the backend as seen by the request's principal (anonymous when the
// route is public and nobody is signed in).
func (s *Server) as(c *gin.Context) backend.Backend {
	if u := getUser(c); u != nil {
		return s.svc.As(*u)
	}
	return s.svc.As(types.SessionUser{})
}

// statusFor maps a backend error kind to its HTTP status — the one place
// that does (backend.Remote maps them back).
func statusFor(err error) int {
	switch {
	case errors.Is(err, backend.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, backend.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, backend.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, backend.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, backend.ErrUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// backendError writes err as {"error": message} with its mapped status.
func backendError(c *gin.Context, err error) {
	status := statusFor(err)
	if status == http.StatusServiceUnavailable {
		c.Header("Retry-After", "30")
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

// reply writes v with status, or the backend error.
func reply[T any](c *gin.Context, status int, v T, err error) {
	if err != nil {
		backendError(c, err)
		return
	}
	c.JSON(status, v)
}
