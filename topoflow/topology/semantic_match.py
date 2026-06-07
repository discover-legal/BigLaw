# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Embed + cosine + threshold + top-k + ordering helpers (spec §8.3) [DT].

The encoder is frozen and pluggable. `MockEmbedder` is a deterministic,
dependency-free bag-of-words hashing embedder used by tests (no network).
`SentenceTransformerEmbedder` lazily loads all-MiniLM-L6-v2 for live runs.
"""
from __future__ import annotations

import math
import re
from typing import Protocol, runtime_checkable


@runtime_checkable
class Embedder(Protocol):
    def embed(self, texts: list[str]) -> list[list[float]]:
        ...


_WORD = re.compile(r"[a-z0-9]+")


class MockEmbedder:
    """Deterministic bag-of-words hashing embedder. Cosine ~ shared-token overlap,
    which is enough to drive golden-file edge tests with hand-built descriptors."""

    def __init__(self, dim: int = 128):
        self.dim = dim

    def embed(self, texts: list[str]) -> list[list[float]]:
        return [self._one(t) for t in texts]

    def _one(self, text: str) -> list[float]:
        v = [0.0] * self.dim
        for tok in _WORD.findall((text or "").lower()):
            v[hash(tok) % self.dim] += 1.0
        return v


def _lazy_sentence_transformer(model_name: str):
    from sentence_transformers import SentenceTransformer  # lazy, optional

    return SentenceTransformer(model_name)


class SentenceTransformerEmbedder:
    """Live encoder (frozen). Loaded once and cached. Requires sentence-transformers."""

    def __init__(self, model_name: str = "sentence-transformers/all-MiniLM-L6-v2"):
        self._model = _lazy_sentence_transformer(model_name)

    def embed(self, texts: list[str]) -> list[list[float]]:
        vecs = self._model.encode(list(texts), normalize_embeddings=False)
        return [list(map(float, v)) for v in vecs]


# ── vector ops ──────────────────────────────────────────────────────────────
def normalize(v: list[float]) -> list[float]:
    n = math.sqrt(sum(x * x for x in v))
    if n == 0:
        return list(v)
    return [x / n for x in v]


def dot(a: list[float], b: list[float]) -> float:
    return sum(x * y for x, y in zip(a, b))


def cosine(a: list[float], b: list[float]) -> float:
    return dot(normalize(a), normalize(b))


# ── topology induction (eq 4-7) ─────────────────────────────────────────────
def relevance_matrix(q_vecs: list[list[float]], k_vecs: list[list[float]]) -> list[list[float]]:
    """R[i][j] = cos(Q_i, K_j). Edge j->i (j provides to i) keys on R[i][j]."""
    Q = [normalize(v) for v in q_vecs]
    K = [normalize(v) for v in k_vecs]
    n = len(Q)
    return [[dot(Q[i], K[j]) for j in range(n)] for i in range(n)]


def build_edges(R: list[list[float]], tau: float, k_in: int) -> list[tuple[int, int, float]]:
    """Return directed edges (j, i, score) meaning j provides to i.

    A[j->i] = 1 if R[i][j] > tau and i != j; then enforce max in-degree k_in by
    keeping the top-k_in providers per recipient i by R[i][j].
    """
    n = len(R)
    edges: list[tuple[int, int, float]] = []
    for i in range(n):
        providers = [(j, R[i][j]) for j in range(n) if j != i and R[i][j] > tau]
        # top-k_in by score, deterministic tie-break by provider index
        providers.sort(key=lambda p: (-p[1], p[0]))
        for j, s in providers[:k_in]:
            edges.append((j, i, s))
    return edges


# ── deterministic ordering (eq 8-11) ────────────────────────────────────────
def _in_adj(n: int, edges: list[tuple[int, int, float]]) -> dict[int, list[int]]:
    incoming: dict[int, list[int]] = {i: [] for i in range(n)}
    for (j, i, _s) in edges:
        incoming[i].append(j)
    return incoming


def topo_or_cyclebreak(n: int, edges: list[tuple[int, int, float]]) -> list[int]:
    """Topological sort if acyclic; greedy cycle-break otherwise. Returns a full
    permutation of [0..n-1]."""
    succ: dict[int, set[int]] = {i: set() for i in range(n)}
    indeg = [0] * n
    for (j, i, _s) in edges:
        if i not in succ[j]:
            succ[j].add(i)
            indeg[i] += 1

    order: list[int] = []
    remaining = set(range(n))
    cur_indeg = list(indeg)
    while remaining:
        # nodes with zero restricted in-degree, smallest index first (deterministic)
        ready = sorted(x for x in remaining if cur_indeg[x] == 0)
        if not ready:
            # cycle: greedily place the min restricted in-degree node
            pick = min(remaining, key=lambda x: (cur_indeg[x], x))
            ready = [pick]
        for node in ready:
            order.append(node)
            remaining.discard(node)
            for s in succ[node]:
                if s in remaining and cur_indeg[s] > 0:
                    cur_indeg[s] -= 1
    return order


def make_embedder(model_name: str = "sentence-transformers/all-MiniLM-L6-v2", live: bool = False) -> Embedder:
    """Factory: MockEmbedder for offline/tests, SentenceTransformerEmbedder for live."""
    if live:
        return SentenceTransformerEmbedder(model_name)
    return MockEmbedder()


def order_incoming(i: int, edges: list[tuple[int, int, float]]) -> list[int]:
    """Order recipient i's incoming providers by descending R score, with a
    deterministic tie-break on provider index."""
    ins = [(j, s) for (j, r, s) in edges if r == i]
    ins.sort(key=lambda p: (-p[1], p[0]))
    return [j for j, _s in ins]
