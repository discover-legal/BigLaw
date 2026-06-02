# Big Michael

Multi-agent legal AI orchestration platform. Runs DyTopo rounds of granular
epistemic/conceptual/writing agents over a RuVector native HNSW registry, with a
debate + verification protocol on every finding before final synthesis.

**Version 0.3.0** — full RuVector migration: native in-process HNSW for all vector
stores (no Qdrant), Q-learning agent recruitment, two-wave DyTopo processing with
intra-round whiteboard + inter-round memory rollup, billable time tracking, and
NOSLEGAL v4 taxonomy.

## Quick start

```bash
# 1. Start infrastructure
docker compose up -d          # DocuSeal (e-signature) — vector DB is in-process, no service needed

# 2. Configure secrets
cp .env.example .env
# Edit .env — at minimum set ANTHROPIC_API_KEY
# Optional: TAVILY_API_KEY (web search), DOCUSEAL_API_KEY (e-signature)
# Optional: connector API keys (WESTLAW_API_KEY, COURT_LISTENER_API_KEY, etc.)
# Optional: INFISICAL_* vars to load all secrets from Infisical instead

# 3. Install deps
npm install
pip install -r requirements.txt   # PyMuPDF, Camelot, Tesseract

# 4. Verify everything works
npm run smoke-test

# 5. Start server (MCP stdio + REST API)
npm start               # production (requires npm run build first)
npm run dev             # dev mode with tsx watch
```

REST API at `http://localhost:3101`.
MCP server on stdio (activated when stdin is not a TTY — i.e. from Claude Code).

## Using from Claude Code

`.mcp.json` at the project root registers Big Michael as an MCP server.
When Claude Code opens this directory, it can call all tools directly:

```
submit_task          — start a multi-agent legal task (supports jurisdiction= param)
get_task             — poll status + findings
list_tasks           — list all tasks
approve_gate / reject_gate  — human review of flagged findings
submit_from_template — run a pre-built workflow (eu-competition-brief etc.)
list_templates       — see available workflow templates
list_plugins         — list loaded external plugins and their contributed tools/agents
get_round            — inspect a specific DyTopo round
ingest_document      — add a document to the knowledge store
search_knowledge     — semantic search across documents
list_agents          — browse the agent registry
query_memory         — query inter-round memory
get_audit            — retrieve the structured audit log
get_time_entries     — retrieve billable time entries (lawyers see own; partners see all)
```

Claude Code actuates Laverne agent configs (from `agents/laverne/*.json`),
MikeOSS workflow templates (from `workflows/mikeoss/*.json`), and any JSON
plugin adapters (from `adapters/external/*.json`).

### Example Claude Code sessions

```
# US federal antitrust matter
Use big-michael to research whether our planned acquisition of Acme Inc
triggers HSR pre-merger notification. Jurisdiction: US. Run a full_bench workflow.

# UK employment matter
Review the attached settlement agreement under English employment law.
Submit as a review workflow with jurisdiction UK.
```

Claude Code will call `submit_task`, poll `get_task`, approve any human
gates via `approve_gate`, and surface the final synthesis.

## Architecture

```
T0  Root Orchestrator (1)
    ↓ issues RoundGoals each phase
T1  Domain Managers (4)       — research / analysis / drafting / compliance
    ↓ DyTopo: Need/Offer matching → directed comm graph
T2  Epistemic agents (26)     — reason within a specific practice area
T2  Conceptual agents (8)     — own a cross-domain legal concept
T2  Writing agents (13)       — produce a specific document type
    ↓ tool_use agentic loop (allowedTools enforcement)
T3  Tool agents (6)           — web_search, doc retrieval, extraction,
                                translation, citation check, e-signing
    +
    18 connector tools        — CourtListener, Westlaw, Everlaw, Trellis,
                                Descrybe, Ironclad, iManage, Definely
```

Each DyTopo round:
1. Every agent generates a Need/Offer descriptor (Haiku, ~10 tokens)
2. Engine cosine-matches Needs → Offers to build a directed comm graph
3. **Jurisdiction filter**: agents tagged `jurisdictions: ["US"]` are excluded from EU/UK/AU tasks
4. Matched agents receive routed messages from their Need partners
5. Agents run full agentic loops with routed messages + inter-round memory → Findings
6. Findings written to intra-round whiteboard
7. Findings pass through CitationGate → Debate (Opus) → Verification (Haiku ×10)
8. Low-confidence or challenged Findings go to human gate before final output
9. Haiku synthesises whiteboard into round digest → written to inter-round memory store

## Key files

