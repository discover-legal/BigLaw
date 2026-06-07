# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Legal-action enumeration A(s) (spec §5) [AF] + [NEW].

Edges are never arms: the topology family is a handful of (mode, tau, round)
arms, not per-edge choices. With 2 modes, 4 tau buckets, 3 round buckets the
topology family is 1 + 4*3 = 13 arms; plus ~14 invoke variants + a few skips
≈ ~30 arms total.
"""
from __future__ import annotations

from dataclasses import dataclass, field

from .config import Config
from .types import Action


@dataclass
class Plan:
    """Tracks which optional aux cells are still schedulable."""

    optional: list[str] = field(default_factory=list)
    invoked: set[str] = field(default_factory=set)
    skipped: set[str] = field(default_factory=set)

    def still_scheduled(self) -> list[str]:
        return [c for c in self.optional if c not in self.invoked and c not in self.skipped]

    def mark_invoked(self, cell: str) -> None:
        self.invoked.add(cell)

    def mark_skipped(self, cell: str) -> None:
        self.skipped.add(cell)


def cell_enabled(skill: str, plan: Plan, cfg: Config) -> bool:
    # optional aux cells may be invoked at most once
    if skill in cfg.optional_aux:
        return skill not in plan.invoked and skill not in plan.skipped
    # solvers and the evaluator are always invocable (budget bounds them)
    return True


def legal_actions(sig, handoff, plan: Plan, budget, cfg: Config) -> list[Action]:
    A: list[Action] = []
    # [AF] invoke(skill, model)
    for k, m in cfg.variant_pool:
        if cell_enabled(k, plan, cfg):
            A.append(Action("invoke", skill=k, model=m))
    # [NEW] topology(mode, tau_bucket, k_in, round_bucket)
    for mode in cfg.topo_modes:
        if mode == "linear":
            A.append(Action("topology", topo_mode="linear"))
        elif mode == "dytopo":
            for tb in range(len(cfg.tau_buckets)):
                for rb in range(len(cfg.round_buckets)):
                    A.append(
                        Action(
                            "topology",
                            topo_mode="dytopo",
                            tau_bucket=tb,
                            k_in=cfg.dytopo_k_in,
                            round_bucket=rb,
                        )
                    )
    # [AF] skip:X for still-scheduled optional cells, only if >=1 other legal action remains
    if A:
        for X in plan.still_scheduled():
            A.append(Action("skip", target=X))
    # [AF] terminate is never emitted here — it is implicit (§9)
    return prune_to_keep_finishable(A, plan, budget, cfg)


def prune_to_keep_finishable(A: list[Action], plan: Plan, budget, cfg: Config) -> list[Action]:
    """Guarantee a termination path exists. The evaluator invoke (always enabled)
    provides it; if for any reason it is absent, add it."""
    has_eval = any(a.kind == "invoke" and a.skill == "evaluator" for a in A)
    if not has_eval:
        A.append(Action("invoke", skill="evaluator", model=cfg.default_model_for("evaluator")))
    return A
