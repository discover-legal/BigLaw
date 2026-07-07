# BigLaw

Multi-agent legal AI orchestration platform — the BigLaw tool stack, open and free for
solos, boutiques, and small firms. Runs DyTopo rounds of granular epistemic/conceptual/writing
agents over an in-process vector agent registry, with a debate + verification protocol on every
finding before final synthesis.

**Big Michael** is the channel agent: lives in Teams and Slack, @-mentionable in any channel,
dispatches tasks to BigLaw's bench, and posts results back. See `biglaw-go/internal/bots/`
for the implementation.

**Version 1.0.0** — the Go platform replaces TypeScript on `main` (TS preserved at the
`typescript-final` tag). 131 agent definitions, Teams/Slack channel bots (Big Michael),
hub-and-spoke client briefing swarm (Chalkboard pattern), SharePoint + Teams search, email
search (Graph + Gmail), playbook-aware contract redlining, headnote generator, precedent
generator, four-tier playbook cascade, lawyer voice fingerprinting, per-call cost tracking,
Q-learning agent recruitment, TopoFlow (AgensFlow bandit over DyTopo), LPM status-report
spine, billing (pre-bills, LEDES, invoice validation, OCG checks), docket/regulatory/budget
monitors, two-wave DyTopo with intra-round whiteboard + inter-round memory rollup, billable
time tracking, and NOSLEGAL v4 taxonomy.

**License: Apache-2.0** — a clean-room reimplementation of the document-production/tabular
tools (spec: `docs/clean-room-spec-document-tools.md`) removed the last copyleft dependency;
LICENSE/NOTICE/SPDX headers all swapped.

**Negotiation intelligence + reviews (latest release):** counter-redline loop
(`respond_to_redline` — parse opposing tracked changes, judge vs the playbook cascade, emit
countered redlines + rationale cards), judge memory across rounds with standoff escalation,
Redtime (`register_document_version`/`get_redline_timeline`, `GET /documents/:id/timeline` —
per-clause negotiation timelines, silent-edit detection, playbook drift), Integrity Check
(`check_document_integrity` — Unicode obfuscation + unmarked changes), persisted tabular
reviews with a citation-verification ladder (exact → tolerant → paraphrase judge → ensemble;
`GET /reviews/:id` + `/reviews/:id/table.csv`, landscape docx export), reviews + Redtime
workbench views, `biglaw demo` (one-command end-to-end tour, ~$0.03), and BELO
(`internal/ontology/` — epistemic ontology, graph-discovered section spine).

## Quick start

**The platform is Go** (`biglaw-go/` — single static binary, runs on a Raspberry Pi 4GB or
fully on local models). The retired TypeScript implementation is preserved at the git tag
`typescript-final`; `src/*.ts` paths in older docs map to `biglaw-go/internal/*`.

**Easiest — one command (needs git + Docker):**

```bash
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

**Already have the repo cloned:**

```bash
bash setup.sh                                        # docker compose stack → :3102
# or natively (Go 1.25+, run from the repo root so templates/ + deadlines/rules/ resolve):
go run ./biglaw-go/cmd/biglaw                        # REST API on :3101
# tests:
cd biglaw-go && go test ./...
```

The Docker stack is three containers: TypeDB → conflict-graph sidecar (Unix-socket IPC) →
BigLaw core. REST API at `http://localhost:3102` (Docker) or `:3101` (native).
MCP server on stdio (activated when stdin is not a TTY — i.e. from Claude Code).

**Web workbench:** `cd ui && BIG_MICHAEL_API=http://localhost:3102 npm run dev` → :5173.

**Run modes** (`BIG_MICHAEL_MODE`): the vector DB takes an exclusive single-writer lock and the
REST API binds one port, so only one process can own them. To run the web UI and the Claude
Code MCP at once, one process owns the DB and the other attaches as a thin client over REST.
- `auto` (default) — own the DB if free, else attach as an MCP client
- `backend` — own DB + REST, never MCP (what the Docker stack runs)
- `mcp` — pure MCP client, requires a reachable backend (`BIG_MICHAEL_API` sets the URL)
- `standalone` — classic single process (own DB + REST + MCP on stdio)

