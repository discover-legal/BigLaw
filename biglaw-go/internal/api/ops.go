// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Operational routes — docket monitoring, regulatory pulse, status reports,
// background job queue, conflict-graph sync, plugin listing, memory query.
// Mirrors the TS backend contract in src/mcp/server.ts.
//
// Subsystems not injected into Server (docket monitor, reg-pulse monitor,
// report generator, job queue, plugin registry, budget monitor) are
// constructed lazily on first use from cfg paths and orchestrator accessors.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/budget"
	"github.com/discover-legal/biglaw-go/internal/dockets"
	"github.com/discover-legal/biglaw-go/internal/graph"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/memory"
	"github.com/discover-legal/biglaw-go/internal/queue"
	"github.com/discover-legal/biglaw-go/internal/regulatory"
	"github.com/discover-legal/biglaw-go/internal/reports"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/store"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	opsMaxDocketSSEListeners = 20 // mirrors MAX_DOCKET_SSE_LISTENERS in TS
	opsMaxRegSSEListeners    = 20 // mirrors MAX_REG_SSE_LISTENERS in TS
)

// registerOpsRoutes registers all operational routes on the router.
// Call from Server.New after the core route groups.
func (s *Server) registerOpsRoutes(r *gin.Engine) {
	// ── Docket monitoring ─────────────────────────────────────────────────
	r.POST("/dockets/watch", s.handleDocketWatch)
	r.DELETE("/dockets/watch/:matterNumber", s.handleDocketUnwatch)
	r.GET("/dockets", s.handleDocketList)
	r.POST("/dockets/check-now", s.handleDocketCheckNow)
	r.GET("/dockets/alerts/stream", s.handleDocketAlertStream)

	// ── Regulatory pulse ──────────────────────────────────────────────────
	r.GET("/regulatory/alerts/stream", s.handleRegulatoryAlertStream)
	r.POST("/regulatory/check-now", s.handleRegulatoryCheckNow)

	// ── Client status reports ─────────────────────────────────────────────
	// The /tasks group already uses ":id" for this path position.
	r.POST("/tasks/:id/status-report", s.handleStatusReport)

	// ── Job queue monitoring ──────────────────────────────────────────────
	r.GET("/jobs", s.handleListJobs)
	r.GET("/jobs/stats", s.handleJobStats)
	r.POST("/jobs/:id/retry", s.handleRetryJob)

	// ── Conflict graph ────────────────────────────────────────────────────
	// GET /clients/:id/conflicts is already registered in server.go.
	r.POST("/graph/sync", s.handleGraphSync)
	r.POST("/clients/check-conflict-graph", s.handleCheckConflictGraph)

	// ── Plugins ───────────────────────────────────────────────────────────
	r.GET("/plugins", s.handleListPlugins)

	// ── Inter-round memory query ──────────────────────────────────────────
	r.POST("/memory/query", s.handleMemoryQuery)
}

// ─── Lazy subsystem state ─────────────────────────────────────────────────────
// Server's struct cannot grow (server.go is owned elsewhere), so per-Server
// lazy state lives in a package-level map keyed by the Server pointer.

type opsState struct {
	docketsOnce     sync.Once
	docketMonitor   *dockets.Monitor
	docketBroadcast *opsBroadcaster[types.DocketAlert]

	regOnce      sync.Once
	regMonitor   *regulatory.Monitor
	regBroadcast *opsBroadcaster[types.RegulationAlert]
	regErr       error

	reportsOnce sync.Once
	reportsGen  *reports.Generator
	reportsErr  error

	jobsOnce sync.Once
	jobQueue *queue.Queue

	pluginsOnce sync.Once
	plugins     *adapters.Registry

	budgetOnce    sync.Once
	budgetMonitor *budget.Monitor
}

var (
	opsStatesMu sync.Mutex
	opsStates   = map[*Server]*opsState{}
)

func (s *Server) ops() *opsState {
	opsStatesMu.Lock()
	defer opsStatesMu.Unlock()
	st, ok := opsStates[s]
	if !ok {
		st = &opsState{}
		opsStates[s] = st
	}
	return st
}

// ─── Env helpers ──────────────────────────────────────────────────────────────

func opsEnvBool(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1"
}

