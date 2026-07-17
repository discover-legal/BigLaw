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
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Tasks ────────────────────────────────────────────────────────────────────

type submitTaskBody struct {
	Description  string             `json:"description"`
	WorkflowType types.WorkflowType `json:"workflowType"`
	Jurisdiction string             `json:"jurisdiction"`
	ClientNumber string             `json:"clientNumber"`
	MatterNumber string             `json:"matterNumber"`
	DocumentIDs  []string           `json:"documentIds"`
}

func (s *Server) handleSubmitTask(c *gin.Context) {
	u := getUser(c)
	var body submitTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.Description == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "description is required"})
		return
	}
	if body.WorkflowType == "" {
		body.WorkflowType = types.WorkflowRoundtable
	}

	task, err := s.orch.SubmitTask(orchestrator.SubmitParams{
		Description:        body.Description,
		WorkflowType:       body.WorkflowType,
		DocumentIDs:        body.DocumentIDs,
		ClientNumber:       body.ClientNumber,
		MatterNumber:       body.MatterNumber,
		Jurisdiction:       body.Jurisdiction,
		CreatedByProfileID: u.ProfileID,
	})
	if err != nil {
		if errors.Is(err, orchestrator.ErrTaskQueueFull) {
			c.Header("Retry-After", "30")
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, task)
}

func (s *Server) handleListTasks(c *gin.Context) {
	u := getUser(c)
	all := s.orch.ListTasks()

	if auth.IsPartner(u) {
		c.JSON(http.StatusOK, all)
		return
	}

	// Non-partner: only tasks assigned to this profile or created by them.
	var filtered []*types.Task
	for _, t := range all {
		if auth.CanViewTask(u, t.AssignedLawyerIDs) || t.CreatedByProfileID == u.ProfileID {
			filtered = append(filtered, t)
		}
	}
	if filtered == nil {
		filtered = []*types.Task{}
	}
	c.JSON(http.StatusOK, filtered)
}

func (s *Server) handleGetTask(c *gin.Context) {
	u := getUser(c)
	task := s.orch.GetTask(c.Param("id"))
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if !auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID && !auth.IsPartner(u) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, task)
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

type submitFromTemplateBody struct {
	TemplateID    string            `json:"templateId"`
	Substitutions map[string]string `json:"substitutions"`
	DocumentIDs   []string          `json:"documentIds"`
}

func (s *Server) handleSubmitFromTemplate(c *gin.Context) {
	u := getUser(c)
	var body submitFromTemplateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.TemplateID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "templateId is required"})
		return
	}

	task, err := s.orch.SubmitFromTemplate(
		body.TemplateID,
		body.Substitutions,
		body.DocumentIDs,
		orchestrator.SubmitParams{CreatedByProfileID: u.ProfileID},
	)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, task)
}

func (s *Server) handleGetRound(c *gin.Context) {
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

	roundStr := c.Param("round")
	roundNum, err := strconv.Atoi(roundStr)
	if err != nil || roundNum < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "round must be a positive integer"})
		return
	}

	for _, r := range task.Rounds {
		if r.Goal.Round == roundNum {
			c.JSON(http.StatusOK, r)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "round not found"})
}

type approveGateBody struct {
	Note string `json:"note"`
}

func (s *Server) handleApproveGate(c *gin.Context) {
	u := getUser(c)
	var body approveGateBody
	// Note is optional — ignore bind error.
	c.ShouldBindJSON(&body)

	taskID := c.Param("id")
	gateID := c.Param("gateId")
	if !s.canReviewGate(u, taskID, gateID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "gate not found"})
		return
	}

	if err := s.orch.ApproveGate(taskID, gateID, body.Note, u.ProfileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type rejectGateBody struct {
	Reason string `json:"reason"`
}

func (s *Server) handleRejectGate(c *gin.Context) {
	u := getUser(c)
	var body rejectGateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason is required"})
		return
	}

	taskID := c.Param("id")
	gateID := c.Param("gateId")
	if !s.canReviewGate(u, taskID, gateID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "gate not found"})
		return
	}

	if err := s.orch.RejectGate(taskID, gateID, body.Reason, u.ProfileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) canReviewGate(u *types.SessionUser, taskID, gateID string) bool {
	task := s.orch.GetTask(taskID)
	if task == nil || (!auth.IsPartner(u) && task.CreatedByProfileID != u.ProfileID && !auth.CanViewTask(u, task.AssignedLawyerIDs)) {
		return false
	}
	for _, gate := range task.PendingGates {
		if gate.ID == gateID && gate.Status == "pending" {
			return true
		}
	}
	return false
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
