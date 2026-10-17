# Affidavit Maker ↔ BigLaw integration contract (v1)

This document is the **authoritative contract** between the two deployables:

- **BigLaw** (`biglaw-go`) — the firm platform. Ships the optional `intake` and
  `crm` modules defined here (off unless `INTAKE_HMAC_SECRET` is set).
- **affidavit-maker** (discover.legal) — the client-facing portal. In **firm
  mode** (`BIGLAW_API_URL` + `BIGLAW_INTAKE_SECRET` set) it becomes the firm's
  client entry point: drafts route to lawyers and clients manage their CRM
  profile. Standalone (env unset) it stays a pure self-rep product and never
  touches this API.

The same contract is mirrored at `docs/BIGLAW_INTEGRATION.md` in the
affidavit-maker repository. Change either side only by changing both.

**The integration surface is this document plus the JSON Schemas in
[`schemas/intake/v1/`](schemas/intake/v1/)** — not shared code. The two products
stay independent: neither imports the other, neither depends on the other's
internal vocabularies (BigLaw never keys behaviour on the portal's matter-pack
codes), and BigLaw's contract test
(`biglaw-go/internal/intake/contract_test.go`) validates the example requests and
every response the portal routes actually produce against those schemas.

**Tolerant reader, both directions:** receivers ignore unknown properties
(no schema sets `additionalProperties: false`); unknown enum values map to safe
defaults (an unknown fact `category` is stored as `note`; an unknown submission
`status` is shown by the portal as `in_review`). New optional fields may be added
within v1; anything that removes or re-types a field is v2.

---

## 1. Transport & authentication

All portal→BigLaw calls are HTTPS REST with JSON bodies, authenticated by
**HMAC-SHA256 request signing** over a shared secret:

| Side | Env var |
|---|---|
| BigLaw | `INTAKE_HMAC_SECRET` (enables the intake module when set) |
| affidavit-maker | `BIGLAW_INTAKE_SECRET` (+ `BIGLAW_API_URL`) |

Every secret must be at least **32 characters** (BigLaw refuses to start
otherwise; generate with `openssl rand -hex 32`). **Rotation:**
`INTAKE_HMAC_SECRET` may be a comma-separated list — BigLaw accepts a
signature made with any of them. Rotate by adding the new secret to BigLaw's
list, switching the portal's `BIGLAW_INTAKE_SECRET` to it, then removing the
old one.

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
3. **Replay:** a signature already accepted inside the window is refused for
   every write except the idempotent `POST /intake/submissions` upsert (a
   double-click re-sending the same signed request must still succeed);
   `GET`s are harmless to repeat. The portal signs every request afresh, so
   distinct requests never collide.
4. `/intake/*` routes are in the public-route set (they self-authenticate,
   like the Slack/Teams webhooks); every other credential is refused there —
   session cookies and bearer tokens do **not** grant intake access, and the
   intake HMAC grants access to **nothing outside `/intake/*`**.

Failures return `401 {"error": "..."}` without detail about which check
failed.

**Reverse proxies:** BigLaw verifies the path it receives. A proxy in front of
BigLaw must forward the request path and query **unchanged** — stripping a
prefix (e.g. the portal calls `https://firm.example/biglaw/intake/...` and
the proxy forwards `/intake/...`) invalidates every signature. Mount BigLaw
at the root of its host, or have the portal's `BIGLAW_API_URL` point at the
path BigLaw itself sees.

---

## 2. Identity model

The portal is the system of record for the *person* (Auth0). BigLaw is the
system of record for the *client of the firm* (roster + CRM profile).

- `client.externalId` — the portal's stable user id (Auth0 `sub`). BigLaw
  stores it as `CRMProfile.ExternalRef` and uses it to map every later call.
- On first contact (a submission) BigLaw finds the CRM profile by
  `externalRef`, or creates a **prospect** profile. A prospect is *not* on the
  firm's client roster: it has no `clientId`/`clientNumber` yet. Proposals
  (§3.5) never create a profile.
- **Conflict holds belong to the identity, not to one document.** Every name
  the identity has ever supplied (`client.name` on each submission — an
  email-only name is upgraded when a real name arrives) and every adverse
  party it has ever named (all its submissions and `adverse_party` facts) is
  checked on every submission. While any of its submissions is on
  `conflict_hold`, or once a partner has **declined** one, every new
  submission from it is held for a partner too (a declined identity is
  re-held each time with a "previously declined" flag — never auto-admitted).
