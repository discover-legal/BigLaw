# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Dataset loaders (spec §14).

Real loaders (HumanEval, APPS-Competition, MATH-500, Omni-MATH, and the two
AgensFlow corpora) read JSONL from a data directory when present. For offline
milestone tests a small built-in `mock_suite()` mixes verifiable (math) and
judged (incident/advisory, scenario classes C1–C8) tasks.
"""
from __future__ import annotations

import json
import os

from ..types import TaskContext


def mock_suite() -> list[TaskContext]:
    """A tiny mixed suite for offline smoke runs."""
    tasks: list[TaskContext] = []
    # verifiable (math, exact-match)
    tasks.append(TaskContext("math-1", "Compute 2+2.", scenario_class="C1", domain="math", ground_truth="4"))
    tasks.append(TaskContext("math-2", "Compute 6/3 + 2.", scenario_class="C6", domain="math", ground_truth="4"))
    # judged (incident / advisory) across scenario classes
    judged = [
        ("inc-1", "Triage a cascading service outage.", "C3", "incident"),
        ("inc-2", "Procedural restart runbook for node X.", "C1", "incident"),
        ("adv-1", "Advise on conflicting CVE remediations.", "C7", "advisory"),
        ("adv-2", "Cross-team security exception review.", "C8", "advisory"),
        ("adv-3", "Routine dependency bump advisory.", "C6", "advisory"),
        ("inc-3", "Ambiguous multi-cause latency incident.", "C3", "incident"),
    ]
    for tid, prompt, sc, dom in judged:
        tasks.append(TaskContext(tid, prompt, scenario_class=sc, domain=dom))
    return tasks


def _read_jsonl(path: str) -> list[dict]:
    rows = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def load_subset(name: str, n: int = 0, data_dir: str = "data/topoflow") -> list[TaskContext]:
    """Load a real dataset subset if its JSONL exists; else fall back to the mock
    suite filtered/truncated. Real schemas:
      code:  {task_id, prompt, entry_point, test}            -> domain "code"
      math:  {task_id, prompt, answer}                       -> domain "math"
      af:    {task_id, prompt, scenario_class, domain}       -> judged
    """
    path = os.path.join(data_dir, f"{name}.jsonl")
    if not os.path.exists(path):
        suite = mock_suite()
        return suite[:n] if n else suite
    rows = _read_jsonl(path)
    if n:
        rows = rows[:n]
    out: list[TaskContext] = []
    for r in rows:
        if "entry_point" in r and "test" in r:
            gt = {"entry_point": r["entry_point"], "test": r["test"]}
            out.append(TaskContext(r["task_id"], r["prompt"], r.get("scenario_class"), "code", gt))
        elif "answer" in r:
            out.append(TaskContext(r["task_id"], r["prompt"], r.get("scenario_class"), "math", str(r["answer"])))
        else:
            out.append(TaskContext(r["task_id"], r["prompt"], r.get("scenario_class"), r.get("domain", "advisory")))
    return out
