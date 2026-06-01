<div align="center">

# Big Michael

### Legal Intelligence Bench

**A multi-agent legal AI orchestrator that convenes a bench of 100+ specialist agents — jurisdiction-neutral by design — has them debate and verify every finding, and returns cited, signature-ready work product.**

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-2563eb.svg)](LICENSE)
[![TypeScript](https://img.shields.io/badge/TypeScript-strict-3178c6.svg)](tsconfig.json)
[![MCP](https://img.shields.io/badge/MCP-stdio%20server-E6B450.svg)](#using-from-claude-code)
[![Vector DB](https://img.shields.io/badge/Qdrant-vector%20registry-dc2626.svg)](docker-compose.yml)

</div>

---

Big Michael isn't a chatbot with a legal prompt. It's an **orchestration engine** that runs
*DyTopo rounds* of granular epistemic, conceptual, and writing agents over a Qdrant vector
registry — and puts a **debate + verification protocol** between every finding and the page.
Low-confidence or challenged findings stop at a **human gate** before they reach final synthesis.

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
| One-size config | **Admin panel** — lawyer/non-lawyer mode, DyTopo depth, verification & DocuSeal, applied live |

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
     │   ↓ tool_use agentic loop
T3  Tool agents (6)                  web search · retrieval · extraction · translation
                                     · citation check · e-signature

50 jurisdiction-neutral native agents — plus an imported Lavern roster (118 in all).
```

**Each DyTopo round:**

1. Every agent emits a Need/Offer descriptor (Haiku, ~10 tokens)
2. The engine cosine-matches Needs → Offers to build a directed comm graph
3. Matched agents receive routed messages from their Need partners
4. Agents process context + run tool_use loops → produce **Findings**
5. Findings pass **CitationGate → Debate (Opus) → Verification (Haiku ×10)**
6. Low-confidence / challenged findings escalate to a **human gate** before synthesis

---

## The bench's tools

Agents act through a typed `ToolRegistry`. Highlights:

| Tool | What it does |
|---|---|
| `search_knowledge` · `read_document` · `fetch_documents` | Semantic + full-text retrieval over the Qdrant registry |
| `find_in_document` | Whitespace-tolerant Ctrl+F with cited context windows |
| `tabular_review` | Multi-doc × multi-column extraction matrix with **RAG flags** + pinpoint `[[page\|quote]]` citations — each cell routed through debate/verification |
| `read_table_cells` | Read any column/row slice of a persisted review |
| `docx_generate` | Build a Word document (headings, prose, tables, landscape, page breaks) |
| `edit_document` | **Tracked-changes redlining** of a `.docx` — minimal `<w:ins>`/`<w:del>` substitutions with Accept/Reject annotations |
| `replicate_document` | Byte-for-byte `.docx` copies to adapt as templates |
| `pdf_extract_text` · `pdf_extract_tables` · `pdf_ocr` · `pdf_generate` | PyMuPDF / Camelot / Tesseract backend |
| `docuseal_send_for_signing` | DocuSeal e-signature dispatch + status |
| `web_search` · `translate` · `citation_check` | Tavily search, translation, source verification |

> Document generation, tabular review, and tracked-change redlining are ported from
> [Mike](https://github.com/willchen96/mike) (AGPL-3.0) and adapted to Big Michael's tool
> registry and provider abstraction. See [`NOTICE`](NOTICE).

---

## Quick start

### 1 · Backend (orchestrator + MCP + REST)

```bash
# Infrastructure: Qdrant (vectors) + DocuSeal (e-sign)
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
POST /tasks                 GET /tasks · /tasks/:id · /tasks/:id/stream (SSE)
POST /tasks/from-template   POST /tasks/:id/gates/:gateId/{approve,reject}
POST /documents             GET /documents/search · /agents · /templates
GET  /audit · /audit/stream (SSE)            GET /health
```

See [`CLAUDE.md`](CLAUDE.md) for the full architecture guide, agent roster, and extension points
(adding agents, templates, and Lavern configs).

---

## Project layout

| Path | Role |
|---|---|
| `src/orchestrator.ts` | Task lifecycle, phase sequencing, synthesis |
| `src/dytopo/engine.ts` | Need/Offer matching, comm graph, round execution |
| `src/agents/` | 50 jurisdiction-neutral agent definitions + the agentic-loop base class |
| `src/protocols/` | CitationGate · DebateProtocol · VerificationPipeline |
| `src/tools/` | Tool registry — PDF, DocuSeal, docx, tabular, document, tracked-changes |
| `src/routing/model.ts` | Haiku / Sonnet / Opus / Ollama / local routing |
| `src/mcp/server.ts` | MCP stdio server + Fastify REST API |
| `ui/` | Vite + React console |
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
