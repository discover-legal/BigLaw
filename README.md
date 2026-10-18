# BigLaw 2

> **Status: under construction on branch `v2`.** This README describes only what is on disk.
> Everything marked **planned** is specified in the v2 build plan and does not exist yet.
> BigLaw 1 (the DyTopo platform) remains on `main`; its frozen copy will be the branch
> `archive/v1-final` (a branch, because tag pushes are blocked in the build environment).

BigLaw 2 will be a legal AI workbench for small firms: memos, contract review and diligence
tables in which every factual statement cites a quote that the software has verified against the
matter's own documents, with a lawyer approving every deliverable. It is one Go binary plus one
PostgreSQL database. Licensed Apache-2.0 (`LICENSE`, `NOTICE`).

## What exists today (PR 1: skeleton, config, store, CI; PR 2: model client; PR 3a: documents)

| Piece | Path | What it does |
|---|---|---|
| Command | `cmd/biglaw/main.go` | `biglaw serve [--allow-no-auth]` (migrates the database, then serves HTTP) and `biglaw version [--config]` |
| Configuration | `cmd/biglaw/config.go` | Declares all 20 runtime variables; validates the ones this build reads (all problems at once); vendor breakers ported from v1 |
| Store | `internal/store/store.go` | Postgres via `database/sql` + `github.com/lib/pq`; DSN check that matches lib/pq's reading; transaction helper |
| Migrations | `internal/store/migrate.go`, `internal/store/migrations/NNNN_*.sql` | Embedded SQL files applied in order under an advisory lock; each file and its `schema_migrations` row in one transaction |
| Files | `internal/store/files.go` | Binary files (uploads, generated `.docx`) stored in 1 MiB `bytea` chunks, streamed in, with size and SHA-256 checked on every read |
| Model client | `internal/model/` (`client.go`, `errors.go`, `fake.go`, `pricing.go`, `prices.json`) | One OpenAI-compatible chat client with tool calls and image parts, typed failures with their HTTP mapping, at most 3 attempts per call, cache-aware pricing of every call from a built-in table plus `BIGLAW_PRICES` (the host never makes a call free), watt-hour metering when `BIGLAW_LOCAL_WATTS` is set, and `model.Fake` for tests (§ Model client). Nothing calls it yet: the agent loop that does arrives in PR 4a. |
| Documents | `internal/docs/` (`ingest.go`, `pageindex.go`, `chunk.go`, `search.go`, `verify.go`, `http.go`) | Ingest of UTF-8 text, `.docx` and PDF text layers (PDFs parsed in a child process); the byte-exact section tree; search chunks; Postgres full-text search; the page reader (outline, paged sections); the quote-verification ladder; the upload and download routes. Every read is scoped to the caller's document ids (§ Documents) |
| `.docx` reader | `internal/docs/ooxml/` | Opens a `.docx` (128 MiB decompression cap), its visible text, a part-preserving re-zip. The writer and tracked-change parse/apply are planned (PR 3c) |
| Proof-run fixtures | `eval/tasks/` | The five synthetic proof-run matters (documents + answer keys), copied from `feat/proof-runs-multi-practice`; PR 3 reads them for gate E4, PR 4b adds the driver and scorer |
| Test helper | `internal/store/storetest/` | Gives each DB test its own schema; skips without a DSN, fails if `BIGLAW_REQUIRE_DB` is set |
| Budgets | `scripts/loc.py` (+ `scripts/test_loc.py`) | Fails CI if a size budget is exceeded (below) |
| Build/deploy | `Makefile`, `Dockerfile`, `docker-compose.yml`, `render.yaml`, `.env.example` | `make ci`; `postgres:16-alpine` + biglaw under Compose; Render blueprint (inert until PR 8, see § Exposure) |
| CI | `.github/workflows/ci.yml` | `make ci` against `postgres:14` and `postgres:16` service containers with `BIGLAW_REQUIRE_DB=1`; staticcheck + govulncheck; image build + Compose smoke test |

