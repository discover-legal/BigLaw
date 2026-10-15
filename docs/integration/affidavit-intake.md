# Affidavit Maker ↔ BigLaw integration contract (v1)

This document is the **authoritative contract** between the two deployables:

- **BigLaw** (`biglaw-go`) — the firm platform. Ships the `intake` and `crm`
  modules defined here.
- **affidavit-maker** (discover.legal) — the client-facing portal. In **firm
  mode** (`BIGLAW_API_URL` + `BIGLAW_INTAKE_SECRET` set) it becomes the firm's
  client entry point: drafts route to lawyers and clients manage their CRM
  profile. Standalone (env unset) it stays a pure self-rep product and never
  touches this API.

The same contract is mirrored at `docs/BIGLAW_INTEGRATION.md` in the
affidavit-maker repository. Change either side only by changing both.

---

## 1. Transport & authentication

All portal→BigLaw calls are HTTPS REST with JSON bodies, authenticated by
**HMAC-SHA256 request signing** over a shared secret:

| Side | Env var |
|---|---|
| BigLaw | `INTAKE_HMAC_SECRET` (enables the intake module when set) |
| affidavit-maker | `BIGLAW_INTAKE_SECRET` (+ `BIGLAW_API_URL`) |

### Signing scheme

```
bodyHash  = lowercase hex sha256(rawBody)          # sha256("") for GET
canonical = METHOD + "\n" + PATH_WITH_QUERY + "\n" + timestamp + "\n" + bodyHash
signature = "v1=" + lowercase hex hmac_sha256(secret, canonical)
```

- `METHOD` — uppercase (`GET`, `POST`, …).
- `PATH_WITH_QUERY` — request path including the query string exactly as sent
  (`/intake/submissions/abc?x=1`). No scheme/host.
- `timestamp` — unix seconds, as a decimal string.

### Headers

```
X-Intake-Timestamp: <unix seconds>
X-Intake-Signature: v1=<hex>
Content-Type: application/json
```

### Verification rules (BigLaw side)

1. Reject if `|now − timestamp| > 300` seconds (replay window).
2. Recompute the signature over the **raw** request body; compare
   constant-time (`hmac.Equal`).
3. `/intake/*` routes are in the public-route set (they self-authenticate,
   like the Slack/Teams webhooks); every other credential is refused there —
   session cookies and bearer tokens do **not** grant intake access, and the
   intake HMAC grants access to **nothing outside `/intake/*`**.

Failures return `401 {"error": "..."}` without detail about which check
failed.

---

## 2. Identity model

The portal is the system of record for the *person* (Auth0). BigLaw is the
system of record for the *client of the firm* (roster + CRM profile).

- `client.externalId` — the portal's stable user id (Auth0 `sub`). BigLaw
  stores it as `CRMProfile.ExternalRef` and uses it to map every later call.
- On first contact (submission or proposal) BigLaw **finds-or-creates** the
  roster client and CRM profile:
  1. CRM profile by `externalRef` — hit → done.
  2. Roster client by case-insensitive email/name match → attach a new profile.
  3. Otherwise create roster client (name, auto client number `AM-<n>`) +
     profile.

All `/intake/clients/:externalId/...` routes are scoped **hard** to that
externalRef: the portal can only ever act as the client it authenticated.

---

## 3. Intake endpoints (portal → BigLaw, HMAC)

### 3.1 `POST /intake/submissions` — submit a draft to the firm

```jsonc
{
  "externalId": "doc-4711",              // portal's document id (idempotency key)
  "client": {
    "externalId": "auth0|abc123",        // required
    "email": "client@example.com",       // required on first contact
    "name": "Dana Client"                // required on first contact
  },
  "title": "Affidavit of Dana Client",
  "documentType": "affidavit",           // affidavit | divorce_petition | ...
  "matterType": "DIVORCE",               // portal matter-type code, optional
  "jurisdiction": "US-CA",               // BCP-47-style region tag
  "summary": "Client seeks ...",         // optional plain-text abstract
  "content": "…full draft text…",        // required — the rendered draft
  "facts": [                              // optional — seeds CRM proposals
    { "category": "goal", "predicate": "custody", "value": "Primary custody of both children" }
  ],
  "metadata": { "portalVersion": "5.0.0" } // optional, stored verbatim
}
```

**Behaviour** (in order): find-or-create client+profile → conflict check
(name vs adversary lists) → ingest `content` into the knowledge store
(`source: "affidavit-maker"`) → create the submission record → seed one CRM
**proposal** per entry in `facts` (proposer `client`, approver `lawyer`) →
audit `intake.submission_received`.

**Response `201`:**

```jsonc
{
  "submission": {
    "id": "9f0e…",                        // BigLaw submission id — store it
    "externalId": "doc-4711",
    "status": "received",                 // or "conflict_hold"
    "clientId": "…", "clientNumber": "AM-1",
    "crmProfileId": "…",
    "documentId": "…",                    // knowledge-store document
    "conflict": { "hasConflict": false },
    "createdAt": "2026-08-03T12:00:00Z"
  }
}
```

Re-posting the same `externalId` for the same client **updates** the existing
submission (new content re-ingested, status reset to `received` unless it is
`ready`/`rejected`) rather than duplicating it.

### 3.2 `GET /intake/submissions/:id` — one submission

