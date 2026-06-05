# Big Michael — LinkedIn launch collateral

## Post (primary)

I built **Big Michael** on the shoulders of two giants — **Mike** (legal document tooling) and **Lavern** (a roster of legal agents).

It's a *bench*, not a chatbot: 118 specialist agents that self-organize each round, argue with each other, and must cite their sources and survive an adversarial verification pass before anything reaches a human. Jurisdiction-neutral. Local or cloud. Open source.

🌉 The old line is *"I've got a bridge to sell you."* Mine's the opposite: **I have a bridge to give you — and it stands upon the shoulders of giants.** Big Michael bridges separate legal AI systems (Mike, Lavern, your own) into one bench that debates and verifies as a whole. Free, open source, yours to cross.

(And yes: **Claude** built most of it with me. An AI building a multi-agent AI that argues with itself about the law. Turtles all the way down. 🐢)

Screenshots from a live matter 👇 What would a 100-agent bench do to *your* hardest question?

#LegalAI #LegalTech #MultiAgent #OpenSource

---

## Post (ultra-short)

Built on the shoulders of giants — **Mike** and **Lavern** — **Big Michael** is a legal AI that's a *bench*, not a chatbot: 118 agents that argue, cite, and verify each other before anything reaches a human.

Not a bridge to *sell* you — one to *give* you. It unifies separate legal AI systems into one. Open source, yours to cross. 🌉

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
- Synthesis text is a representative example; the UI, communication graph, gates, and routing are exactly as the system produced them.
- Crop wide shots to 1200×627 if LinkedIn compresses them.
- Best B2B/legal post times: Tue–Thu, 8–10am local.

---

## v0.4.0 post — Voice + Cost visibility

Big Michael v0.4.0 is out.

Two new things I'm pleased with:

**Voice fingerprinting, generalized.** The tone import now accepts LinkedIn exports, Word docs, PDFs, CSVs, or plain text. Drop a brief you've written, or a memo, or a decade of LinkedIn posts — same analysis pipeline, same result: the bench learns how *you* write, and drafts that way. A new Admin › Voice UI shows the live waveform and traits, drag-and-drop import, and an animated equalizer while analysis runs.

