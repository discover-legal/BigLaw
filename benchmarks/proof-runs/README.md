# Proof runs — multi-practice end-to-end matter drives

The family and employment matter drives (see `docs/operations/evidence-integrity.md`) proved the
evidence-integrity ladder on two practice areas. Proof runs take the same idea to five more and
make it repeatable: each one is a small, realistic, **fully synthetic** matter with **planted facts**
and a **ground-truth answer key**, driven end to end through a live BigLaw backend and scored
**mechanically**, with no judge model, on two axes:

- **Accuracy:** did the deliverable state each planted fact, avoid each planted trap, and (for
  negotiation) reach the right ACCEPT / COUNTER / REJECT on each change?
- **Grounding:** does every finding's cited quote occur verbatim in the document it names? The
  scorer checks this again from the fixture text and does not rely on the engine's own
  `mechanicallyVerified` flag. It also checks whether each planted fact the deliverable states
  is backed by a verified quote from the right document.

The family and employment fixtures were never committed (they were real-shaped client files), so
the harness here is new. It is data-driven: a matter is a directory plus an entry in `matters.json`.

## Matters

| Slug | Matter (`tasks/<id>`) | Docs | Criteria | What is planted |
|---|---|---|---|---|
| `ma-diligence` | `corporate-ma/kestrel-buyside-diligence` | 9 | 19 facts + 1 trap | Virtual data room: SPA draft, charter, sole-source supply, customer MSA, inbound licence, CEO agreement, cap table, lease, logistics. Cap-table vs rep mismatches (common, options, an undisclosed warrant), Series A 60% consent, 1.5x participating preference ($9.0M), charter ROFR, three change-of-control consents, the customer naming the buyer as a "Competitor", a lapsed lease-renewal deadline, a single-trigger $770K CEO payment, a California non-compete, and SPA gaps (consents not a condition, incomplete Schedule 3.6, cap covering fundamental reps). **Trap:** the logistics contract expressly carves out change of control. |
| `msa-negotiation` | `commercial-contracts/lumen-corvane-msa-negotiation` | 3 | 13 dispositions + 6 counter-positions + 2 facts + 1 trap | Our MSA as sent, the customer's numbered redline and the firm playbook. Ground truth is the playbook disposition for each change, plus the permitted counter numbers (24 months, 3x super-cap, 15%, 1.0%, 12 months / 90 days / 50%, …). One change (warranty 90 → 180 days) is **unmarked**. **Trap:** the numbering skips C-13, so a disposition for C-13 was invented. |
| `merger-control` | `competition-regulatory/altamira-sorell-merger-control` | 4 | 14 facts + 2 traps | HSR: aggregation of a prior 8% stake ($212.5M + $16.0M = $228.5M), size-of-person, the foreign-issuer exemption lost on $138.0M of US sales, the $105,000 fee, waiting-period expiry Nov 5 2025, and gun-jumping in the integration plan. EUMR: Art. 1(2) fails (EUR 3,120M), Art. 1(3) met via DE/FR/NL with Italy failing the EUR 25M limb, the two-thirds rule, standstill, and combined shares of 41% (EEA) and 47% (NL). **Traps:** "foreign issuer exempt" and "no EU dimension". Thresholds come from the matter's own reference sheet (2025 HSR figures, EUMR Art. 1 text), so the answers are fixed by the fixtures. |
| `litigation` | `litigation/greystone-v-talbot-defense` | 5 | 18 facts + 2 traps | Complaint, supply contract, correspondence, docket and a FRCP time-computation desk sheet. Three deliverables (chronology, issues list, deadlines). Deadlines need the holiday rules: the answer is due Nov 12 2026 (day 21 is Veterans Day), and the joint status report is due Nov 25 2026 (counted backward past Thanksgiving under Rule 6(a)(5)). Issues: the one-year contractual limitation bars the Year-2 claim, the 35% shortfall-fee cap ($214,340 / $430,570), the lost-profits exclusion, the skipped negotiation/mediation condition, the admitted defect, the s.9.4 effect of termination, and a $286,900 counterclaim. **Traps:** the unreturned Rule 4(d) waiver (Dec 11) and the unrolled Nov 11. |
| `ip-license` | `ip-licensing/quillon-fernhill-license-review` | 4 | 15 facts + 1 trap | Initialled term sheet, the licensor's draft, the patent schedule and the software components. Field, exclusivity, rate and base deviations, post-expiration royalties (Brulotte/Kimble), a lapsed patent, a chain-of-title gap, a pending application, an improvements assignment, a no-challenge clause, a stripped indemnity, assignment, minimum royalty, a statically linked GPL-3.0 component that contradicts the warranty, and a missing escrow. **Trap:** confidentiality matches the term sheet. |

