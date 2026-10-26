#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Compare two bench summaries (docs/benchmarks.md § Comparing).

    python3 eval/bench/compare.py BASE NEW [--json] [--require-comparable]

Prints, per task both hold: the score delta, the criteria that flipped (gained: fail → pass, lost:
pass → fail), and the cost, call and duration deltas, under one of three verdicts:

- COMPARABLE: the same config hash, mode, harness commit, judge and in-repo yardstick, clean builds
  and fresh databases.
- COMPARABLE WITH CAVEATS (reconstructed baseline): one side is a baseline whose reconstructed config
  hashes to the other's config, and nothing else differs; the keys it only inferred or does not know
  are listed. Still not comparable for --require-comparable.
- NOT COMPARABLE: with every reason and every differing config key. A difference in the proof-run
  scorer or rubric makes the proof-run tasks not comparable; one in the LAB driver or the bench
  harness, every task.

--require-comparable exits 1 unless the verdict is COMPARABLE for every task.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from benchlib.suite import check_summary  # noqa: E402


def flatten(v, prefix: str = "") -> dict:
    """Dotted key → leaf value (lists of objects keyed by their id when they have one)."""
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            out.update(flatten(x, f"{prefix}.{k}" if prefix else k))
        return out
    if isinstance(v, list) and v and all(isinstance(x, dict) and "id" in x for x in v):
        out = {}
        for x in v:
            out.update(flatten({k: y for k, y in x.items() if k != "id"}, f"{prefix}[{x['id']}]"))
        return out
    return {prefix: v}


def config_diff(a: dict | None, b: dict | None) -> list[str]:
    """The config keys whose values differ, as "key: A → B" (a key one side lacks shows as <absent>)."""
    if a is None or b is None:
        return []
    fa, fb = flatten(a), flatten(b)
    out = []
    for k in sorted(set(fa) | set(fb)):
        va, vb = fa.get(k, "<absent>"), fb.get(k, "<absent>")
        if va != vb:
            out.append(f"{k}: {json.dumps(va)} → {json.dumps(vb)}")
    return out


def effective(s: dict) -> tuple[str | None, dict | None, bool]:
    """(hash, config, reconstructed?) a summary compares by: its pinned config, else a baseline's
    reconstructed one, else nothing."""
    if s["config_hash"]:
        return s["config_hash"], s["config"], False
    if s.get("reconstructed_config_hash"):
        return s["reconstructed_config_hash"], s["reconstructed_config"], True
    return None, None, False


def caveat_keys(s: dict) -> list[str]:
    """A reconstructed baseline's keys known only by inference, or not at all."""
    return sorted(f"{k} ({v.split(':', 1)[0]})" for k, v in (s.get("provenance") or {}).items()
                  if v.startswith(("inferred", "unknown")))