func opsEnvDurationMs(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func opsDocketMonitorEnabled() bool { return opsEnvBool("DOCKET_MONITOR_ENABLED") }

// ─── Alert broadcaster ────────────────────────────────────────────────────────
// dockets.Monitor and regulatory.Monitor take a single AlertHandler; SSE needs
// fan-out to N concurrent streams. The broadcaster is that single handler.

type opsBroadcaster[T any] struct {
	mu   sync.Mutex
	subs map[chan T]struct{}
}

func newOpsBroadcaster[T any]() *opsBroadcaster[T] {
	return &opsBroadcaster[T]{subs: make(map[chan T]struct{})}
}

func (b *opsBroadcaster[T]) Subscribe() chan T {
	ch := make(chan T, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *opsBroadcaster[T]) Unsubscribe(ch chan T) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *opsBroadcaster[T]) Publish(v T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- v:
		default: // slow consumer — drop rather than block the monitor
		}
	}
}

func (b *opsBroadcaster[T]) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// ─── Subsystem constructors ───────────────────────────────────────────────────

// opsKnowledgeIngester adapts knowledge.Store to dockets.KnowledgeIngester.
type opsKnowledgeIngester struct{ store *knowledge.Store }

func (k *opsKnowledgeIngester) IngestDoc(title, content, source, docType string) error {
	// Docket/regulatory monitors are trusted internal callers → system identity.
	_, err := k.store.Ingest(store.WithSystem(context.Background()), types.Document{
		Title:        title,
		Content:      content,
		Source:       source,
		DocumentType: docType,
		IngestedAt:   time.Now(),
	})
	return err
}

func (s *Server) opsDockets() (*dockets.Monitor, *opsBroadcaster[types.DocketAlert]) {
	st := s.ops()
	st.docketsOnce.Do(func() {
		// Prefer the firm-wide monitor started in main (AttachDockets): it
		// already polls, ingests filings, and posts channel alerts. The SSE
		// broadcaster chains onto its alert handler.
		if s.dockets != nil {
			b := newOpsBroadcaster[types.DocketAlert]()
			s.dockets.AddAlertHandler(func(a types.DocketAlert) { b.Publish(a) })
			st.docketMonitor = s.dockets
			st.docketBroadcast = b
			return
		}
		// Standalone fallback: no firm monitor running — own the instance.
		path := os.Getenv("DOCKETS_FILE")
		if path == "" {
			path = "./data/dockets.json" // TS default (Config.dockets.file)
		}
		m := dockets.New(path)
		if err := m.Init(); err != nil {
			slog.Warn("ops: docket monitor init failed", "err", err)
		}
		m.SetKnowledgeIngester(&opsKnowledgeIngester{store: s.knowledge})
		b := newOpsBroadcaster[types.DocketAlert]()
		m.SetAlertHandler(func(a types.DocketAlert) { b.Publish(a) })
		if opsDocketMonitorEnabled() {
			m.Start(opsEnvDurationMs("DOCKET_POLL_INTERVAL_MS", 4*time.Hour))
		}
		st.docketMonitor = m
		st.docketBroadcast = b
	})
	return st.docketMonitor, st.docketBroadcast
}

func (s *Server) opsRegulatory() (*regulatory.Monitor, *opsBroadcaster[types.RegulationAlert], error) {
	st := s.ops()
	st.regOnce.Do(func() {
		// Prefer the firm-wide pulse started in main (AttachRegulatory).
		if s.regulatory != nil {
			b := newOpsBroadcaster[types.RegulationAlert]()
			s.regulatory.AddAlertHandler(func(a types.RegulationAlert) { b.Publish(a) })
			st.regMonitor = s.regulatory
			st.regBroadcast = b
			return
		}
		// Standalone fallback: no firm monitor running — own the instance.
		lightID := routing.Light(s.cfg)
		provider, err := s.orch.Providers().Get(lightID)
		if err != nil {
			st.regErr = err
			return
		}
		m := regulatory.New(provider, lightID)
		b := newOpsBroadcaster[types.RegulationAlert]()
		m.SetAlertHandler(func(a types.RegulationAlert) { b.Publish(a) })
		if opsEnvBool("REG_PULSE_ENABLED") && m.IsEnabled() {
			m.Start(opsEnvDurationMs("REG_PULSE_INTERVAL_MS", 7*24*time.Hour), func() []types.Task {
				return opsDerefTasks(s.orch.ListTasks())
			})
		}
		st.regMonitor = m
		st.regBroadcast = b
	})
	return st.regMonitor, st.regBroadcast, st.regErr
}

