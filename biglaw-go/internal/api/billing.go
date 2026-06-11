// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Billing routes — pre-bills, invoice validation, OCG, time-entry exports
// and OCG suggestion listing. Mirrors the TS handlers in src/mcp/server.ts.

package api

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/billing"
	"github.com/discover-legal/biglaw-go/internal/ocg"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Lazy domain singletons ───────────────────────────────────────────────────
// Constructed on first use so the Server constructor stays untouched. The OCG
// store and invoice validator need a model provider, which is only reachable
// through the orchestrator at request time.

var (
	billingOnce      sync.Once
	billingPreBills  *billing.PreBillStore
	billingOcg       *ocg.Store
	billingValidator *billing.InvoiceValidator
)

func (s *Server) billingInit() {
	billingOnce.Do(func() {
		billingPreBills = billing.NewPreBillStore(s.cfg.Persistence.PreBillsFile)
		if err := billingPreBills.Init(); err != nil {
			slog.Warn("billing: pre-bill store load failed", "path", s.cfg.Persistence.PreBillsFile, "err", err)
		}
		provider := s.orch.Providers().MustGet(routing.ModelHaiku)
		billingOcg = ocg.NewStore(s.cfg.Persistence.OcgFile, provider, routing.ModelHaiku)
		if err := billingOcg.Init(); err != nil {
			slog.Warn("billing: OCG store load failed", "path", s.cfg.Persistence.OcgFile, "err", err)
		}
		billingValidator = billing.NewInvoiceValidator(provider, routing.ModelHaiku, routing.ModelSonnet)
	})
}

// registerBillingRoutes registers pre-bill, invoice, OCG, and time-entry
// export routes. Path param names reuse the engine's existing names per
// position (":id" under /clients and at the second segment of /pre-bills,
// /time-entries).
func (s *Server) registerBillingRoutes(r *gin.Engine) {
	// Pre-bill review workflow
	r.POST("/pre-bills", s.handleCreatePreBill)
	r.GET("/pre-bills", s.handleListPreBills)
	r.GET("/pre-bills/:id", s.handleGetPreBill)
	r.PATCH("/pre-bills/:id", s.handlePatchPreBill)

	// Invoice validation (reverse-OCG)
	r.POST("/invoices/validate", s.handleValidateInvoice)
	r.POST("/invoices/upload", s.handleUploadInvoice)

	// Time-entry exports + analytics
	r.GET("/time-entries/agent-summary", s.handleAgentSummary)
	r.GET("/time-entries/export.json", s.handleExportTimeJSON)
	r.GET("/time-entries/export.csv", s.handleExportTimeCSV)
	r.GET("/time-entries/export.ledes", s.handleExportTimeLedes)

	// OCG time-entry compliance
	r.GET("/time-entries/suggestions", s.handleTimeSuggestions)
	r.POST("/time-entries/run-ocg-check", s.handleRunOcgCheck)
	r.POST("/time-entries/:id/suggestions/accept", s.handleAcceptOcgSuggestion)
	r.POST("/time-entries/:id/suggestions/dismiss", s.handleDismissOcgSuggestion)

	// Outside Counsel Guidelines per client
	r.POST("/clients/:id/ocg", s.handleIngestOcg)
	r.GET("/clients/:id/ocg", s.handleGetOcg)
	r.DELETE("/clients/:id/ocg", s.handleDeleteOcg)
	r.GET("/clients/:id/ocg/stats", s.handleOcgStats)
}

// ─── Pre-bills ────────────────────────────────────────────────────────────────

type billingCreatePreBillBody struct {
	MatterNumber string `json:"matterNumber"`
	ClientNumber string `json:"clientNumber"`
	From         string `json:"from"`
	To           string `json:"to"`
}

