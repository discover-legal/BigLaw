[Docs](../index.md) › Deploy & operate › **Run modes**

# Run modes — browsing **and** the Claude Code MCP at the same time

Exactly one process may own a data dir (`RUVECTOR_DATA_DIR`, default `./data`): its SQLite
file, its JSON state files and its in-process vector stores have a single writer. Ownership
is an OS advisory lock on `<data dir>/biglaw.lock`, taken before anything in the dir is
opened and released automatically when the owner exits (a crash never leaves a stale lock).
Every other process attaches to the owner as a **thin client** over its REST API.
`BIG_MICHAEL_MODE` selects the role:

| Mode | Behaviour | Use |
|---|---|---|
| `auto` *(default)* | Take the lock if free → own the DB + REST (and serve MCP on stdio when stdin is a pipe). If another process holds the lock → become an MCP thin client of `BIG_MICHAEL_API` | Just works — Claude Code's MCP coexists with a native backend on the same data dir |
| `backend` | Own DB + REST, never MCP. Refuses to start if the lock is held | The persistent service (the Docker stack runs this) |
| `mcp` | MCP thin client only — no database, no monitors, no vector store; every tool is a REST call. Fails at startup if the backend is unreachable | Point Claude Code at a backend explicitly (e.g. the Docker stack) |
| `standalone` | Own DB + REST + MCP on stdio. Refuses to start if the lock is held | Single process, on demand |

## The thin client

In `mcp` mode (and `auto` mode when the lock is held) the process builds nothing but an HTTP
client and the MCP server. Tasks it submits, documents it ingests and gates it decides land
in the owner — visible in the workbench, to Big Michael and to every other client — and the
owner's docket/budget/regulatory monitors are the only ones polling.

- **Where:** `BIG_MICHAEL_API` (default `http://<API_HOST>:<API_PORT>`, i.e.
  `http://127.0.0.1:3101`; a wildcard bind host maps to loopback). The Docker stack publishes
  `:3102`, and has its own data volume — so to drive it from Claude Code set
  `BIG_MICHAEL_MODE=mcp` and `BIG_MICHAEL_API=http://localhost:3102`.
- **Startup probe:** `GET /me`. Unreachable, wrong service, or rejected credentials each stop
  the client with an error naming the fix. In `auto` mode the client waits up to 30 s for the
  lock holder's API to come up (the owner may still be booting); in `mcp` mode it fails fast.
- **Auth:** when the backend runs with `AUTH_ENABLED=true`, the client sends
  `Authorization: Bearer $BIG_MICHAEL_API_TOKEN` (falling back to `API_KEY`, so a client
  sharing the backend's `.env` just works). The backend binds that key to `API_PROFILE_ID`,
  and the MCP tools then see exactly what that profile may see through REST.

Every MCP tool maps onto an existing REST route (`POST /tasks`, `GET /tasks/:id`,
`POST /tasks/:id/gates/:gateId/approve`, `GET /documents/search`, `GET /audit`,
`GET /plugins`, `GET /time-entries`, …). The in-process implementation mirrors those routes'
semantics and error texts for the local partner, and a contract test
(`biglaw-go/internal/mcp/contract_test.go`) runs every tool through both and requires
identical results.

## Seam

`biglaw-go/internal/backend` is the seam: `Backend` (one method per MCP tool need),
`Local` (in-process owner services) and `Remote` (REST client), plus the data-dir lock.
`internal/mcp` depends only on `Backend`.

Related: [Getting started](../getting-started.md) · [MCP / Claude Code](../integration/mcp.md) · [Models & persistence](models-and-persistence.md)
