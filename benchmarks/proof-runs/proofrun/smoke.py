"""Credential-free end-to-end plumbing smoke.

Starts the scripted mock model server (mock_llm.py) and — with --start-backend —
a throwaway native BigLaw backend wired to it (all tiers local, local
embeddings, its own temp data dir), then drives every selected matter through
the real driver and scorer. Proves: fixtures convert and ingest, tasks submit
and complete, gates resolve, deliverables render, documents.json/biglaw_tasks.json
are written, and scoring reads the run dir. The scores are of a scripted model,
so they measure the pipeline's handling of known findings — never capability.
"""

from __future__ import annotations

import argparse
import os
import shutil
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

from .drive import drive
from .matter import ROOT, load_all
from .mock_llm import serve

REPO = ROOT.parent.parent  # repo root (templates/, deadlines/rules/, agents/ ...)


def _wait_health(api: str, seconds: float) -> bool:
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(api + "/health", timeout=3) as r:
                if r.status == 200:
                    return True
        except OSError:
            pass
        time.sleep(1)
    return False


def start_backend(mock_port: int, port: int, workdir: Path) -> subprocess.Popen:
    binary = workdir / "biglaw"
    print(f"building backend -> {binary}")
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/biglaw"], cwd=REPO / "biglaw-go", check=True)
    # Run from a scratch cwd so ./data, ./audit.jsonl etc. never touch the repo;
    # link in the read-only trees the binary resolves relative to cwd.
    for name in ("templates", "deadlines", "agents", "adapters", "workflows", "flavours", "scripts"):
        if (REPO / name).exists() and not (workdir / name).exists():
            os.symlink(REPO / name, workdir / name)
    mock = f"http://127.0.0.1:{mock_port}"
    env = {
        k: v for k, v in os.environ.items()
        if not k.endswith("_API_KEY") and k not in ("PRIMARY_MODEL_KEY", "PRIMARY_MODEL_URL")
    }
    env.update({
        "API_PORT": str(port),
        "BIG_MICHAEL_MODE": "backend",
        "LOCAL_INFERENCE_URL": mock + "/v1",
        "LOCAL_INFERENCE_MODEL": "proofrun-mock",
        "LOCAL_INFERENCE_TIERS": "all",
        "LOCAL_EMBEDDINGS": "true",
        "LOCAL_EMBEDDING_MODEL": "proofrun-mock-embed",
        "OLLAMA_URL": mock,
        "EMBEDDING_DIMENSIONS": "768",
        "MODEL_CONTEXT_TOKENS": "32768",
        "AUTH_ENABLED": "false",
        "INFISICAL_CLIENT_ID": "",
    })
    log = open(workdir / "backend.log", "w")
    print(f"starting backend on :{port} (log {workdir / 'backend.log'})")
    return subprocess.Popen([str(binary)], cwd=workdir, env=env, stdout=log, stderr=subprocess.STDOUT,
                            stdin=subprocess.DEVNULL)


def smoke(a: argparse.Namespace) -> int:
    matters = load_all(a.matter)
    srv, script = serve(matters, a.mock_port)
    print(f"mock model server on 127.0.0.1:{a.mock_port}")
    proc = None
    workdir = None
    try:
        if a.start_backend:
            workdir = Path(tempfile.mkdtemp(prefix="proofrun-smoke-"))
            proc = start_backend(a.mock_port, a.backend_port, workdir)
            a.api = f"http://127.0.0.1:{a.backend_port}"
            if not _wait_health(a.api, 120):
                print("error: backend did not become healthy; tail of backend.log:")
                print((workdir / "backend.log").read_text(errors="replace")[-4000:])
                return 1
        results = drive(matters, a)
        print(f"\nmock model calls: {dict(script.calls)}")
        failed = [r for r in results if "error" in r]
        for r in failed:
            print(f"FAIL {r['task']}: {r['error']}")
        print("smoke:", "FAIL" if failed else "ok", f"({len(results) - len(failed)}/{len(results)} matters ran end to end)")
        return 1 if failed else 0
    finally:
        srv.shutdown()
        if proc:
            proc.terminate()
            try:
                proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                proc.kill()
        if workdir and not os.environ.get("PROOFRUN_KEEP_SMOKE"):
            shutil.rmtree(workdir, ignore_errors=True)
        elif workdir:
            print(f"kept smoke workdir {workdir}")
