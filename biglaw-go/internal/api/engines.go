// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// REST routes for the drafting/research engines — playbooks, contract
// redline, headnotes, precedents, citation checking, client briefing.
// HTTP contract mirrors the TS backend (src/mcp/server.ts): request fields,
// response shapes, status codes and partner-gating match the TS handlers.
//
// Engines are thin structs over (provider, model IDs) built per request, so a
// provider configured after startup is picked up and a missing one is a typed
// 503 for that request only. The playbook store is opened once (retried until
// it succeeds) and playbook reads never need a model provider.

package api

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/briefing"
	"github.com/discover-legal/biglaw-go/internal/citations"
	"github.com/discover-legal/biglaw-go/internal/headnotes"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/precedent"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/redline"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// registerEnginesRoutes wires the engine routes onto the router. Static
// segments ("build", "resolve") coexist with ":id" at the same position —
// supported since gin 1.7. The /clients tree already uses ":id", so the
// briefing route reuses that param name.
func (s *Server) registerEnginesRoutes(r *gin.Engine) {
	r.GET("/playbooks", s.handleListPlaybooks)
	r.POST("/playbooks/build", s.handleBuildPlaybook)
	r.GET("/playbooks/resolve/:clauseType", s.handleResolvePlaybook)
	r.GET("/playbooks/:id", s.handleGetPlaybook)
	r.DELETE("/playbooks/:id", s.handleDeletePlaybook)

	r.POST("/redline", s.handleRedline)
	r.POST("/headnotes/generate", s.handleGenerateHeadnotes)
	r.POST("/precedents/generate", s.handleGeneratePrecedent)

	r.GET("/citations/check", s.handleCitationCheckGet)
	r.POST("/citations/check", s.handleCitationCheckPost)

	r.GET("/clients/:id/briefing", s.handleClientBriefing)
}

// ─── Engine construction ──────────────────────────────────────────────────────

type enginesBundle struct {
	playbooks *playbook.Store
	builder   *playbook.Builder
	redline   *redline.Engine
	headnotes *headnotes.Engine
	precedent *precedent.Generator
	citations *citations.Engine
	briefing  *briefing.Engine
}

// The playbook store is model-free state: it is opened on first use under a
// mutex and only a SUCCESSFUL open is kept, so a transient failure is retried
// on the next request instead of poisoning the process (as a sync.Once did).
var (
	playbookStoreMu     sync.Mutex
	playbookStoreShared *playbook.Store
)

func (s *Server) playbookStore() (*playbook.Store, error) {
	playbookStoreMu.Lock()
	defer playbookStoreMu.Unlock()
	if playbookStoreShared != nil {
		return playbookStoreShared, nil
	}
	st := playbook.New(s.cfg.Persistence.PlaybooksFile)
	if err := st.Init(); err != nil {
		return nil, err
	}
	playbookStoreShared = st
	return st, nil
}

// engineModels names the provider + mid/light/heavy model IDs for one request.
type engineModels struct {
	provider          providers.Provider
	mid, light, heavy string
}

// engineModelsFor resolves the model provider per request (so configuring a
// provider takes effect without a restart). Model selection goes through
// routing.SelectModel so local-inference configs are honored: with
// LOCAL_INFERENCE_TIERS=all the whole drafting bench runs on the local
// OpenAI-compatible endpoint; otherwise the stack's heavy/mid/light mix. A
// variable so handler tests can substitute a scripted provider.
var engineModelsFor = func(s *Server) (engineModels, error) {
	primary := routing.SelectModel(s.cfg, routing.SelectParams{TaskType: routing.TaskReasoning})
	mid, light, heavy := routing.Mid(s.cfg), routing.Light(s.cfg), routing.Heavy(s.cfg)
	if routing.IsLocalModel(primary) || routing.IsOllamaModel(primary) {
		resolved := routing.ResolveModelID(primary)
		mid, light, heavy = resolved, resolved, resolved
	}
	provider, err := s.orch.Providers().Get(primary)
	if err != nil {
		return engineModels{}, err
	}
	return engineModels{provider: provider, mid: mid, light: light, heavy: heavy}, nil
}

