// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// Model-failure error types carried in the JSON body's "errorType" so clients
// can tell "the AI could not run" apart from a validation or server error.
const (
	errorTypeModelNotConfigured = "model_not_configured"
	errorTypeModelUnavailable   = "model_unavailable"
)

// modelErrorResponse is the ONE place model failures map to HTTP: engines
// return errors wrapping providers.ErrNoProvider (→ 503) or
// providers.ErrModelUnavailable (→ 502), and every model-backed work-product
// handler (redline, headnotes, precedents, playbook build, tone import) writes
// the result of this function instead of a degraded result. Any other error is
// a plain 500 with fallbackMsg.
func modelErrorResponse(err error, fallbackMsg string) (int, gin.H) {
	detail := strutil.Truncate(err.Error(), 400)
	switch {
	case errors.Is(err, providers.ErrNoProvider):
		return http.StatusServiceUnavailable, gin.H{
			"error":     "No AI model provider is configured, so nothing was generated. Configure a model provider and try again.",
			"errorType": errorTypeModelNotConfigured,
			"detail":    detail,
		}
	case errors.Is(err, providers.ErrModelUnavailable):
		return http.StatusBadGateway, gin.H{
			"error":     "The AI model call failed, so no result was produced. Nothing was saved. Try again; if it keeps failing, check the model provider.",
			"errorType": errorTypeModelUnavailable,
			"detail":    detail,
		}
	}
	if fallbackMsg == "" {
		fallbackMsg = err.Error()
	}
	return http.StatusInternalServerError, gin.H{"error": fallbackMsg}
}

// writeModelError writes modelErrorResponse(err, fallbackMsg) to c.
func writeModelError(c *gin.Context, err error, fallbackMsg string) {
	status, body := modelErrorResponse(err, fallbackMsg)
	c.JSON(status, body)
}
