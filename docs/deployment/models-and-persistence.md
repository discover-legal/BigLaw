[Docs](../index.md) › Deploy & operate › **Models, persistence & documents**

# Model stack, persistence & documents

## Model stack

The default is **Qwen** over DashScope's OpenAI-compatible API. Four tiers plus a
vision tier for omnimodal document extraction:

| Tier | Role | Default (qwen) |
|---|---|---|
| Heavy | synthesis · debate · root orchestrator · high-complexity | `qwen-max` |
| Mid | managers · specialists · drafting · extraction reconcile | `qwen-plus` |
| Light | descriptors · extraction · routing · translation · tool agents · classification | `qwen-turbo` |
| Vision | images · scanned / handwritten documents | `qwen-vl-max` |

`MODEL_STACK` selects the family — `qwen` (default) · `glm` · `kimi` · `custom` — and you can point
any stack at an arbitrary OpenAI-compatible endpoint with `PRIMARY_MODEL_URL`/`PRIMARY_MODEL_KEY`.
BigLaw is open, free, secure, and private, and it prioritizes vendors that share those values;
high-risk, closed vendors that make ecosystem-harming moves are gated by a startup breaker
regardless of their popularity, and running against one takes a deliberate operator override.

How the tiers map to agent roles round-by-round: [Model routing](../architecture/model-routing.md).
Running any or all tiers on your own hardware: [Local inference](local-inference.md).

## Persistence & row-level security

Documents persist through a storage seam:

- **SQLite** (default, pure-Go, no cgo) at `./data/biglaw.db` — ideal for local/Pi installs.
- **Postgres** (`DATABASE_URL`, e.g. Supabase, Neon, self-hosted) with **`FORCE` row-level
  security**, **default-deny** policies keyed on the request's lawyer/partner identity (set
  per-transaction), layered *under* the existing application-layer access checks (defense in depth).
  ⚠ RLS only binds **non-superuser** roles — connect as a plain app role (on Supabase, not `service_role`).

### Task queue and snapshots

Task admission uses persisted task state as its durable queue. Submission returns only after
the queued record is written; restored `queued`/legacy `pending` tasks are admitted in creation
order. Configure execution independently from the generic operations-job queue:

```bash
QUEUE_CONCURRENCY=3   # active orchestrator tasks
QUEUE_MAX_PENDING=1000
# TASKS_FILE defaults to <data dir>/.tasks.json (see "State files" below)
```

Task snapshots are serialized by one writer directly to a temporary file and atomically
renamed. The encoder holds a consistent read snapshot, so task slices cannot mutate while
being serialized and no second full JSON buffer is required. This JSON persistence remains
appropriate for a single BigLaw process; horizontally scaled deployments should move task
state and queue claims to a transactional database before adding replicas.

The queue is deliberately bounded. Reaching `QUEUE_MAX_PENDING` returns `503 Service
Unavailable` with `Retry-After`; this is an emergency boundary rather than the normal load
path. Normal saturation remains queued and visible through task position and ETA fields.

## Omnimodal documents

`/documents/upload` accepts PDF (digital + scanned), Word (`.docx`),
images, and text. Extraction is hybrid: the embedded text layer is verbatim ground truth and the
vision model (Qwen-VL) reconciles scans/tables/figures; standalone images go straight to the VLM.
Original images/PDFs are **retained** as attachments (metadata RLS-scoped; bytes in the blob store)
and can be **placed** into generated PDFs. The blob store is pluggable across open, vendor-neutral
backends — local **disk** (default), **WebDAV**, **Supabase Storage** (native API), or an **OCI
registry** via ORAS (`BLOB_BACKEND`); AWS S3 is deliberately not offered. Endpoints:

```
GET  /documents/attachments/:docId           list a document's retained attachments
GET  /documents/attachments/:docId/:attId    stream an attachment's bytes (RLS-scoped)
GET  /documents/export/:docId                render the document (text + images) to PDF
```

Full configuration lives in `.env.example` (`MODEL_STACK`/`QWEN_*`, `DB_BACKEND`/`DATABASE_URL`,
`BLOB_DIR`, `EXTRACT_VISION_*`).

## State files

