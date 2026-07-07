// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package topoflow

import "errors"

// ErrGovernanceAbort is returned by preflight for unrecoverable conditions.
var ErrGovernanceAbort = errors.New("governance abort")

// preflight is the Layer-5 pre-flight check (spec §9).
func preflight(ctx TaskContext, cfg Config, tx Transport) error {
	if tx == nil {
		return errGov("no transport configured")
	}
	if ctx.Prompt == "" {
		return errGov("empty task prompt")
	}
	return nil
}

type govErr struct{ msg string }

func (e govErr) Error() string { return "governance abort: " + e.msg }
func (e govErr) Unwrap() error { return ErrGovernanceAbort }

func errGov(msg string) error { return govErr{msg} }

// violation halts the trajectory on a policy violation (runaway retries).
func violation(t *Trace, cfg Config) bool {
	return t.Retries > cfg.MaxRetries
}
