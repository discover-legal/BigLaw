#!/usr/bin/env python3
"""Abstracted prompt-grounding harness.

Hits qwen2.5:14b directly (Ollama OpenAI endpoint) with several prompt variants on
a real source excerpt and measures the VERBATIM-QUOTE RATE — the fraction of
evidence quotes that are exact (whitespace-normalized) substrings of the source.
That is the metric that drives EvidenceStatus=grounded in the real pipeline, so
maximizing it here lets us pick a prompt before paying for a 28-min pipeline run.

Run:  python benchmarks/prompt-grounding/grounding_test.py [runs_per_variant]
"""
import json, re, sys, urllib.request, pathlib

OLLAMA = "http://localhost:11434/v1/chat/completions"
MODEL = "qwen2.5:14b"
TEMP = 0.2
RUNS = int(sys.argv[1]) if len(sys.argv) > 1 else 3

SRC = pathlib.Path(__file__).with_name("source.txt").read_text(encoding="utf-8")
TASK = "Extract the key allegations and findings against WCA from the SEC enforcement referral notice below."


def norm(s):
    return re.sub(r"\s+", " ", s or "").strip().lower()


SRC_N = norm(SRC)


def strip_wrap(q):
    q = q.strip()
    for a, b in [('"', '"'), ("'", "'"), ("“", "”"), ("«", "»")]:
        if len(q) >= 2 and q[0] == a and q[-1] == b:
            q = q[1:-1].strip()
    return q


def numbered(src):
    # split into rough sentences and number them
    sents = re.split(r"(?<=[.;:])\s+", src)
    return "\n".join(f"[{i+1}] {s.strip()}" for i, s in enumerate(sents) if s.strip())


SRC_NUM = numbered(SRC)

# Each variant: (system, user_template, quote_regex). {src} and {task} are filled.
VARIANTS = {
    "v1_current_evidence": (
        "You are a securities-litigation analyst.",
        "{task}\n\nSOURCE (id=referral):\n{src}\n\n"
        "For each finding:\nFINDING:\nConclusion: <your analysis, your own words>\n"
        "Evidence: SOURCE=referral | QUOTE=<exact text copied from the source> | PAGE=1\n"
        "END_FINDING\n\nThe Conclusion is your reasoning; the Evidence QUOTE must be copied "
        "character-for-character from the source. NEVER put analysis in a QUOTE.",
        r"QUOTE=(.+?)(?:\s*\|\s*PAGE|\n|END_FINDING|$)",
    ),
    "v2_old_single": (
        "You are a securities-litigation analyst.",
        "{task}\n\nSOURCE (id=referral):\n{src}\n\n"
        "For each finding:\nFINDING:\nContent: <your conclusion>\n"
        "Citation: SOURCE=referral | QUOTE=<verbatim text> | PAGE=1\nEND_FINDING\n\n"
        "CRITICAL: QUOTE must be copied character-for-character from the source — an exact "
        "substring. Do NOT summarise, reword, or shorten it.",
        r"QUOTE=(.+?)(?:\s*\|\s*PAGE|\n|END_FINDING|$)",
    ),
    "v3_quote_first": (
        "You are a securities-litigation analyst. You quote sources exactly.",
        "{task}\n\nSOURCE (id=referral):\n{src}\n\n"
        "For each finding, FIRST copy the exact supporting sentence from the SOURCE between "
        "« and », THEN state your conclusion:\n"
        "FINDING:\nQuote: «<exact sentence copied from the source>»\n"
        "Conclusion: <what it shows>\nEND_FINDING\n\n"
        "The text between «» must appear verbatim in the SOURCE.",
        r"«(.+?)»",
    ),
    "v4_locate_copy_strict": (
        "You are an extraction engine. You only output text that appears verbatim in the source.",
        "{task}\n\nSOURCE (id=referral):\n{src}\n\n"
        "For each allegation: locate the sentence in the SOURCE that states it, and copy that "
        "sentence EXACTLY between « and ». Then add a one-line conclusion. If you cannot "
        "find an exact sentence, do not invent one — skip that allegation.\n"
        "FINDING:\nQuote: «<verbatim sentence>»\nConclusion: <one line>\nEND_FINDING",
        r"«(.+?)»",
    ),
    # Production candidate: v3's quote-first mechanism in the real pipeline format
    # (Evidence: SOURCE=|QUOTE= so the gate can parse+verify), quote BEFORE conclusion,
    # and NO aggressive skip clause (that made v4/v6 bail to NO_FINDINGS).
    "v6_prod_quote_first": (
        "You are a securities-litigation analyst.",
        "{task}\n\nSOURCE (id=referral):\n{src}\n\n"
        "For each finding, FIRST copy the exact supporting sentence from the SOURCE into the "
        "Evidence line, THEN state your Conclusion about it:\n"
        "FINDING:\nEvidence: SOURCE=referral | QUOTE=<a sentence copied character-for-character from the source> | PAGE=1\n"
        "Conclusion: <what that evidence shows — your analysis, in your own words>\n"
        "Confidence: <0.0-1.0>\nEND_FINDING\n\n"
        "The Evidence QUOTE must appear verbatim in the SOURCE — copy it exactly; do not reword, "
        "shorten, or summarise. Write the Evidence first, then the Conclusion. Add more Evidence "
        "lines for additional support. Reply NO_FINDINGS only if you genuinely have no findings.",
        r"QUOTE=(.+?)(?:\s*\|\s*PAGE|\n|END_FINDING|$)",
    ),
    "v5_numbered_sentences": (
        "You are a securities-litigation analyst. You cite by sentence number and copy exactly.",
        "{task}\n\nThe SOURCE is given as numbered sentences:\n{src}\n\n"
        "For each finding, give the supporting sentence's number and copy that sentence verbatim:\n"
        "FINDING:\nConclusion: <your analysis>\n"
        "Evidence: SENT=<number> | QUOTE=<copy sentence [number] exactly, without the [n] tag>\n"
        "END_FINDING\n\nThe QUOTE must be the exact text of the cited [number] sentence.",
        r"QUOTE=(.+?)(?:\s*\|\s*PAGE|\n|END_FINDING|$)",
    ),
}


