# BigLaw 2

> **Status: under construction on branch `v2`.** This README describes only what is on disk.
> Everything marked **planned** is specified in the v2 build plan and does not exist yet.
> BigLaw 1 (the DyTopo platform) remains on `main`; its frozen copy will be the branch
> `archive/v1-final`.

BigLaw 2 will be a legal AI workbench for small firms: memos, contract review and diligence
tables in which every factual statement cites a quote that the software has verified against the
matter's own documents, with a lawyer approving every deliverable. It is one Go binary plus one
PostgreSQL database.

## What exists today (PR 1: skeleton, config, store, CI)

| Piece | Path | What it does |
|---|---|---|
| Command | `cmd/biglaw/main.go` | `biglaw serve` (migrates the database, then serves HTTP) and `biglaw version [--config]` |
| Configuration | `cmd/biglaw/config.go` | Reads and validates all 19 runtime variables; reports every problem at once; ports v1's vendor breaker |
| Store | `internal/store/store.go` | Postgres via `database/sql` + `github.com/lib/pq`; transaction helper |
| Migrations | `internal/store/migrate.go`, `internal/store/migrations/NNNN_*.sql` | Embedded SQL files applied in order under an advisory lock, each in its own transaction |
| Files | `internal/store/files.go` | Binary files (uploads, generated `.docx`) stored as `bytea` with a SHA-256 checked on every read |
| Test helper | `internal/store/storetest/` | Gives each DB test its own schema; skips without a DSN, fails if `BIGLAW_REQUIRE_DB` is set |
| Budgets | `scripts/loc.py` | Fails CI if a size budget is exceeded (below) |
| Build/deploy | `Makefile`, `Dockerfile`, `docker-compose.yml`, `render.yaml`, `.env.example` | `make ci`; Postgres 16 + biglaw under Compose; Render blueprint (untested, see Limitations) |
| CI | `.github/workflows/ci.yml` | `make ci` with a `postgres:16` service container and `BIGLAW_REQUIRE_DB=1` |

HTTP surface today: `GET /health` only. It returns `{"status":"ok","version":"…"}` (200) or
`{"status":"database unavailable",…}` (503). Every other path is 404/405.

**Planned** (not built): the model client (PR 2), document ingest/search/verification (PR 3), the
engine and the memo workflow with its REST API (PR 4), diligence (PR 5), contract review (PR 6),
MCP (PR 7), clients/conflicts and OIDC/session/token auth (PR 8), time and billing (PR 9), the
UI (PR 11), jurisdiction and practice packs (PR 12), v1 import and docs (PR 13). The
discover.legal intake module is **not** part of v2 (owner decision; it moves to a separate repo).

## Configuration (environment variables only)

`biglaw version --config` prints the resolved values (secrets hidden, the database password
masked) and exits nonzero if any value is invalid. All 19 variables are validated at start; the
column "used by" says which code consumes each one today.

| Variable | Default | Meaning | Used by |
|---|---|---|---|
| `BIGLAW_DATABASE_URL` | — (required) | Postgres DSN, e.g. `postgres://biglaw:…@localhost:5432/biglaw?sslmode=disable`. lib/pq defaults to `sslmode=require`. | store (PR 1) |
| `BIGLAW_ADDR` | `127.0.0.1:3101` | Listen address | serve (PR 1) |
| `BIGLAW_PUBLIC_URL` | `http://127.0.0.1:3101` | External base URL (OIDC redirect, cookie `Secure`) | planned (PR 8) |
| `BIGLAW_MODEL_URL` | — | OpenAI-compatible base URL; checked by the vendor breaker | breaker (PR 1); client planned (PR 2) |
| `BIGLAW_MODEL_KEY` | — | API key for that URL | planned (PR 2) |
| `BIGLAW_MODEL_LEAD` / `_SPECIALIST` / `_CRITIC` | — | Model id per role; unset roles use the lead's | planned (PR 2/4) |
| `BIGLAW_BUDGET_CALLS` | `150` | Model calls per task (1–100000) | planned (PR 4) |
| `BIGLAW_BUDGET_USD` | `5` | Dollar cap per task | planned (PR 4) |
| `BIGLAW_LOCAL_WATTS` | `250` | Power draw for watt-hour metering | planned (PR 2) |
| `BIGLAW_PRICES` | built-in | JSON object or file path of model rates (stored raw; parsed in PR 2) | planned (PR 2) |
| `BIGLAW_COLLAPSE_THRESHOLD` / `_ACTION` | `0.5` / `strict` | Grounding-collapse policy; threshold in (0, 1], action `strict` or `warn` | planned (PR 4) |
| `BIGLAW_OIDC_ISSUER` | — | `google`, `microsoft:<tenant-id>` or an https issuer URL; requires the two below | planned (PR 8) |
| `BIGLAW_OIDC_CLIENT_ID` / `_CLIENT_SECRET` | — | The OIDC client | planned (PR 8) |
| `BIGLAW_AI_RATE` | — | AI timekeeper hourly rate; unset = non-billable | planned (PR 9) |
| `ALLOW_ANTHROPIC` | off | Opt in to a direct `anthropic.com` model endpoint (vendor breaker) | breaker (PR 1) |

