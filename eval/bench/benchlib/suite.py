# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""The suite loop: each task of the config in order, against one BigLaw API, under the spend cap,
scored, measured, and written as one summary.json that validates against schema/summary.schema.json.

A task starts only when what is already charged plus its reserve (BigLaw's per-task budget, which its
spend cannot pass, plus its judge reserve) fits under the cap; otherwise the suite stops there. After
the run the task is charged BigLaw's metered cost (its budget when unknown); a LAB task's judge then
runs only when its estimated cost (judge.estimate, from the deliverable's size, the criteria and the
pinned price: harvey-labs reports no judge usage) still fits, and is charged that estimate. A key or
credit refusal from the provider, in BigLaw or in the judge, cancels the running task and stops the
suite at once; a task that did not end (a timeout) is cancelled before the next one starts.
"""

from __future__ import annotations

import argparse
import json
import platform
import socket
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from . import collect
from .config import load_schema, task_reserve
from .jsonschema import validate
from .judge import JudgeError, estimate, run_judge

EVAL = Path(__file__).resolve().parents[2]
if str(EVAL) not in sys.path:
    sys.path.insert(0, str(EVAL))

from proofrun.drive import driver_args  # noqa: E402
from proofrun.matter import labdriver, load_matter  # noqa: E402
from proofrun.score import score_run  # noqa: E402

SCHEMA_VERSION = 1


class Cap:
    """The suite's spend cap: what is charged so far, and whether a reserve still fits."""

    def __init__(self, cap_usd: float):
        self.cap, self.charged = float(cap_usd), 0.0

    def admits(self, reserve: float) -> bool:
        return self.charged + reserve <= self.cap + 1e-9

    def charge(self, usd: float) -> None:
        self.charged = round(self.charged + usd, 6)


# The task the suite is running, so a signal or a failure can cancel it before serve stops (run.py).
ACTIVE: dict = {"api": None, "task": None}
TERMINAL = ("complete", "failed", "cancelled")


def cancel_active(log) -> None:
    """Cancel the running task, if any (a stop, a signal, a timeout): it must not go on spending."""
    api, tid = ACTIVE.get("api"), ACTIVE.get("task")
    ACTIVE["task"] = None
    if not api or not tid:
        return
    client = labdriver().biglaw.BigLawClient(api, timeout=10)
    try:
        if client.get_task(tid).get("status") not in TERMINAL:
            client._req("POST", f"/tasks/{tid}/cancel")
            log(f"cancelled task {tid}")
    except labdriver().biglaw.BigLawError as e:
        log(f"could not cancel task {tid}: {e}")


def settle(client, task_id: str, log, wait: float = 120.0) -> None:
    """Cancel a task that has not ended and wait for it to end, so the next task starts alone."""
    deadline = time.monotonic() + wait
    try:
        if client.get_task(task_id).get("status") in TERMINAL:
            return
        client._req("POST", f"/tasks/{task_id}/cancel")
        log(f"task {task_id} had not ended: cancelled")
        while time.monotonic() < deadline:
            if client.get_task(task_id).get("status") in TERMINAL:
                return
            time.sleep(1)
        log(f"task {task_id} did not end within {wait:.0f} s of its cancel")
    except labdriver().biglaw.BigLawError as e:
        log(f"could not settle task {task_id}: {e}")


