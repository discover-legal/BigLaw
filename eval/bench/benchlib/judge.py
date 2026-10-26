# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Scoring a LAB task: Harvey's own eval phase (harvey-labs `run_eval`, the pinned judge model,
single judge), or in smoke mode stubjudge.py, a stand-in with the same command line and output."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

from .collect import fatal_provider_error
from .infra import child_env

BENCH = Path(__file__).resolve().parent.parent


def command(cfg: dict, run_id: str, task: str) -> list[str]:
    j = cfg["judge"]
    if j["runner"] == "stub":
        return [sys.executable, str(BENCH / "stubjudge.py"), "--run-id", run_id, "--task", task, "--judges", *j["models"]]
    return ["uv", "run", "python", "-m", "lab_core.evaluation.run_eval", "--run-id", run_id, "--task", task,
            "--judges", *j["models"], "--parallel", str(j["parallel"])]


class JudgeError(RuntimeError):
    def __init__(self, msg: str, provider_error: str | None = None):
        super().__init__(msg)
        self.provider_error = provider_error


def run_judge(cfg: dict, harness: Path, lab_root: Path, run_id: str, task: str, key: str, log_path: Path, redact) -> dict:
    """Run the judge over lab_root/results/<run_id> and return its scores.json. The judge sees the key
    only as ANTHROPIC_API_KEY, LAB_ROOT is the suite's own (so no harvey-labs .env is read) and
    ANTHROPIC_BASE_URL is never set. A key or credit refusal raises JudgeError with the provider's
    error type."""
    env = child_env({"LAB_ROOT": str(lab_root)})
    if cfg["judge"]["runner"] == "harvey-labs":
        env["ANTHROPIC_API_KEY"] = key
    r = subprocess.run(command(cfg, run_id, task), cwd=harness, env=env, capture_output=True, text=True)
    text = redact((r.stdout or "") + "\n" + (r.stderr or ""))
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_path.write_text(text, encoding="utf-8")
    scores_path = lab_root / "results" / run_id / "scores.json"
    scores = json.loads(scores_path.read_text(encoding="utf-8")) if scores_path.is_file() else None
    reasons = " ".join(str(c.get("reasoning", "")) for c in (scores or {}).get("criteria_results", []) if c.get("verdict") not in ("pass", "fail"))
    fatal = fatal_provider_error(text) or fatal_provider_error(reasons)
    if fatal:
        raise JudgeError(f"the judge was refused by the provider ({fatal})", fatal)
    if r.returncode != 0 or scores is None:
        raise JudgeError(f"the judge exited {r.returncode} without scores.json: see {log_path}")
    return scores