That is **68 planted facts, 13 dispositions + 6 counter-positions and 7 traps (94 criteria)**. Every
document is original synthetic text, and all names, numbers and patent identifiers are fictional.
`run.py validate` checks that every planted fact's evidence string occurs verbatim in its document,
so a perfectly grounded answer is always possible.

## Layout

```
proof-runs/
  matters.json                      registry (workflow, jurisdiction, split mode, cost estimate)
  tasks/<practice>/<matter>/        LAB-compatible: task.json + documents/ + answer_key.json
  proofrun/                         patterns, scorer, validator, mock runs, mock model, drive, smoke
  tests/test_proofrun.py            unit tests (no backend)
  run.py                            CLI
  results/                          run dirs (git-ignored): <task>/<model-dir>/<timestamp>/
```

The tree is laid out like a harvey-labs checkout, so the existing driver
(`../harvey-lab/labdriver`) runs it unchanged. That driver converts, ingests, submits, resolves
gates, renders deliverables and writes metrics. Proof runs add two files to every run dir (LAB runs
get them too): `documents.json` (BigLaw doc id → fixture filename) and `biglaw_tasks.json` (findings
with their citations, synthesis, `groundingAlert`). The scorer writes `scores.json` in LAB's shape
(`score`, `n_passed`, `n_criteria`, `criteria_results[].verdict`), plus `accuracy`, `grounding` and
`run_status` blocks. Because the shape matches, `../harvey-lab/compare.py --results-dir results` and
the recount one-liner in `../runs/README.md` both work. If a task fails, for example on a grounding
collapse, its record is still saved and scored. That failure is a measured outcome.

### Answer-key grammar

A fact's `detect` is `{"all": [[alt, …], …]}`. Every group must match the same deliverable unit
(a paragraph, bullet or table row, prefixed with its heading). Alternatives are plain text
(case- and typography-insensitive), `re:<regex>`, `$money:<n>`, `$date:<YYYY-MM-DD>`, `$pct:<n>` or
`$num:<n>`. The `$` forms match the usual renderings, such as `$9.0 million`, `Nov. 12, 2026` and
`11/12/2026`. A trap trips when a unit matches `forbid` and contains none of its `unless` phrases.
Dispositions read the first `ACCEPT|COUNTER|REJECT` after the change id in its row. The validator
rejects a key whose `model_answer` does not satisfy its own pattern, and a key whose pattern is
already satisfied by the task instructions, so a model cannot score by parroting the prompt.

## Run it

```bash
cd benchmarks/proof-runs
pip install -r ../harvey-lab/requirements.txt     # requests, python-docx, openpyxl, …

python run.py list                                 # matters + criteria counts
python run.py validate                             # fixture/answer-key integrity
python run.py selftest                             # scorer on synthetic oracle/degraded/empty runs
python -m unittest discover -s tests               # unit tests
python run.py smoke --start-backend                # whole pipeline vs a scripted mock model (~3 min/matter, $0)
```

None of the commands above needs model credentials. `smoke` builds the Go binary and starts a
throwaway backend in a temp dir. That backend has all tiers local and local embeddings, pointed at
`proofrun/mock_llm.py`, a scripted server that answers findings prompts with each planted fact and
its verbatim evidence. The real ingest, citation gate, debate, verification, gates, synthesis,
rendering and scoring then all run. Smoke scores measure plumbing, not capability.

### Real drives