HTTP surface today: `GET /health`, which returns `{"status":"ok","version":"…"}` (200) or
`{"status":"database unavailable",…}` (503; the cause is logged, not returned), and the two
document file routes of § Documents: `POST /documents/upload` and `GET /documents/{id}/file`.
Every other path is 404/405. There is no authentication yet (§ Exposure).

**Planned** (not built): vision transcription of images and scanned PDFs, with one role-map
variable replacing the three role variables (PR 3b); the `.docx` writer and tracked-change
parse/apply (PR 3c); the engine and the memo workflow with its REST API (PR 4), diligence (PR 5), contract review (PR 6),
MCP (PR 7), clients/conflicts and OIDC/session/token auth (PR 8), time and billing (PR 9), the
UI (PR 11), jurisdiction and practice packs (PR 12), v1 import and docs (PR 13). The
discover.legal intake module is **not** part of v2 (owner decision; it moves to a separate repo).

## Configuration

Environment variables only. All 20 are declared in `cmd/biglaw/config.go`; each is validated by
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
  A Compose service name with `_` is a plain host. An OIDC preset (`google`,
  `microsoft:<tenant>`) is shown as is.
- Anything else is shown as set (quoted if it contains a control character, so a newline cannot
  forge an output line), unless it contains `://` (then treated as a URL) or has the
  `user:password@` shape (then not shown).

| Variable | Default | Meaning | Read from |
|---|---|---|---|
| `BIGLAW_DATABASE_URL` | — (required) | Postgres DSN: `postgres://user:password@host:5432/db?sslmode=disable`, or libpq `key=value` form. The scheme must be lowercase, and reserved characters in the password percent-encoded (`/`→`%2F`, `?`→`%3F`, `#`→`%23`, `%`→`%25`, `@`→`%40`). An `@` after the host (in the database name or a query value such as `application_name=ops@corp`) is refused too, because it cannot be told apart from a broken password; write `%40`. Anything else is refused with a message that never shows the password. lib/pq defaults to `sslmode=require`. | PR 1 |
| `BIGLAW_ADDR` | `127.0.0.1:3101` | Listen address. Non-loopback needs `serve --allow-no-auth` (§ Exposure). | PR 1 |
| `BIGLAW_PUBLIC_URL` | `http://127.0.0.1:3101` | External base URL (OIDC redirect, cookie `Secure`) | PR 8 |
| `BIGLAW_MODEL_URL` | — | OpenAI-compatible API base, ending in its version (`http://localhost:11434/v1`); calls go to `<URL>/chat/completions`. http(s), a plain ASCII host with at least one letter or digit, a port up to 65535, no `@` anywhere (no credentials in the URL; write any other `@` as `%40`). Required when the key or a role is set. | PR 1 |
| `BIGLAW_MODEL_KEY` | — | API key for that URL, sent as `Authorization: Bearer`; no white space or control characters. `serve` warns if it would go over plain `http`. | PR 2 |
| `BIGLAW_MODEL_LEAD` / `_SPECIALIST` / `_CRITIC` | — | Model id per role, as the endpoint names it (startup refuses only an id holding `://`, `user:password@` or a control character; a wrong id fails at its first call with 404 → `model_not_configured`). Unset specialist or critic use the lead's; setting either without the lead is refused. With no lead, model calls fail with 503 `model_not_configured`. | PR 2 |
| `BIGLAW_BUDGET_CALLS` | `150` | Model calls per task | PR 4 |
| `BIGLAW_BUDGET_USD` | `5` | Dollar cap per task | PR 4 |
| `BIGLAW_LOCAL_WATTS` | — | Power draw of the machine running the model; when set (above 0) every call records watts × duration as watt-hours, for reporting only (never a price). A number from 0 to 100000 | PR 2 |
| `BIGLAW_PRICES` | built-in | Model rates overriding the built-in table: the JSON itself (a value starting with `{`) or a path to a regular file of at most 1 MiB holding it (§ Model client) | PR 2 |
| `BIGLAW_COLLAPSE_THRESHOLD` / `_ACTION` | `0.5` / `strict` | Grounding-collapse policy | PR 4 |
| `BIGLAW_OIDC_ISSUER` | — | `google`, `microsoft:<tenant-id>` or an https issuer URL | PR 8 |
| `BIGLAW_OIDC_CLIENT_ID` / `_CLIENT_SECRET` | — | The OIDC client | PR 8 |
| `BIGLAW_AI_RATE` | — | AI timekeeper hourly rate; unset = non-billable | PR 9 |
| `ALLOW_ANTHROPIC` | off | Opt in to a direct `anthropic.com` model endpoint (vendor breaker) | PR 1 |
| `ALLOW_AWS` | off | Opt in to a direct AWS/Bedrock model endpoint (vendor breaker) | PR 1 |

