# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Belief-delta rules (spec §10) [AF].

Heuristic, deterministic updates applied after a cell/generator runs. Small fixed
step sizes, clipped to [0,1]. handoff_quality is updated but excluded from the
signature (§4).
"""
from __future__ import annotations

from .config import Config
from .types import BeliefVector, CellOutput

STEP = 0.15  # default fixed step (0.1–0.2)
SMALL = 0.1


def _clip(x: float) -> float:
    return 0.0 if x < 0.0 else 1.0 if x > 1.0 else x


def role_of(skill: str) -> str:
    """Map a skill name to its §10 belief-source role category."""
    s = (skill or "").lower()
    if s.startswith("solver"):
        return "solver"
    if s == "planner":
        return "planner"
    if s == "memory" or s.startswith("web_search"):
        return "memory"
    if s.startswith("verifier"):
        return "verifier"
    if s == "synthesiser" or s == "synthesizer":
        return "synthesiser"
    if s == "critic":
        return "critic"
    if s == "evaluator":
        return "evaluator"
    return s


def _apply_role(b: BeliefVector, role: str, out: CellOutput, cfg: Config) -> BeliefVector:
    d = out.handoff_delta
    meta = out.meta
    if role == "planner" and d.get("subproblem") is not None:
        b.handoff_quality = _clip(b.handoff_quality + STEP)
        b.uncertainty = _clip(b.uncertainty - STEP)
    elif role == "memory":
        n = len(d.get("evidence") or []) if isinstance(d.get("evidence"), (list, tuple)) else (1 if d.get("evidence") else 0)
        b.evidence = _clip(b.evidence + min(0.3, SMALL * max(1, n)))
        b.uncertainty = _clip(b.uncertainty - SMALL)
        b.handoff_quality = _clip(b.handoff_quality + SMALL)
    elif role == "solver" and d.get("draft_answer") is not None:
        b.correctness = _clip(b.correctness + STEP)
        b.uncertainty = _clip(b.uncertainty - STEP)
        b.handoff_quality = _clip(b.handoff_quality + STEP)
    elif role == "critic" and d.get("critique") is not None:
        b.contradiction = _clip(b.contradiction + STEP)
        b.uncertainty = _clip(b.uncertainty + SMALL)
    elif role == "verifier":
        verdict = str(meta.get("verdict") or d.get("verification") or "").lower()
        if "support" in verdict or "pass" in verdict or "correct" in verdict:
            b.correctness = _clip(b.correctness + STEP)
            b.uncertainty = _clip(b.uncertainty - STEP)
            b.contradiction = _clip(b.contradiction - SMALL)
            b.evidence = _clip(b.evidence + SMALL)
        elif "refut" in verdict or "fail" in verdict or "incorrect" in verdict:
            b.correctness = _clip(b.correctness - STEP)
            b.contradiction = _clip(b.contradiction + STEP)
            b.uncertainty = _clip(b.uncertainty + SMALL)
    elif role == "synthesiser" and d.get("merged_answer") is not None:
        b.correctness = _clip(b.correctness + STEP)
        b.handoff_quality = _clip(b.handoff_quality + STEP)
        b.uncertainty = _clip(b.uncertainty - STEP)
    # evaluator: no belief delta (its decision feeds reward, not state)
    return b


def update_beliefs_cell(b: BeliefVector, out: CellOutput, cfg: Config) -> BeliefVector:
    return _apply_role(b, role_of(out.skill), out, cfg)


def update_beliefs_topology(b: BeliefVector, res, cfg: Config) -> BeliefVector:
    """For a composite cell [NEW]: derive the delta from which handoff fields its
    merged result populated and from its final verifier/tester outcome — i.e.
    treat the cell's net effect through the same §10 table, applied once."""
    fields = res.merged_handoff.fields
    if fields.get("merged_answer") is not None:
        b = _apply_role(b, "synthesiser", CellOutput(skill="synthesiser", handoff_delta=fields), cfg)
    elif fields.get("draft_answer") is not None:
        b = _apply_role(b, "solver", CellOutput(skill="solver", handoff_delta=fields), cfg)
    if fields.get("verification") is not None:
        b = _apply_role(
            b, "verifier",
            CellOutput(skill="verifier", handoff_delta=fields, meta={"verdict": fields.get("verification")}),
            cfg,
        )
    if fields.get("evidence") is not None:
        b = _apply_role(b, "memory", CellOutput(skill="memory", handoff_delta=fields), cfg)
    return b
