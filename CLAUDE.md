# BigLaw 2 — guide for AI assistants

This is the `v2` rewrite: one Go module at the repo root (`github.com/discover-legal/BigLaw`),
PostgreSQL only. `README.md` is the contract for what is on disk; keep it true in the same
change. v1 (Go under `biglaw-go/`, DyTopo) lives on `main`, not here.

## Built so far
- `cmd/biglaw` — `serve [--allow-no-auth]`, `version [--config]`; `config.go` holds the complete
  env-var table (18 vars, each tagged with the PR that first reads it; `BIGLAW_MODELS` is the role
  map `role=id,…`, ids refused if they hold `://`, `user:pw@`, white space, control or invisible
  characters or `? = &`, are over 200 bytes or look like a key)
  and the vendor breakers. `serve` gives `docs` the embedder when an `embed` role is set and runs
  the embedding backfill at startup, and the vision model when a `vision` role is set and runs the
  transcription jobs at startup and every 15 minutes.
- `internal/store` — `Open` (check DSN, connect, migrate), `CheckDSN`, `InTx`, `DB`, files (chunked
  `bytea`, `PutFile` from an `io.Reader` under 16 upload slots, `GetFile`/`ReadFile` (streams
  one chunk at a time) under 4 read slots,
  `ValidName` = the file-name policy, `ValidID`), migration runner. `Open` creates the pgvector
  extension (`CREATE EXTENSION IF NOT EXISTS vector`, under an advisory lock) before migrating.
