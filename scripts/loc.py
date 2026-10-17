#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""BigLaw 2 size budgets (README § What is on disk; PLAN.md "Rules for every PR").

Fails (exit 1) when any budget is exceeded:
  Go production lines   <= 14,250  physical lines of every non-_test .go file,
                                   test-support packages (storetest) included
  internal packages     <= 12      directories under internal/ holding non-test Go,
                                   except test-support packages (below)
  direct Go deps        <= 3       go.mod requires not marked // indirect
  runtime env vars      <= 20      distinct names read by production Go code
  UI runtime deps       <= 2       ui/package.json "dependencies"
  packages without tests == 0      every Go package directory has a *_test.go
  longest source file   <= 2,000   lines of any .go/.py/.sql/.sh/.js/.ts/.tsx/.css file (owner rule)
It also fails when LICENSE or NOTICE is missing, or a .go/.py file lacks the
SPDX-License-Identifier: Apache-2.0 header in its first five lines.

A test-support package (coordinator ruling) is one whose directory name ends
in "test" (storetest, modeltest) and that no non-_test.go file outside it
imports. It is left out of the package count only: its lines count toward
the LOC ceiling like any other non-_test.go file (owner decision, ceiling
14,250). A *test package that production code imports is an ordinary package.

Env-var names are found statically: literal arguments of os.Getenv/LookupEnv
(and of any getenv(...) call), every `name: "X"` entry of the config table,
and any string literal shaped like BIGLAW_*/ALLOW_*. An os.Getenv or
os.LookupEnv argument that is not one plain string literal (a variable, a
concatenation, a raw string) is itself a violation, because it would hide a
name.
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
    "go_prod_loc": 14250,
    "internal_packages": 12,
    "direct_go_deps": 3,
    "env_vars": 20,
    "ui_runtime_deps": 2,
    "untested_packages": 0,
    "longest_source_file": 2000,
}
TEST_ONLY_VARS = {"BIGLAW_TEST_DSN", "BIGLAW_REQUIRE_DB"}
TEST_ONLY_HOME = os.path.join("internal", "store", "storetest")
SKIP_DIRS = {".git", "node_modules", "vendor", "testdata", "dist"}
SOURCE_EXT = (".go", ".py", ".sql", ".sh", ".js", ".mjs", ".ts", ".tsx", ".css")
SPDX = "SPDX-License-Identifier: Apache-2.0"

CALL_LITERAL = re.compile(r'\b(?:os\.Getenv|os\.LookupEnv|getenv)\(\s*"([A-Za-z_][A-Za-z0-9_]*)"\s*\)')
OS_CALL = re.compile(r'\bos\.(?:Getenv|LookupEnv)\(')
ONE_LITERAL = re.compile(r'\s*"[A-Za-z_][A-Za-z0-9_]*"\s*')
IMPORT = re.compile(r'"([^"\s]+)"')
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


def os_call_args(src):
    """The argument text of every os.Getenv/os.LookupEnv call (parentheses balanced)."""
    for m in OS_CALL.finditer(src):
        depth, i = 1, m.end()
        while i < len(src) and depth:
            depth += {"(": 1, ")": -1}.get(src[i], 0)
            i += 1
        yield src[m.end():i - 1]


def module_path(root):
    path = os.path.join(root, "go.mod")
    if os.path.exists(path):
        for line in open(path, encoding="utf-8"):
            if line.startswith("module "):
                return line.split()[1]
    return ""


def test_support(root, pkgs):
    """Package dirs named *test that no production file of another package imports."""
    mod, imported = module_path(root), set()
    for rel, files in pkgs.items():
        for path in files["prod"]:
            for imp in IMPORT.findall(open(path, encoding="utf-8", errors="replace").read()):
                if mod and imp.startswith(mod + "/") and imp[len(mod) + 1:] != rel.replace(os.sep, "/"):
                    imported.add(imp[len(mod) + 1:])
    return {r for r in pkgs if os.path.basename(r).endswith("test") and r.replace(os.sep, "/") not in imported}


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


def source_files(root):
    for dp, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS and not d.startswith("."))
        for f in sorted(files):
            if f.endswith(SOURCE_EXT):
                yield os.path.join(dp, f)


def lines(src):
    return src.count("\n") + (0 if src.endswith("\n") or not src else 1)


def measure(root):
    pkgs = go_packages(root)
    loc, env, problems, longest = 0, set(), [], 0
    for path in source_files(root):
        src = open(path, encoding="utf-8", errors="replace").read()
        n, rel = lines(src), os.path.relpath(path, root)
        longest = max(longest, n)
        if n > BUDGETS["longest_source_file"]:
            problems.append(f"{rel}: {n} lines exceeds the per-file cap of {BUDGETS['longest_source_file']} (longest_source_file); split it")
        if path.endswith((".go", ".py")) and SPDX not in "\n".join(src.splitlines()[:5]):
            problems.append(f"{rel}: missing the '{SPDX}' header")
    for name in ("LICENSE", "NOTICE"):
        if not os.path.exists(os.path.join(root, name)):
            problems.append(f"{name} is missing at the repository root")
    for rel, files in pkgs.items():
        for path in files["prod"]:
            src = open(path, encoding="utf-8", errors="replace").read()
            loc += lines(src)
            read = set(CALL_LITERAL.findall(src)) | set(TABLE_NAME.findall(src))
            names = read | set(SHAPED.findall(src))
            for arg in os_call_args(src):
                if not ONE_LITERAL.fullmatch(arg):
                    problems.append(f"{os.path.relpath(path, root)}: os.Getenv/LookupEnv with a non-literal name ({' '.join(arg.split())}) hides an env var from this check")
            for n in read & TEST_ONLY_VARS:
                if rel != TEST_ONLY_HOME:
                    problems.append(f"{os.path.relpath(path, root)}: test-only variable {n} read outside {TEST_ONLY_HOME}")
            env |= names - TEST_ONLY_VARS
    support = test_support(root, pkgs)
    internal = sorted(r for r, f in pkgs.items() if f["prod"] and r.startswith("internal" + os.sep) and r not in support)
    untested = sorted(r for r, f in pkgs.items() if f["prod"] and not f["test"])
    deps, ui = direct_deps(root), ui_deps(root)
    m = {
        "go_prod_loc": loc,
        "internal_packages": len(internal),
        "direct_go_deps": len(deps),
        "env_vars": len(env),
        "ui_runtime_deps": len(ui),
        "untested_packages": len(untested),
        "longest_source_file": longest,
    }
    detail = {"internal": internal, "test_support": sorted(r for r in support if pkgs[r]["prod"]), "deps": deps, "env": sorted(env), "ui": ui, "untested": untested}
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
            print(f"{k:19} {m[k]:>7} / {limit:<7} {'ok' if m[k] <= limit else 'OVER'}")
        print("env vars:", ", ".join(detail["env"]) or "-")
        print("direct deps:", ", ".join(detail["deps"]) or "-")
        print("internal packages:", ", ".join(detail["internal"]) or "-")
        print("test-support packages (LOC counted, package budget not):", ", ".join(detail["test_support"]) or "-")
        for p in problems:
            print("FAIL:", p)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