def guarded_client(api: str, log):
    """A BigLawClient that, on every poll of a task, reads the task's error and the notes of its new
    failed or refused steps (BigLaw's own error text, never what a model wrote) and raises ProviderStop
    (after cancelling the task) on a key or credit refusal."""
    ld = labdriver()

    class ProviderStop(ld.biglaw.BigLawError):
        def __init__(self, provider_error: str):
            super().__init__(f"the model provider refused the run ({provider_error}); the suite stops")
            self.provider_error = provider_error

    class Guarded(ld.biglaw.BigLawClient):
        def __init__(self, url):
            super().__init__(url)
            self.cursor: dict[str, int] = {}
            self.stopped: str | None = None
            self.last_task: str | None = None

        def submit_task(self, *a, **k):
            t = super().submit_task(*a, **k)
            self.last_task = ACTIVE["task"] = t["id"]
            return t

        def get_task(self, task_id):
            t = super().get_task(task_id)
            if self.stopped:
                return t
            fatal = collect.fatal_provider_error(t.get("error"))
            after = self.cursor.get(task_id, 0)
            for s in self._req("GET", f"/tasks/{task_id}/steps?after={after}") or []:
                if s.get("status") == "started":
                    break  # read again once it settles
                if s.get("status") in ("failed", "refused"):  # its summary is then the step's error note
                    fatal = fatal or collect.fatal_provider_error(s.get("summary"))
                after = s["step"]
            self.cursor[task_id] = after
            if fatal:
                self.stopped = fatal
                log(f"provider refusal ({fatal}): cancelling task {task_id}")
                try:
                    self._req("POST", f"/tasks/{task_id}/cancel")
                except ld.biglaw.BigLawError:
                    pass  # it may have ended already
                raise ProviderStop(fatal)
            return t

    return Guarded(api), ProviderStop


def all_steps(client, task_id: str) -> list[dict]:
    out, after = [], 0
    while True:
        page = client._req("GET", f"/tasks/{task_id}/steps?after={after}") or []
        out += page
        if len(page) < 1000:
            return out
        after = page[-1]["step"]


def empty_task(t: dict, status: str, error: str | None = None) -> dict:
    return {"id": t["id"], "kind": t["kind"], "task": t["task"], "status": status,
            "score": {"passed": None, "total": None}, "criteria": None, "flips": None,
            "cost_usd": None, "calls": None, "stop_reason": None, "duration_s": None, "stage_spend": None, "stage_calls": None,
            "reader": None, "judge": None, "failures": None, "conflicts": None, "computed_figures": None,
            "finding_gates": None, "judge_cost": None, "models_served": None, "charged_usd": None, "run_dir": None,
            "error": error, "notes": []}


def measure(rec: dict, client, task_id: str | None, run_dir: Path | None, db, tools_before) -> None:
    """Fill rec from the task's record, steps, cost and the suite's database."""
    if run_dir is not None and (run_dir / "metrics.json").is_file():
        rec["duration_s"] = json.loads((run_dir / "metrics.json").read_text(encoding="utf-8")).get("duration_seconds")
    if not task_id:
        return
    v = client.get_task(task_id)
    run = v.get("run") or {}
    rec["stop_reason"] = run.get("stopReason")
    cost = client.task_cost(task_id)
    summ = cost.get("summary") or {}
    rec["cost_usd"] = None if summ.get("usd") is None else round(summ["usd"], 6)
    rec["calls"] = summ.get("calls")
    rec["stage_spend"] = collect.stage_spend(cost)
    rec["models_served"] = sorted({e["model"] for e in cost.get("entries") or [] if e.get("model")})
    rd = cost.get("reader") or {}
    rec["reader"] = {"calls": rd.get("calls", 0), "failed": rd.get("failed", 0), "unfunded": rd.get("unfunded", 0)}
    steps = all_steps(client, task_id)
    rec["failures"] = collect.failures(steps, rec["failures"]["grading_errors"] if rec["failures"] else None)
    rec["computed_figures"] = collect.computed_counts(v.get("output"))
    rec["finding_gates"] = sum(1 for g in v.get("decidedGates") or [] if g.get("kind") == "finding")
    verdicts = db.judge_verdicts(task_id) if db else None
    calls = collect.tool_calls_delta(tools_before, db.tool_calls() if db else None)
    rec["stage_calls"] = calls
    if verdicts is None or calls is None:
        rec["notes"].append("database readings unavailable (no psql, or the query failed): conflicts and judge calls are null")
    rec["conflicts"] = None if verdicts is None else verdicts.get("yes", 0)
    rec["judge"] = {"calls": None if calls is None else calls.get("judge_figures", 0),
                    "pairs": None if verdicts is None else sum(verdicts.values())}


