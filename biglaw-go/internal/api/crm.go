// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// CRM routes — the firm side of the neurosymbolic client profiles. Any
// authenticated firm member reads profiles and works the consent queue;
// lawyer-proposed facts await the client's approval through the intake
// channel (see intake.go), completing the bidirectional-consent loop.
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/crm"
	"github.com/discover-legal/biglaw-go/internal/modules"
)

// AttachCRM registers the CRM endpoints. No-op when svc is nil or the crm
// module is disabled.
func (s *Server) AttachCRM(svc *crm.Service) {
	if svc == nil || !modules.Default.Enabled("crm") {
		return
	}
	s.crm = svc
	g := s.router.Group("/crm")

	g.GET("/profiles", func(c *gin.Context) {
		profiles, err := svc.List(reqIdentity(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"profiles": profiles})
	})

	g.GET("/profiles/:id", func(c *gin.Context) {
		view, err := svc.View(reqIdentity(c), c.Param("id"))
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
		facts, err := svc.Facts(reqIdentity(c), c.Param("id"), c.Query("status"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"facts": emptyIfNilFacts(facts)})
	})

	// A lawyer proposes facts about the client → the client approves them
	// through the portal.
	g.POST("/profiles/:id/facts", func(c *gin.Context) {
		u := getUser(c)
		var body struct {
			Facts []crm.FactInput `json:"facts"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}
		proposals, err := svc.Propose(reqIdentity(c), c.Param("id"),
			crm.Actor{Role: crm.RoleLawyer, ID: u.ProfileID}, "lawyer", body.Facts)
		if err != nil {
			c.JSON(crmErrStatus(err), gin.H{"error": err.Error()})
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
		hits, err := svc.Query(reqIdentity(c), c.Param("id"), body.Query, body.TopK)
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
		facts, err := svc.PendingQueue(reqIdentity(c), c.DefaultQuery("approver", crm.RoleLawyer))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"proposals": emptyIfNilFacts(facts)})
	})

	// A lawyer decides a client-proposed fact.
	g.POST("/proposals/:id/decision", func(c *gin.Context) {
		u := getUser(c)
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
		fact, err := svc.Decide(reqIdentity(c), c.Param("id"),
			crm.Actor{Role: crm.RoleLawyer, ID: u.ProfileID}, body.Decision == "approve", body.Note)
		if err != nil {
			c.JSON(crmErrStatus(err), gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"fact": fact})
	})
}

func crmErrStatus(err error) int {
	switch {
	case errors.Is(err, crm.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, crm.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, crm.ErrNotPending):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func emptyIfNilFacts[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
