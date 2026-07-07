<div align="center">

# BigLaw

### The BigLaw tool stack. Open. Free.

**What Am Law 100 firms spend $2M/year on — consolidated into one open-source platform, free for solos, boutiques, and small firms.**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-2563eb.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8.svg)](biglaw-go/go.mod)
[![MCP](https://img.shields.io/badge/MCP-stdio%20server-E6B450.svg)](docs/integration/mcp.md)
[![Vector search](https://img.shields.io/badge/Vector%20search-in--process-7c3aed.svg)](biglaw-go/internal/agents/registry.go)
[![Status: Experimental](https://img.shields.io/badge/Status-Experimental-red.svg)](docs/security.md)

**The platform is a single static Go binary** — it runs end-to-end on a Raspberry Pi with
4 GB of RAM, or entirely on local models (Ollama / LM Studio). Benchmarks vs the original
TypeScript implementation: 1.25×–6.9× ([methodology](docs/benchmarks-go-vs-ts.md)). The code
lives in [`biglaw-go/internal/`](biglaw-go/internal/); the TypeScript original is preserved
at the tag `typescript-final`.

</div>

---

## ⚠ Experimental — read this first

**BigLaw is an experimental research project, not production-hardened software.** It handles
credentials, client matter data, and privileged legal communications, and it has not undergone
a formal independent security audit. Before anything touches real client data, read the
[security notice](docs/security.md) and the [legal notices & disclaimers](docs/legal-notices.md)
— they are not boilerplate — and engage an independent security review. `AUTH_ENABLED=false`
is the local-dev default; **never expose the API on a shared network without enabling
authentication.** Nothing this software produces is legal advice.

---

## What BigLaw Is

BigLaw is a cross between a **platform**, an **experiment**, and an **art project**.

As a platform, it is the most comprehensive open legal AI stack that exists — spanning research,
drafting, redlining, e-signatures, briefing, docketing, billing, and collaboration across a bench
of 100+ agents in a structured multi-round debate architecture.

As an experiment, it is an ongoing attempt to answer a genuine engineering question: how much of
the $50,000–150,000 per-lawyer-per-year legal tech stack can be replicated with open models, open
protocols, and open code? The answer so far is: most of it.

As an art project, it is a provocation. The cost math is not a sales pitch. It is a
statement about who gets access to tools and who doesn't, and what happens when that changes.
It is deliberately maximalist, deliberately opinionated, and deliberately not finished.

You are not buying a product. You are picking up a thing that is still being built and deciding
what to do with it.

BigLaw isn't a chatbot with a legal prompt. It's an **orchestration engine** that replaces a stack
of vendor contracts with a single open-source platform. It runs *DyTopo rounds* of granular
epistemic, conceptual, and writing agents over an **in-process vector agent registry** — and puts
a **debate + verification protocol** between every finding and the page. Low-confidence or
challenged findings stop at a **human gate** before they reach final synthesis.

**Big Michael** is the agent that lives inside your firm's collaboration channels. @-mention him
in Teams or Slack and he dispatches tasks to BigLaw's bench, surfaces matter status and client
briefings, and posts back when work is done — turning the platform into a conversational layer
on top of everything else the firm already uses.

---

## The math

> The tab in your browser you never click is a $300,000 invoice.

Westlaw + CoCounsel, Practical Law, Contract Express, LexisNexis + PSL, Kira, iManage, Everlaw,
Ironclad, Clio Insights, Solve Intelligence: **$50,000–152,000 per lawyer, per year.** BigLaw
covers the same ground for the price of your model-provider API bill — at typical usage for a
ten-lawyer firm, roughly **$2,400/year against $500,000+**.

The line-by-line cost chart, the math by firm size, and the feature-by-feature comparison:
**[Why BigLaw](docs/why-biglaw.md)**.

Every tool in that chart is a subscription you can cancel the day you run setup.sh.
Not all at once. One at a time. Start with whatever costs the most.
Run the matter through BigLaw. Compare the output. Keep what you cancel.

**Go. Do likewise.**

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

## Sixty seconds to running

```bash
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

Needs git + Docker. Clones if needed, seeds `.env`, brings up the three-container stack,
and waits for the REST API at **http://localhost:3102**. Add your `QWEN_API_KEY` (or
local-inference settings) to `.env` — unconfigured connectors degrade gracefully.

```bash
bash setup.sh                                        # already cloned? same thing
go run ./biglaw-go/cmd/biglaw                        # native (Go 1.25+), REST on :3101
go run ./biglaw-go/cmd/biglaw demo                   # end-to-end tour: seed → tabular review →
                                                     # CP checklist → counter-redline (~$0.03)
cd ui && npm install && \
  BIG_MICHAEL_API=http://localhost:3102 npm run dev  # web workbench on :5173
```

Full install paths, the workbench tour, and your first task: **[Getting started](docs/getting-started.md)**.
Opening this repo in Claude Code registers BigLaw as an MCP server: **[MCP guide](docs/integration/mcp.md)**.

---

## What's on the bench

- **100+ agents, 4 tiers, DyTopo rounds** — Need/Offer matching builds each round's comm graph; every finding survives CitationGate → adversarial debate → verification → human gate → [Architecture](docs/architecture/overview.md)
- **Verbatim citation grounding by construction** — hybrid RAG + staged extraction + multi-pass writer took a local 7B from ~0% to 94% grounded citations → [Grounding & coverage](docs/architecture/grounding.md)
- **The negotiation stack** — counter-redline loop with per-change rationale cards, Redtime per-clause negotiation timelines with silent-edit detection, Integrity Check on every inbound document → [Negotiation](docs/features/negotiation.md)
- **Tabular review** — 50 docs × 30 columns with a citation-verification ladder (exact → tolerant → paraphrase judge → ensemble) and a verified tally on the export → [Tabular review](docs/features/tabular-review.md)
- **Document production** — `.docx` generation, tracked-changes redlining, PDF/OCR, DocuSeal e-signing → [Document production](docs/features/document-production.md)
- **Four-tier playbook cascade** (`client > matter > personal > firm`) driving playbook-aware redlines → [Playbooks](docs/features/playbooks.md)
- **Big Michael in Teams & Slack** — matter status, hub-and-spoke client briefings, task dispatch from an @-mention → [Big Michael](docs/features/big-michael.md)
- **Court deadline calculator** — FRCP / UK CPR / EU Competition, calendar vs business days, cited → [Deadlines](docs/features/deadlines.md)
- **Research engines** — CourtListener-backed citation checking, headnote extraction, precedent generation → [Research engines](docs/features/research-engines.md)
- **Billing & LPM** — automatic 6-minute billable units, LEDES 1998B, pre-bills, OCG compliance, daily status reports, docket/regulatory/budget monitors → [Billing & LPM](docs/features/billing-and-lpm.md)
- **32 connectors across 15 providers** — CourtListener, Westlaw, Everlaw, Ironclad, iManage, Clio (OAuth), Drive, Box, Slack, and more → [Connectors](docs/integration/connectors.md)
- **Court-ready audit trail** — hash-chained JSONL with every tool call attributed to the responsible lawyer → [Audit trail](docs/operations/audit-trail.md)
- **Per-call cost tracking** with cache-aware pricing and local power estimates → [Cost tracking](docs/operations/cost-tracking.md)
- **Lawyer voice fingerprinting** — drafting agents mirror the assigned lawyer's style from their own writing → [Tone profiles](docs/operations/tone-profiles.md)
- **Cloud or fully local** — Qwen by default, any OpenAI-compatible endpoint, or air-gapped Ollama / LM Studio / vLLM → [Local inference](docs/deployment/local-inference.md)

## The benchmark

On Harvey **LAB** (60-criterion all-pass rubric, rubric hidden from the agents; judge
claude-sonnet-4-6): claude-haiku-4-5 raw scores **41/60**; the same model on the fix-wave
BigLaw pipeline scores **49/60** on a healthy 6-round run (a 3-round run scored 50† but starved
its analysis round on a credit outage — within judge noise of the 49, remeasure pending), and a
cross-vendor GLM-5.2 run reached **51\*** (best of two, one round inactive). A free, on-prem
qwen2.5:14b scores **36/60** on the identical build. Spot-checked citations came back verbatim
6/6 on the release run where the raw run fabricated statutory penalty figures. The full ladder,
caveats, and the technique-by-technique account: **[Benchmarks](docs/benchmarks.md)**.

---

## Documentation

Everything lives in **[docs/](docs/index.md)**, one page per topic:

| | |
|---|---|
| **Get started** | [Getting started](docs/getting-started.md) · [Why BigLaw](docs/why-biglaw.md) · [Legal notices](docs/legal-notices.md) |
| **Deploy & operate** | [Security](docs/security.md) · [Run modes](docs/deployment/run-modes.md) · [Models & persistence](docs/deployment/models-and-persistence.md) · [Local inference](docs/deployment/local-inference.md) · [Secrets](docs/deployment/secrets.md) · [Access control](docs/operations/access-control.md) · [Audit trail](docs/operations/audit-trail.md) · [Cost tracking](docs/operations/cost-tracking.md) · [Tone profiles](docs/operations/tone-profiles.md) |
| **Features** | [Negotiation](docs/features/negotiation.md) · [Tabular review](docs/features/tabular-review.md) · [Document production](docs/features/document-production.md) · [Playbooks](docs/features/playbooks.md) · [Big Michael](docs/features/big-michael.md) · [Deadlines](docs/features/deadlines.md) · [Research engines](docs/features/research-engines.md) · [Billing & LPM](docs/features/billing-and-lpm.md) · [The bench's tools](docs/features/agent-tools.md) |
| **Architecture** | [Overview](docs/architecture/overview.md) · [Grounding & coverage](docs/architecture/grounding.md) · [Model routing](docs/architecture/model-routing.md) |
| **Integrate & extend** | [REST API](docs/integration/rest-api.md) · [MCP / Claude Code](docs/integration/mcp.md) · [Connectors](docs/integration/connectors.md) · [Plugins & adapters](docs/integration/plugins.md) · [Jurisdiction & NOSLEGAL](docs/integration/jurisdiction-and-noslegal.md) |
| **Benchmarks & provenance** | [Benchmarks](docs/benchmarks.md) · [Local accuracy journey](docs/local-accuracy-journey.md) · [Provenance & licensing](docs/provenance.md) · [CHANGELOG](CHANGELOG.md) |

---

## License

Apache-2.0 ([`LICENSE`](LICENSE)) with an express patent grant — use it, modify it, embed it,
run it as a service; attribution per [`NOTICE`](NOTICE) is all that is asked. The document tools
are a clean-room reimplementation with a published spec and an executed attestation record
([docs/clean-room-attestations.md](docs/clean-room-attestations.md)) — the full
story: **[Provenance & licensing](docs/provenance.md)**.

<div align="center"><sub>Copyright © 2026 Discover Legal</sub></div>
