# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Policy graph + UCB1 router (spec §6) [AF].

A tabular contextual bandit: one Edge per (Signature, Action) cell. UCB1 with
annealed exploration and a reliability (failure-rate) penalty. Backups are
trajectory-level and support confidence-weighted fractional visits.
"""
from __future__ import annotations

import json
from dataclasses import asdict, dataclass
from math import log, sqrt
from typing import Optional

from .config import Config
from .types import Action, Regime, Signature


@dataclass
class Edge:  # one (signature, action) cell
    visits: float = 0.0
    mean_reward: float = 0.0
    m2: float = 0.0  # for online variance (Welford)
    tokens_sum: int = 0
    failures: float = 0.0  # validation + recoverable-exec failures

    @property
    def variance(self) -> float:
        return self.m2 / self.visits if self.visits > 1 else 0.0

    @property
    def fail_rate(self) -> float:
        return self.failures / self.visits if self.visits > 0 else 0.0


class PolicyGraph:
    def __init__(self, cfg: Config):
        self.edges: dict[tuple[Signature, tuple], Edge] = {}
        self.sig_visits: dict[Signature, int] = {}
        self.cfg = cfg

    # ── scoring / selection ────────────────────────────────────────────────
    def _cs(self, ns: int) -> float:
        """Annealed exploration constant, eq (5)."""
        return max(self.cfg.ucb_floor, self.cfg.ucb_c0 * 2 ** (-ns / self.cfg.ucb_half_life))

    def score(self, sig: Signature, action: Action) -> float:  # eq (4)+(5)
        e = self.edges.get((sig, action.key()))
        if e is None or e.visits == 0:
            return float("inf")  # force initial exploration [AF]
        ns = self.sig_visits.get(sig, 0)
        ucb = self._cs(ns) * sqrt(log(ns + 1) / e.visits)
        return e.mean_reward + ucb - self.cfg.lam * e.fail_rate

    def select(self, sig: Signature, legal: list[Action]) -> Action:
        if not legal:
            raise ValueError("select() called with no legal actions")
        return max(legal, key=lambda a: self.score(sig, a))

    # ── backup ─────────────────────────────────────────────────────────────
    def backup(
        self,
        sig: Signature,
        action: Action,
        reward: float,
        tokens: int,
        failed: bool,
        weight: float = 1.0,
    ) -> None:
        """Trajectory-level backup [AF].

        weight < 1 implements confidence-weighted updates (low-confidence judge
        rewards count as a fractional visit). weight == 1 for verifiable Q.
        """
        if weight <= 0:
            return
        key = (sig, action.key())
        e = self.edges.setdefault(key, Edge())
        delta = reward - e.mean_reward
        e.visits += weight
        e.mean_reward += (weight / e.visits) * delta
        e.m2 += weight * delta * (reward - e.mean_reward)
        e.tokens_sum += tokens
        e.failures += weight if failed else 0.0
        self.sig_visits[sig] = self.sig_visits.get(sig, 0) + 1

    # ── persistence ────────────────────────────────────────────────────────
    def save(self, path: str) -> None:
        records = []
        for (sig, akey), e in self.edges.items():
            records.append(
                {
                    "sig": _sig_to_json(sig),
                    "action_key": list(akey),
                    "edge": asdict(e),
                }
            )
        blob = {
            "sig_visits": [
                {"sig": _sig_to_json(s), "visits": v} for s, v in self.sig_visits.items()
            ],
            "edges": records,
        }
        with open(path, "w") as f:
            json.dump(blob, f, indent=2)

    @classmethod
    def load(cls, path: str, cfg: Config) -> "PolicyGraph":
        with open(path) as f:
            blob = json.load(f)
        g = cls(cfg)
        for rec in blob["edges"]:
            sig = _sig_from_json(rec["sig"])
            akey = tuple(rec["action_key"])
            g.edges[(sig, akey)] = Edge(**rec["edge"])
        for sv in blob["sig_visits"]:
            g.sig_visits[_sig_from_json(sv["sig"])] = sv["visits"]
        return g

    # ── inspection ─────────────────────────────────────────────────────────
    def summary(self) -> dict:
        """Human-inspectable dump: per signature, actions with stats."""
        out: dict[str, list[dict]] = {}
        for (sig, akey), e in self.edges.items():
            out.setdefault(_sig_label(sig), []).append(
                {
                    "action": list(akey),
                    "visits": round(e.visits, 3),
                    "mean_reward": round(e.mean_reward, 4),
                    "variance": round(e.variance, 4),
                    "fail_rate": round(e.fail_rate, 4),
                    "tokens_sum": e.tokens_sum,
                }
            )
        for label in out:
            out[label].sort(key=lambda r: r["mean_reward"], reverse=True)
        return out


# ── (de)serialization helpers ──────────────────────────────────────────────
def _sig_to_json(sig: Signature) -> dict:
    return {
        "regime": sig.regime.value,
        "handoff_mask": list(sig.handoff_mask),
        "correctness_b": sig.correctness_b,
        "uncertainty_b": sig.uncertainty_b,
        "contradiction_b": sig.contradiction_b,
        "evidence_b": sig.evidence_b,
    }


def _sig_from_json(d: dict) -> Signature:
    return Signature(
        regime=Regime(d["regime"]),
        handoff_mask=tuple(d["handoff_mask"]),
        correctness_b=d["correctness_b"],
        uncertainty_b=d["uncertainty_b"],
        contradiction_b=d["contradiction_b"],
        evidence_b=d["evidence_b"],
    )


def _sig_label(sig: Signature) -> str:
    return (
        f"{sig.regime.value}|m{''.join(map(str, sig.handoff_mask))}"
        f"|c{sig.correctness_b}u{sig.uncertainty_b}"
        f"k{sig.contradiction_b}e{sig.evidence_b}"
    )
