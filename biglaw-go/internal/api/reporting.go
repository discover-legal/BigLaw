// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/backend"
	"github.com/discover-legal/biglaw-go/internal/jsonsafe"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/gin-gonic/gin"
)

// ─── Time entries ─────────────────────────────────────────────────────────────

func (s *Server) handleListTimeEntries(c *gin.Context) {
	// profileId is honoured for partners only; the backend limits everyone
	// else to their own time plus AI time on work they requested.
	// Unparseable bounds are ignored.
	filter := timekeeping.TimeFilter{
		ProfileID:    c.Query("profileId"),
		TaskID:       c.Query("taskId"),
		MatterNumber: c.Query("matterNumber"),
	}
	if t, err := time.Parse(time.RFC3339, c.Query("from")); err == nil {
		filter.From = &t
	}
	if t, err := time.Parse(time.RFC3339, c.Query("to")); err == nil {
		filter.To = &t
	}
	entries, err := s.as(c).ListTimeEntries(c.Request.Context(), filter)
	reply(c, http.StatusOK, entries, err)
}

// ─── Cost ─────────────────────────────────────────────────────────────────────

func (s *Server) handleCostSummary(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	summary := s.costs.Summarise(nil)
	c.JSON(http.StatusOK, summary)
}

// ─── Audit ────────────────────────────────────────────────────────────────────

func (s *Server) handleAudit(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit")) // absent/invalid → default 200, max 1000
	entries, err := s.as(c).ReadAudit(c.Request.Context(), backend.AuditQuery{
		TaskID:  c.Query("taskId"),
		ActorID: c.Query("actorId"), // lawyers are locked to their own actions
		Event:   c.Query("event"),
		Limit:   limit,
	})
	reply(c, http.StatusOK, entries, err)
}

// handleAuditStream streams live audit events as Server-Sent Events.
func (s *Server) handleAuditStream(c *gin.Context) {
	u := getUser(c)
	taskID := c.Query("taskId")
	actorID := c.Query("actorId")

	// Lawyers stream only their own actions; partners may follow anyone's.
	if !auth.IsPartner(u) {
		actorID = u.ProfileID
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ch, cancel := audit.Default.Subscribe()
	defer cancel()

	ctx := c.Request.Context()
	flusher, hasFlusher := c.Writer.(http.Flusher)

	writeSSE := func(entry audit.AuditEntry) {
		raw, _ := jsonsafe.Marshal(entry)
		io.WriteString(c.Writer, "data: "+string(raw)+"\n\n")
		if hasFlusher {
			flusher.Flush()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			if taskID != "" && entry.TaskID != taskID {
				continue
			}
			if actorID != "" && entry.ActorID != actorID {
				continue
			}
			writeSSE(entry)
		case <-time.After(30 * time.Second):
			io.WriteString(c.Writer, ": heartbeat\n\n")
			if hasFlusher {
				flusher.Flush()
			}
		}
	}
}
