# BigLaw 2

> **Status: under construction.** This README describes only what is on disk.
> Everything marked **planned** is specified in the v2 build plan and does not exist yet.
> BigLaw 1 (the DyTopo platform) is frozen on the branch `archive/v1-final` (a branch, because
> tag pushes are blocked in the build environment).

BigLaw 2 will be a legal AI workbench for small firms: memos, contract review and diligence
tables in which every factual statement cites a quote that the software has verified against the
matter's own documents, with a lawyer approving every deliverable. It is one Go binary plus one
PostgreSQL database. Licensed Apache-2.0 (`LICENSE`, `NOTICE`).

## What exists today

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
| Manage | `internal/engine/specset.go`, `manage.go`, `manage_validate.go`, `diff.go`, `skills.go`, `custom.go`, `remote.go`, `hookrules.go` (+ `skills/`, `specs/use_skill.json`; migrations `0008_overrides.sql`, `0009_custom_tools.sql`, `0010_secret_origin.sql`), `internal/api/manage.go`, `ui/src/screens/Manage.tsx` | The catalogue of every agent, tool, pack, skill and hook a run uses, admins' versioned overrides in Postgres (strictly validated, compatibility-checked against what Go reads and writes), each task's snapshot of them, history, diffs, revert and a patch/zip export; skills and `use_skill`; admins' own subagents and MCP/HTTP tools with SSRF-checked calls and server-side secrets, a metered Test call, and configurable hooks around calls, findings and finish (§ Manage) |
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
- [`docs/documents.md`](docs/documents.md), [`docs/figures.md`](docs/figures.md) and
  [`docs/reference.md`](docs/reference.md): the design detail this README leaves out (the section
  tree, search, hybrid search and vision transcription in depth; the figure rules; the agent runtime,
  the memo workflow's passes, Manage's validation and hooks), moved here verbatim from the README.
  [`docs/history.md`](docs/history.md): each PR's named tests and the line-budget history.
- [`docs/benchmarks.md`](docs/benchmarks.md): the pinned benchmark suite (`make bench`), its results schema, `compare.py` and the baselines.
- [`docs/consolidation-proposals.md`](docs/consolidation-proposals.md): normalisation and
  consolidation opportunities for the owner to decide on, a proposed README restructure, and the
  places where the docs and the code disagree today.

## Configuration

Environment variables only. All 18 are declared in `cmd/biglaw/config.go`, each in the PR that
first reads it. `biglaw version --config` prints every value and exits nonzero if what this build
reads is invalid. `biglaw serve` warns about any unknown `BIGLAW_*` name (likely a typo).

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

