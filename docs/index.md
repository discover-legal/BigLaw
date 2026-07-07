# BigLaw documentation

One page per topic, grouped by what you came here to do. The [README](../README.md) is the
landing page; everything of substance lives here.

## Get started

| Page | What it covers |
|---|---|
| [Getting started](getting-started.md) | One-command install, Docker vs native, the `biglaw demo` tour, the web workbench, your first task |
| [Why BigLaw](why-biglaw.md) | The cost chart — what the incumbent stack costs per lawyer per year, and the feature-by-feature comparison |
| [Legal notices & disclaimers](legal-notices.md) | No legal advice, privilege, UPL, confidentiality, deployment liability. Read these — they are not boilerplate |

## Deploy & operate

| Page | What it covers |
|---|---|
| [Security](security.md) | The experimental-status notice and the security hardening inventory |
| [Run modes](deployment/run-modes.md) | `BIG_MICHAEL_MODE` — running the workbench and the Claude Code MCP side by side |
| [Models, persistence & documents](deployment/models-and-persistence.md) | Model stack selection, SQLite/Postgres + RLS, omnimodal ingestion, the blob store |
| [Local inference](deployment/local-inference.md) | Ollama / LM Studio / vLLM — air-gapped and per-tier local routing |
| [Secrets](deployment/secrets.md) | Infisical bootstrap — keep everything but three vars out of `.env` |
| [Access control](operations/access-control.md) | Partner/lawyer roles, UX modes, OAuth + bearer-key auth ([provider setup](AUTH_SETUP.md)) |
| [Audit trail](operations/audit-trail.md) | Hash-chained JSONL, lawyer-attributed tool calls, SIEM forwarding |
| [Cost tracking](operations/cost-tracking.md) | Per-call cost records, cache-aware pricing, local power estimates |
| [Tone profiles](operations/tone-profiles.md) | Lawyer voice fingerprinting from LinkedIn/DOCX/PDF writing samples |

## Features

| Page | What it covers |
|---|---|
| [Negotiation stack](features/negotiation.md) | Counter-redline loop, Redtime per-clause timelines, Integrity Check |
| [Tabular review](features/tabular-review.md) | Multi-doc × multi-column extraction with the citation-verification ladder |
| [Document production](features/document-production.md) | `.docx` generation, tracked-changes redlining, PDF tools, DocuSeal e-signing |
| [Playbooks & redlining](features/playbooks.md) | The four-tier playbook cascade and the playbook-aware redline engine |
| [Big Michael](features/big-michael.md) | The Teams/Slack channel agent — commands, setup, the briefing swarm |
| [Court deadlines](features/deadlines.md) | The deadline calculator — FRCP, UK CPR, EU Competition, cited |
| [Research engines](features/research-engines.md) | Citation checking, headnote extraction, precedent generation |
| [Billing, LPM & monitors](features/billing-and-lpm.md) | 6-minute billing units, LEDES, pre-bills, OCG checks, status reports, docket/regulatory/budget monitors |
| [The bench's tools](features/agent-tools.md) | The typed tool registry agents act through |

## Architecture & internals

| Page | What it covers |
|---|---|
| [Architecture overview](architecture/overview.md) | The four tiers, DyTopo rounds, whiteboard + memory, Q-learning recruitment, project layout |
| [Grounding & coverage](architecture/grounding.md) | Verbatim citation grounding by construction; completeness by construction |
| [Model routing](architecture/model-routing.md) | Heavy/Mid/Light/Vision tiers, `MODEL_STACK` families, local overrides |
| [LPM build plan](lpm-plan.md) | The legal-project-management spine — origin and design notes |
| [TypeDB 3 migration plan](typedb-3-migration-plan.md) | Planned conflict-graph sidecar migration (not started) |
| [CHANGELOG](../CHANGELOG.md) | The keyed historical record of every release wave |

## Integrate & extend

| Page | What it covers |
|---|---|
| [REST API reference](integration/rest-api.md) | The full route map |
| [MCP / Claude Code](integration/mcp.md) | Using BigLaw as an MCP server from Claude Code |
| [Connectors](integration/connectors.md) | 32 connector tools across 15 providers, including the Clio OAuth walkthrough |
| [Plugins & adapters](integration/plugins.md) | Integrate any external legal tool with a JSON file drop |
| [Jurisdiction & NOSLEGAL](integration/jurisdiction-and-noslegal.md) | Jurisdiction routing and the NOSLEGAL v4 taxonomy |

## Benchmarks & provenance

| Page | What it covers |
|---|---|
| [Benchmarks](benchmarks.md) | The Harvey LAB ladder, Go-vs-TS backend numbers, and the accuracy journey |
| [Local accuracy journey](local-accuracy-journey.md) | Technique-by-technique account of local-model grounding, 0% → 94% |
| [Go vs TypeScript benchmark](benchmarks-go-vs-ts.md) | Backend throughput/latency methodology and results |
| [Provenance & licensing](provenance.md) | The Apache-2.0 story, attribution, and the clean-room reimplementation |
| [Clean-room spec](clean-room-spec-document-tools.md) | The dated functional specification the document tools were rebuilt from |
