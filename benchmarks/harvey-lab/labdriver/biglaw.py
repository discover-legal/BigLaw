"""Thin REST client for the BigLaw backend (Go API, :3101/:3102)."""

from __future__ import annotations

import time
from typing import Any

import requests


class BigLawError(RuntimeError):
    pass


class BigLawClient:
    def __init__(self, base_url: str, timeout: float = 60.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.http = requests.Session()

    def _req(self, method: str, path: str, **kw: Any) -> Any:
        kw.setdefault("timeout", self.timeout)
        resp = self.http.request(method, self.base_url + path, **kw)
        if resp.status_code >= 400:
            try:
                detail = resp.json().get("error", resp.text)
            except ValueError:
                detail = resp.text
            raise BigLawError(f"{method} {path} -> {resp.status_code}: {detail}")
        return resp.json() if resp.content else None

    def health(self) -> dict:
        return self._req("GET", "/health")

    def ingest_text(self, title: str, content: str) -> str:
        doc = self._req("POST", "/documents", json={"title": title, "content": content})
        return doc["id"]

    def submit_task(
        self,
        description: str,
        workflow_type: str,
        document_ids: list[str],
        jurisdiction: str = "",
    ) -> dict:
        return self._req(
            "POST",
            "/tasks",
            json={
                "description": description,
                "workflowType": workflow_type,
                "documentIds": document_ids,
                "jurisdiction": jurisdiction,
            },
        )

    def get_task(self, task_id: str) -> dict:
        return self._req("GET", f"/tasks/{task_id}")

    def approve_gate(self, task_id: str, gate_id: str, note: str) -> None:
        self._req("POST", f"/tasks/{task_id}/gates/{gate_id}/approve", json={"note": note})

    def reject_gate(self, task_id: str, gate_id: str, reason: str) -> None:
        self._req("POST", f"/tasks/{task_id}/gates/{gate_id}/reject", json={"reason": reason})

    def task_cost(self, task_id: str) -> dict:
        return self._req("GET", f"/tasks/{task_id}/cost")

    def wait_for_task(
        self,
        task_id: str,
        poll_seconds: float,
        timeout_seconds: float,
        on_event=None,
        gate_policy: str = "approve",
    ) -> dict:
        """Poll until the task completes or fails, resolving human gates.

        gate_policy "approve" passes every flagged finding through; "reject"
        drops them (the orchestrator removes the finding and continues), which
        benchmarks the verification gate as a filter. on_event(kind, payload)
        is called for "status" and "gate_resolved" events.
        Returns the final task object; raises BigLawError on failure/timeout.
        """
        deadline = time.monotonic() + timeout_seconds
        last_status = None
        gates_seen: set[str] = set()

        while time.monotonic() < deadline:
            task = self.get_task(task_id)
            status = task.get("status")
            if status != last_status and on_event:
                on_event("status", {"status": status, "round": task.get("currentRound")})
            last_status = status

            if status == "complete":
                return task
            if status == "failed":
                raise BigLawError(f"task {task_id} failed: {task.get('error', 'unknown')}")

            if status == "awaiting_gate":
                for gate in task.get("pendingGates") or []:
                    gid = gate.get("id")
                    if not gid or gid in gates_seen or gate.get("status") not in (None, "", "pending"):
                        continue
                    if gate_policy == "reject":
                        self.reject_gate(task_id, gid, "auto-rejected by LAB benchmark driver (gate-policy=reject)")
                    else:
                        self.approve_gate(task_id, gid, "auto-approved by LAB benchmark driver")
                    gates_seen.add(gid)
                    if on_event:
                        on_event("gate_resolved", {
                            "gateId": gid,
                            "findingId": gate.get("findingId"),
                            "action": gate_policy,
                        })

            time.sleep(poll_seconds)

        raise BigLawError(f"task {task_id} did not complete within {timeout_seconds:.0f}s")
