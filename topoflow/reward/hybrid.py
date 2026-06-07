# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Hybrid reward (spec §7), eq (6).

    r = w_q*Q - w_c*(tokens/cap) - w_rho*retries

tokens and retries aggregate over the ENTIRE trajectory, including all inner
rounds of a composite topology cell [NEW] — the guardrail that stops the policy
treating DyTopo as free.
"""
from __future__ import annotations

from ..config import Config


def hybrid_reward(quality: float, tokens: int, retries: int, cfg: Config) -> float:
    return cfg.w_q * quality - cfg.w_c * (tokens / cfg.token_cap) - cfg.w_rho * retries
