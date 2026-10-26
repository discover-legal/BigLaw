# BigLaw 2

> **Status: under construction on branch `v2`.** This README describes only what is on disk.
> Everything marked **planned** is specified in the v2 build plan and does not exist yet.
> BigLaw 1 (the DyTopo platform) remains on `main`; its frozen copy will be the branch
> `archive/v1-final` (a branch, because tag pushes are blocked in the build environment).

BigLaw 2 will be a legal AI workbench for small firms: memos, contract review and diligence
tables in which every factual statement cites a quote that the software has verified against the
matter's own documents, with a lawyer approving every deliverable. It is one Go binary plus one
PostgreSQL database. Licensed Apache-2.0 (`LICENSE`, `NOTICE`).

## What exists today (PR 1: skeleton, config, store, CI; PR 2: model client; PR 3a: documents; PR 3b: model roles, embeddings, hybrid search; PR 3c: vision transcription; PR 11, pulled forward by owner decision 13: the UI and its recorded engine; PR 4a: the agent runtime and the deterministic memo tools; PR 4b: the memo workflow, durable runs and the REST API; Manage and skills; PR 8: sign-in)

| Piece | Path | What it does |
|---|---|---|
| Command | `cmd/biglaw/main.go` | `biglaw serve [--allow-no-auth] [--admin-without-auth]` (migrates the database, then serves the API and the embedded UI and runs tasks) and `biglaw version [--config]` |
| Configuration | `cmd/biglaw/config.go` | Declares all 18 runtime variables (`BIGLAW_MODELS` is the role map); validates the ones this build reads (all problems at once); vendor breakers ported from v1 |
| Store | `internal/store/store.go` | Postgres via `database/sql` + `github.com/lib/pq`; DSN check that matches lib/pq's reading; transaction helper |
| Migrations | `internal/store/migrate.go`, `internal/store/migrations/NNNN_*.sql` | Embedded SQL files applied in order under an advisory lock; each file and its `schema_migrations` row in one transaction. `store.Open` first creates the `vector` extension (pgvector), which is database-wide (§ Database) |
| Files | `internal/store/files.go` | Binary files (uploads, generated `.docx`) stored in 1 MiB `bytea` chunks, streamed in and out, with size and SHA-256 checked on every read |
| Model client | `internal/model/` (`client.go`, `embed.go`, `errors.go`, `fake.go`, `pricing.go`, `prices.json`) | One OpenAI-compatible chat client with tool calls and image parts, an embeddings call (`Embed`) on the same transport, typed failures with their HTTP mapping, at most 3 attempts per call, cache-aware pricing of every call from a built-in table plus `BIGLAW_PRICES` (the host never makes a call free), watt-hour metering when `BIGLAW_LOCAL_WATTS` is set, and `model.Fake` for tests (§ Model client). `Embed`'s caller is documents (below); `Chat`'s is the agent loop (below), which the memo workflow drives. |
| Documents | `internal/docs/` (`ingest.go`, `child.go` + `child_linux.go`/`child_other.go`, `pageindex.go`, `chunk.go`, `search.go`, `embed.go`, `verify.go`, `http.go`, `vision.go` + `vision_prompts.txt`, `scanpdf.go`, `jobs.go`) | Ingest of UTF-8 text (a plain-text e-mail whose headers say quoted-printable is stored decoded, headers verbatim: `TestEmailBodyDecodedFromQuotedPrintable`), `.docx` and PDF text layers (PDFs and `.docx` files parsed in a bounded child process); images and JPEG-scanned PDF pages transcribed by the `vision` role, each page read twice and the readings compared, with provenance on every document, chunk and citation, and a leased job that transcribes what was stored before a vision role was set (§ Vision transcription); the byte-exact section tree; search chunks; Postgres full-text search, fused with pgvector similarity when an `embed` role is set (chunk embedding at ingest, a startup backfill); the page reader (outline, paged sections); the quote-verification ladder; the upload and download routes. Every read is scoped to the caller's document ids (§ Documents) |
| Agent runtime | `internal/agent/` (`tool.go`, `registry.go`, `schema.go`, `loop.go`, `budget.go`, `policy.go`, `bandit.go`) | The `Tool` interface and its single registry (definitions frozen at registration), JSON-schema validation of every tool input and output, one bounded loop for the lead and its subagents, the budget ledger, and the tool bandit (§ Agent runtime). The memo workflow drives it (below); a step log makes it resumable |
| Engine | `internal/engine/` (+ `specs/`, `packs/`; migrations `0005_runs.sql`, `0006_figure_reads.sql`, `0007_figure_reads_scope.sql`) | The memo workflow: the lead agent and its 12 tools, the ledger, the evidence subagents with Go-side quote verification, the figure reader, the sweep, the three-critic committee, the policy, `finish` (lint, coverage, render), the human gates, grounding collapse, and durable runs (step log, lease and heartbeat, resume) (§ Memo workflow) |
| API | `internal/api/` (+ `dist/`, the embedded UI) | The REST API (tasks, steps, SSE events, gates, cancel, cost, clients, Manage: `manage.go`) over the engine and the document routes, and the web UI built into the binary (§ Memo workflow, § Manage) |
| Manage | `internal/engine/specset.go`, `manage.go`, `manage_validate.go`, `diff.go`, `hooks.go`, `skills.go`, `custom.go`, `remote.go`, `hookrules.go` (+ `skills/`, `specs/use_skill.json`; migrations `0008_overrides.sql`, `0009_custom_tools.sql`, `0010_secret_origin.sql`), `internal/api/manage.go`, `ui/src/screens/Manage.tsx` | The catalogue of every agent, tool, pack, skill and hook a run uses, admins' versioned overrides in Postgres (strictly validated, compatibility-checked against what Go reads and writes), each task's snapshot of them, history, diffs, revert and a patch/zip export; skills and `use_skill`; admins' own subagents and MCP/HTTP tools with SSRF-checked calls and server-side secrets, a metered Test call, and configurable hooks around calls, findings and finish (§ Manage) |
| Memo tools | `internal/analysis/` (+ `belo.json`, `specs/*.json`), `internal/docs/register.go` (+ `specs/*.json`) | Deterministic tools over verified findings: `numbers` (exact `calc`, figure handles, key-figures table), `join_discrepancies`, the coverage checks `finish{op:"check"}` will run, the value normalisers and BELO-lite; and the `retrieve` / `verify_quote` adapters over documents (§ Agent runtime) |
| `.docx` reader | `internal/docs/ooxml/` | Opens a `.docx`'s text parts only (body, footnotes, endnotes; 128 MiB decompression cap) and reads its visible text in one linear XML pass. The writer, the XML escapers, the part-preserving re-zip and tracked-change parse/apply are planned (PR 3d) |
| Evaluation | `eval/` (`run.py`, `proofrun/`, `lab/`, `tests/`, `tasks/`, `e4/embeddings.json`) | The five synthetic proof-run matters (documents + answer keys), copied from `feat/proof-runs-multi-practice`, with their driver, scorer and the v1 comparison (`run.py`), the Harvey LAB driver (`lab/run.py`), and the Go gates E1/E2 that run them through the engine on a scripted model (§ Memo workflow). `eval/e4/embeddings.json` is E4's embeddings tape: today the **self-test** tape of a hash embedder, not a real model's (§ Documents, gate E4) |
| UI | `ui/` (React 19 + Vite + TypeScript strict; runtime deps `react`, `react-dom` only) | The seven screens (§ UI) behind one transport seam, `ui/src/api/transport.ts`: `http.ts` talks to the Go API on the same origin (tasks, steps, findings, gates, cost and documents are served since PR 4b; playbooks, time and profiles are PRs 6/8/9), `recorded.ts` replays a recorded engine in the browser with no network at all |
| Recorded demo | `ui/src/recorded/recording.json`, `eval/demo/record_test.go` | Three synthetic matters (13 documents plus one scanned exhibit, `ui/src/recorded/06-affidavit-of-service-scan.jpg`, drawn by `eval/demo/make_scan.py`) and seven recorded memo tasks (48 findings, 79 citations); documents, outlines, sections, chunks, the scan's transcript (v2's vision path with a scripted reader), run-log and offered search hits and every quote's verification computed by v2's `internal/docs`; claims from the answer keys; sample playbooks, time entries and profiles, and the configuration table read from `cmd/biglaw/config.go` (§ UI, provenance). `TestDemoRecording` fails when the committed recording no longer matches what v2's code computes |
| Sign-in | `internal/auth/` (`oidc.go`, `session.go`; migration `0011_sessions.sql`), `internal/api/signin.go`; `0012_task_submitter.sql` | OpenID Connect code flow with PKCE against one issuer, ID tokens verified with the standard library (discovery, JWKS, RS/ES signatures, claims), server-side sessions behind an opaque cookie, admins by `BIGLAW_ADMINS`, and the API behind a session (§ Sign-in) |
| Test helper | `internal/auth/authtest/` | A local OpenID provider (httptest: discovery, JWKS, PKCE-checking token endpoint, RSA and EC keys, key rotation) and the helpers to craft refused tokens and walk a browser through sign-in |
| Test helper | `internal/store/storetest/` | Gives each DB test its own schema; skips without a DSN, fails if `BIGLAW_REQUIRE_DB` is set |
| Test helper | `internal/engine/figtest/` | The figure reader's stand-in for tests (`figtest.Reader`, a `model.Fake` `Respond` hook): labels plain fixture texts so tests not about figure reading run unscripted (engine, API and eval tests) |
| Budgets | `scripts/loc.py` (+ `scripts/test_loc.py`) | Fails CI if a size budget is exceeded (below) |
| Build/deploy | `Makefile`, `Dockerfile`, `docker-compose.yml`, `render.yaml`, `.env.example` | `make ci` (and the owner-run `make e4-record` / `make e4-live`); `pgvector/pgvector:0.8.6-pg16` + biglaw under Compose; Render blueprint (inert until sign-in is configured, see § Exposure) |
| CI | `.github/workflows/ci.yml` | `make ci` against `pgvector/pgvector:0.8.6-pg14` and `:0.8.6-pg16` service containers (pinned by digest) with `BIGLAW_REQUIRE_DB=1`; staticcheck + govulncheck; image build + Compose smoke test |

HTTP surface today: the REST API of § Memo workflow, the embedded UI at `/`, and `GET /health`, which returns `{"status":"ok","version":"…","search":"hybrid"}` (200;
`"search":"lexical"` when `BIGLAW_MODELS` has no `embed` role) or
`{"status":"database unavailable",…}` (503; the cause is logged, not returned). Every other path
is 404/405. With sign-in configured (§ Sign-in) every route but `/health`, the UI's files and `/auth/*` needs a session; without it there is no authentication (§ Exposure).

**Planned** (not built): the `.docx` writer and tracked-change parse/apply (PR 3d); diligence (PR 5), contract review (PR 6),
MCP (PR 7), clients/conflicts and API tokens (the rest of PR 8; OIDC sign-in and sessions are built, § Sign-in), time and billing (PR 9), the
the full jurisdiction and practice pack catalogue (PR 12; four candidate packs ship with PR 4b), v1 import and docs (PR 13). The
discover.legal intake module is **not** part of v2 (owner decision; it moves to a separate repo).

## Architecture and patterns

This README is the contract. The design is described in `docs/`:
- [`docs/architecture.md`](docs/architecture.md): the packages and the import rules the tests
  enforce, a task's lifecycle from submission to the rendered memo, the budget, meter and journal,
  the spec set and snapshot, the figure pipeline, hooks, and custom tools. Each part links to the
  code that implements it.
- [`docs/patterns.md`](docs/patterns.md): the recurring patterns (typed tool specs, Go checking
  model output, fail closed or disclose, unclear gates, journaled steps, budget shares, content-hash
  caches, versioned overrides, admin-guarded writes, untrusted-text labelling) and the test
  patterns. For each: why it exists, and how to apply it.
- [`docs/glossary.md`](docs/glossary.md): the project's vocabulary and the ledger kinds. The G1–G11,
  P1–P9 and E0–E4 codes this README uses throughout are defined in its
  [Codes table](docs/glossary.md#codes).
- [`docs/benchmarks.md`](docs/benchmarks.md): the pinned benchmark suite (`make bench`), its results schema, `compare.py` and the baselines.
- [`docs/consolidation-proposals.md`](docs/consolidation-proposals.md): normalisation and
  consolidation opportunities for the owner to decide on, a proposed README restructure, and the
  places where the docs and the code disagree today.

## Configuration

Environment variables only. All 19 are declared in `cmd/biglaw/config.go` (20 until PR 3b, whose
`BIGLAW_MODELS` replaced the three role variables; PR 8 added `BIGLAW_ADMINS`); each is validated by
the PR that first reads it ("Read from"). `biglaw version --config` prints every value and exits
nonzero if what this build reads is invalid. `biglaw serve` warns about a variable that is set
but not read yet, and about any unknown `BIGLAW_*` name (likely a typo).

What may be printed follows one rule, used by `version --config` and by every startup error:
whatever does not parse into a shape known to be safe is not shown.
- Secrets (`BIGLAW_MODEL_KEY`, `BIGLAW_OIDC_CLIENT_SECRET`): set/unset only.
- `BIGLAW_DATABASE_URL`: only `host`, `port`, `user`, `dbname` and `sslmode` as lib/pq itself
  parses them (`PG*` defaults included), whether a password is set, and a count of other
  parameters. Nothing is copied from the DSN text, so no quoting in it can expose part of a
  password. A DSN that fails its check is not shown at all.
- URL variables (`BIGLAW_MODEL_URL`, `BIGLAW_PUBLIC_URL`, `BIGLAW_OIDC_ISSUER`): only
  `scheme://host[:port]`, never the path, query or fragment, and only for an absolute http(s) URL
  whose host is a plain ASCII name or IP and which has no `@` anywhere. Everything else (no scheme,
  a typo such as `https:/`, user info, an IPv6 zone id) prints as "(set, not shown …)". The `@`
  rule matters because an unescaped `/`, `?` or `#` in user info makes Go read part of the
  credential as the host and the rest as the path. The host is shown, so never put a key there.
  A Compose service name with `_` is a plain host.
- `BIGLAW_MODELS`: shown as set only if every entry parses as `role=<model id>` with a safe id
  (below); otherwise "(set, not shown …)". A value that does not parse is never shown; a
  well-formed id is shown and logged as written, so ids shaped like a key are refused (below).
- Anything else is shown as set (quoted, with escapes, if it holds a control or other
  non-printing character such as a bidi override or a zero-width space, so it cannot forge or
  disguise an output line), unless it contains `://` (then treated as a URL) or has the
  `user:password@` shape (then not shown).

| Variable | Default | Meaning | Read from |
|---|---|---|---|
| `BIGLAW_DATABASE_URL` | — (required) | Postgres DSN: `postgres://user:password@host:5432/db?sslmode=disable`, or libpq `key=value` form. The scheme must be lowercase, and reserved characters in the password percent-encoded (`/`→`%2F`, `?`→`%3F`, `#`→`%23`, `%`→`%25`, `@`→`%40`). An `@` after the host (in the database name or a query value such as `application_name=ops@corp`) is refused too, because it cannot be told apart from a broken password; write `%40`. Anything else is refused with a message that never shows the password. lib/pq defaults to `sslmode=require`. | PR 1 |
| `BIGLAW_ADDR` | `127.0.0.1:3101` | Listen address. Non-loopback needs `serve --allow-no-auth` (§ Exposure). | PR 1 |
| `BIGLAW_PUBLIC_URL` | `http://127.0.0.1:3101` | How browsers reach BigLaw, `https://host[:port]` with no path: the redirect URI is `<it>/auth/callback`, https makes the cookies `Secure` and `__Host-`, and its host is the one DNS name served under sign-in. Read only with sign-in, which refuses plain http except on loopback | PR 8 |
| `BIGLAW_MODEL_URL` | — | OpenAI-compatible API base, ending in its version (`http://localhost:11434/v1`); calls go to `<URL>/chat/completions` and `<URL>/embeddings`. http(s), a plain ASCII host with at least one letter or digit, a port up to 65535, no `@` anywhere (no credentials in the URL; write any other `@` as `%40`). Required when the key or a role is set. | PR 1 |
| `BIGLAW_MODEL_KEY` | — | API key for that URL, sent as `Authorization: Bearer`; no white space or control characters. `serve` warns if it would go over plain `http`. | PR 2 |
| `BIGLAW_MODELS` | — | Model id per role, as the endpoint names it: comma-separated `role=id`, roles `lead`, `specialist`, `critic`, `vision`, `embed`, each at most once (e.g. `lead=qwen2.5:14b,critic=glm-4.5-air,embed=nomic-embed-text`). Unset `specialist` or `critic` use the lead's; setting either without the lead is refused. `vision` and `embed` have **no** fallback: with no `embed` role search is lexical only (`/health` says `"search":"lexical"`); `vision` names the model images and scanned pages are sent to; without it they are stored as `needs_transcription` (503 `vision_not_configured`) and never sent to another role's model (§ Vision transcription). An unknown role, a repeated one, an empty id, an id over 200 bytes, or an id holding `://`, `user:password@`, white space, a control or invisible (format) character, `?` `=` `&`, or a key's shape (`sk-`, `sk_`, `AKIA`, `ASIA`, `AIza`, `xox?-`, `gh?_` prefixes, or 24 letters and digits in a row) is a startup error that does not echo the value. A wrong id fails at its first call with 404 → `model_not_configured`; with no lead, chat calls fail with 503 `model_not_configured`. | PR 3 |
| `BIGLAW_BUDGET_CALLS` | `400` | Model calls per task, lead turns included (at the first bench's $0.012–0.020 a call, 400 calls cost $4.80–8.00, so the default $5 usually binds first; every share holds on both) | PR 4 |
| `BIGLAW_BUDGET_USD` | `5` | Dollar cap per task | PR 4 |
| `BIGLAW_LOCAL_WATTS` | — | Power draw of the machine running the model; when set (above 0) every call records watts × duration as watt-hours, for reporting only (never a price). A number from 0 to 100000 | PR 2 |
| `BIGLAW_PRICES` | built-in | Model rates overriding the built-in table: the JSON itself (a value starting with `{`) or a path to a regular file of at most 1 MiB holding it (§ Model client) | PR 2 |
| `BIGLAW_COLLAPSE_THRESHOLD` / `_ACTION` | `0.5` / `strict` | Grounding-collapse policy | PR 4 |
| `BIGLAW_OIDC_ISSUER` | — | Turns sign-in on: the issuer's https URL exactly as its discovery document states it (`https://idp.example.com/realms/biglaw`), no query; plain http only on loopback (§ Sign-in) | PR 8 |
| `BIGLAW_OIDC_CLIENT_ID` / `_CLIENT_SECRET` | — | The OIDC client, set with the issuer (the secret is optional: a public client uses PKCE alone); no white space | PR 8 |
| `BIGLAW_ADMINS` | — | Sign-in's admins: comma-separated entries, each a verified email (has an `@`, case-insensitive, matched only when the ID token's `email_verified` is true) or an issuer subject (`sub`, exact). Everyone else who signs in is a member. Refused without sign-in | PR 8 |
| `BIGLAW_AI_RATE` | — | AI timekeeper hourly rate; unset = non-billable | PR 9 |
| `ALLOW_ANTHROPIC` | off | Opt in to a direct `anthropic.com` model endpoint (vendor breaker) | PR 1 |
| `ALLOW_AWS` | off | Opt in to a direct AWS/Bedrock model endpoint (vendor breaker) | PR 1 |

Tests use two more: `BIGLAW_TEST_DSN` and `BIGLAW_REQUIRE_DB` (turns a skipped DB test into a
failure). `TestEnvVarTablesAgree` fails if this table or `docker-compose.yml` lacks a variable
`config.go` declares, if `.env.example` or `render.yaml` sets other than its fixed list (the
model URL/key/models, the OIDC variables and `BIGLAW_ADMINS`; Render adds the database URL and address), or if any
of these files names a variable `config.go` does not declare.

**libpq variables.** lib/pq also reads the standard libpq `PG*` variables (`PGHOST`, `PGPORT`,
`PGUSER`, `PGPASSWORD`, `PGSSLMODE`, `PGPASSFILE`, …) as defaults for anything the DSN leaves
out, and refuses to start if one it does not support (such as `PGGSSENCMODE`) is set. They are
outside the 19-variable surface; `version --config` lists the names it sees, never their values.
Compose uses `PGPASSWORD` on purpose (§ Run it).

**Vendor breakers.** Ported from v1 `biglaw-go/internal/config/guard.go`: a `BIGLAW_MODEL_URL`
containing `anthropic.com` refuses to start unless `ALLOW_ANTHROPIC` is `1/true/yes/on`; one
containing `amazonaws.com` or `bedrock` refuses unless `ALLOW_AWS` is. Matching is
case-insensitive, on the whole URL, and a non-ASCII host is refused (write it in `xn--` form), so
look-alike dots such as `。` cannot slip past; Go's URL parser already refuses `%`-escapes such as
`%2E` in an ASCII host. A Claude model behind
a third-party gateway (OpenRouter, LiteLLM) is allowed.

## Model client

`internal/model` is the only code that talks to a model. It speaks the OpenAI chat-completions
protocol with native tool calls, so one client serves Ollama, LM Studio, vLLM, GoFetch,
OpenRouter, LiteLLM or a cloud vendor's compatible endpoint. No test calls a real model: every
caller is tested against `model.Fake` or an `httptest` server.

- **Requests.** A request names its `Agent` (`lead` or the subagent tool's name), its `Role`
  (`lead`, `specialist`, `critic`, `vision`; resolved through `model.Roles`, the one accessor,
  fed by `BIGLAW_MODELS`) or an explicit `Model`, the messages, the tools, and optionally
  `tool_choice`, `parallel_tool_calls`, `max_tokens` (sent as `max_completion_tokens` to
  `api.openai.com`, which refuses `max_tokens` for reasoning models) and `temperature`. The same
  request encodes to identical bytes every call (a tool's JSON Schema is compacted, deterministically)
  and tools stay in the order given, so a fixed tool list keeps the provider's prompt-cache prefix
  stable; `Strict` sends `"strict": true` (Go validation stays authoritative, since local endpoints
  ignore it). A message's `Content` is a string, `nil` (an assistant turn with only tool calls) or,
  for a user message, `[]model.Part`: text, and images as base64 `data:image/(png|jpeg|webp|gif)`
  URLs only, so the endpoint never fetches a remote URL on BigLaw's behalf. A malformed request (no
  agent, a repeated or malformed tool name, a schema that is not a JSON object, content of any other
  type, a misplaced tool-call id) is refused before sending.
- **Answers.** Text and/or typed tool calls (`id`, `name`, `arguments` as JSON object text; an
  object sent by a non-conforming server is accepted, a missing id becomes `call_<n>`), a
  `Truncated` flag for `finish_reason: length`, and usage. A tool call whose name is malformed
  (`[A-Za-z0-9_-]{1,64}`), whose id repeats another's, or whose arguments are not a UTF-8 JSON
  object fails the call (`ErrUnavailable`); `model.Fake` applies the same check. A 200 the provider
  billed but BigLaw refuses (no choice, nothing usable, a bad tool call) still reports its usage
  and cost on the error (`(*model.Error).Usage`), so the USD budget can count it. Input tokens exclude cache reads
  (`prompt_tokens_details.cached_tokens`) and cache writes (`cache_creation_input_tokens`, sent
  by Anthropic-style gateways); both are assumed to be counted inside `prompt_tokens`, as OpenAI
  does. Redirects are not followed, since prompts carry matter data.
- **Failures** are typed (README G6 of the v2 contract); `model.HTTPStatus` is the one status
  mapping (the API sets `errorType` and the message from the kind, PR 4b):

  | Kind | When | HTTP | `errorType` |
  |---|---|---:|---|
  | `ErrNotConfigured` | no URL, no model for the role, 401/403/404 (a missing or refused key, a wrong base URL or model id: one hint names all three) | 503 | `model_not_configured` |
  | `ErrRateLimited` | 429 after the last attempt (`(*model.Error).RetryAfter` is the provider's `Retry-After`, at most 24 h) | 503 | `model_rate_limited` |
  | `ErrUnavailable` | 5xx after the last attempt, another 4xx (400/413/422 included, see Limitations), a redirect (never followed), an unreachable endpoint, an answer over 32 MiB or not in the chat-completions shape, a malformed tool call | 502 | `model_unavailable` |
  | `ErrTimeout` | the caller's deadline, or no answer within the 5-minute client timeout | 504 | `model_timeout` |
  | `ErrNoContent` | no choice, or a choice with neither text nor tool calls | 422 | `no_content` |
  | (cancelled) | the caller cancelled | 499 | — |

  Anything else is a 500. Error text holds only BigLaw's own fixed words and the status code:
  never the endpoint URL, host or port, the key, or anything the endpoint sent. A transport failure
  is one of "connection refused", "DNS lookup failed", a timeout, or "the connection failed or the
  answer is not valid HTTP" (Go's own error text can quote the bytes a server sent).
  Role ids (`BIGLAW_MODELS`) reach the logs as written, so startup refuses an id that holds `://`,
  `user:password@`, white space, a control or invisible character (a bidi override could disguise a
  log line), `?`, `=` or `&` (a query string such as `gw.example.com/v1?key=…` could carry a key),
  or has a key's shape (vendor prefixes, 24 letters and digits in a row; a heuristic: a key in
  another shape is shown), and never echoes it (`TestRoleIDRefusesQueryCharacters`);
  so `name:tag@quant` is refused too, while LM Studio's `name@quant`, Ollama's `name:tag`,
  OpenRouter's `vendor/model` and Bedrock-style `us.anthropic.…-v1:0` are fine.
- **Retries.** At most 3 attempts per call, only after 429, 500, 502, 503, 504 or 529. The wait
  is the provider's `Retry-After` in seconds, or 1 s then 2 s plus up to 25% jitter. A
  `Retry-After` over 30 s (however large: values past int64 too), or a wait that would outlast
  the caller's deadline, ends the call at once with the last error. Network errors are not retried (the request may already have been
  billed).
- **Pricing.** Every call is priced from the price table, whatever the endpoint's host: a local
  endpoint is not free and open weights are no reason for $0 either (owner decision; v1 priced
  local calls at $0). `internal/model/prices.json` is the built-in table, in the `BIGLAW_PRICES`
  format and read by the same parser: an object mapping a model id (case-insensitive; a trailing
  `*` makes it a prefix) to `{"in": USD, "out": USD}` per million tokens, both required and from 0
  to 10000, or to `null` for a model that is known but has no price. Repeated ids
  (case-insensitive), unknown fields inside an entry and trailing data are refused at startup.
  Errors never quote the value, an id, or the file's contents. A model id resolves in the
  operator's `BIGLAW_PRICES` table first, then the built-in one; in each, an exact id beats the
  longest prefix, and `vendor/model` (OpenRouter style) is also tried as `model`; a key without a
  `/` matches only that stripped `model`, so `deepseek/deepseek-r1` takes the `deepseek-r1*` row,
  not `deepseek*`. So `BIGLAW_PRICES='{"deepseek*": {"in": 0.3, "out": 1.2}}'` reprices the whole
  family, and `{"claude-haiku-4-5": null}` un-prices one model. (Under Compose a file path must
  exist inside the container; the inline JSON form is simpler.) The built-in table has 86
  entries: 79 of v1's 80 rows from `cost/cost.go` (v1's list prices as of early 2026, unverified
  here; v1's `{0, 0}` placeholders are `null`; its `llama*` placeholder is dropped, which leaves
  `llama*` ids unlisted: the same unknown cost), plus OpenAI's `o1*`, `o1-mini*`, `o1-pro*`, `o3-pro*`,
  `o3-deep-research*`, `o4-mini-deep-research*` and `chatgpt*` (list prices from memory,
  unverified). Open-weights families keep their hosted rates: `qwen2.5:14b` on Ollama is charged
  at the `qwen*` rate, `mistral-nemo` at `mistral*`'s. Cost = (input × in + output × out + cache
  writes × in × 1.25 + cache reads × in × 0.10) / 10⁶. A model with no row or a `null` row has an
  **unknown** cost (nil), never $0; the first call to each such model logs one warning. **A model
  you really run for free** is set to zero explicitly:
  `BIGLAW_PRICES='{"qwen2.5:14b": {"in": 0, "out": 0}}'` (or `{"qwen*": {"in": 0, "out": 0}}` for a
  family). `serve` logs each role's price once at startup (`model pricing role=lead model=…
  price="$3 in / $15 out per 1M tokens"`, or `price="unpriced: …"`).
- **Embeddings.** `Embed` sends up to 256 non-blank UTF-8 texts (each at most 16 KiB) in one
  OpenAI-compatible `POST <URL>/embeddings` (`"encoding_format": "float"`, never `dimensions`:
  vectors come at the model's own length) on the `embed` role's model, with the same retries,
  redirect rule and fixed-text typed failures as a chat call. It returns one vector per input in
  input order (the answer's `index` decides, not its order). An answer without exactly one vector
  per input, all of one length (at most 16,000, pgvector's limit) with a norm between 1e-6 and 1e6
  (zero, subnormal or huge values make pgvector's distances NaN), is `ErrUnavailable`; no vectors
  is `ErrNoContent`; either way a billed answer keeps its usage on the error. `model.Fake` applies
  the same vector checks. **It is priced like every call**: `prompt_tokens` (capped at the
  request's size in bytes, since a token is at least a byte) at the model's input rate from the
  same table (`text-embedding-3-small` $0.02, `-3-large` $0.13, `-ada-002` $0.10 per million
  tokens; there is no `text-embedding*` wildcard, so another vendor's model of that name is
  unpriced; `nomic-embed-text` has no row, so its cost is unknown, not $0, unless `BIGLAW_PRICES`
  sets it). An answer with no `usage` has an unknown cost (nil), never $0. Watt-hours are added
  when `BIGLAW_LOCAL_WATTS` is set (`TestEmbeddingsPriced`, `TestEmbedTypedErrors`). No `embed` role is 503
  `model_not_configured`.
- **Watt-hours.** With `BIGLAW_LOCAL_WATTS` set, every call (successful or billed-but-refused)
  records `BIGLAW_LOCAL_WATTS` × its duration (all attempts) as watt-hours, beside its price. They
  report the energy of your own hardware; they never change the cost. Unset, none are recorded.
- **`model.Fake`** answers from scripts keyed by agent name, in order. A call for an agent with
  no script, or past the end of its script, is an error, never a default reply; an empty reply is
  `ErrNoContent` and a malformed tool call `ErrUnavailable`, as from the real client; tool-call ids
  it fills in are `<agent>-<call>-<index>` (the agent's own call count), so runs are deterministic.
  It does not resolve roles: a request's `Role` only names its reply's model (`fake:<role>`).
  `Fake.Embed` answers from a `Vectors(text)` function (unset: an error, never a default vector),
  checks requests as the client does and records them (`EmbedCalls`). `Fake.Route`, when set, names
  the script a request is answered from (by what it holds), so calls that arrive in any order (a
  parallel sweep's visits) get their own replies (`TestFakeRouteAnswersByContent`).

## Exposure

Without sign-in there is no authentication, so `biglaw serve` refuses to listen on a non-loopback
`BIGLAW_ADDR` (anything but `127.0.0.0/8`, `::1` or `localhost`) unless sign-in is configured
(§ Sign-in) or it is started as `biglaw serve --allow-no-auth`. That flag means: something outside
BigLaw limits who can reach the port. **With sign-in configured, `--allow-no-auth` has no effect**
(serve logs that it is ignored, so Compose's command works unchanged) and **`--admin-without-auth`
is refused** at startup (admins are `BIGLAW_ADMINS`; a flag that made everyone an admin cannot
coexist with sign-in). Without sign-in both behave exactly as before. The deploy files follow the
rule, and `TestDeployFilesFollowExposureRule` keeps them that way:
- **Compose** listens on `0.0.0.0` inside the container, publishes the port on the host's
  `127.0.0.1` only, and passes `--allow-no-auth`. Do not publish it on `0.0.0.0` or a LAN address.
  With sign-in, put a TLS reverse proxy on the host in front of `127.0.0.1:3101` (§ Sign-in).
  The test reads every published port and `network_mode` of every service, in every profile,
  from what `docker compose --profile '*' config --no-interpolate` resolves at the repo root (as
  `docker compose up` would, override files included). Each port must name a loopback IP; an
  interpolated host (`${BIND:-127.0.0.1}:…`) is refused because the environment could change it.
  Any Compose file besides `docker-compose.yml` at the root (`compose.yaml`,
  `docker-compose.override.yml`, …) fails the test. It needs `docker`: without it the test is
  skipped, and fails where `CI` is set, like `make compose-check`.
  `TestComposeExposureCheckCatchesBypasses` runs it on the spellings the review committee used to
  get past earlier checks.
- **The image's default command** is plain `serve`, so `docker run -p …` or any platform that
  exposes the container refuses to start unless sign-in is configured.
- **Render** is public by nature, so `render.yaml` is inert until its OIDC variables are set in
  the dashboard: until then the service fails its start on purpose. Set `BIGLAW_PUBLIC_URL` to the
  service's https URL with them: with it left at its loopback default the service starts, but no
  sign-in can complete (it fails closed).
- **Manage under `--allow-no-auth` is read-only** (without sign-in). Reaching the port is not proof of being an admin,
  and an edit in Manage changes every later task of every matter, so with `--allow-no-auth` nobody may
  edit there (403 `forbidden`; the screen says why). An operator who accepts that anyone who can reach
  the server may change its agents, tools, packs and skills passes `serve --allow-no-auth
  --admin-without-auth` (serve logs a warning; the screen shows a banner). Compose keeps
  `["serve", "--allow-no-auth"]`: to edit in Manage under Compose, configure sign-in, or add
  `"--admin-without-auth"` to the service's `command` in a local copy, knowing what it allows. A loopback server started without
  either flag (local development) is an admin.
- **Gates without sign-in have no identity.** Anyone who can reach the API can approve or reject a
  gate: the deliverable's, a finding's, or a configurable hook's approval, and the decision records
  the local user. With sign-in every decision records the signed-in user (`decidedBy`: sub, name,
  email). An approve rule cannot yet require a role ("a partner approves" is a pause for any
  signed-in person, member or admin).

Two browser guards hold in every mode, because a page the lawyer visits can reach a loopback port.
Every `POST` (documents, tasks, cancel, gate decisions, Manage, logout) from another site is refused 403
`cross_origin` (`Origin` other than the request's own host, or `Sec-Fetch-Site` other than
`same-origin`/`none`; `text/plain` and `multipart/form-data` need no CORS preflight), so no site
can spend the budget or decide a human gate (`TestWritesRefuseCrossOrigin`); under sign-in the same
check runs on every unsafe method (`DELETE` too) before the session is read, so the session cookie
never carries a cross-site write (`TestSignInGuardsTheAPI`). Every request but
`GET /health` addressed to a DNS name other than `localhost` (and, under sign-in, `BIGLAW_PUBLIC_URL`'s
host) is refused 403 `bad_host` (DNS rebinding would otherwise make another site BigLaw's own
origin): address BigLaw by IP, `localhost` or its public name (`TestRefusesRebindingHosts`). The UI
cannot be framed (`X-Frame-Options: DENY`, `frame-ancestors 'none'`: `TestUIRefusesFraming`).

## Sign-in (`internal/auth`; PR 8)

Setting `BIGLAW_OIDC_ISSUER` and `BIGLAW_OIDC_CLIENT_ID` (and `BIGLAW_OIDC_CLIENT_SECRET` for a
confidential client) turns on sign-in with OpenID Connect: the authorization code flow with PKCE
(S256), against any standards-compliant issuer (a self-hosted identity server, a hosted identity
service, a corporate directory). It is built on the Go standard library alone (no OIDC or JOSE library).
- **Flow.** `GET /auth/login[?next=/#/…]` reads the issuer's `/.well-known/openid-configuration`
  (once; its `issuer` must equal `BIGLAW_OIDC_ISSUER` exactly, its endpoints be https, and S256 be
  among its PKCE methods when it lists any), seals the sign-in in flight (a random `state`, a random
  `nonce` and PKCE verifier, where to return, and when it began) into the login cookie with AES-GCM
  (`__Host-biglaw_login` over https, 10 minutes, the age checked inside the sealed payload too), and
  redirects to the issuer with scope `openid email profile`. **Nothing is stored on the server** until
  a sign-in completes, so anonymous sign-ins cost no row and no number of them can refuse anyone
  else's (`TestUnfinishedSignInsNeverLockAnyoneOut`). The sealing key is made at process start:
  a restart or a deploy ends the sign-ins in flight (their user clicks Sign in again), and several
  replicas behind one name need sticky routing (a callback must reach the process that sealed its
  cookie; sessions themselves are in Postgres and work on any replica).
  `GET /auth/callback` clears the login cookie at once (single use in that browser), opens it (a
  cookie this process did not seal, or one tampered with, is refused), requires the callback's
  `state` to be the sealed one (a callback is honoured only in the browser that started it: login
  CSRF), and then trades the code with the verifier and the client's credentials (`client_secret_basic`, or
  `_post` when the issuer offers only that) and verifies the ID token: signature by the issuer's
  JWKS (RS256, RS384, RS512, ES256, ES384; `none`, `HS*` and any other alg refused before a key is
  looked up, a key used only for its own type and curve and its JWKS `alg`, `oct` keys never
  loaded, RSA keys of at least 2048 bits), `iss`, `aud` (with `azp`: present, it must be the client;
  several audiences need it), `exp`, `iat` and `nbf` (when present) within 60 s of skew, the
  sign-in's `nonce`, a `sub`. **Claims are decoded strictly**, by their exact names: a token that
  repeats a claim, holds a claim named like one BigLaw reads in other case (`Email`,
  `EMAIL_VERIFIED`, `SUB`: Go's JSON decoder would otherwise let it override the real one) or one of
  the wrong JSON type is refused; `email_verified` counts only as the JSON boolean `true`.
  The JWKS is cached and fetched again for a `kid` it lacks (key rotation), at most once a minute
  (a failed fetch too). Discovery and JWKS fetches are shared by every caller waiting for them
  (one request at a time per kind, 10 s, no lock held across it; a failed discovery is answered
  from memory for 5 s), so a slow or down issuer neither queues sign-ins nor is flooded.
  `next` is a path on this server only, at most 512 bytes (longer: `/`). Redirects from the
  issuer's endpoints are not followed and answers are capped at 1 MiB.
- **Replay without server state.** A replayed callback needs that browser's login cookie as well as
  the URL, and still fails at the issuer: the code is single use and bound to the PKCE verifier,
  and the ID token to the sealed nonce (`TestCallbackRefusals` replays one). A refused sign-in is
  logged at most once a second, so a flood of bad callbacks cannot flood the log.
- **Sessions** (migration `0011_sessions.sql`). A sign-in creates a server-side session row; the
  cookie holds only its random 256-bit id (opaque, `HttpOnly`, `SameSite=Lax`, `Path=/`; `Secure`
  and named `__Host-biglaw_session` when `BIGLAW_PUBLIC_URL` is https, `biglaw_session` on loopback
  http) and the table only that id's SHA-256. A session ends after 8 hours without a request or 7
  days after sign-in, whichever is first; `POST /auth/logout` deletes the row, so a copied cookie
  dies with it. Every sign-in issues a new id and deletes the browser's previous session (no
  fixation). `GET /me` is the session's user `{id: sub, name, email, admin, authEnabled: true}`.
  **The issuer is not asked again during a session**: disabling an account there ends its BigLaw
  sessions only at their idle or absolute end. To end them now, an admin calls
  `POST /manage/sessions/revoke {"user": "<sub or email>"}` (every session of that sub, or of that
  email case-insensitively; `{"revoked": n}`); an open event stream (`/tasks/{id}/events`) checks its
  session every 10 polls (about 5 s) and ends with `event: signedOut` once it is gone (logout,
  revoke or expiry).
