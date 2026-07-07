[Docs](../index.md) › Architecture & internals › **Grounding & coverage**

# Grounding — verbatim by construction

(`internal/rag`, `internal/bm25`, `internal/agents`, `internal/writer`)

The hard problem on cheap/local models is *citation grounding* — agents tend to paraphrase
sources instead of quoting them, so citations fail mechanical verification. BigLaw solves it
structurally, never holding more than a bounded slice in any one model call:

- **Hybrid RAG retrieval** — documents are chunked by section (PageIndex), each chunk dense-embedded
  with doc2query anticipated-questions and BM25-indexed; `search_chunks` fuses dense + question + BM25
  rankings with Reciprocal Rank Fusion. Retrieval lands on the *relevant section*, not the letterhead.
- **Staged extraction** — generation and transcription are split: a lean, persona-free pass copies
  evidence verbatim and each quote is verified as a substring of its source and **locked**; a separate
  pass writes the analytical conclusion per locked quote. Evidence is grounded *by construction* —
  anything paraphrased is dropped before it can become a citation.
- **Multi-pass writer** (synthesis) — the final deliverable is written the same way: findings are
  indexed (`search_findings`), clustered into tight sections, drafted by scoped agentic sub-agents
  that each pull only their section's findings, then hierarchically stitched. No single call ever sees
  all findings — so synthesis works on an 8K-window local model that would otherwise truncate.

On `qwen2.5-7b` (local, fits an 8 GB GPU) this took verbatim-grounded citations from ~0% to **94%**,
confirmed by both the citation gate and independent re-extraction of the sources.
**Local-model note:** the agent model must *fit in VRAM* — a 14B that spills to CPU runs ~10× slower
and trips round timeouts; prefer a model whose weights + KV cache fit your GPU.

# Coverage & figure handling — completeness by construction

(`internal/rag`, `internal/orchestrator`, `internal/writer`)

Grounding makes what the agent *says* faithful; coverage makes sure it *says everything required* —
a different axis a weak model fails at by omission (it never thinks to look for an account number or a
trade count). Four mechanisms make completeness structural rather than emergent:

- **Table/exhibit-aware chunking** — spreadsheet rows (`## Sheet:` + tab-delimited) are chunked one row
  per fact, the row kept verbatim (gate-safe) with a header-enriched embedding so a bare `$7,800,000`
  becomes findable. (`internal/rag/chunk.go`)
- **At-start specifics sweep** (`internal/orchestrator`) — before the rounds, the model reads the
  matter's figure-dense passages and enumerates *entity-aware* queries (it sees the people/accounts/funds,
  so it asks for "Chao's profitable-allocation rate", "the omnibus % of volume", "the brokerage account
  number"), runs each against the exhibits, and seeds the exact figures as grounded findings — bounded and
  deduped. The whole pipeline is then *aware* the facts exist, instead of synthesis trying to query for
  facts no finding ever noticed.
- **Top-down coverage spine** — the matter's own enumerated categories (e.g. a referral's "six categories
  of violations") become *guaranteed* sections; findings map into each, so no required category vanishes
  through clustering variance. (`internal/writer`)
- **Mechanical figure attachment** — each section's grounded figures are surfaced from its mapped findings
  as a *Key figures* list, by construction — so a specific number lands even when the 7B drafter omits it
  from prose. (Transcribing numbers is exactly where a small model is unreliable; so it's not left to the
  model.)

How this cashed out on the Harvey LAB rubric — the 41/60 → 49/60 ladder and the local-model
record — lives in [Benchmarks](../benchmarks.md); the full technique-by-technique account is
the [local accuracy journey](../local-accuracy-journey.md).

Related: [Architecture overview](overview.md) · [Local inference](../deployment/local-inference.md)
