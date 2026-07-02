// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// REST API server for BigLaw Go — wraps all subsystems behind a Gin router.
// Auth: when cfg.Auth.Enabled is false every request is treated as LocalPartner.
// When enabled the X-Profile-ID header is resolved against the ProfileStore.

package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/blob"
	"github.com/discover-legal/biglaw-go/internal/budget"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/dockets"
	"github.com/discover-legal/biglaw-go/internal/graph"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/regulatory"
	"github.com/discover-legal/biglaw-go/internal/settings"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const ctxUserKey = "user"

// Server holds all subsystem references and owns the Gin engine.
type Server struct {
	cfg        *config.Config
	orch       *orchestrator.Orchestrator
	provReg    *providers.Registry
	profiles   *auth.ProfileStore
	clients    *clients.ClientStore
	time       *timekeeping.TimeStore
	knowledge  *knowledge.Store
	registry   *agents.Registry
	costs      *cost.Store
	reviews    store.ReviewRepository // durable tabular-review matrices; nil if unavailable
	blobs      blob.Store             // attachment bytes (disk now, object-store later); nil if unavailable
	graph      *graph.Client
	budget     *budget.Monitor     // read-only burn for bot commands
	dockets    *dockets.Monitor    // set by AttachDockets; nil when disabled
	regulatory *regulatory.Monitor // set by AttachRegulatory; nil when disabled
	lpm        *lpm.Service        // set by AttachLPM; nil when LPM is disabled
	router     *gin.Engine
	started    time.Time
}

// New creates a Server, registers all routes, and returns it.
// Call Run to start listening.
func New(
	cfg *config.Config,
	orch *orchestrator.Orchestrator,
	provReg *providers.Registry,
	profiles *auth.ProfileStore,
	clientStore *clients.ClientStore,
	timeStore *timekeeping.TimeStore,
	knowledgeStore *knowledge.Store,
	registry *agents.Registry,
	costStore *cost.Store,
	reviewRepo store.ReviewRepository,
) *Server {
	s := &Server{
		cfg:       cfg,
		orch:      orch,
		provReg:   provReg,
		profiles:  profiles,
		clients:   clientStore,
		time:      timeStore,
		knowledge: knowledgeStore,
		registry:  registry,
		costs:     costStore,
		reviews:   reviewRepo,
		blobs:     newBlobStore(cfg),
		graph:     graph.New(),
		started:   time.Now(),
	}
	s.budget = budget.NewMonitor(apiBudgetTime{timeStore}, apiBudgetClients{clientStore}, nil)

	// Push the current roster into the TypeDB conflict graph if the sidecar
	// is up. Best-effort: the substring conflict check works without it.
	go s.syncGraph()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(s.authMiddleware())

	// ── Health ────────────────────────────────────────────────────────────
	r.GET("/health", s.handleHealth)
	r.GET("/me", s.handleMe)

	// ── Admin settings ────────────────────────────────────────────────────
	r.GET("/settings", s.handleGetSettings)
	r.PUT("/settings", s.handleUpdateSettings)

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
		// Gin requires one param name per path position: this group already
		// uses ":id" for the task segment.
		tasks.GET("/:id/rounds/:round", s.handleGetRound)
		tasks.POST("/:id/gates/:gateId/approve", s.handleApproveGate)
		tasks.POST("/:id/gates/:gateId/reject", s.handleRejectGate)
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
		cls.GET("/:id/conflicts", s.handleClientGraphConflicts)
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

	// ── Domain route groups (one file each under internal/api/) ──────────
	s.registerBillingRoutes(r) // pre-bills, invoices, OCG, time exports
	s.registerMattersRoutes(r) // budgets, deadlines, matter health, analytics
	s.registerOpsRoutes(r)     // dockets, regulatory, reports, jobs, plugins, memory
	s.registerEnginesRoutes(r) // playbooks, redline, headnotes, precedents, citations, briefing
	s.registerContentRoutes(r) // document library/upload, table.csv, profile cost, tone
	s.registerRedtimeRoutes(r) // document version lineage timeline (Redtime)
	s.registerReviewRoutes(r)  // tabular_review matrix as JSON (due-diligence grid)
	s.registerRemyRoutes(r)    // client-voice briefs + matter notifications (Remy/CNTXT)
	s.registerAuthRoutes(r)    // browser OAuth login + signed-cookie sessions
	s.registerClioRoutes(r)    // Clio OAuth connect flow, matter import, time sync

	s.router = r

	// ── Big Michael bots (Teams/Slack) ────────────────────────────────────
	s.mountBots(r)

	return s
}

// Run starts the HTTP server on addr (e.g. ":3101").
func (s *Server) Run(addr string) error {
	return s.router.Run(addr)
}

// Serve runs the API on addr until ctx is cancelled, then shuts down
// gracefully. In-flight requests get a grace period; long-lived SSE streams
// (/tasks/:id/stream, /audit/stream) never end on their own, so when the
// grace period expires the remaining connections are force-closed — without
// that, shutdown would hang for as long as a browser tab stays open.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.router}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		grace, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(grace); err != nil {
			return srv.Close()
		}
		return nil
	}
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

		// A valid signed session cookie (browser OAuth login) is an
		// alternative credential to the bearer token. Checked before the
		// /auth/ bypass so partner-gated auth routes (e.g. the Clio connect
		// flow) see the logged-in user.
		if u := s.sessionUserFromCookie(c); u != nil {
			c.Set(ctxUserKey, u)
			c.Next()
			return
		}

		// The login flow itself must be reachable without credentials.
		if strings.HasPrefix(c.Request.URL.Path, "/auth/") {
			c.Next()
			return
		}

		// The bearer token is the credential; X-Profile-ID alone is just a
		// claim and would let anyone impersonate any profile.
		authz := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(authz, "Bearer ")
		if !ok || s.cfg.API.APIKey == "" ||
			subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.API.APIKey)) != 1 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "valid bearer token required"})
			c.Abort()
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

// newBlobStore builds the attachment blob store (disk or S3-compatible per
// config). A failure is logged and returns nil — attachment retention then
// degrades gracefully (extraction/ingest still work; originals just aren't kept).
func newBlobStore(cfg *config.Config) blob.Store {
	bs, err := blob.Open(cfg.Blob)
	if err != nil {
		slog.Warn("attachment blob store unavailable; originals will not be retained",
			"backend", cfg.Blob.Backend, "err", err)
		return nil
	}
	slog.Info("attachment blob store ready", "backend", bs.Backend())
	return bs
}

// reqIdentity derives the durable-store identity (drives database RLS) from the
// request's session user. A request with no user is anonymous and, under the
// default-deny policy, sees/writes nothing.
func reqIdentity(c *gin.Context) context.Context {
	u := getUser(c)
	if u == nil {
		return c.Request.Context() // no identity → default-deny
	}
	return store.WithIdentity(c.Request.Context(), u.ProfileID, auth.IsPartner(u))
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

// handleHealth mirrors the TS backend's /health contract — the UI reads
// version, uptime, and the per-status task counts.
func (s *Server) handleHealth(c *gin.Context) {
	var running, awaitingGate, complete int
	all := s.orch.ListTasks()
	for _, t := range all {
		switch t.Status {
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

	if err := s.orch.RejectGate(taskID, gateID, body.Reason, u.ProfileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
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
	c.JSON(http.StatusOK, gin.H{"ok": true})
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
