# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""What a finished task measured, read from the API (task, steps, cost) and, where the API shows
nothing, from the suite's own database (judge verdicts, the tool bandit's per-tool call counts).

Every field is the same reading on every run, so two runs compare; a field the run could not read
is null, never a guess.
"""

from __future__ import annotations

import re

# A provider refusal that no retry or later task can fix: a bad or revoked key (401/403), or no
# credit (Anthropic answers 400 invalid_request_error "credit balance is too low"; others 402 or
# insufficient_quota). BigLaw's model errors carry "HTTP <code> (<type>: <message>)".
FATAL = re.compile(r"HTTP 40[123]\b|credit balance|authentication_error|permission_error|billing_error|"
                   r"insufficient_quota|Error code: 40[123]\b", re.I)
TYPE_IN_BIGLAW = re.compile(r"HTTP (\d{3}) \(([A-Za-z_]+)")
TYPE_IN_SDK = re.compile(r"'type': '([a-z_]+_error)'")


def fatal_provider_error(text: str | None) -> str | None:
    """The provider's error type when text reports a fatal refusal (key or credit), else None. Only
    the type and status are returned: never the message, which may quote a request."""
    if not text or not FATAL.search(text):
        return None
    m = TYPE_IN_BIGLAW.search(text)
    if m:
        typ = f"{m.group(2)} (HTTP {m.group(1)})"
    else:
        m2 = TYPE_IN_SDK.search(text)
        code = re.search(r"Error code: (\d{3})", text)
        typ = (m2.group(1) if m2 else "unknown_error") + (f" (HTTP {code.group(1)})" if code else "")
    if re.search(r"credit balance", text, re.I):
        typ += ": credit balance too low"
    return typ


def computed_counts(output: str | None) -> dict | None:
    """The Computed figures block's items per tier (the deliverable's Limitations), read as
    eval/proofruns_test.go reads them: a "- " line under a "Check first" or "Arithmetic over" line."""
    if output is None:
        return None
    counts, tier = {"check_first": 0, "arithmetic": 0}, ""
    for line in output.splitlines():
        if line.startswith("Check first"):
            tier = "check_first"
        elif line.startswith("Arithmetic over"):
            tier = "arithmetic"
        elif line.startswith("## "):
            tier = ""
        elif tier and line.startswith("- "):
            counts[tier] += 1
    return counts


def stage_spend(cost: dict | None) -> dict | None:
    """USD per stage (the tool of each step that spent; "lead" for the lead's turns). A stage with an
    unpriced call is null."""
    if not cost:
        return None
    out: dict[str, float | None] = {}
    for e in cost.get("entries") or []:
        tool = e.get("tool") or "lead"
        usd = e.get("usd")
        if tool in out and out[tool] is None:
            continue
        out[tool] = None if usd is None else round((out.get(tool) or 0.0) + usd, 6)
    return out


def tool_calls_delta(before: dict | None, after: dict | None) -> dict | None:
    """Model calls per tool during one task: the tool bandit's record (tool_stats) after minus before.
    A tool's calls include those of the subagents it ran inside its step."""
    if before is None or after is None:
        return None
    out = {}
    for tool, calls in after.items():
        d = calls - before.get(tool, 0)
        if d:
            out[tool] = int(round(d))
    return out


def failures(steps: list[dict], grading_errors: int | None) -> dict:
    return {
        "steps_failed": sum(1 for s in steps if s.get("status") == "failed"),
        "steps_refused": sum(1 for s in steps if s.get("status") == "refused"),
        "grading_errors": grading_errors,
    }


def criteria(scores: dict | None) -> dict | None:
    """id → verdict of a scores.json (harvey-labs' and eval/proofrun's share this shape)."""
    if not scores or not scores.get("criteria_results"):
        return None
    return {r["id"]: r.get("verdict") for r in scores["criteria_results"]}


def score(scores: dict | None) -> dict:
    if not scores:
        return {"passed": None, "total": None}
    return {"passed": int(scores["n_passed"]), "total": int(scores["n_criteria"])}