## Using from Claude Code

`.mcp.json` at the project root registers BigLaw as an MCP server.
When Claude Code opens this directory, it can call all tools directly:

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

Claude Code actuates Lavern agent configs (from `agents/lavern/*.json`)
and any JSON plugin adapters (from `adapters/external/*.json`).

### Example Claude Code sessions

```
# US federal antitrust matter
Use BigLaw to research whether our planned acquisition of Acme Inc
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
    32 connector tools        — CourtListener, Westlaw, Everlaw, Trellis,
                                Descrybe, Ironclad, iManage, Definely,
                                DocuSign CLM, Solve Intelligence, Slack,
                                Google Drive, Box, Lawve AI, TopCounsel
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

> The table below predates the Go port: `src/foo.ts` paths describe the architecture and
> map to `biglaw-go/internal/foo/` (e.g. `src/dytopo/engine.ts` → `internal/dytopo/engine.go`,
> `src/mcp/server.ts` → `internal/api/server.go` + `internal/mcp/server.go`). The TS sources
> live at the `typescript-final` tag. Go-only additions: `internal/topoflow/` (AgensFlow
> bandit over DyTopo), `internal/lpm/` (daily status-report spine), `internal/clientvoice/`
> (Remy advocacy briefs), `cmd/biglaw/monitors.go` (firm-wide budget/docket/regulatory monitors).
>
> **Grounding stack (Go-only):** `internal/rag/` (section-chunk hybrid retrieval — dense + doc2query
> + BM25 fused by RRF; `search_chunks`/`get_outline`/`read_section`), `internal/bm25/` (pure-Go Okapi
> BM25), `internal/pageindex/` (legal section-tree parser), `internal/tokenizer/` (pure-Go Qwen BPE),
> and `internal/writer/` (scoped multi-pass synthesis: findings → cluster → tight agentic drafters via
> `search_findings` → hierarchical stitch). Staged extraction lives in `internal/agents/base.go`
> (`runAgenticLoop` retrieves; `extractEvidenceBatch` transcribes verbatim under a substring-lock;
> `analyseEvidence` writes conclusions per locked quote). Synthesis routes through
> `internal/orchestrator/orchestrator.go` `synthesise()` → `writeDeliverable()` when findings exceed
> a single call's budget. These took local-model verbatim citation grounding from ~0% to ~94%.
> Harvey LAB criteria (verified vs scores.json history, post fix-wave July 5 2026): Haiku +
> fix-wave pipeline **49/60** (all-time high; raw Haiku in Harvey's own harness 41; June-26
> pipeline 37; dead-spine release build 34); **local qwen2.5:14b + fix-wave 36/60** (local
> record; prior verified peak 28). The fix wave closed the extraction transcription funnel
> (full-text evidence ingest, context-aware caps, saturation section-walk), added cross-doc
> numeric/date discrepancy joins, defense-lens analytics, and memo-grade authorship. Trust-
> compare task (compare mode): Haiku 6 → 9 → **14/23** (pre-wave → fix-wave → deviation-path
> port e9b95f9), beating the deviation-tuned local qwen record of 12/23. A climb, not a pass.
> (The earlier "30/60" was a two-run union coverage measure; single-run history is as above.)
>
> **Negotiation stack (Go-only):** `internal/tools/negotiate.go` (`respond_to_redline` —
> counter-redlining with playbook judgment, judge memory + standoff escalation, rationale cards),
> `internal/redtime/` + `internal/tools/redtime.go` (document version lineage, per-clause
> timelines, silent-edit detection, playbook drift), `internal/tools/integrity.go`
> (`check_document_integrity` — Unicode obfuscation + unmarked-change detection), and the
> tabular-review citation-verification ladder (exact → tolerant → paraphrase judge → ensemble,
> method + confidence per citation). Reviews persist through the store seam (sqlite/postgres);
> `internal/ontology/` is BELO, the epistemic ontology behind the graph-discovered spine.

| Path | What it does |
|---|---|
| `src/index.ts` | Entry point — loads dotenv → Infisical → starts server |
| `src/config.ts` | All configuration, read from environment |
| `src/orchestrator.ts` | Task lifecycle, phase sequencing, synthesis |
| `src/dytopo/engine.ts` | Need/Offer matching, comm graph, round execution |
| `src/dytopo/jurisdiction.ts` | `jurisdictionMatch()` — agent/task jurisdiction filter |
| `src/agents/definitions.ts` | All 128 agent definitions (58 native + 70 Claude for Legal) |
| `src/agents/registry.ts` | RuVector HNSW agent registry — semantic search, upsert, persist to `./data/agents.rvdb` |
| `src/agents/base.ts` | Agent class — agentic loop, tool dispatch, prompt caching |
| `src/protocols/index.ts` | CitationGate, DebateProtocol, VerificationPipeline |
| `src/routing/model.ts` | Haiku/Sonnet/Opus/Ollama/Local routing; `shouldUseThinking()` |
| `src/providers/anthropic.ts` | Anthropic provider: prompt caching, extended thinking, base URL |
| `src/providers/` | Anthropic + Ollama/LM-Studio provider abstraction |
| `src/tools/index.ts` | All tool implementations + ToolRegistry (extensible via register()) |
| `src/tools/connectors.ts` | 32 legal connector tools across 15 providers |
| `src/tools/pdf.ts` | PyMuPDF/Camelot/Tesseract tools (via python subprocess) |
| `src/tools/docuseal.ts` | DocuSeal e-signature tools |
| `src/adapters/plugin.ts` | Generic `LegalToolPlugin` / `LegalToolAdapter` + PluginRegistry |
| `src/adapters/lavern.ts` | Lavern agent + workflow adapters; external configs |
| `src/adapters/index.ts` | Adapter re-exports (`AgentHarness`, `mergeAgents`, plugin types) |
| `src/audit/index.ts` | Append-only JSONL audit log + SSE stream |
| `src/time/index.ts` | Automatic billable time tracking — open/close entries, 6-min billing units, CSV export |
| `src/secrets/index.ts` | Infisical REST API loader |
| `src/auth/index.ts` | Lawyer profiles (practiceAreas, bio, role), RLS access control |
| `src/clients/index.ts` | Client roster, matter sub-lists, conflict-of-interest checks |
| `src/services/classifier.ts` | Haiku-based practice area, client, and NOSLEGAL tag detection on ingest/submit |
| `src/services/toneAnalyzer.ts` | Chunked recursive Haiku tone analysis (MapReduce, O(log n) depth) |
| `src/linkedin/parser.ts` | RFC 4180 CSV + minimal ZIP parser for LinkedIn data exports |
| `src/cost/index.ts` | CostStore: per-call cost + power tracking, JSONL persistence, cache-aware pricing |
| `src/integrations/clio.ts` | ClioClient — OAuth 2.0, token persistence, auto-refresh, 7 API methods, 4-region routing |
| `src/tools/clio.ts` | 7 Clio tool definitions (list/get matters, documents, activities, notes, contacts) |
| `src/mcp/server.ts` | MCP stdio server + Fastify REST API |
| `src/backend/index.ts` | `LegalBackend` seam — `LocalBackend` (owns DB) / `RemoteBackend` (thin HTTP client) so MCP can run as a client of a separate owner |
| `src/index.ts` | Entry point — also resolves the run mode (`BIG_MICHAEL_MODE`: auto/backend/mcp/standalone) |
| `src/bots/teams.ts` | Big Michael — Teams Outgoing Webhook receiver + Incoming Webhook sender + matter-link management |
| `src/bots/slack.ts` | Big Michael — Slack Events API receiver + Web API sender + matter-link management |
| `src/bots/dispatcher.ts` | Shared @BigMichael command parser (status/briefing/search/task/run/help) |
| `src/integrations/graph.ts` | Microsoft Graph client — SharePoint search, Teams message search, posting |
| `src/email/client.ts` | Email search — Microsoft Graph (O365/Exchange) + Gmail service-account |
| `src/briefing/index.ts` | Hub-and-spoke client briefing swarm (Chalkboard pattern, 10 parallel spokes) |
| `src/playbook/index.ts` | Four-tier playbook cascade: `client (3) > matter (2) > personal (1) > firm (0)` |
| `src/citations/engine.ts` | Citation engine — CourtListener-backed KeyCite/Shepard's replacement |
| `src/redline/engine.ts` | Playbook-aware contract redlining — Definely / Kira replacement |
| `src/headnotes/engine.ts` | Headnote extraction — Westlaw Key Numbers / LexisNexis headnote replacement |
| `src/precedent/generator.ts` | Precedent document generation — Practical Law Standard Docs / PSL replacement |
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

BigLaw ships 32 connector tools across 15 providers:

### Legal Research & Courts
| Provider | Tools | Activation |
|---|---|---|
| CourtListener | `court_listener_search`, `_opinion`, `_docket` | Always on (optional `COURT_LISTENER_API_KEY` for higher rate limits) |
| Westlaw / CoCounsel | `westlaw_research`, `_check_citation` | `WESTLAW_API_KEY` |
| Everlaw | `everlaw_search_documents`, `_get_review_set` | `EVERLAW_API_KEY` |
| Trellis | `trellis_search_cases`, `_get_docket`, `_judge_analytics` | `TRELLIS_API_KEY` |
| Descrybe | `descrybe_search_cases`, `_check_citation` | `DESCRYBE_API_KEY` |
| Solve Intelligence | `solve_intelligence_search_patents`, `_draft_claims` | `SOLVE_INTELLIGENCE_API_KEY` |

### Contract & Document Management
| Provider | Tools | Activation |
|---|---|---|
| Ironclad | `ironclad_search_contracts`, `_get_contract` | `IRONCLAD_API_KEY` |
| DocuSign CLM | `docusign_search_contracts`, `_get_envelope` | `DOCUSIGN_API_KEY` |
| iManage | `imanage_search`, `_get_document` | `IMANAGE_API_KEY` |
| Definely | `definely_analyze_structure`, `_resolve_definition` | `DEFINELY_API_KEY` |
| Lawve AI | `lawve_review_contract`, `_search_clauses` | `LAWVE_API_KEY` |

### VDR & Productivity
| Provider | Tools | Activation |
|---|---|---|
| Google Drive | `google_drive_search`, `_get_file` | `GOOGLE_DRIVE_API_KEY` |
| Box | `box_search`, `_get_file` | `BOX_API_KEY` |
| Slack | `slack_search`, `_send_message` | `SLACK_API_KEY` |

### Outside Counsel
| Provider | Tools | Activation |
|---|---|---|
| TopCounsel | `topcounsel_route_matter`, `_get_panel` | `TOPCOUNSEL_API_KEY` |

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

## Lawyer tone profiles

Every lawyer profile can carry an optional `ToneProfile` derived from their LinkedIn
writing history. Drafting agents and the final Opus synthesis call use the profile to
match the lawyer's voice.

### Getting a LinkedIn export

1. On LinkedIn: **Settings → Data privacy → Get a copy of your data** → select
   "Posts" (and optionally "Messages"). LinkedIn emails a ZIP within 24 h.
2. Upload via the REST endpoint or the Admin → Profiles → Tone tab in the UI:

```bash
curl -X POST /profiles/:id/tone/linkedin-import \
  -F "file=@linkedin_export.zip"   # ZIP or raw CSV both accepted
