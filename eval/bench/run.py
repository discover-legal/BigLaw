#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""BigLaw benchmark suite (docs/benchmarks.md).

    python3 eval/bench/run.py run [--labs-dir ~/harvey-labs]   # the pinned suite (make bench): needs ANTHROPIC_API_KEY
    python3 eval/bench/run.py preflight [--labs-dir DIR]       # everything run starts, up to /health; no task, no model call
    python3 eval/bench/run.py smoke --api URL --out DIR        # offline: what eval/bench_test.go runs (no key, no credit)
    python3 eval/bench/run.py check [SUMMARY ...]              # configs, baselines and summaries against their schemas
    python3 eval/bench/run.py pin [--config PATH]              # re-pin a deliberately edited config (new hash)

`run` reads eval/bench/config.json and nothing else. It refuses a config whose hash does not match,
any OPENAI_* or other vendor opt-in variable, a checkout that differs from its commit (unless
--allow-dirty: NOT COMPARABLE) and a harvey-labs checkout at another commit; starts Postgres (a
pgvector container, or a new database in BIGLAW_DATABASE_URL's server); builds the binary at HEAD;
starts `biglaw serve` with exactly the config's environment; runs each task, judges it, enforces the
spend cap; and writes eval/bench/results/<date>-<sha7>/summary.json. SIGINT, SIGTERM and SIGHUP
cancel the running task and stop serve and the database. The key comes only from ANTHROPIC_API_KEY or
the file ANTHROPIC_API_KEY_FILE names; it is never printed, logged or written to results.
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import sys
from datetime import datetime, timezone
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from benchlib import config as bconfig  # noqa: E402
from benchlib.infra import (InfraError, Postgres, Serve, build, build_state, go_version, harness_commit,  # noqa: E402
                            pandoc_version, yardstick)
from benchlib.suite import cancel_active, check_summary, run_suite, write_summary  # noqa: E402

RESULTS = HERE / "results"


def out_dir(root: Path, sha: str | None) -> Path:
    base = f"{datetime.now(timezone.utc):%Y%m%d}-{(sha or 'nogit')[:7]}"
    p, n = root / base, 2
    while p.exists():
        p, n = root / f"{base}-{n}", n + 1
    (p / "logs").mkdir(parents=True)
    return p


def logger(out: Path, redact):
    path = out / "logs" / "bench.log"

    def log(msg: str) -> None:
        line = redact(f"[{datetime.now(timezone.utc):%H:%M:%S}] {msg}")
        print(line, flush=True)
        with path.open("a", encoding="utf-8") as f:
            f.write(line + "\n")
    return log


def lab_root_for(out: Path, tasks: Path) -> Path:
    """The suite's own LAB_ROOT: tasks/ linked to the harness's tasks, results/ inside the run dir, and
    no .env (harvey-labs loads <LAB_ROOT>/.env, which could set a base URL or another provider)."""
    root = out / "lab"
    (root / "results").mkdir(parents=True)
    (root / "tasks").symlink_to(tasks.resolve(), target_is_directory=True)
    return root


def print_table(summary: dict) -> None:
    print(f"\n{'task':<22} {'status':<14} {'score':>7} {'cost':>8} {'calls':>6}  stop")
    for t in summary["tasks"]:
        s = t["score"]
        sc = f"{s['passed']}/{s['total']}" if s["total"] is not None else "-"
        cost = f"${t['cost_usd']:.2f}" if t["cost_usd"] is not None else "-"
        print(f"{t['id']:<22} {t['status']:<14} {sc:>7} {cost:>8} {t['calls'] if t['calls'] is not None else '-':>6}  {t['stop_reason'] or '-'}")
    sp = summary["spend"]
    print(f"charged ${sp['charged_usd']:.2f} of the ${sp['cap_usd']:.2f} cap; config {summary['config_hash'][:19]}…")
    if summary["stopped"]:
        print(f"STOPPED: {summary['stopped']['reason']}: {summary['stopped']['detail']}")


def _exit_on_signal(signum, _frame):
    raise SystemExit(128 + signum)  # unwinds through every finally: the task is cancelled, serve and the database stopped


def refusals(a, cfg_kind: str = "real") -> tuple[dict, str]:
    """The config and the key, or ConfigError: nothing has started yet."""
    cfg = bconfig.load(Path(a.config))
    if cfg["mode"] != cfg_kind:
        raise bconfig.ConfigError(f"{a.config} is a {cfg['mode']} config: `{a.cmd}` takes the {cfg_kind} one (smoke: `run.py smoke`)")
    env_errs = bconfig.refuse_env(os.environ)
    if env_errs:
        raise bconfig.ConfigError("; ".join(env_errs))
    return cfg, bconfig.load_key(os.environ)


def check_harness(cfg: dict, labs: Path) -> tuple[str | None, bool | None]:
    commit, dirty = harness_commit(labs)
    want = cfg["harness"]["commit"]
    if any(t["kind"] == "lab" for t in cfg["tasks"]):
        if commit is None or not commit.startswith(want):
            raise bconfig.ConfigError(f"{labs} is not a harvey-labs checkout at {want} (found {commit and commit[:7]}): "
                                      f"git -C {labs} checkout {want}")
        if dirty:
            raise bconfig.ConfigError(f"{labs} has modified tracked files: the harness must be {want} as committed")
    return commit, dirty


def start_backend(cfg: dict, key: str, out: Path, log, redact, keep: list):
    """Postgres (always an empty database), the binary at HEAD, and serve with exactly the config's
    environment, checked at /health. Everything started is appended to keep, for the caller to stop."""
    db = Postgres(os.environ.get("BIGLAW_DATABASE_URL") or None, cfg["postgres_image"], log)
    keep.append(db)
    n = db.table_count()
    if n is None:
        raise InfraError("cannot read the suite's database (psql missing or the query failed)")
    if n:
        raise InfraError(f"the suite's database already holds {n} tables: it must start empty")
    sha = build_state()["sha"]
    version = f"bench-{(sha or 'nogit')[:7]}"
    binary = build(out / "build" / "biglaw", version, log)
    serve = Serve(binary, bconfig.serve_env(cfg, db.dsn, key, db.serve_env), out / "logs" / "serve.log", redact)
    keep.append(serve)
    h = serve.health()
    if h.get("version") != version or h.get("search") != cfg["search"]:
        raise InfraError(f"serve answers version {h.get('version')!r} search {h.get('search')!r}; expected {version!r} {cfg['search']!r}")
    log(f"serve {serve.url}: {h}")
    return db, serve, {"fresh": True, **db.versions(), "image": None if os.environ.get("BIGLAW_DATABASE_URL") else cfg["postgres_image"]}


def stop_all(keep: list, log) -> None:
    cancel_active(log)
    for x in reversed(keep):
        try:
            x.stop() if isinstance(x, Serve) else x.close()
        except Exception as e:  # noqa: BLE001 - keep stopping the rest
            log(f"cleanup: {e}")


def cmd_run(a) -> int:
    try:
        cfg, key = refusals(a)
        labs = Path(a.labs_dir).expanduser().resolve()
        commit, labs_dirty = check_harness(cfg, labs)
    except bconfig.ConfigError as e:
        print(f"refused: {e}", file=sys.stderr)
        return 2
    if os.environ.get("ANTHROPIC_BASE_URL"):
        print("note: ANTHROPIC_BASE_URL is set in this shell; the bench unsets it for serve and the judge")
    redact = bconfig.Redactor(key, os.environ.get("BIGLAW_DATABASE_URL", ""))
    bs = build_state()
    if bs["dirty"] and not a.allow_dirty:
        print("refused: the checkout differs from its commit (modified tracked files, or untracked files under "
              f"{', '.join(('internal', 'cmd', 'eval', 'ui', 'go.mod', 'go.sum'))}: {', '.join(bs['untracked'][:10]) or 'see git status'}); "
              "commit or remove them (a result names one commit) or pass --allow-dirty (the run is then NOT COMPARABLE)",
              file=sys.stderr)
        return 2
    out = out_dir(Path(a.results_root), bs["sha"])
    log = logger(out, redact)
    log(f"bench {out.name}: config {cfg['config_hash']}, build {bs['sha']}{' (dirty)' if bs['dirty'] else ''}, harness {commit}")
    keep: list = []
    try:
        db, serve, database = start_backend(cfg, key, out, log, redact, keep)
        summary = run_suite(cfg, api=serve.url, lab_root=lab_root_for(out, labs / "tasks"), out=out, db=db, harness=labs, key=key,
                            redact=redact, log=log, build={**bs, "go": go_version()},
                            harness_info={"name": cfg["harness"]["name"], "commit": commit, "dirty": labs_dirty, "pandoc": pandoc_version()},
                            database=database, yardstick=yardstick(), config_hash=cfg["config_hash"])
    except InfraError as e:
        log(f"error: {e}")
        return 1
    finally:
        stop_all(keep, log)
    path = write_summary(summary, out)
    print_table(summary)
    print(f"summary: {path}")
    return 3 if summary["stopped"] and summary["stopped"]["reason"] == "provider_error" else 0


def cmd_preflight(a) -> int:
    """Everything `run` does up to serve answering /health, then stop: no task, no model call."""
    try:
        cfg, key = refusals(a)
        if a.labs_dir:
            check_harness(cfg, Path(a.labs_dir).expanduser().resolve())
    except bconfig.ConfigError as e:
        print(f"refused: {e}", file=sys.stderr)
        return 2
    redact = bconfig.Redactor(key, os.environ.get("BIGLAW_DATABASE_URL", ""))
    out = Path(a.out).resolve() if a.out else out_dir(Path(a.results_root), build_state()["sha"])
    (out / "logs").mkdir(parents=True, exist_ok=True)
    log = logger(out, redact)
    keep: list = []
    try:
        _, _, database = start_backend(cfg, key, out, log, redact, keep)
        log(f"preflight ok: database {database}")
    except InfraError as e:
        log(f"preflight failed: {e}")
        return 1
    finally:
        stop_all(keep, log)
    return 0


def cmd_smoke(a) -> int:
    cfg = bconfig.load(Path(a.config))
    if cfg["mode"] != "smoke":
        print(f"refused: {a.config} is not a smoke config", file=sys.stderr)
        return 2
    bs = build_state()
    out = Path(a.out).resolve()
    (out / "logs").mkdir(parents=True, exist_ok=True)
    key = os.environ.get("BENCH_SMOKE_SECRET", "")  # the smoke's dummy secret: given to the judge, never written
    redact = bconfig.Redactor(key)
    log = logger(out, redact)
    dsn = os.environ.get("BIGLAW_DATABASE_URL") or None
    db = Postgres(dsn, cfg["postgres_image"], log, fresh=False) if dsn else None
    database = {"fresh": None, **(db.versions() if db else {"server": None, "vector": None}), "image": None}
    try:
        summary = run_suite(cfg, api=a.api, lab_root=lab_root_for(out, HERE / "smoke" / "tasks"), out=out, db=db, harness=HERE / "smoke",
                            key=key, redact=redact, log=log, build={**bs, "go": None},
                            harness_info={"name": cfg["harness"]["name"], "commit": None, "dirty": None, "pandoc": pandoc_version()},
                            database=database, yardstick=yardstick(), config_hash=cfg["config_hash"])
    finally:
        cancel_active(log)
    path = write_summary(summary, out)
    print_table(summary)
    print(f"summary: {path}")
    return 3 if summary["stopped"] and summary["stopped"]["reason"] == "provider_error" else 0


def cmd_check(a) -> int:
    bad = 0
    for p in (bconfig.CONFIG, bconfig.SMOKE_CONFIG):
        errs = bconfig.check(json.loads(p.read_text(encoding="utf-8")))
        print(f"{'FAIL' if errs else 'ok  '}  {p.relative_to(HERE)}")
        for e in errs:
            print(f"      - {e}")
        bad += bool(errs)
    for p in sorted((HERE / "baselines").glob("*.json")) + [Path(x) for x in a.summaries]:
        errs = check_summary(json.loads(Path(p).read_text(encoding="utf-8")))
        print(f"{'FAIL' if errs else 'ok  '}  {p}")
        for e in errs:
            print(f"      - {e}")
        bad += bool(errs)
    return 1 if bad else 0


def cmd_pin(a) -> int:
    print(f"{a.config}: {bconfig.pin(Path(a.config))}")
    print("a new hash: summaries under the old one are no longer comparable with runs under this one")
    return 0


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="BigLaw benchmark suite (docs/benchmarks.md)")
    sub = p.add_subparsers(dest="cmd", required=True)
    sp = sub.add_parser("run", help="run the pinned suite against the real model")
    sp.add_argument("--config", default=str(bconfig.CONFIG))
    sp.add_argument("--labs-dir", default="~/harvey-labs", help="harvey-labs checkout at the config's commit")
    sp.add_argument("--results-root", default=str(RESULTS))
    sp.add_argument("--allow-dirty", action="store_true", help="run an uncommitted checkout (recorded; compare reports it NOT COMPARABLE)")
    sp.set_defaults(fn=cmd_run)
    sp = sub.add_parser("preflight", help="start everything `run` starts, check /health, stop: no task, no model call")
    sp.add_argument("--config", default=str(bconfig.CONFIG))
    sp.add_argument("--labs-dir", help="also check the harvey-labs checkout")
    sp.add_argument("--results-root", default=str(RESULTS))
    sp.add_argument("--out", help="write logs here instead of a new results directory")
    sp.set_defaults(fn=cmd_preflight)
    sp = sub.add_parser("smoke", help="the offline pipeline smoke over a scripted API (eval/bench_test.go)")
    sp.add_argument("--config", default=str(bconfig.SMOKE_CONFIG))
    sp.add_argument("--api", required=True)
    sp.add_argument("--out", required=True)
    sp.set_defaults(fn=cmd_smoke)
    sp = sub.add_parser("check", help="validate the configs, the baselines and any summaries given")
    sp.add_argument("summaries", nargs="*")
    sp.set_defaults(fn=cmd_check)
    sp = sub.add_parser("pin", help="re-pin a config after a deliberate edit")
    sp.add_argument("--config", default=str(bconfig.CONFIG))
    sp.set_defaults(fn=cmd_pin)
    a = p.parse_args(argv)
    for sig in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, _exit_on_signal)
    return a.fn(a)


if __name__ == "__main__":
    sys.exit(main())
