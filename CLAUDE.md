# BigLaw 2 — guide for AI assistants

This is the `v2` rewrite: one Go module at the repo root (`github.com/discover-legal/BigLaw`),
PostgreSQL only. `README.md` is the contract for what is on disk; keep it true in the same
change. v1 (Go under `biglaw-go/`, DyTopo) lives on `main`, not here.

## Built so far
- `cmd/biglaw` — `serve [--allow-no-auth]`, `version [--config]`; `config.go` holds the complete
  env-var table (20 vars, each tagged with the PR that first reads it) and the vendor breakers.
- `internal/store` — `Open` (check DSN, connect, migrate), `CheckDSN`, `InTx`, files (chunked
  `bytea`, `PutFile` from an `io.Reader`, `GetFile`), migration runner.
- `internal/store/storetest` — `storetest.DSN(t)` gives a test its own schema.

Everything else in README is marked planned.

## Rules
- Dependencies: `github.com/lib/pq` only (budget 3; planned second is `ledongthuc/pdf`). No ORM,
  no web framework (stdlib `net/http`), no OIDC/JOSE library.
- New env var = add it to `envVars` in `cmd/biglaw/config.go` (with its PR), README's table and
  docker-compose; `TestEnvVarTablesAgree` fails otherwise. Validate a variable in the PR that
  first reads it, not before. Read env only through that table (literal names; `scripts/loc.py`
  rejects dynamic `os.Getenv`).
- Migrations: a new `internal/store/migrations/NNNN_name.sql` (next number, lowercase name).
  Never edit an applied one (checksums are checked). They are forward-only and each runs in a
  transaction: no `COMMIT`/`ROLLBACK`, no `CREATE INDEX CONCURRENTLY`. Extensions are
  database-wide, which schema-per-test isolation cannot handle: an extension must be created once
  outside the numbered migrations (and storetest taught to skip it) before any migration uses one.
- No source file over 2,000 lines (`scripts/loc.py`). Every `.go`/`.py` file starts with the
  SPDX header.
- Every package has a `_test.go`; DB tests use `storetest.DSN(t)` and `t.Parallel()`.
- No model calls in `go test` (later PRs: only through `model.Fake`).
- Errors and logs must never contain the database password or any key. Run user-supplied DSNs
  through `store.CheckDSN` before anything else sees them.
- Until PR 8 there is no auth: only Compose (loopback-published) may pass `--allow-no-auth`
  (`TestDeployFilesFollowExposureRule`).
- One Go version: go.mod's `toolchain` line, the Dockerfile `FROM` and CI's `go-version` move
  together (`TestGoVersionPinnedEverywhere`).

## Gates
```bash
make ci     # gofmt, vet, mod verify/tidy, static build, go test -race -shuffle, loc.py + tests, compose config -q
make lint vuln   # staticcheck, govulncheck (CI lint job)
# DB tests: BIGLAW_TEST_DSN=postgres://…?sslmode=disable BIGLAW_REQUIRE_DB=1
```
Budgets: Go prod LOC ≤ 14,000, internal packages ≤ 12, direct deps ≤ 3, env vars ≤ 20,
UI runtime deps ≤ 2, untested packages 0, longest source file ≤ 2,000 lines.
