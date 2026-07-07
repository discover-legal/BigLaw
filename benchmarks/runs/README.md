# Scored-run history

This directory is the published record behind every benchmark number in the repo's release
collateral (`docs/benchmarks.md`, `docs/local-accuracy-journey.md`, `CHANGELOG.md`, the
Apache-release deck). It holds **every scored run** on the two Harvey **LAB** tasks BigLaw was
measured on — the good runs, the weak ones, and the invalidated ones alike. Nothing is curated
out.

- **Judge:** `claude-sonnet-4-6` throughout, on every run in this tree. The grading rubric was
  hidden from the agents (judge-only); gains had to be real, not taught to the test.
- **Scoring:** LAB is an **all-pass** rubric — a *task* scores `1.0` only at a perfect 60/60 (or
  23/23). Neither task is passed. The tracked signal is the **criteria-passed count**, not the
  binary `score` field (which reads `0.0` for every run here).

## What's in each run directory

```
<task>/<modeldir>/<timestamp>/
  scores.json      # judge verdict per criterion + n_passed / n_criteria
  metrics.json     # tokens, wall time, cost_usd (cloud), gates
  config.json      # model, agent, task, workflow, split mode
  transcript.jsonl # agent transcript (copied when < 200 KB)
  output/          # the graded deliverable — headline runs only (see tables), to keep the tree small
```

Every scored run carries its metadata quartet. Deliverables (`output/`) are copied only for the
runs cited in collateral — copying them for all 72 would bloat the tree; the score of any other
run is fully reproducible from its `scores.json` without it.

## Recount a score yourself

The number in the tables below is `n_passed` in `scores.json`. To recount from the raw verdicts:

```bash
python -c "import json,sys;d=json.load(open(sys.argv[1]));print(sum(c['verdict']=='pass' for c in d['criteria_results']),'/',d['n_criteria'])" scores.json
```

(or `jq '[.criteria_results[]|select(.verdict==\"pass\")]|length' scores.json`)

## Invalidated, voided, and degraded runs — read before quoting

The record keeps its bad runs on purpose; these must **not** be quoted as capability numbers:

- **`glm52-fixwave/20260706-131113` — 7/60, INVALIDATED.** A GLM-5.2 *thinking*-mode run, ~14 h,
  operator-interrupted at ~24.7M tokens with a malformed deliverable. Not a capability number.
- **`biglaw-apache-release/20260703-130134` — 27/60, VOIDED.** The release-era local qwen run:
  every DyTopo round timed out under three-way task contention, so the 27 reflects the BELO
  deviation layer alone, not a healthy pipeline. Superseded by `qwen-v4` (39).
- **`haiku-rounds3/20260706-003200` — 50/60, SUPERSEDED.** This 3-round run's analysis round
  starved on a transient provider outage (HTTP 400s). A subsequent resilience pass (call
  backoff, durable agent recruitment, loud round-error signaling) plus re-entrant machinery
  made a genuinely healthy 3-round measurement possible: `haiku-v5b/20260707-144125` scores the
  same **50/60** with every round intact — confirming this run's number, not correcting it.
- **`glm52-fast/20260706-044640` (51) and `glm52-fast/20260706-120209` (47) — SUPERSEDED.** Both
  runs of the original GLM-5.2 "fast" pair had one of three rounds (analysis) return zero
  findings under provider rate-limiting. The resilience pass fixed the underlying call-rate
  self-throttling; `glm52-v4b/20260707-113848` scores **52/60** with all rounds intact — the
  current cross-vendor high, and the number now cited in collateral without qualification.

For context on corrected claims: the once-quoted **"30/60"** was a two-run *union* of passed
criteria (a coverage measure), not a single-run score; the verified single-run qwen peak of that
era was **28** (`biglaw-clean2/20260623-170105`). The docs say so.

## Inventory

### SEC enforcement-referral extraction (60-criterion) — 64 scored runs

Task: `white-collar-defense-investigations/extract-key-allegations-from-sec-enforcement-referral-notice`

