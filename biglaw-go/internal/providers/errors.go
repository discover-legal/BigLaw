// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package providers

import (
	"errors"
	"fmt"
)

// Model-failure sentinels. Engines whose output is a substantive legal work
// product (redline reports, headnotes, precedents, playbooks, voice profiles)
// must return one of these instead of degrading to a plausible-looking
// default — an empty report or a placeholder draft is indistinguishable, to a
// lawyer, from a real answer. The API layer maps them to HTTP status codes in
// one place (internal/api/modelerr.go).
var (
	// ErrNoProvider: no model provider is configured for the requested model
	// (HTTP 503 — the deployment cannot serve this feature at all).
	ErrNoProvider = errors.New("no model provider configured")
	// ErrModelUnavailable: the upstream model call failed, or returned output
	// that could not be used (HTTP 502).
	ErrModelUnavailable = errors.New("model call failed")
)

// ModelFailure annotates err with the step that failed and guarantees the
// result matches ErrModelUnavailable or ErrNoProvider under errors.Is. An err
// that already carries either sentinel keeps it (so a missing provider stays a
// 503 rather than becoming a 502).
func ModelFailure(step string, err error) error {
	if err == nil {
		err = errors.New("no usable output")
	}
	if errors.Is(err, ErrNoProvider) || errors.Is(err, ErrModelUnavailable) {
		return fmt.Errorf("%s: %w", step, err)
	}
	return fmt.Errorf("%s: %w: %w", step, ErrModelUnavailable, err)
}

// IsModelFailure reports whether err carries either model-failure sentinel.
func IsModelFailure(err error) bool {
	return errors.Is(err, ErrNoProvider) || errors.Is(err, ErrModelUnavailable)
}
