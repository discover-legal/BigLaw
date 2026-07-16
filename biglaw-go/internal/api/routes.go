// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import "github.com/gin-gonic/gin"

func (s *Server) newRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), s.authMiddleware())

	r.GET("/health", s.handleHealth)
	r.GET("/me", s.handleMe)
	r.GET("/settings", s.handleGetSettings)
	r.PUT("/settings", s.handleUpdateSettings)

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

	s.registerBillingRoutes(r)
	s.registerMattersRoutes(r)
	s.registerOpsRoutes(r)
	s.registerEnginesRoutes(r)
	s.registerContentRoutes(r)
	s.registerRedtimeRoutes(r)
	s.registerReviewRoutes(r)
	s.registerRemyRoutes(r)
	s.registerAuthRoutes(r)
	s.registerClioRoutes(r)
	s.mountBots(r)
	return r
}
