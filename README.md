# BigLaw 2

> **Status: under construction on branch `v2`.** This README describes only what is on disk.
> Everything marked **planned** is specified in the v2 build plan and does not exist yet.
> BigLaw 1 (the DyTopo platform) remains on `main`; its frozen copy will be the branch
> `archive/v1-final` (a branch, because tag pushes are blocked in the build environment).

BigLaw 2 will be a legal AI workbench for small firms: memos, contract review and diligence
tables in which every factual statement cites a quote that the software has verified against the
matter's own documents, with a lawyer approving every deliverable. It is one Go binary plus one
PostgreSQL database. Licensed Apache-2.0 (`LICENSE`, `NOTICE`).

## What exists today (PR 1: skeleton, config, store, CI; PR 2: model client)

| Piece | Path | What it does |
|---|---|---|
| Command | `cmd/biglaw/main.go` | `biglaw serve [--allow-no-auth]` (migrates the database, then serves HTTP) and `biglaw version [--config]` |
| Configuration | `cmd/biglaw/config.go` | Declares all 20 runtime variables; validates the ones this build reads (all problems at once); vendor breakers ported from v1 |
| Store | `internal/store/store.go` | Postgres via `database/sql` + `github.com/lib/pq`; DSN check that matches lib/pq's reading; transaction helper |
| Migrations | `internal/store/migrate.go`, `internal/store/migrations/NNNN_*.sql` | Embedded SQL files applied in order under an advisory lock; each file and its `schema_migrations` row in one transaction |
| Files | `internal/store/files.go` | Binary files (uploads, generated `.docx`) stored in 1 MiB `bytea` chunks, streamed in, with size and SHA-256 checked on every read |
| Model client | `internal/model/` (`client.go`, `errors.go`, `fake.go`, `pricing.go`, `prices.json`) | One OpenAI-compatible chat client with tool calls and image parts, typed failures with their HTTP mapping, at most 3 attempts per call, cache-aware pricing from a built-in table plus `BIGLAW_PRICES`, watt-hour metering for local endpoints, and `model.Fake` for tests (§ Model client). Nothing calls it yet: the agent loop that does arrives in PR 4a. |
| Test helper | `internal/store/storetest/` | Gives each DB test its own schema; skips without a DSN, fails if `BIGLAW_REQUIRE_DB` is set |
| Budgets | `scripts/loc.py` (+ `scripts/test_loc.py`) | Fails CI if a size budget is exceeded (below) |
| Build/deploy | `Makefile`, `Dockerfile`, `docker-compose.yml`, `render.yaml`, `.env.example` | `make ci`; `postgres:16-alpine` + biglaw under Compose; Render blueprint (inert until PR 8, see § Exposure) |
| CI | `.github/workflows/ci.yml` | `make ci` against `postgres:14` and `postgres:16` service containers with `BIGLAW_REQUIRE_DB=1`; staticcheck + govulncheck; image build + Compose smoke test |

HTTP surface today: `GET /health` only. It returns `{"status":"ok","version":"…"}` (200) or
`{"status":"database unavailable",…}` (503; the cause is logged, not returned). Every other path
is 404/405.

**Planned** (not built): document ingest/search/verification (PR 3), the
engine and the memo workflow with its REST API (PR 4), diligence (PR 5), contract review (PR 6),
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
| `BIGLAW_MODEL_LEAD` / `_SPECIALIST` / `_CRITIC` | — | Model id per role, as the endpoint names it (not checked at startup: a wrong id fails at its first call with 404 → `model_not_configured`). Unset specialist or critic use the lead's; setting either without the lead is refused. With no lead, model calls fail with 503 `model_not_configured`. | PR 2 |
| `BIGLAW_BUDGET_CALLS` | `150` | Model calls per task | PR 4 |
| `BIGLAW_BUDGET_USD` | `5` | Dollar cap per task | PR 4 |
| `BIGLAW_LOCAL_WATTS` | `250` | Power draw for watt-hour metering of local calls; a number from 0 to 100000 | PR 2 |
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
- **Retries.** At most 3 attempts per call, only after 429, 500, 502, 503, 504 or 529. The wait
  is the provider's `Retry-After` in seconds, or 1 s then 2 s plus up to 25% jitter. A
  `Retry-After` over 30 s (however large: values past int64 too), or a wait that would outlast
  the caller's deadline, ends the call at once with the last error. Network errors are not retried (the request may already have been
  billed).
