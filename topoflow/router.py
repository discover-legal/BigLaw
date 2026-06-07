# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Macro control loop (spec §9) [AF] + [NEW].

Layered: fold (1) -> typed cells (2) -> UCB1 select (3) -> reward+backup (4) ->
governance/report (5). A topology(dytopo) step is ONE decision-path entry with
ONE backed-up reward, but charges ALL its inner-round tokens.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional

from . import beliefs as beliefs_mod
from . import governance
from .actions import Plan, legal_actions
from .agents.roles import role_set_for
from .agents.transport import LLMRequest, Transport
from .config import Config
from .policy_graph import PolicyGraph
from .reward.hybrid import hybrid_reward
from .reward.quality import make_quality
from .signature import fold
from .topology.base import CompositeTopologyCell
from .topology.dytopo import DyTopoGenerator
from .topology.linear import LinearWithSkipGenerator
from .trace import (
    Trace,
    build_run_report,
    invoke_event,
    skip_event,
    topology_event,
)
from .types import BeliefVector, CellOutput, HandoffState, TaskContext


@dataclass
class Budget:
    cap: int
    used: int = 0

    def charge(self, t: int) -> None:
        self.used += int(t)

    def exhausted(self) -> bool:
        return self.used >= self.cap


@dataclass
class Trajectory:
    trace: Trace
    reward: float
    quality: float
    sub_scores: dict
    report: object = None
    aborted: bool = False


def make_generator(mode: str, cfg: Config) -> CompositeTopologyCell:
    if mode == "linear":
        return LinearWithSkipGenerator()
    if mode == "dytopo":
        return DyTopoGenerator()
    raise ValueError(f"unknown topology mode {mode!r}")


# ── invoke cells (Layer 2 typed I/O) ────────────────────────────────────────
_SOLVER_HF = "draft_answer"


def run_cell(skill: str, model: str, ctx: TaskContext, handoff: HandoffState,
             transport: Transport, cfg: Config, search_provider=None) -> CellOutput:
    s_pre = skill.lower()
    # web-search cells are tool actions, not LLM calls, when a provider is wired.
    if s_pre.startswith("web_search") and search_provider is not None:
        results = search_provider.search(ctx.prompt, k=5)
        out = CellOutput(skill=skill, model=model, tokens=0)
        out.handoff_delta["evidence"] = results
        return out
    present = [k for k, v in handoff.fields.items() if v is not None]
    req = LLMRequest(
        model=model or cfg.default_model,
        system=f"You are the {skill} cell.",
        user=f"Task: {ctx.prompt}\nKnown: {', '.join(present) or '(nothing)'}",
        schema=("answer",),
        purpose="invoke",
        role=skill,
        meta={"skill": skill},
    )
    fields, tokens = transport.complete(req)
    out = CellOutput(skill=skill, model=model, tokens=tokens, failed=bool(fields.get("_failed")))
    s = skill.lower()
    if s.startswith("solver"):
        ans = fields.get("draft_answer") or fields.get("answer")
        if ans is not None:
            out.handoff_delta[_SOLVER_HF] = ans
    elif s == "planner":
        if fields.get("goal") is not None:
            out.handoff_delta["goal"] = fields["goal"]
        if fields.get("subproblem") is not None:
            out.handoff_delta["subproblem"] = fields["subproblem"]
    elif s == "memory" or s.startswith("web_search"):
        ev = fields.get("evidence")
        if ev is not None:
            out.handoff_delta["evidence"] = ev
    elif s.startswith("verifier"):
        v = fields.get("verification")
        if v is not None:
            out.handoff_delta["verification"] = v
            out.meta["verdict"] = v
    elif s == "evaluator":
        out.complete = bool(fields.get("complete"))
    if out.failed:
        out.retries = 1
    return out


def apply_handoff(handoff: HandoffState, out: CellOutput) -> HandoffState:
    for k, v in out.handoff_delta.items():
        handoff.set(k, v)
    return handoff


