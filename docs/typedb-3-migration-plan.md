# TypeDB 2.x → 3.0 migration plan (conflict-graph sidecar)

Status: **planned, not started.** The transitive-dependency vulnerabilities that motivated
this (`@grpc/grpc-js`, `uuid`) are resolved *for now* by npm `overrides` in
`sidecar/typedb/package.json` (audit clean). This migration is the durable fix — the 3.x
HTTP driver has **zero dependencies**, removing grpc-js/uuid entirely — and modernizes the
sidecar onto a supported TypeDB line.

Do **not** start this without a live TypeDB 3.x server to validate against. The query and
schema layers cannot be verified by typechecking alone, and the conflict logic is a
*redesign*, not a syntax port (see §4).

## 1. Why

- **Security (primary):** `typedb-driver@2.29.7` is the newest 2.x and still pins
  `@grpc/grpc-js@1.9.0` + `uuid@8.3.2` (the 4 Dependabot alerts). `@typedb/driver-http@3.11.5`
  depends on nothing, so the whole transitive surface disappears.
- **Support:** TypeDB 2.x is end-of-life; 3.x is the maintained line.

## 2. Scope

- `sidecar/typedb/typedb.ts` (~413 lines) — the `TypeDBConflictGraph` wrapper. Full rewrite of
  the driver/transaction/query layer.
- `sidecar/typedb/schema.tql` — rewrite in TypeQL 3.0 schema syntax.
- `sidecar/typedb/package.json` — drop `typedb-driver` + the `overrides` block; add
  `@typedb/driver-http@^3.11.5`. Regenerate the lockfile.
- `docker-compose.yml` — the `typedb` service image/healthcheck/volume.
- `server.ts` — keep its HTTP contract to the Go core unchanged.

