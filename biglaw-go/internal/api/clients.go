// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/graph"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Clients ──────────────────────────────────────────────────────────────────

func (s *Server) handleListClients(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	list := s.clients.List()
	if list == nil {
		list = []types.Client{}
	}
	c.JSON(http.StatusOK, list)
}

type createClientBody struct {
	Name         string   `json:"name"`
	ClientNumber string   `json:"clientNumber"`
	Adversaries  []string `json:"adversaries"`
	Notes        string   `json:"notes"`
}

func (s *Server) handleCreateClient(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body createClientBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	client, err := s.clients.Create(body.Name, body.ClientNumber, body.Adversaries, body.Notes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	go s.syncGraph()
	c.JSON(http.StatusCreated, client)
}

func (s *Server) handleUpdateClient(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	client, err := s.clients.Update(c.Param("id"), patch)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	go s.syncGraph()
	c.JSON(http.StatusOK, client)
}

func (s *Server) handleDeleteClient(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	deleted, err := s.clients.Remove(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return
	}
	go s.syncGraph()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type addMatterBody struct {
	MatterNumber string `json:"matterNumber"`
	Description  string `json:"description"`
	PracticeArea string `json:"practiceArea"`
}

func (s *Server) handleAddMatter(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body addMatterBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.MatterNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber is required"})
		return
	}

	matter, err := s.clients.AddMatter(c.Param("id"), body.MatterNumber, body.Description, body.PracticeArea)
	if err != nil {
		if err.Error() == "client not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	go s.syncGraph()
	c.JSON(http.StatusCreated, matter)
}

func (s *Server) handleRemoveMatter(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	removed, err := s.clients.RemoveMatter(c.Param("id"), c.Param("num"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if !removed {
		c.JSON(http.StatusNotFound, gin.H{"error": "matter not found"})
		return
	}
	go s.syncGraph()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type checkConflictBody struct {
	Name        string   `json:"name"`
	Adversaries []string `json:"adversaries"`
}

func (s *Server) handleCheckConflict(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body checkConflictBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > 500 {
		name = strutil.Truncate(name, 500)
	}
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	advs := body.Adversaries
	if len(advs) > 200 {
		advs = advs[:200]
	}
	result := s.clients.CheckConflict(name, advs)
	c.JSON(http.StatusOK, result)
}

// handleClientGraphConflicts returns inference-derived conflicts for an
// existing client from the TypeDB sidecar (direct adversity, subsidiary
// chains). 503 if the sidecar or TypeDB is down — never silently empty.
func (s *Server) handleClientGraphConflicts(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	client := s.clients.Get(c.Param("id"))
	if client == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "client not found"})
		return
	}
	reports, err := s.graph.CheckClient(client.ID)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conflict graph unavailable: " + err.Error()})
		return
	}
	if reports == nil {
		reports = []types.ConflictReport{}
	}
	c.JSON(http.StatusOK, gin.H{"clientId": client.ID, "conflicts": reports})
}

// syncGraph pushes the full client/matter roster to the TypeDB sidecar.
// Best-effort: failure is logged, the substring conflict check still works.
// The sidecar creates its socket asynchronously at container start, so the
// initial ping retries with backoff instead of giving up on the first miss.
func (s *Server) syncGraph() {
	var err error
	for attempt, wait := 0, time.Second; attempt < 6; attempt, wait = attempt+1, wait*2 {
		if err = s.graph.Ping(); err == nil {
			break
		}
		time.Sleep(wait)
	}
	if err != nil {
		slog.Warn("conflict graph: sidecar unreachable, graph conflicts disabled", "err", err)
		return
	}
	roster := s.clients.List()
	input := graph.SyncInput{}
	for _, cl := range roster {
		sc := graph.SyncClient{
			ID:          cl.ID,
			Name:        cl.Name,
			Adversaries: cl.Adversaries,
		}
		for _, m := range cl.Matters {
			sc.Matters = append(sc.Matters, graph.MatterRef{
				MatterNumber: m.MatterNumber,
				PracticeArea: m.PracticeArea,
			})
			input.Matters = append(input.Matters, graph.SyncMatter{
				MatterNumber: m.MatterNumber,
				PracticeArea: m.PracticeArea,
				Status:       "active",
			})
		}
		input.Clients = append(input.Clients, sc)
	}
	if err := s.graph.Sync(input); err != nil {
		slog.Warn("conflict graph: sync failed", "err", err)
		return
	}
	slog.Info("conflict graph: roster synced", "clients", len(input.Clients), "matters", len(input.Matters))
}