| Path | What it does |
|---|---|
| `src/index.ts` | Entry point — loads dotenv → Infisical → starts server |
| `src/config.ts` | All configuration, read from environment |
| `src/orchestrator.ts` | Task lifecycle, phase sequencing, synthesis |
| `src/dytopo/engine.ts` | Need/Offer matching, comm graph, round execution |
| `src/dytopo/jurisdiction.ts` | `jurisdictionMatch()` — agent/task jurisdiction filter |
| `src/agents/definitions.ts` | All 58 agent definitions |
| `src/agents/registry.ts` | RuVector HNSW agent registry — semantic search, upsert, persist to `./data/agents.rvdb` |
| `src/agents/base.ts` | Agent class — agentic loop, tool dispatch, prompt caching |
| `src/protocols/index.ts` | CitationGate, DebateProtocol, VerificationPipeline |
| `src/routing/model.ts` | Haiku/Sonnet/Opus/Ollama/Local routing; `shouldUseThinking()` |
| `src/providers/anthropic.ts` | Anthropic provider: prompt caching, extended thinking, base URL |
| `src/providers/` | Anthropic + Ollama/LM-Studio provider abstraction |
| `src/tools/index.ts` | All tool implementations + ToolRegistry (extensible via register()) |
| `src/tools/connectors.ts` | 18 legal connector tools across 8 providers |
| `src/tools/pdf.ts` | PyMuPDF/Camelot/Tesseract tools (via python subprocess) |
| `src/tools/docuseal.ts` | DocuSeal e-signature tools |
| `src/adapters/plugin.ts` | Generic `LegalToolPlugin` / `LegalToolAdapter` + PluginRegistry |
| `src/adapters/lavern.ts` | Lavern agent + workflow adapters; MikeOSS + external configs |
| `src/adapters/index.ts` | Adapter re-exports (`AgentHarness`, `mergeAgents`, plugin types) |
| `src/audit/index.ts` | Append-only JSONL audit log + SSE stream |
| `src/time/index.ts` | Automatic billable time tracking — open/close entries, 6-min billing units, CSV export |
| `src/secrets/index.ts` | Infisical REST API loader |
| `src/auth/index.ts` | Lawyer profiles (practiceAreas, bio, role), RLS access control |
| `src/clients/index.ts` | Client roster, matter sub-lists, conflict-of-interest checks |
| `src/services/classifier.ts` | Haiku-based practice area, client, and NOSLEGAL tag detection on ingest/submit |
| `src/mcp/server.ts` | MCP stdio server + Fastify REST API |
| `src/templates/*.json` | Task templates (due-diligence, dispute-resolution, etc.) |
| `src/types.ts` | All types: AgentDefinition (jurisdictions), Task (jurisdiction), NosLegalTags |
| `src/learning/index.ts` | RuVector Q-learning layer — LearningEngine + FastAgentDB for agent recruitment |
| `scripts/pdf_tools.py` | Python PDF backend — called by tools/pdf.ts |
| `docker-compose.yml` | DocuSeal for local dev (no Qdrant — vector DB is in-process) |

## Model routing

| Condition | Model |
|---|---|
| T0 root orchestrator | Opus |
| debate / synthesis / high complexity | Opus |
| synthesis on Opus/Sonnet | Extended thinking (interleaved-thinking-2025-05-14) |
| T1 managers, T2 specialists, drafting | Sonnet |
| T3 tool agents, descriptors, extraction | Haiku |
| `OLLAMA_TIERS=3` + `OLLAMA_ENABLED=true` | T3 → local Ollama |
| `LOCAL_INFERENCE_TIERS=all` | Everything → LM Studio / vLLM / Jan |

**Prompt caching** is enabled on all system prompts (Anthropic cache_control).
**Extended thinking** activates for synthesis, debate, and T0 reasoning calls.
**Base URL override** (enterprise/proxy routing) via `ANTHROPIC_BASE_URL`.

## Connectors

Big Michael ships 18 connector tools across 8 legal data providers:

| Provider | Tools | Activation |
|---|---|---|
| CourtListener | `court_listener_search`, `_opinion`, `_docket` | Always on (optional `COURT_LISTENER_API_KEY` for higher rate limits) |
| Westlaw / CoCounsel | `westlaw_research`, `_check_citation` | `WESTLAW_API_KEY` |
| Everlaw | `everlaw_search_documents`, `_get_review_set` | `EVERLAW_API_KEY` |
| Trellis | `trellis_search_cases`, `_get_docket`, `_judge_analytics` | `TRELLIS_API_KEY` |
| Descrybe | `descrybe_search_cases`, `_check_citation` | `DESCRYBE_API_KEY` |
| Ironclad | `ironclad_search_contracts`, `_get_contract` | `IRONCLAD_API_KEY` |
| iManage | `imanage_search`, `_get_document` | `IMANAGE_API_KEY` |
| Definely | `definely_analyze_structure`, `_resolve_definition` | `DEFINELY_API_KEY` |