def run_task(cfg: dict, t: dict, cap: Cap, api: str, lab_root: Path, out: Path, model_dir: str, db, harness: Path, key: str,
             redact, log) -> tuple[dict, str | None]:
    """One task end to end: drive it, settle it, measure it, charge BigLaw's spend, then score it (a
    LAB task's judge only when its estimated cost still fits the cap). Returns the record and, when the
    provider refused, the error type."""
    ld = labdriver()
    d = cfg["driver"]
    client, ProviderStop = guarded_client(api, log)
    ACTIVE["api"] = api
    rec = empty_task(t, "scored")
    tools_before = db.tool_calls() if db else None
    run_dir, task_id, stop = None, None, None
    common = dict(api=api, workflow=d["workflow"], tool_bandit=d["tool_bandit"], max_steps=d["max_steps"], gate_policy=d["gate_policy"],
                  model_dir=model_dir, poll=d["poll_seconds"], timeout=d["timeout_seconds"], jurisdiction=t["jurisdiction"], split_mode=t["split_mode"])
    if t["kind"] == "lab":
        args = argparse.Namespace(labs_dir=str(lab_root), task=t["task"], matter=t["matter"] or "", results_dir=None, list=False, **common)
    else:
        matter = load_matter(t["task"])
        args = driver_args(matter, argparse.Namespace(results_dir=str(out / "proofrun"), **common))
    try:
        run_dir = ld.driver.run_task(args, client)
    except ProviderStop as e:
        stop, rec["status"], rec["error"] = e.provider_error, "provider_error", str(e)
        run_dir = getattr(e, "run_dir", None)
    except ld.biglaw.BigLawError as e:
        rec["status"], rec["error"] = "failed", str(e)
        run_dir = getattr(e, "run_dir", None)
    task_id = getattr(client, "last_task", None)
    if run_dir is not None:
        recs = json.loads((run_dir / "biglaw_tasks.json").read_text(encoding="utf-8")) if (run_dir / "biglaw_tasks.json").is_file() else []
        task_id = recs[-1]["id"] if recs else task_id
        rec["run_dir"] = str(run_dir.relative_to(out)) if run_dir.is_relative_to(out) else str(run_dir)
    base = ld.biglaw.BigLawClient(api)
    if task_id:
        settle(base, task_id, log)  # a timeout or a poll error leaves it running: never two tasks at once
    ACTIVE["task"] = None
    try:
        measure(rec, base, task_id, run_dir, db, tools_before)
    except ld.biglaw.BigLawError as e:
        rec["notes"].append(f"measurement incomplete: {e}")
    spent = rec["cost_usd"]
    if spent is None:
        spent = float(cfg["budget"]["usd"])
        rec["notes"].append("BigLaw's cost unknown: charged at the task's budget")
    cap.charge(spent)
    charged = spent
    scores = None
    if t["kind"] == "proofrun" and stop is None and run_dir is not None:
        scores = score_run(matter, run_dir)  # a failed task is scored too: its grounding is a measurement
    elif t["kind"] == "lab" and stop is None and run_dir is not None and rec["status"] == "scored":
        task_cfg = json.loads((lab_root / "tasks" / t["task"] / "task.json").read_text(encoding="utf-8"))
        est = estimate(cfg, task_cfg, run_dir / "output", harness)
        rec["judge_cost"] = est
        if not cap.admits(est["usd"]):
            rec["status"] = "judge_skipped_cap"
            rec["error"] = f"the judge's estimated ${est['usd']:.2f} does not fit: ${cap.charged:.2f} of the ${cap.cap:.2f} cap is charged"
            log(f"task {t['id']}: {rec['error']}; not judged")
        else:
            cap.charge(est["usd"])
            charged += est["usd"]
            run_id = str(run_dir.relative_to(lab_root / "results"))
            try:
                scores = run_judge(cfg, harness, lab_root, run_id, t["task"], key, out / "logs" / f"judge-{t['id']}.log", redact)
            except JudgeError as e:
                rec["status"], rec["error"] = ("provider_error" if e.provider_error else "failed"), str(e)
                stop = e.provider_error
    elif t["kind"] == "lab" and rec["status"] == "failed":
        task_cfg = json.loads((lab_root / "tasks" / t["task"] / "task.json").read_text(encoding="utf-8"))
        n = len(task_cfg.get("criteria") or [])
        rec["score"] = {"passed": 0, "total": n}
        rec["notes"].append(f"the task failed before a deliverable: scored 0/{n}, as harvey-labs scores a run with no output (not judged)")
    if scores is not None:
        rec["score"], rec["criteria"] = collect.score(scores), collect.criteria(scores)
        if rec["failures"] is not None:
            rec["failures"]["grading_errors"] = scores.get("n_grading_errors", 0)
    rec["charged_usd"] = round(charged, 6)
    return rec, stop


