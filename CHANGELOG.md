# Changelog

This changelog is keyed to **collateral drops** (posts), not releases. Every published
post gets a `📣 POST` marker entry recording exactly what it covered and which assets
back it. When drafting the next post: **everything above the most recent 📣 POST marker
is new material** — that's the post's scope, no archaeology required.

House rules:
- New work lands under `[Unreleased]` as it merges, grouped by area.
- When a post ships, retitle the block to a `📣 POST` marker (date + post title),
  list its collateral (screenshots, charts, docs), and start a fresh `[Unreleased]`.
- Collateral lives in `collateral/` (post copy in `linkedin-post.md`, assets in
  `screenshots/`); supporting writeups in `docs/`. Name assets with a per-drop prefix
  (e.g. `go-port-*`) so they tie back to their entry.

---

## [Unreleased]

### Relicensed: AGPL-3.0 → Apache-2.0
A clean-room reimplementation (spec: `docs/clean-room-spec-document-tools.md`) replaced
every Mike-derived component — the five document-production/tabular tools and the built-in
workflow templates — with independently authored native Go, removing the last copyleft
dependency. With all copyright held by Discover Legal, the project is now **Apache-2.0**:
express patent grant, NOTICE-based attribution, no network-service source obligation.
LICENSE, NOTICE, SPDX headers (271 files), badges, and image labels all updated.

### Negotiation intelligence
- **Counter-redline loop** — `respond_to_redline`: parses opposing counsel's tracked
  changes (cross-run, substitution merging), judges each against the four-tier playbook
  cascade, and writes a `.response.docx` with countered redlines + per-change rationale
  cards; failed judgments degrade to `review`, never abort
- **Redtime** — document lineage across negotiation rounds (`document_versions` on the
  sqlite/postgres/memory store seam), tracked-change attribution + silent-edit detection
  via in-house word-diff, per-clause timelines with playbook drift
  (`get_redline_timeline`, `GET /documents/:id/timeline`)
- **Judge memory** — the negotiate judge receives per-clause negotiation history
  (token-budgeted, last 3 rounds) with standoff escalation to playbook fallback or
  lawyer review (`escalation` on decision cards)
- **Integrity Check** — Unicode obfuscation scan (homoglyphs, zero-width, bidi) +
  unmarked-change detection on inbound documents (`check_document_integrity`, wired
  into `respond_to_redline` and knowledge ingest)

### Tabular review, industrialized
- Reviews persist via the store seam (sqlite/postgres/memory; RLS on postgres) and
  reload across restarts; landscape `.docx` matrix export; `GET /reviews/:id` +
  `/reviews/:id/table.csv`; 10-call extraction concurrency cap
- **Citation verification ladder** — every `[[page:N||quote]]` cell citation verified
  at extraction time: exact → tolerant → paraphrase judge → 3-vote ensemble, with
  method + confidence per citation and a matrix-level verified tally (docx stamp)

### Workbench & demo
- **Reviews UI** — RAG-colored due-diligence grid with verification-state citation
  pills; pill click opens the source document with the quote highlighted; Redtime
  timeline view (rounds × clauses, silent-edit warnings, decision + drift badges)
- **`biglaw demo`** — one-command end-to-end tour: seed → tabular review → CP
  checklist → counter-redline with rationale cards (verified live at ~$0.03)
- Fixes: cost ledger now persists on fresh installs; temperature override dropped
  for OpenAI gpt-5.x/o-series (was silently degrading tabular cells to grey)

### Benchmark: cross-tier + raw-baseline runs on the release build (forensics-corrected)
Same task, same claude-sonnet-4-6 judge. Release pipeline: claude-haiku-4-5 **34/60**
(18.4 min), claude-sonnet-4-6 **34/60** (~10× the cost — $13.70 vs $1.34 — the same *number*
of criteria, 34, 31 of them the same ones; the pipeline, not the model tier, is the binding
constraint). Controls: raw claude-haiku-4-5 in
Harvey's own harness **41/60** (5 min, and it skipped a document); prior best pipeline
result **37/60** (Haiku, June-26 build) — the release build carries a −3 regression under
investigation. The local qwen2.5:14b release run (27/60) was invalidated by forensics:
every DyTopo round timed out under three-way task contention, so the score reflects the
BELO deviation layer alone. Verified against the full scores.json history: the earlier "30/60"
was the union of passed criteria across two qwen runs (a coverage measure, not a
single-run score); best single-run qwen result is 28. Root cause of
the raw-vs-pipeline gap (criterion-level autopsy): the extraction transcription funnel —
1500-token tool-result caps, 2-sentence passage limits, and read_document results
bypassing the evidence pool. Task still not passed; climb continues, honestly.