// opsRegPulseEnabled mirrors the TS regPulse.isEnabled(): both
// REG_PULSE_ENABLED=true and TAVILY_API_KEY must be set.
func opsRegPulseEnabled(m *regulatory.Monitor) bool {
	return m != nil && opsEnvBool("REG_PULSE_ENABLED") && m.IsEnabled()
}

func (s *Server) opsReports() (*reports.Generator, error) {
	st := s.ops()
	st.reportsOnce.Do(func() {
		heavyID := routing.Heavy(s.cfg)
		provider, err := s.orch.Providers().Get(heavyID)
		if err != nil {
			st.reportsErr = err
			return
		}
		st.reportsGen = reports.New(provider, heavyID)
	})
	return st.reportsGen, st.reportsErr
}

func (s *Server) opsJobs() *queue.Queue {
	st := s.ops()
	st.jobsOnce.Do(func() {
		q := queue.New(s.cfg.Persistence.JobsFile)
		if err := q.Init(); err != nil {
			slog.Warn("ops: job queue init failed", "err", err)
		}
		st.jobQueue = q
	})
	return st.jobQueue
}

func (s *Server) opsPlugins() *adapters.Registry {
	st := s.ops()
	st.pluginsOnce.Do(func() {
		// The plugin registry is loaded in cmd/biglaw/main.go but not injected
		// into Server; reload the same JSON adapters read-only.
		reg := adapters.New()
		if err := reg.LoadDirectory("adapters/external"); err != nil {
			slog.Warn("ops: plugin adapter load failed", "err", err)
		}
		st.plugins = reg
	})
	return st.plugins
}

// opsTimeAdapter bridges timekeeping.TimeStore to budget.TimeStore.
type opsTimeAdapter struct{ store *timekeeping.TimeStore }

func (a *opsTimeAdapter) List(matterNumber string) []types.TimeEntry {
	return a.store.List(timekeeping.TimeFilter{MatterNumber: matterNumber})
}

func (a *opsTimeAdapter) ListAll() []types.TimeEntry {
	return a.store.List(timekeeping.TimeFilter{})
}

// opsClientAdapter bridges clients.ClientStore to budget.ClientStore for the
// read-only GetBurn path. Persist is never reached from GetBurn (it is only
// called by CheckMatter, which ops.go does not use), and the wrapped store
// persists internally on every mutation anyway.
type opsClientAdapter struct{ server *Server }

func (a *opsClientAdapter) List() []*types.Client {
	list := a.server.clients.List()
	out := make([]*types.Client, len(list))
	for i := range list {
		out[i] = &list[i]
	}
	return out
}

func (a *opsClientAdapter) SetMatterBudgetAlerts(matterNumber string, triggered []float64) error {
	return a.server.clients.SetMatterBudgetAlerts(matterNumber, triggered)
}

func (s *Server) opsBudget() *budget.Monitor {
	st := s.ops()
	st.budgetOnce.Do(func() {
		st.budgetMonitor = budget.NewMonitor(
			&opsTimeAdapter{store: s.time},
			&opsClientAdapter{server: s},
			nil,
		)
	})
	return st.budgetMonitor
}

func opsDerefTasks(tasks []*types.Task) []types.Task {
	out := make([]types.Task, 0, len(tasks))
	for _, t := range tasks {
		if t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// ─── Docket monitoring ────────────────────────────────────────────────────────

type docketWatchBody struct {
	MatterNumber string `json:"matterNumber"`
	DocketNumber string `json:"docketNumber"`
	Court        string `json:"court"`
	CaseName     string `json:"caseName"`
}

func (s *Server) handleDocketWatch(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body docketWatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber, docketNumber, court required"})
		return
	}
	if body.MatterNumber == "" || body.DocketNumber == "" || body.Court == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber, docketNumber, court required"})
		return
	}
	m, _ := s.opsDockets()
	entry, err := m.Watch(body.MatterNumber, body.DocketNumber, body.Court, body.CaseName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, entry)
}

