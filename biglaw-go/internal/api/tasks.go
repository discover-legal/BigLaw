// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/backend"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/gin-gonic/gin"
)

// ─── Tasks ────────────────────────────────────────────────────────────────────

func (s *Server) handleSubmitTask(c *gin.Context) {
	var body backend.SubmitTaskInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	task, err := s.as(c).SubmitTask(c.Request.Context(), body)
	reply(c, http.StatusAccepted, task, err)
}

func (s *Server) handleListTasks(c *gin.Context) {
	tasks, err := s.as(c).ListTasks(c.Request.Context())
	reply(c, http.StatusOK, tasks, err)
}

func (s *Server) handleGetTask(c *gin.Context) {
	task, err := s.as(c).GetTask(c.Request.Context(), c.Param("id"))
	reply(c, http.StatusOK, task, err)
}

func (s *Server) handleDeleteTask(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	err := s.orch.DeleteTask(c.Param("id"))
	if errors.Is(err, orchestrator.ErrTaskNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if errors.Is(err, orchestrator.ErrTaskActive) {
		c.JSON(http.StatusConflict, gin.H{"error": "running tasks cannot be deleted; wait for completion"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type assignBody struct {
	LawyerIDs []string `json:"lawyerIds"`
}

func (s *Server) handleAssignTask(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body assignBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	u := getUser(c)
	task := s.orch.AssignLawyers(c.Param("id"), body.LawyerIDs, u.ProfileID)
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
}

// handleTaskStream streams task progress events as Server-Sent Events.
func (s *Server) handleTaskStream(c *gin.Context) {
	taskID := c.Param("id")

	// Verify task exists and caller can see it.
	u := getUser(c)
	task := s.orch.GetTask(taskID)
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if !auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID && !auth.IsPartner(u) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ch := orchestrator.SubscribeProgress()
	defer orchestrator.UnsubscribeProgress(ch)

	ctx := c.Request.Context()
	flusher, hasFlusher := c.Writer.(http.Flusher)

	writeEvent := func(typ string, data interface{}) {
		raw, _ := json.Marshal(data)
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", typ, raw)
		if hasFlusher {
			flusher.Flush()
		}
	}

	// Send a connected confirmation immediately.
	writeEvent("connected", gin.H{"taskId": taskID})

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.TaskID != taskID {
				continue
			}
			writeEvent(ev.Type, ev.Data)
			if ev.Type == "complete" || ev.Type == "failed" {
				return
			}
		case <-time.After(30 * time.Second):
			// Heartbeat to keep the connection alive.
			fmt.Fprintf(c.Writer, ": heartbeat\n\n")
			if hasFlusher {
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleSubmitFromTemplate(c *gin.Context) {
	var body backend.SubmitFromTemplateInput
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	task, err := s.as(c).SubmitFromTemplate(c.Request.Context(), body)
	reply(c, http.StatusCreated, task, err)
}

func (s *Server) handleGetRound(c *gin.Context) {
	roundNum, _ := strconv.Atoi(c.Param("round")) // non-numeric → 0 → "round must be a positive integer"
	round, err := s.as(c).GetRound(c.Request.Context(), c.Param("id"), roundNum)
	reply(c, http.StatusOK, round, err)
}

func (s *Server) handleApproveGate(c *gin.Context) {
	var body struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&body) // the note is optional
	err := s.as(c).ApproveGate(c.Request.Context(), c.Param("id"), c.Param("gateId"), body.Note)
	reply(c, http.StatusOK, gin.H{"ok": true}, err)
}

func (s *Server) handleRejectGate(c *gin.Context) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	err := s.as(c).RejectGate(c.Request.Context(), c.Param("id"), c.Param("gateId"), body.Reason)
	reply(c, http.StatusOK, gin.H{"ok": true}, err)
}

// ─── Task cost ────────────────────────────────────────────────────────────────

func (s *Server) handleTaskCost(c *gin.Context) {
	u := getUser(c)
	taskID := c.Param("id")
	task := s.orch.GetTask(taskID)
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if !auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID && !auth.IsPartner(u) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}

	entries := s.costs.ForTask(taskID)
	if entries == nil {
		// Summarise(nil) aggregates the whole store — an empty task must
		// report zeros, not firm-wide totals.
		entries = []cost.CostEntry{}
	}
	summary := s.costs.Summarise(entries)
	c.JSON(http.StatusOK, gin.H{
		"taskId":  taskID,
		"summary": summary,
		"entries": entries,
	})
}