- **Pricing.** `internal/model/prices.json` is the built-in table, in the `BIGLAW_PRICES` format
  and read by the same parser: an object mapping a model id (case-insensitive; a trailing `*`
  makes it a prefix) to `{"in": USD, "out": USD}` per million tokens, both required and from 0 to
  10000, to `null` for a model that is known but has no price, or to `{"open": true}` for an
  open-weights model with no price (`"open": true` may also join a rate). Repeated ids
  (case-insensitive) and trailing data are refused at startup; other fields inside an entry are
  ignored. Errors never quote the value, an id, or the file's contents. A model id resolves in the operator's `BIGLAW_PRICES` table
  first, then the built-in one; in each, an exact id beats the longest prefix, and
  `vendor/model` (OpenRouter style) is also tried as `model`. So
  `BIGLAW_PRICES='{"deepseek*": {"in": 0.3, "out": 1.2}}'` reprices the whole family, and
  `{"claude-haiku-4-5": null}` un-prices one model. (Under Compose a file path must exist inside
  the container; the inline JSON form is simpler.) The built-in table has 85 entries: the 80 ported
  from v1's `cost/cost.go` (v1's list prices as of early 2026, unverified here; v1's `{0, 0}`
  placeholders are `null`, or `{"open": true}` for `llama*`, `glm*`, `kimi*`), plus open-weights
  markers that keep v1's rates (`qwen2*`, `qwen3*`, `mistral:*`, `gpt-oss*`) and `qwen3-max*`
  (closed, the `qwen*` rate). `deepseek-r1*` and `mixtral*` are marked open too. Cost = (input × in + output × out + cache writes × in × 1.25 +
  cache reads × in × 0.10) / 10⁶. A model with no price has an **unknown** cost (nil), never
  $0; the first call to each such model logs one warning.
- **Local calls.** The host never makes a closed-weights model free. When `BIGLAW_MODEL_URL`'s
  host is local (loopback, a private or link-local IP, `localhost`, a single-label name such as
  the Compose service `ollama`, or a `.local`, `.internal`, `.lan` or `.home.arpa` name), a call
  to an **open-weights** model (marked `"open": true`) or to a model the table does not list costs
  $0 and is metered at `BIGLAW_LOCAL_WATTS` × the call's duration (all attempts). Any other model
  is priced as on a hosted endpoint (or unknown, never $0), so `claude-sonnet-4-6` behind LiteLLM
  on `localhost:4000` still counts against the USD budget. The operator's `BIGLAW_PRICES` entry
  wins: `{"my-model": {"open": true}}` makes a model free locally, `{"qwen*": {"in": 0, "out": 0}}`
  makes a family $0 everywhere. `serve` logs each role's decision once at startup
  (`model pricing … role=lead model=… free=… rate=…`).
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
PR 3's HTTP read deadline does it.

