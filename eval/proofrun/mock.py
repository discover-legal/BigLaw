# SPDX-License-Identifier: Apache-2.0
# Copyright (C) 2026 Discover Legal
"""Synthesise recorded runs from an answer key, for scorer self-tests.

These are NOT model results. They exist so the scoring code can be exercised
end to end — and its numbers asserted — without model credentials:

* ``oracle``    every fact stated, every finding cites its evidence verbatim,
                every disposition correct, no trap tripped. Must score 100%.
* ``degraded``  a realistic failure profile: every other fact missing, a third
                of findings carry fabricated or misattributed quotes, each trap
                tripped, every third disposition wrong. Expected numbers are
                computed by ``expected_degraded`` so tests assert exact values.
* ``empty``     the empty deliverable — must detect nothing and trip no trap.

Run dirs mirror what the driver writes: output/<deliverable>, biglaw_tasks.json,
documents.json, config.json, metrics.json.
"""

from __future__ import annotations

import json
from pathlib import Path

from .matter import Matter
from .patterns import normalize_quote, pattern_matches

_WRONG = {"ACCEPT": "REJECT", "COUNTER": "ACCEPT", "REJECT": "ACCEPT"}


def _doc_ids(m: Matter) -> dict[str, str]:
    return {f"doc-{i:03d}": name for i, name in enumerate(sorted(m.documents), 1)}


def _doc_containing(m: Matter, docs: list[str], quote: str) -> str:
    q = normalize_quote(quote)
    return next((d for d in docs if q in normalize_quote(m.documents.get(d, ""))), docs[0])


def _disposition_row(d: dict, disp: str) -> str:
    rationale = d.get("model_answer", "") or d["title"]
    return f"| {d['id']} | {d['title']} | {disp} | {rationale} |"


def build(m: Matter, profile: str, run_dir: Path) -> Path:
    key = m.key
    facts = key.get("facts", [])
    disps = key.get("dispositions", [])
    traps = key.get("traps", [])
    ids = _doc_ids(m)
    name_to_id = {v: k for k, v in ids.items()}

    paras: list[str] = [f"# Proof-run mock deliverable ({profile})", ""]
    findings = []
    for i, f in enumerate(facts):
        keep = profile == "oracle" or (profile == "degraded" and i % 2 == 0)
        quote = f["evidence"][0]
        doc = _doc_containing(m, f["documents"], quote)
        cit_quote, cit_source = quote, name_to_id[doc]
        if profile == "degraded" and i % 3 == 1:
            cit_quote = "the parties agreed that all consents had already been obtained in full"  # fabricated
        elif profile == "degraded" and i % 3 == 2:
            other = next((n for n in sorted(m.documents) if n not in f["documents"]), doc)
            cit_source = name_to_id[other]  # real quote, wrong document
        if keep:
            paras += [f"## {f['id']}", f"{f['model_answer']} (Source: {doc}.)", ""]
        if profile != "empty":
            ok = cit_quote == quote and cit_source == name_to_id[doc]
            findings.append({  # the v2 Finding shape (DESIGN § UI contract)
                "fid": f"F{i + 1}",
                "claim": f["model_answer"] if keep else "Reviewed the document; no issue identified.",
                "status": "verified" if ok else "quarantined",
                "citations": [{"documentId": cit_source, "quote": cit_quote, "method": "exact" if ok else "none"}],
            })
    if profile == "degraded":
        for tr in traps:
            paras += [tr["model_violation"], ""]
    if disps and profile != "empty":
        paras += ["## Responses to the redline", "",
                  "| Change | Clause | Disposition | Rationale |", "|---|---|---|---|"]
        for i, d in enumerate(disps):
            disp = d["expected"] if profile == "oracle" or i % 3 != 0 else _WRONG[d["expected"]]
            paras.append(_disposition_row(d, disp))
        paras.append("")

    body = "\n".join(paras) if profile != "empty" else ""
    out = run_dir / "output"
    out.mkdir(parents=True, exist_ok=True)
    deliverables = m.task.get("deliverables") or ["deliverable.md"]
    # Multi-deliverable tasks: the whole mock body goes in the first file, the
    # rest stay empty — exercises the scorer's cross-deliverable concatenation.
    for j, name in enumerate(deliverables):
        (out / name).write_text(body if j == 0 else "", encoding="utf-8")
    (run_dir / "biglaw_tasks.json").write_text(json.dumps([{
        "id": f"mock-{profile}", "status": "complete", "output": body, "findings": findings,
    }], indent=2), encoding="utf-8")
    (run_dir / "documents.json").write_text(json.dumps(ids, indent=2), encoding="utf-8")
    (run_dir / "config.json").write_text(json.dumps({
        "model": f"mock-{profile}", "agent": "proofrun-mock", "task": m.id,
        "note": "SYNTHETIC scorer self-test run - not a model result",
    }, indent=2), encoding="utf-8")
    (run_dir / "metrics.json").write_text(json.dumps({"cost_usd": 0.0, "mock": True}, indent=2), encoding="utf-8")
    return run_dir


def expected_degraded(m: Matter) -> dict:
    """Exact numbers the scorer must report for the degraded profile.

    Detection is recomputed from the kept model answers rather than assumed to
    be "the kept facts": answer keys legitimately overlap (a paragraph that
    computes the Year 2 shortfall fee also states the Year 2 amount), so an
    omitted fact counts as detected when a kept answer covers it.
    """
    facts = m.key.get("facts", [])
    disps = m.key.get("dispositions", [])
    kept = [i for i in range(len(facts)) if i % 2 == 0]
    answers = [facts[i]["model_answer"] for i in kept]
    detected = [j for j, f in enumerate(facts) if any(pattern_matches(f["detect"], a) for a in answers)]
    # Grounded = detected AND some finding whose content states the fact carries a
    # verified quote from the fact's documents. Only findings for kept facts state
    # anything, and only i % 3 == 0 citations verify against the right document.
    def finding_grounds(i: int, j: int) -> bool:
        if i % 3 != 0:
            return False
        doc = _doc_containing(m, facts[i]["documents"], facts[i]["evidence"][0])
        return doc in facts[j]["documents"] and pattern_matches(facts[j]["detect"], facts[i]["model_answer"])
    grounded = [j for j in detected if any(finding_grounds(i, j) for i in kept)]
    return {
        "facts_detected": len(detected),
        "facts_grounded": len(grounded),
        "traps_passed": 0,
        "dispositions_correct": sum(1 for i in range(len(disps)) if i % 3 != 0),
        "citations_verified": sum(1 for i in range(len(facts)) if i % 3 == 0),
        "citations_misattributed": sum(1 for i in range(len(facts)) if i % 3 == 2),
        "citations_unverified": sum(1 for i in range(len(facts)) if i % 3 == 1),
    }