- **Every API route needs a session**: without one it is 401 `unauthenticated`; only `GET /health`,
  the UI's own files (so it can show its sign-in page) and the three sign-in routes are open, by
  exact pattern (`api.public`: a new route, under `/auth/` or not, is behind the session unless it
  is added there). `serve` puts whatever it mounts behind sign-in when sign-in is configured. A
  database failure reading a session is 503, never a sign-out (`TestEveryRouteNeedsASession` sends
  every route and spelling raw).
- **Roles.** **Admins** are the accounts `BIGLAW_ADMINS` names (a verified email, or a `sub`); they
  edit Manage, set secrets, import and enable custom tools and webhooks, press Test and revoke
  sessions. Everyone else the issuer signs in to this client is a **member**: they submit tasks,
  read everything, and decide gates (lawyer review); every Manage write is 403 `forbidden` for them.
  An email entry matches only the address the issuer signed, as it signed it: `email_verified` the
  JSON boolean `true`, the address plain printable ASCII (one with a space, a control or format
  character such as a soft hyphen or zero-width space, or any non-ASCII character never matches, and
  `BIGLAW_ADMINS` refuses such an entry), compared byte for byte after ASCII-only lowercasing (no
  Unicode folding: KELVIN SIGN is not `k`). An unverified claim of an admin's address is a member,
  and its address is not shown. Admin is decided on each request from the session's identity, so a
  restart with a shorter list demotes at once. Chosen over a configurable claim because it needs no
  issuer-side group or role mapping and works the same with every issuer. **Prefer `sub` entries**
  where the issuer lets users, or an upstream provider it brokers, set an email it then marks
  verified: a `sub` is the issuer's own identifier for the account, an email only as trustworthy as
  the issuer's verification of it.
- **Turned on with it:** Manage is writable by admins, and secrets, custom MCP/HTTP tools and
  webhooks work for them under Compose and in containers (`RemotePolicy` is on; the admin guard keeps
  members out). Change-log entries, secrets' "set by", an accepted tool's `acceptedBy`, Test calls
  and gate decisions record the user (`Name <email>`, or `Name (sub)`), never "Local user", and
  every task records who submitted it (`submittedBy`, migration `0012_task_submitter.sql`; the Task
  screen shows the name, with the email on hover to admins). **A member never sees another person's
  email**: tasks' `submittedBy` and gates' `decidedBy`, the change log's and history's authors and
  secrets' setters are names only in every response a member reads, and an accepted tool's file
  (`acceptedBy`, shown in Manage and the export) and a Test call's task record the name alone
  (`TestMembersNeverSeeAnEmail`); admins see the emails. **A display name is never an email**: the
  issuer's `name`, else `preferred_username`, unless either holds an `@`, else the email's local
  part, else "Admin" or "Member" (`＠`, `﹫` and `%40` count as `@`); a stored email-shaped name is
  shown to members as its local part, and members get no `id` (sub) in `submittedBy` or `decidedBy`,
  since some issuers use the email as the subject. Display names are the
  issuer's `name` claim, which a user may be able to edit there: the stored sub and email are what
  identify them.
- **Who may sign in is the issuer's decision**: BigLaw admits every account the issuer
  authenticates for this client id as a member. Use an issuer (a realm, an application) whose
  sign-in is limited to your firm; a consumer or multi-tenant issuer would let anyone in.
- **Never logged or returned:** tokens, codes, PKCE verifiers, state, cookies and the client secret
  (`TestSignInNeverLogsSecrets`, `TestServeWithSignIn`); an issuer's error is shown only as its OAuth
  error code. Sign-ins are logged with sub, email and whether they are an admin.

**Worked example** (Compose, a generic issuer at `https://idp.example.com/realms/biglaw`, BigLaw at
`https://biglaw.example.com`):
1. At the issuer, create a confidential OpenID Connect client `biglaw` with the standard (code)
   flow, PKCE S256 if it can require it, and the redirect URI
   `https://biglaw.example.com/auth/callback`; note its secret. Limit who may sign in to it.
2. In `.env`: `BIGLAW_PUBLIC_URL=https://biglaw.example.com`,
   `BIGLAW_OIDC_ISSUER=https://idp.example.com/realms/biglaw`, `BIGLAW_OIDC_CLIENT_ID=biglaw`,
   `BIGLAW_OIDC_CLIENT_SECRET=…`, `BIGLAW_ADMINS=ada@example.com`.
3. Run a TLS reverse proxy on the host for `biglaw.example.com` that forwards to `127.0.0.1:3101`
   and keeps the `Host` header (BigLaw compares `Origin` with it, and answers only that name).
4. `docker compose up -d`; serve logs `sign-in on` once it has read the issuer's discovery document.
   Open `https://biglaw.example.com`: the UI shows a sign-in page, the header shows the user and
   Sign out.

Locally, an issuer on loopback (`BIGLAW_OIDC_ISSUER=http://127.0.0.1:8080/realms/dev`) and the
default `BIGLAW_PUBLIC_URL` work over plain http, with cookies that are not `Secure` and have no
`__Host-` prefix. Browsers do not separate cookies by port, so **every other service on
`localhost:*` / `127.0.0.1:*` receives BigLaw's loopback session cookie** and can set its login
cookie: use loopback sign-in for development only, never with other untrusted local services, and
https for anything real.

## Run it

```bash
test -e .env || sed "s/^POSTGRES_PASSWORD=\$/POSTGRES_PASSWORD=$(openssl rand -hex 24)/" .env.example > .env
docker compose up -d            # Postgres 16 with pgvector + biglaw; host port 127.0.0.1:3101 only
curl -s http://127.0.0.1:3101/health
```

Compose gives biglaw the database password as `PGPASSWORD` rather than inside
`BIGLAW_DATABASE_URL`, so it needs no percent-encoding. The first line above writes a random hex
password into `.env` (Compose does not run `$(…)` written inside `.env`), and only when there is no
`.env` yet: an existing one keeps its settings and the password its `pgdata` volume was created with.
Compose's own `.env` parsing does constrain it, though: unquoted, a value is cut at ` #` and `$` is expanded (`a$b` fails, and
`abc #def` becomes `abc`, on both the Postgres and the biglaw side). Use a hex password, or
single-quote your own (`POSTGRES_PASSWORD='…'`) without a `'` in it.

Without Docker (Go 1.26+, any supported Postgres ≥ 14 **with the pgvector extension installed**,
e.g. the `postgresql-16-pgvector` package, or pgvector built from source; § Database):

```bash
createdb biglaw
make build                      # static binary: CGO_ENABLED=0, version stamped (embeds ui/dist if built: make ui build)
BIGLAW_DATABASE_URL='postgres:///biglaw?sslmode=disable' ./biglaw serve
# hybrid search: an embedding model on any OpenAI-compatible endpoint, e.g. Ollama's
BIGLAW_MODEL_URL=http://localhost:11434/v1 BIGLAW_MODELS=embed=nomic-embed-text \
  BIGLAW_DATABASE_URL='postgres:///biglaw?sslmode=disable' ./biglaw serve
```

`postgres:///biglaw` uses the libpq defaults (a local socket or `PGHOST`, your OS user or
`PGUSER`). The server opens the database and runs migrations before it starts listening, so a
connection to the port is answered as soon as it is accepted.

## Database: migrations, backups, rollbacks

- **pgvector.** Migration 0003 adds a `vector` column to the chunks, so the database needs the
  pgvector extension. `store.Open` runs `CREATE EXTENSION IF NOT EXISTS vector` before migrating
  (under an advisory lock: concurrent starts would collide). Extensions are database-wide, so this
  is outside the numbered per-schema migrations. Creating it needs a superuser (pgvector is not a
  trusted extension); a role that may not, in a database without it, gets a startup error naming
  the fix: "the pgvector extension (vector) is missing and could not be created: use a Postgres
  with pgvector (the pgvector/pgvector images) or have a superuser run CREATE EXTENSION vector in
  this database". An extension that already exists needs no privilege (`TestOpenNeedsVector`).
  Compose's `pgvector/pgvector` image and CI's carry it; whether Render's managed Postgres offers
  `vector` is not verified here (`render.yaml` is inert until sign-in is set). The `vector` type
  must be on the connection's `search_path` (it is when the extension sits in `public`, the
  default).
- Migrations run on every start. A process that finds another holding the migration lock logs
  "waiting for the migration lock" and waits until that one finishes or it is stopped (a signal
  then gives a clear "stopped … waiting for the migration lock" error). Connecting is bounded
  at 30 s; waiting for the lock is not, so a long migration in one replica does not crash-loop
  the others.
- Each migration file runs in one transaction together with its `schema_migrations` row. A file
  may not `COMMIT`/`ROLLBACK` itself (the runner detects it and refuses), and therefore cannot
  use statements that refuse to run in a transaction, such as `CREATE INDEX CONCURRENTLY`.
