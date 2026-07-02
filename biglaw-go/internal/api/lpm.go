// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// LPM REST routes — attached after construction so the api.New signature stays
// stable and the routes only exist when the LPM subsystem is enabled.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/lpm"
)

// AttachLPM registers the LPM status-report endpoints on the server's router.
// No-op when svc is nil (LPM disabled).
func (s *Server) AttachLPM(svc *lpm.Service) {
	if svc == nil {
		return
	}
	s.lpm = svc // expose to the bot facade (report/portfolio commands)
	g := s.router.Group("/lpm")
	// LPM is a partner / LPM-lead tool: it surfaces cross-matter status and can
	// compose outbound mail. Gate the whole group behind the partner check.
	g.Use(func(c *gin.Context) {
		if !requirePartner(c) {
			c.Abort()
		}
	})

	// Generate a status report for a matter on demand.
	g.POST("/reports/generate", func(c *gin.Context) {
		var body struct {
			MatterNumber string `json:"matterNumber"`
			ClientNumber string `json:"clientNumber"`
			Date         string `json:"date"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.MatterNumber == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber required"})
			return
		}
		rep, err := svc.GenerateForMatter(lpm.MatterRef{
			MatterNumber: body.MatterNumber,
			ClientNumber: body.ClientNumber,
		}, body.Date)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, rep)
	})

	// Query the status-report corpus.
	g.GET("/reports", func(c *gin.Context) {
		reports, err := svc.Corpus().Query(c.Query("matter"), c.Query("from"), c.Query("to"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"reports": reports, "count": len(reports)})
	})

	// Process an outbound draft through the configured email-write-mode + guard.
	g.POST("/draft", func(c *gin.Context) {
		var body struct {
			MatterNumber string   `json:"matterNumber"`
			To           []string `json:"to"`
			Subject      string   `json:"subject"`
			Body         string   `json:"body"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.MatterNumber == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "matterNumber required"})
			return
		}
		out, err := svc.ProcessDraft(lpm.Draft{
			MatterNumber: body.MatterNumber, To: body.To, Subject: body.Subject, Body: body.Body,
		}, actorID(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, out)
	})

	// List drafts awaiting approval (send_gate mode).
	g.GET("/drafts/pending", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"pending": svc.PendingDrafts()})
	})

	// Approve and send a parked draft by ID (explicit human action, re-guarded).
	g.POST("/drafts/:id/approve", func(c *gin.Context) {
		out, err := svc.ApprovePending(c.Param("id"), actorID(c))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, out)
	})

	// Cancel a parked draft without sending it.
	g.POST("/drafts/:id/cancel", func(c *gin.Context) {
		if err := svc.CancelPending(c.Param("id"), actorID(c)); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
	})

	// Generate the portfolio BLUF briefing across all active matters on demand.
	g.POST("/portfolio/generate", func(c *gin.Context) {
		br, err := svc.GeneratePortfolio(svc.ActiveMatters(), c.Query("date"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, br)
	})

	// Download a single report rendered as DOCX.
	g.GET("/reports/:id/docx", func(c *gin.Context) {
		rep, err := svc.Corpus().Get(c.Param("id"))
		if err != nil || rep == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "report not found"})
			return
		}
		b, err := lpm.RenderDOCX(rep)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Header("Content-Disposition", "attachment; filename=\"status-"+safeFilename(rep.MatterNumber)+"-"+rep.Date+".docx\"")
		c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", b)
	})
}

// actorID returns the current principal's profile ID for audit attribution.
func actorID(c *gin.Context) string {
	if u := getUser(c); u != nil {
		return u.ProfileID
	}
	return "local"
}

func safeFilename(s string) string {
	if s == "" {
		return "matter"
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', ' ':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
