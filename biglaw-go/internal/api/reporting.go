// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Time entries ─────────────────────────────────────────────────────────────

func (s *Server) handleListTimeEntries(c *gin.Context) {
	u := getUser(c)

	filter := timekeeping.TimeFilter{}

	// Partners may filter by any profileId; lawyers are locked to their own.
	if auth.IsPartner(u) {
		filter.ProfileID = c.Query("profileId")
	} else {
		filter.ProfileID = u.ProfileID
	}

	filter.TaskID = c.Query("taskId")
	filter.MatterNumber = c.Query("matterNumber")

	if fromStr := c.Query("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			filter.From = &t
		}
	}
	if toStr := c.Query("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			filter.To = &t
		}
	}

	entries := s.time.List(filter)
	if entries == nil {
		entries = []types.TimeEntry{}
	}
	c.JSON(http.StatusOK, entries)
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
	u := getUser(c)

	limit, err := strconv.Atoi(c.DefaultQuery("limit", "200"))
	if err != nil || limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	f := audit.Filter{
		TaskID:  c.Query("taskId"),
		ActorID: c.Query("actorId"),
		Event:   c.Query("event"),
		Limit:   limit,
	}
	// Lawyers see only their own actions; partners may browse anyone's.
	if !auth.IsPartner(u) {
		f.ActorID = u.ProfileID
	}

	entries := audit.Default.ReadFiltered(f)
	if entries == nil {
		entries = []audit.AuditEntry{}
	}
	c.JSON(http.StatusOK, entries)
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
		raw, _ := json.Marshal(entry)
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
