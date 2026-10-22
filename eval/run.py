#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""BigLaw proof runs — CLI.

    python run.py list                         # registered matters + criteria counts
    python run.py validate                     # fixture + answer-key integrity (no backend)
    python run.py selftest                     # score synthetic oracle/degraded/empty runs (no backend)
    python run.py drive [--matter ID ...] --model-dir biglaw-haiku   # live backend: run + score
    python run.py score --matter ID RUN_DIR    # (re)score an existing run dir
    python run.py compare BASELINE RESULTS_DIR [--slack N] [--min-verified R]   # gate A/B check vs v1

Results land in results/<task>/<model-dir>/<timestamp>/ (override --results-dir). drive talks to a
running BigLaw v2 (`biglaw serve`, http://127.0.0.1:3101) whose model is configured by its env.
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from proofrun import mock  # noqa: E402
from proofrun.matter import load_all, load_matter  # noqa: E402
from proofrun.score import score_run, summary_line  # noqa: E402
from proofrun.validate import validate_matter  # noqa: E402


def cmd_list(a) -> int:
    for m in load_all(a.matter):
        k = m.key
        print(f"{m.id}  [{m.entry['practice']}]  docs={len(m.documents)} facts={len(k.get('facts', []))} "
              f"traps={len(k.get('traps', []))} dispositions={len(k.get('dispositions', []))}  "
              f"deliverables={','.join(m.task['deliverables'])}")
    return 0


def cmd_validate(a) -> int:
    bad = 0
    for m in load_all(a.matter):
        errs = validate_matter(m)
        print(f"{'FAIL' if errs else 'ok  '}  {m.id}")
        for e in errs:
            print(f"      - {e}")
        bad += bool(errs)
    return 1 if bad else 0


def selftest(matters, keep: Path | None = None) -> list[str]:
    """Score the three synthetic profiles per matter and check exact expectations."""
    problems = []
    root = keep or Path(tempfile.mkdtemp(prefix="proofrun-selftest-"))
    for m in matters:
        for profile in ("oracle", "degraded", "empty"):
            rd = mock.build(m, profile, root / m.id / f"mock-{profile}" / "selftest")
            s = score_run(m, rd)
            a, g = s["accuracy"], s["grounding"]
            print(f"  [{profile:8}] {summary_line(s)}")
            if profile == "oracle":
                if not s["all_pass"]:
                    fails = [r["id"] for r in s["criteria_results"] if r["verdict"] != "pass"]
                    problems.append(f"{m.id} oracle: failing criteria {fails}")
                if a["facts_grounded"] != a["facts_total"]:
                    problems.append(f"{m.id} oracle: grounded {a['facts_grounded']}/{a['facts_total']}")
                if g["citations"]["verified"] != g["citations_total"]:
                    problems.append(f"{m.id} oracle: citations {g['citations']}")
            elif profile == "degraded":
                exp = mock.expected_degraded(m)
                got = {
                    "facts_detected": a["facts_detected"], "facts_grounded": a["facts_grounded"],
                    "traps_passed": a["traps_passed"], "dispositions_correct": a["dispositions_correct"],
                    "citations_verified": g["citations"]["verified"],
                    "citations_misattributed": g["citations"]["misattributed"],
                    "citations_unverified": g["citations"]["unverified"],
                }
                for k2, v in exp.items():
                    if got[k2] != v:
                        problems.append(f"{m.id} degraded: {k2} expected {v}, got {got[k2]}")
            else:
                if a["facts_detected"] or a["traps_passed"] != a["traps_total"] or a["dispositions_correct"]:
                    problems.append(f"{m.id} empty: scorer credited an empty deliverable ({a})")
    return problems


def cmd_selftest(a) -> int:
    matters = load_all(a.matter)
    keep = Path(a.keep).resolve() if a.keep else None
    problems = selftest(matters, keep)
    for p in problems:
        print(f"FAIL {p}")
    print("selftest:", "FAIL" if problems else "ok")
    return 1 if problems else 0


def cmd_score(a) -> int:
    m = load_matter(a.matter[0])
    s = score_run(m, Path(a.run_dir).resolve())
    print(summary_line(s))
    if a.json:
        print(json.dumps(s, indent=2))
    return 0


def cmd_drive(a) -> int:
    from proofrun.drive import drive
    results = drive(load_all(a.matter), a)
    print("\n=== summary ===")
    for s in results:
        print(f"error: {s['task']}: {s['error']}" if "error" in s else summary_line(s))
    return 1 if any("error" in s for s in results) else 0


def latest_scores(results: Path, matter) -> dict | None:
    """The newest scores.json of a matter under results (any model dir), or None."""
    runs = sorted((results / matter.id).glob("*/*/scores.json"), key=lambda p: p.parent.name)
    return json.loads(runs[-1].read_text(encoding="utf-8")) if runs else None


def compare(baseline: dict, results: Path, slack: int, min_verified: float) -> tuple[list[str], list[str]]:
    """Per-matter deltas against a v1 baseline ({"matters": {id or slug: {n_passed, citation_verified_rate,
    cost_usd}}}); a matter fails when v2 passes fewer than v1's n_passed − slack criteria, verifies under
    min_verified of its citations, or has no scored run; a baseline naming no registered matter fails.
    Returns (lines, failures)."""
    lines, bad = [], []
    if not any((baseline.get("matters") or {}).get(k) is not None for m in load_all() for k in (m.id, m.entry.get("slug"))):
        bad.append("the baseline names no registered matter: nothing was compared")
    for m in load_all():
        base = baseline.get("matters", {}).get(m.id) or baseline.get("matters", {}).get(m.entry.get("slug"))
        if base is None:
            continue
        s = latest_scores(results, m)
        if s is None:
            bad.append(f"{m.id}: no scored run under {results}")
            continue
        rate = (s.get("grounding") or {}).get("citation_verified_rate")
        delta = s["n_passed"] - base["n_passed"]
        lines.append(f"{m.id}: n_passed {s['n_passed']} vs v1 {base['n_passed']} ({delta:+d}); citations verified {rate}; "
                     f"cost {(s.get('run_status') or {}).get('cost_usd')} vs v1 {base.get('cost_usd')}")
        if delta < -slack:
            bad.append(f"{m.id}: {s['n_passed']} criteria passed, below v1's {base['n_passed']} - {slack}")
        if rate is None or rate < min_verified:
            bad.append(f"{m.id}: citation_verified_rate {rate} below {min_verified}")
    return lines, bad


def cmd_compare(a) -> int:
    baseline = json.loads(Path(a.baseline).read_text(encoding="utf-8"))
    lines, bad = compare(baseline, Path(a.results).resolve(), a.slack, a.min_verified)
    for line in lines + [f"BELOW GATE {b}" for b in bad]:
        print(line)
    print("compare:", "FAIL" if bad else "ok")
    return 1 if bad else 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="BigLaw multi-practice proof runs")
    sub = p.add_subparsers(dest="cmd", required=True)

    def matter_arg(sp, required=False):
        sp.add_argument("--matter", action="append", required=required,
                        help="matter id or slug from matters.json (repeatable; default all)")

    sp = sub.add_parser("list"); matter_arg(sp); sp.set_defaults(fn=cmd_list)
    sp = sub.add_parser("validate"); matter_arg(sp); sp.set_defaults(fn=cmd_validate)
    sp = sub.add_parser("selftest"); matter_arg(sp)
    sp.add_argument("--keep", help="write the synthetic run dirs here instead of a temp dir")
    sp.set_defaults(fn=cmd_selftest)
    sp = sub.add_parser("score"); matter_arg(sp, required=True)
    sp.add_argument("run_dir"); sp.add_argument("--json", action="store_true"); sp.set_defaults(fn=cmd_score)

    sp = sub.add_parser("compare")
    sp.add_argument("baseline", help="v1 baseline JSON (eval/baselines/v1.json)")
    sp.add_argument("results", help="results directory written by drive")
    sp.add_argument("--slack", type=int, default=0, help="criteria v2 may trail v1 by per matter (gate A: 0 for model C, 2 for qwen)")
    sp.add_argument("--min-verified", type=float, default=0.94, help="minimum citation_verified_rate (gate A: 0.94 C, 0.85 qwen)")
    sp.set_defaults(fn=cmd_compare)

    for name, fn in (("drive", cmd_drive),):
        sp = sub.add_parser(name); matter_arg(sp)
        sp.add_argument("--api", default="http://127.0.0.1:3101", help="BigLaw v2 API (serve's BIGLAW_ADDR)")
        sp.add_argument("--model-dir", default="biglaw", help="run-id model segment, e.g. v2-haiku / v2-qwen14b")
        sp.add_argument("--tool-bandit", choices=["on", "off"], default="on", help="the tasks' toolBandit option (gate B runs both)")
        sp.add_argument("--max-steps", type=int, help="the tasks' maxSteps (default: BigLaw's 400)")
        sp.add_argument("--results-dir", default=str(HERE / "results"))
        sp.add_argument("--workflow", choices=["memo", "diligence", "contract_review"], help="override the workflow (default memo)")
        sp.add_argument("--jurisdiction", help="override the matter's jurisdiction tag")
        sp.add_argument("--split-mode", choices=["markers", "per-task", "duplicate"])
        sp.add_argument("--gate-policy", choices=["approve", "reject"], default="approve")
        sp.add_argument("--poll", type=float, default=10.0)
        sp.add_argument("--timeout", type=float, default=4 * 3600.0, help="per-task timeout seconds")
        sp.set_defaults(fn=fn)
    a = p.parse_args(argv)
    return a.fn(a)


if __name__ == "__main__":
    sys.exit(main())
