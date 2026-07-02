// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Matter routes: budgets (set / burn / threshold check / alert SSE / prediction),
// court deadlines, matter health scoring, and portfolio + NOSLEGAL analytics.
// HTTP contract mirrors the TypeScript backend (src/mcp/server.ts).
//
// mattersBudgetClientAdapter overlays budget state (budgetUsd / thresholds /
// alerts-triggered) onto roster copies, persisted to its own JSON file beside
// the clients file; budget writes also flow through to the client store so the
// firm-wide monitor and bot facade share the same numbers.
//
// Also hosts the adapters behind the firm-wide bot/monitor budget reader
// (apiBudgetTime / apiBudgetClients) and AttachDockets, which exposes the
// running docket monitor to the REST handlers and the bot facade.

package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/budget"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/deadlines"
	"github.com/discover-legal/biglaw-go/internal/dockets"
	"github.com/discover-legal/biglaw-go/internal/matters"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/regulatory"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// Mirrors MAX_BUDGET_SSE_LISTENERS in the TS backend.
const mattersMaxBudgetSSEListeners = 20

// ─── Subsystem (lazy singleton) ───────────────────────────────────────────────

type mattersSubsystem struct {
	// budgetMu serialises the budget Monitor's List → mutate → Persist
	// sequences (the Monitor itself does no locking).
	budgetMu  sync.Mutex
	adapter   *mattersBudgetClientAdapter
	monitor   *budget.Monitor
	predictor *budget.Predictor

	deadlines *deadlines.Engine

	// healthMu guards matters.Monitor, whose trend-history map is not
	// goroutine-safe.
	healthMu sync.Mutex
	health   *matters.Monitor

	alerts *mattersAlertBroker

	timeAdapter *mattersTimeAdapter
	taskAdapter *mattersTaskAdapter
}

var (
	mattersInitOnce sync.Once
	mattersShared   *mattersSubsystem
)

// mattersDeps lazily constructs the matter-domain subsystems from the
// Server's stores and config.
func (s *Server) mattersDeps() *mattersSubsystem {
	mattersInitOnce.Do(func() {
		overlayPath := filepath.Join(
			filepath.Dir(s.cfg.Persistence.ClientsFile), ".matter-budgets.json")
		adapter := newMattersBudgetClientAdapter(s.clients, overlayPath)
		broker := newMattersAlertBroker()
		timeAdapter := &mattersTimeAdapter{store: s.time}

		sys := &mattersSubsystem{
			adapter:     adapter,
			predictor:   &budget.Predictor{},
			deadlines:   deadlines.New(),
			health:      matters.New(),
			alerts:      broker,
			timeAdapter: timeAdapter,
			taskAdapter: &mattersTaskAdapter{orch: s.orch},
		}
		sys.monitor = budget.NewMonitor(timeAdapter, adapter, broker.Publish)

		// Same rule source as the TS backend: a directory of YAML rule files,
		// overridable via DEADLINES_RULES_DIR. Missing dir degrades gracefully
		// (no rules loaded → compute returns 404), exactly like TS.
		rulesDir := os.Getenv("DEADLINES_RULES_DIR")
		if rulesDir == "" {
			rulesDir = "./deadlines/rules"
		}
		_ = sys.deadlines.LoadRulesDir(rulesDir)

		mattersShared = sys
	})
	return mattersShared
}