Tests use two more: `BIGLAW_TEST_DSN` and `BIGLAW_REQUIRE_DB` (turns a skipped DB test into a
failure). `TestEnvVarTablesAgree` fails if this table or `docker-compose.yml` lacks a variable
`config.go` declares, if `.env.example` or `render.yaml` sets other than its fixed list (the
model URL/key/lead and the OIDC variables; Render adds the database URL and address), or if any
of these files names a variable `config.go` does not declare.

**libpq variables.** lib/pq also reads the standard libpq `PG*` variables (`PGHOST`, `PGPORT`,
`PGUSER`, `PGPASSWORD`, `PGSSLMODE`, `PGPASSFILE`, …) as defaults for anything the DSN leaves
out, and refuses to start if one it does not support (such as `PGGSSENCMODE`) is set. They are
outside the 20-variable surface; `version --config` lists the names it sees, never their values.
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
  (`lead`, `specialist`, `critic`; resolved through `model.Roles`, the one accessor that PR 3's
  single role-map variable will feed) or an explicit `Model`, the messages, the tools, and optionally
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
  Role ids (`BIGLAW_MODEL_LEAD` etc.) reach the logs as written, so startup refuses an id that
  holds `://`, `user:password@`, or a control character (so `name:tag@quant` is refused too;
  LM Studio's `name@quant` and Ollama's `name:tag` are fine).
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
- **Watt-hours.** With `BIGLAW_LOCAL_WATTS` set, every call (successful or billed-but-refused)
  records `BIGLAW_LOCAL_WATTS` × its duration (all attempts) as watt-hours, beside its price. They
  report the energy of your own hardware; they never change the cost. Unset, none are recorded.
- **`model.Fake`** answers from scripts keyed by agent name, in order. A call for an agent with
  no script, or past the end of its script, is an error, never a default reply; an empty reply is
  `ErrNoContent` and a malformed tool call `ErrUnavailable`, as from the real client; tool-call ids
  it fills in are `<agent>-<call>-<index>` (the agent's own call count), so runs are deterministic.
  It does not resolve roles: a request's `Role` only names its reply's model (`fake:<role>`).

## Exposure (there is no authentication yet)

Auth lands in PR 8. Until then `biglaw serve` refuses to listen on a non-loopback `BIGLAW_ADDR`
(anything but `127.0.0.0/8`, `::1` or `localhost`) unless it is started as
`biglaw serve --allow-no-auth`. That flag means: something outside BigLaw limits who can reach
the port. The deploy files follow the rule, and `TestDeployFilesFollowExposureRule` keeps them
that way:
- **Compose** listens on `0.0.0.0` inside the container, publishes the port on the host's
  `127.0.0.1` only, and passes `--allow-no-auth`. Do not publish it on `0.0.0.0` or a LAN address.
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
  exposes the container refuses to start.
- **Render** is public by nature, so `render.yaml` is inert until PR 8: the service fails its
  start on purpose.

## Run it

```bash
test -e .env || sed "s/^POSTGRES_PASSWORD=\$/POSTGRES_PASSWORD=$(openssl rand -hex 24)/" .env.example > .env
docker compose up -d            # postgres:16-alpine + biglaw; host port 127.0.0.1:3101 only
curl -s http://127.0.0.1:3101/health
```

Compose gives biglaw the database password as `PGPASSWORD` rather than inside
`BIGLAW_DATABASE_URL`, so it needs no percent-encoding. The first line above writes a random hex
password into `.env` (Compose does not run `$(…)` written inside `.env`), and only when there is no
`.env` yet: an existing one keeps its settings and the password its `pgdata` volume was created with.
Compose's own `.env` parsing does constrain it, though: unquoted, a value is cut at ` #` and `$` is expanded (`a$b` fails, and
`abc #def` becomes `abc`, on both the Postgres and the biglaw side). Use a hex password, or
single-quote your own (`POSTGRES_PASSWORD='…'`) without a `'` in it.