All connectors use Streamable HTTP MCP (JSON-RPC 2.0). Unconfigured connectors
return a structured `{ error: "not configured" }` object — they never throw, so
they are always safe to register in agent allowedTools.

Security: endpoint URLs are SSRF-validated at startup; response bodies are capped
at 1 MB; requests timeout at 30 s; API keys never appear in logs or error messages.

## Jurisdiction routing

Specify `jurisdiction` when submitting a task to filter out jurisdiction-specific agents:

```bash
# Via MCP tool
submit_task(description="...", workflowType="roundtable", jurisdiction="UK")

# Via REST API
POST /tasks { "description": "...", "workflowType": "roundtable", "jurisdiction": "US-NY" }
```

Jurisdiction codes use BCP-47-style region tags: `US`, `US-NY`, `EU`, `UK`, `AU`, `SG`, `HK`, `IN`, `CA`, etc.

Filtering rule: an agent tagged `jurisdictions: ["US"]` is excluded from `EU`, `UK`, `AU`, etc. tasks.
Prefix-match: agent `["US"]` is included for task `"US-NY"`, `"US-CA"`. Jurisdiction-neutral agents
(most native agents) are always included regardless.

## Generic plugin adapter

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

See `adapters/external/example.json` for a complete template.

For TypeScript adapters (custom executors), implement `LegalToolAdapter` from
`src/adapters/plugin.ts` and call `pluginRegistry.register(adapter)` at startup.

## NOSLEGAL taxonomy

Both documents and tasks carry NOSLEGAL v4 multi-faceted taxonomy tags.
Tags on tasks are auto-detected from the task description at submission time
(Haiku call in `detectNosLegal()`); tags on documents are set on ingest.

```typescript
task.noslegal = {
  areaOfLaw: "Corporate Finance",      // NOSLEGAL Areas of law facet
  workType:  "Transactional",          // Work types facet
  sector:    "Financial Services",     // Sectors facet
  assetType: "Agreement",              // Information assets facet
};
```

These complement (not replace) the canonical `practiceArea` and `documentType`
fields. Use them for interoperability with NOSLEGAL-compatible legal platforms.
See https://github.com/noslegal/taxonomy for the controlled vocabulary.

Aggregate NOSLEGAL breakdowns across all tasks are available via
`GET /analytics/noslegal` (partner only).

## Adding a new agent

1. Add an `AgentDefinition` object to `src/agents/definitions.ts`
2. Add it to the `ALL_AGENT_DEFINITIONS` export
3. Set `tier` (0–3), `type`, `domain`, `systemPrompt`, `allowedTools`, `skills`
4. Optional: set `jurisdictions: ["US"]` if the agent is US-specific
5. Run `npm run smoke-test` — the `Total agents >= 40` and `No duplicate IDs` checks will catch issues

## Adding a task template

1. Create `src/templates/<id>.json` with:
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
2. TemplateStore auto-loads all `*.json` files from `src/templates/` on startup

## Adding Lavern agents and workflows

- **Agents**: Place Lavern agent config JSON files in `agents/laverne/`. Auto-loaded via `LavernAdapter`.
- **Workflows**: Place Lavern workflow JSON files in `workflows/laverne/`. Auto-loaded via `LavernWorkflowAdapter`.

Lavern workflow types (`adversarial`, `counsel`, `full-bench`, `legal-design`, `pre-engagement`,
`review`, `roundtable`, `tabulate`, `verification`) are mapped to Big Michael's WorkflowType.

## Adding MikeOSS workflows

Place MikeOSS workflow JSON files in `workflows/mikeoss/`. Each file may contain a single
`MikeOSSWorkflow` or an array. They are auto-loaded as TaskTemplates at startup.

## Local inference (LM Studio / Jan / Ollama)

```bash
# LM Studio — all tiers local
LOCAL_INFERENCE_URL=http://localhost:1234/v1
LOCAL_INFERENCE_MODEL=llama-3.2-3b-instruct
LOCAL_INFERENCE_TIERS=all

# Ollama — T3 tool agents only
OLLAMA_ENABLED=true
OLLAMA_MODEL=llama3.2
OLLAMA_TIERS=3
```

## Secrets (Infisical)

Only these vars need to be in `.env`; everything else lives in Infisical:

```bash
INFISICAL_CLIENT_ID=...
INFISICAL_CLIENT_SECRET=...
INFISICAL_PROJECT_ID=...
```

