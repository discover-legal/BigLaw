"""Fixture validation — run before any paid drive.

Checks, per matter:
1. task.json has instructions + deliverables; registry entry agrees with it.
2. Every document converts to text exactly as the driver will ingest it.
3. Every planted fact / trap / disposition names documents that exist, and every
   ``evidence`` string occurs verbatim (after normalisation) in one of them —
   i.e. a perfectly grounded answer is possible.
4. Every pattern compiles; each fact's ``model_answer`` satisfies its own
   ``detect`` pattern (the key is self-consistent) and trips no trap.
5. No ``detect`` pattern is already satisfied by the task instructions (a model
   that parrots the prompt must not score), nor by an empty deliverable.
"""

from __future__ import annotations

from .matter import Matter
from .patterns import normalize, normalize_quote, pattern_matches, split_units, validate_pattern


def validate_matter(m: Matter) -> list[str]:
    errs: list[str] = []
    t, key = m.task, m.key
    if not str(t.get("instructions", "")).strip():
        errs.append("task.json: empty instructions")
    if not t.get("deliverables"):
        errs.append("task.json: no deliverables")
    if t.get("id") and t["id"] != m.id:
        errs.append(f"task.json id {t['id']!r} != registry id {m.id!r}")
    if key.get("matter") != m.id:
        errs.append(f"answer_key.json matter {key.get('matter')!r} != {m.id!r}")
    if not m.documents:
        errs.append("no documents")

    norm_docs = {n: normalize_quote(x) for n, x in m.documents.items()}
    instr_units = [normalize(u) for u in split_units(str(t.get("instructions", "")))]
    traps = key.get("traps", [])
    seen = set()

    def check_evidence(item: dict, label: str) -> None:
        docs = item.get("documents") or []
        for d in docs:
            if d not in m.documents:
                errs.append(f"{label}: unknown document {d!r}")
        for ev in item.get("evidence") or []:
            q = normalize_quote(ev)
            if not any(q in norm_docs.get(d, "") for d in docs):
                errs.append(f"{label}: evidence not found verbatim in {docs}: {ev[:80]!r}")
        if not item.get("evidence"):
            errs.append(f"{label}: no evidence strings")

    for f in key.get("facts", []):
        label = f"fact {f.get('id')}"
        if f.get("id") in seen:
            errs.append(f"{label}: duplicate id")
        seen.add(f.get("id"))
        check_evidence(f, label)
        perrs = validate_pattern(f.get("detect") or {})
        errs += [f"{label}: {e}" for e in perrs]
        if perrs:
            continue
        ans = f.get("model_answer", "")
        if not any(pattern_matches(f["detect"], u) for u in split_units(ans)):
            errs.append(f"{label}: model_answer does not satisfy its own detect pattern")
        for tr in traps:
            if pattern_matches(tr["forbid"], ans) and not any(normalize(x) in normalize(ans) for x in tr.get("unless", [])):
                errs.append(f"{label}: model_answer trips trap {tr['id']}")
        if any(pattern_matches(f["detect"], u, normalized=True) for u in instr_units):
            errs.append(f"{label}: detect pattern is satisfied by the task instructions (answer leaks from prompt)")

    for tr in traps:
        label = f"trap {tr.get('id')}"
        check_evidence(tr, label)
        errs += [f"{label}: {e}" for e in validate_pattern(tr.get("forbid") or {})]
        viol = tr.get("model_violation", "")
        if viol and not (pattern_matches(tr["forbid"], viol)
                         and not any(normalize(x) in normalize(viol) for x in tr.get("unless", []))):
            errs.append(f"{label}: model_violation does not trip the trap (trap is untestable)")

    for d in key.get("dispositions", []):
        label = f"disposition {d.get('id')}"
        if d.get("expected") not in ("ACCEPT", "COUNTER", "REJECT"):
            errs.append(f"{label}: expected must be ACCEPT|COUNTER|REJECT")
        check_evidence(d, label)
        if d.get("position"):
            errs += [f"{label}: {e}" for e in validate_pattern(d["position"])]
            if not pattern_matches(d["position"], d.get("model_answer", "")):
                errs.append(f"{label}: model_answer does not satisfy its position pattern")

    n = len(key.get("facts", [])) + len(traps) + len(key.get("dispositions", []))
    if n == 0:
        errs.append("answer key has no criteria")
    return errs