def call(system, user):
    body = {
        "model": MODEL, "stream": False, "max_tokens": 1200, "temperature": TEMP,
        "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
    }
    req = urllib.request.Request(OLLAMA, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=240))["choices"][0]["message"]["content"]


def main():
    print(f"source: {len(SRC)} chars | model: {MODEL} | temp {TEMP} | {RUNS} runs/variant\n")
    rows = []
    for name, (system, tmpl, qre) in VARIANTS.items():
        src = SRC_NUM if "numbered" in name else SRC
        user = tmpl.format(task=TASK, src=src)
        tot = verb = 0
        samples = []
        for _ in range(RUNS):
            try:
                out = call(system, user)
            except Exception as e:
                print(f"  {name}: call failed: {e}")
                continue
            quotes = [strip_wrap(m) for m in re.findall(qre, out, re.S)]
            quotes = [q for q in quotes if len(q) >= 8]
            for q in quotes:
                tot += 1
                ok = norm(q) in SRC_N
                if ok:
                    verb += 1
                if len(samples) < 2:
                    samples.append(("OK " if ok else "MISS") + " " + repr(q[:70]))
        rate = (100 * verb // tot) if tot else 0
        rows.append((name, tot, verb, rate, round(tot / RUNS, 1)))
        print(f"{name:24s} quotes={tot:3d}  verbatim={verb:3d}  rate={rate:3d}%  (~{tot/RUNS:.1f}/run)")
        for s in samples:
            print("      " + s)
        print()
    rows.sort(key=lambda r: r[3], reverse=True)
    print("=== ranking by verbatim rate ===")
    for name, tot, verb, rate, perrun in rows:
        print(f"  {rate:3d}%  {name}  ({verb}/{tot})")


if __name__ == "__main__":
    main()
