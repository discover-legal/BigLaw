[Docs](index.md) › Get started › **Why BigLaw**

# Why BigLaw — the cost chart

> The tab in your browser you never click is a $300,000 invoice.

Am Law 100 firms don't publish what they spend on legal tech. Let's do the math for them.

## Per lawyer, per year

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

## The math by firm size

| Firm size | Annual tool stack (low) | Annual tool stack (high) | BigLaw cost | Year-1 savings |
|---|---|---|---|---|
| Solo | $50,000 | $152,000 | **$0** | $50k–152k |
| 5 lawyers | $250,000 | $760,000 | **$0** | $250k–760k |
| 10 lawyers | $500,000 | $1,520,000 | **$0** | $500k–1.5M |
| 25 lawyers | $1,250,000 | $3,800,000 | **$0** | $1.25M–3.8M |
| 50 lawyers | $2,500,000 | $7,600,000 | **$0** | $2.5M–7.6M |

**What you actually pay to run BigLaw:** your model-provider API bill (Qwen/DashScope by default).
At typical usage (10 lawyers, moderate workload): ~$100–300/month — call it **$2,400/year**.

That's the spread: $500,000/year vs $2,400/year for the same capability.

## Always be closing

Every tool in the table above is a subscription you can cancel the day you run setup.sh.

Not all at once. One at a time. Start with whatever costs the most.
Run the matter through BigLaw. Compare the output. Keep what you cancel.

```bash
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

## Do likewise

A senior associate billed 2,200 hours last year. Her firm paid $80,000 in Westlaw fees
for her seat. The Westlaw subscription cost more than her bonus.

BigLaw gives it back.

Take it. Use it. Tell the next solo down the hall.
Run the math on your firm. Run setup.sh.

**Go. Do likewise.**

---

# Why it's different

| Most legal AI | BigLaw |
|---|---|
| One model, one pass | 100+ agents across 4 tiers, multiple DyTopo rounds |
| "Trust me" answers | Every finding survives **adversarial debate** + **verification passes** before output |
| Hallucinated cites | **CitationGate** rejects any claim whose source isn't in the registry |
| Locked to one jurisdiction | **Jurisdiction-neutral** native bench — applies the governing law each matter specifies |
| Black box | Court-ready **audit trail** — every agent invocation, tool call (with the lawyer's identity), finding, gate decision, and document ingest — hash-chained JSONL + live SSE |
| Text in, text out | Cited briefs, **.docx** with tracked changes, e-signed via DocuSeal |
| Cloud-only | 3-tier cloud routing **or** fully local (Ollama / LM Studio / vLLM) |
| Static agent pool | **Q-learning recruitment** — agents that produce high-confidence findings are promoted; weak ones deprioritised over time |
| Siloed per-round context | **Intra-round whiteboard** broadcast to all agents + **Haiku-synthesised inter-round rollup** carried forward |
| One-size config | **Admin panel** — lawyer/non-lawyer mode, DyTopo depth, verification & DocuSeal, applied live |
| Generic document store | Documents auto-classified by **practice area** with matching lawyers surfaced on ingest |
| No billing integration | Automatic **6-minute billable time units** tracked per lawyer, per matter, exportable as CSV |
| Generic output voice | Per-lawyer **voice fingerprinting** from LinkedIn posts, DOCX, PDF, or CSV — drafting agents mirror the assigned lawyer's style |
| Black-box costs | **Per-call cost tracking** with prompt-cache-aware pricing, local power estimates, and an admin cost dashboard |
| Manual setup | **One-command setup** — one curl, checks prereqs, seeds `.env`, brings up the Docker stack, done |
| No deadline tracking | **Court deadline calculator** — FRCP, UK CPR, EU Competition rules; calendar vs business days, cited |
| Info scattered across systems | **Big Michael hub-and-spoke briefing swarm** — pulls from Clio, iManage, Slack, Teams, Drive, SharePoint, email in parallel |
| Redline ping-pong by hand | **Counter-redline loop** — opposing counsel's tracked changes parsed, judged clause-by-clause against the playbook cascade, answered with countered redlines + per-change rationale cards |
| No negotiation memory | **Redtime** — per-clause timelines across negotiation rounds with silent-edit detection and playbook drift; the judge remembers prior rounds and escalates standoffs |
| Inbound documents taken at face value | **Integrity Check** — Unicode-obfuscation scan (homoglyphs, zero-width, bidi) + unmarked-change detection on every ingest |
| Unverifiable extraction grids | **Citation verification ladder** — every tabular-review citation checked exact → tolerant → paraphrase judge → ensemble, with method + confidence per citation and a verified tally on the export |

Related: [Getting started](getting-started.md) · [Architecture overview](architecture/overview.md) · [Benchmarks](benchmarks.md)
