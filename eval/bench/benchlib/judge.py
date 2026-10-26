# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Scoring a LAB task: Harvey's own eval phase (harvey-labs `run_eval`, the pinned judge model,
single judge), or in smoke mode stubjudge.py, a stand-in with the same command line and output."""

from __future__ import annotations

import json
import math
import os
import signal
import re
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

from .collect import fatal_provider_error
from .infra import child_env

BENCH = Path(__file__).resolve().parent.parent


TEMPLATE = Path("lab_core/evaluation/prompts/rubric_criterion.txt")  # the judge's per-criterion prompt in harvey-labs
TEMPLATE_FALLBACK_CHARS = 2000


def deliverable_text(path: Path) -> str:
    """A deliverable roughly as the judge reads it: a .docx through pandoc when installed (as harvey-labs
    reads it), else its document.xml's text; anything else as UTF-8 text."""
    if path.suffix.lower() == ".docx":
        if shutil.which("pandoc"):
            r = subprocess.run(["pandoc", "-t", "markdown", str(path)], capture_output=True, text=True)
            if r.returncode == 0:
                return r.stdout
        try:
            with zipfile.ZipFile(path) as z:
                return re.sub(r"<[^>]+>", " ", z.read("word/document.xml").decode("utf-8", "replace"))
        except (OSError, KeyError, zipfile.BadZipFile):
            return ""
    return path.read_text(encoding="utf-8", errors="replace")


def estimate(cfg: dict, task_cfg: dict, output_dir: Path, harness: Path) -> dict:
    """What harvey-labs' judge will cost on this run: one call per criterion, each carrying the prompt
    template, the task's instructions, the criterion and the text of its deliverables (all of them when
    it names none), at chars_per_token, plus output_tokens_per_criterion out, at the pinned price. An
    estimate (harvey-labs reports no judge usage), recorded as one; retries are not in it."""
    j = cfg["judge"]
    tpl = harness / TEMPLATE
    overhead = (len(tpl.read_text(encoding="utf-8")) if tpl.is_file() else TEMPLATE_FALLBACK_CHARS) + len(str(task_cfg.get("instructions", "")))
    files = {f.name: len(deliverable_text(f)) for f in sorted(output_dir.glob("*")) if f.is_file()} if output_dir.is_dir() else {}
    total = sum(files.values())
    crits = task_cfg.get("criteria") or []
    chars = 0
    for c in crits:
        named = [files[n] for n in c.get("deliverables") or [] if n in files]
        chars += overhead + len(json.dumps(c)) + (sum(named) if named else total)
    tin = math.ceil(chars / float(j["chars_per_token"]))
    tout = len(crits) * int(j["output_tokens_per_criterion"])
    usd = tin * j["price_per_mtok"]["in"] / 1e6 + tout * j["price_per_mtok"]["out"] / 1e6
    return {"usd": round(usd, 4), "basis": "estimate", "criteria": len(crits), "input_tokens": tin, "output_tokens": tout,
            "deliverable_chars": total}


def command(cfg: dict, run_id: str, task: str) -> list[str]:
    j = cfg["judge"]
    if j["runner"] == "stub":
        return [sys.executable, str(BENCH / "stubjudge.py"), "--run-id", run_id, "--task", task, "--judges", *j["models"]]
    return ["uv", "run", "python", "-m", "lab_core.evaluation.run_eval", "--run-id", run_id, "--task", task,
            "--judges", *j["models"], "--parallel", str(j["parallel"])]


def run_group(cmd: list[str], **kw) -> subprocess.CompletedProcess:
    """Run cmd in its own process group and, if the wait is interrupted (a signal, an error), stop the
    whole group: `uv run` does not pass a kill on to its python child, which would otherwise go on
    judging, and spending, with no one watching (PR 63 r2 MEDIUM-1)."""
    p = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True, **kw)
    try:
        out, err = p.communicate()
    except BaseException:
        kill_group(p)
        raise
    return subprocess.CompletedProcess(cmd, p.returncode, out, err)


def kill_group(p: subprocess.Popen, grace: float = 5.0) -> None:
    for sig in (signal.SIGTERM, signal.SIGKILL):
        try:
            os.killpg(p.pid, sig)
        except ProcessLookupError:
            return
        try:
            p.wait(grace)
            if sig == signal.SIGTERM:
                os.killpg(p.pid, signal.SIGKILL)  # the leader is gone; take any child it left in the group
            return
        except subprocess.TimeoutExpired:
            continue
        except ProcessLookupError:
            return


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
    if key:
        env["ANTHROPIC_API_KEY"] = key  # the stub gets the smoke's dummy secret too, so its log's redaction is tested
    r = run_group(command(cfg, run_id, task), cwd=harness, env=env)
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
