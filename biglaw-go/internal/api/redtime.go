// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Redtime routes — the per-clause redline timeline of a document lineage
// (internal/redtime). One route: GET /documents/:id/timeline, where :id is a
// lineage ID (forgivingly, also a version ID, which resolves to its lineage).
// Resolution runs under the caller's identity so Postgres RLS applies, like
// the review routes.

package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/redtime"
	"github.com/discover-legal/biglaw-go/internal/store"
)

// registerRedtimeRoutes adds the document-version timeline route. The ":id"
// param name matches the /documents/attachments tree's position constraint
// (Gin allows static and param siblings under /documents since v1.7).
func (s *Server) registerRedtimeRoutes(r *gin.Engine) {
	r.GET("/documents/:id/timeline", s.handleDocumentTimeline)
}

// handleDocumentTimeline builds and returns the Timeline JSON for a lineage.
// Query params matterNumber / clientNumber / profileId / practiceArea scope
// the playbook cascade for drift judgment. 404 JSON for an unknown id; 503
// when the configured store has no version support.
func (s *Server) handleDocumentTimeline(c *gin.Context) {
	// The server holds its durable store as a ReviewRepository (the field
	// predates Redtime); the concrete sqlite/postgres/memory store implements
	// VersionRepository too, so a type assertion recovers it without changing
	// the api.New signature while concurrent work is in flight.
	// TODO(post-merge): rename the reviews field to a combined interface.
	vrepo, _ := s.reviews.(store.VersionRepository)
	if vrepo == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": redtime.ErrUnavailable.Error()})
		return
	}
	ctx := reqIdentity(c)

	id := c.Param("id")
	lineageID := id
	if vs, err := vrepo.ListLineage(ctx, id); err == nil && len(vs) == 0 {
		// Not a lineage ID — maybe a version ID.
		if v, found, gerr := vrepo.GetVersion(ctx, id); gerr == nil && found {
			lineageID = v.LineageID
		}
	}

	opts := redtime.OptsFromConfig(s.cfg, s.provReg, playbook.ResolveOpts{
		MatterNumber: c.Query("matterNumber"),
		ClientID:     c.Query("clientNumber"),
		ProfileID:    c.Query("profileId"),
		PracticeArea: c.Query("practiceArea"),
	}, "")

	tl, err := redtime.BuildTimeline(ctx, vrepo, lineageID, opts)
	if errors.Is(err, redtime.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "document version lineage not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "timeline build failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, tl)
}