// engines builds the engine set for one request. Engines are thin structs
// over (provider, model IDs); construction is cheap.
func (s *Server) engines() (*enginesBundle, error) {
	store, err := s.playbookStore()
	if err != nil {
		return nil, err
	}
	m, err := engineModelsFor(s)
	if err != nil {
		return nil, err
	}
	return &enginesBundle{
		playbooks: store,
		builder:   playbook.NewBuilder(m.provider, m.light),
		redline:   redline.New(m.provider, m.mid, m.light),
		headnotes: headnotes.New(m.provider, m.mid, m.light),
		precedent: precedent.New(m.provider, m.heavy, m.light),
		citations: citations.New(m.provider, m.light),
		briefing:  briefing.New(m.provider, m.mid),
	}, nil
}

// enginesRequire resolves the engine set or writes an error and returns nil:
// a typed 503 when no model provider is configured, else a generic 500.
func (s *Server) enginesRequire(c *gin.Context) *enginesBundle {
	eng, err := s.engines()
	if err != nil {
		writeModelError(c, err, "The drafting engines are unavailable.")
		return nil
	}
	return eng
}

// playbooksRequire resolves only the playbook store — reading and deleting
// playbooks never needs a model provider.
func (s *Server) playbooksRequire(c *gin.Context) *playbook.Store {
	st, err := s.playbookStore()
	if err != nil {
		slog.Error("playbook store unavailable", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "The playbook store is unavailable."})
		return nil
	}
	return st
}

// enginesKnowledgeSearcher adapts the knowledge store to the simplified
// Search(query, topK) interface the briefing/precedent engines and the
// playbook builder expect. Engines search firm-wide (no owner filter),
// matching the TS backend.
type enginesKnowledgeSearcher struct {
	store *knowledge.Store
}

func (k enginesKnowledgeSearcher) Search(query string, topK int) []types.SearchResult {
	results, err := k.store.Search(query, knowledge.SearchOpts{TopK: topK})
	if err != nil || results == nil {
		return []types.SearchResult{}
	}
	return results
}

// ─── Playbooks ────────────────────────────────────────────────────────────────

// Playbooks hold confidential negotiation positions and absolute red lines
// (client, matter, and per-lawyer tiers). All playbook endpoints are restricted
// to partners, matching the TS handlers (typescript-final src/mcp/server.ts).

func (s *Server) handleListPlaybooks(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	store := s.playbooksRequire(c)
	if store == nil {
		return
	}
	list := store.List(
		types.PlaybookScope(c.Query("scope")),
		c.Query("ownerId"),
		c.Query("practiceArea"),
	)
	if list == nil {
		list = []types.Playbook{}
	}
	c.JSON(http.StatusOK, list)
}

func (s *Server) handleGetPlaybook(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	store := s.playbooksRequire(c)
	if store == nil {
		return
	}
	pb := store.GetByID(c.Param("id"))
	if pb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Playbook not found"})
		return
	}
	c.JSON(http.StatusOK, pb)
}

