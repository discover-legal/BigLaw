# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""The suite's machinery: the build at the current commit, a Postgres of its own, `biglaw serve`
started from the config alone, and read-only queries of the suite's database."""

from __future__ import annotations

import hashlib
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


BUILD_PATHS = ("internal", "cmd", "eval", "ui", "go.mod", "go.sum")  # what the binary or the suite's Python is made of


def build_state(repo: Path = REPO) -> dict:
    """The checkout as it will be built and run: HEAD's sha, and dirty when any tracked file differs
    from it or any untracked file sits where it would be compiled or imported (BUILD_PATHS; go build
    compiles every .go file of a package directory, the suite imports eval/ from the worktree).
    Ignored files (ui/dist, node_modules) and eval/bench/results do not count. patch is a sha256 of
    the differences when dirty, so two dirty runs can at least be told apart."""
    sha, _ = git_state(repo)
    if sha is None:
        return {"sha": None, "dirty": None, "patch": None, "untracked": []}
    excl = ":(exclude)eval/bench/results"
    st = subprocess.run(["git", "-C", str(repo), "status", "--porcelain", "--untracked-files=all", "--", ".", excl],
                        capture_output=True, text=True, check=True).stdout.splitlines()
    tracked = [line for line in st if not line.startswith("??")]
    untracked = sorted(line[3:] for line in st if line.startswith("??") and line[3:].split("/")[0] in BUILD_PATHS)
    dirty = bool(tracked or untracked)
    patch = None
    if dirty:
        h = hashlib.sha256(subprocess.run(["git", "-C", str(repo), "diff", "HEAD", "--binary", "--", ".", excl],
                                          capture_output=True, check=True).stdout)
        for rel in untracked:
            h.update(rel.encode() + b"\0" + (repo / rel).read_bytes())
        patch = "sha256:" + h.hexdigest()
    return {"sha": sha, "dirty": dirty, "patch": patch, "untracked": untracked[:50]}


# The in-repo yardstick: code and data that score or drive a task but change with the build under test.
# A difference between two runs makes the tasks it touches not comparable (compare.py).
YARDSTICK = {
    "proofrun": ("eval/proofrun", "eval/tasks", "eval/matters.json"),       # gate A's rubric, oracle and scorer
    "labdriver": ("eval/lab",),                                             # task description, conversion, rendering
    "bench": ("eval/bench/benchlib", "eval/bench/run.py", "eval/bench/stubjudge.py", "eval/bench/schema"),
}


def tree_hash(paths: tuple, repo: Path = REPO) -> str:
    """sha256 over the files under paths as they are on disk (what this run executes), by relative path."""
    h = hashlib.sha256()
    files = []
    for p in paths:
        root = repo / p
        files += [root] if root.is_file() else [f for f in root.rglob("*") if f.is_file()]
    for f in sorted(files):
        rel = f.relative_to(repo).as_posix()
        if "__pycache__" in rel or rel.endswith(".pyc"):
            continue
        h.update(rel.encode() + b"\0" + hashlib.sha256(f.read_bytes()).digest())
    return "sha256:" + h.hexdigest()


def yardstick(repo: Path = REPO) -> dict:
    return {k: tree_hash(v, repo) for k, v in YARDSTICK.items()}


