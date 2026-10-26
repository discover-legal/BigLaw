# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Unit tests for the benchmark suite (eval/bench): config pinning and validation, the API key's
handling, the spend cap, the results schema, provider-refusal detection and compare.py. No backend,
no key, no model; eval/bench_test.go (TestBenchSmoke) drives the whole pipeline offline."""

from __future__ import annotations

import contextlib
import copy
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
from pathlib import Path

BENCH = Path(__file__).resolve().parent.parent / "bench"
sys.path.insert(0, str(BENCH))

from benchlib import collect, suite  # noqa: E402
from benchlib import config as bconfig  # noqa: E402
from benchlib import infra  # noqa: E402
from benchlib.infra import pg_env  # noqa: E402
from benchlib.judge import estimate  # noqa: E402
from benchlib.jsonschema import SchemaError, validate  # noqa: E402


def load_module(name: str, path: Path):
    spec = importlib.util.spec_from_file_location(name, path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


compare = load_module("bench_compare", BENCH / "compare.py")
benchrun = load_module("bench_run", BENCH / "run.py")


def real_config() -> dict:
    return json.loads(bconfig.CONFIG.read_text(encoding="utf-8"))


def repin(cfg: dict) -> dict:
    cfg["config_hash"] = bconfig.config_hash(cfg)
    return cfg


def baseline(name: str) -> dict:
    return json.loads((BENCH / "baselines" / name).read_text(encoding="utf-8"))


class ConfigTests(unittest.TestCase):
    def test_committed_configs_are_pinned_and_clean(self):
        for p in (bconfig.CONFIG, bconfig.SMOKE_CONFIG):
            self.assertEqual(bconfig.check(json.loads(p.read_text(encoding="utf-8"))), [], p)

    def test_pinned_settings(self):
        c = bconfig.load(bconfig.CONFIG)
        self.assertEqual(c["models"], {"lead": "claude-haiku-4-5", "specialist": "claude-haiku-4-5", "critic": "claude-haiku-4-5"})
        self.assertEqual(c["model_url"], "https://api.anthropic.com/v1")
        self.assertEqual((c["budget"]["usd"], c["budget"]["calls"], c["search"], c["spend_cap_usd"]), (11, 900, "lexical", 60))
        self.assertEqual((c["harness"]["commit"], c["judge"]["models"]), ("81d6a31", ["claude-sonnet-4-6"]))
        self.assertEqual([t["id"] for t in c["tasks"]], ["sec", "trust", "gate-a-litigation"])
        self.assertEqual(bconfig.models_env(c), "lead=claude-haiku-4-5,specialist=claude-haiku-4-5,critic=claude-haiku-4-5")
        self.assertEqual([(t["id"], t["judge_reserve_usd"]) for t in c["tasks"]], [("sec", 12), ("trust", 8), ("gate-a-litigation", 0)])

    def test_serve_env_is_the_config(self):
        """serve's whole environment (PR 63 r1 B1: without ALLOW_ANTHROPIC=1 serve refuses the endpoint)."""
        env = bconfig.serve_env(bconfig.load(bconfig.CONFIG), "postgres://u@h/db", "k", {"PGPASSWORD": "p"})
        self.assertEqual(env, {"BIGLAW_DATABASE_URL": "postgres://u@h/db", "BIGLAW_MODEL_URL": "https://api.anthropic.com/v1",
                               "BIGLAW_MODEL_KEY": "k", "BIGLAW_MODELS": "lead=claude-haiku-4-5,specialist=claude-haiku-4-5,critic=claude-haiku-4-5",
                               "BIGLAW_BUDGET_USD": "11", "BIGLAW_BUDGET_CALLS": "900", "PGPASSWORD": "p", "ALLOW_ANTHROPIC": "1"})

    def test_hand_edit_without_repin_is_refused(self):
        c = real_config()
        c["budget"]["usd"] = 12
        errs = bconfig.check(c)
        self.assertTrue(any("does not match the content" in e for e in errs), errs)
        self.assertEqual(bconfig.check(repin(c)), [])

    def test_hash_ignores_formatting_not_content(self):
        c = real_config()
        self.assertEqual(bconfig.config_hash(json.loads(json.dumps(c))), bconfig.config_hash(c))
        c2 = copy.deepcopy(c)
        c2["tasks"][0]["jurisdiction"] = "UK"
        self.assertNotEqual(bconfig.config_hash(c2), bconfig.config_hash(c))
        c3 = copy.deepcopy(c)
        c3["name"], c3["description"], c3["budget"]["usd"], c3["spend_cap_usd"] = "renamed", "reworded", 11.0, 60.0
        self.assertEqual(bconfig.config_hash(c3), bconfig.config_hash(c), "labels and 11 vs 11.0 must not change the hash")

    def test_real_mode_rules(self):
        cases = {
            "OpenAI": lambda c: c.__setitem__("model_url", "https://api.openai.com/v1"),
            "claude-": lambda c: c["models"].__setitem__("critic", "gpt-5"),
            "https": lambda c: c.__setitem__("model_url", "http://api.anthropic.com/v1"),
            "embed": lambda c: c["models"].__setitem__("embed", "claude-x"),
            "cannot fund": lambda c: c.__setitem__("spend_cap_usd", 5),
            "repeat": lambda c: c["tasks"].append(copy.deepcopy(c["tasks"][0])),
            "judged by harvey-labs": lambda c: c["judge"].__setitem__("runner", "stub"),
            "judge model 'gpt-5.5'": lambda c: c["judge"].__setitem__("models", ["gpt-5.5"]),
            "vendor_opt_in [] must be exactly ['ALLOW_ANTHROPIC']": lambda c: c.__setitem__("vendor_opt_in", []),
            "must be the matter's slug": lambda c: c["tasks"][2].__setitem__("matter", "LAB"),
            "has no judge": lambda c: c["tasks"][2].__setitem__("judge_reserve_usd", 1),
            "not a matter": lambda c: c["tasks"][2].__setitem__("task", "litigation/nope"),
        }
        for want, edit in cases.items():
            c = real_config()
            edit(c)
            errs = bconfig.check(repin(c))
            self.assertTrue(any(want in e for e in errs), f"{want}: {errs}")
        c = real_config()
        c["vendor_opt_in"] = ["ALLOW_AWS"]
        self.assertTrue(any("is not one of ['ALLOW_ANTHROPIC']" in e for e in bconfig.check(repin(c))))

    def test_schema_refuses_unknown_and_missing_keys(self):
        c = real_config()
        c["temperature"] = 0
        self.assertTrue(any("unexpected key 'temperature'" in e for e in bconfig.check(c)))
        c = real_config()
        del c["spend_cap_usd"]
        self.assertTrue(any("missing 'spend_cap_usd'" in e for e in bconfig.check(c)))

    def test_load_names_every_problem(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "c.json"
            c = real_config()
            c["model_url"] = "https://api.openai.com/v1"
            p.write_text(json.dumps(c))
            with self.assertRaises(bconfig.ConfigError) as cm:
                bconfig.load(p)
            self.assertIn("does not match", str(cm.exception))

    def test_reserve(self):
        c = real_config()
        self.assertEqual([bconfig.task_reserve(c, t) for t in c["tasks"]], [23.0, 19.0, 11.0])


class KeyTests(unittest.TestCase):
    KEY = "sk-ant-api03-not-a-real-key-0123456789"

    def test_from_env_and_file(self):
        self.assertEqual(bconfig.load_key({"ANTHROPIC_API_KEY": self.KEY}), self.KEY)
        with tempfile.NamedTemporaryFile("w", delete=False) as f:
            f.write(self.KEY + "\n")
        try:
            self.assertEqual(bconfig.load_key({"ANTHROPIC_API_KEY_FILE": f.name}), self.KEY)
        finally:
            os.unlink(f.name)

    def test_refusals_never_show_the_key(self):
        for env in ({}, {"ANTHROPIC_API_KEY": self.KEY, "ANTHROPIC_API_KEY_FILE": "/x"}, {"ANTHROPIC_API_KEY": self.KEY + " x"},
                    {"ANTHROPIC_API_KEY_FILE": "/no/such/key/file"}):
            with self.assertRaises(bconfig.ConfigError) as cm:
                bconfig.load_key(env)
            self.assertNotIn(self.KEY, str(cm.exception))

    def test_openai_settings_refused_by_name(self):
        errs = bconfig.refuse_env({"OPENAI_API_KEY": "sk-secret-value", "PATH": "/bin", "OPENAI_BASE_URL": "x"})
        self.assertEqual(len(errs), 1)
        self.assertIn("OPENAI_API_KEY", errs[0])
        self.assertIn("OPENAI_BASE_URL", errs[0])
        self.assertNotIn("sk-secret-value", errs[0])
        self.assertEqual(bconfig.refuse_env({"PATH": "/bin", "ALLOW_ANTHROPIC": "1"}), [])
        errs = bconfig.refuse_env({"ALLOW_AWS": "1"})
        self.assertEqual(len(errs), 1)
        self.assertIn("ALLOW_AWS", errs[0])

    def test_redactor(self):
        r = bconfig.Redactor(self.KEY, "")
        self.assertEqual(r(f"a {self.KEY} b {self.KEY}"), "a [redacted] b [redacted]")

    def run_cli(self, env: dict, *args: str) -> tuple[int, str]:
        err = io.StringIO()
        with unittest.mock.patch.dict(os.environ, env, clear=True), contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            code = benchrun.main(["run", *args])
        return code, err.getvalue()

    def test_run_refuses_before_starting_anything(self):
        path = {"PATH": os.environ.get("PATH", "")}
        code, err = self.run_cli({**path, "OPENAI_API_KEY": "sk-x", "ANTHROPIC_API_KEY": self.KEY})
        self.assertEqual(code, 2)
        self.assertIn("OPENAI_API_KEY", err)
        code, err = self.run_cli(path)
        self.assertEqual(code, 2)
        self.assertIn("no API key", err)
        with tempfile.TemporaryDirectory() as d:
            code, err = self.run_cli({**path, "ANTHROPIC_API_KEY": self.KEY}, "--labs-dir", d)
        self.assertEqual(code, 2)
        self.assertIn("not a harvey-labs checkout at 81d6a31", err)
        self.assertNotIn(self.KEY, err)
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "c.json"
            c = real_config()
            c["budget"]["calls"] = 901
            p.write_text(json.dumps(c))
            code, err = self.run_cli({**path, "ANTHROPIC_API_KEY": self.KEY}, "--config", str(p))
        self.assertEqual(code, 2)
        self.assertIn("re-pinning", err)

    def test_smoke_config_refused_by_run(self):
        code, err = self.run_cli({"PATH": os.environ.get("PATH", ""), "ANTHROPIC_API_KEY": self.KEY}, "--config", str(bconfig.SMOKE_CONFIG))
        self.assertEqual(code, 2)
        self.assertIn("smoke config", err)


class CapTests(unittest.TestCase):
    """run_suite's cap over a stand-in task runner that charges as run_task does (BigLaw's cost, its
    budget when unknown, then the judge's estimate): the order of charges and stops, not BigLaw."""

    def suite(self, cfg: dict, costs: dict, stops: dict | None = None, judge: dict | None = None) -> dict:
        ran = []
        judge = judge or {"sec": 9.7, "trust": 5.8}

        def fake_run_task(cfg, t, cap, *a, **k):
            ran.append(t["id"])
            rec = suite.empty_task(t, "scored")
            rec["cost_usd"] = costs[t["id"]]
            spent = costs[t["id"]] if costs[t["id"]] is not None else cfg["budget"]["usd"]
            cap.charge(spent)
            if t["kind"] == "lab":
                rec["judge_cost"] = {"usd": judge[t["id"]], "basis": "estimate", "criteria": 1, "input_tokens": 1, "output_tokens": 1, "deliverable_chars": 1}
                cap.charge(judge[t["id"]])
                spent += judge[t["id"]]
            rec["charged_usd"] = spent
            return rec, (stops or {}).get(t["id"])

        with unittest.mock.patch.object(suite, "run_task", fake_run_task), tempfile.TemporaryDirectory() as d:
            s = suite.run_suite(cfg, api="http://x", lab_root=Path(d), out=Path(d) / "20261026-abcdef0", db=None, harness=Path(d), key="",
                                redact=bconfig.Redactor(), log=lambda m: None,
                                build={"sha": "abcdef0", "dirty": False, "patch": None, "untracked": [], "go": None},
                                harness_info={"name": "harvey-labs", "commit": "81d6a31", "dirty": False, "pandoc": "3.1.3"},
                                database={"fresh": True, "server": "16", "vector": "0.8.6", "image": None},
                                yardstick=infra.yardstick(), config_hash=cfg["config_hash"])
        self.assertEqual(suite.check_summary(s), [])
        s["ran"] = ran
        return s

    def test_cap_admits_and_charges(self):
        cap = suite.Cap(40)
        self.assertTrue(cap.admits(40))
        cap.charge(27.5)
        self.assertTrue(cap.admits(12.5))
        self.assertFalse(cap.admits(12.51))

    def test_pinned_suite_fits_its_cap(self):
        """Bench6's BigLaw spend with the judge's estimates on bench6's deliverables: about $44 of the $60 cap."""
        s = self.suite(real_config(), {"sec": 10.27, "trust": 8.57, "gate-a-litigation": 9.6})
        self.assertEqual(s["ran"], ["sec", "trust", "gate-a-litigation"])
        self.assertIsNone(s["stopped"])
        self.assertAlmostEqual(s["spend"]["charged_usd"], 10.27 + 9.7 + 8.57 + 5.8 + 9.6)
        self.assertAlmostEqual(s["spend"]["biglaw_usd"], 10.27 + 8.57 + 9.6)
        self.assertAlmostEqual(s["spend"]["judge_usd_estimated"], 9.7 + 5.8)

    def test_worst_case_fits_the_cap(self):
        """Every task at its budget and every judge at its reserve still fits: the cap never stops the pinned suite."""
        c = real_config()
        s = self.suite(c, {"sec": 11, "trust": 11, "gate-a-litigation": 11}, judge={"sec": 12, "trust": 8})
        self.assertEqual(s["ran"], ["sec", "trust", "gate-a-litigation"])
        self.assertLessEqual(s["spend"]["charged_usd"], c["spend_cap_usd"])

    def test_stops_before_a_task_that_would_exceed(self):
        c = real_config()
        c["spend_cap_usd"] = 30
        s = self.suite(repin(c), {"sec": None, "trust": 1.0, "gate-a-litigation": 1.0})
        # sec reserves 23 (fits); its cost unknown: charged its $11 budget + the $9.70 estimate = 20.70; trust reserves 19 > 9.30 left
        self.assertEqual(s["ran"], ["sec"])
        self.assertEqual([t["status"] for t in s["tasks"]], ["scored", "skipped_cap", "not_run"])
        self.assertAlmostEqual(s["tasks"][0]["charged_usd"], 20.7)
        self.assertEqual(s["stopped"]["reason"], "spend_cap")
        self.assertIsNone(s["spend"]["biglaw_usd"])

    def test_provider_refusal_stops_at_once(self):
        s = self.suite(real_config(), {"sec": 0.01, "trust": 0, "gate-a-litigation": 0}, {"sec": "authentication_error (HTTP 401)"})
        self.assertEqual(s["ran"], ["sec"])
        self.assertEqual([t["status"] for t in s["tasks"]], ["scored", "not_run", "not_run"])
        self.assertEqual(s["stopped"]["provider_error_type"], "authentication_error (HTTP 401)")


class JudgeEstimateTests(unittest.TestCase):
    def test_estimate(self):
        cfg = real_config()
        with tempfile.TemporaryDirectory() as d:
            out = Path(d) / "output"
            out.mkdir()
            (out / "memo.md").write_text("x" * 3500)
            (out / "table.md").write_text("y" * 700)
            crits = [{"id": "C-001", "deliverables": ["memo.md"]}, {"id": "C-002"}]
            task = {"instructions": "i" * 100, "criteria": crits}
            e = estimate(cfg, task, out, Path(d))  # no harness template there: 2000 chars assumed
            per = 2000 + 100
            chars = (per + len(json.dumps(crits[0])) + 3500) + (per + len(json.dumps(crits[1])) + 4200)
            tin = -(-chars * 2 // 7)  # ceil(chars / 3.5)
            self.assertEqual((e["input_tokens"], e["output_tokens"], e["criteria"], e["basis"]), (tin, 2000, 2, "estimate"))
            self.assertAlmostEqual(e["usd"], round(tin * 3 / 1e6 + 2000 * 15 / 1e6, 4))

    def test_bench6_sized_deliverables(self):
        """SEC's 170k-character deliverable over 60 criteria and trust's 280k over 23, at the pinned price:
        what docs/benchmarks.md states (about $9.7 and $5.8), under the reserves."""
        cfg = real_config()
        harvey_template = 531
        for chars, n, reserve, want in ((169599, 60, 12, 9.7), (279800, 23, 8, 5.8)):
            with tempfile.TemporaryDirectory() as d:
                out = Path(d) / "output"
                out.mkdir()
                (out / "d.md").write_text("x" * chars)
                (Path(d) / "lab_core/evaluation/prompts").mkdir(parents=True)
                (Path(d) / "lab_core/evaluation/prompts/rubric_criterion.txt").write_text("t" * harvey_template)
                crit = {"id": "C-000", "title": "t" * 60, "deliverables": ["d.md"], "match_criteria": "m" * 400}
                e = estimate(cfg, {"instructions": "i" * 2500, "criteria": [crit] * n}, out, Path(d))
                self.assertAlmostEqual(e["usd"], want, delta=0.3)
                self.assertLess(e["usd"], reserve)


class BuildStateTests(unittest.TestCase):
    def repo(self, d: str) -> Path:
        r = Path(d)
        run = lambda *a: subprocess.run(["git", "-C", d, *a], check=True, capture_output=True)  # noqa: E731
        run("init", "-q")
        (r / "internal").mkdir()
        (r / "internal/a.go").write_text("package a\n")
        (r / "docs").mkdir()
        run("add", ".")
        run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "c")
        return r

    def test_untracked_build_files_are_dirty(self):
        """PR 63 r1 H2: go build compiles an untracked .go file, so it must not pass as clean."""
        with tempfile.TemporaryDirectory() as d:
            r = self.repo(d)
            self.assertFalse(infra.build_state(r)["dirty"])
            (r / "docs/notes.md").write_text("n")
            self.assertFalse(infra.build_state(r)["dirty"], "an untracked file outside the build paths")
            (r / "internal/zz_probe.go").write_text("package a\n")
            st = infra.build_state(r)
            self.assertTrue(st["dirty"])
            self.assertEqual(st["untracked"], ["internal/zz_probe.go"])
            self.assertRegex(st["patch"], r"^sha256:[0-9a-f]{64}$")
            (r / "internal/zz_probe.go").write_text("package a // changed\n")
            self.assertNotEqual(infra.build_state(r)["patch"], st["patch"])
            (r / "internal/z ü.go").write_text("package a\n")  # a name git would quote without -z
            self.assertIn("internal/z ü.go", infra.build_state(r)["untracked"])

    def test_tree_hash(self):
        with tempfile.TemporaryDirectory() as d:
            r = Path(d)
            (r / "p").mkdir()
            (r / "p/x.py").write_text("1")
            h = infra.tree_hash(("p",), r)
            (r / "p/__pycache__").mkdir()
            (r / "p/__pycache__/x.cpython.pyc").write_text("junk")
            self.assertEqual(infra.tree_hash(("p",), r), h)
            (r / "p/x.py").write_text("2")
            self.assertNotEqual(infra.tree_hash(("p",), r), h)
        self.assertEqual(set(infra.yardstick()), {"proofrun", "labdriver", "bench"})


class PostgresCleanupTests(unittest.TestCase):
    def test_container_removed_when_it_never_becomes_ready(self):
        """PR 63 r1 M1: a container started is removed whatever stops the start (here: never ready)."""
        calls = []

        def fake_run(cmd, **k):
            calls.append(cmd)
            return subprocess.CompletedProcess(cmd, 1 if "pg_isready" in cmd else 0, "", "")

        with unittest.mock.patch.object(infra.shutil, "which", lambda name: "/usr/bin/" + name), \
                unittest.mock.patch.object(infra.subprocess, "run", fake_run), unittest.mock.patch.object(infra.time, "sleep", lambda s: None):
            with self.assertRaises(infra.InfraError):
                infra.Postgres(None, "img", lambda m: None)
        self.assertEqual(calls[-1][:3], ["docker", "rm", "-f"])


HELPER = """
import signal, sys
from pathlib import Path
sys.path.insert(0, sys.argv[1])
import importlib.util
spec = importlib.util.spec_from_file_location("bench_run", Path(sys.argv[1]) / "run.py")
run = importlib.util.module_from_spec(spec); spec.loader.exec_module(run)
from benchlib import config, judge
signal.signal(signal.SIGTERM, run._exit_on_signal)
cfg = config.load(config.SMOKE_CONFIG)
lab = Path(sys.argv[2])
judge.run_judge(cfg, Path(sys.argv[1]) / "smoke", lab, "r", "smoke/deadline", "", lab / "judge.log", config.Redactor())
"""


def alive(pid: int) -> bool:
    try:
        state = Path(f"/proc/{pid}/status").read_text()
    except OSError:
        return False
    return "\nState:\tZ" not in state


class SignalTests(unittest.TestCase):
    @unittest.skipUnless(Path("/proc").is_dir(), "needs /proc")
    def test_sigterm_during_judge_leaves_no_judge(self):
        """PR 63 r2 MEDIUM-1: a SIGTERM while the judge runs stops the judge and every process it started
        (the stub judge holds a child, as `uv run` holds python), not only the process the suite started."""
        import time
        with tempfile.TemporaryDirectory() as d:
            lab = Path(d)
            (lab / "tasks").symlink_to(BENCH / "smoke" / "tasks")
            (lab / "results" / "r" / "output").mkdir(parents=True)
            hold = lab / "stubjudge.hold"
            hold.write_text("")
            (lab / "helper.py").write_text(HELPER)
            p = subprocess.Popen([sys.executable, str(lab / "helper.py"), str(BENCH), str(lab)])
            deadline = time.monotonic() + 30
            while not hold.read_text().strip() and time.monotonic() < deadline:
                time.sleep(0.1)
            pids = [int(x) for x in hold.read_text().split()]
            self.assertEqual(len(pids), 2, "the stub judge did not start")
            p.send_signal(subprocess.signal.SIGTERM)
            self.assertEqual(p.wait(30), 128 + 15)
            deadline = time.monotonic() + 15
            while any(alive(x) for x in pids) and time.monotonic() < deadline:
                time.sleep(0.1)
            self.assertEqual([x for x in pids if alive(x)], [], "a judge process survived the stop")

    def test_cleanup_runs_every_step_and_ignores_signals(self):
        """PR 63 r2 MEDIUM-2: a second signal, or a failing step, cannot cut cleanup short."""
        import signal
        seen = []

        class Item:
            def __init__(self, name, fail=None):
                self.name, self.fail = name, fail

            def close(self):
                seen.append((self.name, signal.getsignal(signal.SIGTERM) == signal.SIG_IGN))
                os.kill(os.getpid(), signal.SIGTERM)  # a second stop request mid-cleanup: ignored
                if self.fail:
                    raise self.fail

        before = signal.getsignal(signal.SIGTERM)
        logged = []
        with unittest.mock.patch.object(benchrun, "cancel_active", unittest.mock.Mock(side_effect=KeyboardInterrupt)):
            benchrun.stop_all([Item("db", SystemExit(143)), Item("serve", KeyboardInterrupt())], logged.append)
        self.assertEqual(seen, [("serve", True), ("db", True)])
        self.assertEqual(signal.getsignal(signal.SIGTERM), before)
        self.assertEqual(len(logged), 3, logged)


class SchemaTests(unittest.TestCase):
    def test_baselines_validate(self):
        for p in sorted((BENCH / "baselines").glob("*.json")):
            self.assertEqual(suite.check_summary(json.loads(p.read_text())), [], p)

    def test_baselines_hold_only_recorded_numbers(self):
        v1 = {t["id"]: t for t in baseline("v1-record.json")["tasks"]}
        self.assertEqual([(t["score"]["passed"], t["score"]["total"], t["cost_usd"], t["duration_s"]) for t in v1.values()],
                         [(50, 60, 11.52, 5516.6), (14, 23, 10.52, 4529.0)])
        for t in v1.values():
            self.assertEqual(sum(v == "pass" for v in t["criteria"].values()), t["score"]["passed"])
            self.assertTrue(t["calls"] is None and t["stop_reason"] is None)
        b6 = baseline("v2-bench6.json")
        self.assertEqual(b6["build"]["sha"], "091bcfc")
        t6 = {t["id"]: t for t in b6["tasks"]}
        self.assertEqual([(t["score"]["passed"], t["score"]["total"], t["cost_usd"], t["calls"], t["stop_reason"]) for t in t6.values()],
                         [(38, 60, 10.27, 566, "budget_exhausted"), (11, 23, 8.57, 443, "finished"), (20, 20, 9.6, 444, "budget_exhausted")])
        for tid in ("sec", "trust"):
            c = t6[tid]["criteria"]
            self.assertEqual(sum(v == "pass" for v in c.values()), t6[tid]["score"]["passed"])
        self.assertEqual(t6["sec"]["flips"]["gained"], ["C-002", "C-044", "C-047", "C-056"])
        self.assertEqual(t6["trust"]["flips"]["lost"], ["C-010", "C-011", "C-016"])
        for f in ("reader", "judge", "conflicts", "stage_spend"):
            self.assertIsNone(t6["sec"][f], f)

    def test_violations_reported(self):
        s = baseline("v2-bench6.json")
        s["tasks"][0]["score"]["passed"] = "38"
        s["tasks"][1]["oops"] = 1
        del s["runner"]
        s["tasks"][2]["stop_reason"] = "done"
        errs = suite.check_summary(s)
        for want in ("$.tasks[0].score.passed: expected integer", "unexpected key 'oops'", "missing 'runner'", "'done' is not one of"):
            self.assertTrue(any(want in e for e in errs), (want, errs))

    def test_validator_subset(self):
        self.assertEqual(validate(True, {"type": "integer"}), ["$: expected integer, got bool"])
        self.assertEqual(validate("a1", {"type": "string", "pattern": "^[a-z]$"}), ["$: 'a1' does not match ^[a-z]$"])
        with self.assertRaises(SchemaError):
            validate(1, {"oneOf": []})

    def test_write_summary_refuses_an_invalid_one(self):
        s = baseline("v1-record.json")
        s["kind"] = "guess"
        with tempfile.TemporaryDirectory() as d, self.assertRaises(ValueError):
            suite.write_summary(s, Path(d))


class CollectTests(unittest.TestCase):
    def test_fatal_provider_errors(self):
        cases = {
            "model not configured: HTTP 401 (authentication_error: invalid x-api-key): check BIGLAW_MODEL_KEY": "authentication_error (HTTP 401)",
            "model call failed: HTTP 400 (invalid_request_error: Your credit balance is too low to access the Anthropic API.)":
                "invalid_request_error (HTTP 400): credit balance too low",
            "anthropic.AuthenticationError: Error code: 401 - {'type': 'error', 'error': {'type': 'authentication_error', 'message': 'invalid x-api-key'}}":
                "authentication_error (HTTP 401)",
        }
        for text, want in cases.items():
            self.assertEqual(collect.fatal_provider_error(text), want, text)
        for benign in ("model provider rate limited: HTTP 429 (rate_limit_error: slow down)", "model call failed: HTTP 529 (overloaded_error)", None, ""):
            self.assertIsNone(collect.fatal_provider_error(benign))
        self.assertNotIn("x-api-key", collect.fatal_provider_error(list(cases)[0]))

    def test_computed_counts(self):
        memo = "## Limitations\n### Computed figures\nCheck first\n- a\n- b\nArithmetic over cited values\n- c\n## Sources\n- [F1] x\n"
        self.assertEqual(collect.computed_counts(memo), {"check_first": 2, "arithmetic": 1})
        self.assertIsNone(collect.computed_counts(None))

    def test_stage_spend_and_calls(self):
        cost = {"entries": [{"tool": "lead", "usd": 0.5}, {"tool": "sweep", "usd": 1.25}, {"tool": "lead", "usd": 0.25}, {"tool": "finish", "usd": None}, {"tool": "finish", "usd": 1}]}
        self.assertEqual(collect.stage_spend(cost), {"lead": 0.75, "sweep": 1.25, "finish": None})
        self.assertEqual(collect.tool_calls_delta({"sweep": 10.0}, {"sweep": 14.0, "judge_figures": 3.0, "lead": 0.0}), {"sweep": 4, "judge_figures": 3})
        self.assertIsNone(collect.tool_calls_delta(None, {}))

    def test_pg_env(self):
        e = pg_env("postgres://u:p%40ss@db:5433/x?sslmode=disable&search_path=s1")
        self.assertEqual(e, {"PGHOST": "db", "PGPORT": "5433", "PGDATABASE": "x", "PGUSER": "u", "PGPASSWORD": "p@ss",
                             "PGSSLMODE": "disable", "PGOPTIONS": "-csearch_path=s1"})


class CompareTests(unittest.TestCase):
    def run_summary(self, criteria: dict, cost: float, cfg: dict | None = None) -> dict:
        cfg = cfg or real_config()
        s = copy.deepcopy(baseline("v2-bench6.json"))
        s.update(kind="run", mode="real", label=f"run-{cost}", source=None, config_hash=cfg["config_hash"],
                 config={k: v for k, v in cfg.items() if k != "config_hash"}, fresh_db=True,
                 reconstructed_config=None, reconstructed_config_hash=None, provenance=None, yardstick=infra.yardstick(),
                 database={"fresh": True, "server": "16.15", "vector": "0.8.6", "image": None}, models_served=["claude-haiku-4-5-20251001"],
                 harness={"name": "harvey-labs", "commit": "81d6a31", "dirty": False, "pandoc": "3.1.3"},
                 build={"sha": f"{int(cost * 100):07x}", "dirty": False, "patch": None, "untracked": [], "go": "go1.26.10"})
        t = s["tasks"][0]
        t.update(criteria=criteria, score={"passed": sum(v == "pass" for v in criteria.values()), "total": len(criteria)}, cost_usd=cost, calls=500, flips=None)
        self.assertEqual(suite.check_summary(s), [])
        return s

    def test_deltas_and_flips(self):
        a = self.run_summary({"C-001": "pass", "C-002": "fail", "C-003": "pass", "C-004": "error"}, 10.0)
        b = self.run_summary({"C-001": "fail", "C-002": "pass", "C-003": "pass", "C-004": "pass"}, 9.0)
        c = compare.compare(a, b)
        self.assertTrue(c["comparable"], c)
        row = c["tasks"][0]
        self.assertEqual(row["flips"], {"gained": ["C-002"], "lost": ["C-001"], "other": ["C-004"]})
        self.assertEqual(row["score_delta"], 1)
        text = compare.render(c)
        self.assertIn("cost $10.00 → $9.00 (-1.00)", text)
        self.assertIn("gained (1): C-002", text)
        self.assertIn("lost   (1): C-001", text)
        self.assertNotIn("NOT COMPARABLE", text)

    def test_different_configs_not_comparable(self):
        a = self.run_summary({"C-001": "pass"}, 1.0)
        cfg = real_config()
        cfg["budget"]["usd"] = 15
        cfg["models"]["critic"] = "claude-sonnet-4-6"
        b = self.run_summary({"C-001": "pass"}, 2.0, repin(cfg))
        c = compare.compare(a, b)
        self.assertFalse(c["comparable"])
        self.assertTrue(any("config hashes differ" in w for w in c["not_comparable_because"]))
        self.assertEqual(c["config_diff"], ['budget.usd: 11 → 15', 'models.critic: "claude-haiku-4-5" → "claude-sonnet-4-6"'])
        self.assertIn("NOT COMPARABLE", compare.render(c))

    def test_unpinned_judge_or_db_not_comparable(self):
        a = self.run_summary({"C-001": "pass"}, 1.0)
        for edit, want in ((lambda s: s.__setitem__("config_hash", None), "no config hash"),
                           (lambda s: s["judge"].__setitem__("models", ["claude-opus-4-8"]), "judges differ"),
                           (lambda s: s.__setitem__("fresh_db", False), "already held data"),
                           (lambda s: s["build"].__setitem__("dirty", True), "differs from its commit"),
                           (lambda s: s["yardstick"].__setitem__("labdriver", "sha256:" + "0" * 64), "LAB driver"),
                           (lambda s: s["yardstick"].__setitem__("bench", "sha256:" + "0" * 64), "bench harness"),
                           (lambda s: s.__setitem__("mode", "smoke"), "modes differ"),
                           (lambda s: s["harness"].__setitem__("commit", "1234567"), "harness commits differ")):
            b = copy.deepcopy(a)
            b["label"] = "b"
            edit(b)
            c = compare.compare(a, b)
            self.assertFalse(c["comparable"], want)
            self.assertTrue(any(want in w for w in c["not_comparable_because"]), (want, c["not_comparable_because"]))

    def test_proofrun_yardstick_only_its_tasks(self):
        """PR 63 r1 H3: a changed proof-run scorer or rubric makes gate A not comparable, the LAB tasks still are."""
        a = self.run_summary({"C-001": "pass"}, 1.0)
        b = copy.deepcopy(a)
        b["label"] = "b"
        b["yardstick"]["proofrun"] = "sha256:" + "1" * 64
        c = compare.compare(a, b)
        rows = {r["id"]: r for r in c["tasks"]}
        self.assertEqual(c["state"], "comparable")
        self.assertFalse(c["comparable"])
        self.assertEqual(rows["sec"]["not_comparable_because"], [])
        self.assertIn("proof-run scorer", rows["gate-a-litigation"]["not_comparable_because"][0])
        self.assertIn("gate-a-litigation: NOT COMPARABLE", compare.render(c))
        self.assertTrue(compare.compare(a, a)["comparable"])

    def test_reconstructed_baseline_with_caveats(self):
        """bench6's reconstructed config hashes to the pinned one: COMPARABLE WITH CAVEATS, never plain comparable."""
        b6 = baseline("v2-bench6.json")
        self.assertIsNone(b6["config_hash"])
        self.assertEqual(b6["reconstructed_config_hash"], bconfig.config_hash(b6["reconstructed_config"]))
        self.assertEqual(b6["reconstructed_config_hash"], real_config()["config_hash"])
        run = self.run_summary({"C-002": "pass", "C-015": "pass"}, 9.0)
        c = compare.compare(b6, run)
        self.assertEqual(c["state"], "caveats")
        self.assertFalse(c["comparable"])
        self.assertTrue(c["comparable_with_caveats"])
        text = compare.render(c)
        self.assertIn("COMPARABLE WITH CAVEATS (reconstructed baseline)", text)
        for key in ("budget.usd (inferred)", "models.lead (inferred)", "fresh_db (unknown)", "tool_stats (unknown)",
                    "tasks[gate-a-litigation].task (unknown)"):
            self.assertIn(key, c["caveats"][0])
        self.assertNotIn("budget.calls", c["caveats"][0], "verified keys are not caveats")
        prov = b6["provenance"]
        self.assertTrue(prov["budget.calls"].startswith("verified"))
        self.assertTrue(prov["harness.commit"].startswith("verified"))
        self.assertIn("20261024-053155", b6["source"])
        cfg = real_config()
        cfg["budget"]["calls"] = 901
        c2 = compare.compare(b6, self.run_summary({"C-001": "pass"}, 2.0, repin(cfg)))
        self.assertEqual(c2["state"], "not_comparable")

    def test_baselines_compare_as_recorded(self):
        c = compare.compare(baseline("v1-record.json"), baseline("v2-bench6.json"))
        self.assertFalse(c["comparable"])
        rows = {r["id"]: r for r in c["tasks"]}
        self.assertEqual(rows["sec"]["score_delta"], -12)
        self.assertEqual((rows["sec"]["flips"]["gained"], len(rows["sec"]["flips"]["lost"])), (["C-033", "C-045"], 14))
        self.assertEqual(rows["gate-a-litigation"]["only_in"], "v2-bench6")

    def test_cli_exit_codes(self):
        b1, b6 = str(BENCH / "baselines" / "v1-record.json"), str(BENCH / "baselines" / "v2-bench6.json")
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(compare.main([b1, b6]), 0)
            self.assertEqual(compare.main(["--require-comparable", b1, b6]), 1)
            self.assertEqual(compare.main(["--json", b1, b6]), 0)
        self.assertIn('"comparable": false', out.getvalue())


if __name__ == "__main__":
    unittest.main()