- A new identity is **never** attached to an existing roster client by name or
  email (a name collision would otherwise hand a stranger that client's
  profile and matters).
- A prospect joins the roster (auto client number `AM-<n>`) only when one of
  its submissions **lands** — i.e. after the conflict check clears, or after a
  partner acknowledges the conflict (§3.1).

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
  "matterType": "DIVORCE",               // portal pack code — informational only
  "jurisdiction": "US-CA",               // BCP-47-style region tag
  "practiceArea": "Family",              // optional — BigLaw's own vocabulary (see below)
  "noslegal": { "areaOfLaw": "Family", "workType": "Litigious" }, // optional NOSLEGAL v4 tags
  "adverseParties": [                    // optional — the other side(s), for the conflict check
    { "name": "Robert Client", "role": "spouse" }
  ],
  "summary": "Client seeks ...",         // optional plain-text abstract
  "content": "…full draft text…",        // required — the rendered draft
  "facts": [                              // optional — seeds CRM proposals
    { "category": "goal", "predicate": "custody", "value": "Primary custody of both children" }
  ],
  "metadata": { "portalVersion": "5.0.0" } // optional, stored verbatim
}
```

**Behaviour** (in order):

1. Find the CRM profile by `client.externalId`, or create a prospect (§2).
2. **Conflict check, both directions, every hit, identity-wide (§2):** every
   name the client has supplied against every existing client's adverse
   parties, *and* every adverse party the client has named (`adverseParties`
   plus any `adverse_party` facts, across all its submissions) against every
   existing client — the main case being that the opposing party is already
   the firm's client. A returning client is never checked against itself.
   Names are matched leniently (accents, punctuation, apostrophes, hidden
   characters, surname-first and middle initials are all normalised);
   over-matching only means a lawyer looks. When the check is weak — the
   identity never supplied a real name (only an email) or named no adverse
   party — the firm-side record carries `conflictUnchecked`
   (`name_is_email`, `no_adverse_parties`); the portal should send the
   person's legal name and `adverseParties` (the opposing party / other
   parent) to make it meaningful. Caps: ≤ 50 adverse parties per request,
   ≤ 200 per identity, ≤ 20 names per identity (`400` beyond — never a silent
   truncation of the check).
3. **On any hit (or an open/declined hold on the identity) →
   `conflict_hold`.** The draft is kept only inside the held submission
   record. Nothing else happens: no knowledge-store ingest, no roster client,
   no matter, no fact proposals, no agent work. The hold is audited
   (`intake.conflict_hold`). Holds are **never auto-acknowledged** — a
   re-submit that no longer names the conflicting party stays held. Only a
   partner clears a hold (§4, `POST /intake/submissions/:id/conflict`): an
   acknowledgement requires a reason (informed-consent waiver, ethical wall,
   false positive …) and covers exactly the hold reasons it was given for;
   the check is then **re-run against the roster as it is now**, and anything
   new (e.g. the client's spouse became a client meanwhile) re-holds the
   submission with the new detail (`409`) instead of landing it. A decline
   rejects the submission, discards the held draft and marks the identity
   (§2). Submissions, conflict decisions and proposals are serialised, so two
   simultaneous submissions cannot both pass a check only one of them would
   pass after the other landed.
4. Otherwise (or once acknowledged) the submission **lands**: the prospect is
   admitted to the roster (the named adverse parties are recorded on it for
   future checks), a matter is opened, `content` is ingested into the
   knowledge store (`source: "client-intake"`, a client-voiced document), one
   CRM **proposal** is seeded per entry in `facts` (proposer `client`,
   approver `lawyer`), and `intake.submission_received` is audited.

**Classification:** the matter, the submission and the ingested document carry
`practiceArea` from BigLaw's own vocabulary — a canonical BigLaw practice area
in `practiceArea` (case-insensitive) wins, then NOSLEGAL `areaOfLaw`, then a
free-text `practiceArea`. NOSLEGAL tags land on the document. `matterType` is
stored and displayed but never drives behaviour.

**Response `201`:**

```jsonc
{
  "submission": {
    "id": "9f0e…",                        // BigLaw submission id — store it
    "externalId": "doc-4711",
    "status": "received",                 // or "conflict_hold"
    "clientId": "…", "clientNumber": "AM-1", // "" while held (prospect)
    "matterNumber": "AM-1-1",
    "practiceArea": "Family",
    "crmProfileId": "…",
    "documentId": "…",                    // knowledge-store document
    "conflict": { "hasConflict": false }, // portal never learns with whom
    "createdAt": "2026-08-03T12:00:00Z"
  }
}
```

Re-posting the same `externalId` for the same client **updates** the existing
submission (new content re-ingested, status reset to `received` unless it is
`ready`/`rejected`) rather than duplicating it.

### 3.2 `GET /intake/submissions/:id?clientExternalId=<externalId>` — one submission

Scoped to the calling client: `clientExternalId` (URL-encoded) is
**required** and, being in the query, is covered by the signature.
`200 {"submission": {...}}` — same shape plus `"note"`, `"assignedToName"`,
`"taskId"`, `"updatedAt"`. `400` without `clientExternalId`; `404` if unknown
**or** if the submission belongs to any other client.

### 3.3 `GET /intake/clients/:externalId/submissions` — all of a client's submissions

`200 {"submissions": [ ... ]}` newest-first. The portal dashboard uses this
single call to decorate document rows with live status.

**Submission `status` vocabulary** (BigLaw → portal, superset-safe: treat
unknown values as `in_review`):

| Status | Meaning |
|---|---|
| `received` | Landed, awaiting a lawyer |
| `conflict_hold` | Conflict check flagged; nothing has been worked on; a partner must clear or decline |
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
  "facts": [
    { "category": "contact", "predicate": "phone", "value": "+1 555 0100", "note": "" }
  ]
}
```

