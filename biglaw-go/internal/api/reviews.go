// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Review routes — a completed tabular_review matrix as JSON. One route:
// GET /reviews/:id, the JSON sibling of the CSV export in content.go. It
// returns the persisted ReviewRecord verbatim (columns, rows with per-cell
// flags/reasoning/citations, flagTally, citationTally, legend) so the
// workbench due-diligence grid can render flags and verified-citation pills.
// Resolution goes through the shared lookup (in-process cache, then the
// review repository) under the caller's identity, so Postgres RLS applies;
// unknown ids are a JSON 404.

package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/tools"
)

// registerReviewRoutes adds the tabular-review JSON route. The ":id" param
// name matches /reviews/:id/table.csv (content.go) — Gin requires one name
// per path position.
func (s *Server) registerReviewRoutes(r *gin.Engine) {
	r.GET("/reviews/:id", s.handleGetReview)
}

// handleGetReview returns the full review matrix for a reviewId. Same lookup
// and access model as handleReviewTableCSV: LookupReview under reqIdentity,
// 404 JSON for an unknown id.
func (s *Server) handleGetReview(c *gin.Context) {
	rev, found := tools.LookupReview(reqIdentity(c), s.reviews, c.Param("id"))
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "review not found"})
		return
	}
	c.JSON(http.StatusOK, rev)
}
