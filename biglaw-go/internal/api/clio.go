// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Clio routes — OAuth connect/callback/status/disconnect, matter import, and
// time-entry sync. HTTP contract mirrors the TypeScript backend
// (src/mcp/server.ts "Clio OAuth + matter import" section). The OAuth state
// is held server-side (10-minute TTL) instead of the TS signed cookie — the
// Go API is cookieless.

package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/integrations"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// clioMaxImportDocs caps how many matter documents are ingested on import.
const clioMaxImportDocs = 20

// registerClioRoutes adds the Clio OAuth and sync routes. All paths are
// static — no :param segments — so they cannot conflict with concurrently
// registered route trees.
func (s *Server) registerClioRoutes(r *gin.Engine) {
	r.GET("/auth/clio/status", s.handleClioStatus)
	r.GET("/auth/clio/connect", s.handleClioConnect)
	r.GET("/auth/clio/callback", s.handleClioCallback)
	r.DELETE("/auth/clio/disconnect", s.handleClioDisconnect)
	r.POST("/tasks/from-clio-matter", s.handleTaskFromClioMatter)
	r.POST("/time-entries/sync-to-clio", s.handleSyncTimeToClio)
}

// ─── OAuth state store ────────────────────────────────────────────────────────

// clioStateTTL bounds how long an issued OAuth state is accepted (TS cookie
// maxAge was 600 s).
const clioStateTTL = 10 * time.Minute

var clioStates = struct {
	mu sync.Mutex
	m  map[string]time.Time
}{m: map[string]time.Time{}}

// clioStatePut issues and stores a fresh random state, pruning expired ones.
func clioStatePut() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := hex.EncodeToString(b)
	now := time.Now()
	clioStates.mu.Lock()
	for k, issued := range clioStates.m {
		if now.Sub(issued) > clioStateTTL {
			delete(clioStates.m, k)
		}
	}
	clioStates.m[state] = now
	clioStates.mu.Unlock()
	return state, nil
}

// clioStateTake verifies a state and consumes it (single use).
func clioStateTake(state string) bool {
	if state == "" {
		return false
	}
	clioStates.mu.Lock()
	defer clioStates.mu.Unlock()
	issued, ok := clioStates.m[state]
	if !ok {
		return false
	}
	delete(clioStates.m, state)
	return time.Since(issued) <= clioStateTTL
}

// ─── OAuth routes ─────────────────────────────────────────────────────────────

// handleClioStatus reports { connected, firmName, firmId, connectedAt }.
// Any authenticated user may check.
func (s *Server) handleClioStatus(c *gin.Context) {
	c.JSON(http.StatusOK, integrations.DefaultClioClient.Status())
}

// handleClioConnect begins the OAuth flow: issues a state and redirects the
// browser to Clio's consent page. Partner only.
func (s *Server) handleClioConnect(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	if !integrations.DefaultClioClient.IsConfigured() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Clio integration not configured — set CLIO_CLIENT_ID"})
		return
	}
	state, err := clioStatePut()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not generate OAuth state"})
		return
	}
	c.Redirect(http.StatusFound, integrations.DefaultClioClient.AuthURL(state))
}

// handleClioCallback verifies the state, exchanges the code for tokens, and
// sends the browser back to the UI (?clio=connected / ?clio=error). Falls
// back to JSON when no UI URL is configured.
func (s *Server) handleClioCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")

	if code == "" || !clioStateTake(state) {
		s.clioCallbackDone(c, false, "invalid or expired OAuth state")
		return
	}
	if err := integrations.DefaultClioClient.ExchangeCode(code); err != nil {
		slog.Warn("Clio OAuth callback failed", "err", err)
		s.clioCallbackDone(c, false, "token exchange failed")
		return
	}
	slog.Info("Clio connected", "status", integrations.DefaultClioClient.Status())
	// The callback arrives as a bare browser redirect — it may run outside the
	// authenticated session, so the actor can be absent.
	actorID := ""
	if u := getUser(c); u != nil {
		actorID = u.ProfileID
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "clio.connected",
		ActorID: actorID,
		Data:    integrations.DefaultClioClient.Status(),
	})
	s.clioCallbackDone(c, true, "")
}

// clioCallbackDone finishes the OAuth callback: browser redirect to the UI
// when configured, JSON otherwise (headless / curl flows).
func (s *Server) clioCallbackDone(c *gin.Context, ok bool, reason string) {
	if ui := s.cfg.Auth.UIURL; ui != "" {
		result := "connected"
		if !ok {
			result = "error"
		}
		c.Redirect(http.StatusFound, ui+"?clio="+result)
		return
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": reason})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": integrations.DefaultClioClient.Status()})
}

