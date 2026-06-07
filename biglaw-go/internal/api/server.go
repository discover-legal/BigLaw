// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// REST API server for BigLaw Go — wraps all subsystems behind a Gin router.
// Auth: when cfg.Auth.Enabled is false every request is treated as LocalPartner.
// When enabled the X-Profile-ID header is resolved against the ProfileStore.

package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const ctxUserKey = "user"

// Server holds all subsystem references and owns the Gin engine.
type Server struct {
	cfg       *config.Config
	orch      *orchestrator.Orchestrator
	profiles  *auth.ProfileStore
	clients   *clients.ClientStore
	time      *timekeeping.TimeStore
	knowledge *knowledge.Store
	registry  *agents.Registry
	costs     *cost.Store
	router    *gin.Engine
}

// New creates a Server, registers all routes, and returns it.
// Call Run to start listening.
func New(
	cfg *config.Config,
	orch *orchestrator.Orchestrator,
	profiles *auth.ProfileStore,
	clientStore *clients.ClientStore,
	timeStore *timekeeping.TimeStore,
	knowledgeStore *knowledge.Store,
	registry *agents.Registry,
	costStore *cost.Store,
) *Server {
	s := &Server{
		cfg:       cfg,
		orch:      orch,
		profiles:  profiles,
		clients:   clientStore,
		time:      timeStore,
		knowledge: knowledgeStore,
		registry:  registry,
		costs:     costStore,
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(s.authMiddleware())

	// ── Health ────────────────────────────────────────────────────────────
	r.GET("/health", s.handleHealth)
	r.GET("/me", s.handleMe)

	// ── Tasks ─────────────────────────────────────────────────────────────
	tasks := r.Group("/tasks")
	{
		tasks.POST("", s.handleSubmitTask)
		tasks.GET("", s.handleListTasks)
		tasks.POST("/from-template", s.handleSubmitFromTemplate)
		tasks.GET("/:id", s.handleGetTask)
		tasks.DELETE("/:id", s.handleDeleteTask)
		tasks.POST("/:id/assign", s.handleAssignTask)
		tasks.GET("/:id/stream", s.handleTaskStream)
		tasks.GET("/:id/cost", s.handleTaskCost)
		tasks.GET("/:taskId/rounds/:round", s.handleGetRound)
		tasks.POST("/:taskId/gates/:gateId/approve", s.handleApproveGate)
		tasks.POST("/:taskId/gates/:gateId/reject", s.handleRejectGate)
	}

	// ── Documents ─────────────────────────────────────────────────────────
	docs := r.Group("/documents")
	{
		docs.POST("", s.handleIngestDocument)
		docs.GET("/search", s.handleSearchDocuments)
	}

	// ── Agents ────────────────────────────────────────────────────────────
	r.GET("/agents", s.handleListAgents)

	// ── Templates ─────────────────────────────────────────────────────────
	r.GET("/templates", s.handleListTemplates)

	// ── Profiles ──────────────────────────────────────────────────────────
	profs := r.Group("/profiles")
	{
		profs.GET("", s.handleListProfiles)
		profs.POST("", s.handleCreateProfile)
		profs.GET("/:id", s.handleGetProfile)
		profs.PATCH("/:id", s.handleUpdateProfile)
		profs.DELETE("/:id", s.handleDeleteProfile)
	}

	// ── Clients ───────────────────────────────────────────────────────────
	cls := r.Group("/clients")
	{
		cls.GET("", s.handleListClients)
		cls.POST("", s.handleCreateClient)
		cls.POST("/check-conflict", s.handleCheckConflict)
		cls.PATCH("/:id", s.handleUpdateClient)
		cls.DELETE("/:id", s.handleDeleteClient)
		cls.POST("/:id/matters", s.handleAddMatter)
		cls.DELETE("/:id/matters/:num", s.handleRemoveMatter)
	}

	// ── Time entries ──────────────────────────────────────────────────────
	r.GET("/time-entries", s.handleListTimeEntries)

	// ── Cost ──────────────────────────────────────────────────────────────
	r.GET("/cost/summary", s.handleCostSummary)

	// ── Audit ─────────────────────────────────────────────────────────────
	r.GET("/audit", s.handleAudit)
	r.GET("/audit/stream", s.handleAuditStream)

	s.router = r
	return s
}

// Run starts the HTTP server on addr (e.g. ":3101").
func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

// ─── Middleware ───────────────────────────────────────────────────────────────

func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.cfg.Auth.Enabled {
			u := auth.LocalPartner
			c.Set(ctxUserKey, &u)
			c.Next()
			return
		}

		profileID := c.GetHeader("X-Profile-ID")
		if profileID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "X-Profile-ID header required"})
			c.Abort()
			return
		}

		p := s.profiles.Get(profileID)
		if p == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "profile not found"})
			c.Abort()
			return
		}

		u := &types.SessionUser{
			ProfileID: p.ID,
			Name:      p.Name,
			Email:     p.Email,
			Role:      p.Role,
			Mode:      auth.ResolveMode(p.Role, p.Mode),
		}
		c.Set(ctxUserKey, u)
		c.Next()
	}
}

