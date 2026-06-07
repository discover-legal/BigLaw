# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Signature folding (spec §4) [AF] — fold(ctx, handoff, beliefs, cfg) -> Signature.

The signature is deliberately small: regime + 7-bit handoff mask + four bucketed
beliefs. Topology knobs live in the action space (§5), never here — keeping the
signature small is load-bearing for value sharing across trajectories.
"""
from __future__ import annotations

from math import floor

from .config import Config
from .types import BeliefVector, HandoffState, Regime, Signature, TaskContext


def bucket(x: float, bins: int) -> int:
    """β(x) = floor(clip(x,0,1) * bins), clamped to [0, bins-1]."""
    x = 0.0 if x < 0.0 else 1.0 if x > 1.0 else x
    b = floor(x * bins)
    if b >= bins:
        b = bins - 1
    if b < 0:
        b = 0
    return b


# Deterministic regime rule table. Each entry: (name, predicate(features)).
# Override by replacing detect_regime or passing cfg with a custom hook later.
def detect_regime(ctx: TaskContext, handoff: HandoffState, beliefs: BeliefVector) -> Regime:
    """Rule-based regime detection over typed features [AF].

    Features: contradiction risk, ambiguity/uncertainty, evidence availability,
    verification need, risk class.
    """
    contradiction = beliefs.contradiction
    uncertainty = beliefs.uncertainty
    evidence = beliefs.evidence
    has_any_progress = any(handoff.mask())
    domain = (ctx.domain or "").lower()
    risk_class = (ctx.scenario_class or "").upper()

    # Contradiction dominates: conflicting evidence/critique present.
    if contradiction >= 0.5:
        return Regime.CONTRADICTORY
    # Explicit high-risk scenario classes (security/incident severity).
    if risk_class in ("C7", "C8") or domain == "advisory" and risk_class in ("C5",):
        return Regime.HIGH_RISK
    # Nothing known yet and no progress → exploratory probing.
    if not has_any_progress and uncertainty >= 0.9:
        return Regime.EXPLORATORY
    # High uncertainty with thin evidence → ambiguous.
    if uncertainty >= 0.66 and evidence < 0.34:
        return Regime.AMBIGUOUS
    # Evidence-centric domains or already evidence-rich states.
    if domain in ("incident", "advisory") or evidence >= 0.5:
        return Regime.EVIDENCE_HEAVY
    return Regime.STRAIGHTFORWARD


def fold(ctx: TaskContext, handoff: HandoffState, beliefs: BeliefVector, cfg: Config) -> Signature:
    """Implements eq (1): observations -> hashable Signature."""
    bins = cfg.belief_bins
    return Signature(
        regime=detect_regime(ctx, handoff, beliefs),
        handoff_mask=handoff.mask(),
        correctness_b=bucket(beliefs.correctness, bins),
        uncertainty_b=bucket(beliefs.uncertainty, bins),
        contradiction_b=bucket(beliefs.contradiction, bins),
        evidence_b=bucket(beliefs.evidence, bins),
    )
