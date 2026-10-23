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
  `Section` (paged; a table in whole rows, `TablePageRows`, with its header row by reference), `Verify`/`VerifyQuote` (the ladder; the judge is a caller-supplied func),
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
  role) transcribes images and PDF pages with no text layer, or only a stamp (< 80 chars; kept after the
  transcript); a page with 80–399 chars over a page image is MIXED (`mixedMax`: read, the text layer stored
  verbatim after only the reading's lines it lacks, `beyond`: a line is subtracted only when its joined letters/digits (figures keep separators) start and end on the layer's word boundaries in the layer's joined words; when in doubt kept; VerifyQuote prefers an exact whole-word copy outside the transcript; never held or refused: no role or a failed read →
  text layer + unread-image note); ≥ 400 chars over any image is text (no call, noted), whose images are whole JPEG streams (found by offset in the child, sent as they
  are, re-checked in the server, header dimensions capped); two readings per page by two prompts
  (`vision_prompts.txt`), agreement < 0.75 refuses (422 `transcription_unreliable`), < 0.9 is
  `lowConfidence`, and so is any page whose readings differ on what matters (`compare`: a mark, a
  figure, a negation/modal, ≥ 5 words, word order; `PageRead.Disputed`); `[illegible]` is never
  filled (the kept reading is masked, by position via the LCS `align` with a mark as a one-word wildcard and two tie-breaks, ambiguity masked, where the other has a mark; readings stored as returned, folded only to compare); a mixed PDF's text-layer pages
  are never touched and its failed pages only noted; `Transcript` provenance on the document (byte
  spans per page, usage whenever a call was made, SQL NULL otherwise), `chunks.transcribed`,
  citations `…@transcript` capped at `TranscriptCap` 0.85; a 4xx refusal is 422 (a waiting doc: one attempt, run ends); a damaged image in a doc with text is noted, never refusing it; vision calls run
  in 2 reader slots outside the ingest slots; no vision role → stored `needs_transcription` + 503,
  and `TranscribePending` (leased, serve: startup + every 15 min; stops, config errors, rate limits
  and busy readers spend no attempt) completes it later.
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
- `internal/agent` (PR 4a) — the runtime: `Tool`/`Spec`/`Call`/`Result`, `Registry` (frozen,
  strict-shaped definitions; ≤ 22 tools, ≤ 14 visible; descriptions ≥ 3 sentences + "Do not use" + 1–3
  validating `Example: {…}` lines), `Schema` (the subset: unknown keywords are errors), `Runtime.Run`
  (one loop, lead depth 0 / `Subagent` depth 1) and `Runtime.Invoke` (the single dispatch path:
  offered → no nesting → alone → input schema → `Policy` → grant), `Budget` (holds and charges; a
  model call holds its worst-case cost, also a Go tool's through `Runtime.ChatWithin`; the root's floor only for `Final` tools), `Bandit` (hint line +
  subagent model pick; never refuses), `Journal`/`Step`, `LoadSubagents` (JSON spec + `.md` prompt).
  `internal/analysis` (PR 4a) — pure functions over `Finding`s: `Calc`, `Handles`/`Substitute`
  (`{Codename}`), `KeyFigures`, `Join`, `CheckCoverage`, normalisers, BELO-lite (`belo.json`), and the
  `numbers`/`join_discrepancies` tools over a `Source`. `docs.Service.Tools(rt)` — `retrieve` and
  `verify_quote` (judge on the critic role, within the grant via `rt.ChatWithin`; quote and passage fenced). Tool specs are JSON in `specs/` beside each package.
- Gate E4: `eval/e4/embeddings.json` is replayed by `TestHybridRecallProofRuns`; the committed
  tape is the offline self-test (hash embedder, model `biglaw-selftest-hash`) and the gate SKIPS
  on it. Never present its numbers as the gate. The owner records a real one with `make e4-record`.
  A chunker or fixture change makes the tape stale (`TestE4TapeReplays` fails): re-record the
  self-test tape with `make e4-record EMBED_MODEL=biglaw-selftest-hash` and say so in the PR.
