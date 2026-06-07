# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M5 — DyTopo generator + sync barrier."""
from topoflow.agents.roles import role_set_for
from topoflow.agents.transport import LLMRequest, MockTransport
from topoflow.config import Config
from topoflow.topology.dytopo import DyTopoGenerator
from topoflow.topology.semantic_match import MockEmbedder
from topoflow.types import Action, HandoffState, TaskContext


def _scripted():
    """Descriptors give Developer exactly one provider (Tester); Researcher and
    Designer are isolated. Private messages are role-tagged so we can assert the
    sync barrier."""
    q = {"Developer": "alpha", "Researcher": "gamma", "Tester": "beta", "Designer": "delta"}
    k = {"Developer": "beta", "Researcher": "gamma", "Tester": "alpha", "Designer": "delta"}

    def responder(req: LLMRequest) -> dict:
        r = req.role
        out = {
            "public": f"{r}-public",
            "private": f"{r}-private",
            "q_desc": q[r],
            "k_desc": k[r],
            "_tokens": 50,
        }
        if r in ("Developer", "Solver"):
            out["draft_answer"] = "42"
        if r in ("Tester", "Verifier"):
            out["verification"] = "supported"
        return out

    return responder


def _action(cfg, tau_bucket=1, round_bucket=0):
    return Action("topology", topo_mode="dytopo", tau_bucket=tau_bucket,
                  k_in=cfg.dytopo_k_in, round_bucket=round_bucket)


def test_sync_barrier_excludes_non_neighbor_privates():
    cfg = Config()
    tx = MockTransport(responder=_scripted())
    gen = DyTopoGenerator()
    ctx = TaskContext(task_id="m5", prompt="solve", domain="code")
    agents = role_set_for("code", cfg)
    res = gen.run(ctx, HandoffState(), agents, _action(cfg, round_bucket=0), tx, MockEmbedder(), cfg)

    rounds = res.subtrace["rounds"]
    assert len(rounds) == cfg.round_buckets[0]  # rounds_run == round bucket (3)
    mem0 = rounds[0]["memory_after"]
    dev_mem = " ".join(mem0["Developer"])
    # Developer's only provider is Tester -> Tester's private present
    assert "[from Tester] Tester-private" in dev_mem
    # non-neighbors' private messages must NOT leak into Developer's memory
    assert "Researcher-private" not in dev_mem
    assert "Designer-private" not in dev_mem


def test_inner_round_tokens_aggregate():
    cfg = Config()
    tx = MockTransport(responder=_scripted())
    gen = DyTopoGenerator()
    ctx = TaskContext(task_id="m5", prompt="solve", domain="code")
    agents = role_set_for("code", cfg)
    res = gen.run(ctx, HandoffState(), agents, _action(cfg, round_bucket=1), tx, MockEmbedder(), cfg)
    # 4 agents * 50 tokens * 6 rounds (round_bucket index 1 -> 6)
    assert res.rounds_run == 6
    assert res.total_tokens == 4 * 50 * 6


def test_deterministic_merged_handoff():
    cfg = Config()
    gen = DyTopoGenerator()
    ctx = TaskContext(task_id="m5", prompt="solve", domain="code")
    agents = role_set_for("code", cfg)
    r1 = gen.run(ctx, HandoffState(), agents, _action(cfg), MockTransport(responder=_scripted()), MockEmbedder(), cfg)
    r2 = gen.run(ctx, HandoffState(), agents, _action(cfg), MockTransport(responder=_scripted()), MockEmbedder(), cfg)
    assert r1.merged_handoff.fields == r2.merged_handoff.fields
    assert r1.merged_handoff.get("draft_answer") == "42"
    assert r1.merged_handoff.get("merged_answer") == "42"  # promoted
    assert r1.merged_handoff.get("verification") == "supported"


def test_nested_subtrace_records_every_round():
    cfg = Config()
    gen = DyTopoGenerator()
    ctx = TaskContext(task_id="m5", prompt="solve", domain="code")
    agents = role_set_for("code", cfg)
    res = gen.run(ctx, HandoffState(), agents, _action(cfg, round_bucket=2), MockTransport(responder=_scripted()), MockEmbedder(), cfg)
    rounds = res.subtrace["rounds"]
    assert len(rounds) == 10  # round_bucket index 2 -> 10
    for t, rd in enumerate(rounds):
        assert rd["t"] == t
        assert "edges" in rd and "order" in rd and "descriptors" in rd
        assert sorted(rd["order"]) == sorted(a.name for a in agents)