func (s *Server) handleCreatePreBill(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()

	var body billingCreatePreBillBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.MatterNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber required"})
		return
	}

	entries := s.time.List(timekeeping.TimeFilter{
		MatterNumber: body.MatterNumber,
		ClientNumber: body.ClientNumber,
		From:         billingParseTime(body.From),
		To:           billingParseTime(body.To),
	})
	closed := entries[:0]
	for _, e := range entries {
		if e.EndedAt != nil {
			closed = append(closed, e)
		}
	}
	if len(closed) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "No closed entries found for this matter"})
		return
	}

	bill := billingPreBills.Create(body.MatterNumber, body.ClientNumber, getUser(c).ProfileID, closed)
	c.JSON(http.StatusCreated, bill)
}

func (s *Server) handleListPreBills(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()
	bills := billingPreBills.List(c.Query("matterNumber"))
	if bills == nil {
		bills = []billing.PreBill{}
	}
	c.JSON(http.StatusOK, bills)
}

func (s *Server) handleGetPreBill(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()
	bill := billingPreBills.GetByID(c.Param("id"))
	if bill == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Pre-bill not found"})
		return
	}
	c.JSON(http.StatusOK, bill)
}

type billingPatchPreBillBody struct {
	Status    string  `json:"status"`
	Notes     *string `json:"notes"`
	EntryEdit *struct {
		EntryID     string `json:"entryId"`
		Description string `json:"description"`
	} `json:"entryEdit"`
}

func (s *Server) handlePatchPreBill(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()

	id := c.Param("id")
	var body billingPatchPreBillBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	bill := billingPreBills.GetByID(id)
	if bill == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Pre-bill not found"})
		return
	}

	if body.EntryEdit != nil {
		// Mirrors TS: a refused edit (approved/invoiced bill, unknown entry)
		// silently keeps the previous state.
		if updated := billingPreBills.UpdateEntryDescription(id, body.EntryEdit.EntryID, body.EntryEdit.Description); updated != nil {
			bill = updated
		}
	}
	if body.Notes != nil {
		if updated := billingPreBills.SetNotes(id, *body.Notes); updated != nil {
			bill = updated
		}
	}
	if body.Status != "" {
		updated := billingPreBills.Transition(id, billing.PreBillStatus(body.Status))
		if updated == nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": fmt.Sprintf("Invalid transition from %s to %s", bill.Status, body.Status),
			})
			return
		}
		bill = updated
	}
	c.JSON(http.StatusOK, bill)
}

// ─── Invoice validation ───────────────────────────────────────────────────────

type billingValidateInvoiceBody struct {
	InvoiceText           string `json:"invoiceText"`
	ClientID              string `json:"clientId"`
	SubmittedByFirm       string `json:"submittedByFirm"`
	MatterNumber          string `json:"matterNumber"`
	GenerateDisputeLetter bool   `json:"generateDisputeLetter"`
	TaskID                string `json:"taskId"`
}

func (s *Server) handleValidateInvoice(c *gin.Context) {
	s.billingInit()

	var body billingValidateInvoiceBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.InvoiceText == "" && body.ClientID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invoiceText required"})
		return
	}

	var ocgDoc *types.OcgDocument
	if body.ClientID != "" {
		ocgDoc = billingOcg.GetByClient(body.ClientID)
	}

	result := billingValidator.Validate(
		billing.ParseLEDES(body.InvoiceText),
		ocgDoc,
		billing.ValidateOpts{
			ClientID:              body.ClientID,
			SubmittedByFirm:       body.SubmittedByFirm,
			MatterNumber:          body.MatterNumber,
			GenerateDisputeLetter: body.GenerateDisputeLetter,
			TaskID:                body.TaskID,
		},
	)
	billingNormaliseValidation(result)
	c.JSON(http.StatusOK, result)
}

