// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

// Model-failure error types carried in the JSON body's "errorType" so clients
// can tell "the AI could not run" apart from a validation or server error.
const (
	errorTypeModelNotConfigured = "model_not_configured"
	errorTypeModelUnavailable   = "model_unavailable"
	errorTypeModelRateLimited   = "model_rate_limited"
	errorTypeModelTimeout       = "model_timeout"
	errorTypeNoContent          = "no_content"
)

// statusClientClosedRequest is the de-facto (nginx) status for a request the
// client abandoned; nobody reads the body, it just keeps logs honest.
const statusClientClosedRequest = 499

// modelErrorInfo is how one model failure is answered over HTTP.
type modelErrorInfo struct {
	status     int
	errorType  string // "" for a plain server error
	message    string // user-facing, safe: no endpoints, keys or upstream bodies
	retryAfter int    // seconds; 0 = no Retry-After header
}

// classifyModelError is the ONE place model failures map to HTTP. Engines
// return errors carrying a providers sentinel; every model-backed work-product
// handler (redline, headnotes, precedents, playbook build, tone import) answers
// with this instead of a degraded result. Any other error is a plain 500 with
// fallbackMsg (or a generic message) — never err.Error(), which can carry
// internal endpoint URLs or raw upstream bodies.
func classifyModelError(err error, fallbackMsg string) modelErrorInfo {
	switch {
	case errors.Is(err, context.Canceled):
		return modelErrorInfo{status: statusClientClosedRequest, message: "Request cancelled."}
	case errors.Is(err, providers.ErrNoProvider):
		return modelErrorInfo{http.StatusServiceUnavailable, errorTypeModelNotConfigured,
			"No AI model provider is configured, so nothing was generated. Configure a model provider and try again.", 0}
	case errors.Is(err, providers.ErrModelRateLimited):
		info := modelErrorInfo{http.StatusServiceUnavailable, errorTypeModelRateLimited,
			"The AI model provider is rate-limiting requests, so nothing was generated. Wait a moment and try again.", 0}
		var rl *providers.RateLimitError
		if errors.As(err, &rl) && rl.RetryAfter > 0 {
			info.retryAfter = int(math.Ceil(rl.RetryAfter.Seconds()))
		}
		return info
	case errors.Is(err, providers.ErrModelTimeout):
		return modelErrorInfo{http.StatusGatewayTimeout, errorTypeModelTimeout,
			"The AI model did not respond in time, so nothing was generated. Try again.", 0}
	case errors.Is(err, providers.ErrNoContent):
		return modelErrorInfo{http.StatusUnprocessableEntity, errorTypeNoContent,
			"The AI model found nothing to work on in this input, so nothing was reviewed. This is not a clean result — check the text you supplied.", 0}
	case errors.Is(err, providers.ErrModelUnavailable):
		return modelErrorInfo{http.StatusBadGateway, errorTypeModelUnavailable,
			"The AI model call failed, so no result was produced. Nothing was saved. Try again; if it keeps failing, check the model provider.", 0}
	}
	if fallbackMsg == "" {
		fallbackMsg = "The request could not be completed."
	}
	return modelErrorInfo{status: http.StatusInternalServerError, message: fallbackMsg}
}

// writeModelError logs err server-side (with the full internal detail) under
// a request id and answers with only the safe classified message plus that
// id, so support can correlate without the body leaking internals.
func writeModelError(c *gin.Context, err error, fallbackMsg string) {
	info := classifyModelError(err, fallbackMsg)
	reqID := c.GetHeader("X-Request-Id")
	if reqID == "" || len(reqID) > 64 {
		reqID = uuid.NewString()
	}
	c.Header("X-Request-Id", reqID)
	level := slog.LevelWarn
	if info.status == statusClientClosedRequest {
		level = slog.LevelInfo
	}
	slog.Log(c.Request.Context(), level, "model-backed request failed",
		"requestId", reqID, "path", c.FullPath(), "status", info.status, "errorType", info.errorType, "error", err)
	if info.retryAfter > 0 {
		c.Header("Retry-After", strconv.Itoa(info.retryAfter))
	}
	body := gin.H{"error": info.message, "requestId": reqID}
	if info.errorType != "" {
		body["errorType"] = info.errorType
	}
	c.AbortWithStatusJSON(info.status, body)
}