```

The endpoint accepts multipart uploads of either the full LinkedIn ZIP or a bare
`Shares.csv` / `Posts.csv` file. A 60-second per-profile rate limit prevents
accidental double-submission.

### ToneProfile shape

```typescript
interface ToneProfile {
  generatedAt:      string;          // ISO timestamp
  sourceType:       "linkedin";
  sampleCount:      number;          // posts analysed (max MAX_POSTS=500)
  formality:        string;          // e.g. "formal", "semi-formal"
  sentenceStyle:    string;          // e.g. "concise declarative"
  vocabulary:       string;          // e.g. "technical legal, avoids jargon"
  rhetoricalStyle:  string;          // e.g. "Socratic, evidence-first"
  signaturePatterns: string[];       // recurring phrases / structural habits
  injectionSnippet: string;          // pre-built prompt fragment for injection
}
```

Profile fields added: `toneProfile?: ToneProfile`, `linkedinProfileUrl?: string`.

### How tone injection works

| Site | Behaviour |
|---|---|
| `src/agents/base.ts` | All agents with `domain: "drafting"` receive an `ASSIGNED LAWYER TONE PROFILE` block prepended to their system prompt (uses sanitized `injectionSnippet`) |
| `src/orchestrator.ts` synthesise() | The Opus synthesis call receives `injectionSnippet` injected into its system prompt |
| `src/dytopo/engine.ts` runRound() | Accepts optional `lawyerTone?: ToneProfile`; threads it into every agent `process()` call |

### Analysis pipeline (`src/services/toneAnalyzer.ts`)

Chunked MapReduce with O(log n) Haiku call depth:

| Constant | Value | Purpose |
|---|---|---|
| `POST_CHUNK_SIZE` | 8 | posts per `analyzeChunk` call |
| `NOTE_CHUNK_SIZE` | 6 | notes per `rollupNotes` call |
| `MAX_POSTS` | 500 | cap on posts consumed |

Three Haiku call types: `analyzeChunk` (posts → prose note), `rollupNotes`
(notes → merged note), `buildProfile` (final note → JSON `ToneProfile`).

`sanitizeForHaiku()` strips `FINDING:`, `END_FINDING`, `NO_FINDINGS`,
`NO_CHALLENGE` markers and control characters from user-supplied content before
it enters any prompt. The same guard (`sanitizePromptContent`) is applied in
`src/agents/base.ts` before the `injectionSnippet` is prepended.

### Parser (`src/linkedin/parser.ts`)

Pure Node.js — no new runtime dependencies. RFC 4180 CSV parser handles quoted
fields and embedded newlines. ZIP reader uses Node's built-in `inflateRawSync`;
zip-bomb guard rejects archives whose decompressed output exceeds **50 MB**.
`parseLinkedInExport(buf)` is the single public API; it never throws — malformed
input returns an empty post list.

---

## Cost tracking

`src/cost/index.ts` records a `CostEntry` for every model call and exposes
aggregated summaries via REST.

### CostEntry fields

```typescript
interface CostEntry {
  id:               string;
  timestamp:        string;          // ISO
  model:            string;          // e.g. "claude-haiku-4-5"
  context:          CostContext;
  taskId?:          string;
  profileId?:       string;
  inputTokens:      number;
  outputTokens:     number;
  cacheWriteTokens: number;
  cacheReadTokens:  number;
  costUsd:          number;
  wattHours?:       number;          // local inference only
}
```

Entries persist to `./data/costs.jsonl` (append-only) and reload on restart.

### CostContext values

| Context | Where recorded |
|---|---|
| `task` | Every `callModel()` / `runAgenticLoop()` iteration in `agents/base.ts` |
| `descriptor` | Need/Offer descriptor Haiku calls in `agents/base.ts` |
| `synthesis` | Opus synthesis call in `orchestrator.ts` |
| `tabulate` | Tabulate call in `orchestrator.ts` |
| `round_goal` | Round-goal generation in `orchestrator.ts` |
| `protocol_debate` | DebateProtocol Opus call in `protocols/index.ts` |
| `protocol_verify` | VerificationPipeline Haiku ×10 in `protocols/index.ts` |
| `tone_analysis` | Every Haiku call in `services/toneAnalyzer.ts` (attributed to `profileId`) |
| `classification` | `detectPracticeArea` / `detectClient` / `detectNosLegal` in `services/classifier.ts` |

### Cache-aware pricing

```
cost = (inputTokens        × 1.00 × inputRate)
     + (cacheWriteTokens   × 1.25 × inputRate)
     + (cacheReadTokens    × 0.10 × inputRate)
     + (outputTokens       × outputRate)
