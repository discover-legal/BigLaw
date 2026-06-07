# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Core data model (spec §3).

Plain dataclasses only — no third-party dependency — so the whole coordination
core and its tests run without a network or any heavy package installed.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Optional


class Regime(str, Enum):  # [AF] regime labels
    STRAIGHTFORWARD = "straightforward"
    EVIDENCE_HEAVY = "evidence_heavy"
    AMBIGUOUS = "ambiguous"
    CONTRADICTORY = "contradictory"
    HIGH_RISK = "high_risk"
    EXPLORATORY = "exploratory"


# [AF] 7-bit handoff mask, fixed order:
HANDOFF_FIELDS = (
    "goal",
    "subproblem",
    "evidence",
    "critique",
    "verification",
    "draft_answer",
    "merged_answer",
)


@dataclass(frozen=True)
class Signature:  # [AF] eq (1); MUST be hashable
    regime: Regime
    handoff_mask: tuple[int, ...]  # length 7, values in {0,1}
    correctness_b: int
    uncertainty_b: int
    contradiction_b: int
    evidence_b: int
    # NOTE: handoff_quality belief is tracked at runtime but EXCLUDED here [AF]


@dataclass
class BeliefVector:  # continuous, pre-bucketing [AF]
    correctness: float = 0.0
    uncertainty: float = 1.0
    contradiction: float = 0.0
    evidence: float = 0.0
    handoff_quality: float = 0.0  # inspected, not folded


@dataclass
class HandoffState:  # [AF] structured, typed
    fields: dict[str, Optional[object]] = field(default_factory=dict)

    def mask(self) -> tuple[int, ...]:
        return tuple(int(self.fields.get(f) is not None) for f in HANDOFF_FIELDS)

    def get(self, key: str):
        return self.fields.get(key)

    def set(self, key: str, value) -> None:
        self.fields[key] = value


@dataclass
class TaskContext:  # x_t
    task_id: str
    prompt: str
    scenario_class: Optional[str] = None  # e.g. "C3"; for eval grouping only
    domain: str = ""  # "code" | "math" | "incident" | "advisory"
    ground_truth: Optional[object] = None  # tests / answer key, if verifiable


@dataclass
class Action:  # [AF] + [NEW]
    kind: str  # "invoke" | "skip" | "topology" | "terminate"
    skill: Optional[str] = None  # invoke: skill protocol k
    model: Optional[str] = None  # invoke: model binding m
    target: Optional[str] = None  # skip:X -> X
    topo_mode: Optional[str] = None  # topology: "linear" | "dytopo"   [NEW]
    tau_bucket: Optional[int] = None  # topology: index into TAU_BUCKETS [NEW]
    k_in: Optional[int] = None  # topology: max in-degree           [NEW]
    round_bucket: Optional[int] = None  # topology: index into ROUND_BUCKETS [NEW]

    def key(self) -> tuple:  # canonical, hashable identity
        return (
            self.kind,
            self.skill,
            self.model,
            self.target,
            self.topo_mode,
            self.tau_bucket,
            self.k_in,
            self.round_bucket,
        )


@dataclass
class CellOutput:
    """Result of a single invoke cell (Layer 2 typed I/O)."""

    skill: str
    model: Optional[str] = None
    tokens: int = 0
    retries: int = 0
    failed: bool = False
    complete: bool = False  # evaluator sets this to trigger termination
    # handoff field deltas this cell produced (subset of HANDOFF_FIELDS + extras)
    handoff_delta: dict[str, object] = field(default_factory=dict)
    # parsed verifier verdict / evidence counts etc. for belief updates
    meta: dict[str, object] = field(default_factory=dict)
    text: str = ""
