# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Pluggable quality signal Q (spec §7) [NEW].

GroundTruthQ for verifiable domains (code: run tests; math: exact match) and
JudgedQ for open-ended domains (wraps RelativeJudge). Selection: if
ctx.ground_truth is set use GroundTruthQ, else JudgedQ — overridable via
cfg.quality_strategy.

Q.score(ctx, trajectory, peers) -> (Q in [0,1], sub_scores dict incl "confidence").
"""
from __future__ import annotations

import re
from typing import Protocol

from ..config import Config


class QualitySignal(Protocol):
    def score(self, ctx, trajectory, peers) -> tuple[float, dict]:
        ...


# ── verifiable Q ────────────────────────────────────────────────────────────
def _run_code_tests(candidate: str, gt: dict) -> float:
    """Return fraction of tests passed. Supports HumanEval (check/entry_point)
    and APPS-style (list of assert snippets)."""
    if not candidate:
        return 0.0
    ns: dict = {}
    try:
        exec(candidate, ns)
    except Exception:
        return 0.0
    # APPS-style: explicit list of test snippets
    tests = gt.get("tests")
    if isinstance(tests, (list, tuple)) and tests:
        passed = 0
        for t in tests:
            try:
                exec(t, dict(ns))
                passed += 1
            except Exception:
                pass
        return passed / len(tests)
    # HumanEval-style: a `check(candidate)` harness + entry_point
    test = gt.get("test")
    entry = gt.get("entry_point")
    if test and entry:
        try:
            exec(test, ns)
            ns["check"](ns[entry])
            return 1.0
        except Exception:
            return 0.0
    return 0.0


def _norm_math(s: str) -> str:
    s = str(s).strip()
    if "####" in s:  # GSM8K-style final marker
        s = s.split("####")[-1]
    s = s.replace("$", "").replace(",", "").replace(" ", "")
    s = s.rstrip(".")
    m = re.search(r"-?\d+(?:/\d+|\.\d+)?", s)
    return m.group(0) if m else s.lower()


class GroundTruthQ:
    def score(self, ctx, trajectory, peers) -> tuple[float, dict]:
        gt = ctx.ground_truth
        ans = getattr(trajectory, "final_answer", "") or ""
        domain = (ctx.domain or "").lower()
        if domain == "code" and isinstance(gt, dict):
            q = _run_code_tests(ans, gt)
            return q, {"confidence": 1.0, "kind": "ground_truth_code", "tests_fraction": q}
        # math / exact-match
        ok = 1.0 if _norm_math(ans) == _norm_math(gt) else 0.0
        return ok, {"confidence": 1.0, "kind": "ground_truth_match", "exact": ok}


# ── judged Q ────────────────────────────────────────────────────────────────
class JudgedQ:
    """Wraps a RelativeJudge over a peer group. If no judge is configured (or no
    peers are available) it falls back to a transparent heuristic so the loop is
    runnable before M7; the real judge path is exercised in M7."""

    def __init__(self, judge=None):
        self.judge = judge

    def score(self, ctx, trajectory, peers) -> tuple[float, dict]:
        if self.judge is not None:
            return self.judge.score_one(ctx, trajectory, peers)
        # heuristic fallback (clearly low-confidence)
        ans = getattr(trajectory, "final_answer", "") or ""
        q = 0.6 if ans else 0.0
        return q, {"confidence": 0.4, "kind": "judged_heuristic"}


def make_quality(ctx, cfg: Config, transport=None, judge=None) -> QualitySignal:
    strat = cfg.quality_strategy
    if strat == "ground_truth":
        return GroundTruthQ()
    if strat == "judged":
        return JudgedQ(judge=judge)
    # auto
    if ctx.ground_truth is not None:
        return GroundTruthQ()
    return JudgedQ(judge=judge)
