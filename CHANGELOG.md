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

## [Unreleased] — Go port, workbench UI, Remy integration

Everything on the `claude/low-end-hardware-port` branch. This is the scope of the
next post.

> ⚠ **Open follow-up — security-fix parity audit.** Main's PR #17/#18 security
> sweeps (prompt-injection guards, SSRF, webhook HMAC raw-body verification,
> zip-bomb budget, audit-chain verify, LEDES fixes, per-agent round timeout,
> O(n) Q-learning attribution) were applied to the TypeScript code *after* the
> Go port forked. The Go branch had its own review pass (auth hardening,
> persist races) but has **not** been line-audited against those specific TS
> fixes. The fixed TS is preserved at the `typescript-final` tag — audit
> `internal/` against commits `3428a26`, `6ccd9a5`, `f9f5bad`, `bfc0473`
> before calling the Go side production-grade.

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
