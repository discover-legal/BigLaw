[Docs](../index.md) › Features › **Billing, LPM & monitors**

# Billing, legal project management & monitors

## Billable time tracking

Every task automatically accumulates billable time. Entries open when a task starts and close
when it completes or is deleted; duration is rounded up to the nearest **6-minute unit**
(the standard legal billing increment). Partners see all time entries; lawyers see only their own.

```
GET  /time-entries                query: profileId, taskId, matterNumber, from, to
GET  /time-entries/export.json    full export (partner only)
GET  /time-entries/export.csv     CSV for billing import (partner only)
GET  /time-entries/export.ledes   LEDES 1998B export for e-billing (partner only)
GET  /time-entries/{agent-summary,suggestions}
POST /time-entries/sync-to-clio   push entries to Clio as activities (partner only, idempotent)
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