`200 {"submission": {...}}` — same shape plus `"note"`, `"assignedToName"`,
`"taskId"`, `"updatedAt"`. `404` if unknown.

### 3.3 `GET /intake/clients/:externalId/submissions` — all of a client's submissions

`200 {"submissions": [ ... ]}` newest-first. The portal dashboard uses this
single call to decorate document rows with live status.

**Submission `status` vocabulary** (BigLaw → portal, superset-safe: treat
unknown values as `in_review`):

| Status | Meaning |
|---|---|
| `received` | Landed, awaiting a lawyer |
| `conflict_hold` | Conflict check flagged; partner must clear |
| `in_review` | A lawyer claimed it / task running |
| `ready` | Reviewed draft ready; the firm will reach out |
| `rejected` | Firm declined (see `note`) |

### 3.4 `GET /intake/clients/:externalId/profile` — the client's CRM view

```jsonc
{
  "profile": { "id": "…", "clientId": "…", "clientNumber": "AM-1",
               "name": "Dana Client", "email": "client@example.com" },
  "facts": [ /* approved facts, newest-first */ ],
  "pendingYourApproval":   [ /* lawyer-proposed, awaiting the client */ ],
  "pendingLawyerApproval": [ /* client-proposed, awaiting the firm  */ ]
}
```

`404` if the client has never contacted the firm (portal treats as empty).

**Fact shape** (both directions):

```jsonc
{
  "id": "…",
  "category": "goal",          // see §5 vocabulary
  "predicate": "custody",      // short machine key
  "value": "Primary custody of both children",
  "note": "",                  // free-text context
  "source": "client",          // client | lawyer | intake | system
  "proposedBy": "client:auth0|abc123",   // or "lawyer:<profileId>"
  "approverRole": "lawyer",    // who must approve: lawyer | client
  "status": "approved",        // pending | approved | rejected | superseded
  "decisionNote": "",
  "createdAt": "…", "decidedAt": "…"
}
```

### 3.5 `POST /intake/clients/:externalId/proposals` — client proposes updates

```jsonc
{
  "client": { "email": "…", "name": "…" },       // used only if first contact
  "facts": [
    { "category": "contact", "predicate": "phone", "value": "+1 555 0100", "note": "" }
  ]
}
```

`201 {"proposals": [fact, …]}` — each created `pending`, approver `lawyer`.
Caps: ≤ 20 facts/call, value ≤ 2000 chars, note ≤ 2000 chars.

### 3.6 `POST /intake/proposals/:id/decision` — client decides a lawyer proposal

```jsonc
{ "clientExternalId": "auth0|abc123", "decision": "approve", "note": "" }
```

`200 {"fact": {...}}`. `403` unless the proposal belongs to that client's
profile **and** `approverRole == "client"` **and** `status == "pending"`.

---

## 4. Firm-side endpoints (BigLaw session/bearer auth — not HMAC)

Lawyers work the queue and the consent loop from these (used by the BigLaw
workbench/MCP; listed here because they complete the state machine):

```
GET    /intake/queue                      lawyer: own+unassigned · partner: all
POST   /intake/submissions/:id/claim      assign to self
PATCH  /intake/submissions/:id            { status, note } → drives §3.3 statuses
POST   /intake/submissions/:id/task       spin up an orchestrator review task
GET    /crm/profiles                      list profiles            [any lawyer]
GET    /crm/profiles/:id                  profile + facts by status
POST   /crm/profiles/:id/facts            lawyer proposes → approver=client
POST   /crm/proposals/:id/decision        lawyer decides client proposals
POST   /crm/profiles/:id/query            { query, topK } semantic fact search
GET    /crm/proposals?approver=lawyer     firm-wide pending queue
GET    /modules                           module registry status
```

## 5. CRM fact semantics (the neurosymbolic profile)

- **Symbolic layer** — typed facts `(category, predicate, value)` with full
  provenance and a bidirectional-consent state machine:
  - proposed by **client** → `approverRole: lawyer`
  - proposed by **lawyer** → `approverRole: client`
  - nothing enters the approved profile without the counterparty's approval.
- **Rules on approval**:
  - *Supersedence*: approving a fact replaces any approved fact with the same
    `(category, predicate)` — the old one becomes `superseded`.
  - *Conflict watch*: approving an `adverse_party` fact re-runs the firm's
    conflict check against the roster and audits + flags any hit.
  - *Advocacy sync*: approved `goal|concern|constraint|preference` facts are
    compiled into the per-matter **ClientVoice** advocacy brief (source
    `"crm"`), which the orchestrator surfaces at human gates.
- **Neural layer** — every fact is embedded; `POST /crm/profiles/:id/query`
  ranks a profile's approved facts by cosine similarity for agent/lawyer use.

**Category vocabulary**: `identity`, `contact`, `family`, `financial`,
`employment`, `matter`, `adverse_party`, `goal`, `concern`, `constraint`,
`preference`, `history`, `note`. Unknown categories coerce to `note`.

## 6. Versioning & errors

- Breaking changes bump the signature prefix (`v1=` → `v2=`) and this file.
- Errors: `4xx/5xx` with `{"error": "message"}`. The portal must degrade
  gracefully: BigLaw being unreachable disables firm features for the request
  but never breaks self-rep functionality.
