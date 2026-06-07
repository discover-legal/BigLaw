# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Typed LLM I/O transport (spec §13).

The interface is intentionally synchronous and dependency-free; the live
provider implementation (Instructor over OpenRouter, pydantic schemas) is a lazy
import in `OpenRouterTransport` so the whole core + all milestone tests run
without network or heavy packages. `MockTransport` returns canned structured
output for tests.
"""
from __future__ import annotations

import os
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Optional, Protocol, runtime_checkable


@dataclass
class LLMRequest:
    model: str
    system: str
    user: str
    schema: tuple[str, ...]  # required output field names
    purpose: str = "invoke"  # "invoke" | "agent" | "judge"
    role: Optional[str] = None
    meta: dict = field(default_factory=dict)


@runtime_checkable
class Transport(Protocol):
    def complete(self, req: LLMRequest) -> tuple[dict, int]:
        """Return (parsed_fields, tokens_used)."""
        ...


class TransportError(RuntimeError):
    pass


class MockTransport:
    """Deterministic structured output for offline tests.

    A `responder(req) -> dict` may be supplied for full control. Otherwise a
    sensible default fills the requested schema so the macro loop runs
    end-to-end. `answer` is embedded as draft/merged answers so a GroundTruthQ
    fixture can verify it. `evaluator_completes` controls whether an evaluator
    invoke reports completion.
    """

    def __init__(
        self,
        responder: Optional[Callable[[LLMRequest], dict]] = None,
        *,
        answer: str = "ANSWER",
        tokens_per_call: int = 100,
        evaluator_completes: bool = True,
        fail_skills: tuple[str, ...] = (),
    ):
        self.responder = responder
        self.answer = answer
        self.tokens_per_call = tokens_per_call
        self.evaluator_completes = evaluator_completes
        self.fail_skills = set(fail_skills)
        self.calls: list[LLMRequest] = []

    def complete(self, req: LLMRequest) -> tuple[dict, int]:
        self.calls.append(req)
        if self.responder is not None:
            out = self.responder(req)
            return out, int(out.get("_tokens", self.tokens_per_call))
        return self._default(req), self.tokens_per_call

    def _default(self, req: LLMRequest) -> dict:
        role = (req.role or req.meta.get("skill") or "").lower()
        out: dict[str, object] = {}
        if req.purpose == "agent":
            out.update(
                {
                    "public": f"{req.role}: progress on task",
                    "private": f"{req.role}: internal note",
                    "q_desc": f"{req.role} needs inputs",
                    "k_desc": f"{req.role} provides {req.role} expertise",
                }
            )
            if "test" in role or "verif" in role:
                out["verification"] = "supported"
            if "solver" in role or "develop" in role:
                out["draft_answer"] = self.answer
            return out
        if req.purpose == "judge":
            return {"rankings": req.meta.get("default_rankings", [])}
        # invoke purpose
        if role.startswith("solver"):
            out["draft_answer"] = self.answer
        elif role == "planner":
            out["goal"] = "solve the task"
            out["subproblem"] = "step 1"
        elif role == "memory" or role.startswith("web_search"):
            out["evidence"] = ["fact A", "fact B"]
        elif role.startswith("verifier"):
            out["verification"] = "supported"
        elif role == "evaluator":
            out["complete"] = bool(self.evaluator_completes)
        out["_failed"] = role in self.fail_skills
        return out


class OpenRouterTransport:
    """Live transport: Instructor structured output over an OpenRouter-style
    multi-provider OpenAI client (spec §13). All heavy imports are lazy so the
    core + offline tests never need them. Provider-aware retry/backoff included.
    """

    def __init__(self, api_key: Optional[str] = None,
                 base_url: str = "https://openrouter.ai/api/v1",
                 max_retries: int = 3, max_tokens: int = 4000):
        key = api_key or os.getenv("OPENROUTER_API_KEY")
        if not key:
            raise TransportError("OPENROUTER_API_KEY not set")
        import instructor  # lazy, optional
        import openai  # lazy, optional

        self._client = instructor.from_openai(openai.OpenAI(base_url=base_url, api_key=key))
        self.max_retries = max_retries
        self.max_tokens = max_tokens

    def complete(self, req: LLMRequest) -> tuple[dict, int]:
        from pydantic import create_model  # lazy

        fields = {name: (Optional[Any], None) for name in (req.schema or ("answer",))}
        Model = create_model("StructuredOut", **fields)

        last_err: Optional[Exception] = None
        for attempt in range(self.max_retries):
            try:
                resp, completion = self._client.chat.completions.create_with_completion(
                    model=req.model,
                    response_model=Model,
                    max_tokens=self.max_tokens,
                    messages=[
                        {"role": "system", "content": req.system},
                        {"role": "user", "content": req.user},
                    ],
                )
                data = resp.model_dump()
                tokens = _usage_tokens(completion, req)
                return data, tokens
            except Exception as e:  # noqa: BLE001 — provider errors are heterogeneous
                last_err = e
                time.sleep(2 ** attempt)
        raise TransportError(f"transport failed after {self.max_retries} attempts: {last_err}")


def _usage_tokens(completion, req: LLMRequest) -> int:
    try:
        u = completion.usage
        return int(getattr(u, "total_tokens", 0)) or _estimate_tokens(req)
    except Exception:
        return _estimate_tokens(req)


def _estimate_tokens(req: LLMRequest) -> int:
    return max(1, (len(req.system) + len(req.user)) // 4)


def make_transport(live: bool = False, **kwargs) -> Transport:
    """Factory: MockTransport for offline/tests, OpenRouterTransport for live (M9)."""
    if live:
        return OpenRouterTransport(**kwargs)
    return MockTransport(**kwargs)