def go_version() -> str | None:
    try:
        return subprocess.run(["go", "version"], cwd=REPO, env=_base_env(), capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return None


def pandoc_version() -> str | None:
    """The pandoc harvey-labs converts .docx deliverables with: it shapes the text the judge reads."""
    try:
        out = subprocess.run(["pandoc", "--version"], capture_output=True, text=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return None
    first = out.splitlines()[0] if out else ""
    return first.split()[-1] if first else None


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
    for k in ("GOPATH", "GOCACHE", "GOMODCACHE"):  # caches only: GOFLAGS (tags) and GOTOOLCHAIN would change what is built
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
    """The suite's database, always empty when the suite starts: a throwaway pgvector container
    (config postgres_image) on a loopback port, or, with BIGLAW_DATABASE_URL, a new database the suite
    creates in that server (and drops at the end). fresh=False uses the DSN as it is (the smoke: the
    test's own schema)."""

    def __init__(self, dsn: str | None, image: str, log, fresh: bool = True):
        self.log, self.container, self.password, self.created = log, None, "", None
        self.dsn, self.serve_env = dsn, {}
        if dsn is None:
            if not shutil.which("docker"):
                raise InfraError("no BIGLAW_DATABASE_URL and no docker to start a Postgres: set BIGLAW_DATABASE_URL "
                                 "to a server where the suite may create a database (pgvector available)")
            self.password = secrets.token_hex(16)
            port = free_port()
            name = f"biglaw-bench-{secrets.token_hex(4)}"
            self.container = name  # registered before it exists: any failure from here removes it
            try:
                r = subprocess.run(["docker", "run", "-d", "--rm", "--name", name, "-p", f"127.0.0.1:{port}:5432",
                                    "-e", "POSTGRES_PASSWORD", "-e", "POSTGRES_USER=biglaw", "-e", "POSTGRES_DB=biglaw", image],
                                   env={**os.environ, "POSTGRES_PASSWORD": self.password}, capture_output=True, text=True)
                if r.returncode != 0:
                    raise InfraError(f"docker run {image} failed: {r.stderr.strip()[-500:]}")
                log(f"postgres: container {name} ({image}) on 127.0.0.1:{port}")
                self.dsn = f"postgres://biglaw@127.0.0.1:{port}/biglaw?sslmode=disable"
                self.serve_env = {"PGPASSWORD": self.password}
                self._wait()
            except BaseException:
                self.close()
                raise
        self.env = {**pg_env(self.dsn), **({"PGPASSWORD": self.password} if self.password else {})}
        if dsn is not None and fresh:
            self._create()

    def _create(self) -> None:
        name = f"biglaw_bench_{secrets.token_hex(6)}"
        if self.query(f"CREATE DATABASE {name}") is None:
            raise InfraError("cannot create the suite's database in BIGLAW_DATABASE_URL's server (the role needs CREATEDB): "
                             "the suite always runs on an empty database")
        self.created = (dict(self.env), name)
        u = urlparse(self.dsn)
        q = "&".join(f"{k}={v[-1]}" for k, v in parse_qs(u.query).items() if k != "search_path")
        self.dsn = u._replace(path="/" + name, query=q).geturl()
        self.env = {**pg_env(self.dsn), **({"PGPASSWORD": self.env["PGPASSWORD"]} if "PGPASSWORD" in self.env else {})}
        self.log(f"postgres: created database {name}")

    def _wait(self) -> None:
        for _ in range(120):
            r = subprocess.run(["docker", "exec", self.container, "pg_isready", "-U", "biglaw", "-d", "biglaw", "-h", "127.0.0.1"],
                               capture_output=True)
            if r.returncode == 0:
                return
            time.sleep(1)
        raise InfraError("the Postgres container did not become ready in 120 s")

    def query(self, sql: str, env: dict | None = None) -> list[list[str]] | None:
        """Rows of a query (psql -At, | separated), or None when no psql can run it."""
        if shutil.which("psql"):
            cmd, env = ["psql", "-X", "-At", "-v", "ON_ERROR_STOP=1", "-c", sql], {**_base_env(), **(env or self.env)}
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

    def table_count(self) -> int | None:
        """Tables in the schema serve will use (0: a database nothing has migrated); None when unreadable."""
        rows = self.query("SELECT count(*) FROM pg_tables WHERE schemaname = ANY (current_schemas(false))")
        return int(rows[0][0]) if rows else None

    def versions(self) -> dict:
        rows = self.query("SELECT current_setting('server_version'), coalesce((SELECT extversion FROM pg_extension WHERE extname = 'vector'), '')")
        return {"server": rows[0][0], "vector": rows[0][1] or None} if rows else {"server": None, "vector": None}

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
        if self.created:
            env, name = self.created
            self.created = None
            self.query(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)", env)
            self.log(f"postgres: dropped database {name}")
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