// handleClioDisconnect revokes the stored tokens. Partner only.
func (s *Server) handleClioDisconnect(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	if err := integrations.DefaultClioClient.Disconnect(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear Clio tokens: " + err.Error()})
		return
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "clio.disconnected",
		ActorID: getUser(c).ProfileID,
	})
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ─── Matter import ────────────────────────────────────────────────────────────

type fromClioMatterBody struct {
	MatterID     int    `json:"matterId"`
	WorkflowType string `json:"workflowType"`
	Jurisdiction string `json:"jurisdiction"`
}

// handleTaskFromClioMatter imports a Clio matter: fetches details, downloads
// and ingests its documents (plain-text only — the Go backend has no
// PDF/DOCX pipeline), and submits a task. Partner only.
func (s *Server) handleTaskFromClioMatter(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	if !integrations.DefaultClioClient.IsConnected() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Clio not connected — visit /auth/clio/connect"})
		return
	}
	u := getUser(c)

	var body fromClioMatterBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.MatterID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "matterId is required"})
		return
	}

	matterRaw, err := integrations.DefaultClioClient.GetMatter(body.MatterID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Clio getMatter failed: " + err.Error()})
		return
	}

	matter := clioDataMap(matterRaw)
	displayNumber := clioStr(matter, "display_number")
	description := clioStr(matter, "description")
	if description == "" {
		description = "Clio matter " + displayNumber
	}
	clioArea := ""
	if pa, ok := matter["practice_area"].(map[string]interface{}); ok {
		clioArea, _ = pa["name"].(string)
	}
	practiceArea := clioMapPracticeArea(clioArea)

	// Ingest documents from Clio (cap at clioMaxImportDocs). Best-effort:
	// individual document failures are logged, never fatal.
	documentsIngested := 0
	if docsRaw, derr := integrations.DefaultClioClient.ListDocuments(body.MatterID, clioMaxImportDocs); derr != nil {
		slog.Warn("Clio listDocuments failed", "matterId", body.MatterID, "err", derr)
	} else {
		for _, d := range clioDataList(docsRaw) {
			if documentsIngested >= clioMaxImportDocs {
				break
			}
			docID := clioInt(d, "id")
			name := clioStr(d, "name")
			if docID == 0 || name == "" {
				continue
			}
			buf, dlErr := integrations.DefaultClioClient.DownloadDocument(docID)
			if dlErr != nil {
				slog.Warn("Clio document ingest failed", "docId", docID, "name", name, "err", dlErr)
				continue
			}
			content, ok := clioDocText(buf, name)
			if !ok || strings.TrimSpace(content) == "" {
				continue
			}
			if _, ingErr := s.knowledge.Ingest(reqIdentity(c), types.Document{
				Title:        name,
				Content:      content,
				Source:       "clio",
				DocumentType: "matter_file",
				OwnerID:      u.ProfileID,
				IngestedAt:   time.Now(),
			}); ingErr != nil {
				slog.Warn("Clio document ingest failed", "docId", docID, "name", name, "err", ingErr)
				continue
			}
			documentsIngested++
		}
	}

	workflowType := types.WorkflowType(body.WorkflowType)
	if workflowType == "" {
		workflowType = types.WorkflowRoundtable
	}
	taskDesc := fmt.Sprintf("[Clio matter %s] %s (Practice area: %s)", displayNumber, description, practiceArea)
	task, err := s.orch.SubmitTask(orchestrator.SubmitParams{
		Description:  taskDesc,
		WorkflowType: workflowType,
		// clientNumber deliberately omitted — Clio's internal client ID is not
		// the firm's own numbering scheme; the classifier derives the client.
		MatterNumber:       displayNumber,
		Jurisdiction:       body.Jurisdiction,
		CreatedByProfileID: u.ProfileID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.orch.AssignLawyers(task.ID, []string{u.ProfileID}, u.ProfileID)

	out := s.orch.GetTask(task.ID)
	if out == nil {
		out = task
	}
	c.JSON(http.StatusCreated, gin.H{"task": out, "documentsIngested": documentsIngested})
}

// ─── Time-entry sync ──────────────────────────────────────────────────────────

type syncToClioBody struct {
	ClioMatterID int    `json:"clioMatterId"`
	MatterNumber string `json:"matterNumber"`
	From         string `json:"from"`
	To           string `json:"to"`
}

// handleSyncTimeToClio pushes billable time entries to Clio as TimeEntry
// activities. Synced entries are stamped with clioSyncedAt so re-runs skip
// them. Partner only.
func (s *Server) handleSyncTimeToClio(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	if !integrations.DefaultClioClient.IsConnected() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Clio not connected — visit /auth/clio/connect"})
		return
	}

	var body syncToClioBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.ClioMatterID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clioMatterId is required"})
		return
	}

	filter := timekeeping.TimeFilter{MatterNumber: body.MatterNumber}
	if body.From != "" {
		if t, err := time.Parse(time.RFC3339, body.From); err == nil {
			filter.From = &t
		}
	}
	if body.To != "" {
		if t, err := time.Parse(time.RFC3339, body.To); err == nil {
			filter.To = &t
		}
	}

	toSync, skipped := timekeeping.SplitClioUnsynced(s.time.List(filter))

	synced, errCount := 0, 0
	for _, entry := range toSync {
		dateOn := entry.StartedAt.UTC().Format("2006-01-02")
		_, err := integrations.DefaultClioClient.CreateActivity(
			body.ClioMatterID,
			entry.Description,
			dateOn,
			timekeeping.ClioDurationHours(entry),
		)
		if err != nil {
			slog.Warn("Clio sync activity failed", "entryId", entry.ID, "err", err)
			errCount++
			continue
		}
		s.time.MarkClioSynced(entry.ID)
		synced++
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "time.synced_to_clio",
		ActorID: getUser(c).ProfileID,
		Data: map[string]interface{}{
			"clioMatterId": body.ClioMatterID, "matterNumber": body.MatterNumber,
			"synced": synced, "skipped": skipped, "errors": errCount,
		},
	})
	c.JSON(http.StatusOK, gin.H{"synced": synced, "skipped": skipped, "errors": errCount})
}

