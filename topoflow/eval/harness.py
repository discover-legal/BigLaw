# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Evaluation harness (spec §14): arms 1–7, datasets, metrics H1–H5.

Runs entirely on MockTransport for offline smoke (M8); swap in a live transport
for M9. Learning arms run for `epochs` and report at plateau (last epoch).
"""
from __future__ import annotations

import json
from dataclasses import dataclass
from statistics import mean
from typing import Callable, Optional

from ..agents.transport import LLMRequest, MockTransport
from ..config import Config
from ..policy_graph import PolicyGraph
from ..reward.quality import GroundTruthQ, JudgedQ
from ..reward.relative_judge import RelativeJudge, audit_rescore
from ..router import run_task
from ..topology.semantic_match import MockEmbedder
from . import metrics as M
from .datasets import mock_suite


# ── deterministic mock transport for the suite ──────────────────────────────
def harness_responder(req: LLMRequest) -> dict:
    if req.purpose == "judge":
        base = {"claude-haiku-4-5": 0.7, "gpt-5-mini": 0.6, "qwen-flash": 0.5}.get(req.model, 0.6)
        cands = req.meta.get("candidates", [])
        return {
            "scores": [
                {"id": c["id"], "goal_achievement": base, "grounding": base,
                 "coordination": base, "recovery": base}
                for c in cands
            ]
        }
    role = (req.role or "").lower()
    out: dict = {"_tokens": 40}
    if req.purpose == "agent":
        out.update({"public": f"{req.role}", "private": f"{req.role}",
                    "q_desc": "need inputs", "k_desc": "provide expertise"})
        if "develop" in role or "solver" in role:
            out["draft_answer"] = "4"
        if "test" in role or "verif" in role:
            out["verification"] = "supported"
        return out
    if role.startswith("solver"):
        out["draft_answer"] = "4"
    elif role == "evaluator":
        out["complete"] = True
    elif role.startswith("verifier"):
        out["verification"] = "supported"
    elif role == "memory" or role.startswith("web_search"):
        out["evidence"] = ["fact"]
    elif role == "planner":
        out.update({"goal": "g", "subproblem": "s"})
    return out


# ── fixed-policy selectors (non-learning arms) ──────────────────────────────
def _fixed_linear_selector():
    state = {"i": 0}

    def sel(sig, legal):
        state["i"] += 1
        if state["i"] == 1:
            for a in legal:
                if a.kind == "topology" and a.topo_mode == "linear":
                    return a
        for a in legal:
            if a.kind == "invoke" and a.skill == "evaluator":
                return a
        return legal[0]

    return sel


def _fixed_dytopo_selector():
    state = {"i": 0}

    def sel(sig, legal):
        state["i"] += 1
        if state["i"] == 1:
            best = None
            for a in legal:
                if a.kind == "topology" and a.topo_mode == "dytopo" and a.tau_bucket == 1 and a.round_bucket == 0:
                    best = a
            if best:
                return best
        for a in legal:
            if a.kind == "invoke" and a.skill == "evaluator":
                return a
        return legal[0]

    return sel


@dataclass
class Arm:
    name: str
    topo_modes: tuple
    learning: bool
    selector_factory: Optional[Callable] = None
    warm_from: Optional[str] = None


ARMS: list[Arm] = [
    Arm("1_fixed_linear", ("linear",), False, _fixed_linear_selector),
    Arm("2_pure_dytopo", ("dytopo",), False, _fixed_dytopo_selector),
    Arm("3_pure_agensflow", ("linear",), True, None),
    Arm("4_topoflow_linear", ("linear",), True, None),
    Arm("5_topoflow_dytopo", ("dytopo",), True, None),
    Arm("6_topoflow_free_cold", ("linear", "dytopo"), True, None),
    Arm("7_topoflow_free_warm", ("linear", "dytopo"), True, None, warm_from="6_topoflow_free_cold"),
]


def _quality_for(task, transport, live_rj):
    if task.ground_truth is not None:
        return GroundTruthQ()
    return JudgedQ(judge=live_rj)


def run_arm(arm: Arm, tasks, base_cfg: Config, transport, live_rj, epochs: int,
            warm_graph: Optional[PolicyGraph] = None) -> dict:
    cfg = Config(**{**base_cfg.__dict__, "topo_modes": arm.topo_modes})
    graph = warm_graph if warm_graph is not None else PolicyGraph(cfg)
    embedder = MockEmbedder()
    n_epochs = epochs if arm.learning else 1
    tokens_per_epoch: list[int] = []
    final: list[tuple] = []

    for _e in range(n_epochs):
        epoch_tokens = 0
        snapshot: list[tuple] = []
        for task in tasks:
            q = _quality_for(task, transport, live_rj)
            sel = arm.selector_factory() if arm.selector_factory else None
            traj = run_task(task, cfg, graph, transport, embedder=embedder, quality=q, select_fn=sel)
            epoch_tokens += traj.trace.tokens
            snapshot.append((task, traj))
        tokens_per_epoch.append(epoch_tokens)
        final = snapshot

    # audit re-score (H5): 3-judge for judged tasks; ground-truth stable otherwise
    trajectories = []
    audit_qs = []
    for task, traj in final:
        if task.ground_truth is not None:
            audit_q = traj.quality
        else:
            audit_q = audit_rescore(task, [traj], transport, cfg)[0][0]
        audit_qs.append(audit_q)
        trajectories.append(
            {
                "task_id": task.task_id,
                "scenario_class": task.scenario_class,
                "domain": task.domain,
                "quality": traj.quality,
                "audit_quality": audit_q,
                "tokens": traj.trace.tokens,
                "reward": traj.reward,
                "topology_modes": M._topology_modes(traj),
            }
        )

    return {
        "arm": arm.name,
        "trajectories": trajectories,
        "mean_reward": mean(t["reward"] for t in trajectories) if trajectories else 0.0,
        "mean_quality": mean(t["quality"] for t in trajectories) if trajectories else 0.0,
        "audit_mean_quality": mean(audit_qs) if audit_qs else 0.0,
        "mean_tokens": mean(t["tokens"] for t in trajectories) if trajectories else 0.0,
        "tokens_per_epoch": tokens_per_epoch,
        "graph": graph,
    }


def run_suite(cfg: Optional[Config] = None, transport=None, tasks=None,
              epochs: int = 3, out_path: Optional[str] = None) -> dict:
    cfg = cfg or Config()
    transport = transport or MockTransport(responder=harness_responder)
    tasks = tasks if tasks is not None else mock_suite()
    live_rj = RelativeJudge(transport, [cfg.live_judge], cfg)

    results: dict[str, dict] = {}
    for arm in ARMS:
        warm = None
        if arm.warm_from and arm.warm_from in results:
            # warm-start from a sibling graph (init from the free cold-start graph)
            warm = results[arm.warm_from]["graph"]
        results[arm.name] = run_arm(arm, tasks, cfg, transport, live_rj, epochs, warm_graph=warm)

    metrics = M.compute_all(results)
    report = {
        "arms": {
            name: {
                k: v for k, v in res.items() if k != "graph" and k != "trajectories"
            }
            for name, res in results.items()
        },
        "metrics": metrics,
        "n_tasks": len(tasks),
        "epochs": epochs,
    }
    if out_path:
        with open(out_path, "w") as f:
            json.dump(report, f, indent=2)
    return report
