# BigLaw 2

> **Status: under construction on branch `v2`.** This README describes only what is on disk.
> Everything marked **planned** is specified in the v2 build plan and does not exist yet.
> BigLaw 1 (the DyTopo platform) remains on `main`; its frozen copy will be the branch
> `archive/v1-final` (a branch, because tag pushes are blocked in the build environment).

BigLaw 2 will be a legal AI workbench for small firms: memos, contract review and diligence
tables in which every factual statement cites a quote that the software has verified against the
matter's own documents, with a lawyer approving every deliverable. It is one Go binary plus one
PostgreSQL database. Licensed Apache-2.0 (`LICENSE`, `NOTICE`).

## What exists today (PR 1: skeleton, config, store, CI; PR 2: model client; PR 3a: documents; PR 3b: model roles, embeddings, hybrid search; PR 3c: vision transcription; PR 11, pulled forward by owner decision 13: the UI and its recorded engine; PR 4a: the agent runtime and the deterministic memo tools; PR 4b: the memo workflow, durable runs and the REST API)

| Piece | Path | What it does |
|---|---|---|
| Command | `cmd/biglaw/main.go` | `biglaw serve [--allow-no-auth]` (migrates the database, then serves the API and the embedded UI and runs tasks) and `biglaw version [--config]` |
| Configuration | `cmd/biglaw/config.go` | Declares all 18 runtime variables (`BIGLAW_MODELS` is the role map); validates the ones this build reads (all problems at once); vendor breakers ported from v1 |
| Store | `internal/store/store.go` | Postgres via `database/sql` + `github.com/lib/pq`; DSN check that matches lib/pq's reading; transaction helper |
| Migrations | `internal/store/migrate.go`, `internal/store/migrations/NNNN_*.sql` | Embedded SQL files applied in order under an advisory lock; each file and its `schema_migrations` row in one transaction. `store.Open` first creates the `vector` extension (pgvector), which is database-wide (§ Database) |
| Files | `internal/store/files.go` | Binary files (uploads, generated `.docx`) stored in 1 MiB `bytea` chunks, streamed in and out, with size and SHA-256 checked on every read |
| Model client | `internal/model/` (`client.go`, `embed.go`, `errors.go`, `fake.go`, `pricing.go`, `prices.json`) | One OpenAI-compatible chat client with tool calls and image parts, an embeddings call (`Embed`) on the same transport, typed failures with their HTTP mapping, at most 3 attempts per call, cache-aware pricing of every call from a built-in table plus `BIGLAW_PRICES` (the host never makes a call free), watt-hour metering when `BIGLAW_LOCAL_WATTS` is set, and `model.Fake` for tests (§ Model client). `Embed`'s caller is documents (below); `Chat`'s is the agent loop (below), which the memo workflow drives. |
| Documents | `internal/docs/` (`ingest.go`, `child.go` + `child_linux.go`/`child_other.go`, `pageindex.go`, `chunk.go`, `search.go`, `embed.go`, `verify.go`, `http.go`, `vision.go` + `vision_prompts.txt`, `scanpdf.go`, `jobs.go`) | Ingest of UTF-8 text, `.docx` and PDF text layers (PDFs and `.docx` files parsed in a bounded child process); images and JPEG-scanned PDF pages transcribed by the `vision` role, each page read twice and the readings compared, with provenance on every document, chunk and citation, and a leased job that transcribes what was stored before a vision role was set (§ Vision transcription); the byte-exact section tree; search chunks; Postgres full-text search, fused with pgvector similarity when an `embed` role is set (chunk embedding at ingest, a startup backfill); the page reader (outline, paged sections); the quote-verification ladder; the upload and download routes. Every read is scoped to the caller's document ids (§ Documents) |
| Agent runtime | `internal/agent/` (`tool.go`, `registry.go`, `schema.go`, `loop.go`, `budget.go`, `policy.go`, `bandit.go`) | The `Tool` interface and its single registry (definitions frozen at registration), JSON-schema validation of every tool input and output, one bounded loop for the lead and its subagents, the budget ledger, and the tool bandit (§ Agent runtime). The memo workflow drives it (below); a step log makes it resumable |
| Engine | `internal/engine/` (+ `specs/`, `packs/`; migration `0005_runs.sql`) | The memo workflow: the lead agent and its 11 tools, the ledger, the evidence subagents with Go-side quote verification, the sweep, the three-critic committee, the policy, `finish` (lint, coverage, render), the human gates, grounding collapse, and durable runs (step log, lease and heartbeat, resume) (§ Memo workflow) |
| API | `internal/api/` (+ `dist/`, the embedded UI) | The REST API (tasks, steps, SSE events, gates, cancel, cost, clients) over the engine and the document routes, and the web UI built into the binary (§ Memo workflow) |
| Memo tools | `internal/analysis/` (+ `belo.json`, `specs/*.json`), `internal/docs/register.go` (+ `specs/*.json`) | Deterministic tools over verified findings: `numbers` (exact `calc`, figure handles, key-figures table), `join_discrepancies`, the coverage checks `finish{op:"check"}` will run, the value normalisers and BELO-lite; and the `retrieve` / `verify_quote` adapters over documents (§ Agent runtime) |
| `.docx` reader | `internal/docs/ooxml/` | Opens a `.docx`'s text parts only (body, footnotes, endnotes; 128 MiB decompression cap) and reads its visible text in one linear XML pass. The writer, the XML escapers, the part-preserving re-zip and tracked-change parse/apply are planned (PR 3d) |
| Evaluation | `eval/` (`run.py`, `proofrun/`, `lab/`, `tests/`, `tasks/`, `e4/embeddings.json`) | The five synthetic proof-run matters (documents + answer keys), copied from `feat/proof-runs-multi-practice`, with their driver, scorer and the v1 comparison (`run.py`), the Harvey LAB driver (`lab/run.py`), and the Go gates E1/E2 that run them through the engine on a scripted model (§ Memo workflow). `eval/e4/embeddings.json` is E4's embeddings tape: today the **self-test** tape of a hash embedder, not a real model's (§ Documents, gate E4) |
| UI | `ui/` (React 19 + Vite + TypeScript strict; runtime deps `react`, `react-dom` only) | The seven screens (§ UI) behind one transport seam, `ui/src/api/transport.ts`: `http.ts` talks to the Go API on the same origin (tasks, steps, findings, gates, cost and documents are served since PR 4b; playbooks, time and profiles are PRs 6/8/9), `recorded.ts` replays a recorded engine in the browser with no network at all |
| Recorded demo | `ui/src/recorded/recording.json`, `eval/demo/record_test.go` | Three synthetic matters (13 documents plus one scanned exhibit, `ui/src/recorded/06-affidavit-of-service-scan.jpg`, drawn by `eval/demo/make_scan.py`) and seven recorded memo tasks (48 findings, 79 citations); documents, outlines, sections, chunks, the scan's transcript (v2's vision path with a scripted reader), run-log and offered search hits and every quote's verification computed by v2's `internal/docs`; claims from the answer keys; sample playbooks, time entries and profiles, and the configuration table read from `cmd/biglaw/config.go` (§ UI, provenance). `TestDemoRecording` fails when the committed recording no longer matches what v2's code computes |
| Test helper | `internal/store/storetest/` | Gives each DB test its own schema; skips without a DSN, fails if `BIGLAW_REQUIRE_DB` is set |
| Budgets | `scripts/loc.py` (+ `scripts/test_loc.py`) | Fails CI if a size budget is exceeded (below) |
| Build/deploy | `Makefile`, `Dockerfile`, `docker-compose.yml`, `render.yaml`, `.env.example` | `make ci` (and the owner-run `make e4-record` / `make e4-live`); `pgvector/pgvector:0.8.6-pg16` + biglaw under Compose; Render blueprint (inert until PR 8, see § Exposure) |
| CI | `.github/workflows/ci.yml` | `make ci` against `pgvector/pgvector:0.8.6-pg14` and `:0.8.6-pg16` service containers (pinned by digest) with `BIGLAW_REQUIRE_DB=1`; staticcheck + govulncheck; image build + Compose smoke test |

