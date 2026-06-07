# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M4 — Semantic matching: embed->cosine->threshold->top-k; ordering."""
from topoflow.topology.semantic_match import (
    MockEmbedder,
    build_edges,
    order_incoming,
    relevance_matrix,
    topo_or_cyclebreak,
)


def test_build_edges_threshold_and_no_self():
    # R[i][j] = need of i vs provide of j
    R = [
        [1.0, 0.6, 0.1],
        [0.2, 1.0, 0.9],
        [0.4, 0.05, 1.0],
    ]
    edges = build_edges(R, tau=0.3, k_in=3)
    pairs = {(j, i) for (j, i, _s) in edges}
    assert (1, 0) in pairs  # R[0][1]=0.6 > 0.3 -> provider 1 -> recipient 0
    assert (2, 1) in pairs  # R[1][2]=0.9
    assert (0, 2) in pairs  # R[2][0]=0.4
    # no self edges despite diagonal 1.0
    assert all(j != i for (j, i) in pairs)


def test_in_degree_cap():
    R = [
        [1.0, 0.9, 0.8, 0.7],
        [0.0, 1.0, 0.0, 0.0],
        [0.0, 0.0, 1.0, 0.0],
        [0.0, 0.0, 0.0, 1.0],
    ]
    edges = build_edges(R, tau=0.3, k_in=2)
    incoming0 = [j for (j, i, _s) in edges if i == 0]
    assert len(incoming0) == 2  # capped at k_in
    assert set(incoming0) == {1, 2}  # top-2 by score (0.9, 0.8)


def test_developer_tester_mutual_edge_via_encoder():
    # Hand-built descriptors (DyTopo Table 9 spirit): Developer<->Tester strong.
    names = ["Developer", "Researcher", "Tester", "Designer"]
    q_desc = {
        "Developer": "tests testing coverage",  # dev needs tests
        "Researcher": "facts references",
        "Tester": "code implementation",  # tester needs code
        "Designer": "layout palette",
    }
    k_desc = {
        "Developer": "code implementation",  # dev provides code
        "Researcher": "facts references",
        "Tester": "tests testing coverage",  # tester provides tests
        "Designer": "layout palette",
    }
    emb = MockEmbedder()
    Q = emb.embed([q_desc[n] for n in names])
    K = emb.embed([k_desc[n] for n in names])
    R = relevance_matrix(Q, K)
    edges = build_edges(R, tau=0.3, k_in=3)
    pairs = {(names[j], names[i]) for (j, i, _s) in edges}
    assert ("Tester", "Developer") in pairs  # tester provides tests to developer
    assert ("Developer", "Tester") in pairs  # developer provides code to tester


def test_topo_sort_dag_provider_before_recipient():
    # DAG: 0->1, 0->2, 1->3, 2->3
    edges = [(0, 1, 1.0), (0, 2, 1.0), (1, 3, 1.0), (2, 3, 1.0)]
    order = topo_or_cyclebreak(4, edges)
    assert sorted(order) == [0, 1, 2, 3]  # full permutation
    pos = {n: i for i, n in enumerate(order)}
    for (j, i, _s) in edges:
        assert pos[j] < pos[i]  # provider before recipient


def test_cycle_break_valid_permutation():
    # cycle 0->1->2->0
    edges = [(0, 1, 1.0), (1, 2, 1.0), (2, 0, 1.0)]
    order = topo_or_cyclebreak(3, edges)
    assert sorted(order) == [0, 1, 2]  # still a valid full permutation


def test_order_incoming_descending():
    edges = [(1, 0, 0.5), (2, 0, 0.9), (3, 0, 0.7)]
    assert order_incoming(0, edges) == [2, 3, 1]  # by descending score