// registerMattersRoutes wires budget, deadline, health, and analytics routes.
func (s *Server) registerMattersRoutes(r *gin.Engine) {
	// Construct eagerly so deadline rules load at startup, like the TS init().
	s.mattersDeps()

	// Budget routes live under the existing /clients tree (params ":id"/":num").
	r.PUT("/clients/:id/matters/:num/budget", s.handleSetMatterBudget)
	r.GET("/clients/:id/matters/:num/budget", s.handleGetMatterBudgetBurn)
	r.POST("/clients/:id/matters/:num/budget/check", s.handleCheckMatterBudget)
	r.GET("/budget/alerts/stream", s.handleBudgetAlertStream)

	m := r.Group("/matters")
	{
		m.GET("/:matterNumber/budget-prediction", s.handleBudgetPrediction)
		m.POST("/:matterNumber/deadlines", s.handleMatterDeadlines)
		m.GET("/:matterNumber/health", s.handleMatterHealth)
	}

	r.GET("/deadlines/rules", s.handleDeadlineRules)
	r.POST("/deadlines/compute", s.handleDeadlineCompute)

	r.GET("/analytics/portfolio-health", s.handlePortfolioHealth)
	r.GET("/analytics/noslegal", s.handleNoslegalAnalytics)
}

// ─── Matter budget tracking ───────────────────────────────────────────────────

type mattersSetBudgetBody struct {
	BudgetUsd float64 `json:"budgetUsd"`
	// Pointer distinguishes "absent" (use defaults) from "provided".
	Thresholds *[]float64 `json:"thresholds"`
}

func (s *Server) handleSetMatterBudget(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body mattersSetBudgetBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.BudgetUsd <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "budgetUsd must be positive"})
		return
	}
	var thresholds []float64
	if body.Thresholds != nil {
		for _, t := range *body.Thresholds {
			if !(t > 0 && t <= 1) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": "thresholds must be an array of numbers between 0 (exclusive) and 1 (inclusive)",
				})
				return
			}
		}
		thresholds = *body.Thresholds
	}

	sys := s.mattersDeps()
	sys.budgetMu.Lock()
	matter := sys.adapter.SetBudget(c.Param("id"), c.Param("num"), body.BudgetUsd, thresholds)
	sys.budgetMu.Unlock()
	if matter == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Client or matter not found"})
		return
	}
	// Write through to the client store so the firm-wide budget monitor and
	// the bot facade (which read budgets from the roster) see it too.
	if err := s.clients.SetMatterBudget(c.Param("num"), &body.BudgetUsd, thresholds); err != nil {
		slog.Warn("matter budgets: store write-through failed", "matter", c.Param("num"), "err", err)
	}
	c.JSON(http.StatusOK, matter)
}

func (s *Server) handleGetMatterBudgetBurn(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	sys := s.mattersDeps()
	matterNumber := c.Param("num")
	sys.budgetMu.Lock()
	burn := sys.monitor.GetBurn(matterNumber)
	sys.budgetMu.Unlock()
	if burn == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No budget set for this matter"})
		return
	}
	// TS spreads the burn into the response: { matterNumber, ...burn }.
	c.JSON(http.StatusOK, gin.H{
		"matterNumber": matterNumber,
		"budgetUsd":    burn.BudgetUsd,
		"burnUsd":      burn.BurnUsd,
		"burnPct":      burn.BurnPct,
		"remaining":    burn.Remaining,
	})
}

func (s *Server) handleCheckMatterBudget(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	sys := s.mattersDeps()
	sys.budgetMu.Lock()
	sys.monitor.CheckMatter(c.Param("num"))
	sys.budgetMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleBudgetAlertStream streams budget threshold alerts as SSE data frames.
func (s *Server) handleBudgetAlertStream(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	sys := s.mattersDeps()
	ch, cancel, ok := sys.alerts.Subscribe(mattersMaxBudgetSSEListeners)
	if !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many concurrent budget streams"})
		return
	}
	defer cancel()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ctx := c.Request.Context()
	flusher, hasFlusher := c.Writer.(http.Flusher)

	// Flush headers immediately, like the TS flushHeaders().
	c.Writer.WriteHeaderNow()
	if hasFlusher {
		flusher.Flush()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case alert, open := <-ch:
			if !open {
				return
			}
			raw, _ := json.Marshal(alert)
			io.WriteString(c.Writer, "data: "+string(raw)+"\n\n")
			if hasFlusher {
				flusher.Flush()
			}
		case <-time.After(30 * time.Second):
			// Comment-line heartbeat (invisible to EventSource clients).
			io.WriteString(c.Writer, ": heartbeat\n\n")
			if hasFlusher {
				flusher.Flush()
			}
		}
	}
}

