// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Documents ────────────────────────────────────────────────────────────────

type ingestDocBody struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	OwnerID string `json:"ownerID"`
}

func (s *Server) handleIngestDocument(c *gin.Context) {
	u := getUser(c)
	var body ingestDocBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	if body.Content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "content is required"})
		return
	}

	ownerID := body.OwnerID
	if ownerID == "" {
		ownerID = u.ProfileID
	}

	doc := types.Document{
		Title:      body.Title,
		Content:    body.Content,
		OwnerID:    ownerID,
		IngestedAt: time.Now(),
	}

	result, err := s.knowledge.Ingest(reqIdentity(c), doc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ingest failed: " + err.Error()})
		return
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "document.ingested",
		ActorID: u.ProfileID,
		Data:    map[string]interface{}{"docId": result.ID, "title": result.Title},
	})

	c.JSON(http.StatusCreated, result)
}

func (s *Server) handleSearchDocuments(c *gin.Context) {
	u := getUser(c)
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q parameter is required"})
		return
	}

	topKStr := c.DefaultQuery("topK", "5")
	topK, err := strconv.Atoi(topKStr)
	if err != nil || topK < 1 {
		topK = 5
	}
	if topK > 50 {
		topK = 50
	}

	// Partners see all documents; lawyers see only their own.
	ownerFilter := ""
	if !auth.IsPartner(u) {
		ownerFilter = u.ProfileID
	}

	results, err := s.knowledge.Search(q, knowledge.SearchOpts{
		OwnerID: ownerFilter,
		TopK:    topK,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed: " + err.Error()})
		return
	}
	if results == nil {
		results = []types.SearchResult{}
	}
	c.JSON(http.StatusOK, results)
}

// ─── Agents ───────────────────────────────────────────────────────────────────

func (s *Server) handleListAgents(c *gin.Context) {
	all := s.registry.ListAll()
	if all == nil {
		all = []types.AgentDefinition{}
	}
	c.JSON(http.StatusOK, all)
}

// ─── Templates ────────────────────────────────────────────────────────────────

func (s *Server) handleListTemplates(c *gin.Context) {
	list := s.orch.ListTemplates()
	if list == nil {
		list = []types.TaskTemplate{}
	}
	c.JSON(http.StatusOK, list)
}