def run_suite(cfg: dict, *, api: str, lab_root: Path, out: Path, db, harness: Path, key: str, redact, log,
              build: dict, harness_info: dict, database: dict, yardstick: dict, config_hash: str) -> dict:
    cap = Cap(cfg["spend_cap_usd"])
    model_dir = f"bench-{out.name}"
    tasks, stopped = [], None
    for t in cfg["tasks"]:
        if stopped:
            tasks.append(empty_task(t, "not_run", f"not run: the suite stopped ({stopped['reason']})"))
            continue
        reserve = task_reserve(cfg, t)
        if not cap.admits(reserve):
            stopped = {"reason": "spend_cap", "provider_error_type": None,
                       "detail": f"{t['id']} reserves ${reserve:.2f}; ${cap.charged:.2f} of the ${cap.cap:.2f} cap is charged"}
            log(f"spend cap: {stopped['detail']}: stopping before it")
            tasks.append(empty_task(t, "skipped_cap", stopped["detail"]))
            continue
        log(f"task {t['id']} ({t['kind']} {t['task']}): reserving ${reserve:.2f}")
        started = time.monotonic()
        rec, stop = run_task(cfg, t, cap, api, lab_root, out, model_dir, db, harness, key, redact, log)
        if rec["duration_s"] is None:
            rec["duration_s"] = round(time.monotonic() - started, 1)
        tasks.append(rec)
        log(f"task {t['id']}: {rec['status']} {rec['score']['passed']}/{rec['score']['total']}, cost {rec['cost_usd']}, "
            f"calls {rec['calls']}, stop {rec['stop_reason']}; charged ${cap.charged:.2f} of ${cap.cap:.2f}")
        if stop:
            stopped = {"reason": "provider_error", "provider_error_type": stop, "detail": rec["error"]}
        elif rec["status"] == "judge_skipped_cap":
            stopped = {"reason": "spend_cap", "provider_error_type": None, "detail": rec["error"]}
    ran = [x for x in tasks if x["status"] not in ("not_run", "skipped_cap")]
    served = sorted({m for x in ran for m in x["models_served"] or []})
    return {
        "schema_version": SCHEMA_VERSION,
        "kind": "run",
        "mode": cfg["mode"],
        "label": out.name,
        "source": None,
        "build": build,
        "config_hash": config_hash,
        "config": {k: v for k, v in cfg.items() if k != "config_hash"},
        "reconstructed_config": None,
        "reconstructed_config_hash": None,
        "provenance": None,
        "yardstick": yardstick,
        "harness": harness_info,
        "judge": {"models": cfg["judge"]["models"], "runner": cfg["judge"]["runner"]},
        "database": database,
        "models_served": served,
        "runner": {"host": socket.gethostname(), "date": datetime.now(timezone.utc).isoformat(timespec="seconds"),
                   "python": platform.python_version()},
        "fresh_db": database.get("fresh"),
        "spend": {"cap_usd": cap.cap, "charged_usd": cap.charged,
                  "biglaw_usd": None if any(x["cost_usd"] is None for x in ran) else round(sum(x["cost_usd"] for x in ran), 6),
                  "judge_usd_estimated": round(sum((x["judge_cost"] or {}).get("usd", 0) for x in ran
                                                   if x["status"] != "judge_skipped_cap"), 4)},
        "stopped": stopped,
        "tasks": tasks,
    }


def check_summary(summary: dict) -> list[str]:
    return validate(summary, load_schema("summary.schema.json"))


def write_summary(summary: dict, out: Path) -> Path:
    errs = check_summary(summary)
    path = out / "summary.json"
    path.write_text(json.dumps(summary, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    if errs:
        raise ValueError(f"{path} does not validate against schema/summary.schema.json:\n  - " + "\n  - ".join(errs))
    return path