HTTP surface today: the REST API of § Memo workflow, the embedded UI at `/`, and `GET /health`, which returns `{"status":"ok","version":"…","search":"hybrid"}` (200;
`"search":"lexical"` when `BIGLAW_MODELS` has no `embed` role) or
`{"status":"database unavailable",…}` (503; the cause is logged, not returned). Every other path
is 404/405. There is no authentication yet (§ Exposure).

**Planned** (not built): the `.docx` writer and tracked-change parse/apply (PR 3d); diligence (PR 5), contract review (PR 6),
MCP (PR 7), clients/conflicts and OIDC/session/token auth (PR 8), time and billing (PR 9), the
the full jurisdiction and practice pack catalogue (PR 12; four candidate packs ship with PR 4b), v1 import and docs (PR 13). The
discover.legal intake module is **not** part of v2 (owner decision; it moves to a separate repo).

## Configuration

Environment variables only. All 18 are declared in `cmd/biglaw/config.go` (20 until PR 3b, whose
`BIGLAW_MODELS` replaced the three role variables); each is validated by
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
| `BIGLAW_PUBLIC_URL` | `http://127.0.0.1:3101` | External base URL (OIDC redirect, cookie `Secure`) | PR 8 |
| `BIGLAW_MODEL_URL` | — | OpenAI-compatible API base, ending in its version (`http://localhost:11434/v1`); calls go to `<URL>/chat/completions` and `<URL>/embeddings`. http(s), a plain ASCII host with at least one letter or digit, a port up to 65535, no `@` anywhere (no credentials in the URL; write any other `@` as `%40`). Required when the key or a role is set. | PR 1 |
| `BIGLAW_MODEL_KEY` | — | API key for that URL, sent as `Authorization: Bearer`; no white space or control characters. `serve` warns if it would go over plain `http`. | PR 2 |
| `BIGLAW_MODELS` | — | Model id per role, as the endpoint names it: comma-separated `role=id`, roles `lead`, `specialist`, `critic`, `vision`, `embed`, each at most once (e.g. `lead=qwen2.5:14b,critic=glm-4.5-air,embed=nomic-embed-text`). Unset `specialist` or `critic` use the lead's; setting either without the lead is refused. `vision` and `embed` have **no** fallback: with no `embed` role search is lexical only (`/health` says `"search":"lexical"`); `vision` names the model images and scanned pages are sent to; without it they are stored as `needs_transcription` (503 `vision_not_configured`) and never sent to another role's model (§ Vision transcription). An unknown role, a repeated one, an empty id, an id over 200 bytes, or an id holding `://`, `user:password@`, white space, a control or invisible (format) character, `?` `=` `&`, or a key's shape (`sk-`, `sk_`, `AKIA`, `ASIA`, `AIza`, `xox?-`, `gh?_` prefixes, or 24 letters and digits in a row) is a startup error that does not echo the value. A wrong id fails at its first call with 404 → `model_not_configured`; with no lead, chat calls fail with 503 `model_not_configured`. | PR 3 |
| `BIGLAW_BUDGET_CALLS` | `400` | Model calls per task, lead turns included (at the first bench's $0.012–0.020 a call, 400 calls cost $4.80–8.00, so the default $5 usually binds first; every share holds on both) | PR 4 |
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
model URL/key/models and the OIDC variables; Render adds the database URL and address), or if any
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

