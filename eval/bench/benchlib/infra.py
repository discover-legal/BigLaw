# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""The suite's machinery: the build at the current commit, a Postgres of its own, `biglaw serve`
started from the config alone, and read-only queries of the suite's database."""

from __future__ import annotations

import json
import os
import secrets
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlparse

REPO = Path(__file__).resolve().parents[3]


class InfraError(RuntimeError):
    pass


def git_state(repo: Path = REPO) -> tuple[str | None, bool | None]:
    """(HEAD's sha, whether tracked files differ from it), or (None, None) outside a git checkout.
    Results under eval/bench/results do not count as a change."""
    try:
        sha = subprocess.run(["git", "-C", str(repo), "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()
        st = subprocess.run(["git", "-C", str(repo), "status", "--porcelain", "--untracked-files=no", "--", ".",
                             ":(exclude)eval/bench/results"], capture_output=True, text=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return None, None
    return sha, bool(st.strip())


def harness_commit(labs: Path) -> tuple[str | None, bool | None]:
    """The harvey-labs checkout's HEAD and whether its tracked files are modified."""
    return git_state(labs) if (labs / ".git").exists() else (None, None)


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def build(out: Path, version: str, log) -> Path:
    """The static binary of the checkout as it is, versioned so /health proves which one serves."""
    out.parent.mkdir(parents=True, exist_ok=True)
    env = {**_base_env(), "CGO_ENABLED": "0"}
    for k in ("GOPATH", "GOCACHE", "GOMODCACHE", "GOFLAGS", "GOTOOLCHAIN"):
        if k in os.environ:
            env[k] = os.environ[k]
    r = subprocess.run(["go", "build", "-trimpath", f"-ldflags=-s -w -X main.version={version}", "-o", str(out), "./cmd/biglaw"],
                       cwd=REPO, env=env, capture_output=True, text=True)
    log(f"go build: exit {r.returncode}")
    if r.returncode != 0:
        raise InfraError(f"go build failed:\n{r.stderr[-4000:]}")
    return out


def _base_env() -> dict:
    """A child's environment starts from nothing: only what finds programs and a home. Nothing else of
    the caller's shell (BIGLAW_*, ANTHROPIC_BASE_URL, OPENAI_*, proxies' credentials) reaches serve or
    the judge unless the suite puts it there."""
    keep = ("PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
            "https_proxy", "http_proxy", "no_proxy", "REQUESTS_CA_BUNDLE", "UV_CACHE_DIR", "XDG_CACHE_HOME")
    return {k: os.environ[k] for k in keep if k in os.environ}


def child_env(extra: dict) -> dict:
    env = _base_env()
    env.update(extra)
    env.pop("ANTHROPIC_BASE_URL", None)  # the judge and serve talk to the pinned endpoint only
    return env


def pg_env(dsn: str) -> dict:
    """libpq variables for a postgres:// DSN, so psql never takes the password on its command line.
    A search_path parameter (storetest's DSNs) becomes PGOPTIONS."""
    u = urlparse(dsn)
    if u.scheme not in ("postgres", "postgresql"):
        raise InfraError("BIGLAW_DATABASE_URL must be a postgres:// URL for the bench's own queries")
    q = {k: v[-1] for k, v in parse_qs(u.query).items()}
    env = {"PGHOST": u.hostname or "localhost", "PGPORT": str(u.port or 5432), "PGDATABASE": unquote(u.path.lstrip("/")) or "postgres"}
    if u.username:
        env["PGUSER"] = unquote(u.username)
    if u.password:
        env["PGPASSWORD"] = unquote(u.password)
    if "sslmode" in q:
        env["PGSSLMODE"] = q["sslmode"]
    if "search_path" in q:
        env["PGOPTIONS"] = f"-csearch_path={q['search_path']}"
    return env


class Postgres:
    """The suite's database: BIGLAW_DATABASE_URL when given, else a throwaway pgvector container
    (config postgres_image) on a loopback port, removed at the end."""

    def __init__(self, dsn: str | None, image: str, log):
        self.log, self.container, self.password = log, None, ""
        self.dsn, self.serve_env = dsn, {}
        if dsn is None:
            if not shutil.which("docker"):
                raise InfraError("no BIGLAW_DATABASE_URL and no docker to start a Postgres: set BIGLAW_DATABASE_URL "
                                 "to a fresh database with pgvector available")
            self.password = secrets.token_hex(16)
            port = free_port()
            name = f"biglaw-bench-{secrets.token_hex(4)}"
            r = subprocess.run(["docker", "run", "-d", "--rm", "--name", name, "-p", f"127.0.0.1:{port}:5432",
                                "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_USER=biglaw", "-e", "POSTGRES_DB=biglaw", image],
                               env={**os.environ, "POSTGRES_PASSWORD": self.password}, capture_output=True, text=True)
            if r.returncode != 0:
                raise InfraError(f"docker run {image} failed: {r.stderr.strip()[-500:]}")
            self.container = name
            log(f"postgres: container {name} ({image}) on 127.0.0.1:{port}")
            self.dsn = f"postgres://biglaw@127.0.0.1:{port}/biglaw?sslmode=disable"
            self.serve_env = {"PGPASSWORD": self.password}
            self._wait()
        self.env = {**pg_env(self.dsn), **({"PGPASSWORD": self.password} if self.password else {})}

    def _wait(self) -> None:
        for _ in range(120):
            r = subprocess.run(["docker", "exec", self.container, "pg_isready", "-U", "biglaw", "-d", "biglaw", "-h", "127.0.0.1"],
                               capture_output=True)
            if r.returncode == 0:
                return
            time.sleep(1)
        raise InfraError("the Postgres container did not become ready in 120 s")

    def query(self, sql: str) -> list[list[str]] | None:
        """Rows of a read-only query (psql -At, | separated), or None when no psql can run it."""
        if shutil.which("psql"):
            cmd, env = ["psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql], {**_base_env(), **self.env}
        elif self.container:
            cmd = ["docker", "exec", "-e", "PGPASSWORD", self.container, "psql", "-X", "-At", "-v", "ON_ERROR_STOP=1",
                   "-h", "127.0.0.1", "-U", "biglaw", "-d", "biglaw", "-c", sql]
            env = {**os.environ, "PGPASSWORD": self.password}
        else:
            return None
        r = subprocess.run(cmd, env=env, capture_output=True, text=True, timeout=60)
        if r.returncode != 0:
            self.log(f"db query failed: {r.stderr.strip()[-300:]}")
            return None
        return [line.split("|") for line in r.stdout.splitlines() if line]

    def task_count(self) -> int | None:
        """Tasks already in the database (0 before serve has migrated it); None when it cannot be read."""
        exists = self.query("SELECT to_regclass('tasks') IS NOT NULL")  # the count would fail to plan without the table
        if not exists:
            return None
        if exists[0][0] != "t":
            return 0
        rows = self.query("SELECT count(*) FROM tasks")
        return int(rows[0][0]) if rows else None

    def tool_calls(self) -> dict | None:
        rows = self.query("SELECT tool, sum(calls) FROM tool_stats GROUP BY tool")
        return None if rows is None else {r[0]: float(r[1]) for r in rows}

    def judge_verdicts(self, task_id: str) -> dict | None:
        """The figure judge's verdicts of one task by answer (yes = a conflict)."""
        if not all(c in "0123456789abcdef-" for c in task_id):
            return None
        rows = self.query(f"SELECT data->>'sameQuantity', count(*) FROM ledger WHERE task_id = '{task_id}' AND kind = 'verdict' GROUP BY 1")
        return None if rows is None else {r[0]: int(r[1]) for r in rows}

    def close(self) -> None:
        if self.container:
            subprocess.run(["docker", "rm", "-f", self.container], capture_output=True)
            self.log(f"postgres: container {self.container} removed")
            self.container = None


class Serve:
    """`biglaw serve` with exactly the config's environment, on a loopback port, logging to a file."""

    def __init__(self, binary: Path, env: dict, logfile: Path, redact):
        self.port = free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        logfile.parent.mkdir(parents=True, exist_ok=True)
        self.logfile, self.redact = logfile, redact
        self._raw = logfile.with_suffix(".raw")
        self._fh = self._raw.open("wb")
        self.proc = subprocess.Popen([str(binary), "serve"], env=child_env({**env, "BIGLAW_ADDR": f"127.0.0.1:{self.port}"}),
                                     stdout=self._fh, stderr=subprocess.STDOUT)

    def health(self, timeout: float = 60) -> dict:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise InfraError(f"biglaw serve exited with {self.proc.returncode}: see {self.logfile}")
            try:
                with urllib.request.urlopen(self.url + "/health", timeout=3) as r:
                    return json.loads(r.read())
            except (urllib.error.URLError, OSError, ValueError):
                time.sleep(0.5)
        raise InfraError(f"biglaw serve did not answer /health in {timeout:.0f} s")

    def stop(self) -> None:
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(30)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self._fh.close()
        self.logfile.write_text(self.redact(self._raw.read_text(encoding="utf-8", errors="replace")), encoding="utf-8")
        self._raw.unlink()
