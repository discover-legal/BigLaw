#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""The bench smoke's stand-in for harvey-labs' run_eval: the same arguments (--run-id, --task,
--judges), LAB_ROOT the same way, and a scores.json of the same shape. A criterion passes when its
`stub_contains` phrase appears in the deliverables (case-insensitive). It calls no model and is used
only by smoke.json; its scores measure the pipeline, never BigLaw."""

from __future__ import annotations

import argparse
import json
import os
from datetime import datetime, timezone
from pathlib import Path


def main(argv=None) -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--run-id", required=True)
    p.add_argument("--task", required=True)
    p.add_argument("--judges", nargs="+", required=True)
    a = p.parse_args(argv)
    root = Path(os.environ["LAB_ROOT"])
    task = json.loads((root / "tasks" / a.task / "task.json").read_text(encoding="utf-8"))
    run_dir = root / "results" / a.run_id
    text = "\n".join(f.read_text(encoding="utf-8", errors="replace") for f in sorted((run_dir / "output").glob("*")) if f.is_file()).lower()
    results = [{"id": c["id"], "title": c["title"], "verdict": "pass" if c["stub_contains"].lower() in text else "fail",
                "reasoning": f"stub: {'found' if c['stub_contains'].lower() in text else 'did not find'} {c['stub_contains']!r}"}
               for c in task["criteria"]]
    n = sum(r["verdict"] == "pass" for r in results)
    scores = {"score": 1.0 if n == len(results) else 0.0, "max_score": 1.0, "summary": f"{n}/{len(results)} criteria passed (stub judge).",
              "all_pass": n == len(results), "n_criteria": len(results), "n_passed": n, "n_grading_errors": 0,
              "criteria_results": results, "run_id": a.run_id, "task": a.task, "judge_model": a.judges[0],
              "scored_at": datetime.now(timezone.utc).isoformat()}
    (run_dir / "scores.json").write_text(json.dumps(scores, indent=2), encoding="utf-8")
    print(scores["summary"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
