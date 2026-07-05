[Docs](../index.md) › Deploy & operate › **Audit trail**

# Audit trail

Every significant event is recorded in an **append-only, SHA-256 hash-chained JSONL** file — tamper-evident by construction. The in-memory buffer is restored from disk on restart so the live panel always shows history, not just new events.

## What gets logged

| Event category | Events recorded |
|---|---|
| **Task lifecycle** | `task.created`, `task.started`, `task.complete`, `task.failed`, `task.deleted` |
| **Lawyer assignment** | `task.assigned` — carries the assigning partner's profileId, plus added/removed lawyer delta |
| **DyTopo rounds** | `round.start`, `round.complete`, `round.digest` — includes agent roster, finding count, phase |
| **Agent activity** | `agent.processing`, `agent.complete` — agentId, tier, domain, round, duration |
| **Findings** | `finding.produced` — findingId, confidence, content preview, attributed to responsible lawyer |
| **Tool calls** | `tool.call`, `tool.result` — **actorId = the responsible lawyer** (not "system") |
| **Protocol** | `debate.start`, `debate.resolved`, `verification.start`, `verification.complete` |
| **Human gates** | `gate.approved`, `gate.rejected` — with reviewer's profileId |
| **Documents** | `document.ingested`, `document.uploaded` |
| **Authentication** | `auth.login`, `auth.logout`, `auth.failed` — provider, role |
| **Voice profiles** | `profile.tone.imported`, `profile.tone.cleared` |
| **Matters** | `matter.client_voice_updated`, `matter.notification` |
| **OCG compliance** | `client.ocg.ingested`, `client.ocg.deleted` |

## Key design for legal defensibility

**External system access is attributed to the responsible lawyer**, not "system". When BigLaw calls Westlaw, CourtListener, Clio, or any of the 32 connectors on behalf of a task, the `actorId` on the `tool.call` entry is the lawyer who submitted (or was assigned to) that matter. A court question of the form *"did Sarah Chen access Westlaw on Thursday?"* can be answered directly from the JSONL.

**Assignment changes are delta-logged**: `task.assigned` records both the final lawyer list and the `added`/`removed` diff, and carries the partner's profileId as actor so the audit trail shows *who* changed the assignment.

## Querying

```
GET /audit                        all recent entries (access-filtered; partner sees all)
GET /audit?taskId=<id>            entries for a specific matter
GET /audit/stream                 live SSE stream of new events
```

The hash chain is re-verified when the log is restored on restart — a break logs a tamper
warning.

## SIEM forwarding

Entries also forward asynchronously (best-effort, fire-and-forget) to **OpenSearch**,
**Splunk HEC**, or a **custom webhook** — set `AUDIT_OPENSEARCH_URL`,
`AUDIT_SPLUNK_HEC_URL` + `AUDIT_SPLUNK_HEC_TOKEN`, or `AUDIT_WEBHOOK_URL` to activate.

Related: [Security](../security.md) · [Access control](access-control.md)