Tests use two more: `BIGLAW_TEST_DSN` and `BIGLAW_REQUIRE_DB` (turns a skipped DB test into a
failure). `biglaw serve` warns about any other `BIGLAW_*` variable it sees (likely a typo).

**Vendor breaker.** Ported from v1 `biglaw-go/internal/config/guard.go`: a `BIGLAW_MODEL_URL`
containing `anthropic.com` (any case) refuses to start unless `ALLOW_ANTHROPIC` is `1/true/yes/on`.
A Claude model behind a third-party gateway (OpenRouter, LiteLLM) is allowed. v1's AWS breaker
(`ALLOW_AWS`) is not ported (see Limitations).

## Run it

```bash
cp .env.example .env            # set POSTGRES_PASSWORD (URL-safe, e.g. openssl rand -hex 24)
docker compose up -d            # postgres:16 + biglaw; host port 127.0.0.1:3101 only
curl -s http://127.0.0.1:3101/health
```

Without Docker (Go 1.25+, any Postgres ≥ 13):

```bash
createdb biglaw
make build                      # go build -o biglaw ./cmd/biglaw  (no UI yet)
BIGLAW_DATABASE_URL='postgres:///biglaw?sslmode=disable' ./biglaw serve
```

Migrations run on every start under a Postgres advisory lock, so several processes can start
at once. The server refuses to start if the database records a migration this binary does not
have (a newer build migrated it) or one whose file changed after it was applied.

## Test it

```bash
go test -race ./...             # unit tests; DB tests SKIP without a DSN
BIGLAW_TEST_DSN='postgres://postgres:PASSWORD@localhost:5432/postgres?sslmode=disable' \
  BIGLAW_REQUIRE_DB=1 go test -race ./...   # DB tests too; each gets its own schema, dropped after
make ci                         # gofmt, vet, build, tests, scripts/loc.py, docker compose config
```

Named tests (PLAN.md PR 1): `TestConfigDefaults`, `TestVendorBreakerRefusesDirectAnthropic`,
`TestMigrationFilesOrderedAndUnique`, `TestStoretestSkipsWithoutDSN`,
`TestStoretestFailsWhenRequired`, `TestLocBudget`, and against Postgres `TestMigrateIdempotent`,
`TestConcurrentMigrateSafe`, `TestFailedMigrationRollsBack`, `TestFilesRoundTripBytea`,
`TestSchemaPerTestIsolation`. Extra: config rejection table, secret redaction, DSN-password
hiding, migration drift refusal, file edge cases (empty, oversized, malformed id, tampered
bytes), transaction rollback, `/health`, command dispatch, and a real `serve` end to end.

## Budgets (`scripts/loc.py`, run in CI)

| Budget | Ceiling | Today |
|---|---:|---:|
| Go production lines (physical lines, non-`_test.go`) | 14,000 | 960 |
| Packages under `internal/` | 12 | 2 (`store`, `store/storetest`) |
| Direct Go dependencies | 3 | 1 (`github.com/lib/pq`) |
| Runtime environment variables | 20 | 19 |
| UI runtime dependencies | 2 | 0 (no UI yet) |
| Packages without a `_test.go` | 0 | 0 |

Env-var names are found statically (literal `os.Getenv`/`LookupEnv`/`getenv` arguments, the
config table, and `BIGLAW_*`/`ALLOW_*` literals); a non-literal `os.Getenv` argument fails the check.

## Limitations

- Nothing user-facing exists yet beyond `/health`; every workflow, the REST API, auth and the
  UI are planned. Do not expose a deployment beyond loopback until auth (PR 8) lands: from PR 4
  on, endpoints will exist before auth does.
- `render.yaml` has not been deployed. Render's internal connection string may not accept
  lib/pq's default `sslmode=require`; if the service cannot connect, append the right `sslmode`.
- The Docker image has not been built in this environment (no Docker daemon); `docker compose
  config -q` is the only Compose check run.
- GitHub Actions and container images are pinned to major tags (`@v4`, `golang:1.25-alpine`,
  `postgres:16`), not to SHAs/digests, so an upstream retag changes what CI runs.
- v1's AWS/Bedrock vendor breaker (`ALLOW_AWS`) is not ported: the v2 contract has no
  `ALLOW_AWS` variable. An `amazonaws.com`/Bedrock model URL is not refused (open owner question).
- The breaker matches `anthropic.com` as a substring, so it fails closed: a gateway URL that
  merely contains that text is refused too and needs `ALLOW_ANTHROPIC`.
- Files are capped at 64 MiB each; there is no per-matter quota or upload concurrency limit yet
  (an API concern, planned with the upload endpoint). Stored `content_type` is not validated;
  a later download endpoint must not serve it to browsers unchecked.
- `/health` is unauthenticated and reports the version string.
- `internal/store/storetest` counts as an internal package; with the 11 planned packages the
  12-package budget has no headroom.
- Postgres is required, including on a Pi. There is no embedded-database mode.