Without Docker (Go 1.26+, any supported Postgres ≥ 14):

```bash
createdb biglaw
make build                      # static binary: CGO_ENABLED=0, version stamped (no UI yet)
BIGLAW_DATABASE_URL='postgres:///biglaw?sslmode=disable' ./biglaw serve
```

`postgres:///biglaw` uses the libpq defaults (a local socket or `PGHOST`, your OS user or
`PGUSER`). The server opens the database and runs migrations before it starts listening, so a
connection to the port is answered as soon as it is accepted.

## Database: migrations, backups, rollbacks

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
Anything else is refused with 415 `unsupported_type` (images included: transcription is PR 3b);
a damaged, encrypted or over-2,000-page PDF and a damaged `.docx` are 422 `unreadable`. A refused
upload leaves no row behind (`TestIngestRefusalLeavesNothing`). A document that has nothing
readable (a scanned PDF, a `.docx` holding only images, an empty file) is stored with
`noText: true` and a note saying so, never as a silently empty document
(`TestScannedPDFFlaggedNoText`); a PDF with some blank pages keeps its text and its note lists the
pages that were not read. PDF pages are joined with a form feed. The PDF parser runs in a child
process of the same binary (`biglaw` re-executed with a private argument), killed
after 2 minutes or when the request ends: the parser recurses on nested objects, and a few MB of
nesting is a fatal stack overflow that would otherwise kill the server (`TestHostilePDFContained`).
Extracted text has invalid UTF-8 replaced and control characters other than tab, newline, CR and
form feed turned into spaces, so it can be stored.

**Section tree and chunks.** v1's page index, rewritten: articles, sections (`Section 1`, `§ 1`),
decimal clauses (`1`, `1.1`; at most three digits, so a year or a figure at a line start is not a
heading), `(a)` and `(i)` items, schedules/exhibits/annexes, recitals, and Markdown `#` or bold
headings. Joining every section's own text gives back the document byte for byte, so every
section and chunk is a verbatim slice (`TestReconstruction`, `TestChunksCoverEveryWordOnce`).
Chunks hold at most 400 tokens (3.5 characters per token), cut at a sentence end, else at white
space; a tab-separated table becomes one chunk per row, indexed with its column headers
(`TestTableRowChunking`).

**Search.** Postgres full text, no vectors: each chunk has a stored English `tsvector` and a
`simple` (unstemmed) one, both GIN-indexed. Every word of the query is a separate term (OR, not
AND), scored as `ts_rank_cd` of that term (saturated) times its rarity in the scope
(`ln(1 + N/(1 + df))`), on both vectors, summed; ties go to the lower chunk id. So a rare term
outranks a repeated common one (`TestSearchRareTermOutranksCommon`), results are deterministic
(`TestSearchTopKDeterministic`), and identifiers such as `s.9.4`, `C-13`, `FS-26-01234`,
`Schedule A` or `Annex I` (whose letter is an English stop word) are found
(`TestSearchExactIdentifier`). Queries are at most 1,024 bytes; `k` is 1–50. **Gate E4** passes:
over the 68 planted proof-run facts, searching each fact's title within its matter returns a chunk
containing one of its evidence strings in the top 8 for **67/68 (0.985)**; counting every
evidence string separately, 101/106 (0.953) (`TestLexicalRecallProofRuns`, which prints both and
per-matter recall). So there is no pgvector.

