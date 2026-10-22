# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Unit tests for the proof-run harness (no backend, no model credentials).

    python3 -m unittest discover -s eval/tests -v
"""

from __future__ import annotations

import copy
import json
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(HERE))

import run as cli  # noqa: E402
from proofrun.matter import load_all, load_matter  # noqa: E402
from proofrun.patterns import pattern_matches, split_units  # noqa: E402
from proofrun.score import Corpus, score_run  # noqa: E402
from proofrun.validate import validate_matter  # noqa: E402


class PatternTests(unittest.TestCase):
    def m(self, alt: str, text: str) -> bool:
        return pattern_matches({"all": [[alt]]}, text)

    def test_money_renderings(self):
        for t in ("$9,000,000", "$9 million", "$9.0 million", "$9M", "USD 9m", "9,000,000"):
            self.assertTrue(self.m("$money:9000000", t), t)
        for t in ("$228.5 million", "$228,500,000", "228.50 million"):
            self.assertTrue(self.m("$money:228500000", t), t)
        self.assertTrue(self.m("$money:770000", "a payment of $770K"))
        self.assertFalse(self.m("$money:9000000", "$19,000,000"))
        self.assertFalse(self.m("$money:9000000", "$9,000,0001"))

    def test_date_renderings(self):
        for t in ("November 12, 2026", "Thursday, Nov. 12, 2026", "12 November 2026", "2026-11-12", "11/12/2026"):
            self.assertTrue(self.m("$date:2026-11-12", t), t)
        self.assertFalse(self.m("$date:2026-11-12", "November 11, 2026"))
        self.assertFalse(self.m("$date:2026-11-12", "November 12, 2025"))

    def test_percent_and_number(self):
        self.assertTrue(self.m("$pct:1", "reduce interest to 1.0% per month"))
        self.assertFalse(self.m("$pct:1", "interest of 11% or 1.5%"))
        self.assertTrue(self.m("$pct:60", "sixty percent (60%)"))
        self.assertTrue(self.m("$num:6412500", "6,412,500 shares"))
        self.assertFalse(self.m("$num:6412500", "16,412,500 shares"))

    def test_units(self):
        text = "# Deadlines\n\n| Deadline | Date |\n|---|---|\n| Answer | Nov. 12, 2026 |\n\n- first\n  continued\n- second\n\nPara one\nstill one.\n\nPara two."
        units = split_units(text)
        self.assertIn("Deadlines — | Answer | Nov. 12, 2026 |", units)
        self.assertIn("Deadlines — - first continued", units)
        self.assertIn("Deadlines — Para one still one.", units)
        self.assertEqual(len(units), 6)  # header row, data row, 2 bullets, 2 paras


class FixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.matters = load_all()

    def test_registry_has_five_practice_areas(self):
        self.assertEqual(len(self.matters), 5)
        self.assertEqual(len({m.entry["practice"] for m in self.matters}), 5)

    def test_fixtures_validate(self):
        for m in self.matters:
            self.assertEqual(validate_matter(m), [], m.id)

    def test_validator_catches_broken_key(self):
        m = copy.deepcopy(load_matter("ma-diligence"))
        m.key["facts"][0]["evidence"] = ["this sentence appears in no document at all"]
        m.key["facts"][1]["model_answer"] = "nothing relevant"
        m.key["facts"][2]["detect"] = {"all": [["Halberd"]]}  # leaks from the instructions
        errs = "\n".join(validate_matter(m))
        self.assertIn("evidence not found verbatim", errs)
        self.assertIn("does not satisfy its own detect pattern", errs)
        self.assertIn("satisfied by the task instructions", errs)

    def test_selftest_profiles(self):
        with tempfile.TemporaryDirectory() as td:
            self.assertEqual(cli.selftest(self.matters, Path(td)), [])


class NaturalLanguageTests(unittest.TestCase):
    """Hand-written, model-style phrasings (not the key's model_answer) must score."""

    def run_text(self, matter: str, text: str, findings=None) -> dict:
        m = load_matter(matter)
        with tempfile.TemporaryDirectory() as td:
            rd = Path(td)
            (rd / "output").mkdir()
            (rd / "output" / m.task["deliverables"][0]).write_text(text, encoding="utf-8")
            (rd / "biglaw_tasks.json").write_text(json.dumps([{"findings": findings or []}]), encoding="utf-8")
            s = score_run(m, rd, write=False)
        return {r["id"]: r for r in s["criteria_results"]} | {"_scores": s}

    def test_litigation_deadline_table(self):
        text = ("## Deadlines\n\n| Deadline | Trigger | Rule | Raw date | Adjusted |\n|---|---|---|---|---|\n"
                "| Answer / Rule 12 motion | Service 10/21/2026 | FRCP 12(a)(1)(A)(i), 6(a)(1)(C) | Nov. 11, 2026 (Veterans Day) | **Thursday, Nov. 12, 2026** |\n"
                "| Joint initial status report | Hearing 12/3/2026 | Dkt. 4; FRCP 6(a)(5) | Nov. 26 (Thanksgiving) | **Wed., November 25, 2026** |\n")
        r = self.run_text("litigation", text)
        self.assertEqual(r["LIT-D1"]["verdict"], "pass")
        self.assertEqual(r["LIT-D2"]["verdict"], "pass")
        self.assertEqual(r["TRAP-LIT-02"]["verdict"], "pass")  # Nov 11 named only with the holiday roll

    def test_litigation_trap_trips(self):
        r = self.run_text("litigation", "Talbot must file its answer by December 11, 2026.")
        self.assertEqual(r["TRAP-LIT-01"]["verdict"], "fail")
        r = self.run_text("litigation", "Talbot's answer would have been due December 11, 2026 only if it had waived service; it did not sign the waiver.")
        self.assertEqual(r["TRAP-LIT-01"]["verdict"], "pass")

    def test_negotiation_table(self):
        text = ("| Change | Clause | Disposition | Counter-position | Ref |\n|---|---|---|---|---|\n"
                "| C-01 | 4.1 Payment | ACCEPT | Net 45 within fallback | P-1 |\n"
                "| C-03 | 10.1 Cap | COUNTER | Cap at 24 months' Fees; 36 months is not acceptable | P-3 |\n"
                "| C-05 | 4.4 MFN | REJECT | Strike | P-5 |\n")
        r = self.run_text("msa-negotiation", text)
        self.assertEqual(r["C-01"]["got"], "ACCEPT")
        self.assertEqual(r["C-03"]["got"], "COUNTER")
        self.assertEqual(r["C-03/position"]["verdict"], "pass")
        self.assertEqual(r["C-05"]["got"], "REJECT")
        self.assertEqual(r["C-02"]["verdict"], "fail")  # not addressed

    def test_grounding_reverification(self):
        m = load_matter("merger-control")
        corpus = Corpus(m.documents, {"doc-9": "02-financial-and-turnover-data.csv"})
        ok = "Sorell Diagnostics B.V.,Sales in or into the United States,United States,138000000,USD"
        self.assertEqual(corpus.verify("doc-9", ok)[0], "verified")
        self.assertEqual(corpus.verify("02-financial-and-turnover-data", ok)[0], "verified")
        self.assertEqual(corpus.verify("01-deal-summary.md", ok)[0], "misattributed")
        self.assertEqual(corpus.verify("doc-9", "Sorell's US sales were $250 million last year")[0], "unverified")
        self.assertEqual(corpus.verify("doc-9", "USD")[0], "trivial")


class CompareTests(unittest.TestCase):
    """eval/run.py compare: per-matter deltas against the v1 baseline, nonzero below the gate."""

    def test_compare_exit_codes(self):
        m = load_matter("litigation")
        with tempfile.TemporaryDirectory() as td:
            results = Path(td) / "results"
            from proofrun import mock
            from proofrun.score import score_run
            score_run(m, mock.build(m, "oracle", results / m.id / "v2-test" / "20261022-000000"))
            n = json.loads(next(results.rglob("scores.json")).read_text())["n_passed"]
            base = Path(td) / "v1.json"

            def run(passed, *flags):
                base.write_text(json.dumps({"matters": {"litigation": {"n_passed": passed, "citation_verified_rate": 1.0, "cost_usd": 1.0}}}))
                return cli.main(["compare", str(base), str(results), *flags])

            self.assertEqual(run(n), 0)                       # at parity
            self.assertEqual(run(n + 1), 1)                   # one criterion below v1
            self.assertEqual(run(n + 2, "--slack", "2"), 0)   # qwen's allowance
            self.assertEqual(run(n + 3, "--slack", "2"), 1)
            base.write_text(json.dumps({"matters": {"ma-diligence": {"n_passed": 0}}}))
            self.assertEqual(cli.main(["compare", str(base), str(results)]), 1)  # no scored run for that matter

    def test_compare_fails_when_baseline_names_no_matter(self):
        """PR 4b r1 M6: a baseline that names no registered matter (empty, or mis-keyed) is a failed
        gate, not a vacuous pass."""
        with tempfile.TemporaryDirectory() as td:
            base = Path(td) / "v1.json"
            for doc in ({}, {"matters": {}}, {"matters": {"no-such-matter": {"n_passed": 0}}}):
                base.write_text(json.dumps(doc))
                self.assertEqual(cli.main(["compare", str(base), str(Path(td) / "results")]), 1, doc)


if __name__ == "__main__":
    unittest.main()
