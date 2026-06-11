# Harvey LAB driver

Runs BigLaw against [Harvey's Legal Agent Benchmark (LAB)](https://github.com/harveyai/harvey-labs)
— 1,251 long-horizon legal tasks across 24 practice areas, graded by expert-written rubrics.

LAB splits into a **run phase** (an agent produces deliverable files) and an **eval phase**
(a Claude judge grades those files against the task rubric). LAB's own `ModelAdapter` plug-in
point is built for raw chat models, where its harness owns the agentic loop and tools.
BigLaw already *is* the loop, so this driver replaces the run phase instead: it drives the
BigLaw REST API end-to-end and writes results in the exact layout LAB's eval phase reads.
Scoring stays 100% Harvey's code.

```
LAB task.json + documents/          this driver               harvey-labs eval (unchanged)
─────────────────────────  ──────────────────────────────  ───────────────────────────────
instructions, rubric,  →   convert docs → ingest →         evaluation.run_eval reads
.docx/.pdf/.xlsx inputs    submit_task → auto-approve      results/<run-id>/output/ +
                           gates → synthesis → render      metrics.json, writes scores.json
                           real .docx/.xlsx/.pdf into
                           results/<run-id>/output/
```

## One command (Windows)

`bench.ps1` does the whole Prerequisites section for you — clones harvey-labs to
`~\harvey-labs` if missing, installs the Python dependencies if missing, starts a
native backend if none is reachable (logs to `data\bench-backend*.log`), and runs
the driver:

```powershell
cd benchmarks\harvey-lab
.\bench.ps1 -List                                            # browse the dataset
.\bench.ps1 -Task corporate-ma/review-data-room-red-flag-review
.\bench.ps1 -Task <task> -GatePolicy reject -ModelDir biglaw-reject
.\bench.ps1 -Task <task> -Api http://localhost:3102          # use the Docker stack
```

Extra flags (`--workflow`, `--split-mode`, …) pass through to `run.py` verbatim.

The backend reads `.env` at the repo root. To benchmark BigLaw on **OpenAI** instead
of Anthropic, set `OPENAI_API_KEY` and `OPENAI_MODEL` there (see `.env.example`) —
chat and embeddings then both run on the OpenAI key, and `--model-dir biglaw-gpt`
keeps those runs separate in `compare.py`. (Harvey's eval judge is always Claude,
so scoring still needs an `ANTHROPIC_API_KEY` for the `run_eval` step.)
When the run finishes the driver prints the exact `evaluation.run_eval` command for
scoring. The auto-started backend keeps running between runs (stop it with
`Stop-Process -Name biglaw`).

## Prerequisites

- A [harvey-labs](https://github.com/harveyai/harvey-labs) checkout (the task dataset + eval
  harness; eval needs `uv` and an `ANTHROPIC_API_KEY` for the judge).
- A running BigLaw backend: `bash setup.sh` (Docker, `:3102`) or
  `go run ./biglaw-go/cmd/biglaw` (native, `:3101`) from the repo root, with its own
  `ANTHROPIC_API_KEY` configured.
- Python 3.11+ with this driver's dependencies:

```bash
cd benchmarks/harvey-lab
pip install -r requirements.txt        # requests, python-docx, openpyxl, python-pptx, PyMuPDF
```

`pandoc` on PATH is an optional fallback renderer/converter for formats the Python
libraries don't cover.

## Usage

```bash
# Browse the dataset
python run.py --labs-dir ~/harvey-labs --list

# Run one task through BigLaw (native backend on :3101 by default)
python run.py --labs-dir ~/harvey-labs \
  --task corporate-ma/review-data-room-red-flag-review

# Docker stack
python run.py --labs-dir ~/harvey-labs --task <task> --api http://localhost:3102

# Score with Harvey's eval phase, unchanged (from the harvey-labs checkout):
uv run python -m evaluation.run_eval --run-id <printed-run-id> --task <task>
uv run python -m evaluation.report --run-id <printed-run-id>
```

The driver prints the exact `run_eval` command when a run finishes. Run IDs follow the
harness convention `{task}/biglaw/{timestamp}` and land in `<labs-dir>/results/` so the
eval and dashboard tooling find them without flags.

## What the driver does

1. **Converts** everything under the task's `documents/` to text client-side — `.pdf`
   (PyMuPDF), `.docx` (python-docx), `.xlsx` (openpyxl), `.pptx` (python-pptx), text
   formats as-is, pandoc for the rest. (The Go backend's `/documents/upload` is
   text-only, so conversion cannot be delegated to it.) Unconvertible files are
   skipped and logged, never fatal.
2. **Ingests** each document via `POST /documents` and **submits** the task via
   `POST /tasks` with the LAB instructions plus the deliverables list, the ingested
   document IDs, and `jurisdiction` (default `US`).
3. **Polls** `GET /tasks/:id`, resolving every human gate per `--gate-policy` so the
   run is fully autonomous. Each resolution (gate ID, finding ID, action) is recorded
   in `metrics.json` — gates-per-task is itself an interesting signal of how often
   BigLaw's debate/verification protocols flag findings.
4. **Renders format-faithful deliverables** into the filenames `task.json` names:
   `.docx` via python-docx (headings, lists, pipe tables), `.xlsx` via openpyxl
   (preferring the structured `Task.table` from a tabulate run, else markdown tables
   in the synthesis), `.pdf` via PyMuPDF, everything else as text.
5. **Writes the run dir**: `config.json`, `transcript.jsonl` (driver-level event log),
   `metrics.json` (token counts and cost from `GET /tasks/:id/cost`, duration, document
   counts, plus a `biglaw` block with task IDs, gate resolutions, findings count), and
   `output/` with the deliverables.

## Multi-deliverable tasks (`--split-mode`)

| Mode | Behaviour | Cost |
|---|---|---|
| `markers` (default) | One BigLaw task; the synthesis is instructed to delimit each deliverable with `=== DELIVERABLE: <filename> ===` lines and is split accordingly. Any deliverable the model failed to delimit falls back to the full synthesis. | 1× |
| `per-task` | One BigLaw task per deliverable, each focused on that single file (an `.xlsx` sibling still routes to tabulate). Highest fidelity. | N× |
| `duplicate` | Full synthesis rendered into every named file (the original behaviour). | 1× |

## Gate policy (`--gate-policy`)

| Policy | Behaviour |
|---|---|
| `approve` (default) | Flagged findings pass into the synthesis — the fully-autonomous ceiling. |
| `reject` | Flagged findings are dropped (the orchestrator removes them and continues) — benchmarks the debate/verification gate as a *filter*. Run both policies on the same tasks to measure whether the gate helps or hurts rubric scores. |

## Comparing against published baselines

Harvey's eval writes `scores.json` into each run dir. Aggregate yours and lay them
against Harvey's published numbers (pinned in `published_baselines.json`, from their
[initial results post](https://www.harvey.ai/blog/legal-agent-benchmark-initial-results) —
all-pass rates: Opus 4.7 7.1%, Sonnet 4.6 5.4%, Opus 4.6 4.2%, GPT-5.5 2.1%,
Gemini 3.5 Flash 0.8%):

```bash
python compare.py --labs-dir ~/harvey-labs
```

The table groups by `--model-dir`, so name runs distinctly to compare configurations,
e.g. `--model-dir biglaw-approve` vs `--model-dir biglaw-reject`. Update
`published_baselines.json` when Harvey publishes new numbers.

## Workflow mapping

| LAB `work_type` | BigLaw workflow |
|---|---|
| `analyze` | `roundtable` |
| `draft` | `roundtable` |
| `review` | `review` |
| `research` | `full_bench` |
| any task with an `.xlsx` deliverable | `tabulate` (its structured table feeds the renderer) |

Override per run with `--workflow`.

## Caveats

- **Comparability.** Published baseline agents ran inside Harvey's sandboxed six-tool
  harness with turn caps; BigLaw runs its own multi-agent loop with convergence and
  halting — that loop is the thing being measured, not a confound. Same judge, same
  rubrics, different environments: partial-set all-pass rates are also noisy against
  full-set published numbers, so compare on as many tasks as budget allows.
- **`metrics.json` keys** follow the harness conventions best-effort; eval folds them
  into `scores.json` metadata and does not gate on them.
- **Cost.** Every LAB task is a full DyTopo run with Opus debate + synthesis. Start with
  one practice area, watch `GET /cost/summary`, and budget before attempting all 1,251
  tasks. The backend's single-writer vector DB also means one backend process: run tasks
  sequentially through it rather than in parallel driver processes.
