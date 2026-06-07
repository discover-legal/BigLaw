# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Role registry + skill cards (spec §13 / §8.3 Roles [DT])."""
from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Role:
    name: str
    skill_card: str
    required_fields: tuple[str, ...] = ("public", "private", "q_desc", "k_desc")


# ── code role set {Developer, Researcher, Tester, Designer} ──────────────────
CODE_ROLES: list[Role] = [
    Role("Developer", "You implement the solution code. Output working code in `draft_answer`."),
    Role("Researcher", "You gather relevant facts, APIs, and edge cases for the problem."),
    Role("Tester", "You design and run tests; report pass/fail in `verification`."),
    Role("Designer", "You shape the interface and decomposition of the solution."),
]

# ── math role set {ProblemParser, Solver, Verifier} ─────────────────────────
MATH_ROLES: list[Role] = [
    Role("ProblemParser", "You restate the problem precisely and identify givens/goal."),
    Role("Solver", "You derive the solution step by step; put the final answer in `draft_answer`."),
    Role("Verifier", "You check the derivation; report `verification` as supported/refuted."),
]


def role_set_for(domain: str, cfg) -> list[Role]:
    d = (domain or "").lower()
    if d == "math":
        return MATH_ROLES[: cfg.n_agents_math]
    if d == "code":
        return CODE_ROLES[: cfg.n_agents_code]
    # incident / advisory / default → reuse the code role set shape (4 agents)
    return CODE_ROLES[: cfg.n_agents_code]
