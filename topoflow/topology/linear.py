# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""LinearWithSkipGenerator (spec §8.2) [AF].

A thin single-round adapter: runs the role set once as a sequential chain,
threading the handoff through each role, applies the net delta and returns
immediately (rounds_run=1). This recovers pure AgensFlow behaviour.
"""
from __future__ import annotations

from ..agents.transport import LLMRequest
from ..types import HandoffState
from .base import CompositeTopologyCell, TopologyResult


def _handoff_summary(handoff: HandoffState) -> str:
    present = [k for k, v in handoff.fields.items() if v is not None]
    return "Known so far: " + (", ".join(present) if present else "(nothing yet)")


class LinearWithSkipGenerator(CompositeTopologyCell):
    def run(self, ctx, handoff, agents, action, transport, embedder, cfg, budget_remaining=None) -> TopologyResult:
        merged = HandoffState(fields=dict(handoff.fields))
        total_tokens = 0
        retries = 0
        failed = False
        publics: list[str] = []
        steps = []
        for role in agents:
            req = LLMRequest(
                model=cfg.default_model,
                system=role.skill_card,
                user=f"Task: {ctx.prompt}\n{_handoff_summary(merged)}",
                schema=role.required_fields,
                purpose="agent",
                role=role.name,
                meta={"round_goal": "single linear pass"},
            )
            fields, tokens = transport.complete(req)
            total_tokens += tokens
            if fields.get("_failed"):
                failed = True
                retries += 1
            pub = fields.get("public")
            if pub:
                publics.append(str(pub))
            # thread produced handoff fields forward
            for hf in ("goal", "subproblem", "evidence", "critique", "verification",
                       "draft_answer", "merged_answer"):
                if fields.get(hf) is not None:
                    merged.set(hf, fields[hf])
            steps.append({"role": role.name, "tokens": tokens, "produced": sorted(
                k for k in fields if fields.get(k) is not None and not k.startswith("_"))})

        # if a draft exists but no merged answer, promote it
        if merged.get("draft_answer") is not None and merged.get("merged_answer") is None:
            merged.set("merged_answer", merged.get("draft_answer"))

        return TopologyResult(
            merged_handoff=merged,
            public_messages=publics,
            rounds_run=1,
            total_tokens=total_tokens,
            retries=retries,
            failed=failed,
            subtrace={"mode": "linear", "steps": steps},
        )
