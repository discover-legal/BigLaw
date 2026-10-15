// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// CRM HTTP surface — the firm side of the client profiles. Any authenticated
// firm member reads profiles and works the consent queue; lawyer-proposed
// facts await the client's approval through the intake module's portal
// routes, completing the bidirectional-consent loop.
package crm

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/modules"
)

// ModuleName is the registry name of the CRM module.
const ModuleName = "crm"

// HTTP is the CRM module's route set (implements modules.HTTPModule).
type HTTP struct{ svc *Service }

// NewHTTP builds the CRM HTTP module.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

// ModuleName implements modules.HTTPModule.
func (h *HTTP) ModuleName() string { return ModuleName }

// PublicRoute implements modules.HTTPModule: every CRM route requires a firm
// credential.
func (h *HTTP) PublicRoute(method, path string) bool { return false }

// Mount registers the /crm endpoints.
func (h *HTTP) Mount(r gin.IRouter, host modules.Host) {
	g := r.Group("/crm")

	g.GET("/profiles", func(c *gin.Context) {
		profiles, err := h.svc.List(host.Identity(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profiles": profiles})
	})

	g.GET("/profiles/:id", func(c *gin.Context) {
		view, err := h.svc.View(host.Identity(c), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if view == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
			return
		}
		c.JSON(http.StatusOK, view)
	})

	g.GET("/profiles/:id/facts", func(c *gin.Context) {
		facts, err := h.svc.Facts(host.Identity(c), c.Param("id"), c.Query("status"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"facts": NonNil(facts)})
	})

	// A lawyer proposes facts about the client → the client approves them
	// through the portal.
	g.POST("/profiles/:id/facts", func(c *gin.Context) {
		u := host.User(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		var body struct {
			Facts []FactInput `json:"facts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		proposals, err := h.svc.Propose(host.Identity(c), c.Param("id"),
			Actor{Role: RoleLawyer, ID: u.ProfileID}, "lawyer", body.Facts)
		if err != nil {
			c.JSON(ErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"proposals": proposals})
	})

	// Semantic profile query (neural layer).
	g.POST("/profiles/:id/query", func(c *gin.Context) {
		var body struct {
			Query string `json:"query"`
			TopK  int    `json:"topK"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		hits, err := h.svc.Query(host.Identity(c), c.Param("id"), body.Query, body.TopK)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"results": hits})
	})

	// Firm-wide pending queue. approver=lawyer (default) lists client
	// proposals awaiting the firm; approver=client lists what's out with
	// clients for approval.
	g.GET("/proposals", func(c *gin.Context) {
		facts, err := h.svc.PendingQueue(host.Identity(c), c.DefaultQuery("approver", RoleLawyer))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"proposals": NonNil(facts)})
	})

	// A lawyer decides a client-proposed fact.
	g.POST("/proposals/:id/decision", func(c *gin.Context) {
		u := host.User(c)
		if u == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
			return
		}
		var body struct {
			Decision string `json:"decision"`
			Note     string `json:"note"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		if body.Decision != "approve" && body.Decision != "reject" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "decision must be approve or reject"})
			return
		}
		fact, err := h.svc.Decide(host.Identity(c), c.Param("id"),
			Actor{Role: RoleLawyer, ID: u.ProfileID}, body.Decision == "approve", body.Note)
		if err != nil {
			c.JSON(ErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"fact": fact})
	})
}

// ErrStatus maps CRM errors to HTTP status codes.
func ErrStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotPending):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// NonNil returns in, or an empty slice for nil (JSON [] not null).
func NonNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