```

Built-in rates (per million tokens):

| Model | Input | Output |
|---|---|---|
| Haiku | $1 | $5 |
| Sonnet | $3 | $15 |
| Opus | $15 | $75 |

Override any rate at startup via env vars:

```bash
COST_HAIKU_IN=1.00    COST_HAIKU_OUT=5.00
COST_SONNET_IN=3.00   COST_SONNET_OUT=15.00
COST_OPUS_IN=15.00    COST_OPUS_OUT=75.00
```

### Local inference power metering

`calcWattHours(watts, durationMs)` records estimated energy use for Ollama /
LM-Studio calls. Default power draw:

```bash
LOCAL_INFERENCE_WATTS=250   # default; set to your GPU's TDP
```

`wattHours` is `null` for cloud Anthropic calls.

### Querying costs

```
GET /cost/summary                   # CostSummary totals (partner only)
GET /tasks/:id/cost                 # per-task { taskId, summary, entries }
GET /profiles/:id/cost              # per-profile { profileId, summary, entries }
```

The **Admin → Cost** tab (partner only) shows stat cards, a stacked token
breakdown bar, cost-by-model bar chart, cost-by-context bar chart, and a
per-model detail table.

---

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
`review`, `roundtable`, `tabulate`, `verification`) are mapped to BigLaw's WorkflowType.

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

> This list documents the route contract as designed in the TypeScript implementation;
> the Go API on `main` now covers it (browser OAuth uses static
> `/auth/{google,microsoft,linkedin}/{login,callback}` paths rather than `:provider`
> params). The Go API also adds routes the list below predates: playbooks, citations,
> deadlines, matters (health/budget), dockets, regulatory, pre-bills/invoices/OCG, LPM
> reports, client-voice, and `GET /time-entries/export.ledes`. The authoritative Go route
> map is `biglaw-go/internal/api/` (one file per domain group); the people-facing reference
> is `docs/integration/rest-api.md` and reflects it.

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
POST   /profiles/:id/tone/linkedin-import  upload LinkedIn ZIP or CSV → build ToneProfile  [partner or self, 60s rate limit]
DELETE /profiles/:id/tone           clear tone profile        [partner or self]
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
GET    /cost/summary                aggregate CostSummary across all calls        [partner only]
GET    /tasks/:id/cost              { taskId, summary, entries } (same access as task)
GET    /profiles/:id/cost           { profileId, summary, entries }               [partner or self]
GET    /auth/providers              which OAuth providers are configured
GET    /auth/:provider/login        start OAuth login (google|microsoft|linkedin)
GET    /auth/:provider/callback     OAuth callback → session cookie
POST   /auth/logout                 clear session
GET    /auth/clio/status            Clio connection status { connected, firmName, connectedAt }
GET    /auth/clio/connect           begin Clio OAuth flow                         [partner only]
GET    /auth/clio/callback          Clio OAuth callback → store tokens
DELETE /auth/clio/disconnect        revoke stored Clio tokens                     [partner only]
POST   /tasks/from-clio-matter      import Clio matter → ingest docs → submit task [partner only]
POST   /time-entries/sync-to-clio   push BigLaw time entries to Clio activities [partner only]
GET    /audit                       query audit log (access-filtered)
GET    /audit/stream                SSE live audit stream (access-filtered)
GET    /health                      health check
POST   /bots/teams/webhook          Big Michael Teams Outgoing Webhook receiver
POST   /bots/teams/notify           Internal: post message to a Teams channel
POST   /bots/teams/matter-link      Link a matter to a Teams Incoming Webhook URL
GET    /bots/teams/matter-link/:mn  Get the Teams webhook URL for a matter
DELETE /bots/teams/matter-link/:mn  Remove a matter→channel link
POST   /bots/slack/events           Big Michael Slack Events API receiver
POST   /bots/slack/notify           Internal: post message to a Slack channel
POST   /bots/slack/matter-link      Link a matter to a Slack channel ID
GET    /bots/slack/matter-link/:mn  Get the Slack channel for a matter
DELETE /bots/slack/matter-link/:mn  Remove a matter→channel link
POST   /redline                     Playbook-aware contract redline
POST   /headnotes/generate          Headnote extraction from case opinions
POST   /precedents/generate         Precedent document generation
```

