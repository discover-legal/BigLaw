# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M7 — Reward & judge."""
from dataclasses import dataclass

from topoflow.agents.transport import LLMRequest, MockTransport
from topoflow.config import Config
from topoflow.policy_graph import PolicyGraph
from topoflow.reward.quality import GroundTruthQ, JudgedQ, make_quality
from topoflow.reward.relative_judge import RelativeJudge, audit_rescore
from topoflow.router import run_task
from topoflow.types import Action, TaskContext


@dataclass
class FakeTraj:
    final_answer: str


HUMANEVAL_GT = {
    "entry_point": "add",
    "test": "def check(candidate):\n    assert candidate(2,3)==5\n    assert candidate(0,0)==0\n",
}


def test_ground_truth_humaneval_fraction():
    q = GroundTruthQ()
    ctx = TaskContext(task_id="he", prompt="add", domain="code", ground_truth=HUMANEVAL_GT)
    good = FakeTraj(final_answer="def add(a,b):\n    return a+b\n")
    bad = FakeTraj(final_answer="def add(a,b):\n    return a-b\n")
    qg, sg = q.score(ctx, good, [])
    qb, sb = q.score(ctx, bad, [])
    assert qg == 1.0 and sg["confidence"] == 1.0
    assert qb == 0.0


def test_apps_style_partial_fraction():
    q = GroundTruthQ()
    gt = {"tests": ["assert f(1)==1", "assert f(2)==4", "assert f(3)==9"]}
    ctx = TaskContext(task_id="apps", prompt="square", domain="code", ground_truth=gt)
    # passes 2 of 3 (wrong for 3)
    cand = FakeTraj(final_answer="def f(x):\n    return x*x if x<3 else 0\n")
    qv, _ = q.score(ctx, cand, [])
    assert abs(qv - 2 / 3) < 1e-9


def _judge_responder(scores_by_model):
    def responder(req: LLMRequest) -> dict:
        if req.purpose == "judge":
            return {"scores": scores_by_model[req.model]}
        return {"_tokens": 10}

    return responder


def test_relative_judge_axes_and_single_judge_confidence():
    cfg = Config()
    # one judge -> zero disagreement -> confidence 1.0
    scores = {
        "j1": [
            {"id": 0, "goal_achievement": 0.9, "grounding": 0.8, "coordination": 0.7, "recovery": 0.6},
            {"id": 1, "goal_achievement": 0.3, "grounding": 0.4, "coordination": 0.2, "recovery": 0.1},
        ]
    }
    rj = RelativeJudge(MockTransport(responder=_judge_responder(scores)), ["j1"], cfg)
    ctx = TaskContext(task_id="j", prompt="p", domain="advisory")
    group = [FakeTraj("A"), FakeTraj("B")]
    res = rj.score_group(ctx, group)
    q0, s0 = res[0]
    assert set(s0["axes"].keys()) == set(cfg.rubric_axes)  # 4 axes reported
    assert abs(q0 - (0.9 + 0.8 + 0.7 + 0.6) / 4) < 1e-9  # equal weights
    assert s0["confidence"] == 1.0  # single judge, no disagreement
    assert q0 > res[1][0]  # A ranked above B


def test_cross_judge_disagreement_std():
    cfg = Config()
    scores = {
        "j1": [{"id": 0, "goal_achievement": 1.0, "grounding": 1.0, "coordination": 1.0, "recovery": 1.0}],
        "j2": [{"id": 0, "goal_achievement": 0.0, "grounding": 0.0, "coordination": 0.0, "recovery": 0.0}],
        "j3": [{"id": 0, "goal_achievement": 0.5, "grounding": 0.5, "coordination": 0.5, "recovery": 0.5}],
    }
    rj = RelativeJudge(MockTransport(responder=_judge_responder(scores)), ["j1", "j2", "j3"], cfg)
    ctx = TaskContext(task_id="j", prompt="p")
    q, s = rj.score_group(ctx, [FakeTraj("A")])[0]
    assert s["n_judges"] == 3
    assert s["disagreement_std"] > 0.3  # large disagreement
    assert s["confidence"] < 0.7  # low confidence
    assert abs(q - 0.5) < 1e-9  # mean of 1.0, 0.0, 0.5


def test_single_vs_three_judge_reported_separately():
    cfg = Config()
    scores = {
        m: [{"id": 0, "goal_achievement": v, "grounding": v, "coordination": v, "recovery": v}]
        for m, v in (("claude-haiku-4-5", 0.8), ("gpt-5-mini", 0.6), ("qwen-flash", 0.4))
    }
    tx = MockTransport(responder=_judge_responder(scores))
    ctx = TaskContext(task_id="j", prompt="p")
    group = [FakeTraj("A")]
    live = RelativeJudge(tx, [cfg.live_judge], cfg).score_group(ctx, group)[0]
    audit = audit_rescore(ctx, group, tx, cfg)[0]
    assert abs(live[0] - 0.8) < 1e-9  # single live judge
    assert abs(audit[0] - 0.6) < 1e-9  # mean of three
    assert live[0] != audit[0]  # reported separately, may differ


def test_confidence_weighting_scales_backup_via_judged_q():
    cfg = Config(topo_modes=("linear",), quality_strategy="judged")
    g = PolicyGraph(cfg)
    # judged with low-confidence (disagreeing) judges -> fractional visits
    scores = {
        "j1": [{"id": 0, "goal_achievement": 1.0, "grounding": 1.0, "coordination": 1.0, "recovery": 1.0}],
        "j2": [{"id": 0, "goal_achievement": 0.0, "grounding": 0.0, "coordination": 0.0, "recovery": 0.0}],
    }
    tx = MockTransport(responder=_make_loop_responder(scores))
    rj = RelativeJudge(tx, ["j1", "j2"], cfg)
    q = JudgedQ(judge=rj)
    ctx = TaskContext(task_id="cw", prompt="p", domain="advisory")

    # one deterministic decision (evaluator -> terminate) so the single backup's
    # fractional visit equals the judge confidence exactly.
    def pick_eval(sig, legal):
        for a in legal:
            if a.kind == "invoke" and a.skill == "evaluator":
                return a
        return legal[0]

    run_task(ctx, cfg, g, tx, quality=q, select_fn=pick_eval)
    visits = [e.visits for e in g.edges.values()]
    # disagreeing judges (1.0 vs 0.0) -> std 0.5 -> confidence 0.5 -> visit 0.5
    assert len(visits) == 1
    assert abs(visits[0] - 0.5) < 1e-9


def _make_loop_responder(judge_scores):
    """Responder serving both the macro loop (invoke/agent) and the judge."""
    def responder(req: LLMRequest) -> dict:
        if req.purpose == "judge":
            return {"scores": judge_scores[req.model]}
        role = (req.role or "").lower()
        out = {"_tokens": 50}
        if role.startswith("solver"):
            out["draft_answer"] = "X"
        if role == "evaluator":
            out["complete"] = True
        return out
    return responder
