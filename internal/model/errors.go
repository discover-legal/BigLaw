// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Model-failure kinds (README G6). A failed Chat matches exactly one under
// errors.Is (or context.Canceled); a failure is never "no finding".
var (
	ErrNotConfigured = errors.New("model not configured")            // 503 model_not_configured
	ErrRateLimited   = errors.New("model provider rate limited")     // 503 model_rate_limited
	ErrUnavailable   = errors.New("model call failed")               // 502 model_unavailable
	ErrTimeout       = errors.New("model call timed out")            // 504 model_timeout
	ErrNoContent     = errors.New("model returned no usable output") // 422 no_content
)

// Error is a classified failure. Detail is BigLaw's own text and status
// codes only, never the URL, the key or upstream text, so it is safe to log.
type Error struct {
	Kind       error
	Detail     string
	RetryAfter time.Duration // the provider's Retry-After on a 429; 0 if none
}

func (e *Error) Error() string { return e.Kind.Error() + ": " + e.Detail }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, detail string) *Error { return &Error{Kind: kind, Detail: detail} }

// HTTPStatus is the one place a model failure maps to HTTP: status, the
// JSON errorType ("" for a plain 500) and a message safe to return.
func HTTPStatus(err error) (status int, errorType, message string) {
	for _, m := range []struct {
		kind         error
		status       int
		typ, message string
	}{
		{context.Canceled, 499, "", "Request cancelled."},
		{ErrNotConfigured, http.StatusServiceUnavailable, "model_not_configured", "No AI model is configured for this step, so nothing was generated."},
		{ErrRateLimited, http.StatusServiceUnavailable, "model_rate_limited", "The AI model provider is rate-limiting requests, so nothing was generated. Wait and try again."},
		{ErrTimeout, http.StatusGatewayTimeout, "model_timeout", "The AI model did not respond in time, so nothing was generated. Try again."},
		{ErrNoContent, http.StatusUnprocessableEntity, "no_content", "The AI model returned no usable output, so nothing was produced. This is not a clean result."},
		{ErrUnavailable, http.StatusBadGateway, "model_unavailable", "The AI model call failed, so nothing was produced or saved. Try again, or check the model provider."},
	} {
		if errors.Is(err, m.kind) {
			return m.status, m.typ, m.message
		}
	}
	return http.StatusInternalServerError, "", "The request could not be completed."
}

// RetryAfter is the wait a rate-limited failure asks for (0 if none).
func RetryAfter(err error) time.Duration {
	var e *Error
	if errors.As(err, &e) && e.Kind == ErrRateLimited {
		return e.RetryAfter
	}
	return 0
}
