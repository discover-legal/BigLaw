# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Thin REST client for the BigLaw v2 API (README § REST API), standard library only."""

from __future__ import annotations

import json
import re
import time
import urllib.error
import urllib.request
from typing import Any


class BigLawError(RuntimeError):
    pass


class BigLawClient:
    def __init__(self, base_url: str, timeout: float = 120.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def _req(self, method: str, path: str, body: Any = None) -> Any:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base_url + path, data=data, method=method,
                                     headers={"Content-Type": "application/json"} if data else {})
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                detail = json.loads(raw).get("error", raw.decode(errors="replace"))
            except ValueError:
                detail = raw.decode(errors="replace")
            raise BigLawError(f"{method} {path} -> {e.code}: {detail}") from None
        except urllib.error.URLError as e:
            raise BigLawError(f"{method} {path}: cannot reach BigLaw at {self.base_url} ({e.reason})") from None
        return json.loads(raw) if raw else None

    def health(self) -> dict:
        return self._req("GET", "/health")

    def ingest_text(self, name: str, text: str, matter: str = "") -> str:
        # v2 names have no separators: a nested LAB path "a/b.md" becomes "a__b.md".
        safe = re.sub(r"[\\/]+", "__", name).strip() or "document.txt"
        return self._req("POST", "/documents", {"name": safe, "text": text, "matterNumber": matter})["id"]

    def submit_task(self, description: str, workflow_type: str, document_ids: list[str], jurisdiction: str = "",
                    matter: str = "", tool_bandit: str = "on", max_steps: int | None = None) -> dict:
        return self._req("POST", "/tasks", {"description": description, "workflowType": workflow_type, "documentIds": document_ids,
                                            "jurisdiction": jurisdiction, "matterNumber": matter, "toolBandit": tool_bandit, "maxSteps": max_steps})

    def get_task(self, task_id: str) -> dict:
        return self._req("GET", f"/tasks/{task_id}")

    def approve_gate(self, task_id: str, gate_id: str) -> None:
        self._req("POST", f"/tasks/{task_id}/gates/{gate_id}/approve")

    def reject_gate(self, task_id: str, gate_id: str, reason: str) -> None:
        self._req("POST", f"/tasks/{task_id}/gates/{gate_id}/reject", {"reason": reason})

    def task_cost(self, task_id: str) -> dict:
        return self._req("GET", f"/tasks/{task_id}/cost")

    def wait_for_task(self, task_id: str, poll_seconds: float, timeout_seconds: float, on_event=None, gate_policy: str = "approve") -> dict:
        """Poll until the task is complete, resolving its gates; raise on failed, cancelled or timeout.

        Finding gates follow gate_policy (approve, or reject to benchmark the gate as a filter);
        the deliverable gate is always approved, so the run ends with the lead's deliverable.
        """
        deadline = time.monotonic() + timeout_seconds
        last, seen = None, set()
        while time.monotonic() < deadline:
            task = self.get_task(task_id)
            status = task.get("status")
            if status != last and on_event:
                on_event("status", {"status": status, "step": (task.get("run") or {}).get("step"),
                                    "stopReason": (task.get("run") or {}).get("stopReason")})
            last = status
            if status == "complete":
                return task
            if status in ("failed", "cancelled"):
                raise BigLawError(f"task {task_id} {status}: {task.get('error') or task.get('groundingAlert') or 'no reason given'}")
            if status == "awaiting_gate":
                for gate in task.get("pendingGates") or []:
                    gid = gate.get("gateId")
                    if not gid or gid in seen:
                        continue
                    reject = gate.get("kind") == "finding" and gate_policy == "reject"
                    if reject:
                        self.reject_gate(task_id, gid, "rejected by the LAB driver (gate-policy=reject)")
                    else:
                        self.approve_gate(task_id, gid)
                    seen.add(gid)
                    if on_event:
                        on_event("gate_resolved", {"gateId": gid, "kind": gate.get("kind"), "findingId": gate.get("findingId"),
                                                   "action": "reject" if reject else "approve"})
            time.sleep(poll_seconds)
        raise BigLawError(f"task {task_id} did not complete within {timeout_seconds:.0f}s")