func (s *Server) handleUploadInvoice(c *gin.Context) {
	s.billingInit()

	fh := billingFirstFile(c)
	if fh == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	data, err := billingReadFile(fh)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File upload failed"})
		return
	}
	invoiceText := string(data)

	clientID := c.Query("clientId")
	var ocgDoc *types.OcgDocument
	if clientID != "" {
		ocgDoc = billingOcg.GetByClient(clientID)
	}

	result := billingValidator.Validate(
		billing.ParseLEDES(invoiceText),
		ocgDoc,
		billing.ValidateOpts{
			ClientID:              clientID,
			SubmittedByFirm:       c.Query("submittedByFirm"),
			MatterNumber:          c.Query("matterNumber"),
			GenerateDisputeLetter: c.Query("generateDisputeLetter") == "true",
			TaskID:                c.Query("taskId"),
		},
	)
	billingNormaliseValidation(result)
	c.JSON(http.StatusOK, result)
}

// billingNormaliseValidation matches the TS JSON shape: violations is always
// an array, never null.
func billingNormaliseValidation(r *types.InvoiceValidationResult) {
	if r.Violations == nil {
		r.Violations = []types.InvoiceViolation{}
	}
}

// ─── Time-entry analytics + exports ───────────────────────────────────────────

type billingAgentSummary struct {
	AgentID          string  `json:"agentId"`
	AgentName        string  `json:"agentName"`
	Entries          int     `json:"entries"`
	BillingUnits     int     `json:"billingUnits"`
	BillingAmountUsd float64 `json:"billingAmountUsd"`
}

func (s *Server) handleAgentSummary(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	agentOnly := true
	entries := s.time.List(timekeeping.TimeFilter{
		TaskID:       c.Query("taskId"),
		MatterNumber: c.Query("matterNumber"),
		ClientNumber: c.Query("clientNumber"),
		From:         billingParseTime(c.Query("from")),
		To:           billingParseTime(c.Query("to")),
		AgentOnly:    &agentOnly,
	})

	byAgent := map[string]*billingAgentSummary{}
	var order []string
	for _, e := range entries {
		if e.AgentID == "" {
			continue
		}
		sum := byAgent[e.AgentID]
		if sum == nil {
			name := e.AgentName
			if name == "" {
				name = e.AgentID
			}
			sum = &billingAgentSummary{AgentID: e.AgentID, AgentName: name}
			byAgent[e.AgentID] = sum
			order = append(order, e.AgentID)
		}
		sum.Entries++
		sum.BillingUnits += e.BillingUnits
		if e.BillingAmountUsd != nil {
			sum.BillingAmountUsd += *e.BillingAmountUsd
		}
	}

	out := make([]billingAgentSummary, 0, len(order))
	for _, id := range order {
		out = append(out, *byAgent[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].BillingAmountUsd > out[j].BillingAmountUsd
	})
	c.JSON(http.StatusOK, out)
}

// billingExportFilter builds the shared partner export filter from the query.
func billingExportFilter(c *gin.Context) timekeeping.TimeFilter {
	return timekeeping.TimeFilter{
		ProfileID:    c.Query("profileId"),
		AgentID:      c.Query("agentId"),
		TaskID:       c.Query("taskId"),
		MatterNumber: c.Query("matterNumber"),
		From:         billingParseTime(c.Query("from")),
		To:           billingParseTime(c.Query("to")),
		AgentOnly:    billingParseBool(c.Query("agentOnly")),
	}
}

func (s *Server) handleExportTimeJSON(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	entries := s.time.ExportJSON(billingExportFilter(c))
	if entries == nil {
		entries = []types.TimeEntry{}
	}
	c.JSON(http.StatusOK, entries)
}

