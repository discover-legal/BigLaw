# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Metrics H1–H5 (spec §14). The report must answer all five."""
from __future__ import annotations

from collections import defaultdict
from statistics import mean


def _topology_modes(traj) -> list[str]:
    return [
        a.topo_mode
        for (_s, a) in traj.trace.decision_path()
        if a.kind == "topology" and a.topo_mode
    ]


def h1_selection_table(arm_result) -> dict:
    """Per scenario class, the learned generator distribution (linear vs dytopo).

    Falsifier: if generator choice does NOT separate by regime — coordination-
    heavy C3/C7/C8 preferring dytopo while procedural C1/C6 prefer linear — the
    central claim fails and that negative result is the deliverable.
    """
    counts: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
    for tr in arm_result["trajectories"]:
        sc = tr["scenario_class"] or "?"
        for mode in tr["topology_modes"]:
            counts[sc][mode] += 1
    table = {}
    for sc, modes in counts.items():
        total = sum(modes.values()) or 1
        table[sc] = {m: round(c / total, 3) for m, c in modes.items()}
        table[sc]["_n"] = sum(modes.values())
    return table


def h1_separates(table: dict) -> bool:
    """Heuristic check of the H1 separation claim (used as telemetry, not a hard
    gate): coordination-heavy classes lean dytopo, procedural classes lean linear."""
    coord = ("C3", "C7", "C8")
    proc = ("C1", "C6")

    def lean(sc, mode):
        row = table.get(sc, {})
        return row.get(mode, 0.0) >= 0.5

    coord_dytopo = any(lean(c, "dytopo") for c in coord)
    proc_linear = any(lean(c, "linear") for c in proc)
    return coord_dytopo and proc_linear


def h2_frontier(arm_results: dict) -> dict:
    """Per-class quality vs cost per arm (does free-choice dominate the frontier?)."""
    out: dict = {}
    for arm, res in arm_results.items():
        per_class: dict[str, list] = defaultdict(list)
        for tr in res["trajectories"]:
            per_class[tr["scenario_class"] or "?"].append((tr["quality"], tr["tokens"]))
        out[arm] = {
            sc: {"quality": round(mean(q for q, _ in v), 4), "tokens": round(mean(t for _, t in v), 1)}
            for sc, v in per_class.items()
        }
    return out


def h3_learned_vs_swept(arm_results: dict) -> dict:
    """Arm 5 (learned dytopo) vs arm 2 (fixed/swept dytopo)."""
    a5 = arm_results.get("5_topoflow_dytopo")
    a2 = arm_results.get("2_pure_dytopo")
    if not (a5 and a2):
        return {}
    return {
        "arm5_quality": round(a5["mean_quality"], 4),
        "arm2_quality": round(a2["mean_quality"], 4),
        "arm5_tokens": round(a5["mean_tokens"], 1),
        "arm2_tokens": round(a2["mean_tokens"], 1),
        "learned_ge_swept": a5["mean_quality"] + 1e-9 >= a2["mean_quality"],
    }


def h4_cold_start(arm_results: dict) -> dict:
    """Tokens-to-plateau, arm 6 (cold-start free) vs arm 3 (AgensFlow)."""
    a6 = arm_results.get("6_topoflow_free_cold")
    a3 = arm_results.get("3_pure_agensflow")
    out = {}
    if a6:
        out["arm6_tokens_per_epoch"] = a6.get("tokens_per_epoch", [])
    if a3:
        out["arm3_tokens_per_epoch"] = a3.get("tokens_per_epoch", [])
    return out


def h5_reward_fragility(arm_results: dict) -> dict:
    """Every reported quality under single-judge AND three-judge audit; flag sign
    flips. Populated by the harness audit pass per arm (live_quality vs
    audit_quality)."""
    out = {}
    for arm, res in arm_results.items():
        live = res.get("mean_quality")
        audit = res.get("audit_mean_quality")
        if live is None or audit is None:
            continue
        out[arm] = {
            "live": round(live, 4),
            "audit": round(audit, 4),
            "delta": round(audit - live, 4),
            "sign_flip": (live - 0.5) * (audit - 0.5) < 0,
        }
    return out


def compute_all(arm_results: dict) -> dict:
    free = arm_results.get("6_topoflow_free_cold") or next(iter(arm_results.values()))
    h1 = h1_selection_table(free)
    return {
        "H1_selection": h1,
        "H1_separates": h1_separates(h1),
        "H2_frontier": h2_frontier(arm_results),
        "H3_learned_vs_swept": h3_learned_vs_swept(arm_results),
        "H4_cold_start": h4_cold_start(arm_results),
        "H5_reward_fragility": h5_reward_fragility(arm_results),
    }
