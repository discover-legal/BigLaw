# BigLaw 2 — guide for AI assistants

This is the `v2` rewrite: one Go module at the repo root (`github.com/discover-legal/BigLaw`),
PostgreSQL only. `README.md` is the contract for what is on disk; keep it true in the same
change. Design detail lives in `docs/`: `architecture.md` (packages, import rules, a task's
lifecycle), `patterns.md` (why the code is shaped this way), `glossary.md` (vocabulary, G/P/E codes),
`documents.md`, `figures.md`, `reference.md` (in depth), `benchmarks.md`, `user-guide.md` (for
lawyers) and `consolidation-proposals.md` (open owner decisions). Git history and PR notes are the
record of what each PR did. v1 (Go under `biglaw-go/`, DyTopo) is frozen on the branch
`archive/v1-final`, not here.

## Map (what each part owns; where its detail lives)
- `cmd/biglaw` — `serve`/`version`, the env-var table (`config.go`), the vendor breakers, startup
  wiring (embed backfill, transcription jobs, orphan sweep), `bad_host`, graceful stop. README
  § Configuration, § Exposure.
- `internal/store` — `Open` (DSN check, pgvector, migrations), `InTx`, chunked `bytea` files
  (16 upload / 4 read slots), name and id policy. README § Database, § Files.
- `internal/docs` (+ `ooxml`, the `.docx` reader) — documents, sections, chunks: ingest (PDF and
  `.docx` in the bounded child), vision transcription and its jobs, the section tree, lexical and
  hybrid search, the page reader, the verification ladder, the document routes, and the `retrieve`
  and `verify_quote` tools. README § Documents; docs/documents.md.
- `internal/model` — the only model client (chat, tool calls, images, `Embed`), typed errors,
  `Roles`, prices, `model.Fake`. README § Model client; docs/reference.md § Model client.
- `internal/agent` — the runtime: registry, schema subset, `Runtime.Run`/`Invoke`, `Budget`, the
  meter, `Journal`, the bandit, `LoadSubagent`. docs/reference.md § Agent runtime; the schema
  subset (unknown keywords are errors) docs/patterns.md § 1.
- `internal/analysis` — pure functions over findings: figure spans, label checks, the join,
  `numbers`, coverage, normalisers, BELO-lite. docs/figures.md.
- `internal/engine` — the memo workflow (lead, ledger, admission, sweep, figure reader and judge,
  deviations, drafting, committee, coverage pass, finish, gates, grounding collapse, durable runs)
  and Manage (spec set, overrides, validation, skills, packs, hooks, custom tools, remote calls).
  README § Memo workflow, § Manage; docs/architecture.md §§ 2–7; docs/figures.md; docs/reference.md.
- `internal/auth` — sign-in: OIDC code flow + PKCE, ID-token verification, sessions, `IsAdmin`.
  README § Sign-in.