### Benchmark: the fix wave, proven (July 5)
Five fixes landed from the autopsy (extraction floor ca0a035, crossdoc dbce03e, defense
lenses 44c8121, writer authorship 9731d78, runtime hygiene fa2a13a). Proof runs, same
task/judge: claude-haiku-4-5 **34 → 49/60** — the pipeline now beats the raw-harness
baseline (41) by eight; local qwen2.5:14b **→ 36/60** on a clean exclusive run (new local
single-run record; prior verified peak 28). Generalization check on the compare-mode
trust task: Haiku 6 → 9/23 pre/post wave (real transfer; the deviation-tuned local record
of 12/23 stands — deviation-path port queued). Costs: Haiku proof ~$11.48 (7.9M tokens);
qwen proof local-only. Task still not passed; 11 criteria remain on the SEC task.

### BELO — an epistemic ontology, a graph-discovered spine, and What3Words figure handles
The next stretch of the local-accuracy climb — still a single local open-weight model for the
bulk (a 14B handles the small, high-leverage spine pass), no cloud model, no corpus stuffing.
- **BELO — BigLaw Epistemic Legal Ontology** (`internal/ontology/`): a three-layer RDF/OWL
  ontology every agent reads and writes — domain classes + controlled predicates with
  domain/range; a reified `Claim` carrying provenance, grounding, epistemic status, and
  Claim→Claim stance edges; analytic concepts (`requiresElement`) and derived defense issues.
  `Normalize()` canonicalises a weak model's free-text relations and **re-orients reversed
  triples via domain/range** ("Section 206 violates <conduct>" → "<conduct> violates Section
  206"), turning noisy extraction into typed facts. ("As above, so BELO.")
- **Spine discovered, not enumerated** — the deliverable's section set is now derived from the
  evidence graph's typed `Conduct` nodes (paged over the charging document, deterministic at
  temperature 0), replacing a run-to-run-varying LLM enumeration that grabbed policy
  boilerplate (Form ADV review triggers) as "allegations" and dropped real ones. Result: a
  **complete, stable spine** — all six allegation categories every run — instead of a 4↔6
  wobble that had been confounding every comparison.
- **What3Words figure handles** — drafters reference each figure by a neutral codename (the
  digit masked even in the prompt), and the exact grounded value is substituted by key. Names
  are LLM-native where the prior `{{FIG: …}}` meta-placeholder was resisted (a weak drafter
  abstracted figures into vague prose), inert for attention, and an exact key (no fuzzy
  desc-matching). Figures now land verbatim in most sections, never garbled.

Honest status: the spine is complete and figures flow into most sections; a flagship section
still under-states its figures when its handle list is noise-heavy (salient figures buried) —
the next drop ranks handles by salience. The benchmark remains a climb, not a pass.

New areas: `internal/ontology/` (BELO spec `belo.ttl` + Go schema), the evidence-graph `Claim`
store + `Conducts()`, writer figure-handle substitution, and the orchestrator's
allegation-density-ranked charging-document conduct sweep.

### Local-model accuracy: grounding → synthesis → evidence graph (0 → 30/60 on a Harvey-style benchmark)
Took a single local open-weight model from **0 to 30 of 60 rubric criteria** on a
Harvey-style LAB task (white-collar SEC enforcement-referral extraction) through a chain
of orchestration techniques — no model swap, no stuffing the corpus into one context. The
LAB rubric is task-level all-or-nothing (the 0–1 score stays 0.00 until ~every criterion
passes), so the tracked metric is **criteria-passed count**; the task is not "passed" yet,
this is the climb. Techniques, in the order they landed:
- **Verbatim grounding (≈0% → 94%)** — staged extract→analyse under a substring-lock plus
  hybrid RAG (dense + doc2query + BM25 fused by RRF). Evidence is transcribed verbatim and
  locked; conclusions are written only over the locked quotes, never paraphrased.
- **Table/exhibit-aware chunking** — one chunk per spreadsheet data row with header-paired
  embed text, so figures buried in `.xlsx` exhibits (dollar amounts, %, account numbers)
  become retrievable and extractable instead of invisible to semantic search.