## Big Michael — channel agent

Big Michael is the name of the agent embedded in Teams and Slack. He is a thin command
dispatcher on top of BigLaw's orchestrator — not a separate system.

**Commands** (all prefixed `@BigMichael`):
- `status [matter]` — matter health score + active tasks + top risks
- `briefing [client]` — full hub-and-spoke client intelligence briefing (all systems)
- `search [query]` — semantic search across the knowledge store
- `task [description]` — submit a new roundtable task
- `run [template-id]` — run a named workflow template
- `help` — list available commands

**Environment variables:**
```bash
# Teams
TEAMS_WEBHOOK_SECRET=…           # HMAC-SHA256 signing token from Outgoing Webhook setup
TEAMS_INCOMING_WEBHOOK_URL=…     # default channel to post async results + proactive notifications
TEAMS_MATTER_WEBHOOKS='{"M-001":"https://…","M-002":"https://…"}'  # per-matter overrides

# Slack
SLACK_BOT_TOKEN=…                # Bot User OAuth Token (xoxb-…)
SLACK_SIGNING_SECRET=…           # App signing secret for HMAC verification
SLACK_DEFAULT_CHANNEL=…          # fallback channel ID for notifications
SLACK_MATTER_CHANNELS='{"M-001":"C0123ABCD"}'  # per-matter channel map
```

