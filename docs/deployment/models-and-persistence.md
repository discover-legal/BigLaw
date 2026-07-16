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
TASKS_FILE=.tasks.json
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

## Vector storage

Three in-process stores with cosine-similarity search, no external service or native module
required (for a bench this size, brute-force cosine runs in ~1 ms even on ARM64):

| Store | Persistence | Used for |
|---|---|---|
| Agent registry | `./data/agents.json` | Semantic agent recruitment + outcome tracking |
| Inter-round memory | in-memory | Cross-round context retrieval |
| Knowledge base | in-memory | Document chunks + semantic search |

Related: [Getting started](../getting-started.md) · [Legal notices — confidentiality](../legal-notices.md#confidentiality-and-data-security) · [Secrets](secrets.md)