- **Migrations are forward-only.** The server refuses to start if the database records a
  migration this binary does not have, or one whose file changed after it was applied. Rolling
  back the image (an older tag, Render's rollback button) after a release that added a migration
  therefore fails at start with "database has migration … which this binary does not know". To
  go back, restore the backup taken before the upgrade, or deploy a forward fix.
- **Pre-release:** until v2's first release, `0001_init.sql` may still change. A database created
  by an earlier pre-release build then refuses to start ("migration 0001 was changed after it
  was applied"); recreate it (`docker compose down -v`, or `dropdb`/`createdb`). From the first
  release on, applied migrations are never edited.
- **Back up with `pg_dump`** before every upgrade (all state, files included, is in Postgres),
  for example `docker compose exec postgres pg_dump -U biglaw -Fc biglaw > biglaw.dump`; restore
  with `pg_restore`.

## Documents (`internal/docs`)

**Scope (G1).** Every read takes a scope, the task's document ids: `Search`, `Outline`, `Section`,
`VerifyQuote`, `Get`, `File` and `Resolve` treat a document outside it exactly as one that does not
exist, and a file is reachable only through a document in scope (`TestRetrieveNeverLeavesMatter`,
`TestFileNotReachableOutsideMatter`). Ids compare case-insensitively, as Postgres compares uuids.

**Ingest.** The type is detected from the content, never from the name or the declared type:
`%PDF-` is a PDF, a ZIP with `word/document.xml` is a `.docx`, valid UTF-8 without NUL is text.
a PNG, JPEG, GIF or WebP image is transcribed (§ Vision transcription; a TIFF is 422
`scan_encoding_unsupported`). Anything else is refused with 415 `unsupported_type`;
a damaged, encrypted or over-2,000-page PDF and a damaged `.docx` are 422 `unreadable`. A refused
upload leaves no row behind (`TestIngestRefusalLeavesNothing`). A document that has nothing
readable (a PDF with neither text nor JPEG scans, a `.docx` holding only images, an empty file) is stored with
`noText: true` and a note saying so, never as a silently empty document
(`TestScannedPDFFlaggedNoText`); a PDF with some blank pages keeps its text and its note counts
every page that was not read (listing the first ten). PDF pages are joined with a form feed.

PDFs and `.docx` files are parsed in a **child process** of the same binary (`biglaw` re-executed
with a private argument, no environment, so it never holds the server's secrets): the PDF parser
recurses on nested objects, and a few MB of nesting is a fatal stack overflow that would
otherwise kill the server (`TestHostilePDFContained`). The child is killed when the request ends
(its ingest slot is free at once) or after 2 minutes, and it bounds itself even if the server
dies first (crash, `kill -9`, OOM kill): it exits at its own deadline and within 250 ms of its
parent's death, and on Linux the kernel kills it with its parent (`Pdeathsig`)
(`TestChildBoundsItself`, `TestChildDiesWithParent`). Its stack is 64 MiB and, on Linux, its data
segment is a **hard** 1 GiB cap (`RLIMIT_DATA`): a decompression bomb dies at the cap and the
upload is 422 `unreadable` (measured: a 512 MiB text bomb under a 256 MiB test cap died at
142–221 MiB peak RSS across runs). A document's text is capped at **16 MiB** (`maxText`, about
8,000 pages; the file itself may be up to 64 MiB): more, from a PDF, a `.docx` or an uploaded
text file, is 422 `unreadable` ("its text is larger than …"). The child sends its text as a
length-prefixed raw block after a one-line JSON header, which the server reads through hard bounds
(a 64 KiB header, exactly the stated length up to the cap, valid UTF-8, nothing after:
`TestReadChildBounded`), into one allocation. At the cap a child peaks at about 60 MiB RSS on
escape-heavy text, under half its memory cap, every run (`TestChildNearTextCap`), so acceptance
near the cap does not depend on garbage-collector timing. The child is the running image
(`/proc/self/exe` on Linux), so replacing or deleting the binary on disk while serving does not
change the reader (`TestChildRunsTheRunningImage`); a reader that cannot start is a 500 that says
so, not "damaged". Two run at once (`ingestSlots`); an upload waits at most 10 minutes for a slot,
then is 503 `busy` and its file is removed (`TestIngestSlotWaitBounded`). A binary whose `main`
(or test package's `TestMain`) forgot `docs.MaybeRunChild()` gets a clear error instead of
re-running itself (`TestForgottenMaybeRunChildIsAnError`).

`.docx` text is read in one linear pass of an XML decoder (a 9 KB file of 160,000 runs took
48 s in v1's reader; 1,000,000 runs now take about a second: `TestTextLinearAndBounded`), with
elements matched by namespace, whatever their prefix: `<w:tab/>` is a tab, `<w:br/>`/`<w:cr/>` a
line break, `<w:noBreakHyphen/>` is U+2011 (as in `FS‑26‑01234`), a `<w:sym/>` is its character
unless it is a private-use Symbol-font code (dropped), character references are decoded, comments ignored, hidden text (`<w:vanish/>`),
deleted text and markup-compatibility fallbacks left out, nesting capped at 256 elements, and
footnotes and endnotes appended after the body as `[id] text` under a `Footnotes`/`Endnotes`
line (`TestTextFidelity`). Entities are never expanded. Text hidden only by formatting (white on
white, a 1-point font) is read like any other text.

All extracted text, uploaded text included, has invalid UTF-8 replaced and control characters
other than tab, newline, CR and form feed turned into spaces, so it can be stored.

**Section tree and chunks.** v1's page index, rewritten: articles, sections (`Section 1`, `§ 1`),
decimal clauses (`1`, `1.1`; the first number at most three digits, so a year is not a heading,
and a number followed by a lower-case word, such as `100 dollars were paid` or `1.5 million`, is
body text, and so is a bare number before a street word, `12 Elm Street, …`), `(a)` and `(i)` items,
schedules/exhibits/annexes, recitals, and Markdown `#` or bold headings. An article, section, `§` or
schedule line whose title runs on from its number in lower case or with punctuation (`Article 4 of the
Trust, …`, `Section 9, …`, a PDF-wrapped cross-reference) is body text; a separator (`—`, `:`, `.`)
before the title keeps it a heading (`TestWrappedLinesAreNotHeadings`). The tree is bounded so storage stays linear in the input (each chunk carries its
section path): at most 10,000 sections per document (later headings stay body text of the last
one, and the document's `notes` say so, so an outline never looks complete when it is not),
titles cut at 120 bytes, numbers of at most 24 bytes and five decimal levels, so a path has at
most six levels; titles lose bidi and other invisible or control characters. **Bound:** every
chunk column together (path, body, search text, column context) is at most 3× the input for each
hostile shape `TestSectionTreeBounded` builds except one (measured 1.0×–2.9×: a long heading over
40,000 items 2.2×, 2,500 rows of 100 one-digit cells under 100 named columns 2.9×). The bound is
**additive, not a ratio**: about 4× the input (a body, its search text at most 1.5× plus the
column list, a path and a context per chunk of at least 1,024 bytes) **plus** up to 1,165 bytes
per section (a path of six levels of number, 120-byte title and separator, 906 bytes, and a
context of 259), so at most about 11.7 MB more per document at the 10,000-section cap, because a
section's last chunk can be short. Small documents of many short, deeply nested sections show it:
9,990 sections of 120-byte titles six levels deep (1.3 MB in) store 9.0 MB, **6.9×** (3.7 MB, 373
bytes per section, over 4×), and the ratio falls as the document grows past the section cap
(`TestSectionTreeBounded` "deep titles", which asserts the additive bound). v1's tree copied the full path into every chunk (3,284×
and 168 s for the long heading); round 1's table rows copied the whole header into every row
(a 120 KB, 20,000-column table: 1.2 GB and 20 s; 9 ms and 260 KB now). `TestHeadingHeuristics`. Joining every section's own text gives back the document byte for byte, so every
section and chunk is a verbatim slice (`TestReconstruction`, `TestChunksCoverEveryWordOnce`).
Chunks hold at most 400 tokens (3.5 characters per token), cut at a sentence end, else at white
space. A tab-separated table is chunked by whole rows, grouped until a chunk holds at least
1,024 bytes (a row longer than the token cap is split like prose); the first row (after the
start or a `#`/`## Sheet:` line that is not itself a row, so a `#` row-number column stays a
column) is the header, however long (only its first 1,401 cells are kept as names, the most a
row under the token cap can pair). Each data cell is indexed as
`Column: cell` while the names added stay within half the row's length (bare cells after that),
and a chunk's `context` names the sheet and columns (at most 256 bytes; indexed once per chunk)
(`TestTableRowChunking`). So a table hit returns a few rows, not one. A line with two or more
tabs is a table row, never a heading (a numeric row such as `1⇥2023⇥$400` used to become a
section).

**Search.** Postgres full text (the lexical leg; with an `embed` role it is fused with vector
similarity, below): each chunk has a stored English `tsvector` and a `simple` (unstemmed) one. Search reads the scope's
chunks (by document) and matches each term against them once, so a query costs time in
proportion to the scope's size times its terms; there is no GIN index, because this ranking
query cannot use one (`EXPLAIN` showed none used, so it would only cost writes). The bound: at
most **32 distinct words** per query (more is 400 `bad_request`) and a **10 s** statement timeout
(then 400, "search fewer documents, or use fewer or rarer words"). `TestSearchCost` prints the cost on this machine: on a
4 MiB scope of 12,425 chunks, 2 common words took 135 ms, 32 absent words 128 ms, and 32 words of
which 6 are common 492 ms (the round-1 query: 193 ms, 444 ms and 867 ms on the same data). Every word of the query is a separate term (OR, not AND; quotes, `-` and other
operators are plain text, so a quoted phrase is its words), scored as `ts_rank_cd` of that term (saturated) times its rarity in the scope
(`ln(1 + N/(1 + df))`), on both vectors, summed; ties go to the lower chunk id. So a rare term
outranks a repeated common one (`TestSearchRareTermOutranksCommon`), results are deterministic
(`TestSearchTopKDeterministic`), and identifiers such as `s.9.4`, `C-13`, `FS-26-01234`,
`Schedule A` or `Annex I` (whose letter is an English stop word) are found
(`TestSearchExactIdentifier`), and rarity is counted within the scope only, so another matter's
documents cannot reorder this one's results (`TestSearchRarityIsScoped`). Queries are at most
1,024 bytes; `k` is 1–50.

**Hybrid search (PR 3b; owner decision 9).** With an `embed` role in `BIGLAW_MODELS`, every
chunk is embedded at ingest (its section path and search text, at most 4,096 characters, 64 chunks
per call, after the ingest slot is released) and so is every query whose scope holds an embedded
document (the word limit is checked first, so a refused query is never embedded or billed; a scope
with nothing embedded is searched lexically without an embeddings call, so it keeps working during
an embedding outage). The lexical ranking above is fused with the scope's embedded chunks ranked
**exactly** by cosine distance to the query's vector: reciprocal-rank fusion,
`1/(60 + lexical rank) + 1/(60 + vector rank)`, each leg's ties by chunk id, so `Rank` is the fused
score and results are deterministic (`TestHybridFusion`). **A chunk of a lexical-only document**
(its embedding failed, or the model changed and the backfill has not reached it) has no vector
rank, so it scores `2/(60 + lexical rank)`: its lexical rank counts for both legs, so it competes
on equal terms instead of being buried under embedded chunks that share one common word
(`TestLexicalOnlyNotBuried`: the only chunk naming "Zebracorn Holdings" stays first). Every
embedded chunk in scope is a candidate, so a hybrid search returns up to `k` hits even when no word
matches. Both legs read only the scope's documents (G1, `TestHybridKeepsScope`). There is **no
HNSW or IVFFlat index**: an approximate index filtered to one matter's documents returns fewer
than `k` rows (measured at PR 3b: with the HNSW index forced, a scoped top-8 over 50,000 vectors of
which 40 were in scope returned 0 rows; pgvector's iterative scan recovers that only up to its
scan limit), so an index would cost writes and break scoped recall. A query costs scope ×
dimensions; on a 4 MiB scope of 12,426 short clauses with dense unit vectors (a probe run for PR
3b's round-1 fixes, 4 x86 cores), 2 common words took 245–350 ms, 2 absent words (the vector leg
dominates) 105–180 ms and 32 words of which 6 are common 555–710 ms, at 512 and at 1,536
dimensions alike (the lexical leg dominates); `TestSearchCost` prints its own hybrid figures. The
`vector` column has no fixed length: a document records the model that embedded **all** its chunks
(`embedModel`, with the calls' `embedTokens` and `embedCostUsd`, null when unpriced), and the vector
leg reads only documents of the current `embed` model whose vectors have the query's length.
Embedding a document first clears its `embedModel`, and sets it only after every batch landed at
one vector length, so a run that fails part-way (or whose batches come back at different lengths)
leaves it lexical only, never a previous model's name over a mix of two models' vectors
(`TestReembedNeverMixesModels`: roll back to the old model and it is re-embedded completely). A
failed or cancelled embedding call never fails an ingest: the document is stored lexical only with
the note "Search over this document is lexical only: embedding its chunks failed or was cancelled
…" (`TestEmbedFailureLeavesLexical`). `serve` runs a **backfill** at startup, one document at a
time (each within the same 10-minute bound as an ingest), for documents stored before the role was
set, under another model, or whose embedding failed; it skips documents stored after it starts (by
the database's clock), notes each failure on its document, logs how many it embedded and how many
failed (a warning when any failed), stops with the server, and gives up after 3 documents **in a
row** fail (`TestBackfillEmbedsOldDocuments`, `TestServeHybridEndToEnd`). A failed **query**
embedding is returned as its model error (502/503/504), never a silent lexical answer. With no
`embed` role, search is the lexical leg alone, as `/health` says.

**Scale and disk (no index).** Each query reads every embedded chunk in its scope. Stored at 1,536
dimensions (what `text-embedding-3-small` returns), a vector is 6,148 bytes (512: 2,052); the probe
above's chunks table was 119 MB for 12,426 chunks at 1,536 dimensions, 58 MB at 512. The round-1
auditor measured ≈ 8 KB per chunk with TOAST at 1,536 dimensions (≈ 95 MB for a maximum-size
document of about 12,000 chunks) and, for one scope, 497–615 ms warm at 51,000–120,000 chunks and
about 1.1 s cold on 4 vCPUs. The rule for operators: keep a matter's scope × 8 KB within free RAM,
and expect search to near its 10 s timeout past about **100,000 chunks per matter on x86** (≈ 20,000
on a Pi 4 with SD or USB storage: an estimate, not measured). Past that, search fails with 400
"search fewer documents".

**Gate E4 (`TestHybridRecallProofRuns`): not yet judged.** Over the 68 planted proof-run facts, a
search within the fact's matter with an **answer-free question** (one per fact in
`answer_key.json`, written without the fact's figures or conclusion, before the first measurement,
not tuned since) must return a chunk holding one of its evidence strings in the top 8 for ≥ 0.95
(65/68). Lexical search alone gives **60/68 = 0.882** (every evidence string counted separately:
86/106 = 0.811), and with the fact's **title**, which states the answer, 66/68 = 0.971 (100/106),
an upper bound; `TestLexicalRecallProofRuns` prints both and fails only below those floors. Lexical
misses by question: `REG-05`, `REG-14`, `DD-06`, `DD-09`, `LIT-D1`, `LIT-I3`, `LIT-I6`, `LIT-C6`.
The hybrid gate runs in CI without a model key by replaying `eval/e4/embeddings.json`, a tape of
`sha256(text) → vector` for every text the measurement embeds (the 355 proof-run chunks as the
chunker cuts them, and the 68 questions and 68 titles: 491 vectors), with the model that recorded
it, at that model's own vector length (the length `serve` stores: there is no dimensions setting). **The committed tape is a self-test tape**, recorded offline by a bag-of-words
hash embedder (`biglaw-selftest-hash`, 64 dimensions), because no embedding key exists where this
was built. It proves the harness only (`TestE4TapeReplays`: every text is on the tape, replay
leaves no document lexical, the vectors are the hash embedder's), and on it
`TestHybridRecallProofRuns` **skips** with "E4 GATE NOT JUDGED". Its recall with that tape,
50/68, is no gate result (a bag-of-words embedder fused with lexical search did worse than lexical
alone). The owner records the real tape once, and from then on CI judges the gate (`t.Errorf`
below 65/68) and fails if any text it embeds is missing from the tape (the chunker or the fixtures
changed: re-record). In **every** mode (replay, record, live) a matter with a document left
lexical only (a failed or refused embeddings call, or on replay a text not on the tape) fails the
test with "E4 NOT JUDGED" before any recall is scored or any tape written, so a flaky endpoint
cannot produce a verdict or a partial tape. `make test` prints the verdict line (in CI too) and,
where the database is required, fails unless the gate passed or skipped as not judged:

```bash
make e4-record EMBED_MODEL=text-embedding-3-small \
  BIGLAW_MODEL_URL=https://api.openai.com/v1 BIGLAW_MODEL_KEY=sk-… \
  BIGLAW_TEST_DSN='postgres://…?sslmode=disable'      # writes eval/e4/embeddings.json; commit it
make e4-live EMBED_MODEL=… BIGLAW_MODEL_URL=… BIGLAW_TEST_DSN=…   # live, no tape (BIGLAW_MODEL_KEY if the endpoint needs one)
```

Any model may be named; the tape records which. `make e4-record EMBED_MODEL=biglaw-selftest-hash`
regenerates the self-test tape offline. Scoring note (r2 auditor): for a few multi-hop facts
(`DD-06`, `LIT-I3`, `LIT-D1`) the question retrieves the operative clause while the key designates
only the other hop, so 2–3 misses are partly measurement; the per-evidence-string figure is
printed beside the gate figure and the gate is not moved for it. Gate A (PR 4b) measures recall
through the agent's own queries.

**Vision transcription (PR 3c; DESIGN revision 5 § 4).** Images (PNG, JPEG, GIF, WebP, by magic
bytes) and scanned PDF pages are sent to the model of the `vision` role in `BIGLAW_MODELS`, as
base64 `image_url` parts through the same OpenAI-compatible client (every call priced like any
other). A PDF page is scanned when it has no text layer, **or only a stamp** (under 80 characters of
text layer, a Bates number or a header, beside a page image, as v1 counted): such a page is read and
its stamp kept after the transcript, outside the transcript's span (`TestBatesStampedScanTranscribed`).
Characters are counted, not bytes. A page with **80 to 399 characters** of text layer over JPEG page
images (a production page under a long legend, a short letter on letterhead, a title page with a logo)
is **mixed** (owner decision 12): its images are read like a scan (two readings, every agreement,
masking and low-confidence rule), its text layer is stored verbatim as the truth for its own words,
and the page stored is only the reading's lines that add to the text layer, then the text layer (the
added lines are the transcript span; the note says only lines a text layer lacks are kept). The exact
rule (`beyond`): the text layer's letters-and-digits words (a figure such as "1,250,000.00" is one word
with its separators; case and ligatures folded) are joined with nothing between them; a reading line is
subtracted only when its own words, joined the same way, occur in that string starting where a layer
word starts and ending where a layer word ends. A word the text layer splits ("D ear") or glues
("receiptat"), or a compound the reading breaks at a line end ("third-\nparty"), is the same line; a
figure inside a longer one ("$125,000.00" beside "$1,250,000.00"), a stamp inside a word ("APPROVED"
beside "unapproved", "PAID IN FULL" beside "unpaid in full") or a part of a word is kept. A short
line (under 3 words or under 12 letters and digits, not ending in a hyphen: a stamp, a handwritten
figure, a signature, a tick) is subtracted only when it is a whole line of the text layer: "PAID"
beside "Amount paid: $0.00", "APPROVED" beside "to be approved", "$500" beside "Suite 500", "Smith"
beside a "SMITH-0001" Bates number are kept; a stamp the layer itself carries as a line is not. A line
with no letter or digit ("✓", "☒") is kept (a mark is information). When in
doubt the line is kept: a duplicate is disclosed as transcript, a dropped image-only line would be
lost (`TestBeyondWholeLayerWords`: every case above, and 3,000 random layer/reading pairs checked
against the joins of every run of whole layer words). A CR, alone or before a line feed, ends a line.
When the reading adds nothing the text layer is stored alone (trimmed) with the note "Page N: its
image was read and adds nothing beyond its text layer." Such a page never holds or refuses the document: with no vision role, or when its
reads fail or disagree, its text layer is stored with the note "an image on the page was not read"
(`TestShortTextOverPageImageMixed`: the 185-character letter on a page-sized JPEG with no vision role,
with disagreeing readings, with one reading carrying a 30-name partner sidebar, and with agreeing
ones, stored once; a 133-character "HIGHLY CONFIDENTIAL – ATTORNEYS' EYES ONLY" legend whose
readings repeat it, the body stored once with the legend; a reading identical to a letter with a
"third-\nparty" line break, or to a text layer that splits "D ear" and glues "orderdated", stored once
with only "ACME LLP" added; the cost of a mixed page recorded). A quote found in a transcript line
and again later verifies at the later copy when that one is outside every transcript span and is
whole words (no letter or digit just before or after it, nor a figure's separator: "paid $500" never
moves into "paid $5000", "paid $500,000" or "$500.50", nor "500" into "12 500";
a text layer's words stay `exact` 1.0 when an added line repeats them; `TestMixedPageKeepsWhatTheLayerLacks`). The
cut, 400 characters, is five lines of 80: a legend with its protective-order reference and Bates
number runs 55–266 characters, a page of text about 3,700. A page with 400 characters or more over
any image (a letter page, an OCR'd scan) is a page of text: stored as it is, with no call, never held
or refused, and noted (a 2-page letter on letterhead makes 0 calls).
There is no OCR engine and no rasteriser: a scanned PDF page is read only when it is one to four
JPEG images stored whole (`/DCTDecode`), which the reader child finds by their offsets in the file
and the server sends **as they are** (`TestScannedPDFDCTPagesExtracted`: the bytes sent are the
embedded JPEGs). A page whose images use another encoding (CCITT fax, JBIG2, Flate bitmaps, a JPEG
under a second filter), a form XObject, an encrypted PDF, a JPEG stream whose `/Length` is wrong, or
a TIFF image is 422 `scan_encoding_unsupported`, with advice to export as JPEG or from a phone
scanner app, when the document has no text at all (`TestScanEncodingUnsupportedTyped`); beside a
text layer or stamps such a page is noted as unread and the text stored (a one-line cover letter
with a logo, a CCITT-scanned production with Bates stamps: `TestBatesStampedScanTranscribed`). Every image's header is checked
(`image.DecodeConfig`; WebP by magic only) before it is stored or sent: a damaged one, or one
declaring more than 12,000 pixels a side or 50 million in all (a decompression bomb for the model's
server), is 422 `unreadable`, with no call made, when every scanned page's image is such, stamps or not
(`TestImageDimensionsCapped`, `TestMixedPDFDamagedImageNoted`); otherwise that page is noted as
damaged and unread, nothing is sent for it, and the rest is read or stored (`TestMixedPDFDamagedImageNoted`;
a note about unread pages says to upload again once a vision role is set only when none is); a page's images may total 20 MiB, at most
4 per page (`TestChildScanLimits`). The server re-checks the reader child's answer (pages in range,
in order, without a text layer; images inside the file and within 20 MiB): `TestServerRechecksChildAnswer`.
- **Two independent readings.** Each page is read twice, by two different prompts
  (`vision_prompts.txt`; agents `transcribe-a`/`-b`, each at most 4,096 output tokens), each told to
  write `[illegible]` rather than guess and `[blank]` for a page with no text. The text stored is the
  reading as returned (only its marks normalised); to compare the readings, ligatures are made plain
  and a hyphenated word is one token with its hyphen, also when broken at a line end (`third-\nparty`
  is `third-party`; `re-\nlease` is not `release`: `TestBenignNoiseNotLow`); a figure is one token with
  its inner `.` and `,` (so `1.250` is not `1,250`). Agreement is Dice over the readings' word
  multisets (case folded, marks left out; `[illegible…]`, `[unclear…]`, `[blank…]` and `(illegible)`
  are marks, and a guess inside one such as `[illegible: five hundred?]`, also across a line break,
  is dropped): below **0.75** the page is refused, 422
  `transcription_unreliable` for an image or a fully scanned PDF, with nothing stored, no text, no
  chunk, no file (`TestUnreadableImageNotFabricated`: two plausible letters for a noise image); a
  blank reading beside a non-blank one is a disagreement. A stored page is **low confidence** when
  its agreement is below **0.9**, **or**, however high its agreement, when the readings differ on what
  matters: any `[illegible]` mark in either reading; a figure (a token with a digit), a negation or
  modal (`not`, `no`, `nor`, `never`, `n't`, `cannot`, `shall`, `may`, `must`, `will`) one reading has
  and the other lacks; 5 or more words one reading lacks (a dropped or garbled line: every differing
  word counts); or the same words in a different order (swapped parties: a word-level LCS; readings
  whose token counts multiply past 4,000,000, about 2,000 words each, are not compared in order and
  count as different). The page's `disputed` lists those tokens (at most 12: marks, the order flag
  and figures first) and a note names them (`TestReadingsDisagreeOnWhatMatters`:
  $12,500 vs $125,000, FS-26-01234 vs FS-26-01284, "shall not assign" vs "shall assign", a dropped
  line, payer and payee swapped, $1.250 vs $1,250 million, 12.500,00 vs 12,500.00, each in either
  order; one differing prose word is not low;
  `TestAgreementThresholdsPinned`: 0.74 refused, 0.75 stored, 0.889 low, 0.90 not). The first
  reading is kept (the other if it is blank), so which reading's figure is stored is arbitrary, and
  that is why such a page is low confidence. Wherever the other reading has a mark, the readings'
  words are aligned (their LCS, a mark standing for any one word) and every word of the kept reading
  not surely aligned to a word the other read becomes `[illegible]`: one aligned to a mark, and one the
  two tie-breaks of the alignment place differently (where a mark belongs is ambiguous: every
  candidate is masked). So the mark stays at its place even when its word repeats on the page (a
  payee, an amount) or anchors are few (`TestIllegibleMaskedAtItsPlace`: 10,000 random pages of five
  repeating words, a substituted word masked exactly at its place; with words also inserted and
  dropped, a word never kept more often than the marked reading has it); pages too long to align mask
  every occurrence of each word the other reading has fewer of. A mark is never
  filled in from either reading, also when each marks a different word (`TestIllegibleNeverFilled`,
  `TestIllegibleNeverFilledTwoSided`). An endpoint echoing the request's key into a reading has it
  replaced by `[redacted]` (`TestKeyEchoNeverStored`). Nothing readable on any page is 422
  `no_content` (`TestBlankImageNoContent`); a reading cut at the output limit, or longer than 64 KiB
  (16 bytes per token asked for), is 422 `transcription_unreliable`, never a silently short page.
  More than **20** scanned pages is 413 `too_many_pages` before any call (`TestTooManyPages413`). All
  of one document's vision calls stop after 10 minutes, and its text, counted over all its pages,
  after 16 MiB (`TestVisionFailuresTyped`, `TestTextCapCumulative`). Notes round agreement down.
- **A mixed PDF keeps its text layer.** Only its scanned pages are read; each transcript goes in
  its page's place and every text-layer page stays byte for byte (`TestMixedPDFTextLayerNotRewritten`:
  the vision model "reading" different words for them changes nothing). A page there that cannot be
  read (its readings disagree, its vision call fails or is refused, or it is past the first 20
  scanned pages) is left unread with a note naming why, and the text layer is still stored, 201
  (`TestMixedPDFVisionFailureKeepsTextLayer`; v1's model merge of the two is dropped).
- **Provenance.** A document's `transcript` names the model, each transcribed page with its byte
  span in the text, its agreement and `disputed` tokens, the least agreement, `lowConfidence`,
  `transcribedAt`, and the calls' `tokens` and `costUsd` (null when the model is unpriced), recorded
  whenever a vision call was made, also when no page could be transcribed (it is SQL `NULL` only
  when nothing was sent); `method` is `vision` (a mixed PDF stays `text_layer`, its `transcript`
  listing the transcribed pages); a note names the model and says to check quotes against the
  original, which is kept and downloadable. Each chunk that overlaps a transcribed page is marked
  `transcribed` (search hits say so). An upload refused after its vision calls stores nothing, so
  their tokens and cost go to the operator's log (`TestVisionRefusalIs422`).
- **Citations into a transcript** (`VerifyQuote`): the method gets `@transcript`
  (`exact@transcript`, `tolerant@transcript`, `judge@transcript`, …), the confidence is capped at
  **0.85** (below text-exact 1.0 and tolerant 0.95, above the judge's 0.8), the result names the
  model (`transcribedBy`) with the note "machine transcription of an image (model): check against
  the original", and `lowConfidence` is set when the match touches a low-confidence page: per
  quote, so a finding resting on a good page is not flagged
  (`TestTranscriptCitationCappedAndDisclosed`, `TestLowAgreementFindingsGatedIndividually`; the
  engine gates a finding resting only on such quotes for a lawyer, `TestLowAgreementFindingGated`).
- **No vision role.** An image or a fully scanned PDF (stamps included) is stored with
  `status: needs_transcription` (no text beyond its stamps, a note) and the upload is **503
  `vision_not_configured`** with the document; the original is kept
  (`TestVisionNotConfiguredTyped503KeepsOriginal`, `TestExtractImageWithoutVisionDegrades`). Nothing
  is sent to any model: no text role is ever used for images (`TestTextModelNeverSentImages`; end to
  end, `TestServeNeverSendsImagesToTheLead`). A mixed PDF is stored ready with its text layer and a
  note on its unread pages that says to upload it again once a vision role is set: the job does not
  read it later. A vision call's failure on an image or a fully scanned PDF is the model's G6 status
  (502/503/504) and the upload stores nothing; a request the endpoint rejects (HTTP 400 and other
  4xx but 401/403/404/408/429) is 422 `unreadable`, not an outage, and is not retried; its message
  says the cause is the image (damaged, animated, too large) **or** the request (a text-only model
  id, an exhausted account). A waiting document so refused spends one of its 3 attempts and ends the
  job's run, so one refusal never fails the documents queued behind it (`TestVisionRefusalIs422`:
  of 3 waiting documents only the first is tried until it fails at its third run).
- **Reading slots.** A document's extraction holds one of the 2 ingest slots; its vision calls then
  wait for one of 2 reader slots of their own (holding the extracted text, not the file, while they
  wait; the file is read again), so slow or hung vision calls never hold up text, `.docx` or
  text-layer PDF uploads (`TestVisionCallsDoNotHoldIngestSlots`). Each wait is bounded by 10 minutes
  (503 `busy`).
- **Transcription jobs** (`jobs.go`). With a vision role set, `serve` transcribes waiting
  documents at startup and every 15 minutes, oldest first, one at a time, each under a 40-minute
  lease in the database (two servers never take the same one; one whose server died mid-job is
  taken again when its lease ends), completing it under its own id with its original file. One its
  reading refuses (unreliable, no content, unsupported, too many pages, damaged, rejected by the
  model) becomes `status: failed` with the reason as its note. Any other failure ends that run and
  releases the lease, and the document waits for the next run with a note saying why: a stop
  (SIGTERM), a wrong model setting (401/403/404 `model_not_configured`), a rate limit or busy reader
  slots spend none of its attempts (`TestJobConfigErrorSpendsNoAttempt`); an outage (or a database
  failure) spends one, and the third fails it; a document whose server died during three attempts
  (an OOM kill, say) is failed at its fourth claim without a call, not retried forever
  (`TestTranscriptionJobResumesAfterRestart`). The run logs how many it transcribed and failed. The
  operator re-queues failed documents (after fixing the model setting, say) with
  `UPDATE documents SET status = 'needs_transcription', attempts = 0, lease_until = NULL, notes = ARRAY['Waiting for transcription (re-queued by the operator). The original is kept.'] WHERE status = 'failed'`
  (in `psql`; tested verbatim). The contract v1 fields (`POST /documents` with `content` +
  `contentType` → 202 `transcribing`, `GET /health` `capabilities`) are PR 8's (PLAN PR 8), not built here.
- **Live check (owner, no key here).** Every test runs on `model.Fake` or `httptest`. With a vision
  model on an OpenAI-compatible endpoint:
  ```bash
  BIGLAW_DATABASE_URL=… BIGLAW_MODEL_URL=http://localhost:11434/v1 BIGLAW_MODELS='lead=qwen2.5:14b,vision=qwen2.5vl:7b' \
    ./biglaw serve --allow-no-auth &
  curl -s -F file=@letter.jpg http://127.0.0.1:3101/documents/upload | jq '{status, method, notes, transcript}'
  ```
  then download `GET /documents/{id}/file` and compare it with the transcript by eye.

**Page reader.** `Outline` lists every section in order as `{sectionId, number, title, level,
tokens}`, where `tokens` estimates what `Section` returns for it: the section with its
subsections. `Section` reads that text verbatim from a character `offset`, at most `limit` tokens
(default 2,000, at most 8,000), cut at white space; a longer section comes back with
`truncated: true` and `next`, never silently cut, and the pages join to the section exactly
(`TestOutlineHasNumbersTitlesTokens`, `TestSectionPaginates`). A table's page (a sheet, a `.docx`
table: two or more tab-delimited rows) is whole rows, at most `TablePageRows` (25), and a page after
the table's first names its header row in `header` (with `headerAt`, where it starts): a reference to
the row, read where the page's own table starts (a table starts after the section's start, a blank
line, prose or a `#` marker; looked for in a window before the page that doubles from 64 KiB until
the table's start is in it, bounded by the section: `TestTableHeaderIsThePagesOwn`,
`TestTableHeaderOfASecondTable`), never copied into the page's text, which stays verbatim (`TestTablePagedByRows`). An offset past 2^31 − 1 is 400 and a
section id past it is 404, never a database error.

**Verification ladder** (v1's `tabcite` ladder): exact substring, confidence 1.0; tolerant
substring (curly quotes straightened, U+2010/U+2011 hyphens read as `-`, white space collapsed, case folded on both sides; `TestToleranceFoldsHyphens`), 0.95;
a quote with `|` between table cells where the document separates them with tabs (an `.xlsx` row) is
matched with its separators set aside, still `tolerant` but at 0.9 (`CellConfidence`,
`TestTableRowQuotedWithPipesVerifies`); one
paraphrase-judge call over the best-matching window of the document (at most about 3,000 bytes
around the span holding most of the quote's words), capped at 0.8; when the judge is less than
0.75 sure, three independent votes, a strict majority needed, confidence 0.75 × votes/3, below
the judge's cap; otherwise `none` with a note (`TestLadderMethodsAndCaps` and the carried
`TestCitation*` vectors). The judge is a function the caller supplies (the model wiring lands with
the agent runtime, PR 4a). **A judge failure is returned as an error, never as "unverified"**,
whether it is the first call or any one of the three votes, since a lost vote could have decided
the majority (changed from v1, G6: `TestCitationJudgeFailureIsAnError`). `VerifyQuote` also names the deepest
section holding the match. `Resolve` maps a model's citation by file name (any case, directory or
extension) to the one document in scope with that name; an ambiguous name resolves to nothing.

**Upload and download.** `POST /documents/upload` takes `multipart/form-data` with one part named
`file`, streams it into the files table and ingests it: 201 with the document (`id`, `name`,
`contentType` (detected), `fileId`, `method` `text`|`text_layer`|`vision`, `noText`, `status`
(`ready`|`needs_transcription`|`failed`), `transcript` (when anything was transcribed), `pages`, `notes`,
`sections`, `chunks`, `createdAt`). A part with no file name is 400 `bad_request`; the declared
type is kept as the file's metadata only (a wildcard such as `*/*` is stored as
`application/octet-stream`). Errors are `{error, errorType}`: 400 `bad_request`/`bad_name`/
`bad_content_type`, 403 `cross_origin`, 408 `upload_timeout`, 413 `too_large`/`too_many_pages`, 415, 422
`unreadable`/`transcription_unreliable`/`no_content`/`scan_encoding_unsupported`, 499
`cancelled`, 500 `internal` (logged, fixed text), and a vision call's failure as its model kind (502
`model_unavailable`, 503 `model_rate_limited`/`model_not_configured`, 504 `model_timeout`; fixed text). 503
`vision_not_configured` also carries the stored `document`. A client must send at least 64 KiB every 30
seconds and the whole file within 15 minutes, or its read fails and its upload slot and pool
connection are released at once (`TestUploadReadDeadline`: 16 stalled clients and one trickling
client, then the slots are free, the pool is back to its baseline, a 17th upload succeeds and no
file row persists; `TestUploadTotalDeadline`). A browser request from another site is refused
(`Origin` other than the request's own host, or `Sec-Fetch-Site` other than `same-origin`/`none`),
because `multipart/form-data` needs no CORS preflight. `GET /documents/{id}/file` returns the
original with the **detected** content type (never the uploader's claim), `nosniff`,
`Content-Security-Policy: sandbox`, `Content-Length` and an RFC 6266 attachment header: `filename*`
carries the name (UTF-8, percent-encoded), and both it and the ASCII `filename=` fallback (non-ASCII
and `%` become `_`) are safe to save on Windows (`<>:"/\|?*` and controls become `_`, a device name
such as `CON`, `aux.txt` or `COM1.tar.gz` gets a `_` prefix, trailing dots and spaces go:
`TestDownloadNameSafe`). The download **streams**: the file's size and SHA-256 are verified in a
first pass (so corrupt bytes are never sent), then it is sent one 1 MiB chunk at a time under a
read slot, each 64 KiB under its own 30-second write deadline and the whole within 15 minutes, so
a client that stops reading is cut within a window and holds at most one chunk meanwhile
(`TestDownloadBounded`: once all 4 read slots' holders are blocked writing to their stalled
clients, 12 stalled downloads of an 8 MiB file hold +8 MiB of live heap, 2 chunks per slot, under
a bound of 2 chunks per slot + 8 MiB, and were all cut after about 4.9 s at a 1.5 s window; the route used to hold a whole file per stalled client;
`TestDownloadDeadlinePerWindow` pins the 64 KiB per deadline). The last chunk is sent only after
the streamed bytes' hash matches, so a file whose rows change between the verifying pass and the
streaming pass reaches the client as a short body, never as a complete 200
(`TestReadFileWithholdsUnverifiedTail`). Where
a deadline cannot be set (a response writer that hides its connection, such as a middleware
wrapper without `Unwrap`), an upload is refused and a download cut before its first byte, never
run unbounded (`TestTransferDeadlinesFailClosed`). `Delete`
removes a document with its sections, chunks and original file in one statement; deleting either a
document or its file in any other way (SQL, a later cascade) takes the other along: a trigger and a
cascade (`TestFileDeletedWithDocument`). A file left without a document by a server that died
mid-ingest is deleted at the next `serve` start once it is an hour old (`TestOrphanFilesSwept`);
no live upload is that old (15 minutes to upload + 10 to wait for a slot + 2 to read + 10 to wait for a reader + 10 for the vision calls). A stop
(SIGTERM) waits 10 s for requests in flight, then cancels them: an ingest still running answers
503 `stopping` and removes its file, `serve` exits 0 with a warning, and connections still open
after another 10 s are closed (`TestServeStopCancelsIngest`).

## Agent runtime and memo tools (`internal/agent`, `internal/analysis`; PR 4a)

The memo workflow (§ Memo workflow) drives these. What PR 4a ships, all tested with `model.Fake` or no model:

- **Tools and the registry.** A `Tool` has a `Spec` (name, kind `go`/`subagent`/`fanout`, a
  description, input and output JSON Schemas, a budget class, a turn cap, allowed tools for a
  subagent, ops that must be called alone, `Final` for `finish`) and `Call(ctx, Call) (Result,
  error)`; a `Call` carries the task's document ids (G1), the input, a grant and the depth. The
  registry refuses a duplicate name, a 49th tool (`agent.MaxTools`, 48), a schema that is not strict-shaped (every object
  `additionalProperties: false`, every field required, optional fields nullable; in and out), a
  description under three sentences or without a "Do not use" line, and 1–3 `Example: {…}` lines
  that do not validate. Definitions are frozen when registered: the same bytes reach the model on
  every turn (prompt cache), and no matter data can reach a schema. The definition sent leaves out
  `minLength`/`maxLength` (strict mode refuses them); Go enforces them before a tool runs. At most 24 tools are visible to
  one agent (`agent.MaxVisible`; both caps raised from 22/14 for custom agents and tools, § Manage). Specs are data: `internal/docs/specs/*.json`, `internal/analysis/specs/*.json`.
- **One loop, two depths** (`Runtime.Run`). The lead (depth 0) and a subagent (depth 1, the
  `Subagent` tool: its own prompt, allowed tools, turn cap, grant and output schema; its transcript
  is discarded) run the same loop. Every tool call goes through `Runtime.Invoke`, in this order:
  offered to that agent, no subagent or fan-out tool at depth 1, alone when it must be (`finish`,
  `ledger{op:"plan"}`), input schema, the task's `Policy` (PR 4b), then a grant; the tool's usage is
  charged even when it fails and its output must match its schema. A subagent's answer must validate
  (one repair request, then `ErrToolOutput`). At most 8 tool calls run per turn. Tool results older
  than four turns are replaced by a handle naming their step. **Tool outputs are bounded by type and
  never trimmed** (owner decision, lab-r3 r4: a byte cut of JSON panicked on escaped text and showed a
  part as if whole): every tool output that holds a list or long text is one page of at most
  `agent.PageBytes` (12 KB, under the 16 KB retry bound, so a page always reaches the model whole),
  filled by `agent.Page` (`Take` keeps the leading items that fit, at least one; `TakeText` a page of
  text), and says how to get the rest in its schema: `next` (an offset: `retrieve` search, outline and
  section; `ledger` findings, relations, critics and `draft_get`) or `truncated: true` with a narrower
  call (`numbers` handles and table, whole rows; `join_discrepancies`; `finish`'s lists, by priority;
  `committee`, the rest from `ledger` op `critics`; `sweep`, the findings in the ledger and the uncovered
  pages for a later sweep). The other outputs are bounded by their input or answer: `verify_quote` (one
  match), `cover` (≤ 40 issue ids), a subagent's answer (its schema's `maxItems`/`maxLength` and its
  8,192-token answer cap) (`TestRetrievePagesByType`, `TestLedgerPagesByType`, `TestReportsPageByType`,
  `TestAnalysisToolsPageByType`). The runtime's last guard refuses rather than trims: a result over
  100 KB reaches the model as a typed refusal, `{"error":{"code":"result_too_large","message":…,"tool",
  "bytes","limit","next"}}` (`agent.ErrorSchema`, `TooLarge`; `TestOversizedResultRefusedAndShrunk`,
  `TestV4EscapedResultsNeverPanic`); an endpoint's 400/413/422 compacts all but the latest turn's and
  retries, then replaces a latest result over 16 KB by the same refusal and retries once more, for that
  request only (`TestR2VShrinkSticksAndHidesNext`). A tool that panics ends its run with `ErrPanic` (its
  stack logged; its step keeps the usage its meter recorded: `TestPanickedStepKeepsItsUsage`), and the engine fails that task as an internal error; the task goroutine recovers too,
  so a panic never stops `serve` (`TestPanickingToolFailsOnlyItsRun`). Text in tool results is labelled untrusted matter data.
  The model sees refusals (reason and next step) and tool failures in BigLaw's own words only; any
  other error is logged and shown as "internal error". A model failure (G6) ends the run, except a
  subagent's answer cut off at its token limit before any text or whole tool call: that is a bad
  answer (one shorter try, then `ErrToolOutput`), not a failure (`TestSubagentCutOffEmptyAnswerIsNotAModelFailure`).
- **Budget** (`Budget`). Calls and USD per task, held and charged in Go. A model call holds one call
  and its worst-case cost (each request byte a token plus 1,000, as a cache write, plus `MaxTokens`
  of output) and then charges what it spent, a refused-but-billed answer included; a tool's grant is
  up to its turn cap, refused only when no call is free, and its unused part returns when it closes.
  The task keeps back, in calls and USD, what its later phases need (`Keep`, by level: evidence <
  work (lead turns, drafting, free tools) < review (the committee) < final): a grant never takes what
  a higher level keeps, so only `finish` spends the reserve + meta. Query embeddings are charged
  in USD and tokens; an unpriced model counts as a call of unknown cost.
- **Tool bandit** (`Bandit`, `Stats`; `MemStats` here, Postgres in 4b). Per task signature
  (workflow, practice, jurisdiction prefix, lead model, documents 1–3/4–9/10+), each tool's and each
  subagent model binding's calls, reward, failures and spend; a reliability-aware UCB1 score; an
  untried tool ranks first once. It adds one hint line to the latest tool result before a lead turn
  (`retrieve`, `ledger`, `committee`, `finish` first, `verify_quote` never ranked), picks a
  subagent's model only among the role's and the lead's, and never hides or refuses a tool.
  `Off` sends no hint. Every tool result is recorded (refused or failed: reward 0).
- **`retrieve`** (`op` search, outline, section; `k` ≤ 8; sections paged with `next`) and
  **`verify_quote`** (the ladder; the paraphrase judge runs on the `critic` role, at most 4 calls
  from the caller's grant, each held and charged like the loop's own calls through
  `Runtime.ChatWithin`, so it never spends past the grant's calls or USD; the quote and the passage
  each fenced as untrusted text, fence markers inside them neutralised). A document outside the call's
  ids is "not found".
- **`numbers`**: `calc` (exact decimals: `sum`, `diff`, `ratio`, `pct`, `product` (a percentage
  counts as its fraction: 35% of $100,000 is $35,000), `date_add`, `date_diff`, `business_days`
  with the caller's holidays; a negative day count counts back; `date_add` with holidays set, even
  `[]`, rolls a result on a weekend or holiday to the next day that is not, backward for a negative
  count, as FRCP 6(a)(1)(C) and 6(a)(5) do, the worksheet showing the raw date) over verified finding
  values (`F12.v1`, the form extraction results, `ledger` and `handles` show; also read: `[F12].v1`,
  `f12.V1`, `F12 v1`, `F12.d1` = its first date value, a bare `F12` = its only value) or literals (a
  literal that is plainly a full date or "21 days" needs no unit; a count before a date is swapped).
  What cannot be read is an error listing the finding's values by ref, with a call that works
  (`TestCalcAcceptsWhatModelsSend`); a
  worksheet id is a hash of its steps; a quarantined or unknown finding is refused, never 0. Money is
  USD only when its unit is empty, `$`, `usd` or `dollars`; another currency keeps its unit, so a sum
  across currencies is refused. Date differences are exact over any span.
  `handles`: a `{Codename}` per distinct verified money, percentage, count or identifier value (never
  a date), most salient first, 64 at most, assigned over the whole verified ledger (a codename names
  one figure whatever `fids` a call asks for); codenames given before (`kept`) keep their figures
  and a new figure takes the next unused one, so in a run (where the engine keeps them in the
  ledger) a codename never changes meaning; `Substitute` puts the exact value in for a codename or a
  worksheet's `{W…}` (its wid), and an unmapped one is left as written and reported. `table`: a
  key-figures table from verified values only.
- **`join_discrepancies`**: cross-document conflicts between verified values, Go's candidates and a
  judge's verdicts (lab-r4 r7, owner decision: whether two figures are one quantity is the agent's, never a
  rule in Go). `analysis.Candidates` proposes the pairs: two values the figure reader read clearly as one
  kind in one currency (money is never compared with a share count; durations and identifiers, kind
  other, are not compared), from two findings in two documents, their values different (dates: not
  compatible at their common precision); two values in two documents are asked once however many findings
  repeat them. Pairs already judged come first whatever the cap (a "yes" never falls out as findings
  arrive); then every value's three most alike, closest counterparts (`rankPairs`: tf-idf cosine of each
  value's entity, metric, reader labels and claim, plus a floor, times the closeness of the amounts or
  dates, so closeness orders pairs that share no word too; no exact label match; summed in a fixed order
  and rounded, so the same input ranks the same way on every call: `TestRankPairsDeterministic`), then
  the rest; the cap is the judge's budget (`CandidateLimit`: 20 pairs a funded call), the rest counted
  (lab-r4 r8 M1, r9: on bench6's ledgers, under the stand-in's, reworded, sparse or synonym labels,
  every true find ranks within the first 94 of 13,037 SEC pairs and 8 of 1,022 trust pairs). A join
  that leaves figures with none of their counterparts asked says how many in Limitations (at the
  default finish budget, 73 of bench6 SEC's figures). The engine's `judge_figures` subagent (critic role, its tool
  `figure_context`; `internal/engine/judge.go`) gets both values' quotes, claims, the analyst's entity and
  metric, the reader's labels (party, counterparty, measure, period, typed entity) and the lead's aliases,
  and answers per pair `yes`, `no` or `unclear` with a reason quoting both passages; each side's figure is
  marked ⟦…⟧ in its quote (the span admission matched its value to, `Reading.Stated`) and given as written
  (`valueText`), and a yes is only for two figures that claim to measure one quantity of one party and
  period and differ (bench7: all four of its yeses were false). Pairs whose amounts are one amount at
  another precision or sign ("$45,000.00", "-45000", "$45,000") are never proposed. Go checks every
  verdict (`CheckVerdicts`: one for a pair it did not propose is rejected, so a "yes" on non-spans or on
  two kinds asserts nothing; a second verdict for one pair or an unknown answer is unclear, and so is a
  yes or no whose reason does not repeat, for each side, four words in a row of that side's quote or
  claim that the other side's texts do not hold: r8 L1, r9 L1; and a yes whose reason does not write both
  figures beside its quotations, by amount ("$103,800" names "103,800.00"), its quotations the runs a side's
  passages hold with four or more words besides the figures' own, whatever quote mark opens them or
  none, so neither a passage's own quote marks nor single or missing quotes turn its figure into the
  reason's words: bench7, `TestBench7FalseYesesAreNotConflicts`, `TestNamesValuesReadsQuotationsByPassage`,
  `TestNamesValuesQuotationsWhateverOpensThem`,
  `TestBench7TrueTargetsReachAYes`). Only "yes" is a conflict (with its reason); "unclear" is a low-confidence Limitations line
  naming the pair; pairs the cap or an unfunded judge left out are counted in a Limitations line
  (`TestJoinAssertsOnlyTheJudgesYes`, `TestJoinRejectsVerdictsGoDidNotPropose`, `TestJoinScenarios`,
  `TestJoinLabelsAreContextNotKeys`, `TestJoinBenchLedgers`). The judge is called like the reader:
  batched (20 pairs, 32 KB a call), on one bandit-picked model, metered and journaled through `Invoke` out
  of its caller's grant (`join_discrepancies`' own 16 turns; finish judges first from a quarter of its
  grant's calls and USD, after its reads and beside the drafting share's keep, at least a quarter of 4 calls when the root is below that keep: `TestFinishJudgeHasAFloor`; the coverage pass from a quarter of its share: r8 L3), and its yes and no answers
  are cached across a matter's runs (`figure_reads`, keyed by the judge's spec hash, the model, the matter
  (a task with no matter number: the task alone), the task's documents, the aliases naming either side
  and the pair's content: `TestJoinVerdictsCachedAcrossRuns`, `TestNoMatterTasksDoNotShareVerdicts`,
  `TestVerdictKeysScopeAliasesAndDocuments`); every verdict is kept in the task's ledger (kind `verdict`,
  keyed by the two values in their two documents and the aliases naming them), so `finish` and the
  coverage pass read the verdicts the tool got. The join runs over every
  verified finding; `fids` only choose which conflicts are reported (those holding a named finding), so a
  cross-issue pair is found from either side (`TestJoinDiscrepanciesFidsDoNotScopeTheJoin`), and the
  lead's aliases are kept for the run (ledger kind `alias`) and given to every judge call
  (`TestJoinAliasesReachFinish`). Its output also names the documents the sweep left partly unread
  (`unreadDocuments`).