// ─── Budget prediction ────────────────────────────────────────────────────────

func (s *Server) handleBudgetPrediction(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	sys := s.mattersDeps()
	prediction := sys.predictor.Predict(c.Param("matterNumber"), sys.timeAdapter, sys.taskAdapter)
	if prediction == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No billing data found for this matter"})
		return
	}
	c.JSON(http.StatusOK, prediction)
}

// ─── Deadline calculator ──────────────────────────────────────────────────────

func (s *Server) handleDeadlineRules(c *gin.Context) {
	list := s.mattersDeps().deadlines.ListJurisdictions()
	if list == nil {
		list = []map[string]interface{}{}
	}
	c.JSON(http.StatusOK, list)
}

type mattersComputeDeadlinesBody struct {
	Jurisdiction string `json:"jurisdiction"`
	TriggerEvent string `json:"triggerEvent"`
	TriggerDate  string `json:"triggerDate"`
}

func (s *Server) handleDeadlineCompute(c *gin.Context) {
	var body mattersComputeDeadlinesBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.Jurisdiction == "" || body.TriggerEvent == "" || body.TriggerDate == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "jurisdiction, triggerEvent, triggerDate required"})
		return
	}
	date, ok := mattersNormalizeISODate(body.TriggerDate)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "triggerDate must be a valid ISO date string"})
		return
	}
	result, err := s.mattersDeps().deadlines.Compute(body.Jurisdiction, body.TriggerEvent, date)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if result.Deadlines == nil {
		result.Deadlines = []types.ComputedDeadline{}
	}
	c.JSON(http.StatusOK, result)
}

type mattersMatterDeadlinesBody struct {
	TriggerEvent string `json:"triggerEvent"`
	TriggerDate  string `json:"triggerDate"`
}

func (s *Server) handleMatterDeadlines(c *gin.Context) {
	var body mattersMatterDeadlinesBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.TriggerEvent == "" || body.TriggerDate == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "triggerEvent and triggerDate required"})
		return
	}

	// Resolve the matter's jurisdiction from the first associated task,
	// matching the TS lookup (first task by matter number wins).
	matterNumber := c.Param("matterNumber")
	jurisdiction := ""
	for _, t := range s.orch.ListTasks() {
		if t != nil && t.MatterNumber == matterNumber {
			jurisdiction = t.Jurisdiction
			break
		}
	}
	if jurisdiction == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "No task with jurisdiction found for this matter"})
		return
	}

	date, ok := mattersNormalizeISODate(body.TriggerDate)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "triggerDate must be a valid ISO date string"})
		return
	}
	result, err := s.mattersDeps().deadlines.Compute(jurisdiction, body.TriggerEvent, date)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	if result.Deadlines == nil {
		result.Deadlines = []types.ComputedDeadline{}
	}
	c.JSON(http.StatusOK, result)
}

// mattersNormalizeISODate accepts YYYY-MM-DD or a full RFC 3339 timestamp and
// returns the date in YYYY-MM-DD form (the only format the deadline engine
// takes). Mirrors the TS Date.parse() validity gate.
func mattersNormalizeISODate(s string) (string, bool) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("2006-01-02"), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02"), true
	}
	return "", false
}

// ─── Matter health ────────────────────────────────────────────────────────────

func (s *Server) handleMatterHealth(c *gin.Context) {
	sys := s.mattersDeps()
	tasks := sys.taskAdapter.ListAll()
	sys.healthMu.Lock()
	score := sys.health.Compute(c.Param("matterNumber"), tasks, sys.timeAdapter, mattersLockedBurner{sys})
	sys.healthMu.Unlock()
	if score.RiskFactors == nil {
		score.RiskFactors = []types.MatterRiskFactor{}
	}
	c.JSON(http.StatusOK, score)
}