**Proactive notifications** — when any matter task completes, Big Michael automatically posts
to the linked channel (Teams Incoming Webhook or Slack channel). Wire up at startup with
`attachTeamsTaskNotifier(orch)` and `attachSlackTaskNotifier(orch)` — already done in
`src/mcp/server.ts`.

**Client intelligence briefing** — launched by `@BigMichael briefing [client]`. Spawns
10 parallel spokes (12 s timeout each via `Promise.allSettled`):

| Spoke | Source |
|---|---|
| `clio` | Matter list, contacts, notes from Clio |
| `imanage` | Document search from iManage |
| `slack` | Channel mentions via Slack search API |
| `teams_chat` | Chat message search via Microsoft Graph |
| `sharepoint` | File search via Microsoft Graph (entityTypes: driveItem) |
| `google_drive` | File search via Google Drive API |
| `box` | File search via Box API |
| `email_graph` | Exchange/O365 mail search via Microsoft Graph |
| `email_gmail` | Gmail search via Gmail API (service-account) |
| `knowledge_store` | Semantic search across ingested documents |
| `internal_tasks` | BigLaw task list, matter health scores |
| `internal_time` | Billable time entries per matter |

### Access control

When `AUTH_ENABLED=true`, identity comes from OAuth (Google/Microsoft/LinkedIn)
and every request carries a `SessionUser` from the signed session cookie. A
**partner** sees all matters and manages assignment; a **lawyer** sees only
matters assigned to them. Locally (`AUTH_ENABLED=false`) every request is a
single local partner. See `src/auth/` and `docs/operations/access-control.md`.
Access rules are unit-tested (`npm test`).

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