- `internal/api` — the REST surface (request bodies validated by the agent's schema subset), SSE,
  Manage routes, `Principal`, `RequireSignIn`, the embedded UI. README § Memo workflow (REST API), § Sign-in.
- Test support: `internal/store/storetest` (a schema per DB test, slots), `internal/engine/figtest`
  (the figure reader's stand-in), `internal/auth/authtest` (a fake OpenID provider).
  docs/patterns.md § Test patterns.
- `ui/` — React 19 + Vite + TS strict: Matters, Documents, Task, Manage behind
  `ui/src/api/transport.ts` (`http.ts` live, `recorded.ts` the demo replaying
  `ui/src/recorded/recording.json`, made by `eval/demo`). README § UI.
- `eval/` — proof-run matters, driver and scorer (`run.py`, `proofrun/`), the Harvey LAB driver
  (`lab/`), Go gates E1/E2 (`TestProofRunsOracle`, `TestProofRunsFabricator`), the E4 tape, and the
  pinned bench (`eval/bench/`). README § Memo workflow; docs/benchmarks.md.
- `setup.sh` — the one-command installer (README § Install; `TestSetupScriptMatchesRepo`).

Planned, not built: see README § What exists today (next: PR 3d the `.docx` writer and tracked
changes, PR 5 diligence). Lexical search alone measured E4 0.882; the hybrid gate is not yet judged.

## Rules
- Dependencies: `github.com/lib/pq` and `github.com/ledongthuc/pdf` (budget 3). No ORM, no web
  framework (stdlib `net/http`), no OIDC/JOSE library.
- Never parse an uploaded PDF or `.docx` in the server's process: `docs.Extract` runs them in a
  bounded child (`ledongthuc/pdf` can die of a fatal stack overflow; the child has a deadline, dies
  with its parent and has a hard memory cap on Linux). `main` and the `TestMain` of every package
  whose tests ingest a PDF or `.docx` call `docs.MaybeRunChild()` first (forgetting it is an error,
  `errNoChild`). Tests that start a child must never leave one running (kill it in `t.Cleanup`).
  The child has no environment, except that a race-detector test build passes `GORACE=atexit_sleep_ms=0`
  (`child_race.go`: no one-second exit sleep per PDF/.docx read under `-race`).
  The child's output is a JSON header line plus raw text, read through `readChild`'s bounds; it is
  `/proc/self/exe` on Linux. A server stop cancels in-flight requests after `shutdownGrace`
  (`docs.ErrStopping`, 503).
- Every `docs` read takes the caller's scope; never read `documents`/`chunks`/`files` around it.
  Bounds to keep: document text ≤ 16 MiB (`maxText`), table chunks grouped (no per-row copy of the
  header), ≤ 32 query terms and a 10 s search timeout, chunks inserted by `COPY`. Never add an ANN
  index to the vector leg (it breaks scoped recall); a query is embedded only after the word check
  and only when the scope has vectors. An embedding failure leaves the document lexical
  (`EmbedNote`), never fails the ingest.
- New env var = add it to `envVars` in `cmd/biglaw/config.go`, README's table and
  docker-compose; `TestEnvVarTablesAgree` fails otherwise. Declare a variable in the PR that
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
  (`specs/*.json`) and its schemas strict-shaped; tool outputs are bounded by type and never trimmed: a
  list or long text is one page of `agent.PageBytes` (`agent.Page`/`Take`/`TakeText`) with `next` or
  `truncated` in its output schema, and a test that fills it past the page; the runtime refuses a result
  over its cap with the typed `TooLarge` error (`agent.ErrorSchema`), never cuts one; a tool panic is
  `ErrPanic` and fails only its task; a tool reports its usage in `Result` even when it fails;
  errors the model may read are `agent.Fail(...)` in BigLaw's words (anything else shows as "internal
  error"). `analysis` imports only `agent`; the allowed import table is `internal/agent/boundaries_test.go`
  (`TestImportBoundaries`): add a row for a new package. No matter data in any schema
  (`TestToolSchemasContainNoMatterData`), including description examples.
- No source file over 2,000 lines (`scripts/loc.py`). Every `.go`/`.py` file starts with the
  SPDX header.
- Every package has a `_test.go`; DB tests use `storetest.DSN(t)` (or `storetest.Open(t, store.OpenPool)`,
  the test pool) and `t.Parallel()`; only a test of the production pool's bounds uses
  `store.Open`/`store.MaxOpenConns`. Tests are filed by subject (the code they pin), never in a file
  named after a review round; the round goes in the comment. `figtest` is test data, never a rule of
  the engine. A doc that cites a test must name a real one (`TestDocsCiteRealTests`).
- Gate E4: the committed `eval/e4/embeddings.json` is the offline self-test tape (hash embedder,
  model `biglaw-selftest-hash`) and `TestHybridRecallProofRuns` SKIPS on it: never present its
  numbers as the gate (the owner records a real one with `make e4-record`). A chunker or fixture
  change makes the tape stale (`TestE4TapeReplays` fails): re-record it with
  `make e4-record EMBED_MODEL=biglaw-selftest-hash` and say so in the PR.
- Images and scans go only to the `vision` role (`docs.UseVision`); never wire another role's
  model there (`TestTextModelNeverSentImages`, `TestServeNeverSendsImagesToTheLead`). Text a model
  transcribed is evidence of a lower class: keep its provenance and the `@transcript` cap
  (`TranscriptCap`, 0.85). `[illegible]` is never filled; a mixed PDF's text-layer pages are never
  touched; a mixed page is never held or refused (no vision role or a failed read: its text layer
  plus a note).
- Engine: a lead step's ledger writes and bandit stats go through `e.write`/the step buffer and commit
  with the step (never write the ledger around it); every run change checks the lease (`lease_owner`)
  and status. The deliverable is rendered only by `finish`; findings that did not verify never reach
  a drafter, a subagent's ledger read, or the memo (G3). A new tool or subagent spec goes in
  `internal/engine/specs/` and must validate (`TestSpecsLoadAndValidate`); what Go needs from a built-in tool or subagent name
  (the lead's list, Go's callers and inputs, ledger writers, hooked, passes…) is its one row in `goContract`
  (`register.go`), never a new hand-kept list. Every model call inside a journaled tool step is
  journaled as it happens (`agent.Meter`; a Go-side search uses `docs.SearchMetered`).
- Figures and evidence: what a number means is a model's reading (`read_figures`, every label checked
  by `analysis.ReadLabels`), never a word list in Go: never add a semantic number rule in Go (owner
  decision, lab-r4 r4). `analysis/figures.go` only finds spans and reads their digits as written. No
  Go rule decides whose figure, what measure, which period or which counterparty; the cross-document
  join asserts only what `judge_figures` says (only yes is a conflict). A dropped value is noted
  (`ValueNotes`), never silent. Only exact duplicates are merged (`restated`), never a similarity
  merge. `check_deviation`: never add word-list refusals or layout walks (the requirement is read in
  `contextOf`'s bounded window; the section tree is only a label); a section check not had admits the
  finding `Low` (gated), never drops it. docs/figures.md has the rules in full.
- Manage: read specs, prompts, packs and skills only through `spec(ctx, …)`/`prompt(ctx, …)`/
  `packsFor`/`r.sp()` (the task's snapshot), never a package global. The lead sees `leadVisible(sp)`,
  never `MemoVisible` alone. Deleting an item Go needs is refused (`goDepends`). A new editable file must map to an item (`itemOf`; `TestCatalogue`), and
  a change to any of them makes `ui/src/recorded/manage.json` stale (`make demo-record`;
  `TestManageRecording`, no DB). Any change to a pack's or skill's content resets it to candidate
  unless the save names a new reviewer. A hook quarantine is `Finding.HookBlocked` (`f.ok()`), never
  `Verified=false`, so G4 never counts it; only a `ruleFailure` follows a rule's failure policy, any
  other error fails the call.
- Remote calls (custom tools, MCP servers): every request goes through `remoteClient` (`checkURL` at
  save, `dialCheck` at connect). Secrets are read only by `secretValue`, sent only to their bound
  origin (`secretRef`), redacted from every answer (`redact`) and every error built from remote data
  (`scrub`), and never written to a response, log, error, snapshot or export. An imported
  description is untrusted text (`untrustedText`), and its tool stays disabled until an admin accepts
  it. Only tests set `RemotePolicy.AllowLoopback`/`RootCAs` (through `SetRemotePolicy`); `cmd/biglaw`
  sets only `Off`.
- Sign-in (`internal/auth`, stdlib only): decode ID-token claims by exact name only (`decodeClaims`):
  never `json.Unmarshal` claims into a struct (its case-insensitive matching lets `"Email"` override
  `"email"`); hold no lock across network I/O; never add server state an anonymous request can fill
  (a sign-in in flight lives only in its sealed login cookie). `IsAdmin` matches the sub exactly, or a
  verified plain-ASCII email byte-exact after ASCII lowercasing: never Unicode folding or a cleaned
  address. `Alive` (the SSE liveness check) never moves `seen_at`. Never log or return a token, code,
  verifier, state, cookie or the client secret. A member never sees another person's email
  (`api.names`, `authors`, `engine.DisplayName` in files): keep it so.
- No model calls in `go test`: only `model.Fake` or an `httptest` server. Model errors carry
  BigLaw's own fixed text and, of an error answer, only the provider's error type (or code) and message (the
  whole redacted by `redact`: the key and anything key-, token-, masked-key-, credential-, URL-, IP- or
  host-shaped, but never a dotted validation path like `messages.0.content`,
  then bounded: `providerError`); never the URL, host, key,
  a header or the rest of what the endpoint sent, so never `err.Error()` of a transport error. The API
  maps failures with `engine.ErrorType` (the document routes with `docs.writeError`). `model.Roles` is
  the one role accessor (`vision` and `embed` never fall back); every call is priced from the table
  whatever the host (no row: unknown cost, never $0); `model.Fake` errs when unscripted or exhausted,
  never a default reply.
- Errors and logs must never contain the database password or any key. Run user-supplied DSNs
  through `store.CheckDSN` before anything else sees them; print config values only through
  `shown` in `cmd/biglaw/config.go` (DSNs via `store.DescribeDSN`), never raw.
- Without sign-in there is no auth: only Compose (loopback-published) may pass `--allow-no-auth`, and
  every Compose port must name a loopback IP (`TestDeployFilesFollowExposureRule`); keep the no-sign-in
  behaviour exactly as it is when changing sign-in.
  Every POST route wears `docs.Guard` (cross-site writes 403 `cross_origin`); `routes` refuses
  requests by DNS name other than `localhost` (and, with sign-in, `BIGLAW_PUBLIC_URL`'s host: 403
  `bad_host`, DNS rebinding); the UI forbids framing. A new API route is behind the session
  automatically; a route that must be public goes in `api.public` with a reason.
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
Budgets: Go prod LOC reported with no limit (owner decision 17: the total ceiling is dropped; `python3 scripts/loc.py` prints today's figure, never write it here; each PR reports its lines vs its DESIGN budget; test-support packages such as `storetest` count), internal
packages ≤ 12 (9 used; test-support packages do not count), direct deps ≤ 3, env vars ≤ 20 (18 used), UI runtime
deps ≤ 2 (2 used), UI source lines (`ui/src`) ≤ 3,200 (raised from 2,400 for Manage), untested packages 0, longest source file ≤ 2,000 lines.
