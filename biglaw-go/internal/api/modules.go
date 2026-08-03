// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/modules"
)

// handleListModules reports every registered feature module, its resolved
// state, and what decided it (config default or BIGLAW_MODULE_* override).
func (s *Server) handleListModules(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"modules": modules.Default.List()})
}
