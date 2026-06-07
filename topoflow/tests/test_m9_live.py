# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""M9 — Live run wiring. The offline-safe parts run here; the real network run is
skipped unless OPENROUTER_API_KEY is set and providers are installed."""
import os

import pytest

from topoflow.agents.tools import MockSearchProvider, make_search_provider
from topoflow.agents.transport import (
    MockTransport,
    OpenRouterTransport,
    TransportError,
    make_transport,
)
from topoflow.config import Config
from topoflow.eval.harness import run_suite
from topoflow.policy_graph import PolicyGraph
from topoflow.router import run_task
from topoflow.topology.semantic_match import MockEmbedder, make_embedder
from topoflow.types import TaskContext


def test_make_transport_offline_is_mock():
    assert isinstance(make_transport(live=False), MockTransport)


def test_openrouter_transport_requires_key():
    # Without a key it raises cleanly (no network), proving the guard works.
    old = os.environ.pop("OPENROUTER_API_KEY", None)
    try:
        with pytest.raises(TransportError):
            OpenRouterTransport(api_key=None)
    finally:
        if old is not None:
            os.environ["OPENROUTER_API_KEY"] = old


def test_make_embedder_offline_is_mock():
    assert isinstance(make_embedder(live=False), MockEmbedder)


def test_search_provider_factory_offline():
    assert isinstance(make_search_provider("web_search_exa", live=False), MockSearchProvider)


def test_web_search_cell_uses_provider():
    cfg = Config(topo_modes=("linear",))
    g = PolicyGraph(cfg)
    ctx = TaskContext("ws", "find facts", domain="advisory")
    tx = MockTransport(responder=lambda req: {"complete": True, "_tokens": 10}
                       if (req.role or "") == "evaluator" else {"_tokens": 10})

    captured = {}

    def sel(sig, legal):
        for a in legal:
            if a.kind == "invoke" and a.skill == "web_search_exa":
                captured["used"] = True
                return a
        for a in legal:
            if a.kind == "invoke" and a.skill == "evaluator":
                return a
        return legal[0]

    run_task(ctx, cfg, g, tx, select_fn=sel, search_provider=MockSearchProvider())
    assert captured.get("used")


@pytest.mark.skipif(not os.getenv("OPENROUTER_API_KEY"), reason="live run needs OPENROUTER_API_KEY")
def test_live_smoke_real_models():  # pragma: no cover - network
    cfg = Config(token_cap=12000)
    tx = make_transport(live=True)
    report = run_suite(cfg, transport=tx, epochs=1)
    assert report["arms"] and "H1_selection" in report["metrics"]
