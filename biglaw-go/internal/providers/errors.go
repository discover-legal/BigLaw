// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Model-failure sentinels. Engines whose output is a substantive legal work
// product (redline reports, headnotes, precedents, playbooks, voice profiles)
// must return one of these instead of degrading to a plausible-looking
// default — an empty report or a placeholder draft is indistinguishable, to a
// lawyer, from a real answer. The API layer maps them to HTTP status codes in
// one place (internal/api/modelerr.go).
var (
	// ErrNoProvider: no model provider is configured for the requested model
	// (HTTP 503 model_not_configured).
	ErrNoProvider = errors.New("no model provider configured")
	// ErrModelUnavailable: the upstream model call failed, or returned output
	// that could not be used (HTTP 502 model_unavailable).
	ErrModelUnavailable = errors.New("model call failed")
	// ErrModelRateLimited: the provider kept answering 429 after retries
	// (HTTP 503 model_rate_limited, Retry-After passed through). Match with
	// errors.Is; errors.As(*RateLimitError) yields the wait.
	ErrModelRateLimited = errors.New("model provider rate limited")
	// ErrModelTimeout: the model call timed out (HTTP 504 model_timeout).
	ErrModelTimeout = errors.New("model call timed out")
	// ErrNoContent: the model answered validly but found nothing to work on
	// (zero clauses, zero holdings) — HTTP 422 no_content, "nothing was
	// reviewed". Never rendered as an empty-but-successful result.
	ErrNoContent = errors.New("model found no content")
)

// RateLimitError is a 429 that survived retries. It matches
// ErrModelRateLimited under errors.Is.
type RateLimitError struct {
	RetryAfter time.Duration // 0 when the provider sent no usable Retry-After
	Err        error
}

func (e *RateLimitError) Error() string        { return "model provider rate limited: " + e.Err.Error() }
func (e *RateLimitError) Unwrap() error        { return e.Err }
func (e *RateLimitError) Is(target error) bool { return target == ErrModelRateLimited }

var classified = []error{ErrNoProvider, ErrModelUnavailable, ErrModelRateLimited, ErrModelTimeout, ErrNoContent, context.Canceled}

// ModelFailure annotates err with the step that failed and guarantees the
// result matches one of the sentinels under errors.Is. An err that already
// carries a sentinel (or context.Canceled) keeps it; anything else becomes
// ErrModelUnavailable.
func ModelFailure(step string, err error) error {
	if err == nil {
		err = errors.New("no usable output")
	}
	for _, s := range classified {
		if errors.Is(err, s) {
			return fmt.Errorf("%s: %w", step, err)
		}
	}
	return fmt.Errorf("%s: %w: %w", step, ErrModelUnavailable, err)
}

// NoContent reports that the model validly found nothing (step: what found
// nothing; what: user-facing description, e.g. "no clauses").
func NoContent(step, what string) error {
	return fmt.Errorf("%s: %w: %s", step, ErrNoContent, what)
}

// IsModelFailure reports whether err carries any model-failure sentinel.
func IsModelFailure(err error) bool {
	for _, s := range classified[:5] {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// unconfigured is a Provider standing in for one that could not be resolved:
// every call fails with the resolution error (which carries ErrNoProvider), so
// callers built at startup degrade per call (HTTP 503 model_not_configured)
// instead of panicking the process.
type unconfigured struct{ err error }

func (u unconfigured) Chat(ChatParams) (*ChatResponse, error) { return nil, u.err }

// Unconfigured returns a Provider whose every call fails with err (wrapped
// with ErrNoProvider if it does not already carry it).
func Unconfigured(err error) Provider {
	if err == nil || !errors.Is(err, ErrNoProvider) {
		err = fmt.Errorf("%w: %v", ErrNoProvider, err)
	}
	return unconfigured{err: err}
}

// GetOrUnconfigured resolves modelID, or returns an Unconfigured provider and
// the resolution error (for the caller to log) when no provider serves it.
func (r *Registry) GetOrUnconfigured(modelID string) (Provider, error) {
	p, err := r.Get(modelID)
	if err != nil {
		return Unconfigured(err), err
	}
	return p, nil
}

// Describe returns a short, user-safe description of a model failure — no
// endpoint URLs, keys or upstream bodies — for surfaces that are not HTTP
// (agent tool results, chat bots). Non-model errors get a generic phrase.
func Describe(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "the request was cancelled"
	case errors.Is(err, ErrNoProvider):
		return "no AI model provider is configured"
	case errors.Is(err, ErrModelRateLimited):
		return "the AI model provider is rate-limiting requests"
	case errors.Is(err, ErrModelTimeout):
		return "the AI model did not respond in time"
	case errors.Is(err, ErrNoContent):
		return "the AI model found nothing to work on"
	case errors.Is(err, ErrModelUnavailable):
		return "the AI model call failed"
	}
	return "an internal error occurred"
}