func (s *Server) handleDocketUnwatch(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	m, _ := s.opsDockets()
	if !m.Unwatch(c.Param("matterNumber")) {
		c.JSON(http.StatusNotFound, gin.H{"error": "No watched docket for this matter"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) handleDocketList(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	m, _ := s.opsDockets()
	c.JSON(http.StatusOK, m.List())
}

func (s *Server) handleDocketCheckNow(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	if !opsDocketMonitorEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Docket monitoring not enabled (set DOCKET_MONITOR_ENABLED=true)"})
		return
	}
	m, _ := s.opsDockets()
	m.CheckAll()
	c.JSON(http.StatusOK, gin.H{"ok": true, "watching": len(m.List())})
}

func (s *Server) handleDocketAlertStream(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	_, b := s.opsDockets()
	if b.Count() >= opsMaxDocketSSEListeners {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many concurrent docket streams"})
		return
	}
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)
	opsStreamSSE(c, ch)
}

// ─── Regulatory pulse ─────────────────────────────────────────────────────────

func (s *Server) handleRegulatoryAlertStream(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	m, b, err := s.opsRegulatory()
	if err != nil || !opsRegPulseEnabled(m) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Regulatory pulse not enabled (set REG_PULSE_ENABLED=true and TAVILY_API_KEY)"})
		return
	}
	if b.Count() >= opsMaxRegSSEListeners {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many concurrent regulatory streams"})
		return
	}
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)
	opsStreamSSE(c, ch)
}

func (s *Server) handleRegulatoryCheckNow(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	m, _, err := s.opsRegulatory()
	if err != nil || !opsRegPulseEnabled(m) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Regulatory pulse not enabled"})
		return
	}
	tasks := opsDerefTasks(s.orch.ListTasks())
	alerts := m.CheckAll(tasks)
	if alerts == nil {
		alerts = []types.RegulationAlert{}
	}
	c.JSON(http.StatusOK, gin.H{"checked": len(tasks), "alerts": alerts})
}

// opsStreamSSE streams values from ch as `data: {json}` SSE frames until the
// client disconnects. Heartbeats every 30s keep proxies from closing the pipe
// (same pattern as handleAuditStream).
func opsStreamSSE[T any](c *gin.Context, ch <-chan T) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ctx := c.Request.Context()
	flusher, hasFlusher := c.Writer.(http.Flusher)
	flush := func() {
		if hasFlusher {
			flusher.Flush()
		}
	}

	// Flush headers immediately so the client sees the stream open.
	flush()

	for {
		select {
		case <-ctx.Done():
			return
		case v, ok := <-ch:
			if !ok {
				return
			}
			raw, _ := json.Marshal(v)
			io.WriteString(c.Writer, "data: "+string(raw)+"\n\n")
			flush()
		case <-time.After(30 * time.Second):
			io.WriteString(c.Writer, ": heartbeat\n\n")
			flush()
		}
	}
}

// ─── Client status reports ────────────────────────────────────────────────────

type statusReportBody struct {
	Format             *string `json:"format"`
	IncludeTimeEntries *bool   `json:"includeTimeEntries"`
	IncludeBudgetBurn  *bool   `json:"includeBudgetBurn"`
	IncludeOcgFlags    *bool   `json:"includeOcgFlags"` // accepted for contract parity; Go generator has no OCG flag support yet
	CustomNote         string  `json:"customNote"`
}

func (s *Server) handleStatusReport(c *gin.Context) {
	u := getUser(c)
	task := s.orch.GetTask(c.Param("id"))
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if !auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID && !auth.IsPartner(u) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	// Body is optional in TS (req.body ?? {}); ignore bind errors on empty body.
	var body statusReportBody
	_ = c.ShouldBindJSON(&body)

	format := "markdown"
	if body.Format != nil {
		format = *body.Format
	}
	if format != "html" && format != "markdown" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "format must be html or markdown"})
		return
	}
	includeTime := body.IncludeTimeEntries == nil || *body.IncludeTimeEntries
	includeBudget := body.IncludeBudgetBurn == nil || *body.IncludeBudgetBurn

	var timeEntries []types.TimeEntry
	if includeTime {
		timeEntries = s.time.List(timekeeping.TimeFilter{TaskID: task.ID})
	}

	var burn *types.BudgetBurn
	if includeBudget && task.MatterNumber != "" {
		burn = s.opsBudget().GetBurn(task.MatterNumber)
	}

	// Resolve the submitting lawyer's tone profile if available.
	assignedID := task.CreatedByProfileID
	if len(task.AssignedLawyerIDs) > 0 {
		assignedID = task.AssignedLawyerIDs[0]
	}
	var lawyer *types.LawyerProfile
	if assignedID != "" {
		lawyer = s.profiles.Get(assignedID)
	}

	gen, err := s.opsReports()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "report generator unavailable: " + err.Error()})
		return
	}
	report, err := gen.Generate(*task, timeEntries, burn, reports.Opts{
		Format:             format,
		IncludeTimeEntries: includeTime,
		IncludeBudgetBurn:  includeBudget,
		CustomNote:         body.CustomNote,
	}, lawyer)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if format == "html" {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(report.Content))
		return
	}
	c.JSON(http.StatusOK, report)
}