- **Targeted multi-query retrieval** — a single section-title query left specific facts at
  rank 17+; per-fact queries land them in the top 8. One blunt query is the wrong key.
- **At-start specifics sweep** — entity-aware figure/citation queries hunt specifics into
  findings *before* the debate rounds, not only at synthesis (the jump to 22/60).
- **Neurosymbolic figure landing** — drafters write `{{FIG: …}}` placeholders; the exact
  grounded value is injected mechanically, so the model never types (and so can't garble,
  e.g. 81.6%→68.6%) a digit; figures left unstated are appended by construction.
- **Top-down coverage spine** — the matter's own enumerated allegations become guaranteed
  sections, so no category silently vanishes through bottom-up clustering variance.
- **Matter classification + precise recruitment** — the matter is classified from its
  documents and specialists are recruited on it (a securities matter had been staffed with
  patent analysts); one specialist per distinct allegation, off a shared, deduped
  enumeration that recruitment and the coverage spine both consume.
- **Paged writing-agent synthesis** — the deliverable is authored by the DyTopo writing
  agents over the evidence blackboard: each finished section is compacted out of working
  context and uncompacted on demand, then assembled losslessly. Replaces a compressing
  stitch that silently dropped whole allegations, and lets the deliverable exceed the
  model's context window.
- **Lite evidence graph** — grounded two-pass entity/relation extraction (entity-anchored,
  parenthetical/omission-aware) builds a per-matter graph of typed facts; facts route
  per-section so relations land with correct attribution (a "victim-of → directed-brokerage"
  edge cannot render under cherry-picking). Ungrounded edges (quote not verbatim in source)
  are dropped, never kept.

New areas: `internal/evidencegraph/` (grounded graph + two-pass extractor),
`internal/writer/paged.go` (context-paging synthesis), orchestrator shared-enumeration +
task-start graph build. Measured on a local 14B model.

Collateral: post copy in `collateral/linkedin-post.md`; technique-by-technique writeup with
the run trajectory in `docs/local-accuracy-journey.md`.

### TS→Go porting complete — feature parity with `typescript-final`
Everything previously marked "TS-only, not yet ported" is now on the Go platform:
- **Browser OAuth login** (Google / Microsoft / LinkedIn OIDC): static
  `/auth/<provider>/{login,callback}` routes, first-login provisioning
  (partner via `ADMIN_EMAILS`), stateless HMAC-signed session cookies
  (constant-time verify, jti revocation, 12 h), session accepted as an
  alternative credential to the bearer key, 20 req/min/IP auth rate limiting,
  `auth.login/logout/failed` audit events
- **Clio**: `/auth/clio/{status,connect,callback,disconnect}` connect flow
  (single-use server-side state), seven `clio_*` agent tools,
  `POST /tasks/from-clio-matter` (fetch → ingest docs → submit task),
  `POST /time-entries/sync-to-clio` (6-min units, `clioSyncedAt` idempotency);
  new ClioClient methods (GetMatter, DownloadDocument, CreateNote, ListContacts)
- **Document-production tools**: `docx_generate`,
  tracked-changes `edit_document` (order-preserving OOXML round-trip,
  4-stage anchor matching, multi-run reconstruction), `replicate_document`,
  `pdf_extract_text/_tables/_ocr/_generate` (via `scripts/pdf_tools.py`,
  path allow-list, 30 s timeout), DocuSeal tools (`_list_templates`,
  `_send_for_signing`, `_submission_status`), `tabular_review` +
  `read_table_cells` (50×30 caps, per-cell extraction with citations),
  `fetch_documents` (20-ID cap)
- **Generic tone import**: `POST /profiles/:id/tone/import` accepts LinkedIn
  ZIP/CSV, DOCX, PDF, CSV, and plain text/Markdown
  (`services.ExtractWritingSamples`); `…/tone/linkedin-import` keeps its
  legacy contract
- **Audit forwarding**: async best-effort to OpenSearch / Splunk HEC /
  custom webhook (`AUDIT_*` env vars)
- **Bot notify routes**: `POST /bots/{teams,slack}/notify` with
  explicit-target → matter-link → default resolution, partner-gated,
  SSRF-validated webhook URLs
- **Cost overrides**: `COST_{HAIKU,SONNET,OPUS}_{IN,OUT}` env vars applied to
  the pricing table by model family