| Variable | Default | Meaning |
|---|---|---|
| `BIGLAW_DATABASE_URL` | — (required) | Postgres DSN: `postgres://user:password@host:5432/db?sslmode=disable`, or libpq `key=value` form. The scheme must be lowercase, and reserved characters in the password percent-encoded (`/`→`%2F`, `?`→`%3F`, `#`→`%23`, `%`→`%25`, `@`→`%40`). An `@` after the host (in the database name or a query value such as `application_name=ops@corp`) is refused too, because it cannot be told apart from a broken password; write `%40`. Anything else is refused with a message that never shows the password. lib/pq defaults to `sslmode=require`. |
| `BIGLAW_ADDR` | `127.0.0.1:3101` | Listen address. Non-loopback needs `serve --allow-no-auth` (§ Exposure). |
| `BIGLAW_PUBLIC_URL` | `http://127.0.0.1:3101` | How browsers reach BigLaw, `https://host[:port]` with no path: the redirect URI is `<it>/auth/callback`, https makes the cookies `Secure` and `__Host-`, and its host is the one DNS name served under sign-in. Read only with sign-in, which refuses plain http except on loopback |
| `BIGLAW_MODEL_URL` | — | OpenAI-compatible API base, ending in its version (`http://localhost:11434/v1`); calls go to `<URL>/chat/completions` and `<URL>/embeddings`. http(s), a plain ASCII host with at least one letter or digit, a port up to 65535, no `@` anywhere (no credentials in the URL; write any other `@` as `%40`). Required when the key or a role is set. |
| `BIGLAW_MODEL_KEY` | — | API key for that URL, sent as `Authorization: Bearer`; no white space or control characters. `serve` warns if it would go over plain `http`. |
| `BIGLAW_MODELS` | — | Model id per role, as the endpoint names it: comma-separated `role=id`, roles `lead`, `specialist`, `critic`, `vision`, `embed`, each at most once (e.g. `lead=qwen2.5:14b,critic=glm-4.5-air,embed=nomic-embed-text`). Unset `specialist` or `critic` use the lead's; setting either without the lead is refused. `vision` and `embed` have **no** fallback: with no `embed` role search is lexical only (`/health` says `"search":"lexical"`); `vision` names the model images and scanned pages are sent to; without it they are stored as `needs_transcription` (503 `vision_not_configured`) and never sent to another role's model (§ Vision transcription). An unknown role, a repeated one, an empty id, an id over 200 bytes, or an id holding `://`, `user:password@`, white space, a control or invisible (format) character, `?` `=` `&`, or a key's shape (`sk-`, `sk_`, `AKIA`, `ASIA`, `AIza`, `xox?-`, `gh?_` prefixes, or 24 letters and digits in a row) is a startup error that does not echo the value. A wrong id fails at its first call with 404 → `model_not_configured`; with no lead, chat calls fail with 503 `model_not_configured`. |
| `BIGLAW_BUDGET_CALLS` | `400` | Model calls per task, lead turns included (at the first bench's $0.012–0.020 a call, 400 calls cost $4.80–8.00, so the default $5 usually binds first; every share holds on both) |
| `BIGLAW_BUDGET_USD` | `5` | Dollar cap per task |
| `BIGLAW_LOCAL_WATTS` | — | Power draw of the machine running the model; when set (above 0) every call records watts × duration as watt-hours, for reporting only (never a price). A number from 0 to 100000 |
| `BIGLAW_PRICES` | built-in | Model rates overriding the built-in table: the JSON itself (a value starting with `{`) or a path to a regular file of at most 1 MiB holding it (§ Model client) |
| `BIGLAW_COLLAPSE_THRESHOLD` / `_ACTION` | `0.5` / `strict` | Grounding-collapse policy |
| `BIGLAW_OIDC_ISSUER` | — | Turns sign-in on: the issuer's https URL exactly as its discovery document states it (`https://idp.example.com/realms/biglaw`), no query; plain http only on loopback (§ Sign-in) |
| `BIGLAW_OIDC_CLIENT_ID` / `_CLIENT_SECRET` | — | The OIDC client, set with the issuer (the secret is optional: a public client uses PKCE alone); no white space |
| `BIGLAW_ADMINS` | — | Sign-in's admins: comma-separated entries, each a verified email (has an `@`, case-insensitive, matched only when the ID token's `email_verified` is true) or an issuer subject (`sub`, exact). Everyone else who signs in is a member. Refused without sign-in |
| `ALLOW_ANTHROPIC` | off | Opt in to a direct `anthropic.com` model endpoint (vendor breaker) |
| `ALLOW_AWS` | off | Opt in to a direct AWS/Bedrock model endpoint (vendor breaker) |

Tests use two more: `BIGLAW_TEST_DSN` and `BIGLAW_REQUIRE_DB` (turns a skipped DB test into a
failure). `TestEnvVarTablesAgree` fails if this table or `docker-compose.yml` lacks a variable
`config.go` declares, if `.env.example` or `render.yaml` sets other than its fixed list (the
model URL/key/models, the OIDC variables and `BIGLAW_ADMINS`; Render adds the database URL and address), or if any
of these files names a variable `config.go` does not declare.

**libpq variables.** lib/pq also reads the standard libpq `PG*` variables (`PGHOST`, `PGPORT`,
`PGUSER`, `PGPASSWORD`, `PGSSLMODE`, `PGPASSFILE`, …) as defaults for anything the DSN leaves
out, and refuses to start if one it does not support (such as `PGGSSENCMODE`) is set. They are
outside the 18-variable surface; `version --config` lists the names it sees, never their values.
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