def comparability(a: dict, b: dict) -> tuple[list[str], list[str], list[str]]:
    """(reasons the two are not comparable, caveats of a reconstructed baseline, warnings)."""
    why, cav, warn = [], [], []
    (ha, _, ra), (hb, _, rb) = effective(a), effective(b)
    recon = ra or rb
    for s, h in ((a, ha), (b, hb)):
        if h is None:
            why.append(f"{s['label']} records no config hash (its settings were neither pinned nor reconstructed)")
    if ha and hb and ha != hb:
        why.append(f"config hashes differ ({ha[:15]}… vs {hb[:15]}…)")
    if a["mode"] and b["mode"] and a["mode"] != b["mode"]:
        why.append(f"modes differ ({a['mode']} vs {b['mode']})")
    for field, name in (("commit", "harness commits"), ("pandoc", "pandoc versions")):
        va, vb = (a["harness"] or {}).get(field), (b["harness"] or {}).get(field)
        if va and vb and va != vb:
            (why if field == "commit" else warn).append(f"{name} differ ({va} vs {vb})")
        elif (va is None) != (vb is None) and not recon:
            warn.append(f"{name}: unknown on one side ({va} vs {vb})")
    ja, jb = (a["judge"] or {}).get("models"), (b["judge"] or {}).get("models")
    if ja != jb:
        why.append(f"judges differ ({ja} vs {jb})")
    for s in (a, b):
        if s["fresh_db"] is False:
            why.append(f"{s['label']} ran on a database that already held data (cross-run caches, bandit priors)")
        if s["build"]["dirty"]:
            why.append(f"{s['label']} was built from a checkout that differs from its commit (--allow-dirty): its sha does not name the code")
        if s["stopped"]:
            warn.append(f"{s['label']} stopped early ({s['stopped']['reason']}): tasks it did not run are not compared")
    ya, yb = a.get("yardstick"), b.get("yardstick")
    for part in ("labdriver", "bench"):
        va, vb = (ya or {}).get(part), (yb or {}).get(part)
        if va and vb and va != vb:
            why.append(f"the {'LAB driver (eval/lab)' if part == 'labdriver' else 'bench harness (eval/bench)'} differs between the runs: "
                       "every task's input or measurement may differ")
    if recon and not why:
        side = a if ra else b
        cav.append(f"{side['label']}'s settings are reconstructed, not pinned; inferred or unknown: {', '.join(caveat_keys(side))}")
        cav.append("its database freshness, bandit priors and in-repo yardstick are unknown: deltas within about ±3 criteria "
                   "on SEC are within plausible run-to-run variance")
    ma, mb = a.get("models_served"), b.get("models_served")
    if ma and mb and ma != mb:
        warn.append(f"the models served differ ({ma} vs {mb}): an alias may have moved")
    if a["build"]["sha"] and (a["build"]["sha"] or "")[:7] == (b["build"]["sha"] or "")[:7]:
        warn.append(f"both are builds of {a['build']['sha'][:7]}: the deltas are run-to-run variance")
    return why, cav, warn


def task_why(a: dict, b: dict, x: dict) -> list[str]:
    """Reasons one task is not comparable beyond the run-wide ones: the proof-run scorer or rubric."""
    if x["kind"] != "proofrun":
        return []
    va, vb = (a.get("yardstick") or {}).get("proofrun"), (b.get("yardstick") or {}).get("proofrun")
    if va and vb and va != vb:
        return ["the proof-run scorer, rubric or matter (eval/proofrun, eval/tasks, eval/matters.json) differs between the runs"]
    return []


def delta(x, y, fmt="{:+d}"):
    if x is None or y is None:
        return "unknown"
    return fmt.format(y - x)


def task_rows(a: dict, b: dict) -> list[dict]:
    ta, tb = {t["id"]: t for t in a["tasks"]}, {t["id"]: t for t in b["tasks"]}
    rows = []
    for tid in [t["id"] for t in a["tasks"]] + [t for t in tb if t not in ta]:
        x, y = ta.get(tid), tb.get(tid)
        if x is None or y is None:
            rows.append({"id": tid, "only_in": a["label"] if y is None else b["label"]})
            continue
        r = {"id": tid, "not_comparable_because": task_why(a, b, x), "score": [x["score"]["passed"], y["score"]["passed"]], "total": [x["score"]["total"], y["score"]["total"]],
             "score_delta": None if None in (x["score"]["passed"], y["score"]["passed"]) else y["score"]["passed"] - x["score"]["passed"],
             "cost": [x["cost_usd"], y["cost_usd"]], "calls": [x["calls"], y["calls"]], "duration_s": [x["duration_s"], y["duration_s"]],
             "stop_reason": [x["stop_reason"], y["stop_reason"]], "status": [x["status"], y["status"]]}
        if x["score"]["total"] is not None and y["score"]["total"] is not None and x["score"]["total"] != y["score"]["total"]:
            r["warning"] = f"criteria counts differ ({x['score']['total']} vs {y['score']['total']}): a different rubric"
        ca, cb = x["criteria"], y["criteria"]
        if ca is None or cb is None:
            r["flips"] = None
            r["flips_unknown"] = [s for s, c in ((a["label"], ca), (b["label"], cb)) if c is None]
        else:
            r["flips"] = {"gained": sorted(k for k in cb if cb[k] == "pass" and ca.get(k) == "fail"),
                          "lost": sorted(k for k in cb if cb[k] == "fail" and ca.get(k) == "pass"),
                          "other": sorted(k for k in set(ca) | set(cb) if (ca.get(k), cb.get(k)) not in
                                          {("pass", "pass"), ("fail", "fail"), ("pass", "fail"), ("fail", "pass")})}
        rows.append(r)
    return rows


