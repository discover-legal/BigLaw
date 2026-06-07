# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Two-level trace & RunReport (spec §11). JSON-serializable throughout."""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional

from .types import Action, Signature


@dataclass
class Trace:
    task_id: str
    events: list[dict] = field(default_factory=list)
    tokens: int = 0
    retries: int = 0
    any_failure: bool = False
    final_answer: str = ""
    # macro decision path -> list[(Signature, Action)] for backup
    _decisions: list[tuple] = field(default_factory=list)

    def add(self, event: dict) -> None:
        self.events.append(event)

    def record_decision(self, sig: Signature, action: Action) -> None:
        self._decisions.append((sig, action))

    def decision_path(self) -> list[tuple]:
        return list(self._decisions)

    def to_dict(self) -> dict:
        return {
            "task_id": self.task_id,
            "tokens": self.tokens,
            "retries": self.retries,
            "any_failure": self.any_failure,
            "final_answer": self.final_answer,
            "events": self.events,
        }


# ── event builders ──────────────────────────────────────────────────────────
def invoke_event(action: Action, out) -> dict:
    return {
        "type": "invoke",
        "skill": action.skill,
        "model": action.model,
        "tokens": out.tokens,
        "complete": out.complete,
        "failed": out.failed,
        "delta_fields": sorted(out.handoff_delta.keys()),
    }


def skip_event(action: Action) -> dict:
    return {"type": "skip", "target": action.target}


def topology_event(action: Action, res) -> dict:
    return {
        "type": "topology",
        "mode": action.topo_mode,
        "tau_bucket": action.tau_bucket,
        "k_in": action.k_in,
        "round_bucket": action.round_bucket,
        "rounds_run": res.rounds_run,
        "total_tokens": res.total_tokens,
        "retries": res.retries,
        "failed": res.failed,
        "subtrace": res.subtrace,
    }


@dataclass
class RunReport:
    task_id: str
    reward: float
    quality: float
    sub_scores: dict
    tokens: int
    retries: int
    decision_path: list[dict]
    aborted: bool = False
    abort_reason: Optional[str] = None
    trace: Optional[dict] = None

    def to_dict(self) -> dict:
        return {
            "task_id": self.task_id,
            "reward": self.reward,
            "quality": self.quality,
            "sub_scores": self.sub_scores,
            "tokens": self.tokens,
            "retries": self.retries,
            "decision_path": self.decision_path,
            "aborted": self.aborted,
            "abort_reason": self.abort_reason,
            "trace": self.trace,
        }


def build_run_report(ctx_task_id, trace: Trace, reward, quality, sub, aborted=False, reason=None) -> RunReport:
    dp = [
        {"signature": _sig_brief(s), "action": list(a.key())} for (s, a) in trace.decision_path()
    ]
    return RunReport(
        task_id=ctx_task_id,
        reward=reward,
        quality=quality,
        sub_scores=sub,
        tokens=trace.tokens,
        retries=trace.retries,
        decision_path=dp,
        aborted=aborted,
        abort_reason=reason,
        trace=trace.to_dict(),
    )


def _sig_brief(sig: Signature) -> str:
    return f"{sig.regime.value}|m{''.join(map(str, sig.handoff_mask))}"
