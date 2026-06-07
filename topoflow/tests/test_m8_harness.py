# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M8 — Full harness (smoke). All 7 arms runnable on a small subset with
MockTransport; metrics H1–H5 computed and written."""
import json

from topoflow.config import Config
from topoflow.eval.harness import run_suite


def test_all_seven_arms_run_and_metrics_emitted(tmp_path):
    out = str(tmp_path / "report.json")
    report = run_suite(Config(), epochs=2, out_path=out)

    # all 7 arms ran
    assert len(report["arms"]) == 7
    for name in (
        "1_fixed_linear", "2_pure_dytopo", "3_pure_agensflow", "4_topoflow_linear",
        "5_topoflow_dytopo", "6_topoflow_free_cold", "7_topoflow_free_warm",
    ):
        assert name in report["arms"]

    # all five metrics present
    m = report["metrics"]
    for key in ("H1_selection", "H2_frontier", "H3_learned_vs_swept", "H4_cold_start", "H5_reward_fragility"):
        assert key in m

    # H1 selection table is produced (plumbing must emit it)
    assert isinstance(m["H1_selection"], dict)

    # report was written to disk and round-trips
    with open(out) as f:
        disk = json.load(f)
    assert disk["arms"].keys() == report["arms"].keys()


def test_arm4_recovers_arm3_and_arm5_recovers_arm2():
    report = run_suite(Config(), epochs=2)
    arms = report["arms"]
    tol = 1e-6
    # arm4 (TopoFlow/linear, learned) must ≈ recover arm3 (pure AgensFlow)
    assert abs(arms["4_topoflow_linear"]["mean_quality"] - arms["3_pure_agensflow"]["mean_quality"]) <= tol
    # arm5 (TopoFlow/dytopo, learned) ≈ arm2 (pure DyTopo) on quality (mock Q is
    # topology-independent, so they match)
    assert abs(arms["5_topoflow_dytopo"]["mean_quality"] - arms["2_pure_dytopo"]["mean_quality"]) <= tol


def test_h5_audit_reported_separately():
    report = run_suite(Config(), epochs=1)
    h5 = report["metrics"]["H5_reward_fragility"]
    assert h5  # at least one arm reported
    for arm, rec in h5.items():
        assert "live" in rec and "audit" in rec and "delta" in rec and "sign_flip" in rec
