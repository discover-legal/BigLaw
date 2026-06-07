# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""RelativeJudge + cross-judge audit (spec §7) [AF].

A group of N same-class peer trajectories for the same task is ranked
side-by-side against an explicit rubric. Each judge emits per-trajectory scores
on 4 axes; the scalar composes via axis weights. Relative (not absolute) scoring
is the anti-reward-hacking mechanism. Cross-judge averaging surfaces per-judge
disagreement std as confidence telemetry.
"""
from __future__ import annotations

from statistics import pstdev

from ..agents.transport import LLMRequest
from ..config import Config

RUBRIC = (
    "Rank the candidate trajectories RELATIVE to each other for the same task. "
    "Score each on four axes in [0,1]:\n"
    "- goal_achievement: did it actually solve the task?\n"
    "- grounding: are claims supported by evidence/reasoning?\n"
    "- coordination: did the agents/cells collaborate coherently?\n"
    "- recovery: did it detect and recover from errors?\n"
    "Return JSON {\"scores\":[{\"id\":int, <axis>:float, ...}, ...]}."
)


def _clip01(x: float) -> float:
    return 0.0 if x < 0.0 else 1.0 if x > 1.0 else x


class RelativeJudge:
    def __init__(self, transport, judge_models, cfg: Config):
        self.transport = transport
        self.judge_models = list(judge_models)
        self.cfg = cfg
        self.axes = cfg.rubric_axes
        self.weights = cfg.effective_axis_weights()

    def _index_scores(self, raw, n: int) -> list[dict]:
        """Normalize a judge's raw `scores` list into per-candidate axis dicts."""
        by_id: dict[int, dict] = {}
        for row in raw or []:
            try:
                idx = int(row.get("id"))
            except (TypeError, ValueError):
                continue
            by_id[idx] = {ax: _clip01(float(row.get(ax, 0.5))) for ax in self.axes}
        return [by_id.get(i, {ax: 0.5 for ax in self.axes}) for i in range(n)]

    def score_group(self, ctx, group) -> list[tuple[float, dict]]:
        cands = [
            {"id": i, "answer": (getattr(t, "final_answer", "") or "")[:2000]}
            for i, t in enumerate(group)
        ]
        per_judge: list[list[dict]] = []
        for jm in self.judge_models:
            req = LLMRequest(
                model=jm,
                system=RUBRIC,
                user=f"Task: {ctx.prompt}\nCandidates: {cands}",
                schema=("scores",),
                purpose="judge",
                meta={"candidates": cands},
            )
            fields, _tokens = self.transport.complete(req)
            per_judge.append(self._index_scores(fields.get("scores"), len(group)))

        results: list[tuple[float, dict]] = []
        for i in range(len(group)):
            axis_means = {
                ax: sum(per_judge[j][i][ax] for j in range(len(per_judge))) / len(per_judge)
                for ax in self.axes
            }
            scalars = [
                sum(self.weights[ax] * per_judge[j][i][ax] for ax in self.axes)
                for j in range(len(per_judge))
            ]
            q = sum(self.weights[ax] * axis_means[ax] for ax in self.axes)
            std = pstdev(scalars) if len(scalars) > 1 else 0.0
            results.append(
                (
                    q,
                    {
                        "confidence": _clip01(1.0 - std),
                        "axes": axis_means,
                        "per_judge_scalars": scalars,
                        "disagreement_std": std,
                        "n_judges": len(per_judge),
                        "kind": "relative_judge",
                    },
                )
            )
        return results

    def score_one(self, ctx, trajectory, peers) -> tuple[float, dict]:
        group = [trajectory] + list(peers or [])
        return self.score_group(ctx, group)[0]


def audit_rescore(ctx, group, transport, cfg: Config) -> list[tuple[float, dict]]:
    """Post-hoc cross-family three-judge audit (metric H5)."""
    return RelativeJudge(transport, cfg.audit_judges, cfg).score_group(ctx, group)
