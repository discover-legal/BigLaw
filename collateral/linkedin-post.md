# BigLaw — LinkedIn launch collateral

> Post scope is tracked in `/CHANGELOG.md` — everything above the most recent
> `📣 POST` marker there is the next post's material.

---

## BigLaw BigUpdate post (v1.0 release)

**BigLaw BigUpdate**

→ **BigLaw is Now Cheaper:** the whole platform — orchestrator, DyTopo engine, 131 agents, billing, playbooks, redlining, all of it — is one Go binary that runs on a 4 GB Raspberry Pi. The hardware floor for a firm's back office is now less than a billable hour.

→ **BigLaw is Now Faster:** same routes, same data, head-to-head with the old TypeScript — **6.9× on the heavy endpoint**. 125 → 864 requests/sec. 389 ms → 53 ms. And Go was handicapped in a VM while Node ran bare metal.

→ **BigLaw is Now Safer:** the conflict-of-interest graph moved behind a Unix socket — off the network entirely. Then every security fix from the old codebase was audited line-by-line and ported: prompt-injection scrubbing, SSRF lockdown, tamper-evident audit logs, signed webhooks. Parity — with tests to prove it.

→ **BigLaw is Now Smarter:** one console became a nine-room workbench — matters, library, clients, billing, budgets & deadlines, a watchtower for docket and regulatory alerts, a drafting studio, analytics, admin.

→ **BigLaw is Now Sharper:** drop in a counterparty draft and your playbook sweeps it — client > matter > personal > firm. Every clause graded against your position, deviations redlined with replacement text, and the protections that *should be there but aren't* flagged with language to paste in. Accept, dismiss, export.

→ **BigLaw is Now More Private:** audit split into a personal feed and a partner-only firm view. And the whole bench runs on your own local models — Ollama, LM Studio — on command. Flip the tiers to local and not one token leaves the building.

→ **BigLaw Now Learns:** TopoFlow rides on top of DyTopo. The fast loop wires the agents for a matter; the slow loop — AgensFlow, a contextual bandit — learns *across* matters which formations actually pay off. No training runs, no fine-tuning bill. Pure bandit math, 41 tests under the race detector.

Oh, and by the way, one more thing:

→ **Meet Remy.** The client-advocate agent from the CNTXT hackathon, now wired into BigLaw. Her brief rides along with the matter, and at every human-review gate she asks the one question nobody else does: *is this actually what the client said they wanted?* Toggle her on or off — firm-wide or per lawyer.

——

The short version: a whole firm's tooling, on hardware cheaper than a billable hour — or entirely on your own models — faster than before, hardened fix-for-fix, reviewing contracts against your own playbook, learning as it goes, with the client's voice in the room.

Tagged v1.0.0, live on main. The TypeScript original is kept at the `typescript-final` tag. Benchmarks + repro in the repo (docs/benchmarks-go-vs-ts.md) — run them yourself.

AGPL-3.0, as always. Link in comments.

#LegalAI #LegalTech #OpenSource #Golang #BigLaw

**Carousel:** go-port-00-benchmark-chart → go-port-06-drafting → go-port-07-remy-audit-trail → go-port-04-budgets-deadlines → go-port-09-remy-portal

---

## Cost chart post — AIDA · ABC · Do Likewise

**[ATTENTION]**

The tab nobody at your firm ever clicks is a $300,000 invoice.

---

**[INTEREST]**

Per lawyer. Per year. In licensing fees.

→ Westlaw + CoCounsel (Thomson Reuters): $15,000–50,000
→ Practical Law standard docs (Thomson Reuters): $10,000–20,000
→ Contract Express playbooks (Thomson Reuters): $5,000–20,000
→ LexisNexis + PSL (RELX): $8,000–25,000
→ Definely / Kira contract review: $2,000–8,000
→ iManage document management: $2,000–5,000
→ Everlaw eDiscovery: $3,000–10,000
→ Clio Insights + Grow: $1,000–3,000

Total: $46,000–141,000 per lawyer per year.

---

**[DESIRE]**

10 lawyers.

Low estimate: $460,000/year in licensing.
High estimate: $1,410,000/year in licensing.

BigLaw (the open-source one): $0 in licensing.
Plus your Anthropic API bill — roughly $100–300/month for a 10-lawyer firm.

Call it $2,400/year.

The spread is $460,000 vs $2,400. For the same research. The same redlining. The same precedents. The same matter health dashboard. The same @-mentionable agent in your Teams.

---

**[ACTION]**

```
curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash
```

That's the whole install.

---

**[ALWAYS BE CLOSING]**

Every line in that list above is a subscription you can cancel the day you run setup.sh.

Not all at once. One at a time. Start with whatever costs the most.
Run a matter through BigLaw. Compare the output. Cancel what you cancel.

The bench will still be there tomorrow. It doesn't charge a renewal fee.

---

**[DO LIKEWISE]**

A senior associate billed 2,200 hours last year. Her firm paid $80,000 in Westlaw fees for her seat. The Westlaw subscription cost more than her bonus.

BigLaw gives that back.