// ─── Auth helpers ─────────────────────────────────────────────────────────────

func getUser(c *gin.Context) *types.SessionUser {
	if v, ok := c.Get(ctxUserKey); ok {
		if u, ok := v.(*types.SessionUser); ok {
			return u
		}
	}
	return nil
}

// requirePartner writes 403 and returns false if the caller is not a partner.
func requirePartner(c *gin.Context) bool {
	u := getUser(c)
	if !auth.IsPartner(u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "partner access required"})
		return false
	}
	return true
}

// ─── Health / Me ──────────────────────────────────────────────────────────────

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleMe(c *gin.Context) {
	u := getUser(c)
	c.JSON(http.StatusOK, gin.H{
		"user":        u,
		"authEnabled": s.cfg.Auth.Enabled,
	})
}

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
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, task)
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
	deleted := s.orch.DeleteTask(c.Param("id"))
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
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
	taskID := c.Param("taskId")
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

	taskID := c.Param("taskId")
	gateID := c.Param("gateId")

	if err := s.orch.ApproveGate(taskID, gateID, body.Note, u.ProfileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"approved": true})
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

	taskID := c.Param("taskId")
	gateID := c.Param("gateId")

	if err := s.orch.RejectGate(taskID, gateID, body.Reason, u.ProfileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rejected": true})
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
	summary := s.costs.Summarise(entries)
	c.JSON(http.StatusOK, gin.H{
		"taskId":  taskID,
		"summary": summary,
		"entries": entries,
	})
}

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

	result, err := s.knowledge.Ingest(doc)
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

// ─── Profiles ─────────────────────────────────────────────────────────────────

func (s *Server) handleListProfiles(c *gin.Context) {
	list := s.profiles.List()
	if list == nil {
		list = []types.LawyerProfile{}
	}
	c.JSON(http.StatusOK, list)
}

func (s *Server) handleGetProfile(c *gin.Context) {
	p := s.profiles.Get(c.Param("id"))
	if p == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

type createProfileBody struct {
	Name          string   `json:"name"`
	Email         string   `json:"email"`
	Role          string   `json:"role"`
	Title         string   `json:"title"`
	Color         string   `json:"color"`
	PracticeAreas []string `json:"practiceAreas"`
	Bio           string   `json:"bio"`
	Mode          string   `json:"mode"`
}

func (s *Server) handleCreateProfile(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body createProfileBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	p, err := s.profiles.Create(auth.CreateProfileInput{
		Name:          body.Name,
		Email:         body.Email,
		Role:          body.Role,
		Title:         body.Title,
		Color:         body.Color,
		PracticeAreas: body.PracticeAreas,
		Bio:           body.Bio,
		Mode:          body.Mode,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (s *Server) handleUpdateProfile(c *gin.Context) {
	u := getUser(c)
	targetID := c.Param("id")

	// Partners may update anyone; lawyers may only update their own profile
	// but cannot change their role.
	if !auth.IsPartner(u) && u.ProfileID != targetID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot update another profile"})
		return
	}

	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	// Non-partners cannot change their own role.
	if !auth.IsPartner(u) {
		delete(patch, "role")
	}

	p, err := s.profiles.Update(targetID, patch)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) handleDeleteProfile(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	deleted, err := s.profiles.Remove(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

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
	c.JSON(http.StatusOK, gin.H{"deleted": true})
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
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

type checkConflictBody struct {
	Name string `json:"name"`
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
	if body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	result := s.clients.CheckConflict(body.Name)
	c.JSON(http.StatusOK, result)
}

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

	taskID := c.Query("taskId")
	limitStr := c.DefaultQuery("limit", "200")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	// Non-partners may only query audit entries for tasks they can see.
	// We apply a simple taskID restriction: if not partner and no taskID provided,
	// return only entries that would be visible (via task access). For simplicity,
	// non-partners must supply a taskId.
	if !auth.IsPartner(u) && taskID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "taskId query parameter required for non-partner access"})
		return
	}

	if !auth.IsPartner(u) && taskID != "" {
		// Verify the caller can see this task.
		task := s.orch.GetTask(taskID)
		if task == nil || (!auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
	}

	entries := audit.Default.ReadRecent(taskID, limit)
	if entries == nil {
		entries = []audit.AuditEntry{}
	}
	c.JSON(http.StatusOK, entries)
}

// handleAuditStream streams live audit events as Server-Sent Events.
func (s *Server) handleAuditStream(c *gin.Context) {
	u := getUser(c)
	taskID := c.Query("taskId")

	if !auth.IsPartner(u) && taskID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "taskId query parameter required for non-partner access"})
		return
	}

	if !auth.IsPartner(u) && taskID != "" {
		task := s.orch.GetTask(taskID)
		if task == nil || (!auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID) {
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
			return
		}
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
			writeSSE(entry)
		case <-time.After(30 * time.Second):
			io.WriteString(c.Writer, ": heartbeat\n\n")
			if hasFlusher {
				flusher.Flush()
			}
		}
	}
}
