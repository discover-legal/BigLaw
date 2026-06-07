# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Discover Legal
"""Web-search tool providers (spec §13).

Web search cells (web_search_exa, web_search_tavily) are first-class tool
actions with provider-aware retry/backoff. MockSearchProvider serves tests; the
Exa/Tavily providers lazily import their clients for live runs.
"""
from __future__ import annotations

import os
import time
from typing import Protocol, runtime_checkable


@runtime_checkable
class SearchProvider(Protocol):
    def search(self, query: str, k: int = 5) -> list[dict]:
        ...


class MockSearchProvider:
    def __init__(self, canned: list[dict] | None = None):
        self.canned = canned or [{"title": "doc", "snippet": "fact", "url": "https://example/"}]

    def search(self, query: str, k: int = 5) -> list[dict]:
        return self.canned[:k]


class ExaProvider:
    def __init__(self, api_key: str | None = None, max_retries: int = 3):
        from exa_py import Exa  # lazy, optional

        self._exa = Exa(api_key or os.getenv("EXA_API_KEY"))
        self.max_retries = max_retries

    def search(self, query: str, k: int = 5) -> list[dict]:
        last = None
        for attempt in range(self.max_retries):
            try:
                res = self._exa.search_and_contents(query, num_results=k)
                return [{"title": r.title, "snippet": (r.text or "")[:500], "url": r.url} for r in res.results]
            except Exception as e:  # noqa: BLE001
                last = e
                time.sleep(2 ** attempt)
        raise RuntimeError(f"exa search failed: {last}")


class TavilyProvider:
    def __init__(self, api_key: str | None = None, max_retries: int = 3):
        from tavily import TavilyClient  # lazy, optional

        self._client = TavilyClient(api_key=api_key or os.getenv("TAVILY_API_KEY"))
        self.max_retries = max_retries

    def search(self, query: str, k: int = 5) -> list[dict]:
        last = None
        for attempt in range(self.max_retries):
            try:
                res = self._client.search(query, max_results=k)
                return [{"title": r.get("title"), "snippet": r.get("content", "")[:500], "url": r.get("url")}
                        for r in res.get("results", [])]
            except Exception as e:  # noqa: BLE001
                last = e
                time.sleep(2 ** attempt)
        raise RuntimeError(f"tavily search failed: {last}")


def make_search_provider(name: str, live: bool = False):
    if not live:
        return MockSearchProvider()
    if "exa" in name:
        return ExaProvider()
    if "tavily" in name:
        return TavilyProvider()
    return MockSearchProvider()
