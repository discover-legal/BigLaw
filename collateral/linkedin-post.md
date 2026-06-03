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
