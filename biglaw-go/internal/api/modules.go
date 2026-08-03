// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/modeltier"
	"github.com/discover-legal/biglaw-go/internal/modules"
	"github.com/discover-legal/biglaw-go/internal/routing"
)

// handleListModules reports every registered feature module, its resolved
// state, and what decided it (config default or BIGLAW_MODULE_* override).
func (s *Server) handleListModules(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"modules": modules.Default.List()})
}

// handleModelTiers reports the BigLaw model ladder and where this deployment's
// configured stack lands on it, so operators can pick a quality profile
// without lab-brand class names.
func (s *Server) handleModelTiers(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	rate := func(id string) gin.H {
		bare := routing.ResolveModelID(id)
		return gin.H{"model": bare, "tier": modeltier.Rate(bare)}
	}
	c.JSON(http.StatusOK, gin.H{
		"ladder": modeltier.Ladder(),
		"stack": gin.H{
			"heavy":  rate(routing.Heavy(s.cfg)),
			"mid":    rate(routing.Mid(s.cfg)),
			"light":  rate(routing.Light(s.cfg)),
			"vision": rate(routing.Vision(s.cfg)),
		},
	})
}
