# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""DyTopoGenerator (spec §8.3) [DT] — full per-round semantic graph induction.

Mirrors DyTopo Algorithm 1. Hyperparameters come from the selecting Action:
tau = cfg.tau_buckets[action.tau_bucket], k_in = action.k_in,
max_rounds = cfg.round_buckets[action.round_bucket].

Termination [NEW] replaces DyTopo's heuristic manager halt: stop on max_rounds,
evaluator-complete, or budget exhaustion.
"""
from __future__ import annotations

from ..agents.transport import LLMRequest
from ..types import HandoffState
from .base import CompositeTopologyCell, TopologyResult
from .semantic_match import (
    MockEmbedder,
    build_edges,
    order_incoming,
    relevance_matrix,
    topo_or_cyclebreak,
)

# handoff fields a DyTopo cell may populate from agent outputs
_HANDOFF_KEYS = ("draft_answer", "merged_answer", "verification", "evidence", "critique")


class DyTopoGenerator(CompositeTopologyCell):
    def run(self, ctx, handoff, agents, action, transport, embedder, cfg, budget_remaining=None) -> TopologyResult:
        if embedder is None:
            embedder = MockEmbedder()
        tau = cfg.tau_buckets[action.tau_bucket]
        k_in = action.k_in or cfg.dytopo_k_in
        max_rounds = cfg.round_buckets[action.round_bucket]

        n = len(agents)
        # H_i: per-agent accumulated memory, seeded from the incoming handoff
        present = [k for k, v in handoff.fields.items() if v is not None]
        seed = "Known so far: " + (", ".join(present) if present else "(nothing yet)")
        memories: list[list[str]] = [[seed] for _ in range(n)]

        total_tokens = 0
        retries = 0
        failed = False
        rounds: list[dict] = []
        last_fields: list[dict] = [{} for _ in range(n)]
        publics_all: list[str] = []
        complete = False

        for t in range(max_rounds):
            round_goal = f"Round {t}: advance the task '{ctx.prompt[:120]}'"
            # ── Phase 1 — single-pass agent inference [DT eq 1,2] ───────────
            outs: list[dict] = []
            for i, role in enumerate(agents):
                S_i = f"{role.skill_card}\n{round_goal}\nMemory:\n" + "\n".join(memories[i])
                req = LLMRequest(
                    model=cfg.default_model,
                    system=S_i,
                    user=f"Task: {ctx.prompt}",
                    schema=role.required_fields,
                    purpose="agent",
                    role=role.name,
                    meta={"round": t},
                )
                fields, tokens = transport.complete(req)
                total_tokens += tokens
                if fields.get("_failed"):
                    failed = True
                    retries += 1
                outs.append(fields)
                if fields.get("complete"):
                    complete = True
            last_fields = outs

            # ── Phase 2 — topology induction [DT eq 4,5,6,7] ────────────────
            q_desc = [str(o.get("q_desc", "")) for o in outs]
            k_desc = [str(o.get("k_desc", "")) for o in outs]
            R = relevance_matrix(embedder.embed(q_desc), embedder.embed(k_desc))
            edges = build_edges(R, tau, k_in)

            # ── Phase 3 — deterministic ordering [DT eq 8-11] ───────────────
            sigma = topo_or_cyclebreak(n, edges)

            # ── Phase 4 — synchronization barrier + memory update [DT eq 3] ──
            # NOTE: memories update ONLY AFTER routing is computed for ALL agents.
            new_mem = [list(memories[i]) for i in range(n)]
            for i in range(n):
                pub_i = outs[i].get("public")
                if pub_i:
                    new_mem[i].append(f"[public {agents[i].name}] {pub_i}")
                providers = order_incoming(i, edges)  # by descending R, deterministic
                for j in providers:
                    priv_j = outs[j].get("private")
                    if priv_j:
                        new_mem[i].append(f"[from {agents[j].name}] {priv_j}")
            memories = new_mem

            mem_snapshot = {agents[i].name: list(memories[i]) for i in range(n)}

            for o in outs:
                if o.get("public"):
                    publics_all.append(str(o["public"]))

            rounds.append(
                {
                    "t": t,
                    "round_goal": round_goal,
                    "descriptors": {
                        agents[i].name: {"q": q_desc[i], "k": k_desc[i]} for i in range(n)
                    },
                    "edges": [
                        {"src": agents[j].name, "dst": agents[i].name, "score": round(s, 4)}
                        for (j, i, s) in edges
                    ],
                    "order": [agents[i].name for i in sigma],
                    "memory_after": mem_snapshot,
                }
            )

            # ── Phase 5 — termination [NEW] ─────────────────────────────────
            if complete:
                break
            if budget_remaining is not None and total_tokens >= budget_remaining:
                break

        # ── fold final memories + outputs into a single merged handoff ───────
        merged = HandoffState(fields=dict(handoff.fields))
        for o in last_fields:
            for key in _HANDOFF_KEYS:
                if o.get(key) is not None:
                    merged.set(key, o[key])
        if merged.get("draft_answer") is not None and merged.get("merged_answer") is None:
            merged.set("merged_answer", merged.get("draft_answer"))
        if complete:
            merged.set("complete", True)

        return TopologyResult(
            merged_handoff=merged,
            public_messages=publics_all,
            rounds_run=len(rounds),
            total_tokens=total_tokens,
            retries=retries,
            failed=failed,
            subtrace={"mode": "dytopo", "tau": tau, "k_in": k_in, "rounds": rounds},
        )
