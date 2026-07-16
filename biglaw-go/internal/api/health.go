// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Health / Me ──────────────────────────────────────────────────────────────

// handleHealth mirrors the TS backend's /health contract — the UI reads
// version, uptime, and the per-status task counts.
func (s *Server) handleHealth(c *gin.Context) {
	var queued, running, awaitingGate, complete int
	all := s.orch.ListTasks()
	for _, t := range all {
		switch t.Status {
		case types.TaskStatusQueued, types.TaskStatusPending:
			queued++
		case types.TaskStatusRunning:
			running++
		case types.TaskStatusAwaitingGate:
			awaitingGate++
		case types.TaskStatusComplete:
			complete++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"ok":      true,
		"version": "0.1.0",
		"uptime":  int(time.Since(s.started).Seconds()),
		"tasks": gin.H{
			"total":         len(all),
			"queued":        queued,
			"running":       running,
			"awaiting_gate": awaitingGate,
			"complete":      complete,
		},
	})
}

func (s *Server) handleMe(c *gin.Context) {
	u := getUser(c)
	mode := types.ModeLite
	if u != nil && u.Mode != "" {
		mode = u.Mode
	}
	c.JSON(http.StatusOK, gin.H{
		"user":        u,
		"authEnabled": s.cfg.Auth.Enabled,
		// Mode metadata the UI needs to theme itself and gate features.
		"mode":         mode,
		"modeColor":    types.ModeColors[mode],
		"capabilities": types.ModeCapabilitySet[mode],
	})
}

// ─── Admin settings ───────────────────────────────────────────────────────────
// Both GET and PUT are partner-only: GET exposes the DocuSeal URL and enabled
// state; PUT can redirect DocuSeal requests (SSRF) or weaken debate/gate
// settings.

func (s *Server) handleGetSettings(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	c.JSON(http.StatusOK, s.orch.Settings().Get())
}

func (s *Server) handleUpdateSettings(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var patch settings.Patch
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	updated, err := s.orch.Settings().Update(patch)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	fields := []string{}
	if patch.Presentation != nil {
		fields = append(fields, "presentation")
	}
	if patch.DyTopo != nil {
		fields = append(fields, "dytopo")
	}
	if patch.Debate != nil {
		fields = append(fields, "debate")
	}
	if patch.DocuSeal != nil {
		fields = append(fields, "docuseal")
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "settings.updated",
		ActorID: getUser(c).ProfileID,
		Data:    map[string]interface{}{"fields": fields},
	})
	c.JSON(http.StatusOK, updated)
}