Built on Mike (Will Chen) and Lavern (Antti Innanen)'s shoulders. Standing on the work of people who gave theirs away too.

Take it. Use it. Tell the next solo down the hall. Tell the boutique that just lost a pitch to a firm that could afford CoCounsel.

Run the math on your firm. Run setup.sh.

Go. Do likewise.

#LegalAI #LegalTech #OpenSource #BigLaw

---

## Cost chart post (ultra-short)

The tab nobody clicks: $460,000/year for 10 lawyers.

→ Westlaw + CoCounsel: $15k–50k/seat
→ Practical Law: $10k–20k/seat
→ Contract Express: $5k–20k/seat
→ LexisNexis + PSL: $8k–25k/seat
→ Definely/Kira: $2k–8k/seat

BigLaw (the open-source one): $0/seat.

`curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash`

Go. Do likewise.

#LegalAI #OpenSource #BigLaw

---

## Launch post (primary)

I built **BigLaw** on the shoulders of two giants — **Mike** (legal document tooling) and **Lavern** (a roster of legal agents).

The name isn't irony. It's the point.

BigLaw firms spend $2M+ per year per seat on Westlaw, Practical Law, Contract Express, Clio Insights, Definely, iManage, and twenty tools nobody at a five-person firm can afford. BigLaw the platform consolidates all of it. Open source. Free. One curl command.

**Big Michael** is the agent who lives in your Teams and Slack. @-mention him in a channel: `@BigMichael briefing Acme Corp` and he launches a swarm of agents that pull from Clio, iManage, Teams, Slack, SharePoint, email, and your document store in parallel — synthesised into a single partner briefing before the call. The scattered-file problem — every matter spread across 10 mailboxes, 2 call notes, and 4 DM threads — that's what he solves.

🌉 The old line is *"I've got a bridge to sell you."* Mine's the opposite: **I have a bridge to give you — and it stands upon the shoulders of giants.** BigLaw bridges a $2M/year vendor stack into a single open-source platform that any solo or small firm can run on a $5 VPS. Free, open source, yours to cross.

(And yes: **Claude** built most of it with me. An AI building a multi-agent AI that argues with itself about the law. Turtles all the way down. 🐢)

Screenshots from a live matter 👇 What would BigLaw's 100-agent bench do to *your* hardest question?

#LegalAI #LegalTech #MultiAgent #OpenSource #BigLaw

---

## Launch post (ultra-short)

**BigLaw**: the Am Law 100 tool stack, open source, free for everyone else.

100-agent bench that argues, cites, and verifies itself. @BigMichael lives in your Teams and Slack. One command install.

Built on Mike and Lavern. Not a bridge to sell you — one to give you. 🌉

#LegalAI #MultiAgent #OpenSource

---

## Screenshot shot list

In `collateral/screenshots/` (1600×1000 @2x):

1. **03-rounds.png** — Rounds view: active agents, the Need/Offer **communication graph**, per-round findings. *Hero shot.*
2. **04-synthesis.png** — final synthesis, rendered (risk table, real citations).
3. **06-admin.png** — live **admin panel**: lawyer/non-lawyer mode, DyTopo depth, verification, DocuSeal.
4. **02-submit.png** — "Convene the bench": workflows, templates, **client/matter numbering**.
5. **05-findings.png** — findings grid with confidence, citations, review state.
6. **01-dashboard.png** — matter list + selected matter.

## Posting notes

- Carousel order: **03-rounds → 04-synthesis → 06-admin → 02-submit**.
- Best B2B/legal post times: Tue–Thu, 8–10am local.

---

## Big Michael post — channel agent launch

**Big Michael** is now in your Teams and Slack.

@-mention him in any channel. He knows your matters.

```
@BigMichael status M-2024-001
→ matter health 87/100 🟢 · 2 active tasks · 1 pending gate

@BigMichael briefing Acme Corp
→ scanning 10 sources in parallel…
→ [posts back] Acme Corp — Partner Briefing: …

@BigMichael task review the force majeure clause in the Acme MSA
→ submitted · Task ID: tsk_01abc · use @status to follow progress
```

The briefing command is the one I'm most pleased with. Law firms have a structural problem: client information is scattered across Clio, iManage, email, Teams, Slack, and whatever else people actually use. Nobody has time to pull it together before a call. Big Michael runs a hub-and-spoke swarm — 10 parallel agent spokes pulling from every connected system at once — and synthesises it into a single briefing. Twelve seconds. One @-mention.

Part of **BigLaw** — open source, AGPL-3.0.

#LegalAI #LegalTech #Teams #Slack #OpenSource

---

## Big Michael post (ultra-short)

@BigMichael briefing [client] → client intel from Clio + iManage + Teams + Slack + email + SharePoint, synthesised into a partner briefing. One @-mention.

Part of BigLaw — open source.

#LegalAI #OpenSource

---

## v0.5.0 post — The full feature drop

**BigLaw v0.5.0** is the version that makes the BigLaw tool stack argument serious.

What Am Law 100 firms pay for. What this costs: $0.