func (s *Server) handleExportTimeCSV(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	csv := s.time.ExportCSV(billingExportFilter(c))
	c.Header("Content-Disposition", `attachment; filename="time-entries.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(csv))
}

var billingFilenameRe = regexp.MustCompile(`[^\w\-.]`)

func (s *Server) handleExportTimeLedes(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	matterNumber := c.Query("matterNumber")
	clientNumber := c.Query("clientNumber")
	if matterNumber == "" && clientNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber or clientNumber required"})
		return
	}

	entries := s.time.List(timekeeping.TimeFilter{
		MatterNumber: matterNumber,
		ClientNumber: clientNumber,
		From:         billingParseTime(c.Query("from")),
		To:           billingParseTime(c.Query("to")),
	})
	closed := entries[:0]
	for _, e := range entries {
		if e.EndedAt != nil {
			closed = append(closed, e)
		}
	}

	invoice := c.Query("invoiceNumber")
	if invoice == "" {
		ref := matterNumber
		if ref == "" {
			ref = clientNumber
		}
		invoice = ref + "-" + time.Now().UTC().Format("2006-01-02")
	}

	ledes := billing.ExportLedes1998B(closed, invoice)
	safe := billingFilenameRe.ReplaceAllString(invoice, "_")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safe+".ledes"))
	c.Data(http.StatusOK, "application/edi-x12", []byte(ledes))
}

// ─── OCG suggestions (read side) ──────────────────────────────────────────────

// handleTimeSuggestions lists entries that carry at least one pending OCG
// suggestion. Lawyers are locked to their own entries; partners may filter
// by any profileId.
func (s *Server) handleTimeSuggestions(c *gin.Context) {
	u := getUser(c)
	filter := timekeeping.TimeFilter{
		ClientNumber: c.Query("clientNumber"),
		MatterNumber: c.Query("matterNumber"),
	}
	if auth.IsPartner(u) {
		filter.ProfileID = c.Query("profileId")
	} else {
		filter.ProfileID = u.ProfileID
	}

	entries := s.time.List(filter)
	out := []types.TimeEntry{}
	for _, e := range entries {
		for _, sug := range e.OcgSuggestions {
			if sug.Status == "pending" {
				out = append(out, e)
				break
			}
		}
	}
	c.JSON(http.StatusOK, out)
}

// ─── OCG suggestions (write side) ─────────────────────────────────────────────

type billingRunOcgCheckBody struct {
	ClientNumber string `json:"clientNumber"`
	MatterNumber string `json:"matterNumber"`
	Limit        *int   `json:"limit"`
}

// handleRunOcgCheck bulk-checks closed-or-open time entries against each
// client's OCG document and writes the resulting suggestions back onto the
// entries. Partner only. Response: { checked, withSuggestions }.
func (s *Server) handleRunOcgCheck(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()

	var body billingRunOcgCheckBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	// limit = min(limit ?? 100, 500), floored at 0 (TS slices with the raw value).
	limit := 100
	if body.Limit != nil {
		limit = *body.Limit
	}
	if limit > 500 {
		limit = 500
	}
	if limit < 0 {
		limit = 0
	}

	entries := s.time.List(timekeeping.TimeFilter{
		ClientNumber: body.ClientNumber,
		MatterNumber: body.MatterNumber,
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}

	// Group entries by client ID, keeping only clients that have an OCG doc.
	// Insertion order is preserved to mirror the TS Map iteration.
	byClient := map[string][]types.TimeEntry{}
	var clientOrder []string
	for _, e := range entries {
		if e.ClientNumber == "" {
			continue
		}
		client := s.clients.GetByClientNumber(e.ClientNumber)
		if client == nil {
			continue
		}
		if billingOcg.GetByClient(client.ID) == nil {
			continue
		}
		if _, seen := byClient[client.ID]; !seen {
			clientOrder = append(clientOrder, client.ID)
		}
		byClient[client.ID] = append(byClient[client.ID], e)
	}

	checked := 0
	withSuggestions := 0
	for _, clientID := range clientOrder {
		group := byClient[clientID]
		doc := billingOcg.GetByClient(clientID)
		if doc == nil {
			continue
		}
		for _, e := range group {
			sugs, err := billingOcg.CheckEntry(e, doc)
			if err != nil {
				// The TS semantic checker swallows model errors and yields the
				// mechanical hits only; do the same with the partial result.
				slog.Warn("OCG check batch failed", "clientId", clientID, "err", err)
			}
			if len(sugs) > 0 {
				s.time.SetSuggestions(e.ID, sugs)
				withSuggestions++
			}
		}
		checked += len(group)
	}

	c.JSON(http.StatusOK, gin.H{"checked": checked, "withSuggestions": withSuggestions})
}

type billingSuggestionDecisionBody struct {
	RuleID string `json:"ruleId"`
}

func (s *Server) handleAcceptOcgSuggestion(c *gin.Context) {
	s.handleOcgSuggestionDecision(c, "accepted")
}

func (s *Server) handleDismissOcgSuggestion(c *gin.Context) {
	s.handleOcgSuggestionDecision(c, "dismissed")
}

// handleOcgSuggestionDecision is the shared accept/dismiss flow: lawyers may
// decide on their own entries, partners on any. The outcome is recorded on
// the client's OCG rule stats when the entry maps to a known client.
func (s *Server) handleOcgSuggestionDecision(c *gin.Context, outcome string) {
	s.billingInit()
	u := getUser(c)
	id := c.Param("id")

	var body billingSuggestionDecisionBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.RuleID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ruleId is required"})
		return
	}

	var entry *types.TimeEntry
	for _, e := range s.time.List(timekeeping.TimeFilter{}) {
		if e.ID == id {
			cp := e
			entry = &cp
			break
		}
	}
	if entry == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Time entry not found"})
		return
	}
	if !auth.IsPartner(u) && (u == nil || u.ProfileID != entry.ProfileID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
		return
	}

	var updated *types.TimeEntry
	if outcome == "accepted" {
		updated = s.time.AcceptSuggestion(id, body.RuleID)
	} else {
		updated = s.time.DismissSuggestion(id, body.RuleID)
	}
	if updated == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Suggestion not found"})
		return
	}

	if entry.ClientNumber != "" {
		if client := s.clients.GetByClientNumber(entry.ClientNumber); client != nil {
			billingOcg.RecordOutcome(client.ID, body.RuleID, outcome)
		}
	}
	c.JSON(http.StatusOK, updated)
}

// ─── Outside Counsel Guidelines ───────────────────────────────────────────────

type billingIngestOcgBody struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

func (s *Server) handleIngestOcg(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()

	clientID := c.Param("id")
	client := s.clients.Get(clientID)
	if client == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Client not found"})
		return
	}

	// Rate-limit: disallow re-ingestion within 60 s of last update.
	if existing := billingOcg.GetByClient(clientID); existing != nil {
		if time.Since(existing.UpdatedAt) < 60*time.Second {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "OCG was just updated. Please wait before re-ingesting."})
			return
		}
	}

	title := "Outside Counsel Guidelines"
	text := ""

	if c.ContentType() == "multipart/form-data" {
		form, err := c.MultipartForm()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
			return
		}
		if v := form.Value["title"]; len(v) > 0 && strings.TrimSpace(v[0]) != "" {
			title = strings.TrimSpace(v[0])
		}
		fh := billingFirstFile(c)
		if fh == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
			return
		}
		data, err := billingReadFile(fh)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File upload failed"})
			return
		}
		// The TS backend extracts text from PDF/DOCX/ZIP uploads; that
		// extraction pipeline is not ported, so only plain-text uploads
		// are accepted here.
		if billingLooksBinary(data) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": "Binary documents (PDF/DOCX/ZIP) are not supported by this build; upload plain text or POST JSON {title, text}",
			})
			return
		}
		text = strings.TrimSpace(string(data))
	} else {
		var body billingIngestOcgBody
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		if strings.TrimSpace(body.Title) != "" {
			title = strings.TrimSpace(body.Title)
		}
		text = strings.TrimSpace(body.Text)
	}

	if text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "OCG text is required"})
		return
	}

	doc, err := billingOcg.Ingest(clientID, title, text)
	if err != nil {
		slog.Error("OCG ingestion failed", "clientId", clientID, "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OCG ingestion failed. Please try again."})
		return
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "client.ocg.ingested",
		ActorID: getUser(c).ProfileID,
		Data:    map[string]interface{}{"clientId": clientID, "ruleCount": len(doc.Rules)},
	})
	c.JSON(http.StatusOK, gin.H{"ocg": doc, "ruleCount": len(doc.Rules)})
}

func (s *Server) handleGetOcg(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()
	doc := billingOcg.GetByClient(c.Param("id"))
	if doc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No OCG document found for this client"})
		return
	}
	c.JSON(http.StatusOK, doc)
}

func (s *Server) handleDeleteOcg(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()

	clientID := c.Param("id")
	// The TS handler 404s via clients.clearOcg when the client is unknown.
	if s.clients.Get(clientID) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Client not found"})
		return
	}
	if err := billingOcg.Remove(clientID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "OCG removal failed: " + err.Error()})
		return
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "client.ocg.deleted",
		ActorID: getUser(c).ProfileID,
		Data:    map[string]interface{}{"clientId": clientID},
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// billingOcgRuleStat is one row of the per-rule stats response. AcceptanceRate
// serialises as null until at least one accept/dismiss decision exists.
type billingOcgRuleStat struct {
	RuleID         string                `json:"ruleId"`
	Category       types.OcgRuleCategory `json:"category"`
	Text           string                `json:"text"`
	Severity       string                `json:"severity"`
	Violations     int                   `json:"violations"`
	Accepted       int                   `json:"accepted"`
	Dismissed      int                   `json:"dismissed"`
	AcceptanceRate *float64              `json:"acceptanceRate"`
}

func (s *Server) handleOcgStats(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	s.billingInit()

	clientID := c.Param("id")
	if s.clients.Get(clientID) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Client not found"})
		return
	}
	doc := billingOcg.GetByClient(clientID)
	if doc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No OCG document for this client"})
		return
	}

	rows := make([]billingOcgRuleStat, 0, len(doc.Rules))
	for _, r := range doc.Rules {
		row := billingOcgRuleStat{
			RuleID:   r.ID,
			Category: r.Category,
			Text:     r.Text,
			Severity: r.Severity,
		}
		if st := doc.RuleStats[r.ID]; st != nil {
			row.Violations = st.Violations
			row.Accepted = st.Accepted
			row.Dismissed = st.Dismissed
			if decided := st.Accepted + st.Dismissed; decided > 0 {
				rate := float64(st.Accepted) / float64(decided)
				row.AcceptanceRate = &rate
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Violations > rows[j].Violations })

	c.JSON(http.StatusOK, gin.H{
		"totalRules": len(doc.Rules),
		"ruleStats":  rows,
	})
}

// ─── File-local helpers ───────────────────────────────────────────────────────

// billingParseTime accepts RFC 3339 or bare dates, mirroring `new Date(str)`
// for the formats the UI sends. Unparseable values mean "no constraint".
func billingParseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// billingParseBool maps "true"/"false" to a tri-state pointer (nil = unset),
// matching the TS agentOnly query handling.
func billingParseBool(s string) *bool {
	switch s {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	}
	return nil
}

// billingFirstFile returns the first uploaded file part of a multipart
// request, regardless of field name (the TS handlers use req.file()).
func billingFirstFile(c *gin.Context) *multipart.FileHeader {
	form, err := c.MultipartForm()
	if err != nil {
		return nil
	}
	for _, files := range form.File {
		if len(files) > 0 {
			return files[0]
		}
	}
	return nil
}

const billingMaxUploadBytes = 25 << 20 // 25 MiB

func billingReadFile(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, billingMaxUploadBytes))
}

// billingLooksBinary reports whether an upload is a binary container we
// cannot extract text from (PDF, ZIP/DOCX) or contains NUL bytes.
func billingLooksBinary(data []byte) bool {
	if bytes.HasPrefix(data, []byte("%PDF")) || bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return true
	}
	return bytes.IndexByte(data, 0) >= 0
}
