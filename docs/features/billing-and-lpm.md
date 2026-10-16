[Docs](../index.md) › Features › **Billing, LPM & monitors**

# Billing, legal project management & monitors

## Billable time tracking

Every task automatically accumulates billable time. Entries open when a task starts and close
when it completes or is deleted; duration is rounded up to the nearest **6-minute unit**
(the standard legal billing increment). Partners see all time entries; lawyers see only their own.
**AI time is billed as AI time.** A task run is attributed to the firm's AI timekeeper
(`AI_TIMEKEEPER_ID`, default `biglaw-ai`; `AI_TIMEKEEPER_NAME`, default `BigLaw AI`) at its own
hourly rate `AGENT_BILLING_RATE_DEFAULT` (gated by `AGENT_BILLING_ENABLED`) — never to the submitting lawyer, who is recorded as
`requestedByProfileId` (and can see the entry). Only active processing time counts: the clock
pauses while the task waits at a human gate (`pausedMs`). **If the rate is unset or not
positive, AI time closes non-billable with reason "AI rate not configured"** ("AI billing
disabled" when `AGENT_BILLING_ENABLED=false`).

A run that **fails** (error, grounding collapse, worker panic, synthesis error) or is
interrupted by a restart is closed **non-billable**: the elapsed time stays on record with
`nonBillable: true` and a `nonBillableReason`, at 0 units, and is excluded from LEDES,
pre-bills and Clio sync. A one-off startup migration (owner process only, recorded in
`<TIME_FILE>.migrations.json`) writes off historical entries of failed/interrupted runs and of
runs that "completed" with a synthesis error (for tasks since deleted, the audit log's last
`task.failed`/`task.interrupted`/`task.complete` event decides), skipping any already on an
approved/invoiced pre-bill or synced to Clio, and reports draft pre-bills to regenerate (audit
event `time.migration.failed_runs_written_off`). A pre-bill that lists a written-off entry cannot
be approved or invoiced (409 with `nonBillableEntryIds`); regenerate it.

The LEDES 1998B export follows the published 24-field layout (`LEDES1998B[]` line, field-name
line, `|`-separated rows each ending in `[]`). `TIMEKEEPER_CLASSIFICATION` comes from the
profile's title (partner / associate / of counsel / paralegal or legal assistant) and otherwise
its role (partner → `PT`, lawyer → `AS`); unresolvable → `OT`. AI time uses the AI timekeeper's
id/name and rate, classification `OT`, labelled "AI agent:" (Clio sync sends the same rate as the
activity price and labels the note). Each (client, matter) becomes its own invoice block
(`INVOICE_NUMBER` suffixed `-1`, `-2`, … in (client, matter) order when an export holds several
invoices, kept within 1998B's 20 characters by shortening the base; a longer `invoiceNumber` is a
400; default `<matter-or-client, 8 chars>-YYYYMMDD`) with its own total and billing period. Work
with no client number is left off and reported like legacy rows below. `TIMEKEEPER_NAME` is
"Last, First" (max 30). `LINE_ITEM_TOTAL` is always round2(hours × rate); a legacy entry whose stored
amount disagrees is left off, logged, audited (`time.ledes_export_skipped`) and counted in the
`X-Ledes-Skipped-Entries` response header. Free text is transliterated to printable ASCII
(1998B is an ASCII format) and cut to 200 characters. Dates use `FIRM_TIMEZONE` (IANA name,
default UTC). UTBMS codes are passed through when an entry carries them; gate reviews
derive activity `A104`; nothing is guessed. Set `LEDES_LAW_FIRM_ID` to fill `LAW_FIRM_ID`.

```
GET  /time-entries                query: profileId, taskId, matterNumber, from, to
GET  /time-entries/export.json    full export (partner only)
GET  /time-entries/export.csv     CSV for billing import (partner only; includes nonBillable,
                                  nonBillableReason, requestedByProfileId, pausedMs)
GET  /time-entries/export.ledes   LEDES 1998B export for e-billing (partner only)
GET  /time-entries/{agent-summary,suggestions}
POST /time-entries/sync-to-clio   push entries to Clio as activities, dated in FIRM_TIMEZONE (partner only, idempotent)
```

## Pre-bills, invoices & OCG compliance

`biglaw-go/internal/billing/` (pre-bills, LEDES 1998B export/parse, invoice validation) and
`biglaw-go/internal/ocg/` (outside-counsel-guidelines compliance checks):

```
POST /pre-bills                   GET/PATCH /pre-bills(/:id)
POST /invoices/{validate,upload}
POST /clients/:id/ocg             GET/DELETE /clients/:id/ocg · GET …/ocg/stats
```

## LPM — status reports & portfolio

`biglaw-go/internal/lpm/` — the daily status-report spine: per-matter stakeholder updates as
DOCX plus machine-readable JSON, accumulating into a mineable corpus. Design notes:
[LPM build plan](../lpm-plan.md).

```
POST /tasks/:id/status-report     generate a status report for a task
POST /reports/generate            POST /portfolio/generate (portfolio BLUF)
GET  /reports · /reports/:id/docx
GET  /analytics/portfolio-health  (partner only)
```

## Matter health, budgets & monitors

Firm-wide monitors run from the entry point (`biglaw-go/cmd/biglaw/`): docket watch,
regulatory alerts, and matter budget monitors, each with a live SSE alert stream.

```
GET     /matters/:matterNumber/{health,budget-prediction}
PUT/GET /clients/:id/matters/:num/budget    POST …/budget/check
POST    /dockets/watch · /dockets/check-now   GET /dockets · /dockets/alerts/stream (SSE)
POST    /regulatory/check-now                 GET /regulatory/alerts/stream (SSE)
GET     /budget/alerts/stream (SSE)
```

Matter health also surfaces through Big Michael (`@BigMichael status M-…`) and proactive
channel notifications — see [Big Michael](big-michael.md).

## Client voice (Remy advocacy briefs)

`biglaw-go/internal/clientvoice/` — per-matter client-voice advocacy briefs:

```
PUT/GET /matters/:matterNumber/client-voice
```

Related: [Cost tracking](../operations/cost-tracking.md) · [REST API](../integration/rest-api.md) · [Connectors — Clio](../integration/connectors.md)
