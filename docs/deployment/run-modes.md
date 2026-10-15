[Docs](../index.md) › Deploy & operate › **Run modes**

# Run modes — browsing **and** the Claude Code MCP at the same time

Exactly one process may own a data dir (`RUVECTOR_DATA_DIR`, default `./data`): every piece
of BigLaw state — the SQLite database and its journal, the JSON state files, attachments, cost
and audit ledgers, the in-process vector stores — defaults into it and has a single writer.
Ownership is an OS advisory lock on `<data dir>/biglaw.lock`, taken before anything in the dir
is opened and released automatically when the owner exits (a crash never leaves a stale lock).
The owner records its PID and REST URL in the lock file. Every other process attaches to the
owner as a **thin client** over its REST API. `BIG_MICHAEL_MODE` selects the role:

| Mode | Behaviour | Use |
|---|---|---|
| `auto` *(default)* | Take the lock if free → own the DB + REST (and serve MCP on stdio when stdin is a pipe). If another process holds the lock → become an MCP thin client of **the URL the owner advertises in the lock** (`BIG_MICHAEL_API` is only the fallback; a different explicit value is warned about) | Just works — Claude Code's MCP coexists with a native backend on the same data dir |
| `backend` | Own DB + REST, never MCP. Refuses to start if the lock is held | The persistent service (the Docker stack runs this) |
| `mcp` | MCP thin client only — no database, no monitors, no vector store; every tool is a REST call to `BIG_MICHAEL_API` | Point Claude Code at a backend explicitly (e.g. the Docker stack) |
| `standalone` | Own DB + REST + MCP on stdio. Refuses to start if the lock is held | Single process, on demand |

**Upgrading:** builds before this change took no lock. Stop every old BigLaw process before
starting a new one on the same data dir — the new lock cannot see an old owner.

**Filesystems without advisory locks** (some network mounts return `ENOLCK`/`ENOTSUP`): BigLaw
logs a warning and runs unguarded. `BIGLAW_LOCK=off` skips the lock deliberately. Either way,
run at most one owner per data dir yourself.

## The thin client

In `mcp` mode (and `auto` mode when the lock is held) the process builds nothing but an HTTP
client and the MCP server. Tasks it submits, documents it ingests and gates it decides land
in the owner — visible in the workbench, to Big Michael and to every other client — and the
owner's docket/budget/regulatory monitors are the only ones polling.

- **Where:** `BIG_MICHAEL_API` (default `http://<API_HOST>:<API_PORT>`, i.e.
  `http://127.0.0.1:3101`; a wildcard bind host maps to loopback). The Docker stack publishes
  `:3102`, and has its own data volume — so to drive it from Claude Code set
  `BIG_MICHAEL_MODE=mcp` and `BIG_MICHAEL_API=http://localhost:3102`.
- **Attach:** the MCP server answers `initialize` immediately; the backend is probed
  (`GET /me`) in the background and again by the first tool call. Unreachable, wrong service,
  redirected, or rejected credentials are logged to stderr and returned by every tool as an
  actionable error until the backend answers. In `auto` mode an unreachable owner is retried for
  20 s (it may still be booting).
- **Auth:** when the backend runs with `AUTH_ENABLED=true`, the client sends
  `Authorization: Bearer $BIG_MICHAEL_API_TOKEN` (falling back to `API_KEY`, so a client
  sharing the backend's `.env` just works). The backend binds that key to `API_PROFILE_ID`,
  and the tools then see exactly what that profile may see.
- **Transport safety:** redirects are never followed (a 3xx would carry the bearer elsewhere —
  it is an error naming the target); plain `http` to a non-loopback host logs a warning; any
  password in the URL is redacted from logs and errors.

## One implementation

`biglaw-go/internal/backend` holds the operations the MCP tools expose — tasks, gates,
templates, rounds, ingest, search, agents, audit, plugins, time entries — **and their access
rules**: `backend.Local.As(principal)` is the only implementation. The REST handlers for those
routes are thin transports over it (parse the request, call `As(sessionUser)`, map the error
kind to a status in one place), the owner's in-process MCP server calls it directly, and
`backend.Remote` calls those REST routes. A partner sees everything; anyone else sees only the
tasks they created or are assigned to, their own documents, time entries and audit actions,
and no partner-only listings — identically on every path.

The owner's in-process MCP server acts as the local partner, or — with `AUTH_ENABLED=true` — as
the `API_PROFILE_ID` profile (it refuses to start if that is unset or unknown).

A transport contract test (`biglaw-go/internal/mcp/contract_test.go`) runs every MCP tool
in-process and over REST, for a partner and for a lawyer, and requires identical results and
error texts.

## Seam

`Backend` (one method per MCP tool need), `Local` + `As`, `Remote`, and the data-dir lock all
live in `biglaw-go/internal/backend`; `internal/mcp` depends only on `Backend`.

Related: [Getting started](../getting-started.md) · [MCP / Claude Code](../integration/mcp.md) · [Models & persistence](models-and-persistence.md)