Two browser guards hold until then, because a page the lawyer visits can reach a loopback port.
Every `POST` (documents, tasks, cancel, gate decisions) from another site is refused 403
`cross_origin` (`Origin` other than the request's own host, or `Sec-Fetch-Site` other than
`same-origin`/`none`; `text/plain` and `multipart/form-data` need no CORS preflight), so no site
can spend the budget or decide a human gate (`TestWritesRefuseCrossOrigin`). Every request but
`GET /health` addressed to a DNS name other than `localhost` is refused 403 `bad_host` (DNS
rebinding would otherwise make another site BigLaw's own origin): address BigLaw by IP or
`localhost` (`TestRefusesRebindingHosts`). The UI cannot be framed (`X-Frame-Options: DENY`,
`frame-ancestors 'none'`: `TestUIRefusesFraming`).

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
make build                      # static binary: CGO_ENABLED=0, version stamped (no UI yet)
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
  `vector` is not verified here (`render.yaml` is inert until PR 8). The `vector` type
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
body text), `(a)` and `(i)` items, schedules/exhibits/annexes, recitals, and Markdown `#` or bold
headings. The tree is bounded so storage stays linear in the input (each chunk carries its
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
substring (curly quotes straightened, U+2010/U+2011 hyphens read as `-`, white space collapsed, case folded on both sides; `TestToleranceFoldsHyphens`), 0.95; one
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
  registry refuses a duplicate name, a 23rd tool, a schema that is not strict-shaped (every object
  `additionalProperties: false`, every field required, optional fields nullable; in and out), a
  description under three sentences or without a "Do not use" line, and 1–3 `Example: {…}` lines
  that do not validate. Definitions are frozen when registered: the same bytes reach the model on
  every turn (prompt cache), and no matter data can reach a schema. The definition sent leaves out
  `minLength`/`maxLength` (strict mode refuses them); Go enforces them before a tool runs. At most 14 tools are visible to
  one agent. Specs are data: `internal/docs/specs/*.json`, `internal/analysis/specs/*.json`.
- **One loop, two depths** (`Runtime.Run`). The lead (depth 0) and a subagent (depth 1, the
  `Subagent` tool: its own prompt, allowed tools, turn cap, grant and output schema; its transcript
  is discarded) run the same loop. Every tool call goes through `Runtime.Invoke`, in this order:
  offered to that agent, no subagent or fan-out tool at depth 1, alone when it must be (`finish`,
  `ledger{op:"plan"}`), input schema, the task's `Policy` (PR 4b), then a grant; the tool's usage is
  charged even when it fails and its output must match its schema. A subagent's answer must validate
  (one repair request, then `ErrToolOutput`). At most 8 tool calls run per turn. Tool results older
  than four turns are replaced by a handle naming their step, and any one result is cut to 100 KB
  in the transcript with a note on how to page (`TestOversizedResultCappedAndShrunk`; bench4: a
  682 KB ledger result overflowed the lead's context); an endpoint's 400/413/422 compacts all
  but the latest turn's and retries, then cuts the latest results to 16 KB and retries once more; the cut is for that request only, and it is structure-aware: a JSON object loses trailing array items (or the tail of its largest string) and stays valid JSON with its scalar fields such as `next` (`TestR2VShrinkSticksAndHidesNext`, `TestClipJSONDropsTrailingItems`). Text in tool results is labelled untrusted matter data.
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
- **`join_discrepancies`**: cross-document conflicts between verified values that share a metric
  label, a quantity class (money is never compared with a share count) and an entity after the
  lead's aliases and name containment ("Ostrowski" in "Richard Ostrowski"); dates tie to one event by
  label or two shared distinctive words and are compared at their common precision; a conflict needs
  two documents with disjoint value sets. Durations and identifiers are not compared. Whatever their
  labels, two values of one class from two documents that are near each other (a quantity within a
  quarter, a full date within a month), whose quotes share three distinctive words, are also a
  conflict when neither document states the other's value (4,217 vs 4,312 trades analysed, $92,600
  vs $103,800, 73% vs 78%, April 22 vs April 15: `TestNearConflictsAcrossLabels`; at most 20). Its
  output also names the documents the sweep left partly unread (`unreadDocuments`).
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
- `join_discrepancies` trusts the extracting model's metric labels for the label join: one label
  for two metrics is compared (the lead must check the quotes). The near join finds two labels for
  one metric only when the values are close (an eighth; a month for dates), of one entity,
  compared after their stated scale (the value's, its unit's, or the word after it in its quote; a
  bare figure whose scale nothing states never pairs with a scaled one, while a figure written in full, grouped or seven digits, does: `TestJoinComparesScaledFigures`, `TestR2VScaledVsWholeFigure`), a value its quote frames as a part of the other's quantity ("$950,000 of the Purchase Price", a deposit or instalment toward it) never paired with it (`TestR2VHoldbackNearFalsePair`), their
  labels not naming two things (`sameMetric`) and the quotes share their words: a conflict between
  far-apart values under different labels is still missed, and two close figures about different
  things that share words and label words can still be reported (`TestJoinRefusesDifferentFacts`). A value's document is the quote that holds its text, else the finding's first quote.
- Calls in one turn run sequentially, not concurrently; `retrieve` and Go tools spend no calls, so a
  lead can issue up to 8 per turn for every turn its budget funds.
- **Line budget:** `agent` 1,242 against 1,100, `analysis` 1,183 against 950, the `docs` adapters
  153 against 50 (§ Budgets: accepted by owner decision 14; no total ceiling since decision 17).

## Memo workflow, durable runs and the REST API (`internal/engine`, `internal/api`; PR 4b)

`POST /tasks` with `workflowType: "memo"` (the only workflow built; `diligence` and
`contract_review` are 400 until PRs 5 and 6) stores a task and wakes the runner. The lead agent
(role `lead`, prompt `internal/engine/specs/lead.md`) sees 11 tools in a fixed order: `retrieve`,
`ledger`, `sweep`, `extract_facts`, `analyze_issue`, `check_deviation`, `numbers`,
`join_discrepancies`, `draft_section`, `committee`, `finish`. Specs are data
(`internal/engine/specs/*.json`, subagents `specs/agents/*.{json,md}`: the five above plus the
critics `prosecute`, `reproduce`, `audit_gaps`). Everything below is tested with `model.Fake`.

- **The ledger** (`ledger`; one Postgres table, a JSON row per kind and key): `plan` (the issues
  the deliverable must answer, each typed by a BELO-lite class), `findings` / `get` (brief or full:
  citations and value refs `F3.v1`; `findings` a page of about 12 KB at a time (under the 16 KB over-long cut), `offset` from the
  result's `next`, `TestLedgerFindingsPage`), `resolve` (a critic finding fixed or disclosed, with a note),
  `draft_get` (a finished section only: `not_drafted` otherwise) and `relations`. A subagent may only
  read, and never sees a quarantined finding (G3).
- **Evidence** (`extract_facts`, `analyze_issue`, `check_deviation`; subagents on the `specialist`
  role). Go completes each call (the section's text, the packs) and checks every answer before it
  reaches the ledger: each quote goes through the ladder (`VerifyQuote`, then the `verify_quote`
  tool with its judge); a finding is verified only when every quote is (G2), else it is stored
  quarantined (G3) and never drafted from; a typed value absent from its own quotes is dropped, and a
  money value's currency is taken from the symbol beside it in its quote (£, €, ¥, CHF, C$, A$), not the model; a finding
  nearly the same as a verified one already held on the same issue (3-word-shingle Jaccard ≥ 0.5)
  merges into it, its quotes added. `check_deviation`'s requirement is the controlling document's own
  words: Go verifies it in `requirementDocId` (one of `controllingDocIds`) like any quote before the
  subagent runs and hands the subagent the document's text, with the reviewed documents' passages
  most like it (`reviewedPassages`, ≤ 16 KB, so it answers at once: `TestDeviationGetsReviewedPassages`;
  its only tool is `retrieve`, as Go verifies every quote it gives): JSON escape artefacts (`\"Tommy\"`) are
  undone, and a requirement joined from several passages (a heading and its list, two sentences from
  two paragraphs) verifies part by part, split where the model joined them (at most 12 joins; its
  ellipses stripped from the parts' edges; one normalised index of the document, the caller's ctx),
  every part the document's own words, the parts in the
  document's order, one passage (§ Limitations, LAB round 3); a part under four words only as a
  heading line before another. The verified requirement is then read in its context (`requirement.go`
  `reqContext`, by structure: markers, colons, blank lines, headings, never word lists): the paragraph or
  list item that holds it, every introduction that governs it (a unit above ending with a colon, the line
  that opens its list; an earlier item of the same list passed over) and its section heading, about 1,500
  bytes with the requirement marked ⟦ ⟧. The subagent judges against that context (`requirementContext`),
  its verdict's controlling quote must lie in it (else the verdict is dropped), and the finding quotes it
  verbatim as its controlling citation, the verdict's words marked, with the part and the correction given
  as the analyst's words, not quotations; prosecute checks them against it (`TestR2VProbesReadInContext`,
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
  (`TestExtractFactsRefusesABadThrough`); a visit is keyed (document, section, offset[, through]) and never repeated;
  it stops at its share of the budget and reports `incomplete` with the sections it did not reach
  (a batch as its whole run, "s3 to s9": `TestSweepUncoveredNamesTheWholeBatch`),
  which also go into Limitations. Never silently partial. It reads up to `sweepWorkers` (4) visits at
  once and is still deterministic: a visit starts with a full grant (the subagent's turns and a
  quarter of the sweep's USD) while one is free, else only once nothing is in flight, with what is
  left, so no grant depends on timing and the shares hold exactly as one at a time; findings are
  admitted (fids, near-duplicate merges) and visits recorded in reading order (each visit waits, after
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
  with the draft, `audit_gaps` gets the draft. Their findings are HIGH/MEDIUM/LOW, open until the
  lead resolves them; a `prosecute` verdict credits or debits the arm that produced the finding
  (delayed credit, ±0.5). A critic that fails leaves a MEDIUM "did not run" finding.
- **Policy** (`policy.go`): P1 nothing but `retrieve`/`ledger` before a plan, and if the lead has
  spent its plan share (7%, at least 3 calls) without one, the packs' checklist becomes the plan
  (disclosed); P4 class shares, each on calls **and** USD (evidence 50% of which sweep 39% and the
  coverage pass 6%, kept for it, so the lead's own evidence calls may spend 44%, at least 5% beside a
  full sweep; draft
  15%, committee cycles of 20 calls or 20% of a small budget with a fifth of the USD, a reserve of 5
  and meta 3 only `finish` spends). The root budget keeps back the unspent drafting share from
  evidence, one committee cycle and (until it has run) the coverage pass from everything below the
  committee (lead turns included), and the
  reserve from all but `finish`, so evidence can never spend the draft's budget and the last draft can
  always be reviewed: when the lead runs out with a draft no committee has read, Go runs the
  committee on it from that cycle before the forced finish (`TestPhaseSharesHoldInUSD`,
  `TestCommitteeAlwaysReviewsTheLastDraft`). `sweep` stops at its USD share as at its calls. The
  sweep's 39% (35% before r3) is sized from the bench: at about $0.035 a one-call visit, 39% of $11
  is about 120 pages, so the trust compare set (86 pages) is read whole and the SEC referral set
  (142 pages as the LAB driver converts it) to about five sixths, its least relevant pages last;
  reading all of it needs about $5 of sweep, more than the evidence share at $11 (an estimate from
  bench costs, not yet measured on a rerun).
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
  `{Heron}`). A memo citing a `{W…}` worksheet with typed inputs (a period, a rate) says so in
  Limitations, with what it computes from what. After two `finish` calls that reported lint problems, the next discloses them in
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
  disclosed ("Coverage: N verified findings … never reached the memo"). Every figure conflict the
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
- **Gates** (G7): every deliverable opens a gate (`G-deliverable-N`); a finding resting only on
  low-confidence transcript pages gets its own (`G-<fid>`). The run stops `awaiting_gate`; approving
  the deliverable completes the task; rejecting it (a reason is required) puts the reason in the
  lead's log as a note step and the run resumes. Once all finding gates are decided the run resumes
  with the decisions; a rejected finding leaves the memo.
- **Grounding collapse** (G4): from 8 findings on, if more than `BIGLAW_COLLAPSE_THRESHOLD` of them
  are quarantined the task fails (`strict`, default) with a grounding alert, or (`warn`) carries the
  alert into the Evidence Note.
- **Model failures** (G6) end the run `failed` with the kind's fixed text (`model_rate_limited`,
  …), never a finding, never the endpoint's words.

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
its whole grant). A task whose document
was deleted after submission fails (`bad_request`) instead of being claimed again forever. A run stops `finished` (gate open),
`budget_exhausted` or `max_steps` (a draft changed since its last review gets one committee from the
kept cycle, then `finish` is forced from the reserve, open findings disclosed; `maxSteps` defaults to
twice the call budget, at least 400),
`awaiting_gate`, `cancelled` or `failed`. Tool-bandit statistics persist in `tool_stats`.

**Packs** (`internal/engine/packs/{jurisdiction,practice}/*.md`, embedded): `us`, `us-ny`,
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
GET    /                              the web UI (embedded by `make ui build`), or a notice in a build without it
```

Model failures map to `model_not_configured` (503), `model_rate_limited` (503), `model_timeout`
(504), `no_content` (422), `model_unavailable` (502), a cancelled request to 499; a grounding
collapse is 422 `grounding_collapse`; an unknown task 404, a gate that is not open 409; a
cross-site write 403 `cross_origin`, a request by DNS name 403 `bad_host` (§ Exposure).

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
engine and API, scored by the Python scorer: 1.0, verified rate 1.0), `TestProofRunsFabricator`
(E2: every matter fails with a grounding alert) and `TestLabRunnerSmoke` (the LAB driver against an
`httptest` server) run with no model.

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
`running`; the recorded demo has no revised run, so there it stays `rejected`). The Task screen's Table and
Redline tabs (diligence, contract review) are **planned** with PRs 5 and 6, the Documents version
timeline with PR 6, sign-in with PR 8.

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
                                # loc.py + its tests, docker compose config
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
`httptest` server), `TestSweepShareBounded`, `TestSubagentLedgerReadsSkipQuarantine`, `TestUSDBoundFinishDeliversWithoutMeta`, `TestCurrencyFromQuote`,
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
| Go production lines (physical lines, non-`_test.go`, test-support packages included) | reported, no limit (owner decision 17) | 14,745 |
| Packages under `internal/`, test-support packages excluded | 12 | 8 (`agent`, `analysis`, `api`, `docs`, `docs/ooxml`, `engine`, `model`, `store`; `store/storetest` is test support) |
| Direct Go dependencies | 3 | 2 (`github.com/lib/pq`, `github.com/ledongthuc/pdf`) |
| Runtime environment variables | 20 | 18 |
| UI runtime dependencies | 2 | 2 (`react`, `react-dom`) |
| UI source lines (`ui/src` `.ts`/`.tsx`/`.css`; tests, config and `recording.json` not counted) | 2,400 | 1,902 |
| Packages without a `_test.go` | 0 | 0 |
| Lines in any one source file (`.go`, `.py`, `.sql`, `.sh`, `.js`, `.ts`, `.css`; tests included) | 2,000 | 968 (`internal/docs/vision_safety_test.go`) |

**There is no total line ceiling (owner decision 17, 2026-10-23).** `scripts/loc.py` reports the Go production total (14,745) and fails only on the budgets that bound bloat directly: no file over 2,000 lines, ≤ 3 direct deps, ≤ 20 env vars, ≤ 12 internal packages, ≤ 2,400 UI source lines, no untested package. Each PR still reports its lines against its DESIGN budget in its notes. (The ceiling's history, 14,000 → 17,500 through owner decisions 7–16, is in PLAN.) A **test-support package** is one
whose directory name ends in `test` and that no production file outside it imports, such as
`storetest`. Under the coordinator's ruling it is left out of the package count, but its lines
count toward the reported Go total like any other non-`_test.go` file. A `*test` package that
production code imports is an ordinary package. loc.py also fails if `LICENSE` or `NOTICE` is
missing or a `.go`/`.py` file lacks its SPDX header. Env-var names are found statically (literal
`os.Getenv`/`LookupEnv`/`getenv` arguments, the config table, and `BIGLAW_*`/`ALLOW_*` literals).
An `os.Getenv`/`os.LookupEnv` argument that is not one plain string literal (a variable, a
concatenation such as `"BIGLAW_" + x`, a raw string) fails the check.

## Limitations

- The UI's Playbooks, Time & Billing and profile views have no live routes until PRs 6/8/9 (404);
  Clients shows matters derived from documents' matter numbers until PR 8's client roster.
- **Memo workflow (PR 4b):**
  - No model has run it: every test scripts the model. Whether it meets gate A (v1's criteria on
    `litigation` and `ma-diligence`) is the owner's measurement (§ Memo workflow, run it).
  - The deliverable is Markdown only (`GET /tasks/{id}/deliverable.md`); the `.docx` export waits for
    PR 3d's writer.
  - Quote verification proves a quote is in the document, not that the claim follows from it:
    `prosecute` checks that, and a lawyer approves every deliverable. The G8 lint traces digits, not
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
  - Until PR 8, BigLaw answers only requests addressed by IP or `localhost` (§ Exposure): a LAN or
    proxy hostname gets 403 `bad_host`.
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
  The exposure rule above is the only protection until PR 8: anyone who can reach the port can
  upload, list and read every document, and run, approve or cancel tasks. The cross-origin check stops a web page from uploading through the lawyer's browser,
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
    visit, three calls), not a measured split; the 39% share's reach is estimated from it.
  - A salient finding is judged by its tool, severity, typed relations, figures or the words of its
    claim ("deviates", "does not", "fails to"): a left-out finding stated otherwise is not placed, and
    a claim that merely says "does not" is. The pass places at most 40, one redraft per issue.
  - A requirement's parts must be the document's words, in its order, one passage: only white space
    and list markers between them, or, after a sentence, at most two whole sentences (400 bytes); never
    a heading or a list's introduction between them. A fragment is not refused: it is read and quoted
    in its context, which is found by structure, so a document whose structure the text does not show
    (a PDF that wraps lines without blank lines or markers) gives a coarser context: a wrapped
    paragraph's lines are one unit, an unmarked heading needs a blank line above it, and a run-on line
    can be taken for a heading or an item. Over-inclusion is the chosen failure: a sibling's colon line or
    a preceding list's opener may be quoted as context. The context is bounded at about 1,500 bytes
    (an introduction keeps its last 300 bytes); the requirement is always whole. Whether the subagent
    and prosecute read the context correctly is the model's judgment: the context makes the governing
    words visible, it does not make the verdict right.
  - The near join pairs only one entity's values (alias-unified, or both with none), within an eighth
    of each other, and not when each label names a thing its own quote states and the other side
    never mentions (a fee and an allocation, a notice and a demand); dates joined by shared words take
    the same label test. It is a word test: a part of a figure is told apart from it only when its
    quote frames it as one ("of the Purchase Price", a deposit, instalment or holdback beside the
    other's label); a part described otherwise is told apart only by the eighth.
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
    It runs once, one analysis and one redraft per touched issue, within its 6% share; issues it does
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
  - Until PR 8 there is no auth or quota: anyone who can reach the port can upload documents and
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
  - Until PR 8 there is no auth or quota: anyone who can reach the port can make the server pay for
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
  - Only `Embed` has a caller yet (documents); `Chat`'s first caller is the agent loop (PR 4a),
    so no real endpoint has been exercised: every test uses `httptest` or
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
