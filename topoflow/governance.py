# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Governance (spec §9 Layer 5): pre-flight, halting, policy violations."""
from __future__ import annotations

from .config import Config


class GovernanceAbort(RuntimeError):
    """Raised by preflight for unrecoverable conditions (bad key/quota/etc.)."""


def preflight(ctx, cfg: Config, transport) -> None:
    """Layer 5 pre-flight: abort early on unrecoverable transport problems."""
    if transport is None:
        raise GovernanceAbort("no transport configured")
    if not ctx.prompt:
        raise GovernanceAbort("empty task prompt")


def violation(trace, cfg: Config) -> bool:
    """Detect a policy violation that should halt the trajectory.

    Conservative guards that protect against runaway loops / repeated failures.
    """
    if trace.retries > cfg.max_retries:
        return True
    return False
