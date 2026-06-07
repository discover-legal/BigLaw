<div align="center">

# BigLaw

### The BigLaw tool stack. Open. Free.

**What Am Law 100 firms spend $2M/year on — consolidated into one open-source platform, free for solos, boutiques, and small firms.**

[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-2563eb.svg)](LICENSE)
[![TypeScript](https://img.shields.io/badge/TypeScript-strict-3178c6.svg)](tsconfig.json)
[![MCP](https://img.shields.io/badge/MCP-stdio%20server-E6B450.svg)](#using-from-claude-code)
[![Vector DB](https://img.shields.io/badge/RuVector-native%20HNSW-7c3aed.svg)](src/agents/registry.ts)
[![Status: Experimental](https://img.shields.io/badge/Status-Experimental-red.svg)](#-experimental--security-notice)

</div>

---

## ⚠ Experimental — Security Notice

**BigLaw is an experimental research project. It is not production-hardened software.**

The goal of this project is to build the **most comprehensive open legal AI platform possible** — covering the widest breadth of legal workflows, integrations, agent types, and jurisdictions. Comprehensiveness of capability is the primary objective. Test coverage and security hardening, while taken seriously and continuously improved, are secondary to that goal.

**What this means in practice:**

- The platform handles credentials, client matter data, and privileged legal communications. Firms deploying it are responsible for their own threat model.
- The codebase receives ongoing security sweeps and bug fixes, but has **not undergone a formal independent security audit**.
- **Before deploying in any environment where real client data is involved, you must engage an independent security professional (pen tester, security engineer, or FDE — Forward Deployed Engineer / Formal Deployment Expert) to review the deployment configuration and code.**
- `AUTH_ENABLED=false` is the default for local development. **Never expose the API on a public or shared network without enabling authentication.**
- API keys, session secrets, and OAuth credentials must be treated as production secrets regardless of environment.

**Independent security review is not optional for production deployments. It is a prerequisite.**

This notice does not diminish what BigLaw is — it is the most capable open legal AI stack available. It does mean you should not deploy it like a SaaS product without the due diligence that any complex, credential-holding, client-data-processing system requires.

---

## What BigLaw Is

BigLaw is a cross between a **platform**, an **experiment**, and an **art project**.

As a platform, it is the most comprehensive open legal AI stack that exists — spanning research,
drafting, redlining, e-signatures, briefing, docketing, billing, and collaboration across a bench
of 100+ agents in a structured multi-round debate architecture.

As an experiment, it is an ongoing attempt to answer a genuine engineering question: how much of
the $50,000–150,000 per-lawyer-per-year legal tech stack can be replicated with open models, open
protocols, and open code? The answer so far is: most of it.

As an art project, it is a provocation. The cost chart below is not a sales pitch. It is a
statement about who gets access to tools and who doesn't, and what happens when that changes.
It is deliberately maximalist, deliberately opinionated, and deliberately not finished.

You are not buying a product. You are picking up a thing that is still being built and deciding
what to do with it.

---

## Legal Notices and Disclaimers

*Read these. They are not boilerplate. They describe real risks that apply to you.*

### No Legal Advice

**BigLaw does not provide legal advice. Nothing produced by this software — no output, finding,
draft, analysis, summary, headnote, redline, briefing, or synthesis — constitutes legal advice,
and none of it should be relied upon as such.**

BigLaw is a software tool that uses large language models to assist with legal research and
document tasks. LLMs hallucinate. They misstate case holdings. They miss recent developments.
They confuse jurisdictions. They produce authoritative-sounding text that is factually wrong.
The debate and verification protocols in this system reduce these errors but do not eliminate them.

**Every output of this system requires review by a licensed attorney before it is used in any
legal matter.** Relying on unreviewed AI output in client matters may constitute malpractice,
regardless of how capable the underlying system appears.

If you are not a licensed attorney and you are using this software to answer legal questions
about your own situation: please consult a lawyer. This software is not a substitute.

### No Attorney-Client Relationship

Use of BigLaw does not create an attorney-client relationship of any kind — between you and
Discover Legal, between you and any contributor to this project, or between you and any AI
system operated through this software.

> # ⚠ PRIVILEGE IS NOT GUARANTEED
>
> **Whether communications, outputs, or data processed through this system attract
> legal professional privilege (attorney-client privilege, legal advice privilege,
> litigation privilege, or equivalent) depends entirely on your jurisdiction, the
> specific facts of your deployment, how the system is configured, who has access
> to it, and how outputs are used.**
>
> **Do not assume privilege applies. It may not.**
>
> To structure a deployment that maximises privilege protection for your jurisdiction
> — including network isolation, access controls, data residency, and workflow design —
> **engage an independent FDE (Forward Deployed Engineer / Formal Deployment Expert) before handling any privileged matter.**

### Unauthorised Practice of Law

Depending on your jurisdiction, using AI tools to perform certain legal tasks — drafting court
documents, providing legal advice to third parties, representing parties in legal proceedings —
may constitute the unauthorised practice of law if performed by a non-attorney. The fact that
the work is AI-assisted does not change this analysis. Know your jurisdiction's rules.

If you are a law firm deploying BigLaw, you remain responsible for supervising all AI-assisted
work product under your professional responsibility obligations, including the duty of competence
(understanding the technology), the duty of confidentiality (securing client data), and the duty
of supervision (reviewing outputs before they leave the firm).

### Confidentiality and Data Security

**BigLaw processes whatever data you give it.** If you feed it client communications, privileged
documents, personally identifiable information, health records, financial data, or anything else
that is sensitive or regulated, that data will flow through your configured model provider and
may be stored locally. Where that data goes depends entirely on how you have deployed the system.

**BigLaw supports multiple inference backends — the data handling implications differ for each:**

```mermaid
flowchart LR
    BL["BigLaw"]

    BL -->|"default<br/>ANTHROPIC_API_KEY"| ANT["Anthropic API<br/><i>Haiku / Sonnet / Opus</i><br/>─────────────<br/>Data leaves infrastructure<br/>BAA: enterprise tier only<br/>Review DPA before use"]

    BL -->|"OPENAI_API_KEY or<br/>AZURE_OPENAI_*"| OAI["OpenAI / Azure OpenAI<br/><i>GPT-4o etc.</i><br/>─────────────<br/>Data leaves infrastructure<br/>BAA: ChatGPT Ent / Azure only<br/>Azure has stronger DPA terms"]

    BL -->|"OLLAMA_ENABLED=true<br/>LOCAL_INFERENCE_URL"| LOC["Local inference<br/><i>Ollama · LM Studio · vLLM</i><br/>─────────────<br/>Data stays on your hardware<br/>No BAA needed<br/>Air-gap capable"]

    style LOC fill:#166534,color:#fff
    style ANT fill:#1e3a5f,color:#fff
    style OAI fill:#1e3a5f,color:#fff
```

- **Anthropic API (default)** — data is sent to Anthropic's servers subject to their data
  processing terms and usage policies. Review these before using with client data.
- **OpenAI / Azure OpenAI** — data is sent to OpenAI or Microsoft's servers subject to their
  respective terms. Azure OpenAI offers enterprise data handling commitments that the standard
  OpenAI API does not.
- **Ollama / LM Studio / local inference** (`OLLAMA_ENABLED=true` or `LOCAL_INFERENCE_URL`) —
  data never leaves your infrastructure. For air-gapped or maximally confidential deployments,
  local inference is the only option that gives you complete data control.

**Regardless of backend, data may also be:**
- Stored in the local vector database (persists to disk at `./data/`)
- Written to the audit log (JSONL, also on disk)
- Included in prompts that are cached by a cloud API provider

**Regulatory obligations depend on your jurisdiction and the nature of the data:**

- **HIPAA (US)** — if you process protected health information, you need a Business Associate
  Agreement (BAA) with your model provider. Anthropic offers BAAs on certain enterprise tiers
  only. OpenAI offers BAAs on ChatGPT Enterprise and Azure OpenAI. Standard API tiers typically
  do not include BAA coverage. If you cannot get a BAA, use local inference.
- **GDPR (EU/EEA)** — processing personal data of EU residents requires a lawful basis and,
  for cloud providers, appropriate Standard Contractual Clauses or equivalent transfer mechanisms.
  Data residency matters. Check where your provider processes and stores data.
- **CCPA / US state privacy laws** — obligations vary by state and the nature of the data.
- **Bar association ethics rules** — most jurisdictions now have guidance on cloud-based legal
  technology. Many require a reasonable investigation of the provider's security and privacy
  practices before using the service with client data.

**The bottom line: your data handling obligations depend on your jurisdiction, your client base,
the sensitivity of the data, and which inference backend you use. There is no universal answer.
Engage qualified legal counsel and an independent FDE to map your specific obligations before
deploying with real client data.**

### Deployment Liability

**You deploy this software at your own risk.** Discover Legal and the contributors to this
project provide it under the AGPL-3.0 licence, which explicitly disclaims all warranties,
including fitness for a particular purpose and non-infringement.

Specific risks that arise from misconfigured or insecure deployment include:

- **Client data breach.** If the API is exposed without authentication (`AUTH_ENABLED=false`
  on a network-accessible host), any client matter data ingested into the system is potentially
  accessible to anyone who can reach the endpoint. This would constitute a data breach under
  most applicable law and a serious professional responsibility violation.
- **Credential exposure.** API keys, OAuth tokens, and session secrets stored in `.env` files
  or accessible via a misconfigured server can be extracted and used to incur costs, access
  third-party systems, or impersonate your firm.
- **Prompt injection.** Malicious content in documents you ingest or queries you run through
  the system could potentially manipulate agent outputs. The system includes defences against
  this but they are not complete.
- **Malpractice exposure.** Using AI-generated output without adequate review in a client matter
  creates professional liability risk. This risk is yours, not ours.
- **Regulatory exposure.** Depending on your jurisdiction and practice area, use of AI tools
  in legal matters may trigger disclosure obligations to clients, adverse parties, or courts.
  Some courts require disclosure of AI use in filings. Check your local rules.

### Jurisdiction

This software is designed to support legal work across multiple jurisdictions. It is not
certified, approved, or validated for use in any jurisdiction. The agents, workflows, and
outputs are not a substitute for jurisdiction-specific legal expertise.

### Third-Party Services

BigLaw integrates with numerous third-party services — Anthropic, Microsoft Graph, Google
Workspace, Slack, Clio, CourtListener, Westlaw, Everlaw, Ironclad, DocuSign, and others.
Your use of those services through this software is governed by their own terms. BigLaw is
not affiliated with, endorsed by, or a certified partner of any of these services.

### Summary

You are using experimental software in one of the highest-stakes professional contexts that
exists. The software is capable and the engineering is serious. It is also unaudited,
incompletely tested, and built for comprehensiveness first. Use it with appropriate scepticism,
appropriate oversight, and appropriate professional responsibility.

---

BigLaw isn't a chatbot with a legal prompt. It's an **orchestration engine** that replaces a stack
of vendor contracts with a single open-source platform.

It runs *DyTopo rounds* of granular epistemic, conceptual, and writing agents over a **RuVector
native HNSW registry** — and puts a **debate + verification protocol** between every finding
and the page. Low-confidence or challenged findings stop at a **human gate** before they reach
final synthesis.

**Big Michael** is the agent that lives inside your firm's collaboration channels. @-mention him
in Teams or Slack and he dispatches tasks to BigLaw's bench, surfaces matter status and client
briefings, and posts back when work is done — turning the platform into a conversational layer
on top of everything else the firm already uses.

---

## The cost chart

> The tab in your browser you never click is a $300,000 invoice.

Am Law 100 firms don't publish what they spend on legal tech. Let's do the math for them.

### Per lawyer, per year

| Vendor | What it does | Cost / lawyer / year | BigLaw |
|---|---|---|---|
| **Westlaw + CoCounsel** (Thomson Reuters) | Case law, statutes, AI research assist, citation checking | $15,000–50,000 | ✓ `citation_check`, `westlaw_research`, `court_listener_*` |
| **Practical Law** (Thomson Reuters) | Standard documents, precedents, know-how notes | $10,000–20,000 | ✓ Precedent generator, playbook cascade |
| **Contract Express** (Thomson Reuters) | Document automation, clause playbooks | $5,000–20,000 | ✓ Four-tier playbook cascade |
| **LexisNexis + PSL** (RELX) | Headnotes, legal analysis, PSL standard docs | $8,000–25,000 | ✓ Headnote engine, `descrybe_*`, `trellis_*` |
| **Definely / Kira / Luminance** | AI contract review, clause extraction, redlining | $2,000–8,000 | ✓ Playbook-aware redline engine |
| **iManage / NetDocuments** | Document management, matter workspace | $2,000–5,000 | ✓ `imanage_search`, `imanage_get_document` |
| **Everlaw / Relativity** | eDiscovery, document review | $3,000–10,000 | ✓ `everlaw_search_documents`, `_get_review_set` |
| **Ironclad / DocuSign CLM** | Contract lifecycle management | $2,000–5,000 | ✓ `ironclad_*`, `docusign_*` |
| **Clio Insights + Grow** | Matter health, client analytics, CRM | $1,000–3,000 | ✓ Matter health monitor, client briefing swarm |
| **Solve Intelligence** | Patent drafting and claims | $2,000–6,000 | ✓ `solve_intelligence_*` |
| **TOTAL** | | **$50,000–152,000 / lawyer / year** | **$0** |

_Estimates based on publicly reported ranges and firm procurement disclosures. Enterprise deals vary; BigLaw firms negotiate volume pricing. Actual costs may be higher._

### The math by firm size

| Firm size | Annual tool stack (low) | Annual tool stack (high) | BigLaw cost | Year-1 savings |
|---|---|---|---|---|
| Solo | $50,000 | $152,000 | **$0** | $50k–152k |
| 5 lawyers | $250,000 | $760,000 | **$0** | $250k–760k |
| 10 lawyers | $500,000 | $1,520,000 | **$0** | $500k–1.5M |
| 25 lawyers | $1,250,000 | $3,800,000 | **$0** | $1.25M–3.8M |
| 50 lawyers | $2,500,000 | $7,600,000 | **$0** | $2.5M–7.6M |

**What you actually pay to run BigLaw:** your Anthropic API bill.
At typical usage (10 lawyers, moderate workload): ~$100–300/month — call it **$2,400/year**.

That's the spread: $500,000/year vs $2,400/year for the same capability.

### Always be closing

Every tool in the table above is a subscription you can cancel the day you run setup.sh.

Not all at once. One at a time. Start with whatever costs the most.
Run the matter through BigLaw. Compare the output. Keep what you cancel.

```bash
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

### Do likewise

A senior associate billed 2,200 hours last year. Her firm paid $80,000 in Westlaw fees
for her seat. The Westlaw subscription cost more than her bonus.

BigLaw gives it back.

Take it. Use it. Tell the next solo down the hall.
Run the math on your firm. Run setup.sh.

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

## Why it's different

| Most legal AI | BigLaw |
|---|---|
| One model, one pass | 100+ agents across 4 tiers, multiple DyTopo rounds |
| "Trust me" answers | Every finding survives **adversarial debate** + **verification passes** before output |
| Hallucinated cites | **CitationGate** rejects any claim whose source isn't in the registry |
| Locked to one jurisdiction | **Jurisdiction-neutral** native bench — applies the governing law each matter specifies |
| Black box | Court-ready **audit trail** — every agent invocation, tool call (with the lawyer's identity), finding, gate decision, document search, and access denial — hash-chained JSONL + live SSE |
| Text in, text out | Cited briefs, **.docx** with tracked changes, e-signed via DocuSeal |
| Cloud-only | 3-tier cloud routing **or** fully local (Ollama / LM Studio / vLLM) |
| Static agent pool | **Q-learning recruitment** — agents that produce high-confidence findings are promoted; weak ones deprioritised over time |
| Siloed per-round context | **Intra-round whiteboard** broadcast to all agents + **Haiku-synthesised inter-round rollup** carried forward |
| One-size config | **Admin panel** — lawyer/non-lawyer mode, DyTopo depth, verification & DocuSeal, applied live |
| Generic document store | Documents auto-classified by **practice area** with matching lawyers surfaced on ingest |
| No billing integration | Automatic **6-minute billable time units** tracked per lawyer, per matter, exportable as CSV |
| Generic output voice | Per-lawyer **voice fingerprinting** from LinkedIn posts, DOCX, PDF, or CSV — drafting agents mirror the assigned lawyer's style |
| Black-box costs | **Per-call cost tracking** with prompt-cache-aware pricing, local power estimates, and an admin cost dashboard |
| Manual setup | **Interactive setup wizard** — one curl, checks prereqs, checkbox connector picker, writes `.env`, done |
| No deadline tracking | **Court deadline calculator** — FRCP, UK CPR, EU Competition rules; calendar vs business days, cited |
| Info scattered across systems | **Big Michael hub-and-spoke briefing swarm** — pulls from Clio, iManage, Slack, Teams, Drive, SharePoint, email in parallel |

---

## Architecture

```mermaid
graph TD
    T0["T0 · Root Orchestrator<br/><i>Opus — issues RoundGoals each phase</i>"]
    T1R["Research Manager"]
    T1A["Analysis Manager"]
    T1D["Drafting Manager"]
    T1C["Compliance Manager"]
    T2E["Epistemic agents ×18<br/><i>contract · M&A · privacy · antitrust<br/>employment · IP · tax · litigation…</i>"]
    T2C["Conceptual agents ×8<br/><i>materiality · liability · causation<br/>enforceability · good faith…</i>"]
    T2W["Writing agents ×13<br/><i>brief · memo · redline · headnote<br/>precedent · NDA · opinion…</i>"]
    T3["Tool agents ×6<br/><i>web search · retrieval · extraction<br/>translation · citation · e-sign</i>"]
    WB[("Intra-round<br/>whiteboard")]
    MEM[("Inter-round<br/>memory store")]
    GATE["Human gate<br/><i>low-confidence findings</i>"]
    SYN["Opus synthesis<br/><i>final output</i>"]

    T0 -->|RoundGoal| T1R & T1A & T1D & T1C
    T1R & T1A & T1D & T1C -->|DyTopo Need/Offer match| T2E & T2C & T2W
    T2E & T2C & T2W -->|tool_use agentic loop| T3
    T2E & T2C & T2W -->|findings| WB
    WB -->|CitationGate → Debate → Verify ×10| GATE
    GATE -->|approved findings| MEM
    MEM -->|context for next round| T1R & T1A & T1D & T1C
    MEM --> SYN
```

**Each DyTopo round:**

```mermaid
sequenceDiagram
    participant O as Orchestrator
    participant E as DyTopo Engine
    participant A as Agents (T2)
    participant P as Protocols
    participant G as Human Gate
    participant M as Memory

    O->>E: RoundGoal
    E->>A: Need/Offer descriptors (Haiku ~10 tokens each)
    E->>E: cosine-match Needs → Offers<br/>build directed comm graph
    E->>A: route messages along graph edges
    A->>A: agentic loops with tools + memory context
    A->>P: findings → CitationGate
    P->>P: Debate (Opus)
    P->>P: Verification (Haiku ×10)
    P-->>G: low-confidence / challenged findings
    G-->>P: approved / rejected
    P->>M: round digest (Haiku synthesis)
    M-->>O: inter-round context for next phase
```

1. Every agent emits a Need/Offer descriptor (Haiku, ~10 tokens)
2. The engine cosine-matches Needs → Offers to build a sparse directed comm graph
3. Messages routed along graph edges to each agent
4. Agents run full agentic loops with routed messages + inter-round memory → findings
5. Findings written to the **intra-round whiteboard**
6. Findings pass **CitationGate → Debate (Opus) → Verification (Haiku ×10)**
7. Haiku synthesises the whiteboard into a round digest → written to **inter-round memory** for the next round
8. Low-confidence / challenged findings escalate to a **human gate** before synthesis

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

## Big Michael — the channel agent

**Big Michael** is BigLaw's conversational face in your collaboration tools. Add him to Teams or
Slack and he responds to @-mentions in any channel, dispatching work to BigLaw's bench and
posting results back.

```
@BigMichael status M-2024-001        → matter health score + active tasks + risks
@BigMichael briefing Acme Corp       → full hub-and-spoke client intelligence briefing
@BigMichael search force majeure     → semantic search across the knowledge store
@BigMichael task review this NDA     → submit a roundtable AI task
@BigMichael run due-diligence        → run a named workflow template
@BigMichael help                     → list available commands
```

**Teams setup** (`TEAMS_WEBHOOK_SECRET` + `TEAMS_INCOMING_WEBHOOK_URL`):
1. Teams admin → Apps → Outgoing Webhooks → Create
2. Set callback URL to `https://<host>/bots/teams/webhook`
3. Copy the security token → `TEAMS_WEBHOOK_SECRET`
4. Channel → … → Connectors → Incoming Webhook → copy URL → `TEAMS_INCOMING_WEBHOOK_URL`

**Slack setup** (`SLACK_BOT_TOKEN` + `SLACK_SIGNING_SECRET`):
1. [api.slack.com/apps](https://api.slack.com/apps) → Create App → From scratch
2. Bot Token Scopes: `chat:write`, `channels:history`, `search:read`
3. Event Subscriptions → Request URL: `https://<host>/bots/slack/events`
4. Subscribe to: `app_mention`
5. Install to workspace → copy Bot Token + Signing Secret

**Proactive notifications** — when any task completes, Big Michael posts to the matter's linked
channel automatically:

```bash
# Link a matter to a Teams channel
POST /bots/teams/matter-link  { "matterNumber": "M-001", "webhookUrl": "https://..." }

# Link a matter to a Slack channel
POST /bots/slack/matter-link  { "matterNumber": "M-001", "channelId": "C0123ABCD" }
```

**Client intelligence briefing** — Big Michael's briefing command launches a hub-and-spoke
swarm that pulls from all connected systems in parallel (12 s per spoke, `Promise.allSettled`):

```mermaid
graph LR
    CMD["@BigMichael briefing Acme Corp"]
    HUB["Hub<br/><i>Sonnet synthesis</i>"]
    OUT["Client briefing<br/><i>single Markdown doc</i>"]

    CMD --> HUB

    HUB <-->|parallel, 12s timeout| S1["Clio<br/><i>matters · contacts · notes</i>"]
    HUB <-->|parallel, 12s timeout| S2["iManage<br/><i>documents · matters</i>"]
    HUB <-->|parallel, 12s timeout| S3["Slack<br/><i>channel mentions</i>"]
    HUB <-->|parallel, 12s timeout| S4["Teams chat<br/><i>message search</i>"]
    HUB <-->|parallel, 12s timeout| S5["SharePoint<br/><i>file search</i>"]
    HUB <-->|parallel, 12s timeout| S6["Google Drive / Box<br/><i>files · folders</i>"]
    HUB <-->|parallel, 12s timeout| S7["Graph Mail<br/><i>O365 email threads</i>"]
    HUB <-->|parallel, 12s timeout| S8["Gmail<br/><i>email threads</i>"]
    HUB <-->|parallel, 12s timeout| S9["Knowledge store<br/><i>ingested docs · semantic search</i>"]
    HUB <-->|parallel, 12s timeout| S10["Internal<br/><i>tasks · health · time entries</i>"]

    HUB --> OUT
```

The hub Sonnet synthesises all spokes into a single Markdown briefing. The scattergun problem —
client info spread across 10 mailboxes, 2 call notes, and 4 DM threads — solved in one command.

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
| 32 connector tools | CourtListener · Westlaw · Everlaw · Trellis · Descrybe · Ironclad · iManage · Definely · DocuSign CLM · Lawve AI · Solve Intelligence · Google Drive · Box · Slack · TopCounsel |
| `compute_deadlines` | Court deadline calculator — trigger date → all deadlines under FRCP / UK CPR / EU Competition rules, with citations |
| `redline_contract` | Playbook-aware contract redlining — clause extraction → Sonnet analysis → tracked-change report |
| `generate_headnotes` | Westlaw Key Number / LexisNexis headnote replacement — Sonnet extraction + Haiku meta |
| `generate_precedent` | Practical Law / PSL replacement — Haiku structure + Opus drafting from firm knowledge + playbook cascade |

> Document generation, tabular review, and tracked-change redlining are ported from
> [Mike](https://github.com/willchen96/mike) (AGPL-3.0) and adapted to BigLaw's tool
> registry and provider abstraction. See [`NOTICE`](NOTICE).

---

## Quick start

### The easy way — one command

```bash
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

Handles everything: Node.js install (via nvm if needed), clone, `npm install`, then an interactive wizard that checks Python / Docker / Tesseract, walks you through every API key with inline instructions, shows a **checkbox picker for all 32 connectors**, writes `.env`, and optionally runs the smoke test. Re-run any time to add connectors.

### Already have the repo cloned?

```bash
bash setup.sh       # or: npm run setup  (requires Node 18+ already installed)
```

### Manual setup

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

### Run modes — browsing **and** the Claude Code MCP at the same time

The vector DB under `./data` takes an exclusive single-writer lock and the REST API binds
one port, so only **one** process can own them. To run the web console and the Claude Code
MCP together, one process owns the DB and the other attaches as a thin client over the REST
API. `BIG_MICHAEL_MODE` selects the role:

| Mode | Behaviour | Use |
|---|---|---|
| `auto` *(default)* | Own the DB if the port is free; otherwise attach as an MCP client | Just works — the MCP coexists with a running console |
| `backend` | Own DB + REST, never start MCP | The persistent console service — `npm run serve` |
| `mcp` | Pure MCP client — errors if no backend is reachable | Force Claude Code's MCP to be a client |
| `standalone` | Classic single process: own DB + REST + MCP on stdio | The original behaviour, on demand |

```bash
npm run serve                           # dedicated backend (owns DB + REST), no MCP
```

With a backend running, the console (`:5173 → :3101`) and Claude Code's MCP both connect to
it — Claude Code's `.mcp.json` runs in `auto` mode, so it detects the owner and attaches as a
client automatically. Set `BIG_MICHAEL_API` to point a client at a non-default owner URL.

---

## Legal data connectors

BigLaw ships 32 connector tools across 15 providers, all using Streamable HTTP MCP (JSON-RPC 2.0).
Unconfigured connectors return a structured `{ error: "not configured" }` — they never crash the server.

**Legal research & courts**

| Provider | Tools | Activation |
|---|---|---|
| CourtListener | `court_listener_search`, `_opinion`, `_docket` | Always on (optional key for higher rate limits) |
| Westlaw / CoCounsel | `westlaw_research`, `_check_citation` | `WESTLAW_API_KEY` |
| Everlaw | `everlaw_search_documents`, `_get_review_set` | `EVERLAW_API_KEY` |
| Trellis | `trellis_search_cases`, `_get_docket`, `_judge_analytics` | `TRELLIS_API_KEY` |
| Descrybe | `descrybe_search_cases`, `_check_citation` | `DESCRYBE_API_KEY` |
| Solve Intelligence | `solve_intelligence_search_patents`, `_draft_claims` | `SOLVE_INTELLIGENCE_API_KEY` |

**Contract & document management**

| Provider | Tools | Activation |
|---|---|---|
| Ironclad | `ironclad_search_contracts`, `_get_contract` | `IRONCLAD_API_KEY` |
| DocuSign CLM | `docusign_search_contracts`, `_get_envelope` | `DOCUSIGN_API_KEY` |
| iManage | `imanage_search`, `_get_document` | `IMANAGE_API_KEY` |
| Definely | `definely_analyze_structure`, `_resolve_definition` | `DEFINELY_API_KEY` |
| Lawve AI | `lawve_review_contract`, `_search_clauses` | `LAWVE_API_KEY` |

**VDR & productivity**

| Provider | Tools | Activation |
|---|---|---|
| Google Drive | `google_drive_search`, `_get_file` | `GOOGLE_DRIVE_API_KEY` |
| Box | `box_search`, `_get_file` | `BOX_API_KEY` |
| Slack | `slack_search`, `_send_message` | `SLACK_API_KEY` |

**Outside counsel**

| Provider | Tools | Activation |
|---|---|---|
| TopCounsel | `topcounsel_route_matter`, `_get_panel` | `TOPCOUNSEL_API_KEY` |

**Practice management**

| Provider | Tools | Activation |
|---|---|---|
| Clio | `clio_list_matters`, `clio_get_matter`, `clio_list_documents`, `clio_download_document`, `clio_create_activity`, `clio_create_note`, `clio_list_contacts` | `CLIO_CLIENT_ID` + `CLIO_CLIENT_SECRET` (OAuth) |

Clio uses OAuth 2.0 rather than a static API key. After setting credentials, a partner visits
`GET /auth/clio/connect` to authorise the firm's Clio account. Tokens are persisted to
`./data/clio-auth.json` and auto-refreshed. All four Clio data regions are supported (`CLIO_REGION=us|eu|ca|au`).

**Matter import:** `POST /tasks/from-clio-matter` fetches a Clio matter's details, ingests its
attached documents into the knowledge base, and submits a BigLaw task in one call.

**Time sync:** `POST /time-entries/sync-to-clio` pushes BigLaw billable time entries back to a
Clio matter as activity records, preserving 6-minute billing unit rounding. Idempotent — entries
are stamped with `clioSyncedAt` on success and skipped on subsequent calls.

---

## Court deadline calculator

`src/deadlines/engine.ts` — pure TypeScript, no external service required.

Feed it a trigger event and date; it returns every downstream deadline under the applicable rule set, calendar vs business days computed correctly, jurisdiction holidays applied, with the procedural citation for each.

```typescript
import { DeadlineEngine } from "./src/deadlines/engine.js";

const engine = new DeadlineEngine();
await engine.loadRulesDir("./src/deadlines/rules");

const result = engine.compute({
  jurisdiction: "us-federal-frcp",
  trigger: "complaint_served",
  triggerDate: new Date("2025-09-01"),
});
// result.deadlines → [{ event: "answer_due", date: Date, cite: "FRCP 12(a)(1)(A)(i)", warning: Date }, …]
```

**Rule sets shipped** (marked `SAMPLE — AI-GENERATED — NOT VERIFIED BY COUNSEL` until a practitioner submits a verified PR):

| File | Jurisdiction | Rules |
|---|---|---|
| `us-federal-frcp.yaml` | US Federal | FRCP answer, reply, MTD opposition, MSJ, FRAP appeal, service, Rule 26(f) |
| `uk-cpr.yaml` | UK | CPR acknowledgment, defence, summary judgment response, appeal notice |
| `eu-competition.yaml` | EU | Competition regulation response, appeal, leniency deadlines |

Holiday tables are computed in-process (US federal, UK bank, EU institutions — Butcher/Meeus Easter). Adding a new jurisdiction is a YAML file drop in `src/deadlines/rules/`.

> ⚠️ **These rule sets are illustrative examples only.** Deadlines vary by judge, local rules, and standing orders. ALWAYS verify with a licensed attorney before relying on any computed deadline. See `src/deadlines/rules/CONTRIBUTING.md` to submit a verified rule set.

---

## Clio — getting started

Clio uses OAuth 2.0 rather than a static API key. Setup takes about five minutes.

### 1. Register an OAuth app in Clio

1. Log in to Clio as a firm admin.
2. Go to **Settings → Developer Applications → New Application**.
3. Fill in a name (e.g. "BigLaw") and set the **Redirect URI** to:
   ```
   http://localhost:3101/auth/clio/callback
   ```
   For production, replace with your actual `PUBLIC_BASE_URL`, e.g.:
   ```
   https://biglaw.yourfirm.com/auth/clio/callback
   ```
   Clio performs an exact-string match — the URI must be identical to `CLIO_REDIRECT_URI` in your `.env`.

4. In the app's **API Access** panel, enable permissions for each resource BigLaw uses:

   | Resource | Why |
   |---|---|
   | **Matters** | `clio_list_matters`, `clio_get_matter`, matter import |
   | **Contacts** | `clio_list_contacts` |
   | **Documents** | `clio_list_documents`, `clio_download_document` |
   | **Activities** | `clio_create_activity`, time-entry sync |
   | **Notes** | `clio_create_note` |
   | **Users** | `who_am_i` call to fetch firm name on connect |

5. Save. Copy the **Client ID** and **Client Secret**.

### 2. Configure your `.env`

```bash
CLIO_CLIENT_ID=your-client-id
CLIO_CLIENT_SECRET=your-client-secret

# Must match where the firm's data is hosted — wrong region = 401 on every call
# us (default) | eu | ca | au
CLIO_REGION=us

# Only needed if your app deployment URL differs from the default
# CLIO_REDIRECT_URI=https://biglaw.yourfirm.com/auth/clio/callback
```

### 3. Connect

Start the server (`npm start` or `npm run dev`), then have a **partner** visit:

```
GET http://localhost:3101/auth/clio/connect
```

This redirects to Clio's OAuth consent screen. After the firm admin approves, Clio redirects back
and tokens are stored to `./data/clio-auth.json`. They auto-refresh; you won't need to reconnect
unless the refresh token is revoked.

Check the connection status at any time:

```bash
curl http://localhost:3101/auth/clio/status
# → { "connected": true, "firmName": "Smith & Jones LLP", "connectedAt": "2026-06-03T..." }
```

### 4. Use it

**Import a matter:**
```bash
curl -X POST http://localhost:3101/tasks/from-clio-matter \
  -H "Content-Type: application/json" \
  -d '{ "matterId": 12345, "workflowType": "roundtable" }'
```
This fetches the matter, ingests attached documents into the knowledge base, and kicks off a full
bench run. Returns `{ task, documentsIngested }`.

**Sync billable time to Clio:**
```bash
curl -X POST http://localhost:3101/time-entries/sync-to-clio \
  -H "Content-Type: application/json" \
  -d '{ "clioMatterId": 12345, "matterNumber": "001-2024" }'
```
Returns `{ synced, skipped, errors }`. Already-synced entries are skipped automatically.

---

## Using from Claude Code

`.mcp.json` registers BigLaw as an MCP server. Opening this directory in Claude Code exposes
the full toolset (`submit_task`, `get_task`, `approve_gate`, `submit_from_template`,
`ingest_document`, `search_knowledge`, `get_audit`, …):

```
Use BigLaw to review this SaaS master services agreement under New York law —
flag the uncapped indemnity and unlimited-liability exposure, and recommend fallback
positions for the customer. Run a roundtable workflow.
```

Claude Code submits the task, polls progress, approves any human gates, and surfaces the
final synthesis.

`.mcp.json` runs in `auto` mode: if a backend is already serving the REST API (e.g. the web
console started with `npm run serve`), Claude Code's MCP attaches to it as a thin client
instead of opening the vector DB itself — so the console and the MCP run side by side without
fighting over the single-writer lock. See **Run modes** above.

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
POST   /profiles/:id/tone/import           DELETE /profiles/:id/tone
POST   /profiles/:id/tone/linkedin-import  (backwards-compatible alias)
GET    /cost/summary                                                       (partner only)
GET    /tasks/:id/cost        GET /profiles/:id/cost
GET    /auth/providers        GET /auth/:provider/{login,callback} · POST /auth/logout
GET    /auth/clio/status      GET /auth/clio/connect · GET /auth/clio/callback
DELETE /auth/clio/disconnect
POST   /tasks/from-clio-matter                                             (partner only)
POST   /time-entries/sync-to-clio                                         (partner only)
GET    /audit · /audit/stream (SSE)        GET /health
POST   /bots/teams/webhook                 Teams Outgoing Webhook receiver
POST   /bots/teams/notify                  Internal: post to a Teams channel
POST   /bots/slack/events                  Slack Events API receiver
POST   /bots/slack/notify                  Internal: post to a Slack channel
POST   /bots/{teams,slack}/matter-link     Link a matter to a channel
GET    /redline                            Contract redline (playbook-aware)
POST   /headnotes/generate                 Headnote extraction from case opinions
POST   /precedents/generate                Precedent document generation
```

Document ingestion (`POST /documents`, `POST /documents/upload`) returns enriched metadata:
```json
{ "id": "…", "practiceArea": "Corporate & M&A", "detectedClient": { "clientNumber": "C-001", "clientName": "Acme Corp" }, "suggestedLawyers": [{ "id": "…", "name": "Jane Smith" }] }
```

Every matter-scoped route enforces access control — see below.

See [`CLAUDE.md`](CLAUDE.md) for the full architecture guide, agent roster, and extension points
(adding agents, templates, and Lavern configs).

---

## Audit trail

Every significant event is recorded in an **append-only, SHA-256 hash-chained JSONL** file — tamper-evident by construction. The in-memory buffer is restored from disk on restart so the live panel always shows history, not just new events.

### What gets logged

| Event category | Events recorded |
|---|---|
| **Task lifecycle** | `task.created`, `task.started`, `task.complete`, `task.failed`, `task.deleted` |
| **Lawyer assignment** | `task.assigned` — carries the assigning partner's profileId, plus added/removed lawyer delta |
| **DyTopo rounds** | `round.start`, `round.complete` — includes agent roster, finding count, phase |
| **Agent activity** | `agent.processing`, `agent.complete` — agentId, tier, domain, round, duration |
| **Findings** | `finding.produced` — findingId, confidence, content preview, attributed to responsible lawyer |
| **Tool calls** | `tool.call`, `tool.result` — **actorId = the responsible lawyer** (not "system"); category field distinguishes `external_connector` (Westlaw, CourtListener, Clio…) from `internal` tools |
| **Protocol** | `citation.gate`, `debate.start`, `debate.resolved`, `verification.start`, `verification.complete` |
| **Human gates** | `gate.created`, `gate.approved`, `gate.rejected` — with reviewer's profileId |
| **Documents** | `document.ingested`, `document.uploaded`, `document.searched` — actor, query, result count |
| **Access control** | `access.denied` on every 403 (method, URL, actor); `auth.session.expired` on every 401 |
| **Authentication** | `auth.login`, `auth.logout`, `auth.failed` — provider, role |
| **Billable time** | `time.opened`, `time.closed` — entryId, matter, billing units, attributed to lawyer or agent |
| **Profiles & clients** | `profile.created/updated/deleted`, `client.created/updated/deleted`, `matter.added/removed` |
| **OCG compliance** | `ocg.violation`, `ocg.outcome` |
| **Security** | `security.ssrf_blocked`, `security.rate_limited` |

### Key design for legal defensibility

**External system access is attributed to the responsible lawyer**, not "system". When BigLaw calls Westlaw, CourtListener, Clio, or any of the 32 connectors on behalf of a task, the `actorId` on the `tool.call` entry is the lawyer who submitted (or was assigned to) that matter. A court question of the form *"did Sarah Chen access Westlaw on Thursday?"* can be answered directly from the JSONL.

**Assignment changes are delta-logged**: `task.assigned` records both the final lawyer list and the `added`/`removed` diff, and carries the partner's profileId as actor so the audit trail shows *who* changed the assignment.

### Querying

```
GET /audit                        all recent entries (access-filtered; partner sees all)
GET /audit?taskId=<id>            entries for a specific matter
GET /audit/stream                 live SSE stream of new events
```

Entries also forward to **OpenSearch**, **Splunk**, or a **custom webhook** — set `AUDIT_OPENSEARCH_URL`, `AUDIT_SPLUNK_HEC_URL`, or `AUDIT_WEBHOOK_URL` to activate.

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

## Lawyer voice fingerprinting

Drafting agents and the final Opus synthesis call use the **assigned lawyer's writing style** —
so work product reads as if the lawyer wrote it themselves, not as generic AI output.

**How it works:**

1. Partner or lawyer uploads writing samples to `POST /profiles/:id/tone/import` or clicks
   **Voice** in Admin › Users — the UI shows a polished modal with drag-and-drop and a live
   profile display once the voice is built
2. Any of the following file types are accepted:
   - **LinkedIn ZIP** (or extracted `Shares.csv` / `Posts and Articles.csv`) — detected automatically
   - **DOCX** — paragraphs extracted from `word/document.xml`
   - **PDF** — PyMuPDF text extraction, split into paragraphs
   - **CSV** — scores columns by average text length; uses the richest column (or joins all cells)
   - **Plain text / Markdown** — split on double-newlines
3. Content is sanitised (`sanitizeForHaiku` strips `FINDING:/END_FINDING` markers and other
   prompt injection vectors) before reaching any model
4. A chunked recursive MapReduce Haiku analysis runs: batches of 8 samples → prose notes → merged
   up to a single note → structured `ToneProfile`
5. The `ToneProfile` is stored on the lawyer's profile and injected into all drafting-domain agent
   system prompts and the final Opus synthesis call

**Getting a LinkedIn export:**

1. Go to <https://www.linkedin.com/mypreferences/d/download-my-data>
2. Select **Posts & Articles** → **Request archive**
3. Download the ZIP when LinkedIn emails you the link
4. Upload the ZIP (or the extracted CSV) — or just drop a DOCX, PDF, or CSV of your own writing

---

## Cost visibility

Every Anthropic and Ollama API call is recorded and persisted to `./data/costs.jsonl`.

**Pricing table (per million tokens, input / output):**

| Model | Input | Output |
|---|---|---|
| Claude Haiku 4.5 | $1 | $5 |
| Claude Sonnet 4.6 | $3 | $15 |
| Claude Opus 4.8 | $15 | $75 |

Override any entry via env: `COST_<MODEL_ID>_IN` / `COST_<MODEL_ID>_OUT` (USD per MTok).

**Local power estimate:** set `LOCAL_INFERENCE_WATTS` to your GPU's TDP
(default: 250 W for GPU, 30 W for Apple Silicon, 65 W for CPU).

**REST endpoints:**

```
GET  /cost/summary          aggregate cost across all tasks (partner only)
GET  /tasks/:id/cost        cost breakdown for a single task
GET  /profiles/:id/cost     cost attributed to a lawyer's tasks
```

---

## Security hardening

BigLaw handles legal work product, client PII, and privileged communications — so the
attack surface is treated seriously.

| Area | What's in place |
|---|---|
| **Profile data scoping** | `GET /profiles/:id` returns full PII only to partners and the profile owner; other lawyers receive display-only fields |
| **Constant-time auth** | API key comparison pads to expected length before `timingSafeEqual` so wrong-length keys don't short-circuit the comparison and leak key length |
| **Auth rate limiting** | Auth endpoints are sliding-window rate-limited to 20 req/min per IP |
| **Input caps** | `fetch_documents` capped at 20 IDs; `tabular_review` capped at 50 documents × 30 columns |
| **CSV safety** | Time-entry and table CSV exports strip `\r\n` from field values to prevent row injection |
| **SSRF protection** | All admin-configurable endpoint URLs are validated against a private/loopback blocklist at startup |
| **Path traversal** | PDF and docx tools enforce an allow-list of read roots; the plugin directory is pinned to the project root |
| **Prompt injection** | Lavern agent system prompts are sanitised with `sanitizePromptContent()` to remove rogue markers |
| **Bot signature verification** | Teams Outgoing Webhook: HMAC-SHA256 (`Authorization: HMAC <base64>`). Slack Events API: signing-secret + 5-min replay window |
| **No secrets in logs** | API keys appear only in `Authorization` headers; connector error messages are capped at 200–400 chars |
| **Signed sessions** | Session cookies are signed, httpOnly, sameSite:lax, secure on HTTPS |

---

## Lawyers, roles & access control

BigLaw is multi-user when deployed. Identity comes from **OAuth** (Google,
Microsoft, or LinkedIn); each person is a **lawyer profile** with a role:

- **partner** (admin) — sees every matter, manages the lawyer roster, assigns
  matters to lawyers, and manages clients.
- **lawyer** — sees **only** the matters they're assigned to. There is no
  inter-lawyer visibility unless a partner shares a case.

This is enforced at every matter-scoped endpoint and documented in unit tests (`npm test`).

### Lawyer profiles

Each profile stores name, email, title, role, practice areas (one or more of 15 canonical areas),
bio, and optionally a `ToneProfile` for voice fingerprinting.

### UX modes

| Mode | Accent | Who | Features |
|---|---|---|---|
| `admin` | gold | Partners (immutable) | Everything: user management, analytics, all settings, time reporting |
| `full_flavour` | scarlet | Lawyers (default) | Full law firm stack: all workflows, 32 connectors, conflict checks, time tracking |
| `lite` | amber-gold | Lawyers (partner-assigned) | Core only: submit tasks, view results, library, basic search |

### Auth setup (production)

```bash
AUTH_ENABLED=true
SESSION_SECRET=<random 32+ char secret>
PUBLIC_BASE_URL=https://api.your-host
PUBLIC_UI_URL=https://app.your-host
CORS_ORIGINS=https://app.your-host
ADMIN_EMAILS=you@firm.com

GOOGLE_CLIENT_ID=…       GOOGLE_CLIENT_SECRET=…
MICROSOFT_CLIENT_ID=…    MICROSOFT_CLIENT_SECRET=…
LINKEDIN_CLIENT_ID=…     LINKEDIN_CLIENT_SECRET=…
```

**Local dev** runs with auth OFF — a single "local partner" who sees everything. No OAuth required to develop.

📖 Full step-by-step provider registration: [`docs/AUTH_SETUP.md`](docs/AUTH_SETUP.md).

---

## Playbook cascade

BigLaw ships a four-tier playbook system that replaces Contract Express, Practical Law Standard Docs,
and any precedent library that charges per user:

```
client (3) > matter (2) > personal (1) > firm (0)
```

Client requirements win; firm defaults are the market-standard baseline. A playbook at a higher
priority level overrides the corresponding clause or preference from any lower level.

```bash
# Create a firm-level fallback playbook
POST /playbooks { "scope": "firm", "name": "Standard NDA positions", "clauses": [...] }

# Override at matter level (e.g. bespoke retention terms for M&A)
POST /playbooks { "scope": "matter", "matterNumber": "M-001", "clauses": [...] }
```

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
| `src/tools/connectors.ts` | 32 legal connector tools across 15 providers |
| `src/routing/model.ts` | Haiku / Sonnet / Opus / Ollama / local routing |
| `src/auth/` | Lawyer profiles, roles, RLS access control + OAuth login |
| `src/clients/` | Client roster, matter sub-lists, conflict-of-interest checks |
| `src/time/index.ts` | Billable time tracking — 6-min units, open/close lifecycle, CSV export |
| `src/playbook/index.ts` | Four-tier playbook cascade — firm/personal/matter/client |
| `src/citations/engine.ts` | Citation engine — CourtListener-backed KeyCite replacement |
| `src/redline/engine.ts` | Playbook-aware contract redlining |
| `src/headnotes/engine.ts` | Headnote extraction from case opinions |
| `src/precedent/generator.ts` | Precedent document generation from knowledge store + playbooks |
| `src/briefing/index.ts` | Hub-and-spoke client briefing swarm (Chalkboard pattern) |
| `src/bots/teams.ts` · `src/bots/slack.ts` | Big Michael — Teams + Slack channel agent |
| `src/integrations/graph.ts` | Microsoft Graph API — SharePoint, Teams, Exchange |
| `src/email/client.ts` | Email search — Microsoft Graph (O365) + Gmail |
| `src/services/classifier.ts` | Haiku-based practice area + client + NOSLEGAL detection |
| `src/services/toneAnalyzer.ts` | Chunked recursive Haiku tone analysis (MapReduce) |
| `src/cost/index.ts` | `CostStore`, pricing table, `calcCostUsd`, `calcWattHours` |
| `src/settings/` | Live admin settings (DyTopo depth, debate, DocuSeal, modes) |
| `src/mcp/server.ts` | MCP stdio server + Fastify REST API |
| `src/adapters/` | Plugin adapter — drop JSON in `adapters/external/` for instant integration |
| `src/secrets/index.ts` | Infisical secrets manager (bootstrap from `.env`, rest from vault) |
| `ui/` | Vite + React console |
| `tests/` | Unit tests (`npm test`) — routing, adapters, access control, path safety |
| `workflows/mikeoss/` · `src/templates/` | Workflow presets (CP checklist, credit/SHA summary, …) |

---

## License & attribution

BigLaw is distributed under the **GNU Affero General Public License v3.0** ([`LICENSE`](LICENSE)).
Because it bundles an AGPL-3.0 component, AGPL §13 applies: running a modified version as a network
service obliges you to offer the complete corresponding source to its users.

It builds on two upstreams, fully attributed in [`NOTICE`](NOTICE):

- **Lavern** ("The Shem") — agent definitions & prompts (Apache-2.0)
- **Mike** ([mikeoss.com](https://github.com/willchen96/mike)) — document generation, tabular review, tracked-change redlining (AGPL-3.0)

*"Lavern", "The Shem", and "Mike" are the marks of their respective authors, used here only for attribution.*

<div align="center"><sub>Copyright © 2026 Discover Legal</sub></div>