All state lives under one data dir — `RUVECTOR_DATA_DIR`, default `./data` — so a single
volume (the Docker stack's `biglaw-data`) holds everything, and the data dir's single-writer
lock (`biglaw.lock`, see [Run modes](run-modes.md)) guards all of it. Every default below is
derived from the data dir; at startup the owner asserts that no state path that is not
explicitly overridden lies outside it.

| In the data dir | Override |
|---|---|
| `biglaw.db` (+ `-wal`/`-shm`/`-journal`) | `SQLITE_PATH` |
| `.tasks.json`, `.settings.json`, `.profiles.json`, `.clients.json` | `TASKS_FILE`, `SETTINGS_FILE`, `PROFILES_FILE`, `CLIENTS_FILE` |
| `.time-entries.json`, `.qtable.json`, `.ocg.json`, `.jobs.json` | `TIME_FILE`, `LEARNING_FILE`, `OCG_FILE`, `JOBS_FILE` |
| `audit.jsonl`, `costs.jsonl` | `AUDIT_LOG_FILE`, `COST_LOG_FILE` |
| `attachments/` | `BLOB_DIR` |
| `prebills.json`, `playbooks.json`, `clientvoice.json`, `dockets.json`, `clio-tokens.json` | `PREBILLS_FILE`, `PLAYBOOKS_FILE`, `CLIENT_VOICE_FILE`, `DOCKETS_FILE`, `CLIO_TOKENS_FILE` |
| `status-reports.jsonl`, `reports/`, `routed-emails.jsonl`, `pending-drafts.json`, `backfill-cursor.json` | `LPM_CORPUS_FILE`, `LPM_REPORT_DIR`, `LPM_ROUTED_FILE`, `LPM_PENDING_FILE`, `LPM_BACKFILL_CURSOR_FILE` |
| `agents.json` | — |
| `.matter-budgets.json` | follows `CLIENTS_FILE`'s directory |

**Upgrading from a build that kept state elsewhere** — dotfiles in the working directory
(`./.tasks.json`, `./audit.jsonl`, …), or `./data/*` while `RUVECTOR_DATA_DIR` pointed
somewhere else: on its first start as the data-dir owner, BigLaw moves each legacy path to its
new default and logs every move. Stop any old-version BigLaw process first (old builds take no
lock). The migration is idempotent and conservative:

- a path moves only when the legacy copy exists and the data-dir one does not; if both exist
  the data-dir copy wins and the legacy one is left untouched with a warning;
- a `./data` that contains `biglaw.lock` is another install's data dir (a data-dir-era build,
  possibly running right now), not legacy state: nothing is migrated out of it, with a warning;
- a symlink moves as a symlink (a relative target is re-pointed so it still resolves);
- the database and its `-wal`/`-shm`/`-journal` move as one unit — companions first, the
  database last — recorded in `biglaw.db.migration-journal` (beside the destination) before the
  first move and removed after the last. If a start is interrupted mid-unit, the next start
  finds the journal and finishes the unit (or refuses, naming any file that is at neither end
  or differs at both); the database is never opened without its WAL;
- a legacy `-wal`/`-shm`/`-journal` whose database is gone, or one already at the destination
  before its database arrives, stops startup — it may hold commits, and pairing it with the
  wrong database (or dropping it) loses data;
- rename first; across filesystems a file falls back to copy + fsync + atomic rename + remove;
- anything that cannot be moved (a directory across filesystems, a directory where a file
  belongs, the companion cases above) stops startup with an error naming the env var and
  listing exactly which files moved and which did not — BigLaw never comes up on empty state
  while yours sits elsewhere.

An explicit `*_FILE`/`*_DIR` override always wins and is never migrated — **except** a value
equal to the old default (e.g. `TASKS_FILE=.tasks.json` or `AUDIT_LOG_FILE=./audit.jsonl`
copied from an old `.env.example`, or `SQLITE_PATH=./data/biglaw.db`), which counts as unset:
that file is migrated and the data-dir path is used.

## Vector storage

Three in-process stores with cosine-similarity search, no external service or native module
required (for a bench this size, brute-force cosine runs in ~1 ms even on ARM64):

| Store | Persistence | Used for |
|---|---|---|
| Agent registry | `./data/agents.json` | Semantic agent recruitment + outcome tracking |
| Inter-round memory | in-memory | Cross-round context retrieval |
| Knowledge base | in-memory | Document chunks + semantic search |

Related: [Getting started](../getting-started.md) · [Legal notices — confidentiality](../legal-notices.md#confidentiality-and-data-security) · [Secrets](secrets.md)
