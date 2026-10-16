// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"strconv"

	"github.com/discover-legal/biglaw-go/internal/backend"
	"github.com/gin-gonic/gin"
)

// ─── Documents ────────────────────────────────────────────────────────────────

func (s *Server) handleIngestDocument(c *gin.Context) {
	var body backend.IngestInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	doc, err := s.as(c).IngestDocument(c.Request.Context(), body)
	reply(c, http.StatusCreated, doc, err)
}

func (s *Server) handleSearchDocuments(c *gin.Context) {
	topK, _ := strconv.Atoi(c.Query("topK")) // absent/invalid → default
	results, err := s.as(c).SearchKnowledge(c.Request.Context(), c.Query("q"), topK)
	reply(c, http.StatusOK, results, err)
}

// ─── Agents ───────────────────────────────────────────────────────────────────

func (s *Server) handleListAgents(c *gin.Context) {
	agents, err := s.as(c).ListAgents(c.Request.Context())
	reply(c, http.StatusOK, agents, err)
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (s *Server) handleListTemplates(c *gin.Context) {
	list, err := s.as(c).ListTemplates(c.Request.Context())
	reply(c, http.StatusOK, list, err)
}
