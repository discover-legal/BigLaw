# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""TopoFlow — a two-level coordination substrate for LLM multi-agent systems.

A slow cross-trajectory contextual bandit (AgensFlow) selects skills, model
bindings, skips, and which topology generator to run; a fast within-trajectory
generator (DyTopo or linear-with-skip) produces the actual coordination graph.
"""
from .config import Config
from .types import (
    Action,
    BeliefVector,
    CellOutput,
    HandoffState,
    Regime,
    Signature,
    TaskContext,
)

__all__ = [
    "Config",
    "Action",
    "BeliefVector",
    "CellOutput",
    "HandoffState",
    "Regime",
    "Signature",
    "TaskContext",
]