- **MCP**: `list_plugins` and `get_time_entries` tools restored
- Unit tests across auth (sessions, rate limiter), tools (17 tracked-changes/
  docx/tabular tests), timekeeping (sync skip), cost (overrides), audit
  (forwarding), services (sample extraction)

### Docs
- README + docs verified against the Go platform and corrected: Docker-based
  setup.sh description (was the Node wizard), Go agent counts (131 definitions;
  architecture diagram), in-process vector search (was "RuVector native HNSW";
  registry persists to `data/agents.json`, memory/knowledge are in-memory),
  REST route map regenerated from `internal/api/` (adds playbooks, citations,
  deadlines, matters, dockets, regulatory, pre-bills/invoices/OCG, LPM, LEDES;
  `POST /redline` was listed as GET), bench tool table split into Go agent
  tools vs REST engines, verification passes routed to Haiku (was listed under
  Sonnet), deadline calculator example moved to `POST /deadlines/compute` with
  rules at `deadlines/rules/`, cost section drops unimplemented
  `COST_<MODEL>_IN/OUT` overrides, audit event table trimmed to events the Go
  log actually emits, security table updated to shipped hardening
- TS-only features now explicitly marked as preserved at `typescript-final`
  and not yet ported: browser OAuth login (banner added to
  `docs/AUTH_SETUP.md`), Clio connect flow / matter import / time sync,
  document-production tools (docx/tabular/PDF/DocuSeal), generic tone
  import (Go is LinkedIn-only), audit forwarding (OpenSearch/Splunk/webhook)
- CLAUDE.md: version block updated to 1.0.0/Go, MCP tool list matched to the
  Go server, route-list caveat added, `agents/lavern/` path fixed

---

## 📣 POST — BigUpdate: open, private, multimodal + role-aware redesign *(2026-06-15)*

### Open, private, multimodal — Qwen stack, row-level security, self-imposed vendor breaker
A reorientation of the platform around openness, data sovereignty, and omnimodal
document handling — with privacy enforced at the database, not just the app.

- **Model stack is open by default.** The default bench runs on **Qwen** over an
  OpenAI-compatible endpoint; `MODEL_STACK` selects `qwen | glm | kimi | custom`,
  and any OpenAI-compatible endpoint works (`PRIMARY_MODEL_URL`). The four tiers
  (heavy/mid/light + a **vision** tier) resolve from the active stack. Extended
  thinking is model-agnostic (token budget + optional `reasoning_effort`).
- **Self-imposed vendor breaker.** The platform concentrates support on open,
  privacy-respecting vendors and **refuses to start** if its config is coupled
  directly to a gated closed vendor's service. A dependency-level guard fails the
  build if a gated SDK is ever re-introduced. The Anthropic provider/SDK and the
  AWS SDK were removed outright; a model wrapped behind a neutral OpenAI-compatible
  gateway is still allowed — the gate keys on the *endpoint*, not the model name.
- **Omnimodal ingest.** `/documents/upload` accepts PDF (digital **and** scanned),
  Word, images, and text. Hybrid extraction keeps the embedded text layer as
  verbatim ground truth and uses a vision model to reconcile scans, tables, and
  figures; standalone images go straight to the VLM. No more 422 on a PDF.
- **Place images, not just ingest them.** Uploaded images/PDFs are **retained** as
  attachments and can be **embedded into generated PDFs** via a pure-Go engine.
- **Persistence + database row-level security.** Documents and attachments persist
  through a storage seam: pure-Go **SQLite** by default, or **Postgres** with
  **`FORCE` row-level security**, **default-deny** policies keyed on the requesting
  lawyer/partner identity — enforced *beneath* the existing app-layer checks
  (defense in depth), and proven against a live Postgres for both documents and
  attachments.
- **Open, vendor-neutral blob storage.** Attachment bytes live in a pluggable
  store: local **disk** (default), **WebDAV**, **Supabase Storage** (native API),
  or an **OCI registry** via ORAS. AWS S3 is deliberately not offered.
- **Hardened, open packaging.** OCI image-spec labels, reproducible build flags,
  fixed non-root UID, SBOM/provenance documented.

New endpoints: `GET /documents/attachments/:docId`, `/:docId/:attId`, and
`GET /documents/export/:docId`. New config: `MODEL_STACK`/`QWEN_*`,
`DB_BACKEND`/`DATABASE_URL`, `BLOB_BACKEND`/`BLOB_*`, `EXTRACT_VISION_*`,
`REASONING_EFFORT`.

