// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// REST routes for the drafting/research engines — playbooks, contract
// redline, headnotes, precedents, citation checking, client briefing.
// HTTP contract mirrors the TS backend (src/mcp/server.ts): request fields,
// response shapes, status codes and partner-gating match the TS handlers.
//
// Engines are constructed lazily on first use (file-scoped sync.Once): they
// only need a provider + the playbook store, and most requests never touch
// them.

package api

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/briefing"
	"github.com/discover-legal/biglaw-go/internal/citations"
	"github.com/discover-legal/biglaw-go/internal/headnotes"
	"github.com/discover-legal/biglaw-go/internal/knowledge"
	"github.com/discover-legal/biglaw-go/internal/playbook"
	"github.com/discover-legal/biglaw-go/internal/precedent"
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

// ─── Lazy engine construction ─────────────────────────────────────────────────

type enginesBundle struct {
	playbooks *playbook.Store
	builder   *playbook.Builder
	redline   *redline.Engine
	headnotes *headnotes.Engine
	precedent *precedent.Generator
	citations *citations.Engine
	briefing  *briefing.Engine
}

var (
	enginesOnce    sync.Once
	enginesShared  *enginesBundle
	enginesInitErr error
)

// engines builds all engine singletons on first use. Model selection goes
// through routing.SelectModel so local-inference configs are honored: with
// LOCAL_INFERENCE_TIERS=all the whole drafting bench runs on the local
// OpenAI-compatible endpoint; otherwise the TS engines' cloud model mix
// (Haiku extract / Sonnet analyse / Opus draft).
func (s *Server) engines() (*enginesBundle, error) {
	enginesOnce.Do(func() {
		primary := routing.SelectModel(s.cfg, routing.SelectParams{TaskType: routing.TaskReasoning})
		sonnetID, haikuID, opusID := routing.ModelSonnet, routing.ModelHaiku, routing.ModelOpus
		if routing.IsLocalModel(primary) || routing.IsOllamaModel(primary) {
			resolved := routing.ResolveModelID(primary)
			sonnetID, haikuID, opusID = resolved, resolved, resolved
		}

		provider, err := s.orch.Providers().Get(primary)
		if err != nil {
			enginesInitErr = err
			return
		}

		store := playbook.New(s.cfg.Persistence.PlaybooksFile)
		if err := store.Init(); err != nil {
			enginesInitErr = err
			return
		}

		enginesShared = &enginesBundle{
			playbooks: store,
			builder:   playbook.NewBuilder(provider, haikuID),
			redline:   redline.New(provider, sonnetID, haikuID),
			headnotes: headnotes.New(provider, sonnetID, haikuID),
			precedent: precedent.New(provider, opusID, haikuID),
			citations: citations.New(provider, haikuID),
			briefing:  briefing.New(provider, sonnetID),
		}
	})
	if enginesInitErr != nil {
		return nil, enginesInitErr
	}
	return enginesShared, nil
}

// enginesRequire resolves the engine bundle or writes a 500 and returns nil.
func (s *Server) enginesRequire(c *gin.Context) *enginesBundle {
	eng, err := s.engines()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "engines unavailable: " + err.Error()})
		return nil
	}
	return eng
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
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	list := eng.playbooks.List(
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
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	pb := eng.playbooks.GetByID(c.Param("id"))
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, pb)
}

func (s *Server) handleResolvePlaybook(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	eng := s.enginesRequire(c)
	if eng == nil {
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
		c.JSON(http.StatusOK, eng.playbooks.ResolveAll(opts))
		return
	}
	resolved := eng.playbooks.Resolve(clauseType, opts)
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
	eng := s.enginesRequire(c)
	if eng == nil {
		return
	}
	if !eng.playbooks.Delete(c.Param("id")) {
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		CaseName:     body.CaseName,
		Citation:     body.Citation,
		Court:        body.Court,
		DateFiled:    body.DateFiled,
		Jurisdiction: body.Jurisdiction,
		TaskID:       body.TaskID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
	c.JSON(http.StatusOK, eng.citations.Check(q, c.Query("taskId")))
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
	c.JSON(http.StatusOK, eng.citations.Check(body.Query, body.TaskID))
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

	result, err := eng.briefing.Generate(client, allTasks, allEntries, briefing.GenerateOpts{
		Knowledge:       enginesKnowledgeSearcher{store: s.knowledge},
		TaskID:          c.Query("taskId"),
		BriefingDate:    c.Query("briefingDate"),
		IndustryContext: c.Query("industryContext"),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