type buildPlaybookBody struct {
	Scope        string   `json:"scope"`
	OwnerID      string   `json:"ownerId"`
	OwnerName    string   `json:"ownerName"`
	PracticeArea string   `json:"practiceArea"`
	Jurisdiction string   `json:"jurisdiction"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	ClauseTypes  []string `json:"clauseTypes"`
	TaskID       string   `json:"taskId"`
}

func (s *Server) handleBuildPlaybook(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	var body buildPlaybookBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.PracticeArea == "" || body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "practiceArea and name required"})
		return
	}

	// Unrecognised scopes fall back to "firm", as in the TS handler.
	resolvedScope := types.PlaybookScopeFirm
	switch types.PlaybookScope(body.Scope) {
	case types.PlaybookScopeFirm, types.PlaybookScopeClient, types.PlaybookScopeMatter, types.PlaybookScopePersonal:
		resolvedScope = types.PlaybookScope(body.Scope)
	}

	ks := enginesKnowledgeSearcher{store: s.knowledge}
	pb, err := eng.builder.Build(eng.playbooks, ks.Search, playbook.BuildOpts{
		Ctx:          c.Request.Context(),
		Scope:        resolvedScope,
		OwnerID:      body.OwnerID,
		OwnerName:    body.OwnerName,
		PracticeArea: body.PracticeArea,
		Jurisdiction: body.Jurisdiction,
		Name:         body.Name,
		Description:  body.Description,
		ClauseTypes:  body.ClauseTypes,
		TaskID:       body.TaskID,
	})
	if err != nil {
		writeModelError(c, err, "")
		return
	}
	c.JSON(http.StatusCreated, pb)
}

func (s *Server) handleResolvePlaybook(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	store := s.playbooksRequire(c)
	if store == nil {
		return
	}
	clauseType := c.Param("clauseType")
	opts := playbook.ResolveOpts{
		PracticeArea: c.Query("practiceArea"),
		MatterNumber: c.Query("matterNumber"),
		ClientID:     c.Query("clientId"),
		ProfileID:    c.Query("profileId"),
	}
	if clauseType == "*" {
		c.JSON(http.StatusOK, store.ResolveAll(opts))
		return
	}
	resolved := store.Resolve(clauseType, opts)
	if resolved == nil {
		c.JSON(http.StatusOK, gin.H{
			"clauseType": clauseType,
			"resolved":   nil,
			"message":    "No playbook entry found for this clause type",
		})
		return
	}
	c.JSON(http.StatusOK, resolved)
}

func (s *Server) handleDeletePlaybook(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	store := s.playbooksRequire(c)
	if store == nil {
		return
	}
	if !store.Delete(c.Param("id")) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Playbook not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// ─── Contract redline ─────────────────────────────────────────────────────────

type redlineBody struct {
	DocumentText  string `json:"documentText"`
	PracticeArea  string `json:"practiceArea"`
	Jurisdiction  string `json:"jurisdiction"`
	MatterNumber  string `json:"matterNumber"`
	ClientID      string `json:"clientId"`
	ProfileID     string `json:"profileId"`
	DocumentID    string `json:"documentId"`
	DocumentTitle string `json:"documentTitle"`
	TaskID        string `json:"taskId"`
}

func (s *Server) handleRedline(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	var body redlineBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.DocumentText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "documentText required"})
		return
	}

	// Synchronous, like the TS handler: the engine makes several model calls
	// and the report is returned in the response body.
	report, err := eng.redline.Redline(body.DocumentText, eng.playbooks, redline.RedlineOpts{
		Ctx:           c.Request.Context(),
		PracticeArea:  body.PracticeArea,
		Jurisdiction:  body.Jurisdiction,
		MatterNumber:  body.MatterNumber,
		ClientID:      body.ClientID,
		ProfileID:     body.ProfileID,
		DocumentID:    body.DocumentID,
		DocumentTitle: body.DocumentTitle,
		TaskID:        body.TaskID,
	})
	if err != nil {
		writeModelError(c, err, "")
		return
	}
	c.JSON(http.StatusOK, report)
}

// ─── Headnote generation ──────────────────────────────────────────────────────

type headnotesBody struct {
	OpinionText  string `json:"opinionText"`
	CaseName     string `json:"caseName"`
	Citation     string `json:"citation"`
	Court        string `json:"court"`
	DateFiled    string `json:"dateFiled"`
	Jurisdiction string `json:"jurisdiction"`
	TaskID       string `json:"taskId"`
}

func (s *Server) handleGenerateHeadnotes(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	var body headnotesBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.OpinionText == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "opinionText required"})
		return
	}

	report, err := eng.headnotes.Generate(body.OpinionText, headnotes.GenerateOpts{
		Ctx:          c.Request.Context(),
		CaseName:     body.CaseName,
		Citation:     body.Citation,
		Court:        body.Court,
		DateFiled:    body.DateFiled,
		Jurisdiction: body.Jurisdiction,
		TaskID:       body.TaskID,
	})
	if err != nil {
		writeModelError(c, err, "")
		return
	}
	c.JSON(http.StatusOK, report)
}

// ─── Precedent generation ─────────────────────────────────────────────────────

type precedentBody struct {
	DocumentType        string `json:"documentType"`
	PracticeArea        string `json:"practiceArea"`
	Jurisdiction        string `json:"jurisdiction"`
	ActingFor           string `json:"actingFor"`
	MatterNumber        string `json:"matterNumber"`
	ClientID            string `json:"clientId"`
	ProfileID           string `json:"profileId"`
	SpecialInstructions string `json:"specialInstructions"`
	TaskID              string `json:"taskId"`
}

func (s *Server) handleGeneratePrecedent(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	var body precedentBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.DocumentType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "documentType required"})
		return
	}

	doc, err := eng.precedent.Generate(
		body.DocumentType,
		enginesKnowledgeSearcher{store: s.knowledge},
		eng.playbooks,
		precedent.GenerateOpts{
			Ctx:                 c.Request.Context(),
			PracticeArea:        body.PracticeArea,
			Jurisdiction:        body.Jurisdiction,
			ActingFor:           body.ActingFor,
			MatterNumber:        body.MatterNumber,
			ClientID:            body.ClientID,
			ProfileID:           body.ProfileID,
			SpecialInstructions: body.SpecialInstructions,
			TaskID:              body.TaskID,
		},
	)
	if err != nil {
		writeModelError(c, err, "")
		return
	}
	c.JSON(http.StatusOK, doc)
}

// ─── Citation check ───────────────────────────────────────────────────────────

func (s *Server) handleCitationCheckGet(c *gin.Context) {
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q (citation string) required"})
		return
	}
	s.writeCitationCheck(c, eng, q, c.Query("taskId"))
}

// writeCitationCheck answers a citation check: a CourtListener failure is a
// 200 "unknown — not checked" result; a model failure is a typed error
// (502/503/504), never a treatment verdict.
func (s *Server) writeCitationCheck(c *gin.Context, eng *enginesBundle, query, taskID string) {
	res, err := eng.citations.Check(c.Request.Context(), query, taskID)
	if err != nil {
		writeModelError(c, err, "The citation check failed.")
		return
	}
	c.JSON(http.StatusOK, res)
}

type citationCheckBody struct {
	Query  string `json:"query"`
	TaskID string `json:"taskId"`
}

func (s *Server) handleCitationCheckPost(c *gin.Context) {
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	var body citationCheckBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}
	if body.Query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query required"})
		return
	}
	s.writeCitationCheck(c, eng, body.Query, body.TaskID)
}

// ─── Client briefing ──────────────────────────────────────────────────────────

// handleClientBriefing generates a hub-and-spoke client intelligence briefing.
// Partner-only, like the TS handler (and like every other /clients route): it
// aggregates the full client relationship — matters, mail, time entries.
func (s *Server) handleClientBriefing(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}

	id := c.Param("id")
	client := s.clients.Get(id)
	if client == nil {
		client = s.clients.GetByClientNumber(id)
	}
	if client == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Client not found"})
		return
	}

	allTasks := s.orch.ListTasks()
	allEntries := s.time.List(timekeeping.TimeFilter{})

	// A failed synthesis is a 200 briefing marked degraded:true (record-
	// derived statistics only, no legal analysis); a cancelled client aborts.
	result, err := eng.briefing.Generate(client, allTasks, allEntries, briefing.GenerateOpts{
		Ctx:             c.Request.Context(),
		Knowledge:       enginesKnowledgeSearcher{store: s.knowledge},
		TaskID:          c.Query("taskId"),
		BriefingDate:    c.Query("briefingDate"),
		IndustryContext: c.Query("industryContext"),
	})
	if err != nil {
		writeModelError(c, err, "The briefing could not be generated.")
		return
	}
	c.JSON(http.StatusOK, result)
}
