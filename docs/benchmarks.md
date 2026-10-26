# Benchmarks

The benchmark suite is committed code in [`eval/bench/`](../eval/bench). Its one rule is that every
run is comparable to every other run. A run reads all of its settings from one pinned config file.
It writes one summary in a fixed schema. Two summaries are reported comparable only when they ran
under the same config, from clean builds, on fresh databases, scored by the same yardstick.

Before this suite, benchmarks ran from ad-hoc scripts kept outside the repo, with settings set by
hand for each run and results left in loose JSON files. Those runs are kept as
[baselines](#baselines). One of them has a reconstructed config, so a new run compares with it
"with caveats". The other has none, so it never compares.

## Running it

```bash
export ANTHROPIC_API_KEY=…          # or ANTHROPIC_API_KEY_FILE=/path/to/a/file/holding/it
git -C ~/harvey-labs checkout 81d6a31
python3 eval/bench/run.py preflight --labs-dir ~/harvey-labs   # optional: everything up to serve's /health, no task, no model call
make bench                          # = python3 eval/bench/run.py run --labs-dir ~/harvey-labs
make bench LABS_DIR=/elsewhere/harvey-labs
```

**Expected cost of a full run: about $44.** The cap is $60, and the suite never charges more. The
worst case is $53 while each judge estimate stays within its task's reserve. A judge whose estimate
exceeds its reserve still runs when the estimate fits under the cap, so the charged total can reach
$60. Real spend can also exceed what is charged, by the estimate's error and the judge's retries.

- BigLaw is metered. Bench6's three tasks spent $10.27 + $8.57 + $9.60 = $28.44, and each task can
  spend at most its $11 budget, so BigLaw costs at most $33.
- The judge (harvey-labs) is estimated, because harvey-labs reports no judge usage. It makes one
  Sonnet 4.6 call per criterion, each carrying the whole deliverable, with no prompt caching. On
  bench6-sized deliverables that is about $9.70 for SEC (170k characters × 60 criteria) and about
  $5.80 for trust (280k × 23). The judge reserves are $12 and $8.
- Judge retries (harvey-labs retries transient errors up to 6 times) are not in the estimate.
  The cap's $7 above the $53 worst case is the margin for them, and for a judge that needs more
  than its reserve.

The command needs Go, Python 3, `uv` (for the harvey-labs judge), `psql`, and either Docker or
`BIGLAW_DATABASE_URL`. In order, it:

1. **Refuses to start** when any of these holds. Nothing has run and nothing is spent at that point.
   - The config's content does not match its `config_hash`.
   - An `OPENAI_*` variable is set, or a vendor opt-in other than `ALLOW_ANTHROPIC` (`ALLOW_AWS`, …).
     The refusal names the variables, never their values.
   - No key is set.
   - The harvey-labs checkout is not at the pinned commit, or its tracked files are modified.
   - The BigLaw checkout differs from its commit. That means a modified tracked file, or an untracked
     file under `internal/`, `cmd/`, `eval/`, `ui/`, `go.mod` or `go.sum`: `go build` compiles every
     `.go` file in a package directory, and the suite imports `eval/` from the worktree.
     `--allow-dirty` runs it anyway and records `build.dirty`, `build.patch` (a hash of the
     differences) and the untracked files. A dirty run is **NOT COMPARABLE** with any other run.
2. **Starts an empty Postgres.** With no `BIGLAW_DATABASE_URL`, it starts a throwaway
   `pgvector/pgvector:0.8.6-pg16` container on a loopback port and removes it at the end. With
   `BIGLAW_DATABASE_URL`, it creates a new database in that server and drops it at the end; the role
   needs CREATEDB. Either way the database starts with no tables, and the suite checks that. Runs
   share caches (figure readings, judge verdicts, the tool bandit's priors and Manage overrides), so
   a used database would make a run incomparable. There is no way to run on one.
3. **Builds the binary** at HEAD: static, `-X main.version=bench-<sha7>`. The build sees no
   `GOFLAGS` or `GOTOOLCHAIN` from your shell.
4. **Starts `biglaw serve`** with exactly the config's environment, from an empty one.
   - That environment: the database, `BIGLAW_MODEL_URL`, the key as `BIGLAW_MODEL_KEY`,
     `BIGLAW_MODELS`, the budgets, and the config's `vendor_opt_in` (`ALLOW_ANTHROPIC=1`, which
     serve's vendor breaker requires for `api.anthropic.com`).
   - Nothing else from your shell reaches it: no other `BIGLAW_*` variable, `ANTHROPIC_BASE_URL`
     or other opt-in.
   - It then checks that `/health` reports the built version and the pinned search mode.
5. **Runs each task in order** through the LAB driver (`eval/lab/labdriver`).
   - A task that did not end (the driver's timeout, a poll error) is cancelled and waited for before
     the next one starts, so two tasks never run at once.
   - After each task, BigLaw's metered cost is charged.
   - A **LAB task** is then judged by harvey-labs' own `run_eval`, single judge
     (`--judges claude-sonnet-4-6`), in the suite's own `LAB_ROOT`, so no harvey-labs `.env` is read.
     Before the judge runs, its cost is estimated from the deliverable and charged; when the estimate
     does not fit the cap, the judge is skipped (`judge_skipped_cap`) and the suite stops.
   - A **LAB task that failed** before a deliverable is scored 0/N, as harvey-labs scores a run with
     no output, with a note. It is not judged.
   - A **proof run** is scored by `eval/proofrun`, and a failed one is scored too.
6. **Enforces the spend cap.** A task starts only when what has been charged so far plus the task's
   reserve fits under `spend_cap_usd`. The reserve is BigLaw's $11 budget, which its spend cannot
   pass, plus the task's `judge_reserve_usd`. When a task does not fit, the suite stops before it.
   - BigLaw's metered USD is charged; when it is unknown (an unpriced call), its whole budget is.
   - The judge's estimate is charged.
7. **Stops immediately on a key or credit refusal**, whether BigLaw or the judge hits it. This covers
   a 401 or 403, a 402, or Anthropic's `credit balance is too low`.
   - On every poll, the suite reads BigLaw's own error fields: the task's error, and the notes of
     failed or refused steps. It never reads text a model wrote, such as a lead turn or a search
     query.
   - On a refusal, it cancels the task, marks the remaining tasks `not_run`, reports the provider's
     error type (never its message) and exits with code 3.
8. **Stops cleanly on SIGINT, SIGTERM or SIGHUP:** it cancels the running task, then stops serve and
   the database. A container that never became ready is removed too.
   - The judge runs in its own process group, and the whole group is stopped. `uv run` does not
     pass a kill on to its python child, which would otherwise go on judging, and spending,
     unattended.
   - While cleanup runs, further signals are ignored, and each step runs whatever the one before it
     raised. A second Ctrl-C cannot leave the database or the container behind.
9. **Writes `eval/bench/results/<UTC date>-<sha7>/summary.json`**, validated against the schema,
   and prints a table of the results.

The key is read only from `ANTHROPIC_API_KEY`, or from the file that `ANTHROPIC_API_KEY_FILE`
names. It reaches serve as `BIGLAW_MODEL_KEY` and the judge as `ANTHROPIC_API_KEY`, and nothing else.
It is never printed or written to results. Every log passes through a redactor.

Other commands:

```bash
python3 eval/bench/run.py preflight [--labs-dir DIR]   # the refusals, the database, the build, serve up to /health; then stop
python3 eval/bench/run.py check [summary.json …]       # the configs, the baselines and any summaries against their schemas (make eval runs it)
python3 eval/bench/run.py pin                          # re-pin config.json after a deliberate edit
make bench-smoke                                       # the offline tests (below); needs BIGLAW_TEST_DSN
```

## What is pinned, and why

[`eval/bench/config.json`](../eval/bench/config.json) is checked against
[`schema/config.schema.json`](../eval/bench/schema/config.schema.json):

| Setting | Value | Why |
|---|---|---|
| `models` | lead, specialist and critic all `claude-haiku-4-5` | Every subagent and figure agent runs on the lead model. No vision or embed role. The ids BigLaw's cost ledger records are kept as `models_served`, so a moved alias shows. |
| `model_url`, `vendor_opt_in` | `https://api.anthropic.com/v1`, `["ALLOW_ANTHROPIC"]` | One endpoint, and the one vendor breaker it needs. Any other opt-in is refused. `ANTHROPIC_BASE_URL` is unset for every child process. OpenAI hosts and non-`claude-` ids (the judge's too) are refused. |
| `budget` | `$11`, `900` calls per task (`BIGLAW_BUDGET_USD`, `BIGLAW_BUDGET_CALLS`) | Bench6's run-ids carry `b11`; its runs used 566 + 334 and 443 + 457 calls. |
| `search` | `lexical` | No embed role. `/health` must report it. |
| `harness` | harvey-labs `81d6a31` | The judge's prompts and parsing are part of the score. The pandoc that converts `.docx` deliverables is recorded too. |
| `judge` | `claude-sonnet-4-6`, single judge; $3/$15 per million tokens, 1,000 output tokens per criterion, 3.5 characters per token | The judge of the v1 record and of bench6, and the parameters of its cost estimate. |
| `tasks[].judge_reserve_usd` | SEC $12, trust $8, gate A $0 | What each task's judge may need: above the estimate on bench6's deliverables. |
| `spend_cap_usd` | `$60` per suite | The worst case is $53 while judge estimates stay within their reserves: three tasks at $11 plus the $20 of judge reserves. The rest is the margin for judge retries and for a judge over its reserve. Nothing is charged past $60. |
| `driver` | memo workflow, tool bandit on, finding gates approved, BigLaw's default `maxSteps`, 4 h timeout | The drivers' defaults, with a timeout long enough for bench6's 68-minute SEC run. |
| `tasks` | `sec`, `trust`, `gate-a-litigation` | The three tasks earlier runs used (below). |
| `postgres_image` | `pgvector/pgvector:0.8.6-pg16` | As Compose runs. The server and pgvector versions are recorded either way. |

The three tasks:

| id | kind | task |
|---|---|---|
| `sec` | LAB | `white-collar-defense-investigations/extract-key-allegations-from-sec-enforcement-referral-notice` (60 criteria) |
| `trust` | LAB | `trusts-estates-private-client/compare-trust-documents-against-client-instructions` (23 criteria) |
| `gate-a-litigation` | proof run | `litigation/greystone-v-talbot-defense` (`eval/tasks`, 20 criteria; `matter` must be its slug in `eval/matters.json`) |

`config_hash` is the sha256 of the config's canonical JSON: sorted keys, numbers by value (`11` and
`11.0` hash alike), and without `config_hash`, `name` and `description` (labels). A run refuses a
config whose content no longer matches its hash, so no setting can drift by hand. To change a
setting on purpose, edit the file, run `run.py pin` and commit both. Every result made under the old
hash then stops comparing with new ones.

**The in-repo yardstick.** Part of what scores a task lives in this repo and changes with the build
under test. Each summary records a content hash of each part, as it was on disk when the run
executed (`yardstick`):

- `proofrun`: `eval/proofrun`, `eval/tasks` and `eval/matters.json` (gate A's scorer, rubric and
  documents);
- `labdriver`: `eval/lab` (how a task is described, its documents converted, its deliverable
  rendered for the judge);
- `bench`: the bench harness itself.

A different `proofrun` hash makes the proof-run tasks not comparable. A different `labdriver` or
`bench` hash makes every task not comparable. The driver is counted as part of the yardstick, not of
the product: a driver change is a deliberate change of how the benchmark measures.

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

- `build`: sha, dirty, patch, untracked files, Go version;
- `config_hash`, and the whole `config` as it ran;
- `yardstick`;
- `harness`: commit, dirty, pandoc;
- `judge`: models, runner;
- `database`: fresh, server and pgvector versions, image;
- `models_served`;
- `runner`: host, date, Python;
- `spend`:
  - `cap_usd`;
  - `charged_usd`: BigLaw's metered cost plus the judge estimates;
  - `biglaw_usd`: metered;
  - `judge_usd_estimated`: an estimate, never a meter;
- `stopped`: `spend_cap` or `provider_error`, with the provider's error type.

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
| `judge_cost` | The LAB judge's cost, `basis: "estimate"`, with its criteria, tokens and deliverable size. |
| `models_served` | The model ids BigLaw's cost ledger recorded. |
| `charged_usd` | What the cap charged. |
| `run_dir`, `error`, `notes` | Where the run's files are, any error, and any notes. |

`conflicts` and `judge` are read from the suite's own database with `psql`. Without `psql` they
are null, with a note.

## Comparing

```bash
python3 eval/bench/compare.py eval/bench/results/<a>/summary.json eval/bench/results/<b>/summary.json
python3 eval/bench/compare.py --json BASE NEW                 # machine-readable
python3 eval/bench/compare.py --require-comparable BASE NEW   # exit 1 unless every task is COMPARABLE
```

For each task that both runs hold, the compare tool prints:

- the score delta;
- the criteria that flipped, as `gained` (fail → pass) and `lost` (pass → fail), and any criterion
  that is neither pass nor fail on one side;
- the cost, call and stop-reason changes.

It gives one of three verdicts:

- **COMPARABLE.** Both runs record the same config hash, mode, harness commit, judge models and
  yardstick, from clean builds on fresh databases.
- **COMPARABLE WITH CAVEATS (reconstructed baseline).** One side is a baseline whose reconstructed
  config hashes to the other's config, and nothing else differs. The output lists every key the
  baseline only inferred or does not know, and says plainly that its database freshness and bandit
  priors are unknown, so small deltas (about ±3 criteria on SEC) are within plausible variance.
  `comparable` stays false and `--require-comparable` exits 1. In JSON, `comparable_with_caveats`
  is true.
- **NOT COMPARABLE.** The output names every reason and every config key that differs
  (`budget.usd: 11 → 15`). A task can be NOT COMPARABLE on its own, such as gate A when the
  proof-run yardstick differs.

It also warns when:

- a run stopped early;
- the pandoc versions or the models served differ;
- the rubric sizes differ;
- both runs are the same commit, so the deltas are run-to-run variance.

## Baselines

The baselines are in [`eval/bench/baselines/`](../eval/bench/baselines). They use the summary schema
with `kind: "baseline"`, and they hold only recorded numbers. Anything not recorded is null.

| Baseline | Build | SEC | trust | gate A litigation | Compares |
|---|---|---|---|---|---|
| `v1-record` | v1 (`archive/v1-final`), Haiku | 50/60, $11.52, 92 min | 14/23, $10.52, 75 min | — | Never: another platform; settings unknown. |
| `v2-bench6` | `091bcfc` | 38/60, $10.27, 68 min, 566 calls, `budget_exhausted` | 11/23, $8.57, 42 min, 443 calls, `finished` | 20/20, $9.60, 444 calls, `budget_exhausted` | With caveats: reconstructed config. |

Both were judged by `claude-sonnet-4-6` as a single judge. Times are the harness's wall clock for
one run (`duration_s`, harvey-labs `metrics.json`); costs are BigLaw's own spend, not the judge's.

**v1-record** is v1's best Haiku runs, committed on `archive/v1-final` under `benchmarks/runs/`: SEC
`haiku-v5b/20260707-144125` (50/60; it matches `haiku-rounds3/20260706-003200`, whose analysis round
had starved, with every round healthy; an earlier version of this baseline said 49, the
`biglaw-fixwave-haiku` run) and trust `haiku-devport/20260705-213721` (14/23). Score, per-criterion
verdicts, cost and duration are copied from those runs' `scores.json` and `metrics.json`; the harness
commit, calls and settings were never recorded, so it still never compares. Its criteria are the
same rubric as bench6's: the same 60 and 23 ids with the same titles, except that v1's Windows runs
mis-decoded `—`, `–` and `§` in 12 of them. `compare.py` lists its per-criterion flips against bench6
(SEC: 2 gained, 14 lost; trust: 3 lost) for reading, not as a verdict.

**v2-bench6** has:

- per-criterion verdicts for SEC and trust, copied from the runs' harvey-labs `scores.json`
  (`.../v2-haiku45-b11-r4-091bcfc/20261024-074818` and `.../20261024-090112`);
- gate A's totals only, from the owner's record;
- its flips against bench5 (build 4d6ac3c, `.../v2-haiku45-b11-r6-4d6ac3c/20261024-041012` for SEC
  and the 05:31 trust run `.../20261024-053155`), as recorded and recomputed from both runs' scores:
  - SEC gained C-002, C-044, C-047, C-056 and lost C-015, C-023, C-024, C-025, C-026, C-028,
    C-052, C-059;
  - trust gained C-013, C-018 and lost C-010, C-011, C-016.

Its settings were set by hand, so its `config_hash` is null. `reconstructed_config` holds them as
believed, and `provenance` says how each key is known:

- **Verified:** the task ids and paths (run-ids); workflow, jurisdiction, split mode, gate policy
  and tool bandit (the runs' `config.json` and `metrics.json`); 900 calls (used plus remaining);
  harness 81d6a31 (the checkout's reflog); the single `claude-sonnet-4-6` judge and pandoc 3.1.3
  (`scores.json`).
- **Inferred:** $11 (`b11`); every role on Haiku 4.5 (`haiku45`; the exact id was not recorded);
  the Anthropic endpoint; lexical search; the LAB matter number, `maxSteps` and the poll interval
  (driver defaults).
- **Unknown:** whether the database was fresh; the tool bandit's priors; the in-repo yardstick; the
  database image; the timeout (at least 4091 s); all of gate A's settings.
- **Not applicable:** the suite-level cap, reserves and judge-estimate parameters, which did not
  exist then. They are set to the pinned values so that the reconstruction hashes to the pinned
  config.

`reconstructed_config_hash` equals today's `config_hash`, so a new run compares with bench6
**with caveats**. When the pinned config changes, the reconstruction no longer matches and bench6
becomes NOT COMPARABLE, until someone deliberately re-reconstructs it. The first real run of this
suite becomes the first fully comparable reference.

## The offline tests

Three Go tests in [`eval/bench_test.go`](../eval/bench_test.go) run in `make ci`'s `go test` (a few
seconds; they need the test database). `make bench-smoke` runs them alone.

- **`TestBenchSmoke`** serves the real API and engine over scripted models (`model.Fake`, the figure
  reader's stand-in), then runs `run.py smoke` against them with
  [`smoke.json`](../eval/bench/smoke.json). It checks the following:
  - A LAB-shaped task (`eval/bench/smoke/tasks/smoke/deadline`) is driven, its judge cost is
    estimated and charged, and it is judged by [`stubjudge.py`](../eval/bench/stubjudge.py). The
    stub has harvey-labs' command line and `scores.json` shape, and passes a criterion when its
    `stub_contains` phrase is in the deliverable. The task scores 2/3, with C-003 failing by design.
  - The gate A litigation proof run (the E1 oracle) is driven and scored by `eval/proofrun` at 20/20.
  - Cost, calls, the reader line, Computed figures and finding gates are measured. When `psql` is
    installed, the database readings are measured too.
  - The $12 cap stops the suite before the third task: unpriced calls are charged at the $5 budget.
  - The summary validates, compares as comparable with itself and not comparable with `v2-bench6`.
  - The stub judge echoes the dummy key it is given, as a leaky harness might, and the suite's judge
    log holds it redacted. The key appears in no file.
- **`TestBenchSmokeProviderRefusal`** answers every model call as BigLaw's 401
  (`authentication_error`). The suite cancels the first task, does not run the rest, names the error
  type (never the message) and exits 3.
- **`TestBenchPreflight`** builds the binary and starts `biglaw serve` with exactly the real suite's
  environment from `config.json`: the Anthropic endpoint and its opt-in, the budgets, the roles, a
  dummy key and a new database. It runs up to `/health`, then stops. Any startup refusal, such as the
  vendor breaker, fails it. Outbound HTTPS goes to a loopback proxy that refuses everything and must
  see nothing, and the created database must be dropped. Like the real suite, it creates a database,
  so `BIGLAW_TEST_DSN`'s role needs CREATEDB (CI's `postgres` superuser has it). Without it, the
  test skips and says why.

[`eval/tests/test_bench.py`](../eval/tests/test_bench.py) (in `make eval`) unit-tests the following:

- config validation, the hash, and refusals of hand edits;
- serve's environment;
- the key's sources, and refusals that never show it;
- the `OPENAI_*` and opt-in refusals;
- the `run` command's refusals before anything starts;
- the cap's arithmetic, including unknown cost, the worst case and a provider refusal mid-suite;
- the judge estimate, including on bench6-sized deliverables;
- untracked build files counting as dirty;
- the yardstick hashes;
- container cleanup when the container never becomes ready;
- a SIGTERM during the judge leaving no judge process, the stub judge's child included;
- cleanup that runs every step through a second signal or a failing step;
- schema validation of good and bad summaries;
- provider-error detection (types only, rate limits ignored);
- compare's deltas, flips and three verdicts.

## Adding a task

1. Add the task to `tasks` in `eval/bench/config.json`. Give it an `id` (lowercase, dashes), a
   `kind` and a `task` path:
   - `lab`: a path under harvey-labs' `tasks/` at the pinned commit;
   - `proofrun`: a matter id in `eval/matters.json`, with `matter` its slug.

   Also give it the `jurisdiction` and `split_mode` it should run with, and a `judge_reserve_usd`.
   For a LAB task, size the reserve from the judge estimate: about (deliverable characters ÷ 3.5 +
   3k) × criteria × $3/M + criteria × $0.015, with headroom. A proof run's reserve is 0.
2. Raise `spend_cap_usd` to the new worst case (every task's budget plus every reserve) plus a
   margin. `run.py check` reports it when the cap cannot fund even one task.
3. Run `python3 eval/bench/run.py pin` and commit the config with its new hash. Results under the
   old hash are not comparable with results under the new one, which is the point: a suite with
   another task is another suite. Bench6's reconstruction no longer matches either.
4. If the task has a recorded earlier result, add it to a baseline with its source. Put null where
   a number was not recorded.