`201 {"proposals": [fact, …]}` — each created `pending`, approver `lawyer`.
Caps: ≤ 20 facts/call, value ≤ 2000 chars, note ≤ 2000 chars.

Only an **admitted** client (on the roster) with **no submission on
conflict hold** may propose; otherwise `409` (a prospect's information stays
inside its held submission until a partner clears it). Proposals never
create a profile; a legacy `client` block is accepted and ignored.
`adverse_party` facts run through the identity-wide conflict check: a hit no
partner has acknowledged flags the profile for the firm (audited
`intake.conflict_flagged`) and holds the client's next submission.

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
GET    /intake/queue                      lawyer: own + unassigned not on hold · partner: all
POST   /intake/submissions/:id/claim      assign to self (lawyer: only unassigned and
                                          not on hold; partner: any)
PATCH  /intake/submissions/:id            [partner or assignee] { status, note } →
                                          drives §3.3 statuses (cannot enter or
                                          leave conflict_hold)
POST   /intake/submissions/:id/conflict   [partner] { decision: acknowledge|decline,
                                          conflictReason } — the only way out of
                                          conflict_hold (reason required to
                                          acknowledge; 409 + the new detail if the
                                          re-run check finds something new)
POST   /intake/submissions/:id/task       [partner or assignee] spin up an
                                          orchestrator review task (409 while on
                                          conflict_hold)
GET    /crm/profiles                      list profiles [any lawyer: admitted clients;
                                          partner: also prospects]
GET    /crm/profiles/:id                  profile + facts by status
POST   /crm/profiles/:id/facts            lawyer proposes → approver=client
POST   /crm/proposals/:id/decision        lawyer decides client proposals
POST   /crm/profiles/:id/query            { query, topK } semantic fact search
GET    /crm/proposals?approver=lawyer     firm-wide pending queue
GET    /modules                           module registry status
```

**Screening (Rule 1.18):** a held submission and every conflict detail (who
the conflict is with, the hits, acknowledgements) are visible only to
partners and the submission's assignee; other lawyers see neither held
submissions nor prospect CRM profiles (404), and get only `hasConflict` on
the submissions they can see.

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

- Breaking changes bump the signature prefix (`v1=` → `v2=`), this file, and
  the schema directory (`schemas/intake/v2/`). Additive optional fields stay v1.
- Firm-side responses (§4) carry the full conflict record (every hit, who it
  is with, acknowledgement); portal responses carry only `hasConflict`.
- Errors: `4xx/5xx` with `{"error": "message"}`. `5xx` messages are generic
  (`"internal error"`) — store/driver detail is logged, never returned. The portal must degrade
  gracefully: BigLaw being unreachable disables firm features for the request
  but never breaks self-rep functionality.