| Score | Run (`modeldir/timestamp`) | Deliverables | Note |
|---|---|---|---|
| **52/60** | `glm52-v4b/20260707-113848` | `output/` | GLM-5.2, resilience wave + re-entrant machinery, all 3 rounds intact — current cross-vendor high |
| **51/60** | `glm52-fast/20260706-044640` | `output/` | superseded by `glm52-v4b` (52) |
| **50/60** | `haiku-v5b/20260707-144125` | `output/` | claude-haiku-4-5, resilience wave + re-entrant machinery + writer-discipline fix, all 3 rounds intact |
| **50/60** | `haiku-rounds3/20260706-003200` | `output/` | confirmed (not corrected) by `haiku-v5b` |
| **49/60** | `biglaw-fixwave-haiku/20260705-033626` | `output/` | healthy 6-round fix-wave Haiku |
| **48/60** | `haiku-unshackled/20260706-041439` | `output/` | 3-round unshackled Haiku |
| **47/60** | `glm52-fast/20260706-120209` | `output/` | superseded by `glm52-v4b` (52) |
| **41/60** | `haiku-raw/manual-001` | `output/` | Harvey's own raw harness (read 7 of 8 documents) |
| **41/60** | `haiku-rounds1/20260706-003158` | `output/` | 1-round fix-wave Haiku |
| **39/60** | `qwen-v4/20260707-031344` | `output/` | local qwen2.5:14b, resilience wave + re-entrant machinery, new record (measured ahead of the writer-discipline fix) |
| **37/60** | `biglaw-haiku-fix/20260626-204440` | `output/` | best single-run Haiku, June-26 build |
| **36/60** | `biglaw-fixwave-qwen/20260705-033309` | `output/` | local qwen2.5:14b, fix-wave, superseded by `qwen-v4` (39) |
| **35/60** | `biglaw-haiku/20260626-183226` | — |  |
| **34/60** | `biglaw-haiku-release/20260703-035054` | `output/` | release build, Haiku ($1.34) |
| **34/60** | `biglaw-sonnet-release/20260703-141449` | `output/` | release build, Sonnet ($13.70; 31 of 34 the same criteria) |
| **28/60** | `biglaw-clean2/20260623-170105` | `output/` | verified single-run qwen peak of its era |
| **27/60** | `biglaw-7b-paged/20260627-105946` | — |  |
| **27/60** | `biglaw-apache-release/20260703-130134` | — | VOIDED — every DyTopo round timed out under contention |
| **26/60** | `biglaw-7b-paged3/20260627-165825` | — |  |
| **26/60** | `biglaw-belo-analytic/20260630-152555` | — |  |
| **26/60** | `biglaw-egraph/20260625-011059` | — |  |
| **26/60** | `biglaw-paged/20260624-184600` | — |  |
| **25/60** | `biglaw-7b-belo/20260628-150727` | — |  |
| **25/60** | `biglaw-7b-figfix/20260627-191620` | — |  |
| **24/60** | `biglaw-neuro/20260623-153558` | — |  |
| **23/60** | `biglaw-7b-adjud/20260627-204735` | — |  |
| **23/60** | `biglaw-7b-fix2/20260626-221949` | — |  |
| **23/60** | `biglaw-huddle/20260626-162037` | — |  |
| **23/60** | `biglaw-mix-xcut/20260630-123544` | — |  |
| **22/60** | `biglaw-plumb/20260623-112340` | — |  |
| **21/60** | `biglaw-fig2/20260626-005358` | — |  |
| **21/60** | `biglaw-mix-7b14b/20260630-070042` | — |  |
| **20/60** | `biglaw-7b-paged2/20260627-123733` | — |  |
| **20/60** | `biglaw-spine3/20260625-141816` | — |  |
| **19/60** | `biglaw-egraph2/20260625-030718` | — |  |
| **19/60** | `biglaw-fig/20260625-225830` | — |  |
| **18/60** | `biglaw-cover/20260624-152819` | — |  |
| **18/60** | `biglaw-noregex/20260626-133400` | — |  |
| **17/60** | `biglaw-7b-canon/20260628-032209` | — |  |
| **17/60** | `biglaw-7b-spine/20260626-234714` | — |  |
| **17/60** | `biglaw-spine2/20260625-122257` | — |  |
| **17/60** | `biglaw-synth3/20260624-024310` | — |  |
| **15/60** | `biglaw-synth/20260623-204048` | — |  |
| **14/60** | `biglaw-hard/20260623-192517` | — |  |
| **14/60** | `biglaw-spine4/20260625-181506` | — |  |
| **13/60** | `biglaw-spine/20260622-150724` | — |  |
| **13/60** | `biglaw-synth2/20260623-215603` | — |  |
| **12/60** | `biglaw-fx/20260626-105156` | — |  |
| **12/60** | `biglaw-keyfig/20260622-191633` | — |  |
| **11/60** | `biglaw-det/20260622-175803` | — |  |
| **11/60** | `biglaw-fig/20260622-163138` | — |  |
| **11/60** | `biglaw-full/20260623-180158` | — |  |
| **11/60** | `biglaw-sweep/20260623-012228` | — |  |
| **10/60** | `biglaw-7b-spinefix/20260629-062923` | — |  |
| **10/60** | `biglaw-table/20260621-145732` | — |  |
| **10/60** | `biglaw-writer/20260620-203114` | — |  |
| **9/60** | `biglaw-keyfix/20260622-222109` | — |  |
| **8/60** | `biglaw-critic/20260622-205502` | — |  |
| **7/60** | `glm52-fixwave/20260706-131113` | `output/` | INVALIDATED — GLM-5.2 thinking, operator-interrupted |
| **6/60** | `biglaw-syn/20260622-052436` | — |  |
| **0/60** | `biglaw-14b-fix/20260627-014838` | — |  |
| **0/60** | `biglaw-haiku/20260626-165240` | — |  |
| **0/60** | `biglaw-haiku-fix/20260626-202352` | — |  |
| **0/60** | `biglaw-qwen-local/20260615-215255` | — | early qwen baseline |

### Trust-vs-instructions compare (23-criterion) — 11 scored runs

Task: `trusts-estates-private-client/compare-trust-documents-against-client-instructions`

| Score | Run (`modeldir/timestamp`) | Deliverables | Note |
|---|---|---|---|
| **15/23** | `qwen-devport/20260705-213725` | `output/` | local qwen2.5:14b — current task record |
| **14/23** | `haiku-devport/20260705-213721` | `output/` | deviation-path port, Haiku |
| **12/23** | `biglaw-paired3/20260702-124218` | `output/` | prior deviation-tuned local record |
| **11/23** | `biglaw-comprehensive/20260702-191845` | — |  |
| **9/23** | `haiku-after-fixwave/20260705-053648` | `output/` | fix-wave transfer (no task tuning) |
| **8/23** | `biglaw-consist2/20260702-162409` | — |  |
| **7/23** | `biglaw-general/20260701-184515` | — |  |
| **6/23** | `haiku-before-fixwave/20260705-053622` | `output/` | pre-fix-wave baseline |
| **3/23** | `biglaw-deviations/20260701-011842` | — |  |
| **3/23** | `biglaw-epistemic/20260630-210951` | — |  |
| **3/23** | `biglaw-grounded-dev/20260701-031700` | — |  |

---

*72 scored runs total (61 SEC + 11 trust). Only runs with a `scores.json` are published here;
un-scored exploratory runs are omitted.*
