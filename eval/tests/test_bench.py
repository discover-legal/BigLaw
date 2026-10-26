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
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

BENCH = Path(__file__).resolve().parent.parent / "bench"
sys.path.insert(0, str(BENCH))

from benchlib import collect, suite  # noqa: E402
from benchlib import config as bconfig  # noqa: E402
from benchlib.infra import pg_env  # noqa: E402
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
        self.assertEqual((c["budget"]["usd"], c["budget"]["calls"], c["search"], c["spend_cap_usd"]), (11, 900, "lexical", 40))
        self.assertEqual((c["harness"]["commit"], c["judge"]["models"]), ("81d6a31", ["claude-sonnet-4-6"]))
        self.assertEqual([t["id"] for t in c["tasks"]], ["sec", "trust", "gate-a-litigation"])
        self.assertEqual(bconfig.models_env(c), "lead=claude-haiku-4-5,specialist=claude-haiku-4-5,critic=claude-haiku-4-5")

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

    def test_real_mode_rules(self):
        cases = {
            "OpenAI": lambda c: c.__setitem__("model_url", "https://api.openai.com/v1"),
            "claude-": lambda c: c["models"].__setitem__("critic", "gpt-5"),
            "https": lambda c: c.__setitem__("model_url", "http://api.anthropic.com/v1"),
            "embed": lambda c: c["models"].__setitem__("embed", "claude-x"),
            "cannot fund": lambda c: c.__setitem__("spend_cap_usd", 5),
            "repeat": lambda c: c["tasks"].append(copy.deepcopy(c["tasks"][0])),
            "judged by harvey-labs": lambda c: c["judge"].__setitem__("runner", "stub"),
        }
        for want, edit in cases.items():
            c = real_config()
            edit(c)
            errs = bconfig.check(repin(c))
            self.assertTrue(any(want in e for e in errs), f"{want}: {errs}")

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
        self.assertEqual([bconfig.task_reserve(c, t) for t in c["tasks"]], [13.0, 13.0, 11.0])


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
        self.assertEqual(bconfig.refuse_env({"PATH": "/bin"}), [])

    def test_redactor(self):
        r = bconfig.Redactor(self.KEY, "")
        self.assertEqual(r(f"a {self.KEY} b {self.KEY}"), "a [redacted] b [redacted]")

    def run_cli(self, env: dict, *args: str) -> tuple[int, str]:
        err = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=True), contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
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
    """run_suite's cap over a stand-in task runner: the order of charges and stops, not BigLaw."""

    def suite(self, cfg: dict, costs: dict, stops: dict | None = None) -> dict:
        ran = []

        def fake_run_task(cfg, t, *a, **k):
            ran.append(t["id"])
            rec = suite.empty_task(t, "scored")
            rec["cost_usd"] = costs[t["id"]]
            if t["kind"] == "lab":
                rec["judged"] = True
            return rec, (stops or {}).get(t["id"])

        with mock.patch.object(suite, "run_task", fake_run_task), tempfile.TemporaryDirectory() as d:
            s = suite.run_suite(cfg, api="http://x", lab_root=Path(d), out=Path(d) / "20261026-abcdef0", db=None, harness=Path(d), key="",
                                redact=bconfig.Redactor(), log=lambda m: None, build={"sha": "abcdef0", "dirty": False},
                                harness_info={"name": "harvey-labs", "commit": "81d6a31", "dirty": False}, fresh_db=True,
                                config_hash=cfg["config_hash"])
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
        s = self.suite(real_config(), {"sec": 10.27, "trust": 8.57, "gate-a-litigation": 9.6})
        self.assertEqual(s["ran"], ["sec", "trust", "gate-a-litigation"])
        self.assertIsNone(s["stopped"])
        self.assertAlmostEqual(s["spend"]["charged_usd"], 10.27 + 2 + 8.57 + 2 + 9.6)
        self.assertAlmostEqual(s["spend"]["actual_usd"], 10.27 + 8.57 + 9.6)

    def test_stops_before_a_task_that_would_exceed(self):
        c = real_config()
        c["spend_cap_usd"] = 30
        s = self.suite(repin(c), {"sec": 10.0, "trust": None, "gate-a-litigation": 1.0})
        # sec: 10 + 2 judge = 12; trust's cost unknown: charged its $11 budget + 2 = 25; litigation reserves 11 > 5 left
        self.assertEqual(s["ran"], ["sec", "trust"])
        self.assertEqual([t["status"] for t in s["tasks"]], ["scored", "scored", "skipped_cap"])
        self.assertEqual(s["tasks"][1]["charged_usd"], 13.0)
        self.assertEqual(s["stopped"]["reason"], "spend_cap")
        self.assertIsNone(s["spend"]["actual_usd"])

    def test_provider_refusal_stops_at_once(self):
        s = self.suite(real_config(), {"sec": 0.01, "trust": 0, "gate-a-litigation": 0}, {"sec": "authentication_error (HTTP 401)"})
        self.assertEqual(s["ran"], ["sec"])
        self.assertEqual([t["status"] for t in s["tasks"]], ["scored", "not_run", "not_run"])
        self.assertEqual(s["stopped"]["provider_error_type"], "authentication_error (HTTP 401)")


class SchemaTests(unittest.TestCase):
    def test_baselines_validate(self):
        for p in sorted((BENCH / "baselines").glob("*.json")):
            self.assertEqual(suite.check_summary(json.loads(p.read_text())), [], p)

    def test_baselines_hold_only_recorded_numbers(self):
        v1 = {t["id"]: t for t in baseline("v1-record.json")["tasks"]}
        self.assertEqual((v1["sec"]["score"], v1["trust"]["score"]), ({"passed": 49, "total": 60}, {"passed": 14, "total": 23}))
        self.assertTrue(all(t["criteria"] is None and t["cost_usd"] is None for t in v1.values()))
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
                 harness={"name": "harvey-labs", "commit": "81d6a31", "dirty": False}, build={"sha": f"{int(cost * 100):07x}", "dirty": False})
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
                           (lambda s: s.__setitem__("fresh_db", False), "already held tasks"),
                           (lambda s: s.__setitem__("mode", "smoke"), "modes differ"),
                           (lambda s: s["harness"].__setitem__("commit", "1234567"), "harness commits differ")):
            b = copy.deepcopy(a)
            b["label"] = "b"
            edit(b)
            c = compare.compare(a, b)
            self.assertFalse(c["comparable"], want)
            self.assertTrue(any(want in w for w in c["not_comparable_because"]), (want, c["not_comparable_because"]))

    def test_baselines_compare_as_recorded(self):
        c = compare.compare(baseline("v1-record.json"), baseline("v2-bench6.json"))
        self.assertFalse(c["comparable"])
        rows = {r["id"]: r for r in c["tasks"]}
        self.assertEqual(rows["sec"]["score_delta"], -11)
        self.assertIsNone(rows["sec"]["flips"])
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
