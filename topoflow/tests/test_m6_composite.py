# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M6 — Composite integration: topology(dytopo) is ONE decision-path entry with
ONE reward but ALL inner tokens charged; beliefs update once; signature recomputed."""
from topoflow.agents.transport import LLMRequest, MockTransport
from topoflow.config import Config
from topoflow.policy_graph import PolicyGraph
from topoflow.router import run_task
from topoflow.types import Action, TaskContext


def _scripted():
    q = {"Developer": "alpha", "Researcher": "gamma", "Tester": "beta", "Designer": "delta"}
    k = {"Developer": "beta", "Researcher": "gamma", "Tester": "alpha", "Designer": "delta"}

    def responder(req: LLMRequest) -> dict:
        if req.purpose == "agent":
            r = req.role
            out = {"public": f"{r}-pub", "private": f"{r}-priv",
                   "q_desc": q[r], "k_desc": k[r], "_tokens": 50}
            if r == "Developer":
                out["draft_answer"] = "42"
                out["complete"] = True  # ends the dytopo cell after round 0
            return out
        # invoke purpose
        if (req.role or "").startswith("solver"):
            return {"draft_answer": "42", "_tokens": 100}
        return {"_tokens": 100}

    return responder


def _planned_selector(plan_keys):
    """Return a selector that walks the desired action keys, matching against the
    legal set; falls back to the evaluator invoke to terminate."""
    state = {"i": 0}

    def select(sig, legal):
        if state["i"] < len(plan_keys):
            want = plan_keys[state["i"]]
            state["i"] += 1
            for a in legal:
                if a.key() == want:
                    return a
        for a in legal:
            if a.kind == "invoke" and a.skill == "evaluator":
                return a
        return legal[0]

    return select


def test_dytopo_is_one_entry_one_reward_all_tokens():
    cfg = Config(token_cap=100000)
    g = PolicyGraph(cfg)
    ctx = TaskContext(task_id="m6", prompt="solve", domain="code", ground_truth="42")
    tx = MockTransport(responder=_scripted())

    dytopo_key = Action("topology", topo_mode="dytopo", tau_bucket=1,
                        k_in=cfg.dytopo_k_in, round_bucket=0).key()
    plan = [
        Action("invoke", skill="solver_cot", model="haiku").key(),
        Action("skip", target="planner").key(),
        dytopo_key,
    ]
    traj = run_task(ctx, cfg, g, tx, select_fn=_planned_selector(plan))

    # exactly three decisions: invoke, skip, dytopo (dytopo set complete -> end)
    dp = traj.trace.decision_path()
    kinds = [a.kind for (_s, a) in dp]
    assert kinds == ["invoke", "skip", "topology"], kinds

    # each visited (sig, action) backed up exactly once
    for (sig_i, action_i) in dp:
        e = g.edges[(sig_i, action_i.key())]
        assert abs(e.visits - 1.0) < 1e-9

    # the dytopo cell charged ALL inner-round tokens: 4 agents * 50 * 1 round
    topo_events = [ev for ev in traj.trace.events if ev["type"] == "topology"]
    assert len(topo_events) == 1
    ev = topo_events[0]
    assert ev["rounds_run"] == 1
    assert ev["total_tokens"] == 4 * 50  # sum over the cell's rounds

    # whole-trajectory tokens include the solver invoke (100) + dytopo (200)
    assert traj.trace.tokens == 100 + 200

    # correct answer flowed through -> Q == 1
    assert traj.quality == 1.0


def test_signature_recomputed_after_dytopo():
    # After a dytopo cell populates draft/merged answer, the next macro signature
    # reflects higher correctness (different bucket) than the initial one.
    cfg = Config(token_cap=100000)
    g = PolicyGraph(cfg)
    ctx = TaskContext(task_id="m6b", prompt="solve", domain="code", ground_truth="42")

    # dytopo that does NOT complete, so the loop continues to a second decision
    def responder(req):
        q = {"Developer": "alpha", "Researcher": "gamma", "Tester": "beta", "Designer": "delta"}
        k = {"Developer": "beta", "Researcher": "gamma", "Tester": "alpha", "Designer": "delta"}
        if req.purpose == "agent":
            r = req.role
            out = {"public": f"{r}", "private": f"{r}", "q_desc": q[r], "k_desc": k[r], "_tokens": 10}
            if r == "Developer":
                out["draft_answer"] = "42"
            return out
        if (req.role or "") == "evaluator":
            return {"complete": True, "_tokens": 10}
        return {"_tokens": 10}

    dytopo_key = Action("topology", topo_mode="dytopo", tau_bucket=1,
                        k_in=cfg.dytopo_k_in, round_bucket=0).key()
    eval_key = Action("invoke", skill="evaluator", model="haiku").key()
    sel = _planned_selector([dytopo_key, eval_key])
    traj = run_task(ctx, cfg, g, MockTransport(responder=responder), select_fn=sel)

    dp = traj.trace.decision_path()
    assert len(dp) == 2
    sig_before, sig_after = dp[0][0], dp[1][0]
    # the post-dytopo signature differs (handoff mask now has draft_answer set)
    assert sig_before != sig_after
    assert sig_after.handoff_mask[5] == 1  # draft_answer index in HANDOFF_FIELDS
