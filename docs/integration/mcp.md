[Docs](../index.md) › Integrate & extend › **MCP / Claude Code**

# Using BigLaw from Claude Code (MCP)

`.mcp.json` at the repo root registers BigLaw as an MCP server. Opening this directory in
Claude Code exposes the full toolset:

```
submit_task          — start a multi-agent legal task (supports jurisdiction= param)
get_task             — poll status + findings
list_tasks           — list all tasks
approve_gate / reject_gate  — human review of flagged findings
submit_from_template — run a pre-built workflow (eu-competition-brief etc.)
list_templates       — see available workflow templates
get_round            — inspect a specific DyTopo round
ingest_document      — add a document to the knowledge store
search_knowledge     — semantic search across documents
list_agents          — browse the agent registry
query_memory         — query inter-round memory
get_audit            — retrieve the structured audit log
list_plugins         — list loaded external plugins
get_time_entries     — billable time entries (profileId/taskId/matterNumber/from/to filters)
```

## Example session

```
Use BigLaw to review this SaaS master services agreement under New York law —
flag the uncapped indemnity and unlimited-liability exposure, and recommend fallback
positions for the customer. Run a roundtable workflow.
```

Claude Code submits the task, polls progress, approves any human gates, and surfaces the
final synthesis.

## Coexisting with a running backend

`.mcp.json` runs in `auto` mode: if another process already owns the data dir (e.g. a
native process started with `BIG_MICHAEL_MODE=backend`), Claude Code's MCP finds the data
dir's single-writer lock held and attaches to that owner's REST API (`BIG_MICHAEL_API`,
default `http://127.0.0.1:3101`; in practice the URL the owner records in its lock) as a thin client — no database, no monitors, no vector
store of its own. Tasks and documents created over MCP are then the backend's: visible in
the workbench and everywhere else. The Docker stack keeps its data in its own volume, so to
drive it set `BIG_MICHAEL_MODE=mcp` and `BIG_MICHAEL_API=http://localhost:3102` (plus
`BIG_MICHAEL_API_TOKEN` when the backend runs with `AUTH_ENABLED=true`). See
[Run modes](../deployment/run-modes.md).

The MCP server is activated when stdin is not a TTY — i.e. exactly when launched by
Claude Code. The implementation lives in `biglaw-go/internal/mcp/` (tools) over
`biglaw-go/internal/backend/` (the in-process / REST seam).

Related: [REST API](rest-api.md) · [Jurisdiction & NOSLEGAL](jurisdiction-and-noslegal.md)
