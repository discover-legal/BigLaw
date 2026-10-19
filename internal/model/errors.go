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

// Error is a classified failure. Detail is BigLaw's own fixed text and status codes, never the
// URL, the key or anything the endpoint sent, so it is safe to log.
type Error struct {
	Kind       error
	Detail     string
	RetryAfter time.Duration // the provider's Retry-After on a 429; 0 if none
	Usage      *Usage        // a 200 the provider billed but BigLaw refused: its usage and cost
	Status     int           // the endpoint's HTTP status (0: none); a 4xx other than 401/403/404/429 refused this request
}

func (e *Error) Error() string { return e.Kind.Error() + ": " + e.Detail }
func (e *Error) Unwrap() error { return e.Kind }

func fail(kind error, detail string) *Error { return &Error{Kind: kind, Detail: detail} }

// HTTPStatus maps a model failure to its HTTP status (500 if none); the API (PR 4b) owns the rest.
func HTTPStatus(err error) int {
	for i, kind := range []error{context.Canceled, ErrNotConfigured, ErrRateLimited, ErrTimeout, ErrNoContent, ErrUnavailable} {
		if errors.Is(err, kind) {
			return []int{499, 503, 503, 504, 422, 502}[i]
		}
	}
	return http.StatusInternalServerError
}