Run one backend and drive the matters through it sequentially. The vector DB is single-writer.
Start the backend from the repo root (`go run ./biglaw-go/cmd/biglaw`, or the Docker stack with
`--api http://localhost:3102`) with one of these configurations.

**Claude Haiku (cloud).** This build's vendor breaker requires an explicit opt-in to call
Anthropic directly:

```bash
# Embeddings come from OPENAI_API_KEY (or LOCAL_EMBEDDINGS=true + Ollama nomic-embed-text).
ALLOW_ANTHROPIC=1 MODEL_STACK=custom \
PRIMARY_MODEL_URL=https://api.anthropic.com/v1/ PRIMARY_MODEL_KEY=$ANTHROPIC_API_KEY \
MODEL_HEAVY=claude-haiku-4-5 MODEL_MID=claude-haiku-4-5 MODEL_LIGHT=claude-haiku-4-5 \
OPENAI_API_KEY=$OPENAI_API_KEY \
go run ./biglaw-go/cmd/biglaw

python run.py drive --model-dir biglaw-haiku                 # all five
python run.py drive --matter litigation --model-dir biglaw-haiku
```

**Local qwen2.5:14b (Ollama).** This is the platform's model floor for finding-producing tiers:

```bash
ollama pull qwen2.5:14b && ollama pull nomic-embed-text
LOCAL_INFERENCE_URL=http://localhost:11434/v1 LOCAL_INFERENCE_MODEL=qwen2.5:14b LOCAL_INFERENCE_TIERS=all \
LOCAL_EMBEDDINGS=true OLLAMA_URL=http://localhost:11434 LOCAL_EMBEDDING_MODEL=nomic-embed-text \
EMBEDDING_DIMENSIONS=768 \
go run ./biglaw-go/cmd/biglaw

python run.py drive --model-dir biglaw-qwen14b --timeout 21600
```

Useful flags: `--gate-policy reject` measures the gate as a filter; `--split-mode per-task` runs
one BigLaw task per litigation deliverable at roughly 3x the cost; `--workflow` overrides the
matter's workflow. To rescore a run: `python run.py score --matter <slug> results/<task>/<model-dir>/<ts>`.

**Expected cost and time** per matter, extrapolated from the published SEC-referral runs in
`../runs/` (8 documents: Haiku $1.34 on the release build, $3.74 at 1 round and $11.48 at 6 rounds;
qwen2.5:14b took 4.4 h with $0 of model spend). The proof-run matters have 3–9 shorter documents.
Expect **about $1–12 per matter on Haiku (about $10–50 for all five)** and **2–5 h per matter on
a local 14B**, where cost is metered in `wattHours`. These are extrapolations, not measurements;
`metrics.json` records the actual cost and the real figure goes into `run_status.cost_usd` in
`scores.json`.

**Where results land:** `results/<task>/<model-dir>/<timestamp>/`, holding `output/`, `scores.json`,
`metrics.json`, `config.json`, `transcript.jsonl`, `biglaw_tasks.json` and `documents.json`. To
publish a run, copy its directory into `../runs/` following that README's conventions.

## Known issues surfaced by the smoke

- **Citation parser swallows the Conclusion line.** In `biglaw-go/internal/agents/base.go`,
  `reCitationStrict` ends a QUOTE only at `Evidence:`, `Citation:`, `Confidence:` or `END_FINDING`.
  The agent prompt asks for the order Evidence → Conclusion → Confidence. A finding written exactly
  as the prompt's worked example therefore parses its QUOTE as `<quote> | PAGE=7\nConclusion: …`
  (reproduced with the regex alone). Such a quote cannot verify verbatim. It survives only through
  the gate's 60% word-overlap repair, which rewrites the quote to a source window, and short quotes
  fail outright. The first smoke tripped grounding collapse for this reason (34/51 unverified on
  verbatim quotes). The mock now writes Conclusion first to avoid the bug. The fix belongs in Go
  (add `Conclusion|Content` to the terminator set) and is left to the owners of that code.
- **Driver output.** At the end of a run, the driver prints a harvey-labs `run_eval` hint. Ignore
  it for proof runs: `run.py` scores the run itself.