- **Coverage** (`CheckCoverage`, for `finish{op:"check"}` in 4b): plan issues with no section;
  parties in two or more verified findings, a Conduct issue's actor, or a roster name, that the draft
  never mentions (a firm's name does not cover a person); salient figures that never land.
- **BELO-lite** (`belo.json`): the classes and 11 predicates; `Relations` re-orients a reversed
  triple and drops an ill-typed one.

Limitations of this layer:
- **The USD cap holds only if the endpoint bills within `max_tokens` and adds under 1,000 tokens of
  its own** to a request (the worst-case bound); a provider that bills more is charged anyway, so the
  cap can then be passed by that excess. Unpriced models are bounded by the call cap only.
- A tool's grant takes all the free USD because calls run one at a time; a fan-out that runs
  subagents in parallel (PR 4b's `committee`) must size its grants itself.
- A remote tool that spends past its grant is charged what it reports (money already spent); in
  process a grant cannot be exceeded.
- The bandit's statistics are shared by every task with the same signature: one matter's documents
  can make a tool fail often and push it down another matter's hint (it never hides it). Its
  constants (β 0.5, γ 1, c 0.5) are uncalibrated; gate B measures it on and off.
- Codenames are kept for the run (ledger kind `handle`): one names one figure from its first use,
  also in the summaries later drafters read; the renderer substitutes with the set stored with the
  section, only outside quoted spans. The 64-name pool is the limit: a run with more distinct figures
  leaves the least salient late ones without a handle (a drafter may still state them inside a
  cited quote).
- `join_discrepancies` proposes only what the figure reader read clearly (`Value.Fig`) and asserts only
  what the judge says is one quantity: a judge that calls two different quantities one makes a false
  conflict (shown with its reason, for the lawyer to check), and one that calls one quantity two, or a
  reader that reads one figure unclearly, misses one. No rule in Go decides whose figure it is, what it
  measures, for which period or who else is named (r7: the party, counterparty, period and typed-entity
  matching rules, `oneQuantity` and `periodOf`, were deleted; prompt-faithful role-name labels, "the
  Executive" for two people, had made false conflicts of correct readings). Past the pairs its budget
  judges a matter's join is incomplete and says how many figures it left without any counterpart judged;
  a true conflict whose two values' contexts share no rare word and whose amounts are far apart ranks
  late. A value's document is the quote that
  holds its text, else the finding's first quote.
- Calls in one turn run sequentially, not concurrently; `retrieve` and Go tools spend no calls, so a
  lead can issue up to 8 per turn for every turn its budget funds.
- **Line budget:** `agent` 1,242 against 1,100, `analysis` 1,183 against 950, the `docs` adapters
  153 against 50 (§ Budgets: accepted by owner decision 14; no total ceiling since decision 17).

## Memo workflow, durable runs and the REST API (`internal/engine`, `internal/api`; PR 4b)

`POST /tasks` with `workflowType: "memo"` (the only workflow built; `diligence` and
`contract_review` are 400 until PRs 5 and 6) stores a task and wakes the runner. The lead agent
(role `lead`, prompt `internal/engine/specs/lead.md`) sees 12 tools in a fixed order: `retrieve`,
`ledger`, `sweep`, `extract_facts`, `analyze_issue`, `check_deviation`, `numbers`,
`join_discrepancies`, `draft_section`, `committee`, `finish`, `use_skill` (§ Manage, Skills). A task runs on its snapshot of these specs (§ Manage). Specs are data
(`internal/engine/specs/*.json`, subagents `specs/agents/*.{json,md}`: the five above plus the
critics `prosecute`, `reproduce`, `audit_gaps`). Everything below is tested with `model.Fake`.

- **The ledger** (`ledger`; one Postgres table, a JSON row per kind and key): `plan` (the issues
  the deliverable must answer, each typed by a BELO-lite class), `findings` / `get` (brief or full:
  citations and value refs `F3.v1`; `findings` a page of about 12 KB at a time (under the 16 KB over-long bound), `offset` from the
  result's `next`, `TestLedgerFindingsPage`), `resolve` (a critic finding fixed or disclosed, with a note),
  `draft_get` (a finished section only: `not_drafted` otherwise; paged by byte `offset`), `relations` and
  `critics` (the committee's findings with their status), each paged the same way (`TestLedgerPagesByType`). A subagent may only
  read, and never sees a quarantined finding (G3).
- **Evidence** (`extract_facts`, `analyze_issue`, `check_deviation`; subagents on the `specialist`
  role). Go completes each call (the section's text, the packs, and for an `extract_facts` call that
  names no issue, a sweep visit's, the plan's issues: each finding names the issue it bears on, and one
  that names none of the plan's is not admitted (a page read again after `op plan` added issues is sent, and admits, only the new
  ones); no per-call cap drops a page's facts: `TestSweepAdmitsOnlyThePlansFindingsOnce`, `TestTablePageKeepsARowEach`, `TestSweepCapNeverDropsProseFacts`) and checks every answer before it
  reaches the ledger: each quote goes through the ladder (`VerifyQuote`, then the `verify_quote`
  tool with its judge); a finding is verified only when every quote is (G2), else it is stored
  quarantined (G3) and never drafted from; a typed value absent from its own quotes is dropped, with a note on its finding
  (`valueNotes`, shown in the API's finding view and the UI), in the sweep's and `extract_facts`'s `notes`, as one LOW
  committee line per run listing the cited findings not raised before (after the other critics' lines, capped, and the
  committee's page puts HIGH and MEDIUM lines first: `TestR4V3AdmissionNotesFloodCommittee`), and as one Limitations
  line from the coverage pass's `unadmitted` list. What a number means is read by a model, never by
  a word list in Go (lab-r4 r4, owner decision): code finds the number spans of a text and reads their
  digits exactly as written (`internal/analysis/figures.go`: digits, separators, a decimal mark, a minus
  written right before them; a span's readings are what its form allows, "1,200" reading 1200 or 1.2;
  dates found by their form; specified by the literal corpus in `figures_test.go`, a generated property
  test and a fuzz test: no panic, spans in bounds and never overlapping, every reading round-trips),
  and the figure reader (`read_figures`, `specs/agents/read_figures.md`, critic role, with its own tools
  `figure_context`, the document around a quote, bounded and paged, and `exact_calc`, exact decimal
  arithmetic) labels each span by id: money, count, percent, date, other or unclear; its currency, its
  scale and the span or words that state it, which mark is its decimal point, its party, whether it is
  a range's end and its sign, what it measures and the period it is for, and for a typed value what its
  typed entity is to it. Go checks every label (`analysis.ReadLabels`): a label for a span it was not given
  is rejected and noted; a scale must be a power of ten whose source is the exact words of the span's own
  passage, whole words outside the span itself, before it (a heading, a table header, a lead-in) or right
  after its digits or its range's far end (never another passage's words, a span id, or words after the
  figure that follows it), or it is not applied: the figure reads at scale 1, unclear
  (`TestR5ScaleSourceIsItsOwnWords`, `TestR6ScaleWordsAndPlace`); a decimal mark must fit
  the digits; a sign may not contradict a written minus; what it cannot check, or what the reader says is
  unclear, is an unclear reading, and the amount is exact arithmetic on the written digits (reading ×
  scale × sign), so no label makes a figure the text does not state (`TestReadLabelsChecksEveryLabel`,
  `TestR4V4ReaderCannotInventSpansOrFigures`). Three label shapes a real reader gives are read as it
  meant them (bench7, `TestBench7LabelChecks`): a count given a period's unit whose words lie right
  after its digits ("21 months") is that period; money or a rate given the unit of its period ("$2,800
  per month") keeps its amount, the unit dropped (a unit that is no period's is unclear); a scale cited
  from the figure's own trailing zeros ("000" of "$8,238,000"), words its passage states nowhere else, is
  no scale: the digits read as written at scale 1, clear (so a table cell whose "($000)" header lies
  outside the passage reads as written, unscaled; when the passage states the cited zeros elsewhere too,
  a header's "($000)", it is unclear, and any other scale with no words of its passage is still not
  applied and unclear); a decimal mark the label leaves empty or names otherwise is unclear wherever the
  digits read two ways ("1.200"). A count value the reader reads as a period ("18 months") is
  admitted as that period when a quote span states it in its unit. A scaled reading states the same figure as another clearly
  only when the other is read at the same scale from equivalent words (folded for case, punctuation and a
  plural, one side's words all among the other's: "(In Millions)" and "million"; no word is known to be a
  scale); otherwise a match is at best unclear, gated (`SameFigure`; a "$500" read "in thousands" never
  verifies a typed "$500,000" or traces a memo's, and a "$500" scaled from "fee" never verifies a typed
  "$500 thousand": `TestR5ScaleSmuggleReachesMemo`, `TestR6ScaleSmuggleWithAScaledValueReachesMemo`). The reader reads
  each finding's verified quotes (less the numbers inside a locator, "Section 14.2", which G8 masks in a
  memo too), typed values (with their class, unit, entity and metric; a date value only its first number,
  for its party) and claim, and each drafted section's figures its drafter wrote (handle values are Go's),
  in calls of at most 60 spans (its answer, a label of 16 fields a span, must fit the subagent's 8,192-token
  answer cap: bench7's calls of 106 and 123 spans were cut off and failed: `TestReaderCallSpansBounded`) and
  32 KB, the smallest items first, an item over them read in parts (a passage's spans split between them;
  a span over 64 characters or a passage over 32 KB is not sent and stays unread: `TestR5OversizeInputIsSplit`).
  When it reads (bench7: a call a page took 164 of the sweep's 268 calls and the sweep read 103 of 142
  pages): a sweep's visits admit their findings unread and the sweep reads them all together at its end
  from the reader's own share (6% of the calls and 10% of the USD, out of the sweep's 30%: replaying
  bench7's 491 SEC findings through admission takes 17 calls, 87 at a call a page:
  `TestSweepReadsItsFindingsTogether`); a finding that states no typed value is read only when a drafted
  section first cites it, in the section's own call (`Finding.Unread` until then; G8 treats its quotes as
  never read: `TestUncitedFindingReadWhenCited`), or, when its claim states a figure, by `finish`
  before its judge from the judge's share, so the join sees its claim's sums (`TestValuelessClaimSumReachesTheJoin`;
  what that cannot fund is counted in Limitations: `TestUnreadClaimsDisclosed`); a sweep that stops on an
  error drops the values of the findings it never read, each with a note (`TestFailedSweepDropsUncheckedValues`); the lead's own `extract_facts`, `analyze_issue` and
  `check_deviation` admissions read at once, out of a call the subagent's grant holds back for it before
  the subagent runs and the quotes' judges cannot spend (`goTurns`, `TestR5ReaderReservedBeforeDrafting`),
  held only when a finding states a typed value; and `finish` reads first, before its judge spends
  anything, every section whose reading is missing, stale or was unfunded (`TestFinishReadsAStaleSectionFirst`).
  Metered and journaled like any call; P4's class shares count the subagents' turns, the sweep's its visits'
  (its reader's are the `read` share's), and the draft share the lead's drafters' calls, not their
  readers' nor a coverage redraft's, ledger kind `figread`: `TestR6CoverReaderCallsLeaveTheDraftShare`. Every
  reader call that fails is logged with its step and why (cut off or off its schema, out of turns,
  unfunded) and counted, ledger kind `figstats`; a batch that fails (not for want of budget or a model)
  is read again in halves, once, so one failure never drops all its findings (`TestFailedReaderBatchSplitInHalves`); a finding whose read failed stays unread, its values dropped with why, and is read again where it is next needed; the task's cost shows the reader's calls, failures by
  why and failure rate (`TestReaderFailuresCounted`). Its answer schema takes any kind, unit, sign or
  decimal as text (ReadLabels reads one off its words as unclear), so one bad label never sinks a call;
  `figure_context` finds a quote as the ladder matched it (tolerantly: `TestFigureContextFindsATolerantQuote`). Its complete answers (one
  accepted label for every span it was sent, not unclear throughout: `TestR6AllUnclearAnswerIsNotCached`) are cached (`figure_reads`, migrations 0006 and 0007) under a key derived from
  the reader's spec, prompt and tools' specs, the model, the matter and what it was sent (a section's also
  its task), so a run read again gets the same labels and an edited prompt reads again; a row goes with its
  task and its documents (`TestR4V4ReadingIsCachedByContent`, `TestR5CacheStoresOnlyCompleteAnswers`,
  `TestR5CacheKeyAndScope`). A finding it could not read (no budget, a failed call) keeps no reading, and its
  values are dropped with a note. A typed quantity is admitted only when a span of its quotes reads as the same kind,
  currency and exact amount as the value's own span (`valueStated`; a share count is never dollars, one
  quote's table header never scales another's figure: `TestR4V4SharesAreNoSum`,
  `TestR4V4HeaderScaleIsTheReadersPerQuote`, `TestR4V4RangeIsTheReaders`); when either reading is unclear
  but they agree on what can be read, it is admitted at low confidence and its finding is gated for a
  lawyer (`TestR4V4UnclearReadingGates`); its reading (`Value.Fig`) is what `Canon`, the joins, `Calc`'s
  operands and `Salience` read; a value with none is never compared, summed or ranked as a quantity.
  A money value's unit is the currency its figure states. Only an
  exact duplicate of a verified finding merges into it, its dropped values' notes with it (`TestR4V4DuplicateKeepsValueNotes`; `restated`: the same issue, the same quotes in the same documents with case and white
  space folded, the same typed values with their entity and metric, never a breach folded
  into a finding that states none); a near restatement (a reversed term, a mirrored party's clause) is a finding of its own
  (`TestRestatedNeverMergesANegatedTerm`, `TestRestatedNeverMergesMirroredParties`); a sum of money the claim states is
  kept as an untyped value only when the figure reader read the claim's span and a verified quote's span as the same
  money, clearly (`claimSums`; never a share count, never a bare "3" of "Section 3": `TestR4V4SharesAreNoSum`): no
  entity or metric, compared by the reader's measure and party of its claim span. `check_deviation`'s requirement is the controlling document's own
  words: Go verifies it in `requirementDocId` (one of `controllingDocIds`) like any quote before the
  subagent runs and hands the subagent the document's text, with the reviewed documents' passages
  most like it (`reviewedPassages`, ≤ 16 KB, searched by 24 of the requirement's distinct content words, its names, defined terms and numbers first, then its longest, `passageQuery`, under the search's 32, a failed search logged, so it answers at once: `TestDeviationGetsReviewedPassages`;
  its only tool is `retrieve`, as Go verifies every quote it gives): JSON escape artefacts (`\"Tommy\"`) are
  undone, and a requirement joined from several passages (a heading and its list, two sentences from
  two paragraphs) verifies part by part, split where the model joined them (at most 12 joins; its
  ellipses stripped from the parts' edges; one normalised index of the document, the caller's ctx),
  every part the document's own words, the parts in the
  document's order, one passage (§ Limitations, LAB round 3); a part under four words only as a
  heading line before another. The verified requirement is then read in a bounded window of the text around
  it (`requirement.go` `contextOf`; lab-r3 r4, coordinator decision: `docs.Parse`'s heading guesses are
  not reliable enough for a safety check, and a wrapped cross-reference read as a heading split a
  requirement's sentence): the requirement plus about 3 KB of the document before it and 3 KB after it,
  each edge moved to a sentence or line end (outward up to 256 bytes, else inward), at most 8 KB
  whatever Parse makes of the document, so introductions, lead-ins without a colon ("The Trustee shall
  not—"), and overrides or sibling sub-clauses nearby ("5.2 Clause 5.1 shall not apply", "Notwithstanding
  the foregoing …") are in it; the heading path above the window (`docs.Parse`'s sections that hold it) is
  shown as a bracketed label only, never a boundary. The window is marked truncated when the requirement's
  sentence or paragraph starts before it (a paragraph starts after a blank line, or at a line break after
  a sentence or clause end before a capital, a quote or bracket or a list marker, so a `.docx`'s
  one-line-break paragraphs count: `TestV5DocxParagraphNotTruncated`), when its first line is a list item
  (the list's "shall not:" introduction may be above it: `TestV5LongListChapeauConfirmed`), or when an
  edge found no sentence or line end and cuts a sentence
  (`TestContextIsTheTextAroundIt`, `TestContextTruncatedWhenTheSentenceStartsBefore`,
  `TestV4WrappedLineSplitsTheSentence`, `TestV4DecimalSiblingsAndChapeau`, `TestV4ContextBoundHolds`). The
  subagent judges against that text (`requirementContext`, the requirement marked ⟦ ⟧), its verdict's
  controlling quote must lie in it (else the verdict is dropped). Before the subagent runs, out of the
  call's grant first (so its cost is never left unfunded behind the verdicts), one bounded call on the
  `critic` role (`rt.ChatWithin`), with the window fenced as document text and nothing else (no verdict,
  correction or reviewed document: bench5 trust, handed the analyst's correction the check judged it and
  dropped the real incapacity-standard deviation), answers whether the controlling document actually
  imposes the requirement as quoted, not negated or overridden (`checkSection`, prompt
  `specs/section_check.md`; `TestSectionCheckAsksAboutTheRequirement`). Whether a reviewed document
  complies is the subagent's question, never the check's. Its answer is read from one strict JSON
  object only: exactly one `answer` key, spelled so, `yes`/`no`/`unclear` in any case, a trailing "." or "!" aside ("No."), any other
  wording ("Yes, but …") unclear; a code fence around it, its tag in any case, is allowed (`TestCheckAnswerStrict`).
  No (negated or overridden) fails the call before the subagent runs, with the checker's reason, so the
  lead quotes the requirement with what governs it; yes admits each verdict, the claim saying "An
  independent check read the surrounding text (N bytes) and found that it imposes the requirement as
  quoted", with the checker's reasoning labelled "not a quotation"; unclear ("could not tell"), or a yes
  on a truncated window, admits it at low confidence, labelled
  for a lawyer's review and gated at finish like a low-confidence transcript (`lowConfidence`); a check
  not had (the grant could not fund it, its call failed, its answer was malformed) admits the verdicts at
  low confidence and gated too, never drops them (`TestSectionCheckNotHadAdmitsLowGated`; bench5 trust
  dropped three real deviations as unfunded). The finding quotes the window verbatim as its controlling citation, the requirement
  marked, with the part and the correction given as the analyst's words, not quotations; prosecute checks
  them against it (`TestV3BlankSeparatedListLosesIntroduction`, `TestV3OverrideBelowNotInContext`,
  `TestSectionCheckNegatedDropsVerdict`, `TestSectionCheckYesAdmitsBenchRequirements`,
  `TestSectionCheckUnclearOrCutIsLow`, `TestSectionCheckChargedWithinGrant`, `TestR2VProbesReadInContext`,
  `TestR2VFragmentsQuotedInContext`, `TestR2VVerdictQuoteOutsideContextRefused`, `TestOnePartFragmentReadInContext`,
  `TestBenchRequirementsReadInContext`, `TestContextIsBounded`). Format characters (zero-width spaces,
  bidi marks) are folded before any match; the parts handed on joined
  by " … " (`TestRequirementPartsVerify`, `TestDevMultiPartRequirementRuns`, `TestStitchedRequirementRefused`,
  `TestEllipsisJoinVerifies`, `TestStitchedRequirementMakesNoDeviation`); a paraphrase or a stitched
  requirement is refused, before any model call, with the nearest exact passage (its whole sentences
  or lines when they fit in 300 bytes, else at most 300 bytes; fenced as document data) to copy
  (`TestDevParaphrasedRequirementRefused`). It keeps a
  deviation only when both sides' quotes verify (a verdict that restated the requirement, its words
  and no figure of its own, stands on the verified requirement: `TestDevRestatedControllingQuoteRescued`),
  and an omission only when the provision does not verify in the reviewed document: string match,
  then the paraphrase judge on its closest passage, with the passage where it belongs quoted beside
  it (`TestDevRealOmissionsKept`; with no judge call left, fewer than half its significant words in
  the document). The subagent's reward for the bandit is the share of its quotes that verified.
- **`sweep`** visits every section of the task's documents (or one document) once, calling
  `extract_facts` on each page of a top-level section, the page alone (no tools offered: one call a
  visit, where a visit that searched with `retrieve` took about three), most relevant first: each
  plan issue's pages ranked by the BM25 score of its question, taken in turn across the issues, then
  the pages no issue's words reach, in document order (`TestSweepReadsRelevantPagesFirst`); a page
  that is an exact copy of an earlier read one (case and white space folded, or the same table rows
  in another order; across documents only a page of 40 words or more) or is pure boilerplate (no
  letter or digit; a table of contents: its Contents heading over entries, or dot-leadered entries with
  heading-like titles and page numbers that never fall, no table row, no sentence; a tab or space-aligned
  list of figures is read: `TestR2VRealTablesNotBoilerplate`) is skipped and listed under `skipped`
  (`TestSweepSkipsDuplicatesAndBoilerplate`, `TestDataTableIsNotBoilerplate`, `TestDistinctPagesAreRead`);
  a copy of a page the sweep did not read counts unread (`TestSkipOfUnreadPageIsUnread`), and the
  skipped pages are a Limitations line (`TestSkippedPagesReachLimitations`); each document's coverage (planned, read, skipped) is
  kept in the ledger (`swept`) and its partly unread documents are reported (`unreadDocuments`, also
  in `join_discrepancies`' output and in Limitations); every read is sized by the answer,
  not the page (`sweepTokens`, 600: a subagent writes five to seven tokens of findings per token of
  dense text, so a 2,000-token page overflows its 8,192-token answer): a long section is read in
  600-token pages (a table in pages of at most 25 rows, its header row by reference:
  `TestSweepPagesFitTheAnswer`; a lead's direct `extract_facts` on a section reads the same pages),
  and consecutive top-level sections shorter than 600 tokens are batched into one visit of at most
  600 (`through`): a document numbered by paragraph is read a few paragraphs at a
  time, not one subagent run per paragraph (`TestSweepBatchesShortSections`); a `through` that is
  not such a run from `sectionId` (with offset 0) fails the call, never reads `sectionId` alone
  (`TestExtractFactsRefusesABadThrough`); a visit is keyed (document, section, offset[, through]) with the plan's issues it was read for, and is
  repeated only for issues `op plan` added since, sent and admitting only those (`TestIssueAddedAfterSweepGetsItsFacts`); a page
  no sweep read again for them is a Limitations line (`TestIssueAddedAfterSweepNeverReadAgainIsDisclosed`);
  it stops at its share of the budget and reports `incomplete` with the sections it did not reach
  (a batch as its whole run, "s3 to s9": `TestSweepUncoveredNamesTheWholeBatch`),
  which also go into Limitations. Never silently partial. It reads up to `sweepWorkers` (4) visits at
  once and is still deterministic: a visit starts with a full grant (the subagent's turns and a
  quarter of the sweep's USD) while one is free, else only once nothing is in flight, with what is
  left, so no grant depends on timing and the shares hold exactly as one at a time; findings are
  admitted (fids, exact-duplicate merges) and visits recorded in reading order (each visit waits, after
  its subagent ran, for the one before it); a visit a worker's quarter could not fund (a call's
  worst case over it: a pricier model on a small budget) is read again once nothing is in flight,
  one at a time with all that is left, so parallelism never reads less than a one-at-a-time sweep
  (`TestSweepFallsBackToSerialWhenAShareCannotFundACall`), and `visited` counts only visits that
  made a model call; its model is the bandit's one pick for the sweep
  (`agent.Call.Model`); a model or journal failure cancels the rest (`TestSweepReadsInParallelInPlanOrder`).
  The sweep is one step: a process stopped mid-sweep commits none of it and the next re-issues it
  (`TestSweepResumesAfterKillMidSweep`). Its output passes its schema also when incomplete (a
  batch's `through` is a field: `TestSweepUncoveredNamesTheWholeBatch`).
- **Drafting** (`draft_section`, one issue per call): Go gives the drafter the issue, its named
  verified findings with their quotes (each naming its document, so the drafter can say which
  document states what: `TestDraftQuotesNameTheirDocument`), their figure handles (`{Zephyr}`), a `{W…}` handle per
  `numbers` worksheet (labelled with what it computes; rendered as a date in words, `$214,340`,
  `35%` or `14 days`), the summaries of sections already written, in plan order, and on a redraft
  the `checks` (lint lines) its current version fails, and the issue's latest `analyze_issue` answer
  (`analysis`: rule, application, conclusion). The section is stored with the run's handle set at
  that point, a citation group (`[F3, F7]`, `[F3.v1]`) split into single citations.
- **`committee`**: three context-free critics in parallel, each on its own explicit grant (the
  cycle's 8/8/4 calls, the last taking the USD remainder): `prosecute` gets the claims and their
  quotes, `reproduce` gets the issue questions only (never the draft) and its answers are compared
  with the draft, `audit_gaps` gets the plan, the drafted headings (not their text) and the packs'
  checklist. Their findings are HIGH/MEDIUM/LOW, open until the
  lead resolves them; a `prosecute` verdict credits or debits the arm that produced the finding
  (delayed credit, ±0.5). A critic that fails leaves a MEDIUM "did not run" finding.
- **Policy** (`policy.go`): P1 nothing but `retrieve`/`ledger` before a plan, and if the lead has
  spent its plan share (7%, at least 3 calls) without one, the packs' checklist becomes the plan
  (disclosed); P4 class shares, each on calls **and** USD (evidence 45% of which sweep 24% of the
  calls and 20% of the USD, its figure reader 6% and 10%, and the coverage pass 10%, kept for it, so the lead's own evidence calls may spend 35%, at least 5% beside a
  full sweep; draft
  15%, committee cycles of 20 calls or 20% of a small budget with a fifth of the USD, a reserve of 5
  and meta 3 only `finish` spends). The root budget keeps back the unspent drafting share from
  evidence, one committee cycle and (until it has run) the coverage pass from everything below the
  committee (lead turns included), and the
  reserve from all but `finish`, so evidence can never spend the draft's budget and the last draft can
  always be reviewed: when the lead runs out with a draft no committee has read, Go runs the
  committee on it from that cycle before the forced finish (`TestPhaseSharesHoldInUSD`,
  `TestCommitteeAlwaysReviewsTheLastDraft`). The lead runs at most three committee cycles (`maxCycles`; bench6 SEC ran
  nine, $4.16, while verified findings stayed uncited): a fourth is refused with what to do instead, finish no longer asks
  for a review of a draft changed after the capped cycles (a Limitations line says so, naming the critic findings resolved
  as fixed after the last cycle: `TestR4V4UnreviewedFixesNamed`), and the coverage pass, whose own
  re-review of its redrafts is beyond the cap, may spend half of what is free beside one more cycle and the reserve when
  that is more than its share, placing left-out findings in up to three rounds of one redraft per issue, at most 16 each,
  60 per round, a request naming at most 100 findings, the section's own citations and its conflicts' findings never cut
  (a section citing more is not redrafted: `TestR4V4PlacementKeepsCitations`; `TestBench6SECCommitteeCapped`). The cycle it keeps is a fifth of the USD until a cycle
  has run, then 1.5 times the costliest cycle so far (`cycleKeep`; bench5's cycles cost $0.30–0.48 of
  the $2.20 kept). `sweep` stops at its USD share as at its calls. The shares are sized from bench5 at
  $11 (lab-r4): a visit cost $0.012–0.014, so the 142-page SEC set needs about $2 of sweep (18%; 30%
  leaves room for half as many pages again); the coverage pass spent about $0.15 an issue on an analysis
  and its redraft, and at 6% had nothing left to place a finding; a SEC-shaped scripted run at bench
  costs per call (`TestSECShapedRunAnalysesAndPlacesWithin11`: 142 pages, six issues, a lead that loops
  drafting and the committee until the budget stops it) analyses every law issue, places the left-out
  findings and spends under $11.
- **`finish`** (`op: "check"` is a dry run of `submit`): lint (every `[F-id]` names a usable
  finding; every number and date in the draft is a handle, a `numbers` worksheet's result or one of
  its inputs, or inside a cited finding's quote or value (G8, the "68.6% for 81.6%" trap; a deadline's
  raw date before a `date_add` worksheet rolled it past a weekend or holiday traces only in a
  paragraph that states the rolled date, and its lint names the rolled `{W…}` to write, so a drafter's
  own unrolled `date_add` cannot put Veterans Day in the memo as the deadline:
  `TestRawDeadlineNeedsItsRoll`), rule and
  section numbers (`Rule 6(a)(1)(C)`, `Section 4.3`, `§ 9.2`) being locators, not figures, also in
  lists and ranges (`Sections 7.3 and 9.1`, `§§ 4.2–4.5`, `clauses 3(a) and 3(b)`, `paragraphs 12,
  14 and 17`; a figure after a list still counts: `TestFigureLintSkipsLocatorLists`): a list ends at
its last locator, and goes on over "and", "," or "–" only to a number that is itself locator-shaped
(`9.1`, `206(2)`) or followed by punctuation, the end or a word a locator takes ("of", "are"), never
to one followed by a noun or unit or after a "," or ";" once "and"/"or" closed the list
("paragraphs 12 and 14, 37 trades" traces 37: `TestFigureLintTracesCountAfterLocatorList`); each
  untraced figure says how to fix it: the handle to write and the finding to cite, the uncited
  finding that states it, or the `numbers` call that computes it (`TestLintSaysHowToFix`); no
  unknown handle, and none inside a quotation (Go never substitutes there, so the quote would show
  `{Heron}`). A memo stating a figure a worksheet computes (over cited values or typed inputs), by
  its `{W…}` handle or written out, lists it in Limitations' Computed figures block, with what it
  computes from what. After two `finish` calls that reported lint problems, the next discloses them in
  Limitations ("Delivered with an unresolved check after 2 rounds of fixes") instead of refusing,
  so redrafting for the lint is bounded (`TestFigureLintIsBounded`)), coverage (plan issues
  without a section, parties never named, salient figures never landing), and P6: a committee on
  the current draft version, its HIGH/MEDIUM findings resolved while the budget can fund another
  fix (3 calls and their USD beside the kept cycle and reserve), every plan issue drafted or disclosed by issue id.
  **Coverage pass** (`cover.go`, once per run, before the first deliverable): a plan issue is only
  *touched* when no drafted section cites a usable finding for it, or when it is a Conduct or
  Requirement issue (what the law makes of facts) with no analysis (an `analyze_issue` answer, or a
  usable `check_deviation` finding of it). For each touched issue, in plan order, Go runs
  `analyze_issue` and redrafts the section from the issue's findings and that analysis, from the
  share kept for it; then the committee reviews the changed draft (inside a passing `submit`; a run
  finishing from its reserve runs the pass as a step before its last review), and each issue still
  touched is disclosed in Limitations ("Coverage: I6 (…) was not analysed: …";
  `TestCoveragePassFromTheReserve`, `TestCoveragePassOnSubmit`). The pass also places what the
  record proves but the memo left out: each salient verified finding no section cites (a
  `check_deviation` finding, one rated high, one with a breach relation, one stating a salient figure,
  or one whose claim says a document deviates, omits, fails to or does not; at most 40) goes to its
  issue, else the issue whose question its claim matches best, and each cross-document figure
  conflict the memo does not state both sides of goes to the issue citing either finding; each such
  issue gets one redraft with them, the committee reviews it, and what is still left out is
  disclosed ("Coverage: N verified findings … never reached the memo"; the parties and the salient
  figures the memo never names are one line each, at most 12 listed: `TestCoverageLinesSummarised`). Every figure conflict the
  memo does not address is also a `finish` lint line ("memo: the documents state … differently"),
  bounded and disclosed like G8's (`TestCoverageDraftsLeftOutEvidence`). `finish`'s grant holds its meta
  call plus the pass's and one review's calls (so a crash mid-finish loses no more), the pass's own
step (from the reserve) holds only its share's calls and USD (`TestCoverStepGrantIsItsShare`), a
pass a model failure ends is still recorded as run, so a retry does not spend a second share on it
(`TestCoveragePassRecordedWhenAModelFails`), and P6/P7's
  "funded" counts them, and the pass's USD until it has run, as kept (`TestCoverageShareKept`). P7: a `submit` within 2
  calls of a committee whose HIGH/MEDIUM findings were resolved without new work is refused once
  per cycle, while funded, with the instruction to fix properly and run the committee again. `submit` then renders: sections in plan order (summaries never), handles substituted
  outside quotes only, process language and orphan fragments stripped, repeated blocks dropped,
  external authorities (cases, statutes the record does not contain) marked inline and listed for
  verification (G5), one-line Sources (a transcript citation badged "machine transcription (model)"
  and linked to the original, G11), Limitations, an Evidence Note (verified vs quarantined,
  and verified findings a lawyer rejected at a gate; transcript-only and low-confidence counts; a
  grounding alert raised during the run) and one meta call's note to the reviewing lawyer. Text from
  documents and models is neutralised where the memo prints it (`mdSafe`): one-line slots (Sources,
  headings, list items) collapse white space; sections and the note keep their lines with `#`/`##`
  demoted to `###`; `<` and fence openers are backslash-escaped. A quote cannot open a section.
- **Gates** (G7): every deliverable opens a gate (`G-deliverable-N`); a cited finding gets its own
  (`G-<fid>`) when it rests only on low-confidence transcript pages, when it was admitted at low
  confidence (`Finding.Low`: an unclear figure reading, a deviation its section check could not
  confirm, a limited memo figure or ⚠ worksheet input, a `finding_admitted` approve rule), or when
  an open HIGH `prosecute` finding says it is unsupported (`finish.gates`, `gated`). The run stops `awaiting_gate`; approving
  the deliverable completes the task; rejecting it (a reason is required) puts the reason in the
  lead's log as a note step and the run resumes. Once all finding gates are decided the run resumes
  with the decisions; a rejected finding leaves the memo.
- **Grounding collapse** (G4): from 8 findings on, if more than `BIGLAW_COLLAPSE_THRESHOLD` of them
  are quarantined the task fails (`strict`, default) with a grounding alert, or (`warn`) carries the
  alert into the Evidence Note.
- **Model failures** (G6) end the run `failed` with the kind's fixed text (`model_rate_limited`,
  …), never a finding; of the endpoint's words only an error answer's type (or code) and message, the
  whole of it redacted (the key; a key prefix and its fragment; any run of 32 or more key or base64
  characters; a masked key's tail; a bearer, basic, `key=` or `Authorization:` value; a URL; an IPv4
  address; a host name ending in a top-level-domain shape, never a dotted path such as
  `messages.0.content`) and then cut to 240 bytes ("the model endpoint failed: HTTP 400 (invalid_request_error:
  Your credit balance is too low …)", bench7; `TestProviderErrorTypeAndMessage`,
  `TestProviderErrorRedactsTypeCodeTailsAndHosts`, `TestRedactKeepsPathsHidesCredentials`).

**Durable runs.** Each lead turn and tool call is a step in `task_steps`: recorded `started`
(with its grant) before it runs and `committed`/`refused`/`failed` after, together with the
ledger rows and bandit statistics it wrote, in one transaction. A process claims a task with a
lease (30 s, renewed every 10 s; `FOR UPDATE SKIP LOCKED`) and runs at most 4 at once; a stale
lease (a crashed process) is taken over. On resume the committed steps are replayed from the log
(their outputs reused, no model call repeated) and a step that was in flight is charged at its
grant (`lost`, shown in the cost ledger) and issued again; a process that is stopped (SIGTERM, a
deploy) first settles an in-flight tool step's grant to what the tool spent, its aborted model
call at its worst case, so the resume is charged that rather than the tool's whole grant (every
free dollar of the task; `TestResumeAfterStopMidToolChargesWhatItSpent`). A process that dies
without a word (kill -9, a container restart) cannot settle anything, so each model call inside a
tool's step is journaled as it happens: before it is sent its worst case is added to the started
step's grant, and when it returns it is replaced by what it spent; the resume charges that, the spend
so far plus the worst case of the calls in flight (at most one per sweep worker), not the tool's
whole grant (`TestHardCrashChargesOnlyRecordedSpend`; a tool that has made no model call yet keeps
its whole grant). That holds for every model call inside the step: a committee's three parallel
critics' turns, and the query embeddings their (or any subagent's) `retrieve` makes (`agent.Meter`:
`TestKillDuringCommitteeChargesMeteredSpend`; bench5 trust's $0.5078 lost at a restart mid-committee
was its 15 metered calls, $0.205, plus two critic calls in flight at their worst case, about $0.15 each,
not the committee's grant). A task whose document
was deleted after submission fails (`bad_request`) instead of being claimed again forever. A run stops `finished` (gate open),
`budget_exhausted` or `max_steps` (a draft changed since its last review gets one committee from the
kept cycle, then `finish` is forced from the reserve, open findings disclosed; `maxSteps` defaults to
twice the call budget, at least 400),
`awaiting_gate`, `cancelled` or `failed`. Tool-bandit statistics persist in `tool_stats`.

**Packs: jurisdiction and practice guidance** (`internal/engine/packs/{jurisdiction,practice}/*.md`, embedded; an admin may override one, § Manage): `us`, `us-ny`,
`litigation`, `corporate-ma`, front matter plus a checklist, all `candidate` (no named lawyer has
reviewed them, which every deliverable's Limitations says). A task's jurisdiction selects packs by
prefix (`US-NY` → `us`, `us-ny`). The full catalogue is PR 12's.

**REST API** (stdlib `net/http`; JSON; lists never `null`; errors `{error, errorType}`; shapes in
DESIGN § UI contract):

```
GET    /health
POST   /documents                     {name, text, matterNumber?} → Document (text only; files: /documents/upload)
GET    /documents[?matter=N]          GET /documents/{id}   DELETE /documents/{id}
GET    /documents/{id}/outline        GET /documents/{id}/sections/{sectionId}?offset=&limit=
GET    /documents/search?q=&ids=a,b   POST /documents/upload   GET /documents/{id}/file
GET    /clients                       matters derived from documents' matter numbers (clients: PR 8)
POST   /tasks                         {description, workflowType, documentIds[], jurisdiction?, matterNumber?, maxSteps?, toolBandit?: on|off} → 201 Task
GET    /tasks   GET /tasks/{id}       Task with run {status, step, stopReason, budget, openFindings}, gates, findings, output
GET    /tasks/{id}/steps?after=N      the step log, paged
GET    /tasks/{id}/events             SSE: step (each once, in order), task (run changed), done
GET    /tasks/{id}/cost               per-step usage and USD (null when unpriced), lost grants
GET    /tasks/{id}/deliverable.md     the rendered memo
POST   /tasks/{id}/cancel
POST   /tasks/{id}/gates/{gateId}/approve      POST /tasks/{id}/gates/{gateId}/reject {"reason": "…"}
GET    /me                            {id, name, email, admin, authEnabled}: the signed-in user, or without sign-in the local user
GET    /auth/login?next=   GET /auth/callback   POST /auth/logout     sign-in (§ Sign-in; only with BIGLAW_OIDC_ISSUER)
POST   /manage/sessions/revoke {user}   (admin, sign-in only) end every session of a sub or an email → {revoked}
GET    /manage/items                  the catalogue (§ Manage)   GET /manage/items/{id}: with its files now and built in
GET    /manage/items/{id}/history     its versions, each with its files    GET /manage/items/{id}/diff?from=&to= (a number or builtin)
POST   /manage/items/{id}/versions    {base, files, note} → 201 (admin; 422 invalid with problems[], 409 stale base)
POST   /manage/items/{id}/revert      {to, base, note} (admin; to 0: the built-in)   POST /manage/items/{id}/delete {base, note} (admin; skills)
GET    /manage/changes?limit=         the change log    GET /manage/export?format=patch|zip&item=a,b (the overrides at their repo paths)
GET    /manage/secrets                names, the origin each is sent to, who set them, when, used by (never a value)
POST   /manage/secrets {name, value, origin}  (admin; 403 without a real admin)   POST /manage/secrets/{name}/delete (admin)
POST   /manage/items/{server}/discover   (admin) an MCP server's tools   POST /manage/items/{server}/import {tools, offerToLead} (admin)
POST   /manage/items/{id}/test        {documentIds, input} (admin): one call of a subagent or tool, metered
GET    /                              the web UI (embedded by `make ui build`), or a notice in a build without it
```

Model failures map to `model_not_configured` (503), `model_rate_limited` (503), `model_timeout`
(504), `no_content` (422), `model_unavailable` (502), a cancelled request to 499; a grounding
collapse is 422 `grounding_collapse`; an unknown task 404, a gate that is not open 409; a
cross-site write 403 `cross_origin`, a request by DNS name 403 `bad_host` (§ Exposure); under sign-in, no
session 401 `unauthenticated` (§ Sign-in).

**Run it against a model** (owner; no key exists in the build environment):

```bash
BIGLAW_DATABASE_URL=… BIGLAW_MODEL_URL=https://…/v1 BIGLAW_MODEL_KEY=… BIGLAW_MODELS='lead=<C>' \
  ./biglaw serve                                    # loopback :3101; make ui build first to embed the UI
python3 eval/run.py drive --api http://127.0.0.1:3101 --model-dir v2-<C> --matter litigation --matter ma-diligence
python3 eval/run.py compare eval/baselines/v1.json eval/results   # gate A (needs gate 0's baseline)
python3 eval/lab/run.py --labs-dir ~/harvey-labs --api http://127.0.0.1:3101 --model-dir v2-<C> \
  --task corporate-ma/review-data-room-red-flag-review [--tool-bandit on|off]
```

`eval/` (Python 3, stdlib only): `run.py` (`list`, `validate`, `selftest`, `score`, `compare`,
`drive`), `proofrun/` (driver, scorer, mock), `lab/` (the Harvey LAB driver over this API),
`tests/`. `make eval` runs E0 and E3 (fixtures, scorer tests, scorer self-test; no backend). In
`go test ./eval`, `TestProofRunsOracle` (E1: an oracle `model.Fake` per matter through the real
engine and API, scored by the Python scorer: 1.0, verified rate 1.0; its drafter cites another item's
finding or a document line (sharing the memo's words) that states a figure as the memo reads it, and
computes the rest only from cited figures with their units (sums, differences, products of up to three
with one money factor, percentages, days between cited dates, a cited date shifted by a cited period,
rolled past a cited holiday); the lawyer approves each gate. A figure it can neither cite nor compute is
flagged by G8 and, after its rounds, delivered as an unresolved check, pinned per matter as a ratchet
(`unresolvedG8`: 4 across the five matters, from 26 at the r7 review and 5 at r8: two bare figures the
stand-in reads as counts, two code sections written without "Section"; and the findings it gates for the
lawyer, `findingGates`: 4, scale-limited figures), both ratchets),
`TestProofRunsFabricator`
(E2: every matter fails with a grounding alert) and `TestLabRunnerSmoke` (the LAB driver against an
`httptest` server) run with no model.

## Manage: agents, tools, packs, skills and hooks (`internal/engine` specset/manage/skills/hooks, `internal/api/manage.go`)

Everything a run uses is a file built into the binary (go:embed), and an admin can see and change
each one in Manage (`#/manage`) or the routes above, with no rebuild:

- **Agents**: the lead's prompt (`specs/lead.md`), the meta and section-check prompts, the subagents
  (`specs/agents/<name>.json` + `.md`: description, `maxTurns`, budget `class`, model `role`, `allowed`
  tools, `in`/`out` schemas, prompt) and the engine's passes (`sweep`, `committee`, `cover`, `finish`,
  `ledger` specs). **Tools**: the typed tool specs of `internal/analysis/specs`, `internal/docs/specs` and
  `internal/engine/specs/{exact_calc,figure_context,use_skill}.json`; their code stays Go.
  **Packs**: jurisdiction and practice guidance (§ Memo workflow). **Skills**: below. **Hooks**:
  configurable hooks (below), and the Go around some tools and subagents (`hooked`, `sheets`, `aliasJoin` in `register.go`), view only: what
  each does, the tool it wraps and its source, cut with `go/parser` from the engine files embedded
  (`hooks.go`). Each item shows its contents, built-in or override (its version), what it calls and
  who calls it (agents' allowed lists, the lead's list, and the Go callers), and its recent usage: lead
  steps, model calls and USD over the last 20 tasks (`task_steps`; a fan-out's own calls count under it).
- **Versions** (`spec_versions`, migration 0008): every save is a new version of the item holding all
  its files, who (`author`), when, an optional note and the version it replaced; a revert (to any
  version, or to the built-in: files NULL) and a skill's deletion (files `{}`, a tombstone) are versions
  too, so the table is the history and the **change log**. A save names the version it was edited from
  (`base`): a stale one is 409. A save holds files: one with none, which would delete the item, is
  refused for every built-in (with Delete's reason where Delete refuses too), and a request body is
  exactly one JSON object (400 otherwise); a revert to a version that deleted such an item is refused
  the same way (History marks it unrevertable, with why). A stored version that no longer loads is listed
  as broken (why, and what is stored) and reverts like any other; until it is reverted every other write
  is a 422 naming it and every new task fails naming it. A prompt whose file a version removed is listed
  as deleted. Diffs are a line LCS (Go `diff.go` for the API and the export, the UI's
  `components/diff.ts` for its views and unsaved edits).
- **Snapshot.** `Submit` stores the overrides in effect (each item's latest version, unless it is the
  built-in again) in `tasks.specs`, in the statement that stores the task. `execute` builds the run's
  spec set from it (`newSpecs`: the built-ins with those files over them) on every claim, a resume after
  a crash included, and carries it in the run (`run.specs`) and its context (`withSpecs`): every spec,
  prompt, pack and skill a run reads goes through it (`spec(ctx, …)`, `prompt(ctx, …)`, `packsFor`), and
  the registry is built from it (docs and analysis tools re-registered with their override's spec,
  `respec`). A save reaches the next task only (`TestSnapshotAtStartAndResume`). A binary upgrade still
  changes the built-ins under a resumed run, as it changes all Go code.
- **Cache keys.** The figure reader's and judge's cached answers are keyed by the hash of their spec,
  prompt and the spec of every tool their allowed list names, in the run's set (`Specs.agentHash`), so
  an override of any of them, or a tool added to the list, reads and judges again
  (`TestOverrideChangesCacheKeys`, `TestAgentHashCoversAllowedTools`); the built-ins hash as before,
  so existing rows stay valid.
- **Validation** (`validate`, every save and revert, all problems at once, 422 with `problems[]` by
  file): UTF-8; a spec is one JSON object of `agent.Spec`'s fields (unknown fields refused), keeps its
  name, kind and `final`, has `maxTurns` 1–32 (subagent) or 0–10000 (Go tool), a known class, its
  schemas in `agent/schema.go`'s subset and strict-shaped; a prompt is 1 byte–64 KiB; a pack's front
  matter is `code` (its file's), `status` candidate|reviewed (reviewed needs `reviewedBy`) and the
  checklist; then the whole set as a task would load it: `newSpecs` and the runtime's own registration
  (descriptions, examples, allowed tools are registered Go tools, roles, the 48/24 caps, the lead's
  list). A description is at most 4 KiB with no control or format characters but line breaks. An item's files are exactly its own (no file added to or dropped from a built-in item). No
  subagent's allowed list may name a tool that is the lead's own: a Final tool (`finish`: it ends the
  run) or one with ops that must stand alone in a lead turn, except `ledger`, which refuses its writes
  (`plan`, `resolve`) to a subagent in its own code and is in the built-ins' lists for its reads; the
  fan-outs (`sweep`, `committee`, `cover`) are not Go tools, so the registry already refuses them. Class
  `free` (no class share) is kept for the subagents built free (the figure reader and judge, metered
  from their own shares); any other subagent must take a class with a share.
- **Serial saves.** A save, revert or delete runs in one transaction under an advisory lock
  (`locked`): it reads the set, validates the set it would make, and writes, so two saves of different
  items can never make a set that does not load (`TestSavesAreSerial`).
- **Compatibility** (`compatSpec`). Go code depends on schema shapes, so a schema save must keep, at
  every path the built-in schema has (nested and in `items`), the property and its type list (Go
  decodes or writes it by that shape: removing, renaming or retyping is refused naming the path, e.g.
  `out.findings[].claim: Go reads or writes this field`); every enum keeps exactly the built-in's values
  (Go branches on them: a critic's verdict, a gap's severity, an op; renaming, recasing, adding or
  removing one is refused, `out.verdicts[].verdict: Go branches on this field's values`); a schema Go writes (a Go tool's output, the
  input of a tool Go itself calls: `goWritten`, found from the engine's `rt.Invoke` calls) gains no
  property (strict schemas require every property, and Go sends none); the built-in description's
  examples and Go's own literal inputs (`goInputs`: finish's submit, `committee {}`, `cover {}`) must
  still validate. **Limits:** it compares with the built-in, not with a list of the fields each Go
  line reads, so a field Go never reads is protected too (an enum Go never branches on is frozen as
  well); narrowing a bound (a length, an item count) on a value Go writes is caught only where an
  example or a literal input shows it; a field added to a schema the
  model writes is accepted and ignored by Go; semantics (what a field means) are not checked.
- **The lawyer-review rule.** Everything in a pack or skill is its content: every file (a skill's
  reference files too), every front matter field but `status` and `reviewedBy` (a skill's description
  and tools, a pack's checklist) and the body. A save that changes any of it resets the status to
  candidate and clears `reviewedBy`, unless the save names a reviewer who is not the one before; a new
  item (a skill created, or created again after its deletion) is candidate unless it names its
  reviewer. The response says so (`notes`). A status-only edit (a lawyer recording a review) is kept.
- **Who.** `api.Principal` is the seam. With sign-in (§ Sign-in) it is the session's user
  (`api.SessionUser`, put on the request by `api.RequireSignIn`): an admin when `BIGLAW_ADMINS` names
  them, a member otherwise, and every author field records them (`Principal.Label`: `Name <email>`).
  Without sign-in every request is the local user (`api.LocalUser`, recorded as "Local user"). It is
  an admin on a server bound to loopback for local work (no `--allow-no-auth`); under
  `--allow-no-auth` Manage is **read-only** (everyone may view it), unless the operator also passes
  `serve --admin-without-auth`, which makes anyone who can reach the server an admin: serve logs a
  warning and the Manage screen shows a banner saying so (owner decision, review M3; § Exposure).
  Every write wears `docs.Guard` (cross-site 403) and the admin guard (403 `forbidden` for a
  non-admin, telling a member they are one: `TestManageAdminGuard` drives it through the seam,
  `TestSignedInManageRoles` with real sessions over every write route); reads are open to anyone
  who may use BigLaw (under sign-in: anyone signed in).
- **Export** (`GET /manage/export`): the overridden items (all, or `?item=`) as one git-style patch
  against the repository's paths (`git apply` or `patch -p1` at the root; new and deleted files as
  `/dev/null`; `TestExportPatchApplies` applies it), or a zip of the files at their repo paths (with
  `DELETED.txt` naming a deleted skill's files). The server holds no GitHub credentials: the owner
  opens the pull request from the export, which then makes `ui/src/recorded/manage.json` stale
  (re-record with `make demo-record`; `TestManageRecording` needs no database).

**Custom agents and tools** (`custom.go`, `remote.go`; migration 0009). An admin creates, edits,
tests, disables and deletes them in Manage; like every item they are versions and in each task's
snapshot.
- **Subagents** (`specs/agents/<name>.json` + `.md`, the built-ins' format): name, description, prompt,
  class, `maxTurns`, role (the model it runs on), allowed tools, in/out schemas, all validated as the
  built-ins are. The lead is offered every subagent Go does not run itself (`leadVisible`); it runs
  through `Runtime.Invoke` with the same typed-schema enforcement, metering, journal and resume.
  Subagents never call subagents (the runtime's rule), so "an agent's allowed list" can name custom
  *tools*, not custom subagents. A custom subagent's list names no lead-only tool, `ledger` included
  (only the built-ins keep its reads); `use_skill` it may name (decided: a skill is read-only guidance
  an admin wrote, recorded as used, and the same for any agent the admin gives it to). The names
  `lead`, `meta`, `manage_test` and every pass's are reserved.
- **MCP tools** (`specs/servers/<name>.json`: the server's Streamable HTTP URL and a secret by name;
  `specs/tools/<name>.json` kind `mcp`): **Import tools** connects (initialize with a session id,
  `notifications/initialized`, `tools/list` with its cursor; an SSE or JSON answer), converts each
  input schema to BigLaw's strict subset (`convertSchema`: optional fields nullable and sent only when
  not null; `anyOf`/`$ref`/open objects refused with the reason) and saves each as a version. The
  server must speak protocol 2025-06-18 or 2025-03-26; every session is ended (HTTP DELETE). A call is
  one handshake plus `tools/call`, within one 30 s deadline for all of it (Discover/Import too).
- **Imported descriptions and schemas are untrusted.** The converted input schema is third-party text
  too: each property description is cleaned and capped at 200 characters, an enum value or property
  name that is long or holds control or format characters is refused, and the schema is capped at
  16 KiB; a re-import that changes it disables the tool until an admin accepts it again, with the diff.
  The server's text for a tool is cleaned (control and format
  characters and line breaks become spaces, so it can add no line of its own) and capped at 2 KB, kept
  in the file's `imported.text`, and shown to the lead only quoted inside BigLaw's own sentences as
  third-party text. An imported tool is disabled until an admin reads and accepts that text (Edit:
  Accept and enable; the save records the saving admin as `acceptedBy`, whatever the file says), and is
  disabled again whenever a re-import (or a hand edit) changes it; Import shows the change as a diff. A
  tool whose name, description or schema holds the server's secret is not importable.
- **HTTP tools** (kind `http`): a typed input to an https URL, as a JSON body (POST) or query
  parameters (GET), with a secret in one header.
- **Rules for both:** https only, on port 443 or 8443; no user info in a URL; a host that is never
  public (localhost, `*.local`, `*.localdomain`, `*.internal`, a numeric spelling such as `2130706433`,
  and any address the IANA special-purpose registries mark not globally reachable: private, loopback,
  link-local, CGNAT, metadata, documentation, benchmarking, `::/96`, `::ffff:0:0:0/96`, NAT64
  `64:ff9b::/96` and `64:ff9b:1::/48`, 6to4 over a denied IPv4, Teredo and the rest of `2001::/23`,
  site-local `fec0::/10`) is refused when saved, and **every connection is checked when it is dialled**
  (`dialCheck`, so a name that resolves to such an address, DNS rebinding included, is refused too); one
  refusal text whatever the reason (no port or host oracle); redirects only to the same host, at most 3;
  **no proxy: `HTTPS_PROXY` is ignored by design** (a proxy would connect past the check), so custom
  tools need direct egress; 30 s for a whole call; a 1 MB answer cap (over it: refused) and 64 KB of
  headers; per-tool
  `enabled` (a disabled tool is not offered and a call fails closed) and `offerToLead`. The output is
  `{source, untrusted: true, text, truncated, isError}`, one page (a remote call is never repeated to
  page it). Each call is a journaled step (a crash after it commits replays it: the remote is not called
  again) and a log line (tool, source host, status, bytes, ms; never a secret). **A running task keeps
  its snapshot:** disabling or editing a tool reaches new tasks only; to stop a running task's calls,
  delete the tool's secret (read at call time) or cancel the task.
- **Secrets** (`secrets` table): set by an admin **for one origin** (`https://host:port`; migration
  0010) and sent to no other: a save naming a secret for a URL of another origin is refused, a call to
  one is refused, so moving a tool to another origin takes setting its secret again, value and all.
  Listed by name, origin, setter and the items naming them; never returned by any route, logged, put in
  an error, the snapshot or the export. **Only the bound origin ever receives the key.** What that
  origin sends back is redacted of an exact echo of it before it reaches the API, `task_steps`, the
  logs, the UI or a model: the key with and without its prefix, base64 in either alphabet (padded or
  not), URL- and JSON-escaped, and a Basic pair and its password; so is every error built from remote
  data (`secretRef.scrub`: a schema's property path, a call's failure), and the server's own protocol
  version is never echoed. The check runs on what the server sent, whole, before anything is cleaned
  or cut to its cap (a server name, a tool's or a property's description, a problem), so a key that
  straddles a cap is caught too: a listed tool holding it is not importable. An origin that obfuscates its own key (splits, spaces or hex-encodes it) is
  not stopped: it already holds the key. They are stored in
  Postgres as everything is (back up and protect the database accordingly); a task resolves a secret
  by name at call time, so a secret changed mid-run reaches the run's later calls.
- **A real admin only:** without sign-in, serving with `--allow-no-auth` or `--admin-without-auth`
  turns secrets and custom outbound calls off (`RemotePolicy.Off`): setting a secret, Discover, Import
  and a custom tool's Test are 403, custom tools are left out of the lead's list (a Limitations line
  says so) and a custom tool's call in a run (a subagent's) fails closed. **So without sign-in, custom
  tools and secrets work only for a native `biglaw serve` on loopback, with neither flag. With
  sign-in they work for signed-in admins under Compose and in containers too** (the policy is on;
  members are refused by the admin guard: `TestSignedInRemotePolicy`, `TestServeWithSignIn`).
- **Test** (`TestCall`, admin): one call of a subagent or tool as new tasks would make it, over a
  matter's documents, within 8 model calls and $0.25 (and never more than a task's budget); journaled
  as the one step of a cancelled task named for the test (started at its grant, every model call metered
  as it happens, settled to what it spent) and in the change log (`manage_events`), all written whatever
  the request does, so a client that goes away or a crash still leaves the spend counted. Passes,
  `finish` and the lead's own tools are tested only inside a task. The recorded demo has none of these
  writes.
- **Deleting** a custom agent, tool or server is a tombstone version (revertable). What Go depends on
  (the memo lead's tools, the subagents and tools Go calls, the passes, prompts and packs) is refused
  with the reason; so is a deletion that would break the set (a tool an allowed list names, a server a
  tool uses). The registry's caps rose to 48 tools and 24 visible to one agent (from 22/14) to fit them.

**Skills** (`internal/engine/skills/<name>/SKILL.md`, embedded; created, edited and deleted in Manage
too): a named bundle of instructions with a one-line description, a Markdown body, optional reference
files (`.md`, `.json`, `.csv`, ≤ 16 of ≤ 64 KiB) and an optional `tools` list. Front matter: `name`,
`description` (≤ 200 bytes), `status` (candidate until a named lawyer reviews it), `reviewedBy`,
`tools`. Progressive disclosure: the lead's brief lists each skill's name, status and description only
(≤ 40 skills in ≤ 4 KiB, refused at save); `use_skill {name, file, offset}` returns the body or a
reference file one page at a time (`agent.TakeText`; `next` continues it), spends no model call, is a
journaled step like any tool (so a crash resumes past it) and records the skill read in the ledger
(kind `skill`) with the step. **Callers:** the lead (`MemoVisible`, now 12 tools; `use_skill` may run
before a plan, P1); an admin may name it in a subagent's allowed tools. A skill grants no tool: its
`tools` must be tools that use_skill's callers may already call (refused otherwise), and the answer
says to call only tools the caller was offered. A deliverable that read a candidate skill names it in
Limitations, as `candidates()` does for packs. **Starters** (all candidate): `deadlines`,
`figure-conflicts` and `requirement-deviation`, process guidance taken from the lead's prompt and the
numbers, join_discrepancies and check_deviation specs (no legal content of their own); `deadlines`
carries `examples.json`, the numbers calls its spec gives as examples.

**Configurable hooks** (`internal/engine/hookrules.go`; items `rule.<name>`, files
`internal/engine/specs/hooks/<name>.json`, created, edited, tested by a run and deleted in Manage like
any item, in each task's snapshot). A rule is `{name, enabled, order, event, match, when, action,
failure}`:
- **Events:** `before_call` and `after_call` (every call `Runtime.Invoke` makes: the lead's, a
  subagent's and Go's fan-out calls; no rule applies to `finish`, whose hook point is `task_finish`, so
  a forced finish is never refused, nor to `verify_quote`, the engine's own quote check),
  `finding_admitted` (each new finding, after its quotes are verified, before it is recorded: admit's
  evidence findings and check_deviation's deviations alike, through the one path that records a new
  finding, `recordFinding`; `TestFindingWritesGoThroughRecord` fails on any other) and
  `task_finish` (after finish's checks pass, before the deliverable renders).
- **Matchers:** a tool or subagent name, a prefix ending in `*`, or `*`; `when` is an optional
  condition on one field (`eq`, `ne`, `contains` (case-insensitive), `exists`, `absent`, `gt`, `lt`) of
  the call's input (`after_call`: `input.*`, `output.*`), of the finding (`claim`, `issueId`,
  `verified`, `low`, `documents`) or of the finish (`stop`). A condition is a trigger, not a control:
  the lead writes what it reads.
- **`block`:** `before_call`: a refusal the lead reads, and the call does nothing. `after_call`: the
  answer is withheld, and so is every effect: for the evidence and drafting subagents (`extract_facts`,
  `analyze_issue`, `check_deviation`, `draft_section`) the rule runs on the subagent's own answer before
  Go admits a finding or saves a section, so nothing is recorded (a sweep's visit is still done, with
  nothing admitted); an after_call `block` or `check` must name one tool, never a tool that writes the
  ledger as it runs (`ledger`, `sweep`, `committee`, `cover`, `numbers`, `join_discrepancies`,
  `use_skill`, the figure reader and judge) or a subagent allowed one (use `before_call` for those).
  `finding_admitted`: the finding is **quarantined by the hook** (`hookBlocked`: never in the memo, a
  drafter's or a subagent's read, nor `numbers`', `join_discrepancies`' (`Engine.Findings`) or the figure reader's, disclosed in its own Limitations line with the count by rule, and
  never counted as a quote that failed verification: G4 and the Evidence Note leave it out; a check that
  fails closed there quarantines the same way). `task_finish`: the submit is refused with the reason;
  the lead can only finish by a forced finish (its budget or max steps), so an unconditional block
  spends the rest of the task's budget and then delivers with the refusal as a Limitations line.
- **`approve`:** names one tool the lead calls (saved otherwise refused), never `committee` or
  `cover`, which a forced finish runs as the lead's own steps (a gate there would pause a run that must
  end). Only the lead's own calls
  of it can be approved, so **every other call of it fails closed**: a sweep's visits, the coverage
  pass's or the committee's calls and a subagent's calls are blocked (`blocked (needs lead approval)`),
  the caller gets the usual refusal, and the deliverable's Limitations count them by rule; the save's
  notes and the rule's summary in Manage list which callers it will block. A hook gate opens with the call's **whole input**, the refused
  step commits and **the run pauses at the gate** (`awaiting_gate`; a crash before the pause pauses on
  resume, as an open hook gate does). The gate is keyed by a 128-bit hash of the canonical input, and
  an approval covers **exactly that input, once**: the lead makes the call again with the same input and
  it goes through; any other input, or the same one again, opens a new gate. A rejection refuses that
  one attempt. `finding_admitted`: the finding takes its own G7 gate. Each note to the lead lists only
  the gates decided since the last. **Without sign-in, anyone who can reach the API can decide a
  gate, hook approvals included**; with it any signed-in user can (member or admin), and the gate
  records who (`decidedBy`). An approve rule cannot yet require a role.
- **`check`:** a named subagent whose input is `{event, subject, text}` and whose output holds
  `verdict` (allow, block or annotate) and `reason`; its model calls spend from the caller's budget and
  are metered with the call's step. A check agent is never offered to the lead. Its own tool calls are
  hooked like any call, but a `check` rule on them is skipped (no check checks a check).
- **`webhook`:** an https POST through the same SSRF-checked client, origin-bound secret and
  off-without-a-real-admin rule as custom tools. The body is **metadata only** by default (`{hook,
  event, taskId, subject, textBytes, at}`); a rule opts in with `sendContent`, and then **matter content
  leaves BigLaw** (the call's input or answer, the finding's claim and quotes, or the draft's text, up to
  8,000 characters, for every matching event of every client and matter; the editor warns). `wait`
  blocks on a 2xx within 30 s (at least once: a step re-run after a crash sends it again); otherwise it
  is sent in the background (at most 8 in flight; beyond, it is dropped and the outcome says so), its
  answer only logged. With no real admin a webhook cannot be sent, so a waited one fails as its rule
  says: closed blocks every call it matches.
- **`limitation`:** a Limitations line.
- **Known limits:** an after_call block withholds what BigLaw records, never what already happened
  elsewhere: a custom tool's remote effect (an MCP or HTTP call already made) is not undone, and a
  withheld drafter's own numbers worksheet (written by its calls during drafting) stays in the ledger,
  unused; the cap of 8 background webhooks in flight is shared by the whole server, not per rule or task.
- **Order:** rules run by `order`, then name; a block or a gate stops the rest for that event.
- **Failure:** a rule that cannot do its action (a check unfunded, failed or answering no verdict, a
  waited webhook that failed, a field it cannot compare) **fails closed** (blocks) or **open** (lets the
  event through), as it says; a limitation or a webhook that does not wait never blocks. Any other error
  (a record that could not be written, a panic, a stopped run) fails the call, whatever the policy. A
  rule that blocks everything does not wedge a run (it ends), but a running task keeps its snapshot:
  cancel it and submit again.
- **Journal:** every outcome (`allowed`, `blocked`, `gated`, `approved`, `annotated`, `noted`, `sent`,
  `dropped`, `skipped`, `failed-open`, `failed-closed`) is a ledger row (kind `hookrun`, keyed by the
  lead step and its order in it) written with the step, and a log line, so a crash and resume neither
  repeat nor lose one.
- **Validation** at save: the event and action pair, the matcher and condition, that a named tool,
  subagent or check agent exists with the check's schemas, the approve and after_call limits above, the
  webhook URL and secret, a message of at most 2,000 characters with no control or format characters, no
  Go hook's or reserved name; a subagent a rule checks with cannot be deleted. A Test call runs the item
  alone, without the rules.

## Files

Files are stored in `file_chunks` rows of at most 1 MiB (a `CHECK` in the schema refuses a
larger row), written in one transaction from a stream. lib/pq moves `bytea` as hex text, so each
chunk costs a few times its size in transient memory; chunking bounds **writing** by the chunk,
not the file. `ReadFile` streams a file to a writer one chunk at a time (one query per chunk, so
it holds two chunks: the last is written only after verification); `GetFile` reads it into one
buffer of the recorded size. Both check the recorded size against the 64 MiB cap before reading
content and verify the size, the chunk count and SHA-256 before the last chunk is written, so a
writer never receives a whole file that fails the check (`TestReadFileWithholdsUnverifiedTail`;
the download route also verifies once before sending headers, so a corrupt file is an error
status, not a short body). The SHA-256 detects **storage corruption, not tampering**: anyone who can write the rows
can rewrite the hash too.

An upload holds one pooled connection, inside its transaction, for as long as its reader takes.
So at most 16 uploads (`MaxConcurrentUploads`) run at once, leaving 4 of the 20 connections for
`/health` and reads. Further uploads wait for a slot until their context ends. Nothing in the
store ends a stalled upload: its slot and connection are freed when its reader returns (an error
rolls the transaction back, leaving nothing behind). Bounding a stalled client is the caller's job;
the upload route's read deadline does it (§ Documents). At most 4 reads (`MaxConcurrentReads`,
`GetFile` or `ReadFile`) run at once; a read over the cap waits for a slot or its context
(`TestConcurrentReadsBounded`, which also pins that a read holds its slot while its writer
blocks). So whole-file reads (ingest's) stay near 512 MiB, and streamed
reads (downloads) hold one chunk each, a stalled client no longer than one transfer window
(`TestDownloadBounded`).

Names must be 1–255 bytes of UTF-8 and not `.` or `..`. They may not contain `/`, `\`, control
characters, invisible characters (category Cf such as bidi controls, zero-width spaces, the BOM
and soft hyphens; Unicode Default_Ignorable_Code_Point such as the Hangul fillers U+3164/U+115F,
the combining grapheme joiner and variation selectors; the braille blank U+2800), line/paragraph
separators or a run of two or more white-space characters (no-break spaces included), and may not
start or end with white space, any of which can disguise an extension. ZWNJ/ZWJ are allowed once
between two letters, marks or emoji, as Persian, Indic scripts and emoji sequences need them, and
one presentation selector (U+FE0E/U+FE0F) right after an emoji symbol (`❤️ notes.txt`, a ZWJ
family emoji).
Names that are only unsafe on Windows (`CON`, `aux.txt`, `c:evil.txt`) are stored as given and
made safe on download. Content types must
be concrete media types (no `*` wildcard) in printable ASCII, at most 255 bytes in the canonical
form they are stored in (`Text/HTML;  Charset=UTF-8` → `text/html; charset=UTF-8`).

Measured on a 64 MiB file (before → after this layout; peak above baseline, the same for Go heap
and RSS): writing 256 → 67 MiB, reading 128 → 126 MiB at the default `GOGC=100`; with
`GOGC=10`, writing 256 → 11 MiB and reading 128 → 73 MiB. So a read holds about 64 MiB live and
peaks near 128 MiB at the default `GOGC`. The difference is garbage the collector has not yet
reclaimed, so a small machine should set `GOMEMLIMIT` (for example `GOMEMLIMIT=512MiB` on a
4 GB Pi) to make the runtime collect it sooner.

**Memory of one ingest, end to end** (r2: the server, not the capped child, used to take 0.8 GB
for one 431 KB PDF of 63 MiB escape-heavy text: an unbounded buffer of the child's JSON and an
array literal of every chunk body). Measured with `go test -run '^TestIngestMemoryBounded$' -v
./internal/docs` (the server's live heap, sampled every millisecond) while sampling every Postgres
process with `ps -o rss= -C postgres` every 50 ms: ingesting each of seven text-cap shapes raised the server's live heap by
**48–60 MiB** (one run: 15 MiB of escape-heavy text **49 MiB as a PDF** through the child and 49
as a text file; 16 MiB of newlines 48; one header line of 16 M tabs 48; a 16 MiB header of three
4 MiB cells under a 4 MiB `#` line 49–60 across runs; `x\n` × 8 M and `ten bytes\n` × 1.6 M 52).
The test process peaked at 99–111 MiB RSS, the largest Postgres backend at **180 MiB** over the seven
shapes (round 2 measured 127 MiB with the escape-heavy shape alone), and the child at about **60 MiB**
(`TestChildNearTextCap`). The test (skipped under `-race`; `make test` runs it in its non-race
pass) fails above 5× the text cap (80 MiB, a 20 MiB margin over the
largest figure measured). Round 2's version split every section body into a slice of lines and
the header row into a slice of cells first (16 bytes per line or tab): in this test 16 MiB of
newlines cost +288 MiB and a 16 M-tab header +288 MiB (`x\n` lines +172), and a live server
peaked at 326–589 MB; now lines and cells are walked one at a time, and the header keeps at most
the 1,401 cells a pairable row can have (a longer row is split like prose). The live binary
(`VmHWM`, idle 9.8 MB) now peaks at 96 MB for a 16 MiB newline file and then an `x\n` file, and
133 MB for an `x\n` file and a tab-header file at once. Chunks go to Postgres by `COPY`, a row at a time. Add the file
itself, which the reader is handed whole (up to 64 MiB, about 128 MiB peak at the default `GOGC`,
as above). So two ingests at once (`ingestSlots`) of successful files at the text cap take about
2 × (60 + 180 + 60) ≈ 0.6 GB above an idle server, plus up to 2 × 64 MiB of file; the worst case
is two hostile files whose children die at their 1 GiB cap: about 2 GiB of children plus that.

## UI (`ui/`; PR 11, pulled forward by owner decision 13)

Owner decision 13 (2026-10-20): BigLaw needs a live, playable UI on discover.legal before the engine
exists, run against a **recorded** engine, with no model calls. So the UI is built first and talks
to BigLaw only through one interface (`ui/src/api/transport.ts`):

- `http.ts` — the REST API on the same origin (README contract routes; the shapes the contract left
  open are fixed in DESIGN § UI contract, PR 11, and typed in `ui/src/api/types.ts`). PR 4b serves the task, step, gate, cost, client and
  document routes; `biglaw serve` serves the built UI at `/` (`make ui build` embeds `ui/dist`; a
  build without it shows a notice there). Playbooks, time entries and profiles are 404 until PRs 6/8/9. `npm run dev` proxies the API paths to `UI_API` (default `http://127.0.0.1:3101`).
- `recorded.ts` — replays recorded runs in the browser. A build with `VITE_ENGINE=recorded` aliases
  the transport to it, so the bundle contains no HTTP code: `test/offline.test.ts` builds it and
  fails on any `fetch`, `XMLHttpRequest`, `EventSource`, `WebSocket`, `sendBeacon`, worker, preload
  link or absolute URL (`scripts/offline-check.mjs`; the same check flags the live bundle, proving it bites). Two exact strings are
  allowed because they are displayed, never requested: `https://discover.legal/` (the banner's way back) and `http://127.0.0.1:3101` (a default on Settings).

**Manage** (`screens/Manage.tsx`, § Manage): the sections Agents, Tools, Packs, Skills, Hooks and Change log; each item's contents, where it is used and its usage; for an admin an editor over structured fields (description, turn cap, class, role, allowed tools, schemas; a pack's or skill's status, reviewer, checklist or tools, and body) or the raw files, with problems shown beside their file, a preview of unsaved changes, history with a diff between any two versions or the built-in, revert, a new skill, delete, and export links. The recorded demo shows the built-in catalogue read-only (`ui/src/recorded/manage.json`, recorded by `eval/demo/manage_test.go`; no history, no export, every write refused).

**Screens** (hash routes, so a static bundle works under any base path): **Matters** (matter cards,
tasks, the new-task wizard), **Task** (run bar with status, step, stop reason, budget, Cancel; the
deliverable gate with Approve/Reject; tabs Run log (the steps, streamed), Findings (claims,
severity, each citation with its verification badge: `exact`, `tolerant`, `judge`, `ensemble`,
`… @transcript`, `excluded`; a verified quote links to its section with the matched text
highlighted), Deliverable (the memo with `[F-id]` links, Limitations and Evidence Note) and Cost),
**Documents** (library, outline, section reader with highlight, search), **Playbooks**, **Clients &
Conflicts**, **Time & Billing**, **Settings**. The last four are read views over PR 6/8/9 routes;
in the recorded demo Clients shows the sample matters, Playbooks, Time & Billing and Settings'
profiles show sample records written for the demo (labelled so on screen), and Settings also lists
this build's configuration table, read-only. Rejecting the deliverable asks for a reason (sent as
`{"reason"}` to `POST /tasks/{id}/gates/{gateId}/reject`, as v1's reject did) and closes the gate:
the decision is kept in `decidedGates`; live, the lead resumes with the reason (the task goes back to
`running` and the Task screen opens the event stream again, which the server ends with `done` at every gate (`ui/src/api/follow.ts`); the recorded demo has no revised run, so there it stays `rejected`). The Task screen's Table and
Redline tabs (diligence, contract review) are **planned** with PRs 5 and 6, the Documents version
timeline with PR 6. With sign-in the shell shows a sign-in page until there is a session, then the
user and Sign out in the header; members see no admin-only controls in Manage.

**The recorded demo.** A visitor picks one of three sample matters (Greystone v. Talbot litigation
defense, Altamira/Sorell merger control, Quillon/Fernhill license review), types a prompt or picks
a recorded task, and watches the run log replay (about 12–23 s; Skip to end). A prompt is matched
to the matter's recorded tasks deterministically (`ui/src/api/match.ts`: the share of its content
words a recorded task's prompt, title and keywords cover, ties to the earlier task; at least 30%
or it is **off script**: the UI says no recorded run answers it and offers the nearest). Search
offers four **recorded searches** per matter whose hits are v2's own `Search` results; any other
query is ranked in the browser by a port of the server's lexical formula (two legs, stemmed and
exact, `idf × tf/(tf+10)` per term; a plural-dropping stand-in for the English stemmer), labelled as
such on screen; it puts the server's top hit first on every recorded search (`test/recorded.test.ts`).
The banner is one line (`Recorded run; not live …`, a `← discover.legal` link to the site root, and
a "How this demo works" toggle holding the provenance below); the link exists only in the recorded
bundle (`test/offline.test.ts`). Nothing is saved; a reload starts over.

**Provenance (exact).** No model has run on these matters (v1's proof-run harness says "no
real-model scores have been recorded yet"; v1's real LAB runs used Harvey's documents, which are not
in the repo; the family and employment drives' documents and outputs were never committed). So:
the documents are `eval/tasks/{litigation,competition-regulatory,ip-licensing}` (synthetic,
fictional), plus one scanned exhibit drawn for the demo (an affidavit of service in the Greystone
matter, consistent with its docket; `eval/demo/make_scan.py` draws the JPEG from
`eval/demo/testdata/06-affidavit-of-service-scan.txt`). `eval/demo/record_test.go` ingests the text
documents with `docs.IngestText` and the scan with `docs.Ingest` through v2's vision path, whose
reader is a `model.Fake` (named `model.Fake (scripted)` in the transcript provenance) returning that
same text for both readings, so the transcript is what the image shows and no model read it. It
records each document's outline (`Outline`), section text (`Section`), chunks (`Parse` +
`Chunkify`), the run-log and offered search hits (`Search`, lexical) and every quote's verification
(`VerifyQuote`, no judge) — all computed by v2 code. The plan, the claims and the memo text are the
matters' answer keys (reference answers written by hand for scoring): each step is marked
`computed` or `scripted` in the run log, and the banner and memo Limitations say so. To show the
whole ladder, a few quotes and one finding were written for the demo (`demoQuotes`, `demoFacts`)
and verified like the rest: of 79 citations across 48 findings, 74 are `exact`, 2 `tolerant`
(curly quotes typed over straight ones), 1 `exact@transcript` and 1 `tolerant@transcript` (from
the scan, capped at 0.85), and 1 is excluded: finding F5 of the deadlines task misquotes the rule's
figure ("20 days"; the excerpt says 21), fails `VerifyQuote`, is quarantined, left out of the memo
and named in its Evidence Note. The memo's Limitations name each of these. No judge or ensemble rung
ran (they are model calls), and no committee step is recorded. Playbooks, time entries and profiles
are sample records (fictional people). Costs are $0 because no call was made; the Cost tab quotes
v1's best Haiku LAB run ($11.52, 8.3 M tokens) for scale.

```bash
make ui            # cd ui && npm ci && npm run build && npm test  (CI's ui job)
make demo-bundle   # ui/dist-recorded: the static demo for https://discover.legal/demo/biglaw/
                   # (VITE_ENGINE=recorded UI_BASE=/demo/biglaw/; then the no-network check)
make demo-record   # re-record ui/src/recorded/recording.json (needs BIGLAW_TEST_DSN, Postgres + pgvector)
```

The demo bundle is four files (`index.html`, one JS and one CSS asset, and the scan; about 800 KB;
the JS is 640 KB, 133 KB gzipped) and needs no server logic: copy `ui/dist-recorded/` to the site's `/demo/biglaw/`. A
build without `UI_BASE` uses relative asset paths, so it also works from any directory.

## Test it

```bash
go test -race ./...             # unit tests; DB tests SKIP without a DSN
# a throwaway Postgres for the DB tests:
docker run -d --name biglaw-testdb -p 127.0.0.1:55432:5432 -e POSTGRES_PASSWORD=t pgvector/pgvector:0.8.6-pg16
BIGLAW_TEST_DSN='postgres://postgres:t@127.0.0.1:55432/postgres?sslmode=disable' \
  BIGLAW_REQUIRE_DB=1 go test -race ./...   # DB tests too; each gets its own schema, dropped after
make ci                         # gofmt, vet, go mod verify/tidy, static build, tests (-race, shuffled;
                                # then the reader child's bounds again without -race, which turns
                                # its memory cap off, with -v; where BIGLAW_REQUIRE_DB is set it
                                # fails unless TestChildBoundsItself, TestChildNearTextCap and
                                # TestIngestMemoryBounded each report PASS there), make eval (E0/E3),
                                # loc.py + its tests, docker compose config. Its race pass runs at
                                # -p 4 -parallel 4 (TEST_P, TEST_PARALLEL, for CPU); a test store's
                                # pool is storetest.Pool (4) and at most storetest.Slots (8) DB tests
                                # run at once across packages, so the peak stays under 50 of
                                # Postgres's default max_connections of 100 at any -p (PR 55 CI)
make ui build                   # the binary with the web UI embedded (ui/dist → internal/api/dist)
make lint vuln                  # staticcheck and govulncheck (network needed; CI runs both)
```

Named tests (PLAN.md PR 1): `TestConfigDefaults`, `TestVendorBreakerRefusesDirectAnthropic`,
`TestMigrationFilesOrderedAndUnique`, `TestStoretestSkipsWithoutDSN`,
`TestStoretestFailsWhenRequired`, `TestLocBudget` (Python, `scripts/test_loc.py`), and against
Postgres `TestMigrateIdempotent`, `TestConcurrentMigrateSafe`, `TestFailedMigrationRollsBack`,
`TestFilesRoundTripBytea`, `TestSchemaPerTestIsolation`. Extra: DSN checks and secret redaction
(every shape the review committee used to leak one), the exposure rule (runtime refusal and the
Compose port check with its bypass cases), migration atomicity when the bookkeeping row fails, a
file that ends its own transaction, lock-wait logging and cancellation with the port still
closed, chunked storage and the chunk-size `CHECK`, the 64 MiB boundary, corruption detection,
name/type validation, upload slots, a DSN accepted under a uid with no passwd entry, `/health`, command dispatch, a real
`serve` end to end, and repository consistency (env-var tables, one Go version, deploy files,
LICENSE/NOTICE in the image).

Named tests (PLAN.md PR 2, `internal/model`, no network and no key): `TestChatToolCallRoundTrip`,
`TestTypedErrors`, `TestRetryBackoffBounded`, `TestFakeScriptExhaustedIsError`,
`TestFakeIsDeterministic`, `TestFakeRoutesByAgentName`, `TestCallsPricedOnEveryHost` (PLAN's
`TestLocalCallPricedZeroWithWattHours`, inverted by the owner's round-2 ruling and renamed; with
`TestWattHoursRecorded`),
`TestPricesJSONOverride`, `TestImagePartsRoundTrip`, and in `cmd/biglaw` `TestConfigModelVars`.
v1's pricing vectors are carried as `TestLookupPricing`, `TestCalcCostUSD`,
`TestCalcCostUSD_GPT56Luna`, `TestGPT56Siblings_RecognisedButUnpriced`,
`TestSetModelRates_OverridesBuiltins`, `TestRecord_UnknownModelFlaggedAndCounted`,
`TestWarnUnpricedOnce_PerModel`, `TestBuiltinClaudeRatesUnchanged` (v1's
`TestLocalWattMeteredCallsAreFree` is reversed by owner decision: `TestCallsPricedOnEveryHost`), and
`TestBuiltinPricesParse` checks every row of v1's `basePricing` against the new table. Extra:
request validation, stable request bytes for prompt caching, role fallback, and that no error carries the key, the endpoint URL or upstream text. Round 1 of
review added `TestBilledFailureKeepsUsage`, `TestUnpinnedBehaviours` (no retry after a network
error, `max_completion_tokens`, `Truncated`, the 25% jitter bound, `Retry-After` overflow,
duration over all attempts), `TestTransportErrorsFixedText` (an endpoint echoing the key and the
prompt), `TestAnswerToolCallsChecked`, `TestPriceFileChecks` (a FIFO and a 1 MiB + 1 price file) and, in
`cmd/biglaw`, `TestServeLogsModelSetup`. Round 2 added `TestNilContentSentAsEmpty` and pinned the
DNS phrase, `Fake`'s `Truncated`, a billed failure's duration and watt-hours, and the refusal of
role ids that are URLs or credentials.

Named tests (PLAN.md PR 3, slice 3a; DB tests need Postgres): the carried vectors of v1's
`chunk_test` (`TestTableRowChunking`, `TestProseStillSectionChunked`), `pageindex_test`
(`TestReconstruction`, `TestNumberedNesting`, `TestArticleAndRecitalNesting`, `TestFlatDocument`,
`TestSchemes`), `tabcite_test` (`TestCitationExactAndTolerantNoJudge`,
`TestCitationParaphraseJudgeOneCall`, `TestCitationUncertainJudgeTriggersEnsemble`,
`TestCitationEnsembleMajorityUnsupported`, `TestCitationFabricatedQuoteUnverified`,
`TestCitationJudgeFailureIsAnError` (changed: an error, not "unverified"),
`TestCitationWindowLocatesRegion` (without its page variant); the two cell/tally vectors go with
the diligence table, PR 5), the reader part of `ooxml_test` (`TestOpenRejectsNonDocx`,
`TestTextJoinsRunsAndSeparatesParagraphs`; `TestEscapeUnescapeRoundTrip` and
`TestRoundTripPreservesPartsAndOrder` move to PR 3d with the writer code they test), v1 `protocols` (`TestVerbatimContains`,
`TestNormalizeSourceKey`, `TestResolvesByTitle`) and v1 `services` (`TestExtractPlainText`,
`TestExtractDOCX`); `TestScannedPDFFlaggedNoText`, `TestOutlineHasNumbersTitlesTokens`,
`TestSectionPaginates`, `TestRetrieveNeverLeavesMatter`, `TestSearchRareTermOutranksCommon`,
`TestSearchTopKDeterministic`, `TestSearchExactIdentifier`, `TestLexicalRecallProofRuns` (E4),
`TestLadderMethodsAndCaps`, `TestFileNotReachableOutsideMatter`, `TestFileDeletedWithDocument`,
`TestConcurrentReadsBounded`, `TestUploadReadDeadline`, `TestDownloadNameSafe` and the extended
`TestFilesEdgeCases`. Extra: `TestMarkdownHeadings`, `TestChunksCoverEveryWordOnce`,
`TestExtractPDFTextLayer`, `TestHostilePDFContained`, `TestIngestRefusalLeavesNothing`,
`TestUploadTotalDeadline`, `TestUploadErrorsTyped`, `TestWriteErrorKinds`,
`TestOpenRefusesZipBomb` and, in `cmd/biglaw`, `TestBinaryIsItsOwnPDFReader`. Review round 1
added `TestChildBoundsItself`, `TestChildDiesWithParent`, `TestForgottenMaybeRunChildIsAnError`,
`TestTextFidelity`, `TestTextLinearAndBounded`, `TestSectionTreeBounded`,
`TestHeadingHeuristics`, `TestDownloadBounded`, `TestOrphanFilesSwept`,
`TestSearchRarityIsScoped`, the answer-free questions of E4, and cases in
`TestCitationJudgeFailureIsAnError` (a failed vote), `TestExtractPDFTextLayer` (12 blank pages),
`TestDownloadNameSafe`, `TestFilesEdgeCases` (emoji names), `TestOutlineHasNumbersTitlesTokens`
and `TestSectionPaginates` (out-of-range ids and offsets) and `TestServeEndToEnd` (the orphan
sweep at startup). Review round 2 added `TestReadChildBounded`, `TestChildNearTextCap`,
`TestChildRunsTheRunningImage`, `TestIngestMemoryBounded`, `TestSearchCost`,
`TestReadFileWithholdsUnverifiedTail`, `TestDownloadDeadlinePerWindow`,
`TestIngestSlotWaitBounded` and, in `cmd/biglaw`, `TestServeStopCancelsIngest`; extended
`TestSectionTreeBounded` (table shapes, every chunk column, the exact cap and its note),
`TestConcurrentReadsBounded` (a slot held while the writer blocks), `TestHostilePDFContained`
(the text cap on uploads), `TestOpenRefusesZipBomb` (only text parts are read) and
`TestTextFidelity` (`noBreakHyphen`, `sym`). Two carried vectors changed:
`TestTextJoinsRunsAndSeparatesParagraphs` expects a tab for `<w:tab/>`, which v1 dropped, and
`TestTableRowChunking` expects rows grouped into one chunk, not one chunk per row.

Named tests (PLAN.md PR 3, slice 3b; DB tests need Postgres with pgvector): in `cmd/biglaw`
`TestModelsRoleMap` and `TestRoleIDRefusesQueryCharacters`; in `internal/model`
`TestEmbeddingsPriced`; in `internal/docs` `TestEmbedFailureLeavesLexical`,
`TestBackfillEmbedsOldDocuments`, `TestHybridKeepsScope` (G1 with vectors) and
`TestHybridRecallProofRuns` (gate E4; it skips on the self-test tape, above). Extra:
`TestEmbedTypedErrors`, `TestFakeEmbed`, `TestHybridFusion`, `TestEmbedBatchesAndUsage`, `TestEmbedWaitsBounded`,
`TestE4TapeReplays` (the harness self-test), `TestOpenNeedsVector`, `TestVectorOncePerDatabase`
(storetest creates the extension once per database, not per schema), `TestServeHybridEndToEnd`,
the hybrid figures in `TestSearchCost`, the `embed`/`vision` cases of `TestRolesModel`, and the
`search` field in `TestHealth`. `TestLexicalRecallProofRuns` now shares E4's scoring with the
hybrid gate (`e4Measure`). Review round 1 added `TestReembedNeverMixesModels`, `TestLexicalOnlyNotBuried`, and cases in
`TestBackfillEmbedsOldDocuments` (failures counted and noted, "in a row" pinned), `TestEmbedWaitsBounded`
(the backfill's per-document bound), `TestEmbedFailureLeavesLexical` (a lexical-only scope during
an outage), `TestEmbeddingsPriced` (token cap, ada-002, no usage), `TestEmbedTypedErrors`
(subnormal, huge), `TestFakeEmbed`, `TestRoleIDRefusesQueryCharacters` (bidi, zero-width, key
shapes, length), `TestOpenNeedsVector` (8 concurrent first starts) and `TestDownloadBounded`
(measures the stalled state).

Named tests (PLAN.md PR 3, slice 3c, vision; no model key: `model.Fake` and `httptest` only):
`TestTextModelNeverSentImages`, `TestUnreadableImageNotFabricated`, `TestBlankImageNoContent`,
`TestVisionNotConfiguredTyped503KeepsOriginal`, `TestTranscriptCitationCappedAndDisclosed`,
`TestLowAgreementFindingsGatedIndividually`, `TestScannedPDFDCTPagesExtracted`,
`TestScanEncodingUnsupportedTyped` (CCITT, JBIG2, a second filter, TIFF), `TestTooManyPages413`,
`TestMixedPDFTextLayerNotRewritten` and `TestTranscriptionJobResumesAfterRestart` (DB), and v1
`services` vectors `TestExtractImageWithoutVisionDegrades` (changed: the image is kept, waiting,
with a note naming vision, and the upload is 503) and `TestSniffImageMIME`. Extra:
`TestIllegibleNeverFilled`, `TestVisionFailuresTyped` (G6 kinds, a cut-off reading, the text cap,
the 10-minute bound), `TestLexicalOnlyDisplacementMeasured` (PR 3b r2 L2's figures) and, in
`cmd/biglaw`, `TestServeNeverSendsImagesToTheLead` (a lead-only server sends nothing; restarted
with a vision role it transcribes the waiting image at startup). Changed by 3c:
`TestScannedPDFFlaggedNoText` (a JPEG-scanned PDF without a vision role is stored waiting, 503; a
PDF with nothing on it is still `noText` with its note), `TestIngestRefusalLeavesNothing` and
`TestUploadErrorsTyped` (a damaged image is 422, a TIFF or CCITT scan 422
`scan_encoding_unsupported`, all leaving nothing), `TestExtractPlainText` (an image is a page to
transcribe, never text).

Named tests (PLAN.md PR 4b; DB tests need Postgres with pgvector; every model is `model.Fake` or an
`httptest` server): specs `TestSpecsLoadAndValidate`, `TestExtractFactsTypedValues`; `TestModelErrorsAsJSON`,
`TestConfigEngineVars`; policy `TestAllocationsMatchSpec`, `TestClassShareRefused`,
`TestLedgerPlanRequiredFirst`, `TestPlanFallbackToChecklistDisclosed`, `TestCommitteeRepeatsWhileFunded`,
`TestEscalationFiresOnFastFix`, `TestFinishRefusedWithoutCommittee`,
`TestFinishRefusedWithOpenFindingsWhileFunded`, `TestFinishAcceptsWhenBudgetExhaustedWithDisclosure`,
`TestFinishNeedsEverySuccessCriterion`; paged drafting `TestPagedDraftingLossless` (v1's
`TestPagedBoard_CompactPagingIsLossless`), `TestPriorSummariesPassedInOrder`, `TestDraftGetOnlyFinishedSections`,
`TestSummaryNeverRendered`; `TestFinishCheckIsDryRun`, `TestFinishSubmitRunsSameChecks`,
`TestInjectedDocumentCannotSkipCommittee`, `TestLowAgreementFindingGated`,
`TestDeliverableBadgesTranscriptCitations`, `TestEvidenceNoteCountsTranscriptOnlyFindings`;
`TestLedgerBucketsFindingsByIssue`, `TestLedgerNearDuplicateMerged`, `TestSweepVisitsEverySectionOnce` (+ v1's
three `sectionChunks` vectors), `TestSweepIdempotent`, `TestSweepReportsIncompleteWhenBudgetShort`,
`TestCommitteeCriticsContextFree`, `TestCommitteeRunsThreeInParallel`; v1's 16 `deviations_test` vectors,
`TestDeviationNeedsBothSidesVerified`, `TestOmissionRequiresVerifiedAbsence`; durable runs
`TestResumeAfterKillMidRun`, `TestResumeDoesNotRepeatCommittedSteps`,
`TestResumeChargesInFlightCallConservatively`, `TestRunLeaseSingleOwner`,
`TestBudgetStopEndsRunWithDisclosure`, `TestMaxStepsStop`, `TestFinishRejectedByCriticContinuesLooping`,
`TestAwaitingGatePausesAndResumes`, `TestCancelStopsRun`, `TestRunProgressAPI`; bandit
`TestBanditStatsPersistAcrossTasks`, `TestBanditDelayedCriticCredit`; integrity
`TestUnverifiedFindingNotInLedger`, `TestQuarantineExcludedFromDraft`, `TestFabricatingModelFailsTask`,
`TestDeliverableLintRejectsUnknownFid`, `TestExternalAuthorityFlagged`, `TestModelFailureIsNotAFinding`,
`TestEmptyModelReplyIsError`, `TestDeliverableGateBlocksCompletion`, `TestModelDigitsNotFromLedgerFlagged`,
`TestRetrieveNeverLeavesMatter` (engine level); packs `TestPackSelectionByJurisdictionPrefix`,
`TestCandidatePackDisclosed`; E1/E2 `TestProofRunsOracle`, `TestProofRunsFabricator`; UI embedding
`TestEmbeddedUIServesIndex`, `TestBuildWithoutUIServesNotice`, `TestAPIListsNeverNull`; Python
`test_compare_exit_codes`. Extra: `TestLabRunnerSmoke` (the LAB driver end to end against an
`httptest` server), `TestSweepShareBounded`, `TestSubagentLedgerReadsSkipQuarantine`, `TestUSDBoundFinishDeliversWithoutMeta`,
`TestEventsStream`, `TestE2EServe` (not a check: with `E2E_ADDR` set it serves the API, engine and
`ui/dist` over a scripted model for a browser run). Each guarded behaviour was broken once on purpose
and its test failed (65 deliberate breaks, all caught). Bench fixes (after the first real-model
bench; each scripted with `model.Fake` from the logged behaviour, each failing without its fix):
`TestPhaseSharesHoldInUSD`, `TestCommitteeAlwaysReviewsTheLastDraft`, `TestBudgetKeepsLaterPhases`,
`TestCalcAcceptsWhatModelsSend`, `TestLintSaysHowToFix`, `TestFigureLintIsBounded`,
`TestRendererUsesDrafterHandleSet` (codenames kept for the run) and `TestDeadlinesMeetTheAnswerKey`
(the litigation matter's four deadline criteria, from `eval/tasks`, met by a scripted run). LAB
improvements (after the real Haiku LAB rerun; scripted the same way): `TestSweepPagesFitTheAnswer`,
`TestTablePagedByRows`, `TestSweepReadsInParallelInPlanOrder`, `TestSweepResumesAfterKillMidSweep`,
`TestDevParaphrasedRequirementRefused`, `TestDevRealOmissionsKept`, `TestDevRestatedControllingQuoteRescued`,
`TestFigureLintSkipsLocatorLists`, `TestCoveragePassFromTheReserve`, `TestCoveragePassOnSubmit`,
`TestFakeRouteAnswersByContent`.

## Budgets (`scripts/loc.py`, run in CI)

| Budget | Ceiling | Today |
|---|---:|---:|
| Go production lines (physical lines, non-`_test.go`, test-support packages included) | reported, no limit (owner decision 17) | printed by `python3 scripts/loc.py` |
| Packages under `internal/`, test-support packages excluded | 12 | 9 (`agent`, `analysis`, `api`, `auth`, `docs`, `docs/ooxml`, `engine`, `model`, `store`; `store/storetest`, `engine/figtest` and `auth/authtest` are test support) |
| Direct Go dependencies | 3 | 2 (`github.com/lib/pq`, `github.com/ledongthuc/pdf`) |
| Runtime environment variables | 20 | 19 |
| UI runtime dependencies | 2 | 2 (`react`, `react-dom`) |
| UI source lines (`ui/src` `.ts`/`.tsx`/`.css`; tests, config, `recording.json` and `manage.json` not counted) | 3,200 (raised from 2,400 for Manage, owner decision) | printed by `python3 scripts/loc.py` |
| Packages without a `_test.go` | 0 | 0 |
| Lines in any one source file (`.go`, `.py`, `.sql`, `.sh`, `.js`, `.ts`, `.css`; tests included) | 2,000 | 968 (`internal/docs/vision_safety_test.go`) |

**There is no total line ceiling (owner decision 17, 2026-10-23).** `scripts/loc.py` reports the Go production total (run it for today's figure; a number written here goes stale) and fails only on the budgets that bound bloat directly: no file over 2,000 lines, ≤ 3 direct deps, ≤ 20 env vars, ≤ 12 internal packages, ≤ 3,200 UI source lines, no untested package. Each PR still reports its lines against its DESIGN budget in its notes. (The ceiling's history, 14,000 → 17,500 through owner decisions 7–16, is in PLAN.) A **test-support package** is one
whose directory name ends in `test` and that no production file outside it imports, such as
`storetest`. Under the coordinator's ruling it is left out of the package count, but its lines
count toward the reported Go total like any other non-`_test.go` file. A `*test` package that
production code imports is an ordinary package. loc.py also fails if `LICENSE` or `NOTICE` is
missing or a `.go`/`.py` file lacks its SPDX header. Env-var names are found statically (literal
`os.Getenv`/`LookupEnv`/`getenv` arguments, the config table, and `BIGLAW_*`/`ALLOW_*` literals).
An `os.Getenv`/`os.LookupEnv` argument that is not one plain string literal (a variable, a
concatenation such as `"BIGLAW_" + x`, a raw string) fails the check.

## Limitations

- Without sign-in Manage has no per-person admin: the local user is an admin on loopback, read-only under `--allow-no-auth`, and anyone who can reach the server under `--admin-without-auth`; the change log's author is "Local user" (§ Manage, Who). Sign-in has two roles only (admin, member); every signed-in user sees every matter (per-matter access, an approve rule's role, API tokens for scripts and the clients roster are not built), and the issuer is not asked again during a session (removing an account there ends its BigLaw sessions at their idle or absolute end; an admin ends them now with `POST /manage/sessions/revoke`, which has no screen yet). There is no member allowlist on BigLaw's side: who may sign in is the issuer's decision (§ Sign-in).
- The engine's tests write their own skills: an export that deletes a starter skill only makes `ui/src/recorded/manage.json` stale (re-record it).
- Manage creates skills, subagents, MCP and HTTP tools and MCP servers, not packs; the compatibility check's limits are in § Manage. A remote call in flight when a process dies is made again on resume (its step had not committed): remote tools should be safe to repeat. MCP tools: text content only (other parts are noted, not shown), one session per call, no server-initiated requests; the HTTP and MCP answers are one page (truncated, never paged by a second call).
- The UI's Playbooks, Time & Billing and profile views have no live routes until PRs 6/8/9 (404);
  Clients shows matters derived from documents' matter numbers until PR 8's client roster.
- **Memo workflow (PR 4b):**
  - Every test scripts the model (`model.Fake`); real models have run it only in the owner's bench
    and Harvey LAB runs (bench5–bench7, the Haiku LAB reruns: their logged behaviour is scripted into
    the tests named for them). Whether it meets gate A (v1's criteria on
    `litigation` and `ma-diligence`) is the owner's measurement (§ Memo workflow, run it).
  - The deliverable is Markdown only (`GET /tasks/{id}/deliverable.md`); the `.docx` export waits for
    PR 3d's writer.
  - Quote verification proves a quote is in the document, not that the claim follows from it:
    `prosecute` checks that, and a lawyer approves every deliverable. The G8 lint traces figures (a figure the drafter wrote by the figure reader's reading of it, to a cited span or value read clearly as the same kind, currency, scale and amount, never to one read unclearly or as another kind; a date's part or an identifier as written, digit for digit; a handle's value by construction; a figure the reader read unclearly, or left unlabelled in an answer, is flagged, never traced; one its call never read (no model, unfunded, failed, cut off or malformed: bench7) that a cited finding's own verified quotes or values state as written is listed check-first in the Computed figures block, naming those findings and why, traced only as written, and those findings gated, never a blocking lint line that strips figures from a redraft (one that only a worksheet or a handle states, or none, is still flagged: `TestUnreadMemoScaledSumNeverUngated`, `TestUnreadMemoWorksheetOnlyIsFlagged`, `TestUnlabelledMemoSpanIsNoCallFailure`), and the section disclosed in Limitations; a clear figure whose cited quote the reader never read (its admission's call unfunded or failed) is traced only as written, disclosed, and its finding gated for a lawyer (`TestR6UnreadQuoteTracesAsWritten`); one the drafter wrote at a sourced scale where the cited figure states every digit ("$5M" for "$5,000,000") is disclosed, since only the reader's scale makes it the same figure; a cited figure read at a scale never traces one written without it or from other words; a worksheet traces only when its operands are cited values, literals the cited quotes state, traced worksheets' results or counts of days (`tracedSheets`, `TestR6LiteralWorksheetLaundersAFigure`): lab-r4 r5 H1, H2, M1, r6 H1, M2, M3, `TestR5G8SkipsUnreadSingleDigit`, `TestR5UnclearQuoteTracesUngated`, `TestR5NoReaderBudget`), not
    words: a wrong party or a wrong verb with correct figures passes it.
  - The SSE stream polls the database every 500 ms. Cancel stops a run at once in its own process
    and at the next heartbeat (up to 10 s) in another; the call in flight is charged.
  - A hard crash (kill -9, out of memory, power loss) during a tool call is charged the spend its
    journaled model calls recorded plus the worst case of those in flight; a tool step that has made
    no model call yet, and spend no model call records (a query embedding inside `retrieve`, a
    fraction of a cent), are charged as before: the whole grant, or nothing until the step ends.
    What a crashed sweep read is lost with it (its findings commit with the step), so the re-issued
    sweep pays for those pages again; the lost spend is charged to the run, not to the sweep's share.
  - A forced finish (budget or max steps) delivers a draft its lint refused, marked "Delivered with
    an unresolved check"; a sentence citing a finding that is not usable (rejected at its gate or
    quarantined) is removed and counted in Limitations (a citation after the full stop goes with the
    sentence before it), the rest stands as drafted. An abbreviation mid-sentence (`Smith v.`) splits
    the sentence, leaving a fragment, and a table row citing one is blanked, which ends the table.
  - BigLaw answers only requests addressed by IP, `localhost` or (with sign-in) `BIGLAW_PUBLIC_URL`'s
    host (§ Exposure): any other LAN or proxy hostname gets 403 `bad_host`.
  - Tool-bandit statistics are per signature across all matters; a run's practice is not yet part of
    its signature (empty), so memo runs in one jurisdiction with the same lead model and document-count band share
    one bandit row per tool.
  - `/cost/summary` (all tasks, ingest-time embeddings and vision reads) is not built; costs are per
    task (`/tasks/{id}/cost`) and per document.
  - The transcription jobs keep their own loop (`docs/jobs.go`); moving them onto the run lease
    (DESIGN way 5) is deferred.
  - Packs are four `candidate` files; nothing checks a pack's legal content.
  - **Line budget:** PR 4b adds 3,818 lines (7,502 → 11,320) against about 3,060 its components carry
    (engine memo part 2,340, api 660, the documents routes 60): `engine` 3,406, `api` 248, the rest
    in `agent` (resume), `docs` (routes), `cmd` (wiring). The plan after 4b (16,822–16,852) broke the
    16,100 ceiling; owner decision 15 raised it to 17,000, decision 16 (bench fixes) to 17,500, and decision 17 dropped the total ceiling (§ Budgets).
    Round 1's fixes added 74 (11,394; plan 16,896–16,926).
- The recorded demo replays answer-key claims, not a model's work; its `@transcript` badges come
  from a scripted reader, not a vision model; it has no `judge`/`ensemble` badges (model calls); a
  query other than the offered searches is ranked in the browser, approximately; a rejected
  deliverable ends the replay (there is no revised run).
- Only the memo workflow exists; diligence, contract review, MCP, billing and auth are planned.
  Without sign-in the exposure rule above is the only protection: anyone who can reach the port can
  upload, list and read every document, and run, approve or cancel tasks. The cross-origin check stops a web page from uploading through the lawyer's browser,
  and the `bad_host` rule stops DNS rebinding; sign-in's sessions (§ Sign-in) close the rest. Scripts
  (`eval/run.py drive`) have no API tokens yet, so they need a server without sign-in.
- The env-var budget is a static heuristic: code that reads the environment another way
  (`syscall.Getenv`, scanning `os.Environ()`, importing `os` under another name) would evade
  it. Review is the backstop.
- The vendor breakers key on names: an IP literal of a vendor's API is not recognised. They are
  a self-imposed policy, not a security boundary. The substring match fails closed: a gateway
  URL that merely contains `anthropic.com` or `bedrock` is refused too and needs the opt-in.
- `render.yaml` has never been deployed and is inert until its sign-in variables are set (§ Exposure). Render's internal
  connection string may need an explicit `sslmode`, and the instance plans are not pinned.
- The image build and the Compose smoke test run only in CI's `docker` job (green on GitHub
  since PR 2, including the `COPY LICENSE NOTICE` line); the build sandbox has no Docker
  daemon, so they are not run locally. No `linux/arm64`
  image or release binaries are produced yet, so the Raspberry Pi path means building natively
  with `make build`.
- The Compose exposure check runs only where `docker` is installed (CI). An override file an
  operator adds after checkout is outside the repository; publishing beyond loopback there is the
  operator's own act.
- `govulncheck` runs only in CI: the build sandbox cannot reach `vuln.go.dev`.
- GitHub Actions and the build images are pinned to tags (`@v4`, `golang:1.26.10-alpine`), not to
  SHAs/digests, so an upstream retag changes what CI runs. CI's Postgres service images are pinned
  by tag and digest (`pgvector/pgvector:0.8.6-pg14@sha256:fb27…`, `…-pg16@sha256:ccc6…`, the
  registry's index digests on 2026-10-18); Compose's is pinned by tag only.
- Files: no quota. `store.GetFile` itself takes only a file id; scoping is enforced by
  `docs.WriteFile` (through a document in scope), and nothing yet stops another package calling the
  store directly (PR 4a's `TestImportBoundaries` pins who may import `store`). Upload slots are
  held only while the client keeps up the read floor, but the floor is low: 16 clients each
  sending 64 KiB per 30 s (about 35 KiB/s in all) can hold every upload slot for the full 15
  minutes. Uploads are buffered per extraction: two at a time (`ingestSlots`), each holding the
  file (≤ 64 MiB) plus a `.docx`'s decompressed text parts (≤ 128 MiB) in the child, and about
  60 MiB of server heap at the 16 MiB text cap (§ Files, memory of one ingest). Downloads use the same floor
  (64 KiB per 30 s, 15 minutes in all), so 4 clients reading just above it can hold all 4 read
  slots for 15 minutes, and ingest (which reads the stored file back) waits for them meanwhile.
  A stored file with no document is swept only at startup and only after an hour; a later PR that
  stores files of its own must teach `SweepOrphanFiles` about them.
- **LAB round 3 (sweep order and skips, multi-part requirements, crash journal, left-out
  evidence, raw deadlines):**
  - The sweep's order is lexical (BM25 of the plan's questions): generic questions ("what
    allegations are made") rank weakly, and a page that matters for a reason its words do not
    share (a summary sheet whose figure contradicts another document) can come late. Its skips are
    conservative: no page of the SEC or trust sets is a duplicate or boilerplate, so there they save
    nothing. Only an exact copy (case and white space folded, or the same lines of a table in another
    order) is skipped as a duplicate, and its coverage follows the page it copies (a copy of a page
    the sweep did not read is unread too); a page is boilerplate only with no letter or digit, or as
    a table of contents (a Contents heading, or dot-leadered heading-like entries with page numbers that
    never fall) with no table row and no sentence. The count of skipped pages and their
    documents is a Limitations line.
  - One call a visit is an inference from the bench's token counts (about 26,000 input tokens a
    visit, three calls), not a measured split. The lab-r4 shares are sized from bench5's costs per
    call (a visit $0.012–0.014, a committee cycle $0.30–0.48, an analysis and its redraft about $0.15)
    and are not yet measured on a rerun.
  - A salient finding is judged by its tool, severity, typed relations, figures or the words of its
    claim ("deviates", "does not", "fails to"): a left-out finding stated otherwise is not placed, and
    a claim that merely says "does not" is. The pass places at most 40, one redraft per issue, the
    issues with a conflict or the strongest left-out finding first, from the 40% of its USD its
    analyses leave for it (`placeShare`); what it cannot place is one Limitations line.
  - A requirement's parts must be the document's words, in its order, one passage: only white space
    and list markers between them, or, after a sentence, at most two whole sentences (400 bytes); never
    a heading or a list's introduction between them. A fragment is not refused: it is read and quoted
    in about 3 KB of the text before and after it (at most 8 KB). **An override or a governing
    introduction more than about 3 KB away, or in another clause, a definition or a schedule elsewhere in
    the document ("9. Clause 4 shall not apply …" pages later, a defined term's meaning), is not seen.**
    A requirement whose sentence or paragraph starts before the window, or whose window starts at a list
    item, is admitted only at low confidence, for a lawyer's review, so a requirement in a long paragraph
    (over 3 KB above it) or deep in a long list is always gated. Whether the text imposes the requirement as quoted (a
    negation, an override) is the section check's judgment, one critic call per `check_deviation` call:
    the context makes the governing words visible and the check reads them, which does not make a
    verdict right (an analyst who misreads a requirement the document does impose is checked only by
    the claim's quoted window and prosecute).
  - The join asserts what the figure judge says (r7): its verdicts are a model's, checked for form
    (`CheckVerdicts`: a verdict only for a pair Go proposed, one per pair, a reason that quotes both
    passages), not for truth. A judge that calls two quantities one makes a conflict the memo is asked to state (its reason
    shown); "unclear" is disclosed as a low-confidence pair for the lawyer; pairs past the cap or that
    the budget could not judge are counted, not judged.
  - Worksheets (r7 H1, M1): an operand traces only as a cited value's ref, a traced worksheet's result
    in its unit, or a literal the cited quotes state as its unit says, by the figure reader's kind
    (money in its currency, a percentage, a plain number read as a count or a bare number or multiple,
    a date they state, a period read as a duration in that unit, or a day count that is the days between
    two dates they state); never a number read as other, an identifier (r8 H1: "Section 30" is no
    30-day period, "Section 12" no multiplier). A result traces a memo figure only of its own kind and
    currency (a period by its amount never). A period the record does not state (a court rule not among
    the cited quotes) leaves its deadline untraced: cite the rule. `product` takes any numbers with at
    most one amount of money, `ratio` two in at most one currency, `date_add` days, weeks, months or
    years (a month or year on from a day the target month lacks is its last day: January 31 + 1 month
    is February 29, 2024; r9 M1). A duration's unit and a multiple's sign must cite the words beside
    the span that state them (`unitFrom`, r9 H1), checked by position and shape only (`statesUnit`, r10
    L2): a unit's words start at the first word right after the digits (only spaces, ")" or "-"
    between, never "(": "Rule 12(b)" states nothing) and that word has two or more letters; a
    multiple's sign is one character right against the digits or a space away ("1.5x", "x 1.5"); no
    word list, so "of" in "Section 30 of" can be cited, and is caught where the words are compared or
    shown. A memo's period (read as a duration) traces only to a cited duration of that amount and
    unit whose words agree with the memo's as written (case, punctuation and a plural aside), a
    worksheet's period, or the days between two cited dates, never to a section number's digits (r9
    H2); a cited period whose words differ ("days" against "business days" or "of") makes it limited:
    listed first in the block below and its findings gated (r10 M1).
  - The Computed figures block (`computed.go`, r10, r11): one block at the end of Limitations lists,
    once each with the sections that state it, every figure a section states that traces only
    through a worksheet, whatever its operands (a ref's value is the figure reader's reading of it,
    its scale and kind; a literal is a cited reading) and whatever form the figure is written in (G8
    decides: each paragraph is traced a second time without the worksheets, and a figure that then
    fails is listed with the worksheet it traces through alone; a `{W…}` handle's worksheet is listed
    too), and every memo figure that traces only by a reader's label (a period whose words differ
    from the record's, an identifier with a cited quantity's digits, r10 L5). A worksheet result the
    memo writes at a scale ("$228.5 million") is that worksheet's item ("written in the memo as …"),
    not a separate scale line (r11 L5). Each input is written as the memo writes figures with its
    finding and a short quote, cut at a space, comma or pipe, a table or CSV row's from its first
    cell, a date's wherever the quote writes it ("$212,500,000 [F1] “…purchase price of
    $212,500,000”"). Items resting on a unit, multiple or scale the figure reader read from words, on
    a count or number's kind alone (no unit words: it may be a section number), or on the days
    between two cited dates come first ("Check first"); ⚠ marks, at the top, an input whose cited
    words are not its unit, a count used as a period (by its unit, or with none as `date_add`'s or
    `business_days`' days, r12 M1), or a result over inputs read at a scale that the memo writes out
    digit for digit, by its `{W…}` handle, or at a scale from other words ("$8 million" over "$5
    billion" read ×10⁶), and gates the findings it rests on, as a memo figure's would be (r11 H, L1,
    r12 L1); a memo scale whose words agree with every input's ("3,120 million" over "2810, EUR
    million") is listed, not gated. A ref's quote is the span admission matched its value to
    (`Reading.Stated`). Arithmetic over money, percentages and dates the quotes write out follows
    ("Arithmetic"). `numbers` keeps a value's "business days" (`date_add` refuses it; `business_days`
    counts it). So every figure that one wrong unit, scale or kind label could change is shown to the
    lawyer with the words it rests on; whether the label is right is the lawyer's check, since Go
    never decides what words mean.
  - A figure written at one scale beside a cited one at another (the memo's "$5M" for a cited
    "$5,000,000", or its "$5,000,000" for a cited "$5 million", or "$5 per share" scaled from "per
    share") is limited, not traced: disclosed in Limitations and its citing findings gated for a
    lawyer (r7 M4, L2). A scale's words must be adjacent to the figure (right after it or after its
    currency code, earlier in its sentence, or its passage's first line when that is a header, no
    sentence and no figure of its own the words scale, over a table row: a line split by as many tabs,
    "|" or commas (two or more, neither line a sentence) as a row next to it; r8 M2, r9 L3),
    with a letter or a "000" ("($000)"; a bare "$" is none: r7 M2, L1). A caption over prose ("Amounts
    in millions" above a sentence) is no header: such a figure is unclear, gated.
  - The raw-date rule is paragraph-wide: a list that states the raw date and the rolled deadline on
    separate lines without a blank line between is one paragraph and passes; one with a blank line is
    flagged.
- **LAB improvements (sweep pages, parallel sweep, deviations, G8 lists, coverage pass):**
  - Answer-sized pages cost about three times the visits of 2,000-token pages on long documents;
    the parallel sweep pays that in time, not budget. No real model has run them yet: the LAB rerun is
    the owner's.
  - A parallel visit holds a quarter of the sweep's USD. One call's worst case (8,192 output tokens)
    is a few cents on a small model but about $0.13 on a mid-priced one, so on a small budget a
    quarter may not fund one call: such a visit is refused, then read in the serial fallback with
    all that is left (its findings admitted after the parallel visits'); what the refused attempt
    spent before its refusal is spent twice. A
    call billed above its own worst-case bound (a pricing anomaly) can make which later visits get a
    full grant depend on timing. The sweep is one step: a process stopped mid-sweep re-reads it all.
  - A table page's header is the first row of the run of rows the page continues, ended by a blank
    line, prose or a `#` marker: two tables back to back with no line between them read as one
    (their header is the first's), as the chunker reads them, and a table with a blank line inside it
    reads as two. A page deep in a long table reads back to the table's start (doubling windows):
    a 16 MiB section's last page reads up to the whole section once.
  - The coverage pass judges "analysed" by the issue's class (Conduct and Requirement need an
    analysis); an analysis is the model's reasoning, not a verified quote: its facts still cite
    verified findings, and its rules are flagged as external authorities when the record lacks them.
    It runs once, one analysis and one redraft per touched issue, within its 10% share (`Allocation.Cover`); issues it does
    not reach are disclosed, not analysed.
  - An omission is confirmed absent by the paraphrase judge on the reviewed document's closest
    passage: a provision stated far from where its words cluster can be judged absent wrongly (the
    finding then quotes the passage it was judged against, for the lawyer to check).
- **Documents (PR 3a):**
  - There is no OCR: images and scanned pages are read by the vision model (§ Vision
    transcription; its limits are below). A text-layer PDF whose layer is wrong (a scan with an invisible, bad OCR
    layer) is taken as is. Text that the PDF reader puts in an odd order (columns, tables) stays
    in that order.
  - A text upload that happens to start with `%PDF-` is read as a PDF and refused; posted text
    (`IngestText`) is always text.
  - Every package whose tests ingest a PDF or `.docx` needs a `TestMain` that calls
    `docs.MaybeRunChild()` (the test binary is the reader child), as `internal/docs` does;
    `cmd/biglaw`'s `main` calls it first (`TestBinaryIsItsOwnPDFReader`). Without it extraction is
    an error, not a crash. Only the 2-minute timeout bounds a child's CPU: a 438-byte PDF whose page
    tree is its own kid holds one of the two ingest slots for 2 minutes (two such uploads every
    2 minutes stop ingestion; there is no quota or rate limit before PR 8).
  - The child's hard memory cap is Linux-only (`RLIMIT_DATA`; elsewhere only a 512 MiB
    `SetMemoryLimit` target, which the runtime can exceed) and is off in race-detector builds (whose
    shadow memory counts against it; `make ci` re-runs the child tests without `-race`). Outside
    Linux the child is the binary at the server's path, not `/proc/self/exe`, so replacing the
    binary while serving changes the reader there. The
    parent-death signal is tied to the server thread that started the child: Go rarely ends a
    thread, but if it did the child would be killed and the upload reported unreadable. The
    250 ms parent-pid check covers every platform.
  - A document's text is capped at 16 MiB (more is 422, split the document), but there is no
    per-matter quota: a 144 KB PDF can store 15 MiB of text and about as much again in chunks.
  - `.docx`: footnote and endnote references are not marked in the body (the notes follow it, by
    id), so a quote spanning a reference's superscript number reads without it; text boxes keep
    their `mc:Choice` copy only; a run whose formatting history (`<w:rPrChange>`) was hidden is
    treated as hidden; text in headers, footers and comments is not read; a document whose `w:`
    namespace is not declared reads as having no text (Word always declares it); Symbol-font
    `<w:sym/>` codes (private use) are dropped, not mapped to Unicode.
  - Table hits return a chunk of several rows (≥ 1,024 bytes), not one row: per-row chunks would
    need the column list stored once per table to stay linear.
  - Citations carry **no page number**: v1's ladder could use a cited page to pick the judge's
    window (`TestCitationWindowLocatesRegion`'s form-feed variant, dropped); v2 locates the window
    by the quote's words, and a PDF cite has no pinpoint page.
  - Section ids are positions in the section tree (`s0`, `s1`, …) of the text as ingested; a
    change to the heading rules does not renumber stored documents, which keep the ids they were
    ingested with. Re-ingest to pick up new rules.
  - **Lexical search alone does not meet gate E4** (0.882 < 0.95; § Documents), and with no
    `embed` role search is lexical only: a question that shares no word with the passage is not
    found (`REG-05`'s fee-table row by its title; most question misses ask about a date or figure
    the passage states in other words). Each query scans the scope's chunks once per term (at most 32), so a very large scope makes every query slower
    (`TestSearchCost` prints the figures; 32 terms on 4 MiB took about 0.5 s here) up to the 10 s
    timeout, which then refuses it; nothing caps a scope's size. Quoted phrases and `-exclusion`
    are not supported (their words are searched).
  - Heading heuristics are guesses: `999 Main Street` or `10 May 2026 the parties met` at a line
    start (a wrapped PDF line) is still a decimal heading, and a sub-clause whose title starts in
    lower case (`1.1 the Supplier shall`) is now body text of its parent.
  - The judge's passage is document text: `verify_quote`'s judge prompt (PR 4a) fences it and
    declares it untrusted data, but a model can still be swayed by text addressed to the judge (`.docx` text hidden by
    formatting, not by `<w:vanish/>`, reaches it too).
  - The tolerant rung folds case, so `ACME` verifies against `Acme` (at 0.95).
  - **Line budget:** `internal/docs` measures 1,733 lines (1,471 plus `http.go` 262) against the
    1,110 DESIGN plans for what 3a builds plus api's `documents` 200 (moved here, covering the
    routes PR 4b still adds), `docs/ooxml` 174 against its reader's 120, `store` 641 against 580
    and `cmd` 528 against 505. With the 4a adapters and the remaining documents routes counted,
    the plan totals 14,866–14,896, under the 15,500 ceiling (final) by 604–634; with PR 3b's
    pgvector estimate (270–405 lines, DESIGN § PR 3b pgvector estimate) it is 15,136–15,301,
    **under by 199–364**; the ceiling will not rise again, so a later overrun must be trimmed. `scripts/loc.py`'s
    actual count is far below it.
  - Migration `0002_docs.sql` changed during this PR's review (the delete trigger, the dropped GIN
    indexes, prose search text stored once; round 2 changed no SQL): a dev database that applied an earlier version
    refuses to start (checksum drift); recreate it.
  - `ledongthuc/pdf` is a small, lightly maintained library pinned at a 2025 pseudo-version; it
    is the second of the three allowed dependencies.
- Heap-measuring tests share one test process. `TestDownloadBounded` used to sample the heap
  300 ms after starting its downloads, which under `-race` or CPU load caught the slot holders
  still fetching chunks (up to +31 MiB against its 24 MiB bound, r1 L-10): it now waits until all
  4 holders are blocked writing and reads the live heap after a GC (+8 MiB in 9 runs, 4 of them
  beside 6 busy loops on 4 cores). It does not bound the transient peak while chunks are fetched
  (about 6 chunk-sized buffers per slot, measured in review).
- Test schemas are dropped by `t.Cleanup`; a test binary that is killed (SIGKILL, timeout)
  leaves its `t_<hex>` schema behind. Drop them with
  `psql -c "select 'drop schema '||nspname||' cascade;' from pg_namespace where nspname like 't\_%'" -At | psql`.
- Schema-per-test isolation cannot isolate Postgres extensions, which are database-wide:
  `storetest` creates `vector` once per database (outside any test schema, cached per DSN) and puts
  its schema after the test's on the `search_path`, so a test's unqualified name that its schema
  lacks would resolve in that schema (`public` by default) too. A future extension must follow
  the rule in CLAUDE.md.
- No healthcheck for the biglaw container (distroless has no shell); Compose reports it "Up"
  even when `/health` is 503. No access log; logs are text.
- `/health` is unauthenticated and reports the version string. Images built by Compose report
  `2.0.0-dev` (no version is passed at build time).
- Postgres is required, including on a Pi. There is no embedded-database mode.
- **Hybrid search and embeddings (PR 3b):**
  - **Gate E4 has not been judged.** The committed tape is the self-test hash embedder's
    (§ Documents), so `TestHybridRecallProofRuns` skips, and whether hybrid search reaches 65/68
    is unknown until the owner runs `make e4-record` with a real model. Fusion can also make recall
    **worse** than lexical alone: with the bag-of-words self-test embedder it fell from 60/68 to
    50/68. If the real tape misses the gate, the fusion constant or weights, not the gate, are the
    next question (owner).
  - The tape is recorded at the model's own length, as `serve` embeds (1,536 dimensions for
    `text-embedding-3-small`: a tape of about 4 MB, three times the old 512-dimension recipe's).
    A model can be run only at its own length: there is no dimensions setting.
  - Every embedded chunk in scope is a vector candidate, so a hybrid search returns `k` hits even
    for a query nothing matches; callers must not read "a hit" as "relevant". No index: each query
    computes the distance to every embedded chunk in scope, under the same 10 s timeout, and the
    vectors cost about 8 KB of disk per chunk at 1,536 dimensions; see § Documents, "Scale and
    disk" for the measured figures and the ceiling (about 100,000 chunks per matter on x86).
  - The lexical-only rule (`2/(60 + lexical rank)`) makes a lexical-only chunk equal to an
    embedded chunk ranked the same by both legs; it cannot gain from meaning, so for a question
    that shares no word with it, a lexical-only document is still not found (as with lexical
    search alone). It is also the **best case** for that chunk: its vector rank is taken to equal
    its lexical rank, so until the backfill embeds it a lexical-only document whose chunks share a
    query word outranks embedded chunks that are semantically closer. Measured
    (`TestLexicalOnlyDisplacementMeasured`): the vector leg's first match, lexically weak, is #5
    behind 8 such chunks and **#10** (off a page of 8) behind 20; #1 once they are embedded. `serve`
    logs how many documents are lexical only after its startup backfill. A neutral imputation would
    trade this for part of the burying it was chosen to avoid (owner's call).
  - A vector space is known only by the model id: if the model behind an id changes (an Azure
    deployment upgraded in place, an Ollama `:latest` re-pull), old documents are not re-embedded,
    and are compared across models (same length) or silently drop out of the vector leg (another
    length) while `/health` still says `hybrid`. Change the id when the model changes, so the
    backfill re-embeds.
  - Paging re-embeds the query for each page (one call, about $0.0000002 at
    `text-embedding-3-small`'s rate): a per-server cache of the last query's vector was left out,
    because its timing would tell one user whether another just searched the same words.
  - Embedding usage is priced on every call, but only an ingest's usage is recorded (on the
    document, and only for a run that embedded every chunk: a failed run's billed calls are not
    stored), and a search's query embedding is charged to the task through `retrieve`'s result
    (PR 4a; a direct `docs.Search` call outside a task is charged to nothing). An embeddings
    answer with no `usage` has an unknown cost (nil); a **chat** answer without `usage` still
    counts 0 tokens and reads $0 for a priced model (PR 2's behaviour, unchanged here).
  - Embedding runs inside the upload request, after the ingest slot is released: a 16 MiB
    document is about 190 calls of 64 chunks at the 1,400-character chunk size, up to about 780
    for short clauses (`TestSearchCost`'s 4 MiB of them is 12,426 chunks), so its upload answers
    only when they finish (or fails over to
    lexical only, with the note, if the client goes away). The backfill runs only at startup: a
    document whose embedding failed stays lexical until the next start. Two servers on one
    database may both backfill the same old document (twice the cost, same result, **as long as
    both use the same `embed` model**: two servers backfilling under different models can interleave
    on one document and leave it labelled with one model over a mix of both models' vectors; run one
    embed model per database). Changing the
    `embed` model makes every document lexical until the backfill re-embeds it; a re-embed that
    fails turns a document embedded by the old model lexical only (its old vectors are no longer
    trusted, and are overwritten when it is next embedded).
  - The key-shape refusal of `BIGLAW_MODELS` ids is a heuristic: a key in another shape is still
    shown and logged, and a real id with 24 letters and digits in a row (an Azure deployment name,
    say) is refused at startup; name such a deployment with a `-` or `_` in it.
  - Inputs are cut at 4,096 characters; an embedding model with a short context (some 512-token
    BERT-style models) may refuse a 1,400-character chunk (HTTP 400 → `model_unavailable`), which
    leaves that document lexical only.
  - **Compose volume:** the Postgres image changed from `postgres:16-alpine` (musl) to
    `pgvector/pgvector:0.8.6-pg16` (Debian, glibc). Collations differ between the two C
    libraries, so a `pgdata` volume created by an earlier pre-release Compose stack should be
    recreated (`docker compose down -v`; v2 has no release yet) rather than reused.
  - **Line budget:** PR 3b adds 401 production lines (3,894 → 4,295) against DESIGN's 270–405
    (§ PR 3b pgvector estimate), at its top: round 1's fixes (mixed-model re-embeds, lexical-only
    fusion, query checks before embedding, vector and token guards, key-shaped ids) were paid for
    by deleting the dimensions option, the "lexical only" startup log, storetest's extension cache
    and doc comments that repeated this README. Per file: `model/embed.go` 118, `docs/embed.go` 131, `docs/search.go` 27, `docs/ingest.go` 13, `model/fake.go` 39, `cmd/biglaw/main.go` 22, `config.go` 18, `model/client.go` 9, `store.go` 9, `storetest` 15. Plan total
    14,866–14,896 + 401 = 15,267–15,297, under the final 15,500 ceiling by 203–233.
  - **Every chunk of every document goes to the embedding endpoint**, at ingest, and so does
    every search query: with a hosted embedder (OpenAI, a gateway) the full text of privileged
    matter documents leaves the machine, as the chat calls' prompts will. Setting the `embed`
    role for the first time also sends **every stored document** to it (the startup backfill).
    The embed role uses the same `BIGLAW_MODEL_URL` (and vendor breaker) as chat: embeddings
    cannot go to a local endpoint while chat goes to a hosted one. Run a local embedder (Ollama's
    `nomic-embed-text`) to keep the text on the machine.
  - There is no quota, and without sign-in no auth: anyone who can reach the port (with sign-in, anyone signed in) can upload documents and
    make the server pay to embed them (16 MiB of text is about 4.2M tokens at 4 bytes a token: $0.08 at
    `text-embedding-3-small`'s rate, $0.55 at `-large`'s), and every search makes one call.
  - A matter's own documents can steer the vector leg: a chunk written to sit near many queries
    (a "hub") is returned for them, crowding out evidence (inside the scope only: G1 holds). Three
    documents in a row that the endpoint always refuses stop the startup backfill early, so later
    documents stay lexical until the next start (the log counts them; each carries the note).
  - Time bounds: embedding one document, at ingest or in the backfill, stops after 10 minutes
    (then it is lexical only, with the note), a query's embedding after 30 s (then 504
    `model_timeout`; `TestEmbedWaitsBounded`), and an answer with vectors over 16,000 dimensions
    (pgvector's limit) is refused.
- **Vision transcription (PR 3c):**
  - **No vision model has been run.** There is no vision key in the build environment: every test
    uses `model.Fake` or `httptest`, so the prompts, the 0.75/0.9 thresholds, the disagreement rule
    and the 4,096-token output cap are not calibrated on any real model (owner's live check:
    § Vision transcription).
  - Agreement and the disagreement rule are checks against invention, not proof of accuracy: two
    readings that make the **same** mistake (the same misread figure) agree and are stored at normal
    confidence. Where the readings differ on a figure, a negation or modal, a dropped line or word
    order, the page is low confidence and one reading's version is stored (the first), not a merge:
    the note and `disputed` name what the other reading said differently, and the engine's finding gate
    routes such quotes to a person. The rule is conservative: any 5 differing words, or any word
    order difference (two columns read in another order), make a page low confidence, so a long
    page with harmless differences is gated too. Words other than figures and the listed negations
    and modals can change meaning (a name, "Applicant" for "Respondent" in place): one such
    substitution alone is not caught. Pages over 2,000 words are not compared in order and are
    always low confidence.
  - Masking over-masks: where the other reading has a mark, every word of the kept reading outside
    the alignment becomes `[illegible]`, including harmless differences, reordered words and, where
    the alignment is ambiguous, words the other reading did read ("John Smith pays John Smith Jr." vs
    "John Smith pays [illegible] Jr." stores both of the second "John Smith" masked); on a page too
    long to align (over about 2,000 words a reading), every occurrence of a word the other reading has
    fewer of.
  - The stored reading keeps the model's line-end hyphens, ligatures and quotes: a quote of
    "release" does not verify against a stored "re-\nlease" (as with any PDF text layer). A word
    hyphenated at a line end in one reading only ("munici-\npally" vs "municipally") is a differing
    word, and a hyphen read in one reading only ("re-sign" vs "resign", "non-compete" vs "non
    compete") is one too: like any single non-figure word, not low on its own.
  - Benign differences still gate a page: on the round-2 verifier's synthetic Ontario order, 7 of 17
    one-at-a-time harmless variants are low (a page footer, list numbers or cents one reading drops,
    a signature read as `[illegible]`, a party block's or a date's word order, a court stamp only one
    reading has; formerly 8, hyphenation now passing), random pairs of those 88.7% (round 3, compare-only
    folding; 88.1% in round 2), and pairs carrying only formatting noise (reflow, hyphenation,
    ligatures, quotes, markdown, one letter slip) 0 of 2,000 (formerly 1,042).
    These are a model of noise, not a measurement of any vision model; low confidence only gates
    quotes (the engine's finding gates).
  - A waiting document whose image the endpoint refuses holds the queue for 3 job runs (about 30
    minutes) before it fails and the next is tried: a refusal cannot tell the image's fault from the
    request's.
  - A model's **refusal or commentary** ("I can't transcribe this image") is not recognised: if
    both readings phrase it alike they agree, and it is stored as the page's transcript (capped and
    disclosed like any transcript). Telling commentary from text needs a judgement call BigLaw
    does not make here.
  - A WebP image is checked by its magic bytes only (no decoder in the standard library), so a
    damaged one is stored or sent and fails at the model instead (422 when it answers 4xx).
  - **Prompt injection:** text in an image is read by the model like any instruction. A page that
    tells the model to write something else can be obeyed by both readings, which then agree; the
    result is stored as a machine transcription (capped, disclosed), never as text-layer truth, and
    downstream prompts must treat it as untrusted document text (PR 4a).
  - Scans are found only as image XObjects in a page's own resources: inline images (`BI … EI`),
    images inside form XObjects, and pages drawn as vector paths read as blank or unsupported. A PDF
    page with a form feed inside its text layer makes a mixed PDF's pages unplaceable, and then none
    of its scanned pages are transcribed (the note still lists them). TIFF, BMP, HEIC and JPEG 2000
    are not read (no decoder without a dependency). A page with a short text layer and a small
    JPEG (a logo beside a one-line letter under 80 characters) counts as a scan and is read, its text
    layer kept after the transcript (in a one-page document with no vision role it waits, 503).
    A page with 400 or more characters of text layer is never read, whatever image is under it: a
    production page whose legend and Bates number reach 400 characters is stored as its legend only,
    with the note "an image on the page was not read … its text may be missing"; re-export it without
    the legend, or as images, to have it read.
  - **Mixed pages (80–399 characters over an image)** are read even when the image is a logo or
    stationery, not a scan: two vision calls per such page, the image sent off the machine, and a
    logo's words (a firm name) are added as transcript lines. The text layer is subtracted by whole
    reading lines (§ Vision transcription gives the rule): a line is dropped only when its letters and
    digits are the layer's own whole words, so a line that differs only in spacing, hyphens,
    punctuation or case is dropped and the layer's version stored. Kept, by design, as a disclosed
    duplicate (`exact@transcript`, capped): a short line (under 3 words or 12 letters and digits) that
    is not a whole layer line, so a reading that wraps differently from the layer ("Applicant
    monthly" across a layer line break, "Dear Ms. Smith" beside a longer layer line), a legend word or
    Bates digits read alone ("CONFIDENTIAL", "0001") or a figure with a sign the layer lacks ("10.5%")
    is stored twice; a figure whose separators differ from the layer's
    ("1250.00" for "1,250.00"); a reading line that breaks inside a word the text layer glued (a PDF
    whose lines are placed by text-positioning operators reads "consent orderdated"; a reading line
    ending "consent order" is kept, and a quote of it stays `exact@transcript` 0.85 because the layer
    copy is not whole words); a line the reading changed (a figure misread, or rewritten by text in
    the image: prompt injection) beside the text layer's own (that line of the letter twice, once as transcript, capped and
    disclosed); a line mixing a sidebar name with the letter's words (two columns read across) is kept
    whole; a longer body line whose words are all a run of the legend ("Subject to protective order")
    is dropped, its words staying on the page through the text layer. A quote moves to a later layer
    copy that a space and digits follow ("paid $500" into "paid $500 000", a space-grouped figure):
    the right edge cannot refuse a space before a digit without refusing every "October 2, 2026"; it
    needs a transcript figure whose longer, space-grouped form the layer holds later. The added lines are stored before the text
    layer whatever their place on the page. Reading only a mixed page's image cannot see text drawn
    over it; a document over 20 pages with an image reads the first 20 in page order, mixed pages
    included, so a text-less scan past them is not read even when a mixed page before it was: its note
    names the 20-page cap (`TestVisionCapNoted`). Only an `exact` quote moves to a copy outside the
    transcript; a `tolerant` one is still placed at its first copy (`@transcript`, 0.85, when that is an
    added line).
  - A mixed PDF stored while no vision role is set keeps its scanned pages unread for good: the job
    takes only documents with no text layer; its note says to upload it again.
  - **Images leave the machine** with a hosted vision model, as chat prompts will. Setting a vision
    role for the first time also sends every waiting document to it (the startup job run).
  - There is no quota, and without sign-in no auth: anyone who can reach the port (with sign-in, anyone signed in) can make the server pay for
    up to 40 vision calls per upload (20 pages × 2 readings), each up to 4,096 output tokens plus
    its image: at most 163,840 output tokens, $1.05 at `qwen-vl-max`'s output rate ($6.40/M), $1.64
    at `gpt-4o`'s ($10/M), before image input.
  - The calls of a refused transcription (422, or a model failure) are priced by the client but
    stored nowhere (the document is not stored): the operator's log line carries their tokens and
    cost; a failed job's are in the same log. Ingest happens outside any task, so a task's
    ledger never counts these (PR 4a decision; the document records them).
  - **An upload's vision read runs on its request** (DESIGN put it in the leased job; the async
    202 path is PR 8's, PLAN PR 8): the client must keep the connection open for the whole read (up
    to 40 sequential calls, bounded at 10 minutes, then 504 `model_timeout`). A client or proxy
    timeout below that (a reverse proxy's 60 s default) cancels the upload, deletes the file and
    discards the calls already paid; a retry pays again.
  - Memory per vision ingest: the file plus one page's images about three times over (base64 and
    the JSON request), at most about 64 + 3 × 20 MiB, for each of the 2 reader slots; a document
    waiting for a reader holds its extracted text (≤ 16 MiB). Images near the 20 MiB cap exceed
    some providers' own limits (OpenAI's 20 MB per image after base64): those are 422 when the
    endpoint answers 4xx.
  - The job runs every 15 minutes and at startup, not when a role changes while serving (a role
    change needs a restart anyway); a failed document stays failed until the operator re-queues
    it (the SQL above). A finding resting only on `lowConfidence` quotes is gated for a lawyer (§ Memo workflow).
  - **Line budget:** PR 3c adds 572 production lines (4,295 → 4,867: 482 as built, then 90 in
    the round-1 fixes) against DESIGN's 310 for it (`docs` 300 + `cmd` 10; the `model` image parts
    landed in PR 2 and contract v1's `api` 40 is PR 8's): **262 over**. Round 1 per file:
    `docs/vision.go` +68 (the disagreement rule `compare`/`diff`/`lcs` with masking, the 4xx
    refusal, the key scrub, the dimension cap, mixed-PDF failure notes, usage always recorded, the
    refused-spend log), `docs/ingest.go` +17 (reader slots, stamped scan pages, SQL `NULL`),
    `docs/jobs.go` +2, `docs/child.go` +1, `docs/scanpdf.go` +1, `model/errors.go` +1. The prompts
    are data (`vision_prompts.txt`, not counted, as `prices.json` is). Plan total 15,267–15,297 +
    572 − 310 = **15,529–15,559**: over the then-final 15,500 by 29–59, so it went to the owner, who
    raised the ceiling to 15,600 (decision 11; headroom 41–71, which PR 3e's embed endpoint must fit).
    Round 2 adds 33 (4,867 → 4,900: `docs/vision.go` +25 for the positional mask `align` replacing
    `lcs`, figure tokens, marks, line-end hyphens, a damaged page noted in a mixed PDF and the 4xx
    wording; `docs/scanpdf.go` +5 and `docs/ingest.go` +3 for page-shaped legends and the unread-image
    note; `docs/jobs.go` 0): plan **15,562–15,592**, headroom 8–38 for PR 3e's 30–40. Round 3
    removes 2 (4,900 → 4,898: the page-shaped rule out, the alignment's wildcard and second walk and
    the damaged-page note in): plan **15,560–15,590**, headroom 10–40. Round 4 (owner decision 12:
    ceiling 15,650, long-legend scans read now) adds 20 (4,898 → 4,918: `docs/vision.go` +14 for
    `beyond` and the mixed page's notes, `docs/ingest.go` +3, `docs/scanpdf.go` +3 for `mixedMax`):
    plan **15,580–15,610**, headroom 40–70. Round 5 adds 3 (4,918 → 4,921: `docs/vision.go` +1 for
    `beyond`'s letters-and-digits comparison and CR strip, `docs/verify.go` +2 for the quote's copy
    outside the transcript; the cap note and "no call, no Transcript" 0): plan **15,583–15,613**,
    headroom 37–67; with PR 3e's 30–40, 15,613–15,653: PR 3e must stay at or under 37 lines. Round 6
    removes 3 (4,921 → 4,918: `beyond`'s whole-word rule replaces the word-run and 8-character rules,
    the "adds nothing" note and the whole-word quote move in, paid for by compacting `pageList`, the
    WebP sniff and the disputed-word cap): plan **15,580–15,610**, headroom 40–70. Round 7 adds 1
    (4,918 → 4,919: `beyond`'s short-line rule, a short line subtracted only as a whole layer line;
    the quote move's figure-separator edges 0): plan **15,581–15,611**, headroom 39–69; PR 3e must
    stay at or under 39 lines.
- **Model client (PR 2):**
  - Every test uses `httptest` or `model.Fake`. `Chat`'s caller is the agent loop and `Embed`'s is
    documents; the only real endpoints exercised are the owner's bench and LAB runs. Behaviour against
    other real endpoints (Ollama, vLLM, other vendors) is unproven until the owner's gates run them.
  - **Every call is charged, including on your own GPU** (owner decision). An Ollama model under
    an open-weights family name (`qwen2.5:14b`, `mistral-nemo`, `deepseek-r1:14b`) is charged at
    that family's hosted rate, so the USD cap counts money not spent unless you set the model to
    `{"in": 0, "out": 0}` in `BIGLAW_PRICES`. A model with no row (`llama3.1:8b`, `gemma3`, a
    fine-tune) is unknown, never $0: check the startup `model pricing` line.
  - A model with no price (unknown, or `null`) has an unknown cost: the USD cap cannot count it,
    and only the call cap bounds it. One warning is logged per model per process.
  - The built-in rates are v1's list prices (early 2026) and were not re-verified for v2; they
    may be stale. v1's substring families became prefix keys, so a name that only *contains* a
    family (`my-haiku-finetune`) is no longer priced as that family; `vendor/model` ids are
    still matched. One carried vector changed (`gemini-2.5-flash-lite-preview` now takes the
    exact flash-lite row's 0.10/0.40, not v1's contradictory class rate 0.075/0.30).
  - Inside one `BIGLAW_PRICES` entry, Go matches `in`/`out` case-insensitively and a repeated
    field keeps the last value; an exactly repeated id keeps the last
    entry (Go's JSON decoder), a case-variant repeat is refused. Errors name no id, so a typo in
    a long file is found by bisecting it.
  - Upstream 400/413/422 (a context-length or schema refusal) is `ErrUnavailable` "HTTP 4xx" with
    its `Status`, not retried by the client; the agent loop compacts the transcript and tries
    again, twice at most (§ Agent runtime). Tool-call names are checked against the tools offered by the loop.
  - A 200 whose body is an error object (some gateways) is `ErrNoContent` "no choices", not
    retried, and an OpenAI `refusal` is reported as an answer with neither text nor tool calls (its
    text is dropped); both are 422 `no_content` and their usage, when the body gave one, is charged
    (PR 4a decision). A 200 whose body cannot be read (one malformed `usage` field, a string or a
    fraction, is enough) is `ErrUnavailable` with `Status: 200`: the answer is lost, and the task
    ledger counts it as one call of unknown cost. An answer with no
    `usage` object counts as 0 tokens, so a priced model's call reads $0.
  - A call that fails outright (non-200, transport error) carries no usage or watt-hours: a local
    GPU's energy for a failed generation is not metered. Watt-hours assume one machine at a fixed
    draw: a hosted endpoint with `BIGLAW_LOCAL_WATTS` set records watt-hours it did not use.
  - Cache accounting assumes `prompt_tokens` includes cached and cache-write tokens (OpenAI's
    convention). A gateway that reports them separately would make input tokens read low.
  - `Retry-After` is read only in its seconds form; an HTTP date falls back to the backoff.
    Without a caller deadline a call can take up to 3 × 5 minutes plus waits.
  - Answers whose `content` is an array of parts, streaming, and `n > 1` are not supported (the
    first choice is used). The number and size of tool calls in an answer are bounded only by the
    32 MiB answer cap; the agent loop must cap what it executes.
  - `strict: true` is sent only to `api.openai.com` (PR 4a); every other endpoint gets the same
    strict-shaped schema without the flag. `parallel_tool_calls` is passed through as given.
  - `model.Fake` hands out an agent's replies in call order, so concurrent calls *for the same
    agent* get replies in a scheduling-dependent order; script those calls sequentially, or route
    them by content (`Fake.Route`).
  - **Line budget:** `internal/model` measures 714 lines against the 610 the v2 design planned
    (670 in revision 5, less 60 for the price table as data): 104 over its share. `cmd/biglaw`
    measures 505 against PR 1's 477 (+28). The owner raised the ceiling to 14,500 and DESIGN
    records the move: measured the review's way (PR 1 as built, 1,161, plus PR 2's growth over
    plan, the remaining packages at their DESIGN budgets, cmd's import-v1 reservation intact), the
    plan totals 14,245 of 14,500, 255 lines of headroom (superseded: PR 3a's figures and the
    15,500 ceiling are under § Documents above).