def merge_handoff(handoff: HandoffState, merged: HandoffState) -> HandoffState:
    for k, v in merged.fields.items():
        if v is not None:
            handoff.set(k, v)
    return handoff


def evaluator_marks_complete(handoff: HandoffState) -> bool:
    return bool(handoff.get("complete"))


# ── macro loop ──────────────────────────────────────────────────────────────
def run_task(
    ctx: TaskContext,
    cfg: Config,
    policy_graph: PolicyGraph,
    transport: Transport,
    embedder=None,
    quality=None,
    peers: Optional[list] = None,
    select_fn=None,
    search_provider=None,
) -> Trajectory:
    try:
        governance.preflight(ctx, cfg, transport)
    except governance.GovernanceAbort as e:
        trace = Trace(task_id=ctx.task_id)
        report = build_run_report(ctx.task_id, trace, 0.0, 0.0, {"confidence": 1.0}, aborted=True, reason=str(e))
        return Trajectory(trace=trace, reward=0.0, quality=0.0, sub_scores={}, report=report, aborted=True)

    handoff, beliefs = HandoffState(), BeliefVector()
    trace = Trace(task_id=ctx.task_id)
    budget = Budget(cfg.token_cap)
    plan = Plan(list(cfg.optional_aux))
    steps = 0

    while True:
        steps += 1
        sig = fold(ctx, handoff, beliefs, cfg)  # Layer 1
        legal = legal_actions(sig, handoff, plan, budget, cfg)
        if not legal or budget.exhausted() or steps > cfg.max_macro_steps:
            break  # implicit terminate
        # Layer 3, UCB1 (select_fn is an optional policy-injection seam for tests
        # and future policies; it must return one of the legal actions).
        action = select_fn(sig, legal) if select_fn is not None else policy_graph.select(sig, legal)
        trace.record_decision(sig, action)

        if action.kind == "skip":
            plan.mark_skipped(action.target)
            trace.add(skip_event(action))
            continue

        if action.kind == "topology":
            gen = make_generator(action.topo_mode, cfg)
            agents = role_set_for(ctx.domain, cfg)
            res = gen.run(
                ctx, handoff, agents, action, transport, embedder, cfg,
                budget_remaining=budget.cap - budget.used,
            )  # COMPOSITE
            handoff = merge_handoff(handoff, res.merged_handoff)
            beliefs = beliefs_mod.update_beliefs_topology(beliefs, res, cfg)
            budget.charge(res.total_tokens)
            trace.tokens += res.total_tokens
            trace.retries += res.retries
            if res.failed:
                trace.any_failure = True
            trace.add(topology_event(action, res))
            if res.failed or evaluator_marks_complete(handoff):
                break
            continue

        if action.kind == "invoke":
            if action.skill in cfg.optional_aux:
                plan.mark_invoked(action.skill)
            out = run_cell(action.skill, action.model, ctx, handoff, transport, cfg, search_provider)
            handoff = apply_handoff(handoff, out)
            beliefs = beliefs_mod.update_beliefs_cell(beliefs, out, cfg)
            budget.charge(out.tokens)
            trace.tokens += out.tokens
            trace.retries += out.retries
            if out.failed:
                trace.any_failure = True
            trace.add(invoke_event(action, out))
            if action.skill == "evaluator" and out.complete:
                break
            if governance.violation(trace, cfg):
                break

    # final answer for quality scoring
    trace.final_answer = str(handoff.get("merged_answer") or handoff.get("draft_answer") or "")

    q = quality or make_quality(ctx, cfg, transport)
    Q, sub = q.score(ctx, trace, peers or [])
    confidence = float(sub.get("confidence", 1.0))
    r = hybrid_reward(Q, trace.tokens, trace.retries, cfg)

    for (sig_i, action_i) in trace.decision_path():
        policy_graph.backup(sig_i, action_i, r, trace.tokens, trace.any_failure, weight=confidence)

    report = build_run_report(ctx.task_id, trace, r, Q, sub)
    return Trajectory(trace=trace, reward=r, quality=Q, sub_scores=sub, report=report)
