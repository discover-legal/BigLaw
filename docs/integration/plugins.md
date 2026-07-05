[Docs](../index.md) › Integrate & extend › **Plugins & adapters**

# Generic plugin adapter

Any external legal tool can be integrated without code changes. Drop a JSON file
in `adapters/external/` and it will be loaded at startup:

```json
{
  "id": "my-legal-tool",
  "name": "My Legal Tool",
  "version": "1.0.0",
  "description": "Brief description",
  "auth": {
    "type": "api-key",
    "apiKeyEnvVar": "MY_TOOL_API_KEY",
    "endpointEnvVar": "MY_TOOL_MCP_URL"
  },
  "tools": [
    {
      "name": "my_tool_search",
      "description": "Search for legal documents",
      "inputSchema": {
        "type": "object",
        "properties": { "query": { "type": "string" } },
        "required": ["query"]
      },
      "remoteName": "search",
      "requiresAuth": true
    }
  ],
  "agents": [
    {
      "id": "my-tool-specialist",
      "name": "My Tool Specialist",
      "tier": 2,
      "domain": "research",
      "description": "Specialist using My Legal Tool",
      "systemPrompt": "You are the My Tool Specialist...",
      "allowedTools": ["my_tool_search"]
    }
  ],
  "workflows": [
    {
      "id": "my-workflow",
      "name": "My Research Workflow",
      "description": "End-to-end research using My Legal Tool",
      "workflowType": "roundtable",
      "promptTemplate": "Research {{description}} using My Legal Tool."
    }
  ]
}
```

See `adapters/external/example.json` for a complete template. The adapter implementation is
`biglaw-go/internal/adapters/` — custom executors (beyond the generic JSON adapter) are added
there in Go.

Loaded plugins are listed at `GET /plugins` (partner only) and via the `list_plugins` MCP tool.

## Lavern agents, workflows & MikeOSS templates

- **Lavern agents**: place agent config JSON files in `agents/lavern/` — auto-loaded at startup.
- **Lavern workflows**: place workflow JSON files in `workflows/lavern/` — Lavern workflow types
  (`adversarial`, `counsel`, `full-bench`, `legal-design`, `pre-engagement`, `review`,
  `roundtable`, `tabulate`, `verification`) map onto BigLaw's WorkflowType.
- **MikeOSS workflows**: place workflow JSON files in `workflows/mikeoss/` — auto-loaded as
  task templates.

## Task templates

Drop a JSON file in `templates/`:

```json
{
  "id": "my-template",
  "name": "Human-readable name",
  "description": "What this workflow does",
  "workflowType": "roundtable",
  "promptTemplate": "Analyse {{company}} for {{issue}} under EU law.",
  "substitutions": { "company": "...", "issue": "..." }
}
```

The template store auto-loads all `*.json` files at startup; run one via
`POST /tasks/from-template` or the `submit_from_template` MCP tool.

Related: [Connectors](connectors.md) · [MCP / Claude Code](mcp.md)