func (s *Server) handlePortfolioHealth(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	sys := s.mattersDeps()
	tasks := sys.taskAdapter.ListAll()

	// Unique matter numbers in first-seen order (TS uses a Set over tasks).
	seen := map[string]bool{}
	var matterNumbers []string
	for _, t := range tasks {
		if t.MatterNumber != "" && !seen[t.MatterNumber] {
			seen[t.MatterNumber] = true
			matterNumbers = append(matterNumbers, t.MatterNumber)
		}
	}
	if len(matterNumbers) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"totalMatters": 0,
			"green":        0,
			"amber":        0,
			"red":          0,
			"matters":      []types.MatterHealthScore{},
			"computedAt":   time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	sys.healthMu.Lock()
	summary := sys.health.Portfolio(matterNumbers, tasks, sys.timeAdapter, mattersLockedBurner{sys})
	sys.healthMu.Unlock()
	for i := range summary.Matters {
		if summary.Matters[i].RiskFactors == nil {
			summary.Matters[i].RiskFactors = []types.MatterRiskFactor{}
		}
	}
	c.JSON(http.StatusOK, summary)
}

// ─── NOSLEGAL analytics ───────────────────────────────────────────────────────

func (s *Server) handleNoslegalAnalytics(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	// Partner-only, and partners see every task — matches the TS
	// filterVisible(partner, all) behaviour.
	tasks := s.orch.ListTasks()

	byAreaOfLaw := map[string]int{}
	byWorkType := map[string]int{}
	bySector := map[string]int{}
	byAssetType := map[string]int{}
	for _, t := range tasks {
		if t == nil || t.NosLegal == nil {
			continue
		}
		n := t.NosLegal
		if n.AreaOfLaw != nil && *n.AreaOfLaw != "" {
			byAreaOfLaw[*n.AreaOfLaw]++
		}
		if n.WorkType != nil && *n.WorkType != "" {
			byWorkType[*n.WorkType]++
		}
		if n.Sector != nil && *n.Sector != "" {
			bySector[*n.Sector]++
		}
		if n.AssetType != nil && *n.AssetType != "" {
			byAssetType[*n.AssetType]++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"total":       len(tasks),
		"byAreaOfLaw": byAreaOfLaw,
		"byWorkType":  byWorkType,
		"bySector":    bySector,
		"byAssetType": byAssetType,
	})
}

// ─── Adapters ─────────────────────────────────────────────────────────────────

// Compile-time interface checks.
var (
	_ budget.TimeStore     = (*mattersTimeAdapter)(nil)
	_ matters.TimeReader   = (*mattersTimeAdapter)(nil)
	_ budget.ClientStore   = (*mattersBudgetClientAdapter)(nil)
	_ budget.TaskStore     = (*mattersTaskAdapter)(nil)
	_ matters.BudgetBurner = mattersLockedBurner{}
)

// mattersTimeAdapter bridges timekeeping.TimeStore (filter-based List) to the
// matter-number-keyed interfaces the budget and health packages expect.
type mattersTimeAdapter struct {
	store *timekeeping.TimeStore
}

func (a *mattersTimeAdapter) List(matterNumber string) []types.TimeEntry {
	return a.store.List(timekeeping.TimeFilter{MatterNumber: matterNumber})
}

func (a *mattersTimeAdapter) ListAll() []types.TimeEntry {
	return a.store.List(timekeeping.TimeFilter{})
}

// mattersTaskAdapter exposes the orchestrator's task list as value copies.
type mattersTaskAdapter struct {
	orch *orchestrator.Orchestrator
}

