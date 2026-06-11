#!/usr/bin/env python3
"""Aggregate LAB scores.json results and compare against Harvey's published baselines.

    python compare.py --labs-dir ~/harvey-labs
    python compare.py --results-dir ~/harvey-labs/results --baselines published_baselines.json

Scans results/<task>/<model>/<timestamp>/scores.json (Harvey's eval output),
keeps the latest run per (model, task), and prints all-pass rate, mean rubric
score, and cost per task alongside the published numbers — which were produced
inside Harvey's sandboxed harness, while BigLaw runs its own loop. Same judge,
different agent environments; read accordingly.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def _get(data: dict, *keys, default=None):
    """First present key, checking top level then metadata."""
    meta = data.get("metadata") or {}
    for k in keys:
        if k in data:
            return data[k]
        if k in meta:
            return meta[k]
    return default


def load_run(scores_path: Path, results_root: Path) -> dict | None:
    try:
        data = json.loads(scores_path.read_text(encoding="utf-8"))
    except (json.JSONDecodeError, OSError) as e:
        print(f"warning: skipping {scores_path}: {e}", file=sys.stderr)
        return None

    run_dir = scores_path.parent
    rel = run_dir.relative_to(results_root).parts  # (*task, model, timestamp)
    if len(rel) < 3:
        return None
    model, ts = rel[-2], rel[-1]
    task = _get(data, "task") or "/".join(rel[:-2])

    score = _get(data, "score", "overall_score")
    max_score = _get(data, "max_score")
    all_pass = bool(_get(data, "all_pass", default=False))

    # Cost: prefer the eval's own cost metrics, fall back to the driver's metrics.json.
    cost = _get(data, "cost_usd", "cost", "total_cost_usd")
    if cost is None:
        metrics_path = run_dir / "metrics.json"
        if metrics_path.is_file():
            try:
                m = json.loads(metrics_path.read_text(encoding="utf-8"))
                cost = m.get("cost_usd") or (m.get("biglaw") or {}).get("cost_usd")
            except (json.JSONDecodeError, OSError):
                pass

    return {
        "model": model, "task": task, "ts": ts,
        "score": score, "max_score": max_score,
        "all_pass": all_pass, "cost_usd": cost,
    }


def aggregate(runs: list[dict]) -> dict[str, dict]:
    # Latest run per (model, task) — timestamps sort lexicographically.
    latest: dict[tuple[str, str], dict] = {}
    for r in runs:
        key = (r["model"], r["task"])
        if key not in latest or r["ts"] > latest[key]["ts"]:
            latest[key] = r

    by_model: dict[str, dict] = {}
    for r in latest.values():
        agg = by_model.setdefault(r["model"], {
            "tasks": 0, "all_pass": 0, "norm_scores": [], "costs": [],
        })
        agg["tasks"] += 1
        agg["all_pass"] += 1 if r["all_pass"] else 0
        if isinstance(r["score"], (int, float)) and isinstance(r["max_score"], (int, float)) and r["max_score"]:
            agg["norm_scores"].append(r["score"] / r["max_score"])
        if isinstance(r["cost_usd"], (int, float)):
            agg["costs"].append(r["cost_usd"])
    return by_model


def fmt_pct(x: float | None) -> str:
    return f"{100 * x:.1f}%" if x is not None else "—"


def fmt_usd(x: float | None) -> str:
    return f"${x:,.2f}" if x is not None else "—"


def main(argv: list[str] | None = None) -> None:
    p = argparse.ArgumentParser(description="Compare LAB results against published baselines.")
    p.add_argument("--labs-dir", help="harvey-labs checkout (results assumed at <labs-dir>/results)")
    p.add_argument("--results-dir", help="explicit results root (overrides --labs-dir)")
    p.add_argument("--baselines", default=str(Path(__file__).parent / "published_baselines.json"),
                   help="published baseline numbers (default: published_baselines.json beside this script)")
    args = p.parse_args(argv)

    if args.results_dir:
        results_root = Path(args.results_dir).expanduser().resolve()
    elif args.labs_dir:
        results_root = Path(args.labs_dir).expanduser().resolve() / "results"
    else:
        p.error("provide --labs-dir or --results-dir")
    if not results_root.is_dir():
        sys.exit(f"error: {results_root} not found — run some tasks and score them first")

    runs = [r for sp in sorted(results_root.rglob("scores.json"))
            if (r := load_run(sp, results_root))]
    if not runs:
        sys.exit(f"error: no scores.json found under {results_root} — "
                 "run evaluation.run_eval on your runs first")
    by_model = aggregate(runs)

    header = f"{'Agent':<42} {'Tasks':>6} {'All-pass':>9} {'Avg score':>10} {'$/task':>9}"
    print(header)
    print("─" * len(header))
    for model, agg in sorted(by_model.items()):
        all_pass_rate = agg["all_pass"] / agg["tasks"] if agg["tasks"] else None
        avg_score = sum(agg["norm_scores"]) / len(agg["norm_scores"]) if agg["norm_scores"] else None
        avg_cost = sum(agg["costs"]) / len(agg["costs"]) if agg["costs"] else None
        print(f"{model:<42} {agg['tasks']:>6} {fmt_pct(all_pass_rate):>9} "
              f"{fmt_pct(avg_score):>10} {fmt_usd(avg_cost):>9}")

    baselines_path = Path(args.baselines)
    if baselines_path.is_file():
        pub = json.loads(baselines_path.read_text(encoding="utf-8"))
        print(f"\nPublished baselines ({pub.get('source', 'unknown source')}, "
              f"retrieved {pub.get('retrieved', '?')}, full 1,251-task set):")
        print("─" * len(header))
        for b in pub.get("baselines", []):
            print(f"{b['agent']:<42} {'1251':>6} {fmt_pct(b.get('all_pass_rate')):>9} "
                  f"{'—':>10} {fmt_usd(b.get('cost_per_task_usd')):>9}")
        print("\nNote: published agents ran inside Harvey's sandboxed harness; BigLaw runs "
              "its own multi-agent loop (convergence/halting included). Same judge and "
              "rubrics, different environments — and partial-set all-pass rates are noisy.")


if __name__ == "__main__":
    main()
