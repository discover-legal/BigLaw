# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M1 — Types & signature. fold() is deterministic and hashable."""
import random

from topoflow.config import Config
from topoflow.signature import bucket, fold
from topoflow.types import BeliefVector, HandoffState, Regime, TaskContext


def _ctx():
    return TaskContext(task_id="t1", prompt="p", domain="code")


def test_bucket_clamps_and_bins():
    assert bucket(0.0, 4) == 0
    assert bucket(1.0, 4) == 3  # clamped, not 4
    assert bucket(0.24, 4) == 0
    assert bucket(0.25, 4) == 1
    assert bucket(0.5, 4) == 2
    # coarser bins => fewer distinct buckets
    assert bucket(0.4, 2) == 0 and bucket(0.6, 2) == 1


def test_fold_deterministic_and_hashable():
    ctx = _ctx()
    h = HandoffState(fields={"goal": "g", "draft_answer": "d"})
    b = BeliefVector(correctness=0.7, uncertainty=0.2, contradiction=0.0, evidence=0.6)
    cfg = Config()
    s1 = fold(ctx, h, b, cfg)
    s2 = fold(ctx, h, b, cfg)
    assert s1 == s2  # deterministic
    assert hash(s1) == hash(s2)  # hashable
    # usable as a dict key
    d = {s1: 1}
    assert d[s2] == 1


def test_equal_observations_equal_signature():
    cfg = Config()
    ctx = _ctx()
    for _ in range(200):
        b = BeliefVector(
            correctness=random.random(),
            uncertainty=random.random(),
            contradiction=random.random(),
            evidence=random.random(),
        )
        fields = {f: "x" for f in ("goal", "evidence") if random.random() > 0.5}
        h = HandoffState(fields=dict(fields))
        h2 = HandoffState(fields=dict(fields))
        assert fold(ctx, h, b, cfg) == fold(ctx, h2, b, cfg)


def test_belief_bins_changes_bucket_count():
    ctx = _ctx()
    h = HandoffState()
    b = BeliefVector(correctness=0.55, uncertainty=0.55, contradiction=0.55, evidence=0.55)
    s_coarse = fold(ctx, h, b, Config(belief_bins=2))
    s_fine = fold(ctx, h, b, Config(belief_bins=10))
    assert s_coarse.correctness_b == 1  # floor(0.55*2)=1
    assert s_fine.correctness_b == 5  # floor(0.55*10)=5


def test_handoff_mask_order():
    h = HandoffState(fields={"goal": "g", "merged_answer": "m"})
    # goal is index 0, merged_answer index 6
    assert h.mask() == (1, 0, 0, 0, 0, 0, 1)


def test_regime_contradiction_dominates():
    ctx = _ctx()
    b = BeliefVector(contradiction=0.8)
    assert fold(ctx, HandoffState(), b, Config()).regime == Regime.CONTRADICTORY
