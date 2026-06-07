# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""CompositeTopologyCell interface (spec §8.1) — the central integration contract."""
from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field

from ..types import HandoffState


@dataclass
class TopologyResult:
    merged_handoff: HandoffState  # single delta back to the macro loop
    public_messages: list[str] = field(default_factory=list)  # for evaluator / global state
    rounds_run: int = 0
    total_tokens: int = 0
    retries: int = 0
    failed: bool = False
    subtrace: dict = field(default_factory=dict)  # nested per-round record (§11)


class CompositeTopologyCell(ABC):
    """Owns an internal multi-round loop, returns ONE merged handoff to the macro
    router. To the policy graph this whole cell looks like a single action with
    one reward."""

    @abstractmethod
    def run(self, ctx, handoff, agents, action, transport, embedder, cfg, budget_remaining=None) -> TopologyResult:
        ...
