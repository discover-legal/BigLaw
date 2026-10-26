# Benchmarks

The benchmark suite is committed code in [`eval/bench/`](../eval/bench). Its one rule is that every
run is comparable to every other run. A run reads all of its settings from one pinned config file.
It writes one summary in a fixed schema. Two summaries compare only when they were run under the
same config.

Before this suite, benchmarks ran from ad-hoc scripts kept outside the repo, with settings set by
hand for each run and results left in loose JSON files. Those runs are kept as
[baselines](#baselines). Their settings were never recorded, so the compare tool reports every
comparison with them as not comparable.

## Running it

```bash
export ANTHROPIC_API_KEY=…          # or ANTHROPIC_API_KEY_FILE=/path/to/a/file/holding/it
git -C ~/harvey-labs checkout 81d6a31
make bench                          # = python3 eval/bench/run.py run --labs-dir ~/harvey-labs
make bench LABS_DIR=/elsewhere/harvey-labs
```

One command runs the whole suite. It needs Go, Python 3, `uv` (for the harvey-labs judge), `psql`,
and either Docker or `BIGLAW_DATABASE_URL`. In order, it:

1. **Refuses to start** when any of these holds. Nothing has run and nothing is spent at that point.
   - The config's content does not match its `config_hash`.
   - Any `OPENAI_*` variable is set. The refusal names the variables, never their values.
   - No key is set.
   - The harvey-labs checkout is not at the pinned commit, or its tracked files are modified.
   - The BigLaw checkout has uncommitted changes. `--allow-dirty` runs it anyway and records
     `build.dirty`.
2. **Starts Postgres.** It starts a throwaway `pgvector/pgvector:0.8.6-pg16` container on a loopback
   port and removes it at the end. If `BIGLAW_DATABASE_URL` is set, it uses that database instead,
   which must hold no tasks. Runs share caches (figure readings and judge verdicts), so a used
   database would make a run incomparable. `--reuse-db` overrides this and records `fresh_db: false`.
3. **Builds the binary** at HEAD (static, `-X main.version=bench-<sha7>`).
4. **Starts `biglaw serve`** with exactly the config's settings, from an empty environment. No
   `BIGLAW_*`, `ANTHROPIC_BASE_URL` or other variable from your shell reaches it. It then checks that
   `/health` reports the built version and the pinned search mode.
5. **Runs each task in order** through the LAB driver (`eval/lab/labdriver`), then scores it:
   - A LAB task is scored by harvey-labs' own `run_eval`, single judge
     (`--judges claude-sonnet-4-6`). It runs in the suite's own `LAB_ROOT`, so no harvey-labs `.env`
     is read.
   - A proof run is scored by `eval/proofrun`.
6. **Enforces the spend cap.** A task starts only when what has been charged so far, plus the
   task's reserve, fits under `spend_cap_usd`. The reserve is BigLaw's per-task budget, plus the
   judge's reserve for a LAB task. When a task does not fit, the suite stops before it.
   - A finished task is charged its cost ledger's USD.
   - A task whose cost is unknown (an unpriced call) is charged its whole budget.
   - A judged task is also charged the judge's reserve, because harvey-labs reports no judge cost.
7. **Stops immediately on a key or credit refusal**, whether BigLaw or the judge hits it. This
   covers a 401 or 403, a 402, or Anthropic's `credit balance is too low`.
   - On every poll, the suite reads the task's error and its new steps.
   - On a refusal, it cancels the task and marks the remaining tasks `not_run`.
   - It reports the provider's error type, never its message, and exits with code 3.
8. **Writes `eval/bench/results/<UTC date>-<sha7>/summary.json`**, validated against the schema,
   and prints a table of the results.

The key is read only from `ANTHROPIC_API_KEY`, or from the file that `ANTHROPIC_API_KEY_FILE`
names. It reaches serve as `BIGLAW_MODEL_KEY` and the judge as `ANTHROPIC_API_KEY`, and nothing else.
It is never printed, logged or written to results. Logs pass through a redactor as a second
safeguard.

Other commands:

```bash
python3 eval/bench/run.py check [summary.json …]   # the configs, the baselines and any summaries against their schemas (make eval runs it)
python3 eval/bench/run.py pin                      # re-pin config.json after a deliberate edit
make bench-smoke                                   # the offline smoke (below); needs BIGLAW_TEST_DSN
```

## What is pinned, and why

[`eval/bench/config.json`](../eval/bench/config.json) is checked against
[`schema/config.schema.json`](../eval/bench/schema/config.schema.json):

| Setting | Value | Why |
|---|---|---|
| `models` | lead, specialist and critic all `claude-haiku-4-5` | Every subagent and figure agent runs on the lead model. No vision or embed role. |
| `model_url` | `https://api.anthropic.com/v1` | One endpoint. `ANTHROPIC_BASE_URL` is unset for every child process. OpenAI hosts and non-`claude-` ids are refused. |
| `budget` | `$11`, `900` calls per task (`BIGLAW_BUDGET_USD`, `BIGLAW_BUDGET_CALLS`) | Bench6's run-ids carry `b11`, and its SEC run used 566 calls with 334 remaining. |
| `search` | `lexical` | No embed role. `/health` must report it. |
| `harness` | harvey-labs `81d6a31` | The judge's prompts and parsing are part of the score. |
| `judge` | `claude-sonnet-4-6`, single judge, a `$2` reserve per LAB task | The judge of the v1 record and of bench6. The reserve is what the cap charges for the judge. |
| `spend_cap_usd` | `$40` per suite | The three tasks reserve $13 + $13 + $11 = $37. |
| `driver` | memo workflow, tool bandit on, finding gates approved, BigLaw's default `maxSteps`, 4 h timeout | The drivers' defaults, with a timeout long enough for bench6's 68-minute SEC run. |
| `tasks` | `sec`, `trust`, `gate-a-litigation` | The three tasks earlier runs used (below). |
| `postgres_image` | `pgvector/pgvector:0.8.6-pg16` | As Compose runs. |

The three tasks:

| id | kind | task |
|---|---|---|
| `sec` | LAB | `white-collar-defense-investigations/extract-key-allegations-from-sec-enforcement-referral-notice` (60 criteria) |
| `trust` | LAB | `trusts-estates-private-client/compare-trust-documents-against-client-instructions` (23 criteria) |
| `gate-a-litigation` | proof run | `litigation/greystone-v-talbot-defense` (`eval/tasks`, 20 criteria) |

`config_hash` is the sha256 of the config's canonical JSON, with sorted keys and without the hash
itself. A run refuses a config whose content no longer matches its hash, so no setting can drift by
hand. To change a setting on purpose, edit the file, run `run.py pin` and commit both. Every result
made under the old hash then stops comparing with new ones.

## Results

Each run is a directory: `eval/bench/results/<UTC date>-<sha7>[-n]/`. Only its `summary.json` is
committed. Everything else is ignored by
[`results/.gitignore`](../eval/bench/results/.gitignore):

- `logs/` holds the bench, serve and judge logs.
- `lab/` is the suite's `LAB_ROOT`, with the run dirs, deliverables and harvey-labs' `scores.json`
  and `report.html`.
- `proofrun/` holds the proof-run run dirs.
- `build/` holds the binary.

The summary's schema is [`schema/summary.schema.json`](../eval/bench/schema/summary.schema.json).
Every key is always present, and null means unknown (never zero).

The run as a whole records:

- `build` (sha, dirty);
- `config_hash`, and the whole `config` as it ran;
- `harness` (commit, dirty) and `judge` (models, runner);
- `runner` (host, date, Python);
- `fresh_db`;
- `spend` (cap, charged, actual);
- `stopped` (`spend_cap` or `provider_error`, with the provider's error type).

Each task records:

| Field | What it is |
|---|---|
| `score` | Passed and total. |
| `criteria` | id → `pass`/`fail` from the judge or scorer. |
| `cost_usd`, `calls` | From `GET /tasks/{id}/cost`. |
| `stop_reason` | The run's. |
| `duration_s` | Wall-clock duration. |
| `stage_spend` | USD per stage: the tool of each step that spent, and `lead` for the lead's turns. |
| `stage_calls` | Model calls per tool. This is the tool bandit's `tool_stats` delta over the task: a tool counts the calls of the subagents it ran inside its step, so stages overlap. |
| `reader` | The figure reader's calls, failed and unfunded, from the cost's reader line. |
| `judge` | The figure judge's calls (`stage_calls.judge_figures`) and the pairs it judged (ledger `verdict` rows). |
| `failures` | Failed and refused steps, and the judge's grading errors. |
| `conflicts` | `verdict` rows judged `yes`: pairs of figures that the judge decided are one quantity stated differently. |
| `computed_figures` | The Computed figures block's items per tier (check first, arithmetic). |
| `finding_gates` | Finding gates the driver decided. |
| `charged_usd` | What the cap charged. |
| `run_dir`, `error`, `notes` | Where the run's files are, any error, and any notes. |

`conflicts` and `judge` are read from the suite's own database with `psql`. Without `psql` they
are null, with a note.

## Comparing

```bash
python3 eval/bench/compare.py eval/bench/results/<a>/summary.json eval/bench/results/<b>/summary.json
python3 eval/bench/compare.py --json BASE NEW            # machine-readable
python3 eval/bench/compare.py --require-comparable BASE NEW   # exit 1 when not comparable
```

For each task that both runs hold, the compare tool prints:

- the score delta;
- the criteria that flipped, as `gained` (fail → pass) and `lost` (pass → fail), and any criterion
  that is neither pass nor fail on one side;
- the cost, call and stop-reason changes.

Two runs are comparable only when they record the same config hash, mode, harness commit and judge
models, and neither ran on a used database. Otherwise the output starts with **NOT COMPARABLE**
and the reasons, and it lists every config key that differs (`budget.usd: 11 → 15`). It also warns
when:

- a build was dirty;
- a run stopped early;
- the rubric sizes differ;
- both runs are the same commit, so the deltas are run-to-run variance.

## Baselines

The baselines are in [`eval/bench/baselines/`](../eval/bench/baselines). They use the summary schema
with `kind: "baseline"`, and they hold only recorded numbers. Anything not recorded is null.

| Baseline | Build | SEC | trust | gate A litigation | Source |
|---|---|---|---|---|---|
| `v1-record` | v1 (`main`), Haiku | 49/60 | 14/23 | — | v1 scores.json record. Per-criterion results, cost and calls are unknown. |
| `v2-bench6` | `091bcfc` | 38/60, $10.27, 566 calls, `budget_exhausted` | 11/23, $8.57, 443 calls, `finished` | 20/20, $9.60, 444 calls, `budget_exhausted` | Per-criterion verdicts for SEC and trust are from the runs' harvey-labs `scores.json`. Gate A's are unknown. |

Both baselines were judged by `claude-sonnet-4-6` as a single judge. Bench6 records its flips
against bench5:

- SEC gained C-002, C-044, C-047, C-056 and lost C-015, C-023, C-024, C-025, C-026, C-028, C-052,
  C-059.
- trust gained C-013, C-018 and lost C-010, C-011, C-016.

Neither baseline has a config hash, so a new run compared with either is flagged NOT COMPARABLE.
The deltas are still printed, as context rather than as a measurement. The first run of this
suite becomes the first comparable reference.

## The offline smoke

`TestBenchSmoke` ([`eval/bench_test.go`](../eval/bench_test.go)) runs the same pipeline with no key
and no credit, using [`smoke.json`](../eval/bench/smoke.json). It runs in `make ci`'s `go test`
(a few seconds, and it needs the test database), and `make bench-smoke` runs it alone. It serves the
real API and engine over scripted models (`model.Fake`, the figure reader's stand-in), then runs
`run.py smoke` against them. It checks the following:

- A LAB-shaped task (`eval/bench/smoke/tasks/smoke/deadline`) is driven and judged by
  [`stubjudge.py`](../eval/bench/stubjudge.py), which has harvey-labs' command line and
  `scores.json` shape and passes a criterion when its `stub_contains` phrase is in the deliverable.
  The task scores 2/3, with C-003 failing by design.
- The gate A litigation proof run (the E1 oracle) is driven and scored by `eval/proofrun` at 20/20.
- Cost, calls, the reader line, Computed figures and finding gates are measured. When `psql` is
  installed, the database readings are measured too.
- The $12 cap stops the suite before the third task. The first two are charged $6 and $5, because
  unpriced calls are charged at the $5 budget and the judge's $1 reserve is added.
- The summary validates, and it compares as comparable with itself and not comparable with
  `v2-bench6`.
- The smoke's dummy secret appears in no file the suite wrote.

[`eval/tests/test_bench.py`](../eval/tests/test_bench.py) (in `make eval`) unit-tests the parts that
the smoke does not reach:

- config validation, the hash, and refusals of hand edits;
- the key's sources, and refusals that never show it;
- the `OPENAI_*` refusal;
- the `run` command's refusals before anything starts;
- the cap's arithmetic, including unknown cost and a provider refusal mid-suite;
- schema validation of good and bad summaries;
- provider-error detection (types only, rate limits ignored);
- compare's deltas and flips, and every not-comparable rule.

## Adding a task

1. Add the task to `tasks` in `eval/bench/config.json`. Give it an `id` (lowercase, dashes), a
   `kind` and a `task` path:
   - `lab`: a path under harvey-labs' `tasks/` at the pinned commit.
   - `proofrun`: a matter id in `eval/matters.json`.

   Also give it the `jurisdiction`, `split_mode` and `matter` it should run with.
2. Check that `spend_cap_usd` still covers every task's reserve. `run.py check` reports it when the
   cap cannot fund even one task. Raise the cap in the same change if the suite should still run
   every task.
3. Run `python3 eval/bench/run.py pin` and commit the config with its new hash. Results under the
   old hash are not comparable with results under the new one, which is the point: a suite with
   another task is another suite.
4. If the task has a recorded earlier result, add it to a baseline with its source. Put null where
   a number was not recorded.