Self-host: `docker compose -f docker-compose.prod.yml up -d` from the Infisical repo.

## REST API endpoints

```
POST   /tasks                       submit task (accepts jurisdiction, clientNumber, matterNumber)
GET    /tasks                       list tasks (access-filtered)
GET    /tasks/:id                   get task (403→404 if not permitted)
DELETE /tasks/:id                   delete a matter
POST   /tasks/:id/assign            assign lawyer(s)        [partner only]
GET    /tasks/:id/stream            SSE live progress
GET    /tasks/:id/table.csv         download tabulate output as CSV
POST   /tasks/from-template         submit from template
GET    /tasks/:taskId/rounds/:round get round state
POST   /tasks/:taskId/gates/:gateId/approve
POST   /tasks/:taskId/gates/:gateId/reject
POST   /documents                   ingest document (text) → returns practiceArea + detectedClient + suggestedLawyers
POST   /documents/upload            upload a PDF / text file → extract + ingest + classify
GET    /documents/search            semantic search (owner-scoped)
GET    /agents                      list agents
GET    /templates                   list templates
GET    /plugins                     list loaded external plugins [partner only]
GET    /settings                    read admin settings
PUT    /settings                    update admin settings (live)
GET    /me                          current principal + authEnabled
GET    /profiles                    lawyer roster (includes practiceAreas, bio)
GET    /profiles/:id                single profile
POST   /profiles                    create lawyer             [partner only]
PATCH  /profiles/:id                update profile            [partner, or profile owner (no role change)]
DELETE /profiles/:id                remove lawyer             [partner only]
GET    /clients                     client roster             [partner only]
POST   /clients                     create client             [partner only]
PATCH  /clients/:id                 update client             [partner only]
DELETE /clients/:id                 delete client             [partner only]
POST   /clients/:id/matters         add matter to client      [partner only]
DELETE /clients/:id/matters/:num    remove matter             [partner only]
POST   /clients/check-conflict      check name against adversary lists  [partner only]
GET    /time-entries                billable time entries (lawyers: own only; partners: all)
                                    query: profileId, taskId, matterNumber, from, to
GET    /time-entries/export.json    full time entry export as JSON  [partner only]
GET    /time-entries/export.csv     full time entry export as CSV   [partner only]
GET    /analytics/noslegal          NOSLEGAL facet breakdown across visible tasks  [partner only]
GET    /auth/providers              which OAuth providers are configured
GET    /auth/:provider/login        start OAuth login (google|microsoft|linkedin)
GET    /auth/:provider/callback     OAuth callback → session cookie
POST   /auth/logout                 clear session
GET    /audit                       query audit log (access-filtered)
GET    /audit/stream                SSE live audit stream (access-filtered)
GET    /health                      health check
```

### Access control

When `AUTH_ENABLED=true`, identity comes from OAuth (Google/Microsoft/LinkedIn)
and every request carries a `SessionUser` from the signed session cookie. A
**partner** sees all matters and manages assignment; a **lawyer** sees only
matters assigned to them. Locally (`AUTH_ENABLED=false`) every request is a
single local partner. See `src/auth/` and the README "Lawyers, roles & access
control" section. Access rules are unit-tested (`npm test`).

### Practice area classification

`src/services/classifier.ts` runs two Haiku calls on every document ingest:

1. **`detectPracticeArea(title, content)`** — classifies into one of 15 canonical practice areas. The canonical list lives in `src/types.ts` as `PRACTICE_AREAS` and is mirrored in `ui/src/types.ts`.
2. **`detectClient(title, content, clients)`** — matches the document against the known client roster by client number and name.

Both results are stored in the Qdrant document payload (`practiceArea`, `detectedClientNumber`) and returned from the REST API alongside `suggestedLawyers` — profiles whose `practiceAreas` include the detected area.

### Conflict of interest

`ClientStore.checkConflict(name)` does a case-insensitive substring match between the incoming client name and every existing client's `adversaries` array. It is called automatically on `POST /clients` and the result is included in the response. Partners can also call `POST /clients/check-conflict` standalone.

## Known limitations

- **Vector storage**: all three stores (agent registry, memory, knowledge) use RuVector's native
  in-process HNSW — no external service required. Data persists to `./data/` and reloads on restart.
- **Python required**: PDF tools require Python 3.11+ and the packages in
  `requirements.txt`. Install with `pip install -r requirements.txt`.
- **Tesseract required** for OCR: `apt install tesseract-ocr` or `brew install tesseract`.
- **Connectors**: all 8 connectors require external subscriptions except CourtListener
  (free, public API). Unconfigured connectors return structured errors — they never crash the server.