→ **Playbook-aware contract redlining** — Definely / Kira / Luminance replacement. 3-step pipeline: Haiku clause extraction → Sonnet playbook analysis → Sonnet summary. Your playbook cascade (client > matter > personal > firm) applied to every clause, with tracked-change output.

→ **Headnote extraction** — Westlaw Key Numbers / LexisNexis headnote replacement. Sonnet extracts ratios, obiter, and procedural posture. Compounds over time as every processed opinion enriches the store.

→ **Precedent generation** — Practical Law Standard Docs / PSL replacement. Haiku structures the document; Opus drafts from your firm's knowledge store and playbook cascade. Your positions, your style, your knowledge — not generic market standard.

→ **Four-tier playbook cascade** — Contract Express / Practical Law replacement. `client (3) > matter (2) > personal (1) > firm (0)`. Client requirements win. Firm defaults are the market floor. Override at any level.

→ **Big Michael** in Teams and Slack — channel agent with hub-and-spoke client briefing swarm. 10 parallel spokes (Clio, iManage, Teams, Slack, SharePoint, Drive, email), 12 s each, `Promise.allSettled`. One @-mention for a full partner briefing.

→ **Hash-chained audit trail** — every agent call, every tool use, every gate decision, every access denial. External system access attributed to the responsible lawyer by name. OpenSearch / Splunk / webhook forwarding.

→ **Court deadline calculator** — FRCP, UK CPR, EU Competition. Trigger date in, every downstream deadline out, cited.

→ **One-liner install**: `curl -fsSL https://raw.githubusercontent.com/discover-legal/BigLaw/main/setup.sh | bash`

Still on Mike (Will Chen) and Lavern (Antti Innanen)'s shoulders. Still AGPL-3.0. Still turtles all the way down. 🐢

Which vendor contract does BigLaw make awkward for you first?

#LegalAI #LegalTech #OpenSource #LegalBilling #OCG #BigLaw

---

## v0.5.0 post (ultra-short)

BigLaw v0.5.0:

→ Playbook-aware contract redlining (kills Definely/Kira)  
→ Headnote extraction (kills Westlaw Key Numbers)  
→ Precedent generation from your knowledge store (kills Practical Law Standard Docs)  
→ Four-tier playbook cascade: client > matter > personal > firm  
→ @BigMichael in Teams + Slack — client briefing swarm, 10 parallel sources  
→ Hash-chained audit trail with lawyer attribution  
→ One-liner install

AGPL-3.0. Turtles all the way down. 🐢

#LegalAI #OpenSource #BigLaw

---

## Clio post — practice management integration

BigLaw now connects to **Clio**.

One OAuth flow. Four data regions (US, EU, Canada, Australia). Then:

→ Import a matter and its documents — BigLaw fetches the files, extracts text, classifies practice area, and kicks off a full bench run automatically  
→ 7 agent tools: list/get matters, list/download documents, create time entries, post notes, list contacts  
→ Time-entry sync: push BigLaw's billable time back to Clio activities, rounded to 6-minute billing units  
→ Big Michael's briefing swarm pulls Clio intel into every client briefing automatically

Gated on `CLIO_CLIENT_ID`. Unconfigured, the tools register but sit quietly off.

AGPL-3.0.

#LegalAI #LegalTech #Clio #PracticeManagement #OpenSource

---

## Open & private post — security/privacy + multimodal (open-stack drop)

Most legal AI asks you to trust a promise: *"we won't look at your client data."*

BigLaw enforces it in the database.

**Privacy that a bug can't leak**
→ **Row-level security at the database, default-deny** — a lawyer who isn't on a matter sees *zero rows*, enforced by Postgres itself (`FORCE` RLS), not just by app code. Two independent layers between a user and another client's file. A single app-layer bug can't open the door.
→ **Runs fully local / air-gapped** — pure-Go SQLite + local inference (Ollama / LM Studio). Privileged client data never has to leave your hardware. Single static binary, runs on a Raspberry Pi.
→ **No forced cloud, no vendor lock-in.** Open model stack (Qwen / GLM / Kimi / any OpenAI-compatible endpoint). Open storage (disk / WebDAV / Supabase / OCI registry). Open standards top to bottom. AGPL-3.0.

**Data sovereignty, written into the build**
→ A **self-imposed vendor breaker**: the platform *refuses to start* if it's wired directly to a gated, high-risk closed vendor's API — and the **build fails** if a gated SDK is ever quietly re-added. Your supply chain can't drift back to a vendor you've sworn off. No Amazon. No forced Anthropic.

**Omnimodal — because legal work is mainly text, but not only**
→ Drop in a **PDF, a Word doc, a scan, or a photo**. Hybrid extraction keeps the embedded text **verbatim** (no LLM silently paraphrasing a clause) and uses a vision model to reconcile scans, tables, stamps, and signatures.
→ **Place images, not just read them** — uploaded exhibits are retained and embedded into generated PDFs.

Open. Free. Secure. Private. Opinionated about all four.

It concentrates on the projects and vendors that share those values — and actively penalises the closed ones that make ecosystem-harming moves, however popular they are. 🐢

#LegalAI #LegalTech #Privacy #Security #OpenSource #DataSovereignty #RLS #SelfHosted #BigLaw
