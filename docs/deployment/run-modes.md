[Docs](../index.md) › Deploy & operate › **Run modes**

# Run modes — browsing **and** the Claude Code MCP at the same time

The vector DB under `./data` takes an exclusive single-writer lock and the REST API binds
one port, so only **one** process can own them. To run the web workbench and the Claude Code
MCP together, one process owns the DB and the other attaches as a thin client over the REST
API. `BIG_MICHAEL_MODE` selects the role:

| Mode | Behaviour | Use |
|---|---|---|
| `auto` *(default)* | Own the DB if the port is free; otherwise attach as an MCP client | Just works — the MCP coexists with a running workbench |
| `backend` | Own DB + REST, never start MCP | The persistent service (the Docker stack runs this) |
| `mcp` | Pure MCP client — errors if no backend is reachable | Force Claude Code's MCP to be a client |
| `standalone` | Classic single process: own DB + REST + MCP on stdio | The original behaviour, on demand |

With a backend running, the workbench and Claude Code's MCP both connect to it — Claude
Code's `.mcp.json` runs `go run ./biglaw-go/cmd/biglaw` in `auto` mode, so it detects the
owner and attaches as a client automatically. Set `BIG_MICHAEL_API` to point a client at a
non-default owner URL.

Related: [Getting started](../getting-started.md) · [MCP / Claude Code](../integration/mcp.md)