// ─── Job queue monitoring ─────────────────────────────────────────────────────

func (s *Server) handleListJobs(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}
	jobs := s.opsJobs().List(types.JobStatus(c.Query("status")), limit, offset)
	if jobs == nil {
		jobs = []*types.Job{}
	}
	c.JSON(http.StatusOK, jobs)
}

func (s *Server) handleJobStats(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	c.JSON(http.StatusOK, s.opsJobs().Stats())
}

func (s *Server) handleRetryJob(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	id := c.Param("id")
	job := s.opsJobs().Retry(id)
	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("Job not found: %s", id)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "job": job})
}

// ─── Conflict graph ───────────────────────────────────────────────────────────

func (s *Server) handleGraphSync(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	if err := s.graph.Ping(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conflict graph unavailable: " + err.Error()})
		return
	}
	if err := s.graph.Sync(opsBuildSyncInput(s.clients.List())); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "conflict graph sync failed: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "Conflict graph synced"})
}

// opsBuildSyncInput converts the client roster into the sidecar sync payload.
// Mirrors Server.syncGraph in server.go (which cannot be reused here because
// it retries with backoff and swallows errors — wrong shape for a request).
func opsBuildSyncInput(roster []types.Client) graph.SyncInput {
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
	return input
}

type checkConflictGraphBody struct {
	ClientID     string   `json:"clientId"`
	AdversaryIDs []string `json:"adversaryIds"`
}

func (s *Server) handleCheckConflictGraph(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body checkConflictGraphBody
	_ = c.ShouldBindJSON(&body)
	if body.ClientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clientId required"})
		return
	}
	conflicts, err := s.graph.CheckNewMatter(body.ClientID, body.AdversaryIDs)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "conflict graph unavailable: " + err.Error()})
		return
	}
	if conflicts == nil {
		conflicts = []types.ConflictReport{}
	}
	c.JSON(http.StatusOK, gin.H{"conflicts": conflicts, "hasConflict": len(conflicts) > 0})
}

// ─── Plugins ──────────────────────────────────────────────────────────────────

// handleListPlugins mirrors the TS pluginRegistry.list() summary shape:
// { id, name, source, tools, agents, workflows } with counts, never secrets.
func (s *Server) handleListPlugins(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	out := make([]gin.H, 0)
	for _, p := range s.opsPlugins().List() {
		out = append(out, gin.H{
			"id":        p.Plugin.ID,
			"name":      p.Plugin.Name,
			"source":    "json", // Go port loads JSON adapters only
			"tools":     len(p.Plugin.Tools),
			"agents":    len(p.Plugin.Agents),
			"workflows": len(p.Plugin.Workflows),
		})
	}
	c.JSON(http.StatusOK, out)
}

// ─── Inter-round memory query ─────────────────────────────────────────────────

type memoryQueryBody struct {
	Query   string `json:"query"`
	TaskID  string `json:"taskId"`
	AgentID string `json:"agentId"`
	TopK    int    `json:"topK"`
}

func (s *Server) handleMemoryQuery(c *gin.Context) {
	// TS applies no per-route gate here beyond global auth; mirror that.
	var body memoryQueryBody
	_ = c.ShouldBindJSON(&body)

	entries, err := s.orch.MemoryStore().Query(body.Query, memory.QueryOpts{
		TaskID:  body.TaskID,
		AgentID: body.AgentID,
		TopK:    body.TopK,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []types.MemoryEntry{}
	}
	c.JSON(http.StatusOK, entries)
}
