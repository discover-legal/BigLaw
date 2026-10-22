# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Drive proof-run matters through a live BigLaw backend, then score them.

Reuses benchmarks/harvey-lab/labdriver unchanged in behaviour: the proof-runs
directory is laid out like a harvey-labs checkout (tasks/<id>/task.json +
documents/), so ``run_task`` converts, ingests, submits, auto-resolves gates,
renders deliverables and writes the run dir. This module only supplies
per-matter settings from matters.json and scores the run afterwards.
"""

from __future__ import annotations

import argparse
from pathlib import Path

from .matter import ROOT, Matter, labdriver
from .score import score_run, summary_line


def driver_args(m: Matter, opts: argparse.Namespace) -> argparse.Namespace:
    e = m.entry
    return argparse.Namespace(
        labs_dir=str(ROOT),
        task=m.id,
        api=opts.api,
        workflow=opts.workflow,
        jurisdiction=e.get("jurisdiction", m.task.get("jurisdiction", "")) if opts.jurisdiction is None else opts.jurisdiction,
        split_mode=opts.split_mode or e.get("split_mode", "markers"),
        gate_policy=opts.gate_policy,
        model_dir=opts.model_dir,
        results_dir=str(Path(opts.results_dir).expanduser().resolve()),
        poll=opts.poll,
        timeout=opts.timeout,
        tool_bandit=opts.tool_bandit,
        max_steps=opts.max_steps,
        matter=e.get("slug", m.id),
        list=False,
    )


def drive(matters: list[Matter], opts: argparse.Namespace) -> list[dict]:
    """Run each matter sequentially (one backend owns the vector DB) and score it.
    A failing matter is reported and the loop continues."""
    ld = labdriver()
    out = []
    for m in matters:
        print(f"\n=== {m.id} ===")
        try:
            run_dir = ld.driver.run_task(driver_args(m, opts))
        except (ld.biglaw.BigLawError, SystemExit) as e:  # run_task sys.exits on a bad task path
            print(f"error: {m.id}: {e}")
            rd = getattr(e, "run_dir", None)
            if rd is not None:
                # Still score a failed task: its findings carry grounding data
                # (a grounding-collapse failure is a measured outcome, not noise).
                s = score_run(m, rd)
                print(summary_line(s))
                print(f"scores: {rd / 'scores.json'}")
            out.append({"task": m.id, "error": str(e), "run_dir": str(rd) if rd else None})
            continue
        s = score_run(m, run_dir)
        print(summary_line(s))
        print(f"scores: {run_dir / 'scores.json'}")
        out.append(s)
    return out