**Cost visibility.** Every API call is tracked with cache-aware pricing (Anthropic's three token buckets at 1×, 1.25×, and 0.10× of input rate). The Admin › Cost dashboard shows stat cards, stacked token breakdown, cost-by-model and cost-by-context bar charts, and a per-model detail table — all in SVG, no chart library. Local inference records estimated watt-hours from GPU TDP. Partners can drill into cost per task or per lawyer.

Still open source. Still on AGPL-3.0. Still turtles all the way down. 🐢

#LegalAI #LegalTech #OpenSource

---

## v0.4.0 post (ultra-short)

Big Michael v0.4.0:

→ Voice fingerprinting now accepts Word docs, PDFs, CSVs, or plain text — not just LinkedIn  
→ Admin cost dashboard: cache-aware pricing, token breakdown, cost by model/context  
→ New voice UI: drag-and-drop, animated equalizer, waveform per lawyer

Open source, AGPL-3.0.

#LegalAI #OpenSource

---

## v0.4.x post — Clio integration

Big Michael now connects to **Clio**.

One OAuth flow. Four data regions (US, EU, Canada, Australia). Then:

→ Import a matter and its documents in one call — Big Michael fetches the files, extracts text, classifies practice area, and kicks off a full 118-agent bench run automatically  
→ 7 agent tools: list/get matters, list/download documents, create time entries, post notes, list contacts  
→ Time-entry sync: push Big Michael's billable time back to Clio activities, rounded to 6-minute billing units

The integration is gated: **nothing activates until you set `CLIO_CLIENT_ID`**. Unconfigured, the tools still register — they return a structured `{ error }` and never crash the server.

Security notes worth saying out loud: the region base URLs are a hard-coded four-entry allowlist, not a user-configurable string — a malformed `CLIO_REGION` env var throws on startup rather than making a request to an arbitrary host. Tokens persist locally to `./data/clio-auth.json`, auto-refresh 60 s before expiry, and are wiped on disconnect.

Still AGPL-3.0. Still turtles all the way down.

#LegalAI #LegalTech #Clio #PracticeManagement #OpenSource

---

## v0.4.x post — Clio (ultra-short)

Big Michael v0.4.x: **Clio integration** is live.

One OAuth flow → import matters + docs → bench run → push time entries back.  
SSRF-safe region routing. Auto-refresh tokens. Gated on `CLIO_CLIENT_ID` — no config, no activation.

Open source, AGPL-3.0.

#LegalAI #Clio #OpenSource

---

## Quick — Clio time sync idempotency

Small one, but it matters in practice:

Big Michael's Clio time-entry sync is now idempotent. Hit the endpoint twice — the second call is a no-op. Every entry gets stamped with a `clioSyncedAt` timestamp the moment it lands in Clio; subsequent syncs skip it. The response tells you `{ synced, skipped, errors }` so you always know the state.

CSV exports now include the `clioSyncedAt` column. Audit trail in a spreadsheet, no guessing.

#LegalTech #Clio #OpenSource

---

## v0.5.0 post — The billing + audit drop

Big Michael v0.5.0. The billing stack and the audit log landed together. That's what this is.

The billing stack:

→ **LEDES 1998B export with UTBMS task codes** — auto-classified by Haiku, editable before submission. Real legal billing format, not a CSV with delusions.
→ **OCG enforcement.** Outside Counsel Guidelines as a structured rule dictionary. Every time entry gets a compliance pass before it touches a pre-bill. Haiku extracts the mechanical parameters; the rules fire deterministically. No vibes.
→ **Pre-bill review workflow** — draft, review, approve, invoice. The whole cycle. Partners see it; associates see their part.
→ **Matter budget tracking with real-time burn alerts over SSE.** You set a budget. You get warned before it's a problem.
→ **Budget predictor** — pure statistical forecasting from your own historical time data. If this matter looks like those matters, here's the range. No black-box ML.
→ **AI work time accrues as billable entries automatically.** The agents work. The clock runs. You review it like any other time entry.

The audit log:

**Hash-chained, tamper-evident JSONL** with pluggable sinks — OpenSearch, Splunk, or a webhook of your choice. Every agent message, every round, every gate decision, every human approval or rejection. The chain makes the record verifiable. The sinks make it yours to keep, not ours.

The operational layer:

→ CourtListener docket monitoring — new filings auto-ingested, SSE alert pushed to counsel
→ Regulatory pulse — scans for new regulatory updates against your open matters on a schedule
→ Automated client status reports — generated from actual round history, not from memory
→ TypeDB multi-hop conflict graph — n-ary conflict detection, not just a list match
→ Court deadline calculator — FRCP / UK CPR / EU Competition; trigger date → every downstream deadline, cited
→ Twenty CRM integration
→ One-liner install: `curl -fsSL https://raw.githubusercontent.com/discover-legal/big-michael/main/setup.sh | bash`

Still on Mike (Will Chen) and Lavern (Antti Innanen)'s shoulders. Still AGPL-3.0. Still turtles all the way down. 🐢

What part of your practice does this touch first?

#LegalAI #LegalTech #OpenSource #LegalBilling #OCG

---

## v0.5.0 post — The billing + audit drop (ultra-short)

Big Michael v0.5.0:

→ LEDES 1998B + UTBMS auto-classification + OCG enforcement on every entry  
→ Pre-bill review cycle — draft, review, approve, invoice  
→ Matter budgets with real-time burn alerts; statistical cost predictor from your own data  
→ AI work time → billable entries, automatically  
→ Hash-chained tamper-evident audit log with OpenSearch / Splunk / webhook sinks  
→ Docket monitoring, regulatory pulse, client status reports, TypeDB conflict graph  
→ One-liner install now

Still turtles. 🐢

#LegalAI #LegalTech #OpenSource

---

## v0.4.x post — Full feature set

Big Michael v0.4.x. Things I'm actually pleased with:

→ **Every finding is debated, then verified 10 times, then a human can reject it** before synthesis reaches you. Not a chatbot with a legal prompt. A bench that argues with itself.

→ **Append-only audit log.** Every agent message, every round, every gate, every human decision — structured JSONL with a live SSE stream. You can read the entire bench's reasoning afterwards. No black box.

→ **Billable time tracked automatically.** 6-minute billing units, per lawyer, per matter. Push entries to Clio. Export to CSV. It runs in the background — you don't configure it, it just happens.

→ **Per-call cost tracking.** Cache-aware pricing across Haiku / Sonnet / Opus (Anthropic's three token buckets at 1×, 1.25×, and 0.10× of input rate). Local inference gets watt-hour estimates from your GPU's TDP. Admin dashboard breaks it down by model, context, and matter. You always know what you're spending and on what.

→ **Voice fingerprinting.** Drop a LinkedIn export, a brief, a decade of memos. The bench learns how your lawyer writes and the drafting agents do the same.

→ **32 connectors across 15 providers** — Westlaw, Everlaw, iManage, DocuSign CLM, Ironclad, Trellis, Clio, CourtListener (free, always). A checkbox picker wires up the ones you have. Unconfigured ones sit quietly off and never crash anything.

→ **Court deadline calculator.** Trigger date → every FRCP / UK CPR / EU Competition deadline, calendar vs business days, with procedural citations. (Shipped marked SAMPLE — AI GENERATED until a practitioner with standing verifies them and submits a PR.)

→ **One command to set it all up.** `curl -fsSL https://raw.githubusercontent.com/discover-legal/big-michael/main/setup.sh | bash`

Still on Mike (Will Chen) and Lavern (Antti Innanen)'s shoulders. Still AGPL-3.0. Still turtles all the way down. 🐢

What would a self-auditing bench do to your hardest matter?

#LegalAI #LegalTech #OpenSource #MultiAgent

---

## v0.4.x post — Full feature set (ultra-short)

Big Michael v0.4.x:

→ Debate + 10-pass verification before any finding reaches a human  
→ Append-only audit log — full JSONL + live SSE of every agent message and gate  
→ Automatic billable time in 6-minute units, per lawyer per matter, syncs to Clio  
→ Per-call cost tracking with cache-aware pricing and admin dashboard  
→ Voice fingerprinting — LinkedIn export, brief, or memo → bench drafts in that lawyer's voice  
→ 32 connectors, checkbox picker, unconfigured ones sit quietly off  
→ Court deadline calculator — FRCP, UK CPR, EU Competition, cited  
→ `curl -fsSL .../setup.sh | bash` — that's the whole install

Still turtles all the way down. 🐢

#LegalAI #OpenSource