func (a *mattersTaskAdapter) ListAll() []types.Task {
	ptrs := a.orch.ListTasks()
	out := make([]types.Task, 0, len(ptrs))
	for _, t := range ptrs {
		if t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// mattersLockedBurner lets the health monitor read budget burn while holding
// budgetMu per call (the health handlers hold healthMu, never budgetMu, so
// there is no lock-order inversion).
type mattersLockedBurner struct {
	sys *mattersSubsystem
}

func (lb mattersLockedBurner) GetBurn(matterNumber string) *types.BudgetBurn {
	lb.sys.budgetMu.Lock()
	defer lb.sys.budgetMu.Unlock()
	return lb.sys.monitor.GetBurn(matterNumber)
}

// ─── Budget client adapter ────────────────────────────────────────────────────

// mattersBudgetState is the persisted per-matter budget overlay record.
type mattersBudgetState struct {
	BudgetUsd  float64   `json:"budgetUsd"`
	Thresholds []float64 `json:"thresholds,omitempty"`
	Triggered  []float64 `json:"triggered,omitempty"`
}

// mattersBudgetClientAdapter implements budget.ClientStore on top of
// clients.ClientStore. Budget fields are kept in an overlay map persisted to
// its own JSON file, merged onto deep-copied roster snapshots in List().
// All Monitor call sequences are serialised by mattersSubsystem.budgetMu.
type mattersBudgetClientAdapter struct {
	mu      sync.Mutex
	store   *clients.ClientStore
	path    string
	overlay map[string]mattersBudgetState // keyed by matter number
}

func newMattersBudgetClientAdapter(store *clients.ClientStore, path string) *mattersBudgetClientAdapter {
	a := &mattersBudgetClientAdapter{
		store:   store,
		path:    path,
		overlay: map[string]mattersBudgetState{},
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &a.overlay); err != nil {
			slog.Warn("matter budgets: overlay file unreadable, starting empty", "path", path, "err", err)
			a.overlay = map[string]mattersBudgetState{}
		}
	} else if !os.IsNotExist(err) {
		slog.Warn("matter budgets: overlay file read failed", "path", path, "err", err)
	}
	return a
}

// List implements budget.ClientStore: deep-copied roster with budget overlay.
func (a *mattersBudgetClientAdapter) List() []*types.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	roster := a.store.List()
	out := make([]*types.Client, 0, len(roster))
	for i := range roster {
		c := roster[i]
		// Deep-copy Matters: the store's List() copies Client structs but the
		// Matters slice headers still alias its internal arrays.
		mats := make([]types.ClientMatter, len(roster[i].Matters))
		copy(mats, roster[i].Matters)
		c.Matters = mats
		for j := range c.Matters {
			m := &c.Matters[j]
			if st, ok := a.overlay[m.MatterNumber]; ok {
				b := st.BudgetUsd
				m.BudgetUsd = &b
				m.BudgetAlertThresholds = append([]float64(nil), st.Thresholds...)
				m.BudgetAlertsTriggered = append([]float64(nil), st.Triggered...)
			}
		}
		out = append(out, &c)
	}
	return out
}

// SetMatterBudgetAlerts implements budget.ClientStore: record the set of
// thresholds already alerted for a matter (dedup state) in the overlay.
func (a *mattersBudgetClientAdapter) SetMatterBudgetAlerts(matterNumber string, triggered []float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	st, ok := a.overlay[matterNumber]
	if !ok {
		return nil // no budget set for this matter — nothing to record
	}
	st.Triggered = append([]float64(nil), triggered...)
	a.overlay[matterNumber] = st
	return a.save()
}