**Page reader.** `Outline` lists every section in order as `{sectionId, number, title, level,
tokens}`, where `tokens` estimates what `Section` returns for it: the section with its
subsections. `Section` reads that text verbatim from a character `offset`, at most `limit` tokens
(default 2,000, at most 8,000), cut at white space; a longer section comes back with
`truncated: true` and `next`, never silently cut, and the pages join to the section exactly
(`TestOutlineHasNumbersTitlesTokens`, `TestSectionPaginates`).

**Verification ladder** (v1's `tabcite` ladder): exact substring, confidence 1.0; tolerant
substring (curly quotes straightened, white space collapsed, case folded on both sides), 0.95; one
paraphrase-judge call over the best-matching window of the document (at most about 3,000 bytes
around the span holding most of the quote's words), capped at 0.8; when the judge is less than
0.75 sure, three independent votes, a strict majority needed, confidence 0.75 × votes/3, below
the judge's cap; otherwise `none` with a note (`TestLadderMethodsAndCaps` and the carried
`TestCitation*` vectors). The judge is a function the caller supplies (the model wiring lands with
the agent runtime, PR 4a). **A judge failure is returned as an error, never as "unverified"**
(changed from v1, G6: `TestCitationJudgeFailureIsAnError`). `VerifyQuote` also names the deepest
section holding the match. `Resolve` maps a model's citation by file name (any case, directory or
extension) to the one document in scope with that name; an ambiguous name resolves to nothing.

