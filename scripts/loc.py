#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""BigLaw 2 size budgets (README § What is on disk; PLAN.md "Rules for every PR").

Fails (exit 1) when any budget is exceeded:
  Go production lines   <= 14,000  physical lines of every non-_test .go file
  internal packages     <= 12      directories under internal/ holding non-test Go
  direct Go deps        <= 3       go.mod requires not marked // indirect
  runtime env vars      <= 20      distinct names read by production Go code
  UI runtime deps       <= 2       ui/package.json "dependencies"
  packages without tests == 0      every Go package directory has a *_test.go

Env-var names are found statically: literal arguments of os.Getenv/LookupEnv
(and of any getenv(...) call), every `name: "X"` entry of the config table,
and any string literal shaped like BIGLAW_*/ALLOW_*. A non-literal os.Getenv
or os.LookupEnv argument is itself a violation, because it would hide a name.
BIGLAW_TEST_DSN and BIGLAW_REQUIRE_DB are test-only and may appear only in
internal/store/storetest.

Usage: scripts/loc.py [--root DIR] [--json]
"""
import argparse
import json
import os
import re
import sys

BUDGETS = {
    "go_prod_loc": 14000,
    "internal_packages": 12,
    "direct_go_deps": 3,
    "env_vars": 20,
    "ui_runtime_deps": 2,
    "untested_packages": 0,
}
TEST_ONLY_VARS = {"BIGLAW_TEST_DSN", "BIGLAW_REQUIRE_DB"}
TEST_ONLY_HOME = os.path.join("internal", "store", "storetest")
SKIP_DIRS = {".git", "node_modules", "vendor", "testdata", "dist"}

CALL_LITERAL = re.compile(r'\b(?:os\.Getenv|os\.LookupEnv|getenv)\(\s*"([A-Za-z_][A-Za-z0-9_]*)"\s*\)')
CALL_DYNAMIC = re.compile(r'\bos\.(?:Getenv|LookupEnv)\(\s*([^")\s][^)]*)\)')
TABLE_NAME = re.compile(r'\bname:\s*"([A-Z][A-Z0-9_]*)"')
SHAPED = re.compile(r'"((?:BIGLAW|ALLOW)_[A-Z0-9_]+)"')


def go_packages(root):
    """Map package dir (relative) -> {"prod": [files], "test": [files]}."""
    pkgs = {}
    for dp, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS and not d.startswith("."))
        for f in sorted(files):
            if not f.endswith(".go"):
                continue
            rel = os.path.relpath(dp, root)
            kind = "test" if f.endswith("_test.go") else "prod"
            pkgs.setdefault(rel, {"prod": [], "test": []})[kind].append(os.path.join(dp, f))
    return pkgs


def direct_deps(root):
    path = os.path.join(root, "go.mod")
    if not os.path.exists(path):
        return []
    deps, in_block = [], False
    for line in open(path, encoding="utf-8"):
        s = line.strip()
        if s.startswith("require ("):
            in_block = True
            continue
        if in_block and s == ")":
            in_block = False
            continue
        if s.startswith("require ") and not s.startswith("require ("):
            s, in_req = s[len("require "):], True
        else:
            in_req = in_block
        if in_req and s and not s.startswith("//") and "// indirect" not in s:
            deps.append(s.split()[0])
    return deps


def ui_deps(root):
    path = os.path.join(root, "ui", "package.json")
    if not os.path.exists(path):
        return []
    return sorted(json.load(open(path, encoding="utf-8")).get("dependencies", {}))


def measure(root):
    pkgs = go_packages(root)
    loc, env, problems = 0, set(), []
    for rel, files in pkgs.items():
        for path in files["prod"]:
            src = open(path, encoding="utf-8", errors="replace").read()
            loc += src.count("\n") + (0 if src.endswith("\n") or not src else 1)
            read = set(CALL_LITERAL.findall(src)) | set(TABLE_NAME.findall(src))
            names = read | set(SHAPED.findall(src))
            for m in CALL_DYNAMIC.finditer(src):
                problems.append(f"{os.path.relpath(path, root)}: os.Getenv/LookupEnv with a non-literal name ({m.group(1).strip()}) hides an env var from this check")
            for n in read & TEST_ONLY_VARS:
                if rel != TEST_ONLY_HOME:
                    problems.append(f"{os.path.relpath(path, root)}: test-only variable {n} read outside {TEST_ONLY_HOME}")
            env |= names - TEST_ONLY_VARS
    internal = sorted(r for r, f in pkgs.items() if f["prod"] and (r == "internal" or r.startswith("internal" + os.sep)) and r != "internal")
    untested = sorted(r for r, f in pkgs.items() if f["prod"] and not f["test"])
    deps, ui = direct_deps(root), ui_deps(root)
    m = {
        "go_prod_loc": loc,
        "internal_packages": len(internal),
        "direct_go_deps": len(deps),
        "env_vars": len(env),
        "ui_runtime_deps": len(ui),
        "untested_packages": len(untested),
    }
    detail = {"internal": internal, "deps": deps, "env": sorted(env), "ui": ui, "untested": untested}
    return m, detail, problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--root", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args(argv)
    m, detail, problems = measure(os.path.abspath(a.root))
    for k, limit in BUDGETS.items():
        if m[k] > limit:
            problems.append(f"{k} = {m[k]} exceeds the budget of {limit}")
    if a.json:
        print(json.dumps({"measured": m, "budgets": BUDGETS, "detail": detail, "problems": problems}, indent=1))
    else:
        for k, limit in BUDGETS.items():
            print(f"{k:18} {m[k]:>7} / {limit:<7} {'ok' if m[k] <= limit else 'OVER'}")
        print("env vars:", ", ".join(detail["env"]) or "-")
        print("direct deps:", ", ".join(detail["deps"]) or "-")
        print("internal packages:", ", ".join(detail["internal"]) or "-")
        for p in problems:
            print("FAIL:", p)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
