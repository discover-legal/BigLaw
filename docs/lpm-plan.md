# Legal Project Management (LPM) build plan

Built on the low-power Go port (`biglaw-go/`, targeting ARM64 / Raspberry Pi /
cheap local long-running compute). This branch is based off the
`claude/low-end-hardware-port-JfOHf` branch because the LPM vision leans on the
same substrate: small specialised models, running cheaply and continuously on the
box, turning the email tsunami into a *mineable structured corpus* instead of
one-shot answers.

## Origin

Distilled (and sanitised) from a working session with a Lead LPM. The pain it
targets: an LPM burns enormous time turning the overnight inbox into signal and
recycling it into actionable insights for project teams. The shape of the fix:

1. A small-model **per-email classifier that routes each message to the right
   matter** with recursive self-checks.
2. **Daily per-matter status reports** emitted in *two* formats in parallel — a
   human DOCX stakeholder update and a machine-readable JSON for downstream
   harvesting — which **accumulate into a mineable corpus** over the life of a
   deal.
3. A **0600 BLUF "portfolio" briefing** a partner can digest in five minutes
   across all their active matters.
4. A **historical backfill** that grinds old email on cheap local compute.
5. **Draft-and-circulate** updates internally for comment, with **guardrails
   against mis-sending client-confidential information**.
6. All built on **specialised small agents**, not one big general model.

## Guiding principle

Specialised small agents per job. Facts are computed deterministically; the model
writes only the narrative *over* those facts and is then checked. This keeps the
system cheap, auditable, and low-power-friendly.

## Cross-cutting design decisions

**Email-write mode** — one config knob, `LPM_EMAIL_WRITE_MODE`, spanning the full
range so it can be dialled up safely over time:
`off` (insights only) → `channel` (post a draft into the Teams/Slack matter
channel for comment) → `draft` (write an unsent draft into the mailbox) →
`send_gate` (send only after an explicit human approval gate).

**Email intake** — `LPM_INTAKE_MODE`: `shared_inbox | polling | both`. A scheduler
periodically pulls mail via the existing Graph/Gmail search; `shared_inbox` simply
scopes the query to one project mailbox CC'd on everything. Set it to `polling`
and the shared-inbox dependency drops away with no code change.

**Confidentiality guardrails** (the mis-send concern) — before any draft leaves in
`channel`/`draft`/`send_gate` mode: a per-matter recipient-domain allowlist, a
client-confidential / cross-matter leakage scan, a full audit-log entry, and the
human approval gate for `send_gate`.

**DOCX rendering** — a small **pure-Go OOXML writer** (`internal/lpm/docx.go`).
A `.docx` is a ZIP of XML parts; emitting headings/paragraphs/bullets directly
keeps the runtime self-contained on the box with no heavyweight Office dependency.

## Phase 1 — Daily status-report spine ✅ (this branch)

The data backbone everything else feeds on.

- **Structured model** (`internal/types/types.go` → `MatterStatusReport`): the
  single source of truth, carrying health, BLUF, summary, workstreams, risks,
  open questions, deterministic deltas, sources, and a confidence score.
- **Three renderers from one object** (`internal/lpm/render.go`): JSON (machine
  harvest), Markdown, and DOCX (human) — so the human and machine views can never
  drift.
- **Append-only corpus** (`internal/lpm/corpus.go`): `./data/status-reports.jsonl`,
  one report per matter per day. Gives a per-matter time-series and lets each run
  compute the *delta since the last report* by diffing the previous entry.
- **Generator** (`internal/lpm/report.go`): computes deltas deterministically from
  the gathered state (new/closed tasks, new findings, hours logged, billed $,
  health), then a specialised small model writes only the narrative over those
  facts, followed by a lightweight **recursive verify pass** that scores
  groundedness. Degrades gracefully to a fact-only report if the model is
  unavailable.
- **Scheduler** (`internal/lpm/scheduler.go`): a self-contained once-a-day trigger
  at `LPM_DAILY_HOUR` (default 0600), idempotent across restarts — no external
  cron.
- **Service + worker** (`internal/lpm/service.go`): the scheduler enqueues one
  durable job per active matter into the existing job queue; a background worker
  drains it, generates each report, appends to the corpus, and renders artifacts.
  Queue-backed = restart-safe and low priority on cheap compute.
- **Surfaces**: `POST /lpm/reports/generate` (on-demand), `GET /lpm/reports`
  (query the corpus), `GET /lpm/reports/:id/docx` (download). Wired behind
  `LPM_ENABLED` in `cmd/biglaw/main.go`; the synthesis model defaults to the
  low-power tier via `routing.SelectModel`.
- **Config** (`internal/config/config.go` → `LPMConfig`): all knobs above plus the
  Phase 2 email-write-mode and intake-mode settings (typed and validated, ready to
  light up).
- **Tests**: corpus append/query/latest/get + malformed-line tolerance; DOCX zip
  validity + XML escaping; deterministic delta computation, narrative parsing,
  cutoff-from-previous-report, graceful model-failure fallback; scheduler
  once-per-day idempotency; end-to-end service artifact + corpus writes and the
  queue worker path.

## Later phases (layer on the spine)

- **Phase 2 — Per-email classifier + matter routing.** Shared-inbox + polling
  intake → a small `lpm-email-router` agent (recursive self-checks) tags each
  message and routes it to a matter → routed emails flow into the status-report
  deltas (`LPMDeltas.EmailsRouted`) and corpus. Activates the email-write-mode and
  confidentiality-guard cross-cutting design.
- **Phase 3 — 0600 BLUF portfolio briefing.** Roll the status-report corpus +
  matter-health across a partner's active matters into a five-minute digest;
  `@BigMichael portfolio`; scheduled 0600 delivery per write-mode.
- **Phase 4 — Historical email backfill.** A queue-driven, resumable,
  rate-limited worker on a low-end/local model grinding the old email corpus into
  routed/tagged + retro status-deltas — the "run it for ages on the box" piece.

## Status

Phase 1 is implemented, builds clean (`go build ./...`), and is fully unit-tested
(`go test ./internal/lpm/`). Everything is gated behind `LPM_ENABLED=false` by
default, so it is inert until switched on.