def compare(a: dict, b: dict) -> dict:
    why, cav, warn = comparability(a, b)
    rows = task_rows(a, b)
    state = "not_comparable" if why else ("caveats" if cav else "comparable")
    task_ok = all(not r.get("not_comparable_because") for r in rows)
    (_, ca, _), (_, cb, _) = effective(a), effective(b)
    return {"base": a["label"], "new": b["label"], "state": state, "comparable": state == "comparable" and task_ok,
            "comparable_with_caveats": state == "caveats", "not_comparable_because": why, "caveats": cav, "warnings": warn,
            "config_diff": config_diff(ca, cb), "tasks": rows}


def money(v):
    return "?" if v is None else f"${v:.2f}"


def render(c: dict) -> str:
    out = [f"compare {c['base']} → {c['new']}"]
    if c["state"] == "not_comparable":
        out.append("NOT COMPARABLE: " + "; ".join(c["not_comparable_because"]))
    elif c["state"] == "caveats":
        out.append("COMPARABLE WITH CAVEATS (reconstructed baseline):")
        out += [f"  caveat: {x}" for x in c["caveats"]]
    elif c["comparable"]:
        out.append("COMPARABLE")
    for d in c["config_diff"]:
        out.append(f"  config differs: {d}")
    for w in c["warnings"]:
        out.append(f"  warning: {w}")
    for r in c["tasks"]:
        if "only_in" in r:
            out.append(f"{r['id']}: only in {r['only_in']}")
            continue
        sa, sb = r["score"]
        if r["not_comparable_because"]:
            out.append(f"{r['id']}: NOT COMPARABLE: " + "; ".join(r["not_comparable_because"]))
        sd = "unknown" if r["score_delta"] is None else f"{r['score_delta']:+d}"
        out.append(f"{r['id']}: score {sa}/{r['total'][0]} → {sb}/{r['total'][1]} ({sd}); cost {money(r['cost'][0])} → {money(r['cost'][1])} "
                   f"({delta(r['cost'][0], r['cost'][1], '{:+.2f}')}); calls {r['calls'][0]} → {r['calls'][1]} ({delta(r['calls'][0], r['calls'][1])}); "
                   f"stop {r['stop_reason'][0]} → {r['stop_reason'][1]}")
        if r.get("warning"):
            out.append(f"  warning: {r['warning']}")
        if r["flips"] is None:
            out.append(f"  criteria: per-criterion results unknown in {', '.join(r['flips_unknown'])}")
        else:
            out.append(f"  gained ({len(r['flips']['gained'])}): {', '.join(r['flips']['gained']) or '-'}")
            out.append(f"  lost   ({len(r['flips']['lost'])}): {', '.join(r['flips']['lost']) or '-'}")
            if r["flips"]["other"]:
                out.append(f"  neither pass nor fail on one side: {', '.join(r['flips']['other'])}")
    return "\n".join(out)


def load(path: str) -> dict:
    s = json.loads(Path(path).read_text(encoding="utf-8"))
    errs = check_summary(s)
    if errs:
        raise SystemExit(f"{path} is not a bench summary:\n  - " + "\n  - ".join(errs))
    return s


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="Compare two bench summaries")
    p.add_argument("base")
    p.add_argument("new")
    p.add_argument("--json", action="store_true", help="print the comparison as JSON")
    p.add_argument("--require-comparable", action="store_true", help="exit 1 when the two runs are not comparable")
    a = p.parse_args(argv)
    c = compare(load(a.base), load(a.new))
    print(json.dumps(c, indent=2) if a.json else render(c))
    return 1 if a.require_comparable and not c["comparable"] else 0


if __name__ == "__main__":
    sys.exit(main())
