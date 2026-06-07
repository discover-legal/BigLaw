# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M2 — Policy graph. UCB1, annealed exploration, Welford, persistence, bandit."""
import random

from topoflow.config import Config
from topoflow.policy_graph import Edge, PolicyGraph
from topoflow.types import Action, Regime, Signature


def _sig(tag=0):
    return Signature(Regime.STRAIGHTFORWARD, (0, 0, 0, 0, 0, 0, 0), tag, 0, 0, 0)


def _a(name):
    return Action("invoke", skill=name, model="haiku")


def test_unvisited_returns_inf():
    g = PolicyGraph(Config())
    assert g.score(_sig(), _a("x")) == float("inf")


def test_cs_anneal_eq5():
    g = PolicyGraph(Config())
    assert abs(g._cs(0) - 1.4) < 1e-9
    assert abs(g._cs(50) - 0.7) < 1e-9
    assert abs(g._cs(75) - 0.5) < 1e-9  # 1.4*2^-1.5=0.494 -> floored to 0.5


def test_welford_matches_reference():
    g = PolicyGraph(Config())
    sig, a = _sig(), _a("s")
    rewards = [0.2, 0.8, 0.5, 0.9, 0.1, 0.4]
    for r in rewards:
        g.backup(sig, a, r, tokens=10, failed=False)
    e = g.edges[(sig, a.key())]
    mean = sum(rewards) / len(rewards)
    var = sum((x - mean) ** 2 for x in rewards) / len(rewards)  # population variance
    assert abs(e.mean_reward - mean) < 1e-9
    # Welford m2/visits == population variance
    assert abs(e.variance * (e.visits) / e.visits - var) < 1e-9
    assert abs(e.m2 / e.visits - var) < 1e-9


def test_save_load_roundtrip(tmp_path):
    g = PolicyGraph(Config())
    sig = _sig(2)
    for r in [0.3, 0.7]:
        g.backup(sig, _a("s"), r, tokens=5, failed=False)
    g.backup(sig, _a("t"), 0.1, tokens=5, failed=True)
    p = str(tmp_path / "graph.json")
    g.save(p)
    g2 = PolicyGraph.load(p, Config())
    assert g2.edges.keys() == g.edges.keys()
    e1 = g.edges[(sig, _a("s").key())]
    e2 = g2.edges[(sig, _a("s").key())]
    assert abs(e1.mean_reward - e2.mean_reward) < 1e-12
    assert g2.sig_visits == g.sig_visits


def test_two_arm_bandit_converges():
    g = PolicyGraph(Config())
    sig = _sig()
    good, bad = _a("good"), _a("bad")
    rng = random.Random(0)
    for _ in range(400):
        a = g.select(sig, [good, bad])
        # good arm pays ~0.8, bad ~0.2
        r = (0.8 if a.key() == good.key() else 0.2) + rng.uniform(-0.05, 0.05)
        g.backup(sig, a, r, tokens=1, failed=False)
    eg = g.edges[(sig, good.key())]
    eb = g.edges[(sig, bad.key())]
    assert eg.visits > eb.visits * 3  # mostly pulled the good arm
    assert g.select(sig, [good, bad]).key() == good.key()


def test_failure_penalty_demotes_failing_arm():
    cfg = Config(lam=0.5)
    g = PolicyGraph(cfg)
    sig = _sig()
    hi_fail, lo_ok = _a("hi_fail"), _a("lo_ok")
    # hi_fail: reward 0.9 but always "failed"; lo_ok: reward 0.6, never fails
    for _ in range(50):
        g.backup(sig, hi_fail, 0.9, tokens=1, failed=True)
        g.backup(sig, lo_ok, 0.6, tokens=1, failed=False)
    # score(hi_fail) = 0.9 + ucb - 0.5*1.0 = 0.4 + ucb ; score(lo_ok) = 0.6 + ucb
    assert g.score(sig, lo_ok) > g.score(sig, hi_fail)


def test_confidence_weight_fractional_visit():
    g = PolicyGraph(Config())
    sig, a = _sig(), _a("s")
    g.backup(sig, a, 1.0, tokens=1, failed=False, weight=0.25)
    e = g.edges[(sig, a.key())]
    assert abs(e.visits - 0.25) < 1e-9
    assert abs(e.mean_reward - 1.0) < 1e-9
