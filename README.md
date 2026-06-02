<div align="center">

# Big Michael

### Legal Intelligence Bench

**A multi-agent legal AI orchestrator that convenes a bench of 100+ specialist agents — jurisdiction-neutral by design — has them debate and verify every finding, and returns cited, signature-ready work product.**

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-2563eb.svg)](LICENSE)
[![TypeScript](https://img.shields.io/badge/TypeScript-strict-3178c6.svg)](tsconfig.json)
[![MCP](https://img.shields.io/badge/MCP-stdio%20server-E6B450.svg)](#using-from-claude-code)
[![Vector DB](https://img.shields.io/badge/RuVector-native%20HNSW-7c3aed.svg)](src/agents/registry.ts)

</div>

---

Big Michael isn't a chatbot with a legal prompt. It's an **orchestration engine** that runs
*DyTopo rounds* of granular epistemic, conceptual, and writing agents over a **RuVector
native HNSW registry** — and puts a **debate + verification protocol** between every finding
and the page. Low-confidence or challenged findings stop at a **human gate** before they
reach final synthesis.

It is a deliberate **gestalt**: the agent roster and prompts derive from **Lavern**, the document
capabilities (tabular review, Word generation, tracked-change redlining) are ported from **Mike** —
and neither upstream had the other's half. Big Michael runs Mike's document tools *through* Lavern's
multi-agent debate, so a redline or a due-diligence matrix is produced by a bench that argues with
itself and checks its own citations first.

---

## The console

A real matter, mid-flight — the bench self-organising, then the cited result.

| Round-by-round communication graph | Cited, verified synthesis |
|---|---|
| ![Rounds](collateral/screenshots/03-rounds.png) | ![Synthesis](collateral/screenshots/04-synthesis.png) |

| Live admin · DyTopo depth, modes, DocuSeal | Convene a matter — client/matter numbering |
|---|---|
| ![Admin](collateral/screenshots/06-admin.png) | ![Submit](collateral/screenshots/02-submit.png) |

> Screenshots are captured from the running web console against a live matter
> (client `10482` · matter `10482-014`). The interface, DyTopo communication graph,
> human gates, and per-round agent routing are exactly as the system produced them.

---

## Why it's different

| Most legal AI | Big Michael |
|---|---|
| One model, one pass | 100+ agents across 4 tiers, multiple DyTopo rounds |
| "Trust me" answers | Every finding survives **adversarial debate** + **verification passes** before output |
| Hallucinated cites | **CitationGate** rejects any claim whose source isn't in the registry |
| Locked to one jurisdiction | **Jurisdiction-neutral** native bench — applies the governing law each matter specifies |
| Black box | Append-only **audit log** + live **SSE** of every round, message, and gate |
| Text in, text out | Cited briefs, **.docx** with tracked changes, e-signed via DocuSeal |
| Cloud-only | 3-tier cloud routing **or** fully local (Ollama / LM Studio / vLLM) |
| Static agent pool | **Q-learning recruitment** — agents that produce high-confidence findings are promoted; weak ones deprioritised over time |
| Siloed per-round context | **Intra-round whiteboard** broadcast to all agents + **Haiku-synthesised inter-round rollup** carried forward |
| One-size config | **Admin panel** — lawyer/non-lawyer mode, DyTopo depth, verification & DocuSeal, applied live |
| Generic document store | Documents auto-classified by **practice area** with matching lawyers surfaced on ingest |
| No billing integration | Automatic **6-minute billable time units** tracked per lawyer, per matter, exportable as CSV |

---

## Architecture

```
T0  Root Orchestrator (1)            issues RoundGoals each phase
     │
T1  Domain Managers (4)              research · analysis · drafting · review
     │   ↓ DyTopo: Need/Offer cosine-match → directed communication graph
T2  Epistemic agents (18)            reason within a practice area, in any jurisdiction
                                     (contract · corporate · M&A · privacy · antitrust ·
                                      employment · IP · tax · litigation · sanctions · ESG…)
T2  Conceptual agents (8)            own a cross-system legal concept (materiality,
                                     liability, enforceability, causation, good faith…)
T2  Writing agents (13)              produce a specific document type
     │   ↓ tool_use agentic loop (Wave 1: full loop; Wave 2: Haiku broadcast review)
T3  Tool agents (6)                  web search · retrieval · extraction · translation
                                     · citation check · e-signature

50 jurisdiction-neutral native agents — plus an imported Lavern roster (118 in all).
```

**Each DyTopo round:**

1. Every agent emits a Need/Offer descriptor (Haiku, ~10 tokens)
2. The engine cosine-matches Needs → Offers to build a sparse directed comm graph
3. Messages routed along graph edges to each agent
4. Agents run full agentic loops with routed messages + inter-round memory → findings
5. Findings written to the **intra-round whiteboard**
6. Findings pass **CitationGate → Debate (Opus) → Verification (Haiku ×10)**
7. Haiku synthesises the whiteboard into a round digest → written to **inter-round memory** for the next round
7. Low-confidence / challenged findings escalate to a **human gate** before synthesis

**Q-learning agent recruitment** (`src/learning/index.ts`):

- RuVector `LearningEngine` maintains a Q-table across `"phase:jurisdiction:workflowType"` states
- High-confidence uncontested findings → reward; challenged findings → penalised ×0.3
- `FastAgentDB` stores episodes for similarity-based retrieval across past tasks
- Q-table persisted to `.qtable.json` and reloaded on restart

**Vector storage** — three RuVector native HNSW stores, all in-process, no service required:

| Store | Path | Used for |
|---|---|---|
| Agent registry | `./data/agents.rvdb` | Semantic agent recruitment + outcome tracking |
| Inter-round memory | `./data/memory.rvdb` | Cross-round context retrieval |
| Knowledge base | `./data/knowledge.rvdb` | Document chunks + semantic search |

---

## The bench's tools

Agents act through a typed `ToolRegistry`. Highlights:

| Tool | What it does |
|---|---|
| `search_knowledge` · `read_document` · `fetch_documents` | Semantic + full-text retrieval over the RuVector knowledge base |
| `find_in_document` | Whitespace-tolerant Ctrl+F with cited context windows |
| `tabular_review` | Multi-doc × multi-column extraction matrix with **RAG flags** + pinpoint `[[page\|quote]]` citations — each cell routed through debate/verification |
| `read_table_cells` | Read any column/row slice of a persisted review |
| `docx_generate` | Build a Word document (headings, prose, tables, landscape, page breaks) |
| `edit_document` | **Tracked-changes redlining** of a `.docx` — minimal `<w:ins>`/`<w:del>` substitutions with Accept/Reject annotations |
| `replicate_document` | Byte-for-byte `.docx` copies to adapt as templates |
| `pdf_extract_text` · `pdf_extract_tables` · `pdf_ocr` · `pdf_generate` | PyMuPDF / Camelot / Tesseract backend |
| `docuseal_send_for_signing` | DocuSeal e-signature dispatch + status |
| `web_search` · `translate` · `citation_check` | Tavily search, translation, source verification |
| 18 connector tools | CourtListener · Westlaw · Everlaw · Trellis · Descrybe · Ironclad · iManage · Definely |

> Document generation, tabular review, and tracked-change redlining are ported from
> [Mike](https://github.com/willchen96/mike) (AGPL-3.0) and adapted to Big Michael's tool
> registry and provider abstraction. See [`NOTICE`](NOTICE).

---

## Quick start

### 1 · Backend (orchestrator + MCP + REST)

```bash
# Infrastructure: DocuSeal only — vector DB is in-process (no Qdrant needed)
docker compose up -d

# Secrets — at minimum ANTHROPIC_API_KEY
cp .env.example .env

# Dependencies
npm install
pip install -r requirements.txt        # PyMuPDF, Camelot, Tesseract

# Verify everything wires up (config, tools, agents, templates, routing, PDF round-trip)
npm run smoke-test
npm test                                # fast unit tests (routing, adapters, path-safety)

npm run dev                             # tsx watch  →  REST API on :3101
```

### 2 · Web console (Vite + React)

```bash
cd ui
npm install
npm run dev                             # console on :5173, proxies the API on :3101
```

Open **http://localhost:5173** — convene a matter, watch rounds stream live, approve gates,
and pull cited findings, tracked-change `.docx`, and tabular-review CSVs.

---

## Legal data connectors

Big Michael ships 18 connector tools across 8 providers, all using Streamable HTTP MCP (JSON-RPC 2.0).
Unconfigured connectors return a structured `{ error: "not configured" }` — they never crash the server.

| Provider | Tools | Activation |
|---|---|---|
| CourtListener | `court_listener_search`, `_opinion`, `_docket` | Always on (optional key for higher rate limits) |
| Westlaw / CoCounsel | `westlaw_research`, `_check_citation` | `WESTLAW_API_KEY` |
| Everlaw | `everlaw_search_documents`, `_get_review_set` | `EVERLAW_API_KEY` |
| Trellis | `trellis_search_cases`, `_get_docket`, `_judge_analytics` | `TRELLIS_API_KEY` |
| Descrybe | `descrybe_search_cases`, `_check_citation` | `DESCRYBE_API_KEY` |
| Ironclad | `ironclad_search_contracts`, `_get_contract` | `IRONCLAD_API_KEY` |
| iManage | `imanage_search`, `_get_document` | `IMANAGE_API_KEY` |
| Definely | `definely_analyze_structure`, `_resolve_definition` | `DEFINELY_API_KEY` |

---

## Using from Claude Code

`.mcp.json` registers Big Michael as an MCP server. Opening this directory in Claude Code exposes
the full toolset (`submit_task`, `get_task`, `approve_gate`, `submit_from_template`,
`ingest_document`, `search_knowledge`, `get_audit`, …):

```
Use big-michael to review this SaaS master services agreement under New York law —
flag the uncapped indemnity and unlimited-liability exposure, and recommend fallback
positions for the customer. Run a roundtable workflow.
```

Claude Code submits the task, polls progress, approves any human gates, and surfaces the
final synthesis.

---

## Model routing

Three cost/latency tiers, chosen per agent tier + task type — or routed entirely to local inference.

| Condition | Model |
|---|---|
| T0 root orchestrator · debate · synthesis · high complexity | **Opus** |
| T1 managers · T2 specialists · drafting · verification | **Sonnet** |
| T3 tool agents · descriptors · extraction · translation | **Haiku** |
| `OLLAMA_ENABLED=true` + `OLLAMA_TIERS=3` | T3 → local Ollama |
| `LOCAL_INFERENCE_TIERS=all` | Everything → LM Studio / vLLM / Jan |

Correctness-critical paths (debate, synthesis, T0) stay on cloud unless **all** tiers are
explicitly routed local.

---

## REST API

```
POST   /tasks                 GET /tasks · /tasks/:id · /tasks/:id/stream (SSE)
DELETE /tasks/:id             POST /tasks/:id/assign         (partner only)
POST   /tasks/from-template   POST /tasks/:id/gates/:gateId/{approve,reject}
POST   /documents             POST /documents/upload (PDF/text) · GET /documents/search
GET    /agents · /templates · /settings   PUT /settings      (admin)
GET    /plugins                                               (partner only)
GET    /me · /profiles        POST /profiles                 (partner only)
                              PATCH /profiles/:id            (partner or profile owner)
                              DELETE /profiles/:id           (partner only)
GET    /clients               POST /clients · PATCH/DELETE /clients/:id   (partner only)
POST   /clients/:id/matters   DELETE /clients/:id/matters/:matterNumber   (partner only)
POST   /clients/check-conflict                                             (partner only)
GET    /time-entries          GET /time-entries/export.{json,csv}          (partner: all; lawyer: own)
GET    /analytics/noslegal                                                 (partner only)
GET    /auth/providers        GET /auth/:provider/{login,callback} · POST /auth/logout
GET    /audit · /audit/stream (SSE)        GET /health
```

Document ingestion (`POST /documents`, `POST /documents/upload`) returns enriched metadata:
```json
{ "id": "…", "practiceArea": "Corporate & M&A", "detectedClient": { "clientNumber": "C-001", "clientName": "Acme Corp" }, "suggestedLawyers": [{ "id": "…", "name": "Jane Smith" }] }
```

Every matter-scoped route enforces access control — see below.

See [`CLAUDE.md`](CLAUDE.md) for the full architecture guide, agent roster, and extension points
(adding agents, templates, and Lavern configs).

---

## Billable time tracking

Every task automatically accumulates billable time. Entries open when a task starts and close
when it completes or is deleted; duration is rounded up to the nearest **6-minute unit**
(the standard legal billing increment). Partners see all time entries; lawyers see only their own.

```
GET  /time-entries                query: profileId, taskId, matterNumber, from, to
GET  /time-entries/export.json    full export (partner only)
GET  /time-entries/export.csv     CSV for billing import (partner only)
```

---

## NOSLEGAL taxonomy

Tasks carry **NOSLEGAL v4** multi-faceted taxonomy tags, auto-detected by Haiku at submission:

```json
{ "areaOfLaw": "Corporate Finance", "workType": "Transactional", "sector": "Financial Services", "assetType": "Agreement" }
```

Aggregate breakdowns across all tasks are available at `GET /analytics/noslegal` (partner only).

---

## Lawyers, roles & access control

Big Michael is multi-user when deployed. Identity comes from **OAuth** (Google,
Microsoft, or LinkedIn); each person is a **lawyer profile** with a role:

- **partner** (admin) — sees every matter, manages the lawyer roster, assigns
  matters to lawyers, and manages clients.
- **lawyer** — sees **only** the matters they're assigned to. There is no
  inter-lawyer visibility unless a partner shares a case.

This is enforced at every matter-scoped endpoint (list, detail, SSE stream, gates,
CSV, rounds, audit) and documents are scoped to their uploader. The `partner`/
`lawyer` rules are covered by unit tests (`npm test`).

### Lawyer profiles

Each profile stores:
- **Name, email, title, role** — managed by partners in the Admin › Users tab
- **Practice areas** — one or more of the 15 canonical areas (Corporate & M&A, Competition & Antitrust, Employment & Labour, IP, Real Estate, Banking & Finance, Litigation, Tax, Regulatory & Compliance, Data Privacy, Immigration, Insolvency, Capital Markets, Insurance, Environmental & Climate)
- **Bio** — short free-text description

Lawyers can edit their own name, title, bio, and practice areas. Partners control role and can edit any profile.

### Clients & matters

Partners maintain a client roster (`GET/POST/PATCH/DELETE /clients`). Each client has:
- **Client number** — unique firm reference (e.g. `C-001`)
- **Matters** — sub-list of matter numbers with descriptions and practice areas
- **Adverse parties** — names of opposing parties; used for automatic conflict-of-interest checks

When a new client is added the system immediately checks their name against all existing clients' adverse-party lists and surfaces a conflict alert if there is a match. Clicking a client number in the matters sidebar filters the list to that client's matters.

### Practice area auto-detection

Every document ingested through the Library (pasted or uploaded) is automatically classified into one of the 15 practice areas by a lightweight Claude Haiku call. The system also tries to identify which existing client the document relates to. Both the detected area and any matching client are returned in the API response alongside a list of suggested lawyers whose practice areas align.

**Local dev runs with auth OFF** — a single "local partner" who sees everything,
so you don't need OAuth to develop. Turn it on for shared deployments:

```bash
AUTH_ENABLED=true
SESSION_SECRET=<random 32+ char secret>
PUBLIC_BASE_URL=https://api.your-host        # this API (OAuth redirect base)
PUBLIC_UI_URL=https://app.your-host          # where to land after login
CORS_ORIGINS=https://app.your-host           # allow-listed browser origin(s)
ADMIN_EMAILS=you@firm.com                    # emails provisioned as partner

# Register an OAuth app with each provider you want; redirect URI is
#   <PUBLIC_BASE_URL>/auth/<provider>/callback   (provider ∈ google|microsoft|linkedin)
GOOGLE_CLIENT_ID=…       GOOGLE_CLIENT_SECRET=…
MICROSOFT_CLIENT_ID=…    MICROSOFT_CLIENT_SECRET=…
LINKEDIN_CLIENT_ID=…     LINKEDIN_CLIENT_SECRET=…
```

Profiles are auto-provisioned on first login (partner if the email is in
`ADMIN_EMAILS`, otherwise lawyer).

📖 Full step-by-step provider registration: [`docs/AUTH_SETUP.md`](docs/AUTH_SETUP.md).

---

## Project layout

| Path | Role |
|---|---|
| `src/orchestrator.ts` | Task lifecycle, phase sequencing, synthesis |
| `src/dytopo/engine.ts` | Need/Offer matching, comm graph, two-wave round execution |
| `src/agents/` | 50 jurisdiction-neutral agent definitions + the agentic-loop base class |
| `src/agents/registry.ts` | RuVector HNSW agent registry — persists to `./data/agents.rvdb` |
| `src/learning/index.ts` | RuVector Q-learning recruitment — `LearningEngine` + `FastAgentDB` |
| `src/memory/index.ts` | Intra-round whiteboard + inter-round RuVector memory store |
| `src/knowledge/index.ts` | Document knowledge base — chunk ingestion + semantic search |
| `src/protocols/` | CitationGate · DebateProtocol · VerificationPipeline |
| `src/tools/` | Tool registry — PDF, DocuSeal, docx, tabular, document, tracked-changes |
| `src/tools/connectors.ts` | 18 legal connector tools across 8 providers |
| `src/routing/model.ts` | Haiku / Sonnet / Opus / Ollama / local routing |
| `src/auth/` | Lawyer profiles, roles, RLS access control + OAuth login |
| `src/clients/` | Client roster, matter sub-lists, conflict-of-interest checks |
| `src/time/index.ts` | Billable time tracking — 6-min units, open/close lifecycle, CSV export |
| `src/services/classifier.ts` | Haiku-based practice area + client + NOSLEGAL detection |
| `src/settings/` | Live admin settings (DyTopo depth, debate, DocuSeal, modes) |
| `src/mcp/server.ts` | MCP stdio server + Fastify REST API |
| `src/adapters/` | Plugin adapter — drop JSON in `adapters/external/` for instant integration |
| `src/secrets/index.ts` | Infisical secrets manager (bootstrap from `.env`, rest from vault) |
| `ui/` | Vite + React console |
| `tests/` | Unit tests (`npm test`) — routing, adapters, access control, path safety |
| `workflows/mikeoss/` · `src/templates/` | Workflow presets (CP checklist, credit/SHA summary, …) |

---

## License & attribution

Big Michael is distributed under the **GNU Affero General Public License v3.0** ([`LICENSE`](LICENSE)).
Because it bundles an AGPL-3.0 component, AGPL §13 applies: running a modified version as a network
service obliges you to offer the complete corresponding source to its users.

It builds on two upstreams, fully attributed in [`NOTICE`](NOTICE):

- **Lavern** ("The Shem") — agent definitions & prompts (Apache-2.0)
- **Mike** ([mikeoss.com](https://github.com/willchen96/mike)) — document generation, tabular review, tracked-change redlining (AGPL-3.0)

*"Lavern", "The Shem", and "Mike" are the marks of their respective authors, used here only for attribution.*

<div align="center"><sub>Copyright © 2026 Discover Legal</sub></div>