// ─── File-local helpers ───────────────────────────────────────────────────────

// clioPracticeAreaKeywords maps Clio practice-area substrings to the Go
// canonical types.PracticeAreas names. First match wins; keyword order keeps
// the more specific phrases ahead of their generic stems.
var clioPracticeAreaKeywords = []struct{ keyword, area string }{
	{"intellectual property", "Intellectual Property"},
	{"patent", "Intellectual Property"},
	{"trademark", "Intellectual Property"},
	{"real estate", "Real Estate"},
	{"capital markets", "Capital Markets"},
	{"securities", "Capital Markets"},
	{"corporate", "Corporate & M&A"},
	{"m&a", "Corporate & M&A"},
	{"merger", "Corporate & M&A"},
	{"antitrust", "Competition & Antitrust"},
	{"competition", "Competition & Antitrust"},
	{"employment", "Employment & Labour"},
	{"labour", "Employment & Labour"},
	{"labor", "Employment & Labour"},
	{"banking", "Banking & Finance"},
	{"finance", "Banking & Finance"},
	{"litigation", "Litigation & Dispute Resolution"},
	{"dispute", "Litigation & Dispute Resolution"},
	{"tax", "Tax"},
	{"regulatory", "Regulatory & Compliance"},
	{"compliance", "Regulatory & Compliance"},
	{"privacy", "Data Privacy & Cybersecurity"},
	{"cyber", "Data Privacy & Cybersecurity"},
	{"immigration", "Immigration"},
	{"bankruptcy", "Insolvency & Restructuring"},
	{"insolvency", "Insolvency & Restructuring"},
	{"restructuring", "Insolvency & Restructuring"},
	{"insurance", "Insurance"},
	{"environmental", "Environmental & Climate"},
	{"climate", "Environmental & Climate"},
}

// clioMapPracticeArea best-effort maps a Clio practice-area name onto the
// canonical list, defaulting to litigation (the TS handler's fallback).
func clioMapPracticeArea(clioArea string) string {
	lower := strings.ToLower(clioArea)
	for _, m := range clioPracticeAreaKeywords {
		if strings.Contains(lower, m.keyword) {
			return m.area
		}
	}
	return "Litigation & Dispute Resolution"
}

// clioDataMap unwraps a Clio single-object response ({ "data": {...} }).
func clioDataMap(raw interface{}) map[string]interface{} {
	if m, ok := raw.(map[string]interface{}); ok {
		if data, ok := m["data"].(map[string]interface{}); ok {
			return data
		}
	}
	return map[string]interface{}{}
}

// clioDataList unwraps a Clio collection response ({ "data": [...] }).
func clioDataList(raw interface{}) []map[string]interface{} {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	list, ok := m["data"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(map[string]interface{}); ok {
			out = append(out, obj)
		}
	}
	return out
}

func clioStr(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func clioInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok { // JSON numbers decode as float64
		return int(v)
	}
	return 0
}

// clioImportTextExts mirrors the plain-text list of the upload handler
// (content.go) — the Go backend has no PDF/DOCX extraction pipeline.
var clioImportTextExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true,
	".json": true, ".log": true, ".text": true, ".rtf": true,
}

// clioDocText returns the document text when the payload is plain text (by
// extension, or any valid NUL-free UTF-8 body), capped at 50k chars like the
// TS importer. Binary formats return ok=false.
func clioDocText(buf []byte, filename string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(filename))
	isText := clioImportTextExts[ext] ||
		(utf8.Valid(buf) && !strings.ContainsRune(string(buf), 0))
	if !isText {
		return "", false
	}
	text := string(buf)
	if len(text) > 50_000 {
		text = strutil.Truncate(text, 50_000)
	}
	return text, true
}
