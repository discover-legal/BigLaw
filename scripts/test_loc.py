#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Tests for scripts/loc.py (PLAN.md PR 1: TestLocBudget).

Run: python3 -m unittest discover -s scripts -p 'test_*.py'   (make loc does)
"""
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
LOC = os.path.join(HERE, "loc.py")
HEADER = "// SPDX-License-Identifier: Apache-2.0\n"


def run(root):
    p = subprocess.run([sys.executable, LOC, "--root", root], capture_output=True, text=True)
    return p.returncode, p.stdout + p.stderr


def write(root, rel, body):
    path = os.path.join(root, rel)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(body)


class TestLocBudget(unittest.TestCase):
    """loc.py exits nonzero, naming the budget, on a synthetic over-budget tree."""

    def base(self):
        root = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", root]))
        write(root, "go.mod", "module x\n\ngo 1.25\n\nrequire github.com/lib/pq v1.12.3\n")
        write(root, "LICENSE", "Apache License\n")
        write(root, "NOTICE", "x\n")
        write(root, "internal/a/a.go", HEADER + "package a\n")
        write(root, "internal/a/a_test.go", HEADER + "package a\n")
        return root

    def test_minimal_tree_passes(self):
        code, out = run(self.base())
        self.assertEqual(code, 0, out)

    def test_this_repository_passes(self):
        code, out = run(os.path.join(HERE, ".."))
        self.assertEqual(code, 0, out)

    def test_each_budget_fails(self):
        def env_vars(r):
            body = HEADER + 'package a\n\nimport "os"\n\nfunc f() {\n'
            body += "".join(f'\t_ = os.Getenv("X_{i}")\n' for i in range(21))
            write(r, "internal/a/env.go", body + "}\n")

        def packages(r):
            for i in range(12):  # 12 more on top of internal/a
                write(r, f"internal/p{i}/p.go", HEADER + "package p\n")
                write(r, f"internal/p{i}/p_test.go", HEADER + "package p\n")

        over = {
            # 15,501 lines in total (a.go's 2 + 7 x 1,937 + 1,940), spread under the per-file cap
            "go_prod_loc": lambda r: [write(r, f"internal/a/big{i}.go", HEADER + "package a\n" + "// x\n" * (1938 if i == 0 else 1935)) for i in range(8)],
            "internal_packages": packages,
            "direct_go_deps": lambda r: write(r, "go.mod", "module x\n\ngo 1.25\n\nrequire (\n\ta.example/a v1.0.0\n\tb.example/b v1.0.0\n\tc.example/c v1.0.0 // indirect\n\td.example/d v1.0.0\n)\n\nrequire e.example/e v1.0.0\n"),
            "env_vars": env_vars,
            "ui_runtime_deps": lambda r: write(r, "ui/package.json", '{"dependencies":{"react":"18","react-dom":"18","left-pad":"1"}}'),
            "untested_packages": lambda r: write(r, "internal/b/b.go", HEADER + "package b\n"),
            "non-literal": lambda r: write(r, "internal/a/dyn.go", HEADER + 'package a\n\nimport "os"\n\nfunc g(n string) string { return os.Getenv(n) }\n'),
            # r2 prosecutor F6: a concatenation of literals hid BIGLAW_SNEAK.
            "non-literal (concatenation)": lambda r: write(r, "internal/a/dyn.go", HEADER + 'package a\n\nimport "os"\n\nfunc g() (string, bool) { return os.LookupEnv("BIGLAW_" + "SNEAK") }\n'),
            "non-literal (raw string, multi-line)": lambda r: write(r, "internal/a/dyn.go", HEADER + 'package a\n\nimport "os"\n\nvar _ = os.Getenv(\n\t`BIGLAW_RAW`,\n)\n'),
            "test-only": lambda r: write(r, "internal/a/t.go", HEADER + 'package a\n\nimport "os"\n\nvar _ = os.Getenv("BIGLAW_TEST_DSN")\n'),
            "longest_source_file": lambda r: write(r, "internal/a/a_test.go", HEADER + "package a\n" + "// x\n" * 1999),  # 2,001 lines, a test file
            "LICENSE": lambda r: os.remove(os.path.join(r, "LICENSE")),
            "NOTICE": lambda r: os.remove(os.path.join(r, "NOTICE")),
            "SPDX": lambda r: write(r, "internal/a/a.go", "package a\n"),
        }
        for name, mutate in over.items():
            with self.subTest(budget=name):
                root = self.base()
                mutate(root)
                code, out = run(root)
                failed = [l for l in out.splitlines() if l.startswith("FAIL:") and name.split(" (")[0] in l]
                self.assertTrue(code != 0 and failed, f"{name}: exit {code}\n{out}")

    def test_loc_ceiling_boundary(self):
        """15,500 lines pass, 15,501 fail (owner decision 2026-10-18: ceiling 15,500, declared final)."""
        root = self.base()  # internal/a/a.go has 2 lines; big0 2 + 1,937, the other seven 2 + 1,935
        for i in range(8):
            write(root, f"internal/a/big{i}.go", HEADER + "package a\n" + "// x\n" * (1937 if i == 0 else 1935))
        code, out = run(root)
        self.assertEqual(code, 0, out)
        self.assertIn("go_prod_loc           15500 / 15500   ok", out)
        write(root, "internal/a/big0.go", HEADER + "package a\n" + "// x\n" * 1938)
        code, out = run(root)
        self.assertNotEqual(code, 0, out)
        self.assertIn("go_prod_loc           15501 / 15500   OVER", out)

    def test_support_packages_count_lines_not_packages(self):
        """Coordinator ruling: a *test package no production code imports is out of the
        package count (12 real + storetest passes) but its lines still count."""
        root = self.base()
        for i in range(11):  # 12 real packages with internal/a
            write(root, f"internal/p{i}/p.go", HEADER + "package p\n")
            write(root, f"internal/p{i}/p_test.go", HEADER + "package p\n")
        write(root, "internal/store/storetest/storetest.go", HEADER + "package storetest\n" + "// x\n" * 500)
        write(root, "internal/store/storetest/storetest_test.go", HEADER + "package storetest\n")
        code, out = run(root)
        self.assertEqual(code, 0, out)
        self.assertIn("internal_packages        12 / 12", out)
        self.assertIn("go_prod_loc             526", out)  # 12 packages x 2 lines + storetest's 502: still counted
        # A 13th real package fails ...
        write(root, "internal/q/q.go", HEADER + "package q\n")
        write(root, "internal/q/q_test.go", HEADER + "package q\n")
        code, out = run(root)
        self.assertNotEqual(code, 0, out)
        os.remove(os.path.join(root, "internal/q/q.go"))
        os.remove(os.path.join(root, "internal/q/q_test.go"))
        # ... and so does a *test package that production code imports: it is not test support.
        write(root, "go.mod", "module x\n\ngo 1.25\n")
        write(root, "internal/p0/p.go", HEADER + 'package p\n\nimport _ "x/internal/store/storetest"\n')
        code, out = run(root)
        self.assertNotEqual(code, 0, out)
        self.assertIn("internal_packages        13 / 12", out)

    def test_per_file_cap_boundary(self):
        root = self.base()
        write(root, "scripts/ok.py", "# SPDX-License-Identifier: Apache-2.0\n" + "x = 1\n" * 1999)  # exactly 2,000
        code, out = run(root)
        self.assertEqual(code, 0, out)
        write(root, "migrations/0001_big.sql", "-- x\n" * 2001)
        code, out = run(root)
        self.assertNotEqual(code, 0, out)
        self.assertIn("migrations/0001_big.sql: 2001 lines", out)


if __name__ == "__main__":
    unittest.main()