- **Requests and answers.** A request names its `Agent` (`lead` or the subagent tool's name), its
  `Role` (resolved through `model.Roles`, fed by `BIGLAW_MODELS`) or an explicit `Model`, the messages
  (text, and images only as base64 `data:` URLs, so the endpoint never fetches a URL on BigLaw's
  behalf) and the tools. The same request encodes to identical bytes every call, so a fixed tool list
  keeps the provider's prompt-cache prefix. An answer is text and/or typed tool calls, a `Truncated`
  flag and usage; a malformed tool call fails the call (`ErrUnavailable`), and a 200 the provider
  billed but BigLaw refuses still reports its usage and cost on the error. Redirects are never
  followed. Both in full: [docs/reference.md § Model client](docs/reference.md#model-client).
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
  endpoint is not free and open weights are no reason for $0 either (owner decision).
  `internal/model/prices.json` is the built-in table and `BIGLAW_PRICES` the operator's, in one format
  read by one parser: a model id (case-insensitive; a trailing `*` makes it a prefix) mapped to
  `{"in": USD, "out": USD}` per million tokens, or to `null` for a model that is known but has no
  price. The operator's table is tried first; in each an exact id beats the longest prefix. Cost =
  (input × in + output × out + cache writes × in × 1.25 + cache reads × in × 0.10) / 10⁶. A model with
  no row or a `null` row has an **unknown** cost (nil), never $0. **A model you really run for free**
  is set to zero explicitly: `BIGLAW_PRICES='{"qwen2.5:14b": {"in": 0, "out": 0}}'`. `serve` logs
  each role's price once at startup. The matching rules and the built-in rows in full:
  [docs/reference.md § Model client](docs/reference.md#model-client).
- **Embeddings and watt-hours.** `Embed` sends up to 256 texts a call on the `embed` role's model,
  with the same retries, redirect rule and typed failures as a chat call, and is priced like every
  call (no `usage`: an unknown cost, never $0); no `embed` role is 503 `model_not_configured`. With
  `BIGLAW_LOCAL_WATTS` set every call records watt-hours beside its price, never changing it. The
  vector checks and the embedding models' rates: [docs/reference.md § Model client](docs/reference.md#model-client).
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

PDFs and `.docx` files are parsed in a bounded **child process** of the same binary (no environment,
so it never holds the server's secrets; killed when its request ends, after 2 minutes, or with its
parent; on Linux a hard 1 GiB data cap), so a hostile file cannot take the server down
(`TestHostilePDFContained`, `TestChildBoundsItself`). A document's text is capped at **16 MiB**
(`maxText`; the file at 64 MiB): more is 422 `unreadable`. Two extractions run at once
(`ingestSlots`); an upload waits at most 10 minutes for a slot, then is 503 `busy`. `.docx` text is
read in one linear XML pass (body, footnotes and endnotes; hidden and deleted text left out). The
child's bounds and the `.docx` reader's rules: [docs/documents.md § Ingest](docs/documents.md#ingest).

All extracted text, uploaded text included, has invalid UTF-8 replaced and control characters
other than tab, newline, CR and form feed turned into spaces, so it can be stored.

**Section tree, chunks and search** ([docs/documents.md](docs/documents.md) has the bounds, the
rules and the cost measurements). A document is parsed into a byte-exact section tree (v1's page
index, rewritten and bounded) and cut into search chunks of at most 400 tokens; a table's rows are
grouped, never one chunk a row. Search is Postgres full text, ranked deterministically and scoped
to the caller's documents. With an `embed` role in `BIGLAW_MODELS` it is hybrid: chunks are embedded
at ingest (a failure leaves the document lexical, with a note) and by a startup backfill, and the
lexical ranking is fused with an **exact** cosine ranking of the scope's embedded chunks (reciprocal
rank fusion; no approximate index, which breaks scoped recall), so each query reads every embedded
chunk in its scope.

**Gate E4 (`TestHybridRecallProofRuns`): not yet judged.** The committed tape
(`eval/e4/embeddings.json`) is the offline self-test of a hash embedder, on which the gate skips
with "E4 GATE NOT JUDGED"; never present its numbers as the gate. Lexical search alone measured
0.882. The owner records a real tape with `make e4-record EMBED_MODEL=…` (docs/documents.md § Gate E4).

**Vision transcription (PR 3c).** Images and PDF pages with no text layer, or only a stamp, are
transcribed by the `vision` role only, never another role's model. Each page is read twice, by two
prompts: agreement under 0.75 refuses the upload (422 `transcription_unreliable`), under 0.9 marks
the page low confidence, and so does any page whose readings differ on what matters; `[illegible]`
is never filled; a mixed PDF keeps its text layer. Every transcribed document, chunk and citation
carries its provenance, and a citation into a transcript is marked `@transcript` and capped at 0.85.
With no vision role an image is stored waiting (503 `vision_not_configured`, the original kept). The
reading rules, the scanned-PDF bounds and the reading slots: docs/documents.md § Vision transcription.

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

The memo workflow (§ Memo workflow) drives these. Everything here is tested with `model.Fake` or no
model; the full description, with its bounds, the paging rule and this layer's limitations, is
[docs/reference.md § Agent runtime](docs/reference.md#agent-runtime-and-memo-tools).

- **Tools and the registry.** A `Tool` has a `Spec` (name, kind `go`/`subagent`/`fanout`, strict-shaped
  input and output JSON Schemas, a budget class, a turn cap, a subagent's allowed tools); a description
  needs three sentences, a "Do not use" line and 1–3 `Example: {…}` lines that validate. At most 48
  tools are registered (`agent.MaxTools`) and 24 visible to one agent (`agent.MaxVisible`). Definitions
  are frozen when registered, so the same bytes reach the model every turn and no matter data reaches
  a schema. Specs are data (`internal/docs/specs`, `internal/analysis/specs`, `internal/engine/specs`).
- **One loop, two depths** (`Runtime.Run`): the lead and its subagents. Every call goes through
  `Runtime.Invoke`: offered to that agent, no nesting, alone when it must be, input schema, the task's
  `Policy`, then a grant. At most 8 tool calls run a turn. **Tool outputs are bounded by type and never
  trimmed**: a list or long text is one page of `agent.PageBytes` (12 KB) with `next` or `truncated`,
  and a result over 100 KB reaches the model as the typed refusal `result_too_large`. A tool that
  panics fails only its task. Text in tool results is labelled untrusted matter data.
- **Budget** (`Budget`): calls and USD per task. A model call holds its worst-case cost and is charged
  what it spent (a refused-but-billed answer too); the task keeps back what later phases need, so
  only `finish` spends the reserve.
- **Tool bandit** (`Bandit`): per task signature, one hint line before a lead turn and a subagent's
  model pick; it never hides or refuses a tool.
- **Memo tools**: `retrieve` (search, outline, section; `k` ≤ 8), `verify_quote` (the ladder; its
  judge on the `critic` role, at most 4 calls from the caller's grant), `numbers` (exact `calc`,
  `{Codename}` handles, a key-figures table, all over verified values), `join_discrepancies`
  (cross-document conflicts the figure judge decides: [docs/figures.md](docs/figures.md#the-cross-document-join)),
  coverage (`CheckCoverage`) and BELO-lite (`belo.json`).

## Memo workflow, durable runs and the REST API (`internal/engine`, `internal/api`; PR 4b)

`POST /tasks` with `workflowType: "memo"` (the only workflow built; `diligence` and
`contract_review` are 400 until PRs 5 and 6) stores a task and wakes the runner. The lead agent
(role `lead`, prompt `internal/engine/specs/lead.md`) sees 12 tools in a fixed order: `retrieve`,
`ledger`, `sweep`, `extract_facts`, `analyze_issue`, `check_deviation`, `numbers`,
`join_discrepancies`, `draft_section`, `committee`, `finish`, `use_skill` (§ Manage, Skills). A task runs on its snapshot of these specs (§ Manage). Specs are data
(`internal/engine/specs/*.json`, subagents `specs/agents/*.{json,md}`: the five above plus the
critics `prosecute`, `reproduce`, `audit_gaps`). Everything below is tested with `model.Fake`.

How a run works, in order (each pass's full rules, with the tests that pin them:
[docs/reference.md § Memo workflow](docs/reference.md#memo-workflow); the figure rules:
[docs/figures.md](docs/figures.md)):

- **The ledger** (`ledger`; one Postgres table, a JSON row per kind and key): the plan's issues, the
  findings, resolutions, drafted sections, relations and the committee's findings, each paged. A
  subagent may only read, and never sees a quarantined finding (G3).
- **Evidence** (`extract_facts`, `analyze_issue`, `check_deviation`; `specialist` role). Go completes
  each request and checks each answer before anything reaches the ledger: every quote goes through
  the ladder; a finding is verified only when every quote is (G2), else it is stored quarantined and
  never drafted from (G3); a typed value is kept only when its own quotes state it as the figure
  reader reads it, and a dropped one is noted, never silent. What a number means is read by a model
  (`read_figures`), never decided by a word list in Go. A requirement is checked verbatim in its
  controlling document before a deviation is judged.
- **`sweep`** reads every page of the task's documents once, most relevant first, up to 4 visits at
  once, admitting in reading order; exact copies and boilerplate are skipped and disclosed.
- **Drafting** (`draft_section`, one issue a call) from a request Go builds: the issue's verified
  findings with their quotes, `{Codename}` and `{W…}` handles, earlier sections' summaries, and on a
  redraft the checks it fails.
- **`committee`**: three context-free critics in parallel on explicit grants (`prosecute` the claims,
  `reproduce` the questions, `audit_gaps` the plan and headings); their HIGH/MEDIUM/LOW findings stay
  open until the lead resolves them. At most three cycles (`maxCycles`).
- **Policy** (`policy.go`): P1 a plan first (the packs' checklist becomes the plan, disclosed, if the
  plan share runs out); P4 class shares on calls and USD; the coverage pass's kept share.
- **`finish`** (`op: "check"` is a dry run of `submit`): lint (every `[F-id]` names a usable finding;
  every figure traces to a cited quote, value, handle or worksheet, G8; external authorities flagged,
  G5), coverage, P6/P7, then the render (Markdown: Limitations, the Computed figures block, the
  Evidence Note, Sources) and the gates.
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

**Durable runs.** Each lead turn and tool call is a step in `task_steps`: recorded `started` (with
its grant) before it runs and `committed`/`refused`/`failed` after, in one transaction with the
ledger rows and bandit statistics it wrote. A process claims a task with a lease (30 s, renewed every
10 s; `FOR UPDATE SKIP LOCKED`) and runs at most 4 at once; a stale lease is taken over. On resume
the committed steps are replayed from the log (no model call repeated) and a step that was in flight
is charged (`lost`, shown in the cost ledger) and issued again: after a graceful stop what it spent,
after a hard crash what its journaled model calls spent plus the worst case of those in flight. A
run stops `finished` (gate open), `budget_exhausted` or `max_steps` (a draft changed since its last
review gets one committee from the kept cycle, then `finish` is forced from the reserve, open
findings disclosed; `maxSteps` defaults to twice the call budget, at least 400), `awaiting_gate`,
`cancelled` or `failed`. In full: [docs/reference.md § Durable runs](docs/reference.md#durable-runs).

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
python3 eval/lab/run.py --labs-dir ~/harvey-labs --api http://127.0.0.1:3101 --model-dir v2-<C> \
  --task corporate-ma/review-data-room-red-flag-review [--tool-bandit on|off]
```

`eval/` (Python 3, stdlib only): `run.py` (`list`, `validate`, `selftest`, `score`,
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

## Manage: agents, tools, packs, skills and hooks (`internal/engine` specset/manage/skills/hookrules, `internal/api/manage.go`)

Everything a run uses is a file built into the binary (go:embed), and an admin can see and change
each one in Manage (`#/manage`) or the routes above, with no rebuild:

- **Agents**: the lead's prompt (`specs/lead.md`), the meta and section-check prompts, the subagents
  (`specs/agents/<name>.json` + `.md`: description, `maxTurns`, budget `class`, model `role`, `allowed`
  tools, `in`/`out` schemas, prompt) and the engine's passes (`sweep`, `committee`, `cover`, `finish`,
  `ledger` specs). **Tools**: the typed tool specs of `internal/analysis/specs`, `internal/docs/specs` and
  `internal/engine/specs/{exact_calc,figure_context,use_skill}.json`; their code stays Go.
  **Packs**: jurisdiction and practice guidance (§ Memo workflow). **Skills**: below. **Hooks**:
  configurable hooks (below). The Go around some tools and subagents (`hooked`, `sheets`, `aliasJoin`
  in `register.go`, `finish`'s lint) is not an item: its checks, in plain words from the tool's
  `goContract` row, are listed on that tool's own page, view only. Each item shows its contents, built-in or override (its version), what it calls and
  who calls it (agents' allowed lists, the lead's list, and the Go callers), and its recent usage: lead
  steps, model calls and USD over the last 20 tasks (`task_steps`; a fan-out's own calls count under it).
- **Versions and snapshot** (`spec_versions`, migration 0008): every save, revert and skill deletion
  is a new version of the item holding all its files, with its author, time and note, so the table is
  the history and the **change log**. A save names the version it was edited from (`base`): a stale
  one is 409. `Submit` stores the overrides in effect in `tasks.specs`; a run, a resume included,
  reads every spec, prompt, pack and skill from that snapshot, so a save reaches the next task only.
  In full: [docs/reference.md § Manage](docs/reference.md#manage).
- **Validation and compatibility** (every save and revert; all problems at once, 422 with `problems[]`
  by file): each file strictly (unknown fields refused, schemas in the agent subset and strict-shaped),
  then the whole set as a task would build it, then `compatSpec`: no property a built-in schema has may
  be removed or retyped, no enum changed, and a schema Go writes gains none. Saves are serial (an
  advisory lock, one transaction). The figure reader's and judge's cache keys hash their spec, prompt
  and allowed tools' specs, so an override reads again. Any change to a pack's or skill's content
  resets it to `candidate` unless the save names a new reviewer. The full rules:
  [docs/reference.md § Manage](docs/reference.md#manage).
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

Events `before_call` and `after_call` (every `Runtime.Invoke` but `finish` and `verify_quote`),
`finding_admitted` and `task_finish`; actions `block`, `approve` (a lawyer's gate on one call the lead
makes, single use), `check` (a named subagent's verdict), `webhook` and `limitation`; every outcome is a
ledger `hookrun` row written with its step. Matchers, each action's effect, order, the failure policy
and the checks at save: [docs/reference.md § Configurable hooks](docs/reference.md#configurable-hooks).

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

**Memory of one ingest, end to end**: measured in [docs/documents.md § Memory](docs/documents.md#memory).

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

The tests are their own record. Each PR's named tests, and those its review rounds added, are listed
in [docs/history.md](docs/history.md#named-tests).

## Budgets (`scripts/loc.py`, run in CI)

| Budget | Ceiling | Today |
|---|---:|---:|
| Go production lines (physical lines, non-`_test.go`, test-support packages included) | reported, no limit (owner decision 17) | printed by `python3 scripts/loc.py` |
| Packages under `internal/`, test-support packages excluded | 12 | 9 (`agent`, `analysis`, `api`, `auth`, `docs`, `docs/ooxml`, `engine`, `model`, `store`; `store/storetest`, `engine/figtest` and `auth/authtest` are test support) |
| Direct Go dependencies | 3 | 2 (`github.com/lib/pq`, `github.com/ledongthuc/pdf`) |
| Runtime environment variables | 20 | 18 |
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
- Manage creates skills, subagents, MCP and HTTP tools and MCP servers, not packs; the compatibility check's limits are in docs/reference.md § Manage. A remote call in flight when a process dies is made again on resume (its step had not committed): remote tools should be safe to repeat. MCP tools: text content only (other parts are noted, not shown), one session per call, no server-initiated requests; the HTTP and MCP answers are one page (truncated, never paged by a second call).
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
- **LAB rounds 3 and 4** (sweep order and skips, multi-part requirements, the crash journal, left-out
  evidence, raw deadlines, the parallel sweep, deviations, the coverage pass): the sweep's order is
  lexical, so a page that matters for a reason its words do not share can come late; a requirement's
  parts must be the document's own words, in order; an omission is confirmed absent by the paraphrase
  judge on the closest passage; the join asserts only what the figure judge says, and a figure the
  reader read unclearly is gated, not traced. The full lists: [docs/reference.md §
  Limitations](docs/reference.md#limitations-lab-rounds-3-and-4) and [docs/figures.md §
  Limitations](docs/figures.md#limitations).
- **Documents, hybrid search and vision transcription (PRs 3a–3c)**: there is no OCR (scans are read
  by the vision model only); the vector leg is exact, so a query's cost grows with its scope's
  embedded chunks; a transcript is evidence of a lower class (capped, badged, gated when low). The
  full lists: [docs/documents.md § Limitations](docs/documents.md#limitations).
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
- **Model client (PR 2):** behaviour against real endpoints other than the owner's bench and LAB
  runs is unproven. **Every call is charged, including on your own GPU** (owner decision): an
  Ollama model under an open-weights family name (`qwen2.5:14b`) is charged at that family's hosted
  rate unless `BIGLAW_PRICES` sets it to `{"in": 0, "out": 0}`, and a model with no row or a
  `null` one has an unknown cost the USD cap cannot count (only the call cap bounds it). The built-in
  rates are v1's early-2026 list prices, not re-verified for v2. The rest (gateway quirks, cache
  accounting, `Retry-After` forms, `model.Fake`'s order): [docs/reference.md § Limitations of the
  model client](docs/reference.md#limitations-of-the-model-client).
