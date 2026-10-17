# BigLaw 2 — guide for AI assistants

This is the `v2` rewrite: one Go module at the repo root (`github.com/discover-legal/BigLaw`),
PostgreSQL only. `README.md` is the contract for what is on disk; keep it true in the same
change. v1 (Go under `biglaw-go/`, DyTopo) lives on `main`, not here.

## Built so far
- `cmd/biglaw` — `serve`, `version [--config]`; `config.go` holds the complete env-var table
  (19 vars) and the vendor breaker.
- `internal/store` — `Open` (connect + migrate), `InTx`, files (`bytea`), migration runner.
  New schema = a new `internal/store/migrations/NNNN_name.sql` (next number, lowercase name).
  Never edit an applied migration: the runner refuses changed checksums.
- `internal/store/storetest` — `storetest.DSN(t)` gives a test its own schema.

Everything else in README is marked planned.

## Rules
- Dependencies: `github.com/lib/pq` only (budget 3; planned second is `ledongthuc/pdf`). No ORM,
  no web framework (stdlib `net/http`), no OIDC/JOSE library.
- New env var = add it to `envVars` in `cmd/biglaw/config.go`, README's table and docker-compose;
  read env only through that table (literal names; `scripts/loc.py` rejects dynamic `os.Getenv`).
- Every package has a `_test.go`; DB tests use `storetest.DSN(t)` and `t.Parallel()`.
- No model calls in `go test` (later PRs: only through `model.Fake`).
- Errors and logs must never contain the database password or any key.

## Gates
```bash
make ci   # gofmt, vet, build, go test -race, scripts/loc.py, docker compose config -q
# DB tests: BIGLAW_TEST_DSN=postgres://…?sslmode=disable BIGLAW_REQUIRE_DB=1
```
Budgets: Go prod LOC ≤ 14,000, internal packages ≤ 12, direct deps ≤ 3, env vars ≤ 20,
UI runtime deps ≤ 2, untested packages 0.