- `internal/docs` (PR 3a) — owns `documents`/`sections`/`chunks` (migration 0002). `Ingest`/
  `IngestText`, `Extract` (type from content; PDFs and `.docx` in a bounded child process:
  `child.go`), `Parse` (byte-exact, bounded section tree), `Chunkify`, `Search`, `Outline`,
  `Section` (paged), `Verify`/`VerifyQuote` (the ladder; the judge is a caller-supplied func),
  `Resolve`, `WriteFile` (streams a document's original), `Delete`, `SweepOrphanFiles` (serve runs
  it at startup), and `Routes` (`POST /documents/upload` with the read deadline,
  `GET /documents/{id}/file` streamed under a write deadline). Every read takes a scope (the
  task's document ids). Bounds to keep: document text ≤ 16 MiB (`maxText`), table chunks grouped
  (no per-row copy of the header), ≤ 32 query terms and a 10 s search timeout, chunks inserted by
  `COPY`. PR 3b (`embed.go`): `UseEmbedder` turns on hybrid search (RRF of the lexical ranking and
  an exact cosine ranking of the scope's embedded chunks; a lexical-only document's chunk counts
  its lexical rank twice; no ANN index, it breaks scoped recall; the query is embedded only after the
  word check and only when the scope has vectors),
  chunks embedded 64 per call at ingest outside the ingest slot (a failure leaves the document
  lexical only with `EmbedNote`, never fails the ingest), `Backfill`; a document is in the vector
  leg only when `embed_model` (cleared first, set after all its chunks at one length) is the current model.
  PR 3c (`vision.go`, `scanpdf.go`, `jobs.go`): `UseVision` (the `vision` role only; never a text
  role) transcribes images and text-less PDF pages whose images are whole JPEG streams (found by
  offset in the child, sent as they are); two readings per page by two prompts
  (`vision_prompts.txt`), agreement < 0.75 refuses (422 `transcription_unreliable`), < 0.9 is
  `lowConfidence`, `[illegible]` never filled; a mixed PDF's text-layer pages are never touched;
  `Transcript` provenance on the document (byte spans per page), `chunks.transcribed`, citations
  `…@transcript` capped at `TranscriptCap` 0.85; no vision role → stored `needs_transcription`
  + 503, and `TranscribePending` (leased, serve: startup + every 15 min) completes it later.
  `internal/docs/ooxml` — `.docx` reader (text parts only, linear XML pass).
- `internal/store/storetest` — `storetest.DSN(t)` gives a test its own schema, creates the
  `vector` extension once per database and puts its schema on the test's `search_path`.
- `internal/model` — `Client` (OpenAI-compatible chat + tool calls + image parts, typed errors,
  ≤ 3 attempts; a refused-but-billed 200 carries `Usage` on `*model.Error`), `HTTPStatus` (the one
  failure→status mapping), `Roles` (the one role accessor: lead/specialist/critic fall back to the
  lead, vision/embed never), `Embed` (`embed.go`: `/embeddings`, ≤ 256 inputs, vectors of one length with a sane norm,
  priced like chat, no usage = unknown cost), `Prices`/`LoadPrices`/`ParsePrices`
  (embedded `prices.json` + `BIGLAW_PRICES`, one parser; every call priced from the table whatever
  the host, no row = unknown cost, never $0; `BIGLAW_LOCAL_WATTS` only adds watt-hours),
  `Fake` (scripts keyed by agent name; unscripted or exhausted is an error; `Fake.Vectors` answers
  `Embed`).
- Gate E4: `eval/e4/embeddings.json` is replayed by `TestHybridRecallProofRuns`; the committed
  tape is the offline self-test (hash embedder, model `biglaw-selftest-hash`) and the gate SKIPS
  on it. Never present its numbers as the gate. The owner records a real one with `make e4-record`.
  A chunker or fixture change makes the tape stale (`TestE4TapeReplays` fails): re-record the
  self-test tape with `make e4-record EMBED_MODEL=biglaw-selftest-hash` and say so in the PR.

Everything else in README is marked planned (next: PR 3d the `.docx` writer and tracked changes;
contract v1's `content`/202/`capabilities` for vision intake are PR 8's). Lexical search alone measured E4 0.882; the hybrid gate is
not yet judged (self-test tape).

## Rules
- Dependencies: `github.com/lib/pq` and `github.com/ledongthuc/pdf` (budget 3). No ORM, no web
  framework (stdlib `net/http`), no OIDC/JOSE library.
- Never parse an uploaded PDF or `.docx` in the server's process: `docs.Extract` runs them in a
  bounded child (`ledongthuc/pdf` can die of a fatal stack overflow; the child has a deadline, dies
  with its parent and has a hard memory cap on Linux). `main` and the `TestMain` of every package
  whose tests ingest a PDF or `.docx` call `docs.MaybeRunChild()` first (forgetting it is an error,
  `errNoChild`). Tests that start a child must never leave one running (kill it in `t.Cleanup`).
  The child's output is a JSON header line plus raw text, read through `readChild`'s bounds; it is
  `/proc/self/exe` on Linux. A server stop cancels in-flight requests after `shutdownGrace`
  (`docs.ErrStopping`, 503).
- Every `docs` read takes the caller's scope; never read `documents`/`chunks`/`files` around it.
- New env var = add it to `envVars` in `cmd/biglaw/config.go` (with its PR), README's table and
  docker-compose; `TestEnvVarTablesAgree` fails otherwise. Validate a variable in the PR that
  first reads it, not before. Read env only through that table (literal names; `scripts/loc.py`
  rejects dynamic `os.Getenv`).
- Migrations: a new `internal/store/migrations/NNNN_name.sql` (next number, lowercase name).
  Never edit an applied one (checksums are checked). Pre-release exception: until v2's first
  release, `0001_init.sql` may still be rewritten; say so in the PR and tell reviewers to
  recreate dev databases (README § Database). They are forward-only and each runs in a
  transaction: no `COMMIT`/`ROLLBACK`, no `CREATE INDEX CONCURRENTLY`. Extensions are
  database-wide, which schema-per-test isolation cannot handle: an extension is created once
  outside the numbered migrations (`store.Open` and `storetest`, as `vector` is) before any
  migration uses one.
- No source file over 2,000 lines (`scripts/loc.py`). Every `.go`/`.py` file starts with the
  SPDX header.
- Every package has a `_test.go`; DB tests use `storetest.DSN(t)` and `t.Parallel()`.
- Images and scans go only to the `vision` role (`docs.UseVision`); never wire another role's
  model there (`TestTextModelNeverSentImages`, `TestServeNeverSendsImagesToTheLead`). Text a model
  transcribed is evidence of a lower class: keep its provenance and the `@transcript` cap.
- No model calls in `go test`: only `model.Fake` or an `httptest` server. Model errors carry
  BigLaw's own fixed text only (never the URL, host, key or anything the endpoint sent, so never
  `err.Error()` of a transport error); map them with `model.HTTPStatus`.
- Errors and logs must never contain the database password or any key. Run user-supplied DSNs
  through `store.CheckDSN` before anything else sees them; print config values only through
  `shown` in `cmd/biglaw/config.go` (DSNs via `store.DescribeDSN`), never raw.
- Until PR 8 there is no auth: only Compose (loopback-published) may pass `--allow-no-auth`, and
  every Compose port must name a loopback IP (`TestDeployFilesFollowExposureRule`).
- One Go version (1.26.9): go.mod's `go`/`toolchain` lines, the Dockerfile `FROM`, CI's
  `go-version` and the README move together (`TestGoVersionPinnedEverywhere`).

## Gates
```bash
make ci     # gofmt, vet, mod verify/tidy, static build, go test -race -shuffle, loc.py + tests, compose config -q
make lint vuln   # staticcheck, govulncheck (CI lint job)
# DB tests: BIGLAW_TEST_DSN=postgres://…?sslmode=disable BIGLAW_REQUIRE_DB=1 (Postgres with pgvector)
make e4-record EMBED_MODEL=…   # owner-run: record the E4 tape at the model's own length (make e4-live: no tape)
```
Budgets: Go prod LOC ≤ 15,500, final (owner decision 10; test-support packages such as `storetest` count), internal
packages ≤ 12 (test-support packages do not count), direct deps ≤ 3, env vars ≤ 20 (18 used), UI runtime
deps ≤ 2, untested packages 0, longest source file ≤ 2,000 lines.
