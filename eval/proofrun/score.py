# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Mechanical scorer for a proof-run directory.

Two axes, both reproducible from the run dir + fixtures, no judge model:

* **Accuracy** — did the deliverable state each planted fact (answer_key
  ``facts``), avoid each planted trap (``traps``), and reach the right
  disposition on each negotiated change (``dispositions``)?
* **Grounding** — does each finding's cited quote occur verbatim in the
  document it names (re-verified here from the fixture text, independently of
  the engine's own ``mechanicallyVerified`` flag), and is each planted fact
  the deliverable states backed by a verified quote from the right document?

Writes ``scores.json`` in a LAB-compatible shape (``score``/``max_score``/
``n_passed``/``n_criteria``/``criteria_results[].verdict``) so
``benchmarks/harvey-lab/compare.py --results-dir`` and the runs/README recount
one-liner both work unchanged.
"""

from __future__ import annotations

import json
import re
from datetime import datetime, timezone
from pathlib import Path

from .matter import Matter, labdriver
from .patterns import normalize, normalize_quote, pattern_matches, split_units

SCORER_VERSION = "proof-runs/mechanical-v1"
MIN_QUOTE_WORDS = 3          # shorter "quotes" are too generic to count as evidence
DELIVERABLE_QUOTE_WORDS = 6  # quoted spans in the deliverable checked against the corpus

_DISP_UPPER = re.compile(r"\b(ACCEPT(?:ED)?|COUNTER(?:-?PROPOSE[DS]?|ED)?|REJECT(?:ED)?)\b")
_DISP_ANY = re.compile(r"\b(accept(?:ed)?|counter(?:-?propos\w*|ed|offer)?|reject(?:ed)?|decline[sd]?)\b", re.I)
_QUOTED = re.compile(r"[\"“]([^\"“”]{20,}?)[\"”]")


# ─── run loading ─────────────────────────────────────────────────────────────

def load_run(run_dir: Path) -> dict:
    """Read a run dir written by the driver (or by mock.py)."""
    out_dir = run_dir / "output"
    convert = labdriver().convert
    deliverables: dict[str, str] = {}
    if out_dir.is_dir():
        for p in sorted(out_dir.rglob("*")):
            if p.is_file():
                try:
                    deliverables[str(p.relative_to(out_dir))] = convert.extract_text(p)
                except Exception as e:  # noqa: BLE001
                    deliverables[str(p.relative_to(out_dir))] = ""
                    print(f"warning: could not read deliverable {p.name}: {e}")
    tasks = []
    tp = run_dir / "biglaw_tasks.json"
    if tp.is_file():
        tasks = json.loads(tp.read_text(encoding="utf-8"))
    docmap = {}
    dp = run_dir / "documents.json"
    if dp.is_file():
        docmap = json.loads(dp.read_text(encoding="utf-8"))
    metrics = {}
    mp = run_dir / "metrics.json"
    if mp.is_file():
        metrics = json.loads(mp.read_text(encoding="utf-8"))
    return {"deliverables": deliverables, "tasks": tasks, "docmap": docmap, "metrics": metrics}


# ─── citation verification ───────────────────────────────────────────────────

class Corpus:
    def __init__(self, documents: dict[str, str], docmap: dict[str, str]):
        self.docs = documents
        self.norm = {name: normalize_quote(text) for name, text in documents.items()}
        self.docmap = docmap  # BigLaw document id -> filename

    def resolve(self, source: str) -> str | None:
        """Map a citation's ``source`` (doc id, title, or loose filename) to a fixture name."""
        s = (source or "").strip()
        if not s:
            return None
        if s in self.docmap:
            return self.docmap[s]
        if s in self.docs:
            return s
        low = s.lower()
        for name in self.docs:
            stem = name.lower().rsplit(".", 1)[0]
            if name.lower() in low or stem in low:
                return name
        for doc_id, name in self.docmap.items():
            if doc_id and doc_id in s:
                return name
        return None

    def _found(self, quote_norm: str, doc_norm: str) -> bool:
        if quote_norm in doc_norm:
            return True
        # Tolerate an elided quote ("A ... B"): every segment of >= 4 words, in order.
        segs = [x.strip(" \"'.,;:") for x in re.split(r"\.\.\.|\[\.\.\.\]", quote_norm)]
        segs = [x for x in segs if len(x.split()) >= 4]
        if len(segs) < 2:
            return False
        pos = 0
        for seg in segs:
            i = doc_norm.find(seg, pos)
            if i < 0:
                return False
            pos = i + len(seg)
        return True

    def verify(self, source: str, quote: str) -> tuple[str, str | None]:
        """Return (status, doc) — status in verified | misattributed | unverified | trivial."""
        q = normalize_quote(quote)
        if len(q.split()) < MIN_QUOTE_WORDS:
            return "trivial", None
        doc = self.resolve(source)
        if doc and self._found(q, self.norm[doc]):
            return "verified", doc
        for name, text in self.norm.items():
            if name != doc and self._found(q, text):
                return ("verified", name) if doc is None else ("misattributed", name)
        return "unverified", doc

    def find_any(self, quote: str) -> str | None:
        q = normalize_quote(quote)
        for name, text in self.norm.items():
            if self._found(q, text):
                return name
        return None


# ─── scoring ─────────────────────────────────────────────────────────────────

def _change_id_regex(cid: str) -> re.Pattern:
    m = re.match(r"^([A-Za-z]+)-?0*(\d+)$", cid)
    if not m:
        return re.compile(re.escape(cid), re.I)
    prefix, num = m.groups()
    return re.compile(rf"\b{re.escape(prefix)}-?0*{num}\b", re.I)


def _disposition_in(unit: str, cid_re: re.Pattern) -> str | None:
    m = cid_re.search(unit)
    if not m:
        return None
    tail = unit[m.end():]
    hit = _DISP_UPPER.search(tail) or _DISP_ANY.search(tail)
    if not hit:
        return None
    w = hit.group(1).lower()
    if w.startswith("accept"):
        return "ACCEPT"
    if w.startswith("counter"):
        return "COUNTER"
    return "REJECT"


def _excerpt(text: str, n: int = 240) -> str:
    t = re.sub(r"\s+", " ", text).strip()
    return t if len(t) <= n else t[: n - 1] + "…"


def score_run(matter: Matter, run_dir: Path, write: bool = True) -> dict:
    run = load_run(run_dir)
    corpus = Corpus(matter.documents, run["docmap"])
    key = matter.key

    deliverable_text = "\n\n".join(run["deliverables"].values())
    units = split_units(deliverable_text)
    norm_units = [normalize(u) for u in units]

    # ── findings + grounding ──
    findings = [f for t in run["tasks"] for f in (t.get("findings") or [])]
    cit_stats = {"verified": 0, "misattributed": 0, "unverified": 0, "trivial": 0}
    engine_verified = 0
    finding_rows = []
    for f in findings:
        statuses = []
        for c in f.get("citations") or []:  # v2: documentId and method; v1: source and mechanicallyVerified
            st, doc = corpus.verify(c.get("documentId") or c.get("source", ""), c.get("quote", ""))
            cit_stats[st] += 1
            engine_verified += bool(c.get("mechanicallyVerified") or c.get("method", "none") != "none")
            statuses.append((st, doc))
        finding_rows.append({
            "id": f.get("fid") or f.get("id"),
            "content_norm": normalize(f.get("claim") or f.get("content", "")),
            "grounded_docs": {d for st, d in statuses if st == "verified" and d},
            "has_citations": bool(statuses),
            "grounded": any(st == "verified" for st, _ in statuses),
        })
    n_find = len(finding_rows)
    n_cit = sum(cit_stats.values())

    deliv_quotes = [q.strip() for q in _QUOTED.findall(deliverable_text)
                    if len(q.split()) >= DELIVERABLE_QUOTE_WORDS]
    deliv_verified = [q for q in deliv_quotes if corpus.find_any(q)]

    grounding = {
        "findings_total": n_find,
        "findings_with_citations": sum(r["has_citations"] for r in finding_rows),
        "findings_grounded": sum(r["grounded"] for r in finding_rows),
        "findings_grounded_rate": round(sum(r["grounded"] for r in finding_rows) / n_find, 4) if n_find else None,
        "citations_total": n_cit,
        "citations": cit_stats,
        "citation_verified_rate": round(cit_stats["verified"] / n_cit, 4) if n_cit else None,
        "engine_mechanically_verified": engine_verified,
        "deliverable_quotes_checked": len(deliv_quotes),
        "deliverable_quotes_verified": len(deliv_verified),
        "deliverable_quotes_unverified": [_excerpt(q, 160) for q in deliv_quotes if q not in deliv_verified][:25],
        "grounding_alerts": [t.get("groundingAlert") for t in run["tasks"] if t.get("groundingAlert")],
    }

    # ── criteria ──
    results = []
    for fact in key.get("facts", []):
        pat = fact["detect"]
        hit = next((u for u, n in zip(units, norm_units) if pattern_matches(pat, n, normalized=True)), None)
        in_findings = [r for r in finding_rows if pattern_matches(pat, r["content_norm"], normalized=True)]
        fact_docs = set(fact.get("documents") or [])
        grounded_via = None
        if any(r["grounded_docs"] & fact_docs for r in in_findings):
            grounded_via = "finding_citation"
        elif hit is not None:
            for q in _QUOTED.findall(hit):
                if len(q.split()) >= MIN_QUOTE_WORDS and corpus.find_any(q) in fact_docs:
                    grounded_via = "deliverable_quote"
                    break
        results.append({
            "id": fact["id"],
            "kind": "fact",
            "criterion": fact["title"],
            "severity": fact.get("severity", "medium"),
            "verdict": "pass" if hit is not None else "fail",
            "grounded": bool(hit is not None and grounded_via),
            "grounded_via": grounded_via if hit is not None else None,
            "detected_in_findings": len(in_findings),
            "matched_unit": _excerpt(hit) if hit else None,
        })

    for trap in key.get("traps", []):
        unless = trap.get("unless") or []
        bad = None
        for u, n in zip(units, norm_units):
            if pattern_matches(trap["forbid"], n, normalized=True) and not any(normalize(x) in n for x in unless):
                bad = u
                break
        results.append({
            "id": trap["id"],
            "kind": "trap",
            "criterion": trap["title"],
            "verdict": "fail" if bad else "pass",
            "matched_unit": _excerpt(bad) if bad else None,
        })

    for d in key.get("dispositions", []):
        cid_re = _change_id_regex(d["id"])
        got, where = None, None
        for u in units:
            got = _disposition_in(u, cid_re)
            if got:
                where = u
                break
        results.append({
            "id": d["id"],
            "kind": "disposition",
            "criterion": f"{d['id']} {d['title']} -> {d['expected']}",
            "expected": d["expected"],
            "got": got,
            "verdict": "pass" if got == d["expected"] else "fail",
            "matched_unit": _excerpt(where) if where else None,
        })
        if d.get("position"):
            pos_hit = next((u for u in units if cid_re.search(u) and pattern_matches(d["position"], u)), None)
            results.append({
                "id": f"{d['id']}/position",
                "kind": "position",
                "criterion": f"{d['id']} counter-position: {d.get('position_title', '')}".strip(),
                "verdict": "pass" if pos_hit else "fail",
                "matched_unit": _excerpt(pos_hit) if pos_hit else None,
            })

    n_crit = len(results)
    n_pass = sum(r["verdict"] == "pass" for r in results)
    facts = [r for r in results if r["kind"] == "fact"]
    high = [r for r in facts if r["severity"] == "high"]
    accuracy = {
        "facts_total": len(facts),
        "facts_detected": sum(r["verdict"] == "pass" for r in facts),
        "facts_grounded": sum(r["grounded"] for r in facts),
        "high_severity_total": len(high),
        "high_severity_detected": sum(r["verdict"] == "pass" for r in high),
        "traps_total": sum(r["kind"] == "trap" for r in results),
        "traps_passed": sum(r["kind"] == "trap" and r["verdict"] == "pass" for r in results),
        "dispositions_total": sum(r["kind"] == "disposition" for r in results),
        "dispositions_correct": sum(r["kind"] == "disposition" and r["verdict"] == "pass" for r in results),
        "positions_total": sum(r["kind"] == "position" for r in results),
        "positions_matched": sum(r["kind"] == "position" and r["verdict"] == "pass" for r in results),
    }

    scores = {
        "task": matter.id,
        "scorer": SCORER_VERSION,
        "scored_at": datetime.now(timezone.utc).isoformat(),
        "score": round(n_pass / n_crit, 4) if n_crit else 0.0,
        "max_score": 1.0,
        "all_pass": n_crit > 0 and n_pass == n_crit,
        "n_passed": n_pass,
        "n_criteria": n_crit,
        "accuracy": accuracy,
        "grounding": grounding,
        "run_status": {
            "completed": run["metrics"].get("completed", True),
            "error": run["metrics"].get("error"),
            "task_status": [t.get("status") for t in run["tasks"]],
            "cost_usd": (run["metrics"].get("biglaw") or {}).get("cost_usd", run["metrics"].get("cost_usd")),
            "duration_seconds": run["metrics"].get("duration_seconds"),
        },
        "deliverables": sorted(run["deliverables"]),
        "criteria_results": results,
    }
    if write:
        (run_dir / "scores.json").write_text(json.dumps(scores, indent=2), encoding="utf-8")
    return scores


def summary_line(s: dict) -> str:
    a, g = s["accuracy"], s["grounding"]
    rs = s.get("run_status") or {}
    status = "" if rs.get("completed", True) else " [TASK FAILED]"
    parts = [f"{s['task']}{status}: {s['n_passed']}/{s['n_criteria']} criteria",
             f"facts {a['facts_detected']}/{a['facts_total']} (grounded {a['facts_grounded']})",
             f"high-sev {a['high_severity_detected']}/{a['high_severity_total']}"]
    if a["traps_total"]:
        parts.append(f"traps {a['traps_passed']}/{a['traps_total']}")
    if a["dispositions_total"]:
        parts.append(f"dispositions {a['dispositions_correct']}/{a['dispositions_total']}")
    if g["citations_total"]:
        parts.append(f"citations verified {g['citations']['verified']}/{g['citations_total']}")
    if g["findings_total"]:
        parts.append(f"findings grounded {g['findings_grounded']}/{g['findings_total']}")
    return " | ".join(parts)