Names must be 1–255 bytes of UTF-8 and not `.` or `..`. They may not contain `/`, `\`, control
characters, invisible format characters (bidi controls, zero-width spaces, the BOM, soft hyphens;
ZWNJ/ZWJ are allowed for Persian, Indic scripts and emoji) or line/paragraph separators, and may
not start or end with white space, any of which can disguise an extension. Content types must
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
`TestFakeIsDeterministic`, `TestFakeRoutesByAgentName`, `TestLocalCallPricedZeroWithWattHours`,
`TestPricesJSONOverride`, `TestImagePartsRoundTrip`, and in `cmd/biglaw` `TestConfigModelVars`.
v1's pricing vectors are carried as `TestLookupPricing`, `TestCalcCostUSD`,
`TestCalcCostUSD_GPT56Luna`, `TestGPT56Siblings_RecognisedButUnpriced`,
`TestSetModelRates_OverridesBuiltins`, `TestRecord_UnknownModelFlaggedAndCounted`,
`TestWarnUnpricedOnce_PerModel`, `TestBuiltinClaudeRatesUnchanged` (v1's
`TestLocalWattMeteredCallsAreFree` is inside `TestLocalCallPricedZeroWithWattHours`), and
`TestBuiltinPricesParse` checks every row of v1's `basePricing` against the new table. Extra:
request validation, stable request bytes for prompt caching, role fallback, local-host
detection, and that no error carries the key, the endpoint URL or upstream text. Round 1 of
review added `TestBilledFailureKeepsUsage`, `TestUnpinnedBehaviours` (no retry after a network
error, `max_completion_tokens`, `Truncated`, the 25% jitter bound, `Retry-After` overflow,
duration over all attempts), `TestTransportErrorsFixedText` (an endpoint echoing the key and the
prompt), `TestAnswerToolCallsChecked`, `TestLocalPricingRule` (with a FIFO price file) and, in
`cmd/biglaw`, `TestServeLogsModelSetup`.

## Budgets (`scripts/loc.py`, run in CI)

| Budget | Ceiling | Today |
|---|---:|---:|
| Go production lines (physical lines, non-`_test.go`, test-support packages included) | 14,250 | 1,906 |
| Packages under `internal/`, test-support packages excluded | 12 | 2 (`model`, `store`; `store/storetest` is test support) |
| Direct Go dependencies | 3 | 1 (`github.com/lib/pq`) |
| Runtime environment variables | 20 | 20 |
| UI runtime dependencies | 2 | 0 (no UI yet) |
| Packages without a `_test.go` | 0 | 0 |
| Lines in any one source file (`.go`, `.py`, `.sql`, `.sh`, `.js`, `.ts`, `.css`; tests included) | 2,000 | 595 |

The line ceiling is 14,250 (owner decision; it was 14,000). A **test-support package** is one
whose directory name ends in `test` and that no production file outside it imports, such as
`storetest`. Under the coordinator's ruling it is left out of the package count, but its lines
count toward the line ceiling like any other non-`_test.go` file. A `*test` package that
production code imports is an ordinary package. loc.py also fails if `LICENSE` or `NOTICE` is
missing or a `.go`/`.py` file lacks its SPDX header. Env-var names are found statically (literal
`os.Getenv`/`LookupEnv`/`getenv` arguments, the config table, and `BIGLAW_*`/`ALLOW_*` literals).
An `os.Getenv`/`os.LookupEnv` argument that is not one plain string literal (a variable, a
concatenation such as `"BIGLAW_" + x`, a raw string) fails the check.

## Limitations

- Nothing user-facing exists yet beyond `/health`; every workflow, the REST API, auth and the
  UI are planned. The exposure rule above is the only protection until PR 8.
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
- Files: there is no per-matter scoping or owner yet (`GetFile` takes only an id; PR 3 adds the
  document/matter reference and access checks), no delete, no quota, and no limit on concurrent
  reads. Each concurrent read of a maximum-size file holds about 64 MiB live and peaks near
  128 MiB at the default `GOGC`, so the API that exposes files (PR 3) must cap concurrent reads
  on a 4 GB Pi or stream them (PLAN PR 3). Uploads are capped at 16 at once, but a reader that
  blocks (or trickles) holds its slot and connection until it returns, so 16 stalled clients stop
  further uploads (not `/health` or reads) until PR 3's per-request read deadline ends them.
- The file-name policy is provisional: it has no production caller yet. PR 3 decides it with
  the upload/download API. It still admits invisible characters outside category Cf (Hangul
  filler U+3164, braille blank, combining grapheme joiner, variation selectors, ZWJ runs) and
  interior runs of no-break spaces, which can push an extension out of view. Names such as `CON`, `aux.txt` or `c:evil.txt` are stored as given,
  and PR 3 must make them safe on download (RFC 6266 `filename*`).
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
  - **"Local" is a guess from the host name**, and "open weights" is a list in the data. An
    open-weights or unlisted model behind a local gateway that forwards to a paid host (LiteLLM to
    OpenRouter for `llama-3.1-70b`, or a fine-tune name the table does not list) is $0, so only the
    call budget bounds it; the startup `model pricing` line shows it, and a `BIGLAW_PRICES` entry
    prices it. The reverse also holds: an open-weights model run on your own GPU behind a public
    name or a Tailscale address (100.64/10) is priced at its hosted rate unless you mark it
    `{"open": true}`.
  - A model with no price (unknown, or `null`) has an unknown cost: the USD cap cannot count it,
    and only the call cap bounds it. One warning is logged per model per process.
  - The built-in rates are v1's list prices (early 2026) and were not re-verified for v2; they
    may be stale. v1's substring families became prefix keys, so a name that only *contains* a
    family (`my-haiku-finetune`) is no longer priced as that family; `vendor/model` ids are
    still matched. One carried vector changed (`gemini-2.5-flash-lite-preview` now takes the
    exact flash-lite row's 0.10/0.40, not v1's contradictory class rate 0.075/0.30).
  - Inside one `BIGLAW_PRICES` entry, Go matches `in`/`out`/`open` case-insensitively, a repeated
    field keeps the last value, and other fields are ignored; an exactly repeated id keeps the last
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
    GPU's energy for a failed generation is not metered.
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
  - **Line budget:** `internal/model` measures 730 lines against the 610 the v2 design planned
    (670 in revision 5, less 60 for the price table as data): 120 over its share. `cmd/biglaw`
    measures 492 against PR 1's 477. Measured the review's way (PR 1 as built, 1,161, plus PR 2's
    growth over plan, with the remaining packages at their DESIGN budgets and cmd's import-v1
    reservation intact), the plan totals 14,248 of the 14,250 ceiling: 2 lines of headroom, which
    is for the owner to rule on.
