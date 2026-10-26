# BigLaw 2 — guide for AI assistants

This is the `v2` rewrite: one Go module at the repo root (`github.com/discover-legal/BigLaw`),
PostgreSQL only. `README.md` is the contract for what is on disk; keep it true in the same
change. Design detail lives in `docs/` (`architecture.md`, `patterns.md`, `glossary.md`, and
`documents.md`, `figures.md`, `reference.md` moved from the README); per-PR named tests and line
budgets in `docs/history.md`. v2 is `main`; v1 (Go under `biglaw-go/`, DyTopo) is frozen on the branch
`archive/v1-final`, not here.

## Built so far
- `cmd/biglaw` — `serve [--allow-no-auth] [--admin-without-auth]`, `version [--config]`; `config.go` holds the complete
  env-var table (19 vars, each tagged with the PR that first reads it; sign-in's are `BIGLAW_OIDC_ISSUER`,
  `_CLIENT_ID`, `_CLIENT_SECRET`, `BIGLAW_PUBLIC_URL` and `BIGLAW_ADMINS` (PR 8), validated together by `signIn`; `BIGLAW_MODELS` is the role
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
  `IngestText`, `Extract` (type from content; a quoted-printable plain-text e-mail stored decoded, headers verbatim; PDFs and `.docx` in a bounded child process:
  `child.go`), `Parse` (byte-exact, bounded section tree), `Chunkify`, `Search`, `Outline`,
  `Section` (paged; a table in whole rows, `TablePageRows`, with its header row by reference), `Verify`/`VerifyQuote` (the ladder; a `|`-separated table row is matched as cells, tolerant at 0.9; the judge is a caller-supplied func),
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
  `vector` extension once per database and puts its schema on the test's `search_path`; at most
  `storetest.Slots` tests across all test processes hold one at once (a flock per slot). Test stores
  open with `store.OpenPool(ctx, dsn, log, storetest.Pool)`; only a test of the production pool's
  bounds uses `store.Open`/`store.MaxOpenConns`.
- `internal/engine/figtest` — test support: `figtest.Reader`, the figure reader's stand-in (a
  `model.Fake` `Respond` hook labelling plain fixture texts), `figtest.With` for a test's right labels;
  engine, API and eval tests use it (test data, never a rule of the engine).
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
  strict-shaped definitions; ≤ 48 tools, ≤ 24 visible (raised from 22/14 for custom agents and tools); descriptions ≥ 3 sentences + "Do not use" + 1–3
  validating `Example: {…}` lines), `Schema` (the subset: unknown keywords are errors), `Runtime.Run`
  (one loop, lead depth 0 / `Subagent` depth 1) and `Runtime.Invoke` (the single dispatch path:
  offered → no nesting → alone → input schema → `Policy` → grant), `Budget` (holds and charges; a
  model call holds its worst-case cost, also a Go tool's through `Runtime.ChatWithin`; the root's floor only for `Final` tools), `Bandit` (hint line +
  subagent model pick; never refuses), `Journal`/`Step`, `LoadSubagent` (JSON spec + `.md` prompt).
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
- `internal/engine` (PR 4b) — the memo workflow: lead (`specs/lead.md`) over 12 tools (`MemoVisible`, `use_skill` last),
  the ledger (one `ledger` table, JSON rows by kind/key, writes buffered in the running step),
  evidence subagents whose findings Go verifies before admitting them (`register.go` `admit`: G2/G3,
  values must be in their quote (`valueStated`: number meaning is a model's, never a word list in Go (owner decision, lab-r4 r4): `internal/analysis/figures.go` only finds spans and reads their digits as written (literal corpus, property and fuzz tests), the figure reader `read_figures` (critic role; its own tools `figure_context` and `exact_calc`, `figtools.go`) labels each span by id (kind, currency, power-of-ten scale and the exact words of its own passage that state it (adjacent: right after the figure or its currency code, earlier in its sentence, or the passage's first line when it is a header over a table row, `headerLine`/`tableRow`; a letter or a "000", never a bare "$"), decimal mark, party, counterparty, range, sign, what it measures, its period stated absolutely with its year or "none", what a typed value's entity is to it, or unclear), `analysis.ReadLabels` checks every label mechanically (unknown span ids rejected, a scale with no source in its own passage not applied, an amount always the written digits shifted), and consumers only compare readings: a typed value needs a quote span read as the same kind, currency, amount and scale (from equivalent words), an unclear match admits it gated (`Finding.Low`), a value's reading is `Value.Fig` (what `Canon`, the joins, `Calc` and `Salience` read; none: never compared), G8 traces a drafter's figure only by a clear reading of the section (`Section.Read`) matching a clear cited one and fails closed otherwise (flagged, or, when the cited quote was never read or one side is at a scale the other writes out, limited: disclosed and its citing finding gated, `lintLimited`), worksheets trace only from operands the cited quotes state as their units say (a ref, a literal read as that kind and currency, a period read as a duration in its unit or two cited dates' difference; a plain operand only a count or a bare number/multiple, never kind other, an identifier: `tracedSheets`; kinds `duration` with `Unit` and `number`, each with `UnitFrom`, the words beside the span that state its unit or multiple, checked by position and shape, `statesUnit`) and trace only a memo figure of their kind (`sheetKind`); every figure a section states that traces only through a worksheet, refs or literals alike (G8 run again without the worksheets decides, any written form, `{W…}` handles too), or only by a reader's label is in Limitations' one Computed figures block (`computed.go`: check-first and arithmetic tiers, each input with its [F#] and quote; ⚠ inputs, a unit's words or a count used as a period (unitless too, as `date_add`'s days) or a result over scaled inputs written out, by handle or at other scale words, gate their findings); `numbers` keeps "business days" (`date_add` refuses it); a memo duration traces only to a cited duration whose unit words agree as written, a worksheet's period or two cited dates' difference (`durationStated`; differing words: limited and gated); `date_add` by months/years clamps to the target month's last day; and the cross-document join is Go's candidates (`analysis.Candidates`: two clear readings of one kind and currency from two findings in two documents, values different, judged pairs first, then each value's nearest by context and closeness, `rankPairs`, capped at the judge's budget, `CandidateLimit`) decided by the `judge_figures` subagent (`judge.go`: yes/no/unclear per pair with a reason that must quote both passages and, for a yes, name both figures as written beside its quotations (`namesValues`), each side's figure marked ⟦…⟧ in its quote (`Reading.Stated`), one amount at two precisions or signs never proposed (`sameAmount`), checked by `CheckVerdicts`, batched, metered, verdicts in the ledger kind `verdict` (keyed by the two values in their documents) and yes/no cached in `figure_reads` keyed also by the task's documents and the aliases naming the pair (no-matter tasks: per task); only yes is a conflict, unclear a LOW Limitations line, unjudged pairs counted) (no Go rule decides whose figure, what measure, which period or which counterparty: `oneQuantity`/`periodOf` are gone, as are `sameMetric`/`partOf`/`onePair` and the near join); the reader in calls of at most 60 spans (`maxCallSpans`: its answer must fit the answer cap) and 32 KB, smallest items first: a sweep's findings read together at its end from the reader's own share (`Allocation.Read`/`ReadUSD`, `readFindings`, `readQueue`), a finding with no typed value read when a section first cites it (`Finding.Unread`), the lead's own admissions and each drafted section at once (its call held back from the subagent's grant and the quotes' judges, `goTurns`), finish reading stale or unfunded sections before its judge (`readSections`), quote spans inside a locator never sent (`quotePassage`), every failed call logged with its step and counted (`figstats`, the cost's reader line) and a failed batch read again in halves once, a finding whose read failed left `Unread`; finish also reads unread findings whose claims state a figure before its judge (`claimsUnread`, counted in Limitations when unfunded); a memo figure the reader's call never read (`neverRead`: unfunded, failed, cut off, malformed, no model; never an unlabelled span) that a cited finding's own quotes or values state as written is `limitedMemoUnread` (listed check-first naming those findings, gated), not a blocking lint line; one only a worksheet or handle states stays flagged, complete answers (not all unclear) cached in `figure_reads` (migrations 0006, 0007) under a key of the reader's spec hash, model, matter (a section's task) and what was sent, deleted with the task or document; never add a semantic number rule in Go; a dropped value is noted on its finding, `ValueNotes`, shown in the finding view, the sweep's notes, one LOW committee line per run and one Limitations line, never silent), only exact duplicates merged (`restated`: one issue, the same normalised quotes in the same documents, the same values with entity and metric; never a similarity merge); an `extract_facts` call with no issueId is sent the plan's issues and admits only findings naming one, no per-call cap; a claim's money sums kept only when the reader read the claim's span and a quote's as the same money, `claimSums`), `sweep` (answer-sized
  pages, `sweepTokens`; read most relevant first, `sweeporder.go`: BM25 of the plan's questions, taken in
  turn across issues; a visit's record (`visitRec`) holds the plan's issues it was read for: a page is read again for issues `op plan` added since, only those (`issuesKey`), and a page never read again for them is a Limitations line (`staleLine`); exact-duplicate and boilerplate pages skipped, reported and disclosed in Limitations (a copy of an unread page is unread; a table row is never boilerplate); each visit a single call on
  its page, `agent.Call.NoTools`; each document's coverage in the ledger, kind `swept`; up to `sweepWorkers`
  visits at once with timing-free grants and admission in reading order: a visit's `extract_facts` waits
  for its turn via `awaitTurn`; a visit a worker's share cannot fund is read again serially), `check_deviation` (the requirement
  is verified in `requirementDocId` before the subagent runs, JSON escapes undone and a joined requirement
  part by part, `placeRequirement` (`requirement.go`: the document's words, in order, one passage), then read in a bounded window of the text around it (`contextOf`: ~3 KB before and after, edges at sentence/line ends, ≤ 8 KB whatever `docs.Parse` says, the heading path a label only, never a boundary; `Truncated` when the requirement's sentence or paragraph (a blank line, or a line break after a sentence end before a capital/marker: `.docx` paragraphs) starts before the window, its first line is a list item, or an edge cuts a sentence): the subagent judges against it, its controlling quote must lie in it, the finding quotes it; before the subagent runs, out of the call's grant first, one critic call (`checkSection`, `rt.ChatWithin`, the window fenced and nothing else: never the verdict, its correction or a reviewed document; answer from one strict JSON object, `checkAnswer`) answers yes/no/unclear whether the document imposes the requirement as quoted: no fails the call, unclear or truncated admits `Low` (gated), a check not had (unfunded, failed, malformed) admits `Low` (gated), never drops; whether a draft complies is the subagent's question; an override > ~3 KB away or in another clause is not seen (README); never add word-list refusals or layout walks; a refusal quotes the nearest exact passage, bounded and fenced;
  omissions confirmed by the judge), the
  coverage pass (`cover.go`: touched issues get one `analyze_issue` + redraft from `Allocation.Cover`, then
  salient verified findings no section cites and figure conflicts the memo does not state get one redraft
  per issue, `placements`, strongest first, from the `placeShare` (40%) of the pass's USD its analyses leave; inside a passing submit or as a step before the forced finish's review;
  an unaddressed conflict is a finish lint line), `committee`
  (3 context-free critics in parallel on explicit grants, delayed credit; the lead runs at most `maxCycles` (3), the
  coverage pass's re-review beyond it, and the pass then places left-out findings from half of what is free), `policy.go` (P1, P4,
  `Allocate`), `finish` (check = dry run; lint G5/G8, coverage, P6/P7; render; gates G7), grounding
  collapse G4, packs (`packs/*/*.md`, embedded, all candidate), and durable runs (`task_steps` +
  ledger + `tool_stats` in one tx per step, lease/heartbeat, resume by replay, lost grants charged (a graceful stop settles an in-flight tool's grant to its spend; every model call inside a journaled tool step is journaled as it happens, `agent` `meter` (query embeddings too, `agent.Meter`; a Go-side search uses `docs.SearchMetered`), so a hard crash charges the recorded spend plus the in-flight calls' worst cases);
  migration 0005). `views.go` is what the API shows (DESIGN § UI contract).
- Manage (`internal/engine/specset.go`, `manage.go` (+ `manage_validate.go`), `diff.go`, `skills.go`; migration 0008;
  `internal/api/manage.go`; `ui/src/screens/Manage.tsx`) — every agent, tool, pack and skill is a built-in
  file an admin may override with a version in `spec_versions` (save, revert, a skill created or deleted;
  the table is the history and the change log); `Submit` snapshots the overrides into `tasks.specs` and
  `execute` builds the run's `Specs` from it on every claim (resume too): read specs, prompts, packs and
  skills only through `spec(ctx, …)`/`prompt(ctx, …)`/`packsFor`/`r.sp()`, never a package global;
  `validate` = strict per-file checks + the whole set through `e.runtime` + `compatSpec` (no property the
  built-in schema has may be removed or retyped, no enum changed; Go-written schemas (`goContract`'s `goWrites`) gain
  none; built-in examples and each `goContract` row's `sent` input must validate) + no lead-only tool (Final, Solo ops but
  `ledger`) in a subagent's allowed list + class `free` only for subagents built free; saves are serial
  (`locked`: advisory lock, one tx); any change to a pack's or skill's content (every file, all front
  matter but status/reviewedBy) resets it to candidate unless the save names a new reviewer; the reader's and judge's cache keys hash the run's set (`Specs.agentHash`: spec, prompt and every allowed tool's spec).
  Go's checks around a tool are its `goContract` row's `checks` lines (shown on its Manage page, view only;
  `TestWrappedToolsDeclareChecks`); configurable hooks (`hookrules.go`, items
  `rule.<name>` in `specs/hooks/`) run through `Runtime.Hooks` (Before/After around every Invoke, their
  usage in the step's), `admit` (`findingHooks`) and `finish` (`finishHooks`), every outcome a `hookrun`
  ledger row written with its step; an `approve` gate (lead calls only, keyed by a 128-bit hash of the
  canonical input, single use) pauses the run (`run.hold` → `errHeld` → `awaiting_gate`; `execute`
  pauses again on resume while one is open) until `Decide` resumes it; after_call rules on the hooked
  subagents run before Go records anything (`afterHooked`); a hook quarantine is `Finding.HookBlocked`
  (`f.ok()`), never `Verified=false`, so G4 never counts it; only a `ruleFailure` follows a rule's
  failure policy, any other error fails the call. Skills: `skills/<name>/SKILL.md` + reference
  files; the brief lists names and descriptions (≤ 40, ≤ 4 KiB); `use_skill` pages a body or file and
  records it (ledger kind `skill`); a candidate skill read is a Limitations line. `api.Principal` is the
  who-seam: with sign-in it is the session's user (`api.SessionUser`; admin when `BIGLAW_ADMINS` names
  them, else a member; author fields record `Principal.Label()`); without it every request is the local
  user (`api.LocalUser`): an admin on loopback, read-only under `--allow-no-auth`, an admin for anyone
  under `--admin-without-auth` (warned, bannered); writes wear `docs.Guard` + `admin`.
  A new editable file must map to an item (`itemOf`; `TestCatalogue`), and a change to any of
  them makes `ui/src/recorded/manage.json` stale (`make demo-record`; `TestManageRecording`, no DB).
  Custom agents and tools (`custom.go`, `remote.go`, migration 0009): admin-created subagents
  (`specs/agents/`), MCP servers (`specs/servers/`) and MCP/HTTP tools (`specs/tools/`); the lead sees
  `leadVisible(sp)`, never `MemoVisible` alone. Every remote request goes through `remoteClient` (https,
  `checkURL` at save, `dialCheck` at connect, ports 443/8443, one refusal text, same-host redirects, no
  proxy, 30 s per whole call (`withRemoteDeadline`), 1 MB, 64 KB headers); secrets are read only by
  `secretValue`, bound to their origin (`secretRef`, migration 0010; `do` sends one nowhere else),
  redacted of exact echoes in every answer (`redact`) and every error from remote data (`scrub`), never written to a response, log, error, snapshot or export;
  `RemotePolicy.Off` (serve with `--allow-no-auth`/`--admin-without-auth`) turns secrets and custom calls
  off; an imported description is third-party text (`untrustedText`, `importedDescription`) and its tool
  stays disabled until an admin accepts it (`acceptRule`); a tool's answer is labelled untrusted. `goDepends` refuses deleting what Go needs. Tests reach a local fake server only
  through `SetRemotePolicy` (never set by `cmd/biglaw`).
- `internal/auth` (PR 8) — sign-in: OIDC code flow + PKCE S256 (`oidc.go`: discovery, JWKS cache with a
  rate-limited refetch for an unknown kid, ID-token verification RS256/384/512 + ES256/384 with `none`/`HS*`
  refused before key lookup, iss/aud/azp/exp/iat/nbf/nonce; claims decoded by exact name only,
  `decodeClaims`: never `json.Unmarshal` claims into a struct, its case-insensitive matching lets
  `"Email"` override `"email"`; fetches coalesced by `shared`, no lock across network I/O),
  server-side sessions (`session.go`, migration 0011: only SHA-256 of the cookie id stored, idle 8 h /
  absolute 7 d, rotated at sign-in, logout deletes, `Revoke`, `Alive` for SSE (never moves seen_at);
  a sign-in in flight is AES-GCM sealed in the login cookie with a key made at startup: no row, no cap,
  never add server state an anonymous request can fill; `next` ≤ 512 bytes), `IsAdmin` (sub exactly, or a verified plain-ASCII
  email byte-exact after ASCII lowercasing: never Unicode folding or a cleaned address). Stdlib only;
  never log or return a token, code, verifier, state, cookie or the client secret.
  `internal/api/signin.go` `RequireSignIn` puts every route but the exact patterns in `api.public`
  (`GET /`, `/health`, the three `/auth` routes) behind a session (401 `unauthenticated`), `docs.Guard`
  on every unsafe method, mounts `POST /manage/sessions/revoke` (admin) and gives SSE streams a
  liveness check (`liveKey`). Tasks record `submittedBy` (migration 0012). A member never sees
  another person's email (`api.names`, `authors`, `engine.DisplayName` in files): keep it so. Under sign-in `--allow-no-auth` is ignored (warned), `--admin-without-auth` refused, the remote
  policy is on, and `bad_host` also admits `BIGLAW_PUBLIC_URL`'s host. Gate decisions record
  `Gate.DecidedBy` (`engine.Person`). `internal/auth/authtest` is the fake OpenID provider for tests.
- `internal/api` (PR 4b) — the REST surface (tasks, steps, SSE, gates, cancel, cost, clients) over
  the engine plus `docs.Routes`; request bodies validated by the agent's schema subset.
- `eval/` (PR 4b) — `run.py` (validate/selftest/score/compare/drive), `proofrun/`, `lab/` (Harvey LAB
  driver), `tests/`; Go gates `TestProofRunsOracle` (E1), `TestProofRunsFabricator` (E2),
  `TestLabRunnerSmoke`; `TestE2EServe` serves a scripted run for the browser when `E2E_ADDR` is set.
  `eval/bench/` (docs/benchmarks.md): the pinned suite (`config.json`, hash-checked; `make bench`, owner-run), the
  results schema, `compare.py` (yardstick hashes, three verdicts), baselines; `TestBench*` run it offline (smoke, a 401, serve
  preflight with the exact env). A setting change is `run.py pin` + commit; serve gets only the config's env.

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
  The child has no environment, except that a race-detector test build passes `GORACE=atexit_sleep_ms=0`
  (`child_race.go`: no one-second exit sleep per PDF/.docx read under `-race`).
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
- Every package has a `_test.go`; DB tests use `storetest.DSN(t)` and `t.Parallel()`.
- Images and scans go only to the `vision` role (`docs.UseVision`); never wire another role's
  model there (`TestTextModelNeverSentImages`, `TestServeNeverSendsImagesToTheLead`). Text a model
  transcribed is evidence of a lower class: keep its provenance and the `@transcript` cap.
- Engine: a lead step's ledger writes and bandit stats go through `e.write`/the step buffer and commit
  with the step (never write the ledger around it); every run change checks the lease (`lease_owner`)
  and status. The deliverable is rendered only by `finish`; findings that did not verify never reach
  a drafter, a subagent's ledger read, or the memo (G3). A new tool or subagent spec goes in
  `internal/engine/specs/` and must validate (`TestSpecsLoadAndValidate`); what Go needs from a built-in tool or subagent name
  (the lead's list, Go's callers and inputs, ledger writers, hooked, passes…) is its one row in `goContract`
  (`register.go`), never a new hand-kept list.
- No model calls in `go test`: only `model.Fake` or an `httptest` server. Model errors carry
  BigLaw's own fixed text and, of an error answer, only the provider's error type (or code) and message (the
  whole redacted by `redact`: the key and anything key-, token-, masked-key-, credential-, URL-, IP- or
  host-shaped, but never a dotted validation path like `messages.0.content`,
  then bounded: `providerError`); never the URL, host, key,
  a header or the rest of what the endpoint sent, so never `err.Error()` of a transport error; map them
  with `model.HTTPStatus`.
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
packages ≤ 12 (9 used; test-support packages do not count), direct deps ≤ 3, env vars ≤ 20 (19 used), UI runtime
deps ≤ 2 (2 used), UI source lines (`ui/src`) ≤ 3,200 (raised from 2,400 for Manage), untested packages 0, longest source file ≤ 2,000 lines.