### Role/mode-aware Home + answer-first drafting (Double Diamond)
A UI pass that opens each user at the right altitude instead of one fixed dashboard.

- **Role/mode-aware Home** is the new default route: partners get a firm-wide
  portfolio glance + a cross-matter "Needs your review" queue with inline
  approve/reject; lawyers get the same scoped to their assigned matters; Lite
  users get a guided three-tile intake; admins get a system-health line.
- **Answer-first drafting.** Each Drafting tool form (Playbook review, Headnotes,
  Precedents) collapses into a thin re-run bar once a result exists, so the result
  leads, not the form. Lite intake tiles deep-link to the right Library tab.

_Collateral: `collateral/linkedin-post.md` ("BigLaw BigUpdate — open, private,
multimodal" post); screenshots `open-stack-*`._

---

## 📣 POST / 🏷 v1.0.0 — The Go platform *(2026-06-11)*

The Go implementation replaces TypeScript on `main` (tag `v1.0.0`; TS preserved at
`typescript-final`). The release post is `collateral/linkedin-post.md` §"BigLaw BigUpdate".

### Security-fix parity (TS PR #17/#18 → Go)
Audited `internal/` against the TS security sweeps (`3428a26`, `6ccd9a5`,
`f9f5bad`, `bfc0473`) that landed after the Go port forked, and ported the gaps:
- **Prompt injection**: extended `SanitizePromptContent` marker set
  (CHALLENGE/RESOLUTION/DESCRIPTION/EXPECTED_OUTPUT, case-insensitive) + control
  strip; sanitized round-goal/task-description interpolations across agents,
  orchestrator (round-goal/synthesise/tabulate), and the debate resolver;
  confidence clamp; malformed-resolution now routes to a human gate
- **SSRF**: blocklist now also rejects `::`, `0.0.0.0`/`0.x`, CGNAT 100.64/10,
  IPv4-mapped IPv6 `::ffff:`, hex and bare-decimal IP hosts (+ unit tests)
- **Audit**: hash-chain verified on restore (tamper warning) (+ tests)
- **Webhooks**: confirmed Teams/Slack HMAC verifies the raw body before parse
  (already correct — no change)
- **Access control**: partner gate added to playbook read/build/resolve endpoints
- **SSRF egress**: CourtListener client refuses redirects
- **Billing**: LEDES skips zero-unit rows + UTC dates; header-aliased column
  parsing; CSV formula-injection neutralization on time + tabulate exports
- **Embeddings**: batch response length + index validation
- **DyTopo**: per-agent round timeout (`AGENT_ROUND_TIMEOUT_MS`); fixed an
  `errgroup.WithContext(nil)` panic that would crash every round
- **Conflict checks**: entity-name normalization + bidirectional matching (+ tests)
- **Redline**: verdicts bound by echoed clauseIndex (unmatched → escalate),
  not array position
- N/A (no ported defect): memory delete (single-pass, no page cap), invoice
  violation allow-list (Go filters nothing to begin with)
- Deferred (would change response contracts, not parity): conflict check on
  POST /clients create; invoice-type allow-list as new hardening

### Go port (low-end hardware)
- Full platform port to Go targeting ARM64 / Raspberry Pi (4 GB): orchestrator,
  DyTopo engine, protocols (CitationGate/Debate/Verification), all 131 agent
  definitions, providers, routing, knowledge/memory/agent vector stores
- Subsystems ported: billing (pre-bills, invoice validation, LEDES), OCG engine,
  budgets, deadlines, dockets, regulatory pulse, reports, queue, secrets, citations,
  playbooks, redline, headnotes, precedents, briefing, bots (Teams/Slack), email,
  integrations
- Conflict graph moved to a TypeDB sidecar; Go core talks to it over a Unix domain
  socket (no TCP exposure); Docker packaging for the three-container stack
- Hardening pass: auth, persist races, graph sync retry, learning feedback

### API parity wave
- ~50 routes wired to bring the Go REST surface to near-parity with the TS backend:
  pre-bills CRUD, invoice validation, time-entry exports (CSV/JSON/LEDES), OCG
  suggestion workflow (run-check/accept/dismiss), client OCG docs, matter budgets
  (+ SSE alerts + prediction), deadlines, matter/portfolio health, dockets,
  regulatory, status reports, jobs queue, playbooks, redline, headnotes, precedents,
  citation check, client briefing, document library + upload, profile cost, tone
  import, admin settings (nested contract, SSRF guard, clamping, live overlay)
- Contract fixes: `/health`, `/me` (mode/capabilities), `{ok:true}` acks

### Workbench UI (rebrand follow-through: BigLaw is the tool, Big Michael the agent)
- Single-console app reshaped into a nine-workspace workbench: Matters, Library,
  Clients, Billing & Time, Budgets & Deadlines, Watchtower (dockets + regulatory),
  Drafting (playbooks/redline/headnotes/precedents/citations), Analytics, Admin
- ~30 new endpoints wired with loading/empty/error states; per-section error
  boundaries; SSE alert streams

### Remy (CNTXT client-advocate) integration
- Per-matter client-voice store: Remy's advocacy brief travels with the matter
- Review gates carry a client-voice note — Haiku, speaking as the client's advocate,
  assesses each gated finding against the client's stated goals
- Matter notifications from the client side fan out to linked Teams/Slack channels;
  always stored and hash-chain audited
- Toggleable: firm-wide settings (gate notes / channel fan-out) + per-lawyer hide
  preference; CNTXT side gains `notify_matter` tool + brief push on file workup

### Audit
- Personal activity rail (self-scoped, server-enforced; closable)
- Partner-only firm-wide audit browser with event/actor/task filters

### Playbook review (Spellbook-shaped rework)
- Drafting workspace restructured around the real workflow: **Playbook review**
  (apply the whole cascade to a contract) leads; **Draft** (generate from
  playbook); **Playbooks** (manage positions; cascade resolver demoted to an
  inspector)
- Review now detects **missing clauses** — playbook-expected protections absent
  from the draft, flagged with severity and model-drafted insert language
- Per-finding accept/dismiss dispositions + markdown markup export
- Fixed: playbook never engaged in Go redline (free-form clause names vs
  snake_case keys — normalized matching, unit-tested); extraction prompt
  hallucinated practice-area topics on small models; playbook clause vocabulary
  now anchors extraction labels
- Local inference: engines now honor LOCAL_INFERENCE_TIERS=all routing;
  OpenAI-compat base URL /v1 normalization; container reaches host Ollama via
  host.docker.internal; lenient JSON-repair parse layer for small-model output;
  Infisical secrets loader wired into Go startup (was ported but never called)

### TopoFlow / AgensFlow (parallel branch: claude/lpm-functionality-plan)
- Two-level coordination substrate over DyTopo: fast within-trajectory graph
  induction + AgensFlow, a slow cross-trajectory UCB1 contextual bandit that
  learns which skills, model bindings, and topologies pay off (tabular stats,
  frozen encoder, no neural training)
- Python implementation (M1–M9, 44 tests) then reimplemented natively in Go
  (`biglaw-go/internal/topoflow`, 41 tests under -race)
- ⚠ Not yet merged into the go-port branch — merge before shipping the post's
  claims in a release

### Benchmarks
- Go vs TS, identical routes/data, autocannon 50×10s: 1.25× (`/health`),
  3.8× (`/templates`, 33 KB), 6.9× (`/agents`, 850 KB; p50 389 ms → 53 ms) —
  Go measured inside Docker Desktop VM, Node native. Methodology + repro:
  `docs/benchmarks-go-vs-ts.md`

**Collateral:** `collateral/screenshots/go-port-00-benchmark-chart.png` …
`go-port-09-remy-portal.png` (workbench, clients, billing, budgets/deadlines,
watchtower, drafting, Remy audit trail, Remy toggles, Remy portal);
`docs/benchmarks-go-vs-ts.md`; post draft in `collateral/linkedin-post.md`
§ "Go port changelog post".

---

## 📣 POST — Rebrand: Big Michael → BigLaw *(most recent published post)*

Everything up to and including the rebrand. Covered: the rebrand itself (platform =
BigLaw, Big Michael = the channel agent), connector fold-in, the Claude for Legal
agent roster (70 agents joining the 58 native, 128+ total), file investigation
agents, and the v0.5.0 feature set (playbook-aware redlining, headnote extraction,
precedent generation, four-tier playbook cascade, Big Michael in Teams/Slack with
the briefing swarm, Clio integration, hash-chained audit, deadline calculator).

**Collateral:** `collateral/screenshots/new-*.png` and `0*.png`;
`collateral/linkedin-post.md` (launch, v0.5.0, Big Michael, Clio, cost-chart
sections).
