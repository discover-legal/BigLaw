// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package modules

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// HTTPModule is an optional subsystem that brings its own HTTP routes. The
// core API never imports a module's package: the composition root
// (cmd/biglaw) builds the module and hands it to the API server's Mount,
// which mounts it only when the registry has it enabled. A disabled module
// therefore has no routes at all (404), and the core builds, runs and tests
// without it.
type HTTPModule interface {
	// ModuleName is the registry name (BIGLAW_MODULE_<NAME> override).
	ModuleName() string
	// PublicRoute reports the module's self-authenticating routes (e.g.
	// HMAC-signed webhooks). route is the registered gin route pattern the
	// request matched (c.FullPath(), e.g. "/intake/submissions/:id"; empty
	// when nothing matched), never the request path, so a public exemption
	// cannot be borrowed by another route. The core auth middleware lets
	// these through without a session/bearer credential; the module's
	// handlers MUST authenticate every such request themselves.
	PublicRoute(method, route string) bool
	// Mount registers the module's routes.
	Mount(r gin.IRouter, host Host)
}

// Host is everything the core API lends a module: who is calling, under
// which store identity, and display names. Modules reach the rest of the
// platform through the public seams they were constructed with (client
// roster, knowledge ingest, task submission, audit) — never through the
// API package.
type Host interface {
	// User is the authenticated firm principal, nil when anonymous.
	User(c *gin.Context) *types.SessionUser
	// Identity is the request context carrying the store (RLS) identity.
	Identity(c *gin.Context) context.Context
	// ProfileName resolves a lawyer profile id to a display name ("" if unknown).
	ProfileName(profileID string) string
}
