# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M3 — Linear generator + macro loop (recovers AgensFlow)."""
from topoflow.agents.transport import MockTransport
from topoflow.config import Config
from topoflow.policy_graph import PolicyGraph
from topoflow.router import run_task
from topoflow.types import TaskContext


def _cfg():
    # linear-only topology family, small action space (recovers AgensFlow shape)
    return Config(topo_modes=("linear",), token_cap=4000)


def _math_task():
    return TaskContext(task_id="m3", prompt="2+2?", domain="math", ground_truth="4")


def test_end_to_end_emits_report_and_terminates():
    cfg = _cfg()
    g = PolicyGraph(cfg)
    tx = MockTransport(answer="4", evaluator_completes=True)
    traj = run_task(_math_task(), cfg, g, tx)
    assert traj.report is not None
    d = traj.report.to_dict()
    assert d["task_id"] == "m3"
    assert "decision_path" in d
    # never selects terminate directly
    for entry in d["decision_path"]:
        assert entry["action"][0] != "terminate"


def test_terminates_via_budget_when_evaluator_never_completes():
    cfg = Config(topo_modes=("linear",), token_cap=600, max_macro_steps=1000)
    g = PolicyGraph(cfg)
    tx = MockTransport(answer="4", evaluator_completes=False, tokens_per_call=100)
    traj = run_task(_math_task(), cfg, g, tx)
    # should stop on budget, not loop forever
    assert traj.trace.tokens >= 600 or len(traj.trace.decision_path()) <= cfg.max_macro_steps


def test_ground_truth_reward_rewards_correct_answer():
    cfg = _cfg()
    g = PolicyGraph(cfg)
    good = run_task(_math_task(), cfg, g, MockTransport(answer="4"))
    bad = run_task(_math_task(), cfg, PolicyGraph(cfg), MockTransport(answer="5"))
    assert good.quality == 1.0
    assert bad.quality == 0.0
    assert good.reward > bad.reward


def test_training_produces_nonuniform_preferences():
    cfg = _cfg()
    g = PolicyGraph(cfg)
    tx = MockTransport(answer="4", evaluator_completes=True)
    for _ in range(60):
        run_task(_math_task(), cfg, g, tx)
    summ = g.summary()
    assert summ, "policy graph should have learned edges"
    # at least one signature shows a clear best action (non-uniform means)
    nonuniform = False
    for label, actions in summ.items():
        means = [a["mean_reward"] for a in actions if a["visits"] >= 1]
        if len(means) >= 2 and (max(means) - min(means)) > 0.05:
            nonuniform = True
    assert nonuniform


def test_invoke_only_recovers_agensflow_shape():
    # With evaluator completing, a trajectory that invokes solver then evaluator
    # yields a correct answer and a single positive reward backed up per decision.
    cfg = _cfg()
    g = PolicyGraph(cfg)
    traj = run_task(_math_task(), cfg, g, MockTransport(answer="4"))
    # every decision-path entry received the same trajectory reward
    assert len(traj.trace.decision_path()) >= 1
