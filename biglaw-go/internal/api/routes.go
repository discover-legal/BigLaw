// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/modules"
)

// canonicalRawPath re-escapes each segment of a request's raw path
// canonically before gin routes on it (UseRawPath, below). Escapes that
// carry meaning survive — %2F inside an ID stays one segment, %25 stays a
// literal "%" — while gratuitous ones (/heal%74h, /auth/%70roviders) decode,
// so static routes match exactly as they did when gin routed on the decoded
// path. Param values are unaffected: gin unescapes them either way.
func canonicalRawPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" {
			segs := strings.Split(r.URL.RawPath, "/")
			for i, seg := range segs {
				if dec, err := url.PathUnescape(seg); err == nil {
					segs[i] = url.PathEscape(dec)
				}
			}
			u := *r.URL
			u.RawPath = strings.Join(segs, "/")
			r2 := *r
			r2.URL = &u
			r = &r2
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) newRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Route on the escaped path so an ID containing "/" (sent as %2F) stays
	// one :param segment and reaches its handler — a typed not-found, not
	// gin's plain-text 404 for an unmatched route.
	r.UseRawPath = true
	r.Use(gin.Recovery(), requestBodyLimit(), s.authMiddleware())

	r.GET("/health", s.handleHealth)
	r.GET("/me", s.handleMe)
	r.GET("/settings", s.handleGetSettings)
	r.PUT("/settings", s.handleUpdateSettings)
	r.GET("/modules", s.handleListModules)

	tasks := r.Group("/tasks")
	tasks.POST("", s.handleSubmitTask)
	tasks.GET("", s.handleListTasks)
	tasks.POST("/from-template", s.handleSubmitFromTemplate)
	tasks.GET("/:id", s.handleGetTask)
	tasks.DELETE("/:id", s.handleDeleteTask)
	tasks.POST("/:id/assign", s.handleAssignTask)
	tasks.GET("/:id/stream", s.handleTaskStream)
	tasks.GET("/:id/cost", s.handleTaskCost)
	tasks.GET("/:id/rounds/:round", s.handleGetRound)
	tasks.POST("/:id/gates/:gateId/approve", s.handleApproveGate)
	tasks.POST("/:id/gates/:gateId/reject", s.handleRejectGate)

	docs := r.Group("/documents")
	docs.POST("", s.handleIngestDocument)
	docs.GET("/search", s.handleSearchDocuments)

	r.GET("/agents", s.handleListAgents)
	r.GET("/templates", s.handleListTemplates)

	profiles := r.Group("/profiles")
	profiles.GET("", s.handleListProfiles)
	profiles.POST("", s.handleCreateProfile)
	profiles.GET("/:id", s.handleGetProfile)
	profiles.PATCH("/:id", s.handleUpdateProfile)
	profiles.DELETE("/:id", s.handleDeleteProfile)

	clients := r.Group("/clients")
	clients.GET("", s.handleListClients)
	clients.POST("", s.handleCreateClient)
	clients.POST("/check-conflict", s.handleCheckConflict)
	clients.GET("/:id/conflicts", s.handleClientGraphConflicts)
	clients.PATCH("/:id", s.handleUpdateClient)
	clients.DELETE("/:id", s.handleDeleteClient)
	clients.POST("/:id/matters", s.handleAddMatter)
	clients.DELETE("/:id/matters/:num", s.handleRemoveMatter)

	r.GET("/time-entries", s.handleListTimeEntries)
	r.GET("/cost/summary", s.handleCostSummary)
	r.GET("/audit", s.handleAudit)
	r.GET("/audit/stream", s.handleAuditStream)

	// Optional feature modules — resolved by the modules registry; a disabled
	// module's routes simply don't exist (404), matching a not-compiled-in
	// feel without a build matrix.
	if modules.Default.Enabled("billing") {
		s.registerBillingRoutes(r)
	}
	s.registerMattersRoutes(r)
	s.registerOpsRoutes(r)
	s.registerEnginesRoutes(r)
	s.registerContentRoutes(r)
	s.registerRedtimeRoutes(r)
	s.registerReviewRoutes(r)
	if modules.Default.Enabled("clientvoice") {
		s.registerRemyRoutes(r)
	}
	s.registerAuthRoutes(r)
	s.registerClioRoutes(r)
	if modules.Default.Enabled("bots") {
		s.mountBots(r)
	}
	return r
}
