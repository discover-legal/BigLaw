# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Dataclass configs + defaults (spec §12). Defaults are starting points."""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass
class Config:
    # ── signature ──────────────────────────────────────────────────────────
    belief_bins: int = 4
    # ── policy graph ───────────────────────────────────────────────────────
    lam: float = 0.5
    ucb_c0: float = 1.4
    ucb_half_life: int = 50
    ucb_floor: float = 0.5
    # ── reward ─────────────────────────────────────────────────────────────
    w_q: float = 1.0
    w_c: float = 0.3
    w_rho: float = 0.15
    token_cap: int = 8000
    # ── variant pool [AF]: 3 skill protocols x 3 models = 9 solver cells ────
    skill_protocols: tuple = ("solver_concise", "solver_cot", "solver_evidence")
    models: tuple = ("haiku", "fast", "mini")
    aux_cells: tuple = (
        "planner",
        "memory",
        "web_search_exa",
        "web_search_tavily",
        "verifier_a",
        "verifier_b",
        "evaluator",
    )
    default_solver: str = "solver_cot"  # default binding solver_cot_haiku [AF]
    default_model: str = "haiku"
    # ── topology [NEW] ─────────────────────────────────────────────────────
    topo_modes: tuple = ("linear", "dytopo")
    tau_buckets: tuple = (0.2, 0.3, 0.4, 0.5)  # DyTopo sweet spots cluster 0.3-0.4
    round_buckets: tuple = (3, 6, 10)  # DyTopo: HumanEval~5, Math~9, cap 10
    dytopo_k_in: int = 3  # DyTopo max in-degree
    dytopo_temp: float = 0.3
    dytopo_max_tokens: int = 4000  # DyTopo used 3000-5000
    encoder: str = "sentence-transformers/all-MiniLM-L6-v2"
    n_agents_code: int = 4
    n_agents_math: int = 3
    # ── reward audit [AF] ──────────────────────────────────────────────────
    live_judge: str = "claude-haiku-4-5"
    audit_judges: tuple = ("claude-haiku-4-5", "gpt-5-mini", "qwen-flash")
    rubric_axes: tuple = ("goal_achievement", "grounding", "coordination", "recovery")
    # axis weights composing the relative-judge scalar (default equal)
    axis_weights: tuple = ()
    # quality strategy: "auto" | "ground_truth" | "judged"
    quality_strategy: str = "auto"
    # governance
    max_macro_steps: int = 24
    max_retries: int = 6

    # aux cells that may be invoked at most once per trajectory
    optional_aux: tuple = (
        "planner",
        "memory",
        "web_search_exa",
        "web_search_tavily",
        "verifier_a",
        "verifier_b",
    )

    def default_model_for(self, cell: str) -> str:
        """Default model binding for an aux cell."""
        return self.default_model

    @property
    def variant_pool(self) -> list[tuple[str, str]]:
        solvers = [(k, m) for k in self.skill_protocols for m in self.models]
        aux = [(c, self.default_model_for(c)) for c in self.aux_cells]
        return solvers + aux

    def effective_axis_weights(self) -> dict[str, float]:
        if self.axis_weights and len(self.axis_weights) == len(self.rubric_axes):
            return dict(zip(self.rubric_axes, self.axis_weights))
        w = 1.0 / len(self.rubric_axes)
        return {a: w for a in self.rubric_axes}