**Upload and download.** `POST /documents/upload` takes `multipart/form-data` with one part named
`file`, streams it into the files table and ingests it: 201 with the document (`id`, `name`,
`contentType` (detected), `fileId`, `method` `text`|`text_layer`, `noText`, `pages`, `notes`,
`sections`, `chunks`, `createdAt`). Errors are `{error, errorType}`: 400 `bad_request`/`bad_name`/
`bad_content_type`, 403 `cross_origin`, 408 `upload_timeout`, 413 `too_large`, 415, 422, 499
`cancelled`, 500 `internal` (logged, fixed text). A client must send at least 64 KiB every 30
seconds and the whole file within 15 minutes, or its read fails and its upload slot and pool
connection are released at once (`TestUploadReadDeadline`: 16 stalled clients and one trickling
client, then the slots are free, the pool is back to its baseline, a 17th upload succeeds and no
file row persists; `TestUploadTotalDeadline`). A browser request from another site is refused
(`Origin` other than the request's own host, or `Sec-Fetch-Site` other than `same-origin`/`none`),
because `multipart/form-data` needs no CORS preflight. `GET /documents/{id}/file` returns the
original with the **detected** content type (never the uploader's claim), `nosniff`,
`Content-Security-Policy: sandbox` and an RFC 6266 attachment header: `filename*` carries the
name (UTF-8), and both it and the ASCII `filename=` fallback are safe to save on Windows (`<>:"/\|?*`
and controls become `_`, a device name such as `CON`, `aux.txt` or `COM1.tar.gz` gets a `_`
prefix, trailing dots and spaces go: `TestDownloadNameSafe`). `Delete` removes a document with
its sections, chunks and original file in one transaction, and a file deleted any other way takes
its document along (`TestFileDeletedWithDocument`).

## Files

Files are stored in `file_chunks` rows of at most 1 MiB (a `CHECK` in the schema refuses a
larger row), written in one transaction from a stream. lib/pq moves `bytea` as hex text, so each
chunk costs a few times its size in transient memory; chunking bounds **writing** by the chunk,
not the file. **Reading** still holds the whole file: a read checks the recorded size against
the 64 MiB cap before reading content, allocates the file once, and re-verifies size and
SHA-256. The SHA-256 detects **storage corruption, not tampering**: anyone who can write the rows
can rewrite the hash too.

An upload holds one pooled connection, inside its transaction, for as long as its reader takes.
So at most 16 uploads (`MaxConcurrentUploads`) run at once, leaving 4 of the 20 connections for
`/health` and reads. Further uploads wait for a slot until their context ends. Nothing in the
store ends a stalled upload: its slot and connection are freed when its reader returns (an error
rolls the transaction back, leaving nothing behind). Bounding a stalled client is the caller's job;
the upload route's read deadline does it (§ Documents). At most 4 reads (`MaxConcurrentReads`)
hold a file at once; a read over the cap waits for a slot or its context
(`TestConcurrentReadsBounded`), so concurrent reads of maximum-size files stay near 512 MiB.

Names must be 1–255 bytes of UTF-8 and not `.` or `..`. They may not contain `/`, `\`, control
characters, invisible characters (category Cf such as bidi controls, zero-width spaces, the BOM
and soft hyphens; Unicode Default_Ignorable_Code_Point such as the Hangul fillers U+3164/U+115F,
the combining grapheme joiner and variation selectors; the braille blank U+2800), line/paragraph
separators or a run of two or more white-space characters (no-break spaces included), and may not
start or end with white space, any of which can disguise an extension. ZWNJ/ZWJ are allowed once
between two letters, marks or emoji, as Persian, Indic scripts and emoji sequences need them.
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

## Test it

```bash
go test -race ./...             # unit tests; DB tests SKIP without a DSN
# a throwaway Postgres for the DB tests:
docker run -d --name biglaw-testdb -p 127.0.0.1:55432:5432 -e POSTGRES_PASSWORD=t postgres:16-alpine
BIGLAW_TEST_DSN='postgres://postgres:t@127.0.0.1:55432/postgres?sslmode=disable' \
  BIGLAW_REQUIRE_DB=1 go test -race ./...   # DB tests too; each gets its own schema, dropped after
make ci                         # gofmt, vet, go mod verify/tidy, static build, tests (-race, shuffled),
                                # loc.py + its tests, docker compose config
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
the diligence table, PR 5), the reader part of `ooxml_test` (`TestEscapeUnescapeRoundTrip`,
`TestRoundTripPreservesPartsAndOrder`, `TestOpenRejectsNonDocx`,
`TestTextJoinsRunsAndSeparatesParagraphs`), v1 `protocols` (`TestVerbatimContains`,
`TestNormalizeSourceKey`, `TestResolvesByTitle`) and v1 `services` (`TestExtractPlainText`,
`TestExtractDOCX`); `TestScannedPDFFlaggedNoText`, `TestOutlineHasNumbersTitlesTokens`,
`TestSectionPaginates`, `TestRetrieveNeverLeavesMatter`, `TestSearchRareTermOutranksCommon`,
`TestSearchTopKDeterministic`, `TestSearchExactIdentifier`, `TestLexicalRecallProofRuns` (E4),
`TestLadderMethodsAndCaps`, `TestFileNotReachableOutsideMatter`, `TestFileDeletedWithDocument`,
`TestConcurrentReadsBounded`, `TestUploadReadDeadline`, `TestDownloadNameSafe` and the extended
`TestFilesEdgeCases`. Extra: `TestMarkdownHeadings`, `TestChunksCoverEveryWordOnce`,
`TestExtractPDFTextLayer`, `TestHostilePDFContained`, `TestIngestRefusalLeavesNothing`,
`TestUploadTotalDeadline`, `TestUploadErrorsTyped`, `TestWriteErrorKinds`,
`TestOpenRefusesZipBomb` and, in `cmd/biglaw`, `TestBinaryIsItsOwnPDFReader`.

## Budgets (`scripts/loc.py`, run in CI)

| Budget | Ceiling | Today |
|---|---:|---:|
| Go production lines (physical lines, non-`_test.go`, test-support packages included) | 14,500 | 3,498 |
| Packages under `internal/`, test-support packages excluded | 12 | 4 (`docs`, `docs/ooxml`, `model`, `store`; `store/storetest` is test support) |
| Direct Go dependencies | 3 | 2 (`github.com/lib/pq`, `github.com/ledongthuc/pdf`) |
| Runtime environment variables | 20 | 20 |
| UI runtime dependencies | 2 | 0 (no UI yet) |
| Packages without a `_test.go` | 0 | 0 |
| Lines in any one source file (`.go`, `.py`, `.sql`, `.sh`, `.js`, `.ts`, `.css`; tests included) | 2,000 | 626 |

The line ceiling is 14,500 (owner decisions: 14,000, then 14,250, then 14,500 during PR 2's review). A **test-support package** is one
whose directory name ends in `test` and that no production file outside it imports, such as
`storetest`. Under the coordinator's ruling it is left out of the package count, but its lines
count toward the line ceiling like any other non-`_test.go` file. A `*test` package that
production code imports is an ordinary package. loc.py also fails if `LICENSE` or `NOTICE` is
missing or a `.go`/`.py` file lacks its SPDX header. Env-var names are found statically (literal
`os.Getenv`/`LookupEnv`/`getenv` arguments, the config table, and `BIGLAW_*`/`ALLOW_*` literals).
An `os.Getenv`/`os.LookupEnv` argument that is not one plain string literal (a variable, a
concatenation such as `"BIGLAW_" + x`, a raw string) fails the check.

## Limitations

- Nothing user-facing exists yet beyond `/health` and the two document file routes; every
  workflow, the rest of the REST API, auth and the UI are planned. The exposure rule above is the
  only protection until PR 8: anyone who can reach the port can upload and, knowing a document id,
  download. The cross-origin check stops a web page from uploading through the lawyer's browser,
  but not DNS rebinding (a page on a name that resolves to 127.0.0.1 is "same origin"); PR 8's
  sessions and tokens close that.
- The env-var budget is a static heuristic: code that reads the environment another way
  (`syscall.Getenv`, scanning `os.Environ()`, importing `os` under another name) would evade
  it. Review is the backstop.
- The vendor breakers key on names: an IP literal of a vendor's API is not recognised. They are
  a self-imposed policy, not a security boundary. The substring match fails closed: a gateway
  URL that merely contains `anthropic.com` or `bedrock` is refused too and needs the opt-in.
- `render.yaml` has never been deployed and is inert until PR 8 (§ Exposure). Render's internal
  connection string may need an explicit `sslmode`, and the instance plans are not pinned; PR 8
  settles both.
- The image build and the Compose smoke test run only in CI's `docker` job (green on GitHub
  since PR 2, including the `COPY LICENSE NOTICE` line); the build sandbox has no Docker
  daemon, so they are not run locally. No `linux/arm64`
  image or release binaries are produced yet, so the Raspberry Pi path means building natively
  with `make build`.
- The Compose exposure check runs only where `docker` is installed (CI). An override file an
  operator adds after checkout is outside the repository; publishing beyond loopback there is the
  operator's own act.
- `govulncheck` runs only in CI: the build sandbox cannot reach `vuln.go.dev`.
- GitHub Actions and container images are pinned to tags (`@v4`, `golang:1.26.9-alpine`,
  `postgres:14`, `postgres:16`, `postgres:16-alpine`), not to SHAs/digests, so an upstream retag changes what CI
  runs.
- Files: no quota. `store.GetFile` itself takes only a file id; scoping is enforced by
  `docs.File` (through a document in scope), and nothing yet stops another package calling the
  store directly (PR 4a's `TestImportBoundaries` pins who may import `store`). Upload slots are
  held only while the client keeps up the read floor, but the floor is low: 16 clients each
  sending 64 KiB per 30 s (about 35 KiB/s in all) can hold every upload slot for the full 15
  minutes. Uploads are buffered per extraction: two at a time (`ingestSlots`), each holding the
  file (≤ 64 MiB) plus a `.docx`'s decompressed parts (≤ 128 MiB).
- **Documents (PR 3a):**
  - Images and scanned PDFs are refused or stored as `noText` until PR 3b's vision transcription;
    there is no OCR. A text-layer PDF whose layer is wrong (a scan with an invisible, bad OCR
    layer) is taken as is. Text that the PDF reader puts in an odd order (columns, tables) stays
    in that order.
  - A text upload that happens to start with `%PDF-` is read as a PDF and refused; posted text
    (`IngestText`) is always text.
  - Every package whose tests ingest a PDF needs a `TestMain` that calls `docs.MaybeRunChild()`
    (the test binary is the PDF reader child), as `internal/docs` does; `cmd/biglaw`'s `main` calls
    it first (`TestBinaryIsItsOwnPDFReader`). The child's memory is a soft limit (1 GiB
    `SetMemoryLimit`), not a hard cap, and only the 2-minute timeout bounds its CPU.
  - Section ids are positions in the section tree (`s0`, `s1`, …) of the text as ingested; a
    change to the heading rules does not renumber stored documents, which keep the ids they were
    ingested with. Re-ingest to pick up new rules.
  - Search ranking is lexical: a paraphrase that shares no word with the passage is not found (the
    one E4 miss, `REG-05`, is a fee-table row the fact's title does not name). The scope's document
    frequencies are computed per query, so a very large scope makes each query slower; nothing
    caps a scope's size.
  - The judge's passage is document text: the judge prompt (PR 4a) must treat it as untrusted
    data, since a document can contain text addressed to the judge.
  - The tolerant rung folds case, so `ACME` verifies against `Acme` (at 0.95).
  - `ledongthuc/pdf` is a small, lightly maintained library pinned at a 2025 pseudo-version; it
    is the second of the three allowed dependencies.
- Test schemas are dropped by `t.Cleanup`; a test binary that is killed (SIGKILL, timeout)
  leaves its `t_<hex>` schema behind. Drop them with
  `psql -c "select 'drop schema '||nspname||' cascade;' from pg_namespace where nspname like 't\_%'" -At | psql`.
- Schema-per-test isolation cannot isolate Postgres extensions, which are database-wide; a
  future migration that needs one must follow the rule in CLAUDE.md.
- No healthcheck for the biglaw container (distroless has no shell); Compose reports it "Up"
  even when `/health` is 503. No access log; logs are text.
- `/health` is unauthenticated and reports the version string. Images built by Compose report
  `2.0.0-dev` (no version is passed at build time).
- Postgres is required, including on a Pi. There is no embedded-database mode.
- **Model client (PR 2):**
  - Nothing calls it yet; `serve` validates its configuration only. The agent loop (PR 4a) is
    its first caller, so no real endpoint has been exercised: every test uses `httptest` or
    `model.Fake`. Behaviour against real Ollama, vLLM or vendor endpoints is unproven until the
    owner's gates run.
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
  - Upstream 400/413/422 (a context-length or schema refusal) is `ErrUnavailable` "HTTP 4xx", not
    retried, and not yet a distinct kind: PR 4a must tell "compact the transcript" from an outage
    (PLAN PR 4a). An answer's tool-call name is not checked against the tools offered; the agent
    loop must (PR 4a).
  - A 200 whose body is an error object (some gateways) is `ErrNoContent` "no choices", not
    retried, and an OpenAI `refusal` is reported as an answer with neither text nor tool calls (its
    text is dropped). A 200 with one malformed `usage` field (a string or a fraction) is
    `ErrUnavailable`: the billed answer is lost and its usage is not counted. An answer with no
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
  - `Strict` and `parallel_tool_calls` are passed through as given; which tools may run in
    parallel, and turning strict off where an endpoint refuses the combination, is PR 4a's policy.
  - `model.Fake` hands out an agent's replies in call order, so concurrent calls *for the same
    agent* get replies in a scheduling-dependent order; script those calls sequentially.
  - **Line budget:** `internal/model` measures 714 lines against the 610 the v2 design planned
    (670 in revision 5, less 60 for the price table as data): 104 over its share. `cmd/biglaw`
    measures 505 against PR 1's 477 (+28). The owner raised the ceiling to 14,500 and DESIGN
    records the move: measured the review's way (PR 1 as built, 1,161, plus PR 2's growth over
    plan, the remaining packages at their DESIGN budgets, cmd's import-v1 reservation intact), the
    plan totals 14,245 of 14,500, 255 lines of headroom.