**Out of scope:** the Go backend (it talks to the sidecar over HTTP and is unaffected if the
sidecar's HTTP contract is preserved).

## 3. Driver API migration (verifiable by typecheck)

Ground truth: `@typedb/driver-http@3.11.5` `dist/index.d.ts`. Key shape:

| 2.x (gRPC) | 3.x (`TypeDBHttpDriver`) |
|---|---|
| `await TypeDB.coreDriver(addr)` | `new TypeDBHttpDriver({ username, password, addresses: ["http://host:port"] })` |
| `driver.databases.contains(db)` / `.create(db)` | `getDatabase(name)` / `createDatabase(name)` → `ApiResponse` |
| `driver.session(db, SessionType.X)` | **removed — no sessions** |
| `session.transaction(TransactionType.Y, opts)` | `openTransaction(db, "read"\|"write"\|"schema", opts)` → `{ transactionId }` |
| `tx.query.get/insert/define(q)` | `query(transactionId, q, opts)` → `ApiResponse<QueryResponse>` |
| `tx.commit()` / `tx.close()` | `commitTransaction(id)` / `closeTransaction(id)` |
| `stream.collect()` → `ConceptMap[]` | `res.ok.answers: ConceptRowAnswer[]`, each `.data: { [v]: Concept }` |
| `cm.get("v").value` | `row.data["v"]` is a `Concept`; an attribute carries `.value` |
| `new TypeDBOptions(); opts.infer = true` | inference is implicit in 3.x; no per-tx infer flag |

- **`oneShotQuery(query, commit, db, txType, ...)`** runs a single query in its own
  transaction — a clean fit for each existing helper (each does one match or one insert).
  Prefer it over manual open/commit/close where possible; fall back to explicit transactions
  only if a helper needs multiple queries atomically.
- Unwrap every call with `isOkResponse(res)` before touching `res.ok`; on `ApiErrorResponse`
  log-and-no-op (preserve the "TypeDB optional, degrade silently" contract).

### Auth & address
3.x requires credentials. Add `TYPEDB_USERNAME` / `TYPEDB_PASSWORD` env (default
`admin`/`password`), and turn the existing `host:port` `TYPEDB_URL` into a full URL
(`http://host:port`) for `addresses`. **Keep `assertSafeTypeDbUrl`** (extend it to accept an
optional `http(s)://` scheme, still rejecting injection chars).

## 4. The hard part — conflict inference (a redesign, not a port)

TypeDB 3.0 **removed rules.** Today the `conflict` relation is *never inserted — always
inferred* by 3 schema rules (`direct-conflict`, `subsidiary-conflict`,
`subsidiary-conflict-inverse`). There is no drop-in equivalent.

**Recommended approach: inline the rule bodies into the read query.** Each rule's `when {}`
clause is already a match pattern; `queryConflicts` can run those patterns directly (as `or`
branches) instead of matching a materialized `conflict` relation. This needs **no rules and
no functions** — the cleanest 3.0 fit. Concretely:
- Drop the `conflict` relation + all 3 rules from the schema.
- Rewrite `queryConflicts` to `match` the union of the three rule bodies (adverse-direct,
  adverse-via-subsidiary, and its inverse), `select`ing the client/matter attributes, with
  the optional `clientId` filter as today.
- Alternative (only if the logic grows): express each pattern as a TypeQL 3.0 **function**
  returning the conflict tuples. More machinery; not needed at current complexity.

This is the step that **must** be validated against a live 3.x server — the union-query
semantics (symmetric `adverse-to`, the `not { $a is $b }` self-exclusion, n-ary role lookups)
need to be confirmed to reproduce the 2.x rule results exactly.

## 5. Schema + query syntax (TypeQL 3.0)

- Schema `define`: 3.0 changed keywords (entities/relations/attributes declared differently;
  `@key`/`@abstract` annotations, `owns`/`plays`/`relates` syntax adjustments). Rewrite
  `schema.tql` and verify each statement against the 3.0 grammar.
- Read queries: `match …; get $a, $b;` → `match …; select $a, $b;`. Add `limit 1` to the
  existence checks.
- Relations: confirm 3.0 insert/match relation syntax (the `links` form vs `( role: $p ) isa`).
- Re-verify attribute insert (`has external-id "x"`) under 3.0.

## 6. Docker

- `docker-compose.yml` `typedb` service: `vaticle/typedb:2.28.0` → the current TypeDB 3.x
  server image (confirm the exact `typedb/typedb:3.x` tag at migration time).
- Re-check the healthcheck command, the exposed port (HTTP endpoint may differ from the gRPC
  1729), and the data volume path (`/opt/typedb-all-linux-x86_64/server/data` may change).
- Update `TYPEDB_URL` guidance in the compose comments + `.env.example`.

## 7. Must preserve

- Security hardening: `slugId` (ReDoS-safe form — do **not** reintroduce `/^-+|-+$/`),
  `SAFE_ID_RE` + `assertSafeId`, `assertSafeTypeDbUrl`. Prefer parameterized values if the
  3.x driver exposes them; otherwise keep the strict validate-before-interpolate guard.
- "TypeDB is entirely optional" — every method a no-op when unconnected; errors log-and-continue.
- `server.ts`'s HTTP contract to the Go core (no changes the Go side must track).

## 8. Sequence & acceptance

1. Stand up a TypeDB 3.x server (Docker) — prerequisite for any validation.
2. `npm rm typedb-driver`, remove `overrides`, `npm i @typedb/driver-http@^3.11.5`; confirm
   `npm audit` clean and grpc-js/uuid absent from the lockfile.
3. Migrate the driver/transaction/result layer in `typedb.ts`; `tsc --noEmit` green.
4. Rewrite `schema.tql`; load it against the live server.
5. Rewrite CRUD queries; validate upsert/relation round-trips against the live server.
6. Rewrite `queryConflicts` (inline rule bodies); **validate it reproduces the 2.x conflict
   results** on a seeded fixture (direct adverse, subsidiary chain, and its inverse).
7. Update Docker; bring the full stack up; end-to-end conflict check via the Go core.

**Done when:** audit clean + grpc-js/uuid gone; `tsc` green; and the live conflict fixtures
produce the same conflicts the 2.x rules did.

## 9. Open questions to resolve at start

- Exact TypeQL 3.0 schema + relation syntax (verify against 3.0 docs, not memory).
- The correct TypeDB 3.x Docker image tag, HTTP port, healthcheck, and volume path.
- Whether `@typedb/driver-http` surfaces parameterized query values (to replace string
  interpolation) or whether the validate-before-interpolate guard remains the safety boundary.
- Default credentials / how auth is configured for the local Docker server.