// SetBudget mirrors the TS ClientStore.setMatterBudget: validate the client
// and matter exist, set budget + thresholds, reset triggered alerts, persist.
// Returns the updated matter, or nil if the client or matter was not found.
func (a *mattersBudgetClientAdapter) SetBudget(clientID, matterNumber string, budgetUsd float64, thresholds []float64) *types.ClientMatter {
	client := a.store.Get(clientID)
	if client == nil {
		return nil
	}
	var matter *types.ClientMatter
	for i := range client.Matters {
		if client.Matters[i].MatterNumber == matterNumber {
			matter = &client.Matters[i]
			break
		}
	}
	if matter == nil {
		return nil
	}
	if thresholds == nil {
		thresholds = []float64{0.5, 0.8, 1.0}
	}

	a.mu.Lock()
	a.overlay[matter.MatterNumber] = mattersBudgetState{
		BudgetUsd:  budgetUsd,
		Thresholds: append([]float64(nil), thresholds...),
		Triggered:  []float64{},
	}
	err := a.save()
	a.mu.Unlock()
	if err != nil {
		// TS persist failure on setMatterBudget is also warn-only.
		slog.Warn("matter budgets: persist failed", "path", a.path, "err", err)
	}

	cp := *matter
	b := budgetUsd
	cp.BudgetUsd = &b
	cp.BudgetAlertThresholds = append([]float64(nil), thresholds...)
	cp.BudgetAlertsTriggered = []float64{}
	return &cp
}

// save writes the overlay atomically (tmp + rename). Caller holds a.mu.
func (a *mattersBudgetClientAdapter) save() error {
	data, err := json.MarshalIndent(a.overlay, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

// ─── Budget alert SSE broker ──────────────────────────────────────────────────

type mattersAlertBroker struct {
	mu   sync.Mutex
	subs map[chan types.BudgetAlert]struct{}
}

func newMattersAlertBroker() *mattersAlertBroker {
	return &mattersAlertBroker{subs: map[chan types.BudgetAlert]struct{}{}}
}

// Subscribe registers a listener. Returns ok=false when the listener cap is
// reached (the caller answers 429, like the TS backend).
func (b *mattersAlertBroker) Subscribe(max int) (<-chan types.BudgetAlert, func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) >= max {
		return nil, nil, false
	}
	ch := make(chan types.BudgetAlert, 16)
	b.subs[ch] = struct{}{}
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
	return ch, cancel, true
}

// Publish fans an alert out to all subscribers, dropping it for any that are
// too slow to drain their buffer.
func (b *mattersAlertBroker) Publish(alert types.BudgetAlert) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- alert:
		default:
		}
	}
}

// ─── Firm-wide monitor adapters ───────────────────────────────────────────────

// apiBudgetTime adapts the time store to the budget TimeStore interface.
type apiBudgetTime struct{ ts *timekeeping.TimeStore }

func (a apiBudgetTime) List(matter string) []types.TimeEntry {
	return a.ts.List(timekeeping.TimeFilter{MatterNumber: matter})
}
func (a apiBudgetTime) ListAll() []types.TimeEntry { return a.ts.List(timekeeping.TimeFilter{}) }

// apiBudgetClients adapts the client roster to the budget ClientStore interface.
type apiBudgetClients struct{ cs *clients.ClientStore }

func (a apiBudgetClients) List() []*types.Client {
	src := a.cs.List()
	out := make([]*types.Client, len(src))
	for i := range src {
		c := src[i]
		out[i] = &c
	}
	return out
}
func (a apiBudgetClients) SetMatterBudgetAlerts(matterNumber string, triggered []float64) error {
	return a.cs.SetMatterBudgetAlerts(matterNumber, triggered)
}

// AttachDockets exposes the running docket monitor to the REST handlers
// (ops.go serves the /dockets routes against it) and the bot facade
// (watch/unwatch/dockets commands). Routes degrade to 503 when nil.
func (s *Server) AttachDockets(dm *dockets.Monitor) {
	s.dockets = dm
}

// AttachRegulatory exposes the running regulatory pulse monitor to the REST
// handlers (ops.go serves /regulatory routes against it when attached).
func (s *Server) AttachRegulatory(rm *regulatory.Monitor) {
	s.regulatory = rm
}