- `ui/` (PR 11, pulled forward by owner decision 13) — React 19 + Vite + TS strict, the seven
  screens behind `ui/src/api/transport.ts`: `http.ts` (same-origin REST, routes planned from PR 4)
  and `recorded.ts` (replays `ui/src/recorded/recording.json` in the browser). `VITE_ENGINE=recorded`
  aliases the transport (vite.config.ts), so the recorded bundle has no network code
  (`test/offline.test.ts`). The recording is made by `eval/demo/record_test.go` (test-only, no Go
  prod lines) from v2's `internal/docs` over three `eval/tasks` matters; claims come from the answer
  keys, plus a scanned exhibit (`ui/src/recorded/*.jpg`, drawn by `eval/demo/make_scan.py`) read through the
  vision path by a scripted `model.Fake`, and a few quotes/one finding written for the demo (`demoQuotes`,
  `demoFacts`) so every ladder rung but the judge's shows; sample playbooks/time/profiles are labelled as such.
  `TestDemoRecording` (DB) fails when the committed recording is stale: `make demo-record`.
  `internal/api` embeds `ui/dist` (`make ui build` copies it into `internal/api/dist`; a build without
  it serves a notice at `/`).
- `internal/engine` (PR 4b) — the memo workflow: lead (`specs/lead.md`) over 11 tools (`MemoVisible`),
  the ledger (one `ledger` table, JSON rows by kind/key, writes buffered in the running step),
  evidence subagents whose findings Go verifies before admitting them (`register.go` `admit`: G2/G3,
  values must be in their quote, currency from the quote, near-duplicates merged), `sweep` (answer-sized
  pages, `sweepTokens`; up to `sweepWorkers` visits at once with timing-free grants and admission in plan
  order: a visit's `extract_facts` waits for its turn via `awaitTurn`), `check_deviation` (the requirement
  is verified in `requirementDocId` before the subagent runs; omissions confirmed by the judge), the
  coverage pass (`cover.go`: touched issues get one `analyze_issue` + redraft from `Allocation.Cover`,
  inside a passing submit or as a step before the forced finish's review), `committee`
  (3 context-free critics in parallel on explicit grants, delayed credit), `policy.go` (P1, P4,
  `Allocate`), `finish` (check = dry run; lint G5/G8, coverage, P6/P7; render; gates G7), grounding
  collapse G4, packs (`packs/*/*.md`, embedded, all candidate), and durable runs (`task_steps` +
  ledger + `tool_stats` in one tx per step, lease/heartbeat, resume by replay, lost grants charged (a graceful stop settles an in-flight tool's grant to its spend);
  migration 0005). `views.go` is what the API shows (DESIGN § UI contract).
- `internal/api` (PR 4b) — the REST surface (tasks, steps, SSE, gates, cancel, cost, clients) over
  the engine plus `docs.Routes`; request bodies validated by the agent's schema subset.
- `eval/` (PR 4b) — `run.py` (validate/selftest/score/compare/drive), `proofrun/`, `lab/` (Harvey LAB
  driver), `tests/`; Go gates `TestProofRunsOracle` (E1), `TestProofRunsFabricator` (E2),
  `TestLabRunnerSmoke`; `TestE2EServe` serves a scripted run for the browser when `E2E_ADDR` is set.

Everything else in README is marked planned (next: PR 3d the `.docx` writer and tracked changes, PR 3e, PR 5 diligence;
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
- UI: runtime deps `react` + `react-dom` only (exact versions; dev deps exact too); every screen
  talks through the `Transport` interface, never `fetch` directly; nothing in `recorded.ts` (or
  what it imports) may use the network; tests are `node:test` over `.ts` (Node ≥ 22.18 strips
  types; imports carry their `.ts` extension; a component a test renders is plain `.ts` with
  `createElement`, like `badge.ts`). Never present the recording as a model's output: keep its provenance header,
  the banner and the step `source` marks true. A change to `internal/docs` output or `eval/tasks`
  makes the recording stale: re-record (`make demo-record`) and say so in the PR.
- Agent runtime: every tool is registered (never called around `Runtime.Invoke`), its spec is data
  (`specs/*.json`) and its schemas strict-shaped; a tool reports its usage in `Result` even when it fails;
  errors the model may read are `agent.Fail(...)` in BigLaw's words (anything else shows as "internal
  error"). `analysis` imports only `agent`; the allowed import table is `internal/agent/boundaries_test.go`
  (`TestImportBoundaries`): add a row for a new package. No matter data in any schema
  (`TestToolSchemasContainNoMatterData`), including description examples.
- No source file over 2,000 lines (`scripts/loc.py`). Every `.go`/`.py` file starts with the
  SPDX header.
- Every package has a `_test.go`; DB tests use `storetest.DSN(t)` and `t.Parallel()`.
- Images and scans go only to the `vision` role (`docs.UseVision`); never wire another role's
  model there (`TestTextModelNeverSentImages`, `TestServeNeverSendsImagesToTheLead`). Text a model
  transcribed is evidence of a lower class: keep its provenance and the `@transcript` cap.
- Engine: a lead step's ledger writes and bandit stats go through `e.write`/the step buffer and commit
  with the step (never write the ledger around it); every run change checks the lease (`lease_owner`)
  and status. The deliverable is rendered only by `finish`; findings that did not verify never reach
  a drafter, a subagent's ledger read, or the memo (G3). A new tool or subagent spec goes in
  `internal/engine/specs/` and must validate (`TestSpecsLoadAndValidate`).
- No model calls in `go test`: only `model.Fake` or an `httptest` server. Model errors carry
  BigLaw's own fixed text only (never the URL, host, key or anything the endpoint sent, so never
  `err.Error()` of a transport error); map them with `model.HTTPStatus`.
- Errors and logs must never contain the database password or any key. Run user-supplied DSNs
  through `store.CheckDSN` before anything else sees them; print config values only through
  `shown` in `cmd/biglaw/config.go` (DSNs via `store.DescribeDSN`), never raw.
- Until PR 8 there is no auth: only Compose (loopback-published) may pass `--allow-no-auth`, and
  every Compose port must name a loopback IP (`TestDeployFilesFollowExposureRule`).
  Every POST route wears `docs.Guard` (cross-site writes 403 `cross_origin`); `routes` refuses
  requests by DNS name other than `localhost` (403 `bad_host`, DNS rebinding); the UI forbids framing.
- One Go version (1.26.10): go.mod's `go`/`toolchain` lines, the Dockerfile `FROM`, CI's
  `go-version` and the README move together (`TestGoVersionPinnedEverywhere`).

## Gates
```bash
make ci     # gofmt, vet, mod verify/tidy, static build, go test -race -shuffle, make eval, loc.py + tests, compose config -q
make ui build    # the binary with the web UI embedded (ui/dist → internal/api/dist)
make lint vuln   # staticcheck, govulncheck (CI lint job)
make ui          # cd ui && npm ci && npm run build && npm test (CI ui job, Node 22)
make demo-bundle # ui/dist-recorded for discover.legal/demo/biglaw/; make demo-record re-records it
# DB tests: BIGLAW_TEST_DSN=postgres://…?sslmode=disable BIGLAW_REQUIRE_DB=1 (Postgres with pgvector)
make e4-record EMBED_MODEL=…   # owner-run: record the E4 tape at the model's own length (make e4-live: no tape)
```
Budgets: Go prod LOC reported with no limit (owner decision 17: the total ceiling is dropped; 12,728 used; each PR reports its lines vs its DESIGN budget; test-support packages such as `storetest` count), internal
packages ≤ 12 (8 used; test-support packages do not count), direct deps ≤ 3, env vars ≤ 20 (18 used), UI runtime
deps ≤ 2 (2 used), UI source lines (`ui/src`) ≤ 2,400, untested packages 0, longest source file ≤ 2,000 lines.
