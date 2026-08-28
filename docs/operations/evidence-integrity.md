# Evidence integrity & matter isolation

*Current as of August 2026 — written after two full end-to-end matter drives (an Ontario
family separation and a New York employment dispute) were run against the platform on both
a hosted model (gpt-5.6-luna) and a free local model (qwen2.5:7b), specifically to break it.
Everything below is the system as it now ships, with the failure that motivated each layer.*

## The threat model, learned the hard way

Two failure classes showed up in real runs, and they are worse than ordinary "AI mistakes"
because both wear the costume of diligence:

1. **Fabricated evidence that looks cited.** A small local model, starved of retrievable
   text, invented verbatim-looking quotes — complete with document citations — for parties,
   incomes, and dates that exist nowhere ("Marc Tremblay's gross annual income is
   $250,000…"). 90% of that run's findings were fabricated, and the deliverable read
   confidently.
2. **Cross-matter leakage.** With two matters in the firm store, an employment memo came
   back containing another client's matrimonial asset schedule and spousal-support
   analysis — because retrieval searched the whole store, not the task's documents.

## The integrity ladder

Every finding now passes through five layers. Each exists because the layer above it was
observed failing alone.

| Layer | Mechanism | What it stops |
|---|---|---|
| 1. Matter scope | Every retrieval tool (`search_chunks`, `extract_specifics`, `get_outline`, `read_section`, `search_knowledge`, `read_document`, `find_in_document`, `list_documents`) is confined to the task's `documentIds`, threaded through `ToolContext.DocumentIDs` from the engine and every machinery call site. Out-of-scope requests return a structured refusal. | One client's record reaching another client's deliverable. |
| 2. Retrieval liveness | The RAG chunk store is rebuilt on startup (`SetOnIngest` replays for persisted documents). | The empty-index condition that starves honest models into paraphrase and weak models into fabrication. |
| 3. Citation gate | Every quote is mechanically verified verbatim against the source (with tolerant repair). Failures are flagged `unverified` per finding — fail-open by design so loose citation from a local model isn't erased. | Individual fabricated or paraphrased quotes being presented as grounded. |
| 4. Grounding-collapse detection | When the task-cumulative unverified rate crosses `GROUNDING_COLLAPSE_THRESHOLD` (default 0.5, after `GROUNDING_COLLAPSE_MIN_FINDINGS`=20), the task **fails with an explicit error** by default (`GROUNDING_COLLAPSE_ACTION=strict\|warn` to degrade instead; alert on `task.groundingAlert`). | The per-finding flag becoming the norm — wholesale fabrication shipping as a "complete" task. |
| 5. Synthesis quarantine | Findings still unverified at synthesis are excluded from the deliverable (`WRITER_UNVERIFIED=exclude`, default; floor fallback keeps a thin record with caveats rather than emit an empty memo) and an "## Evidence Note" section discloses the count. | Residual unverified findings being drafted into client-facing text without disclosure. |

Alongside the ladder: the human gate is budgeted and calibrated (`GATE_MAX_PER_TASK`=25,
ranked sampling under degenerate confidence, overflow recorded as auditable
`auto_deferred`), so reviewer attention concentrates instead of rubber-stamping.

## Measured results (same matter, same free local model)

| | Before hardening | After |
|---|---|---|
| Findings mechanically grounded | 9/87 (10%) | 306/363 (84%) |
| Fabricated parties/figures in deliverable | invented child, invented incomes | zero |
| Cross-matter content in deliverable | whole foreign-matter sections | zero (pinned by regression tests) |
| Failure mode when a model fabricates wholesale | delivered anyway | task fails with an explicit grounding-collapse error |

The hosted-model run (gpt-5.6-luna) grounded 703/730 (96%) before these changes and is the
quality bar; the ladder exists for everything below that bar.

## Model floor

**qwen2.5:14b (or equivalent) is the floor for finding-producing tiers.** A 7B model is now
a *safe* degraded mode — it can no longer fabricate silently — but it still writes weaker
analysis (it argued the employer's side of a forfeiture clause its own client should
contest). Smaller models remain appropriate for the T3 tool tier (`OLLAMA_TIERS=3`).

## Cost integrity notes

- Watt-metered (local) calls always record **$0** — a local model whose name prefix-matches
  a hosted family's rate class no longer bills phantom dollars. The meter for local runs is
  `wattHours`.
- Unpriced models are flagged `priceUnknown` per entry, counted in `unpricedCalls`, and
  warned once per process; `COST_MODEL_RATES` (JSON env) prices any model without a code
  change.

## Knobs

```bash
GROUNDING_COLLAPSE_THRESHOLD=0.5      # unverified-rate that declares collapse
GROUNDING_COLLAPSE_MIN_FINDINGS=20    # sample floor before judging
GROUNDING_COLLAPSE_ACTION=fail        # fail (default) | strict | warn
WRITER_UNVERIFIED=exclude             # exclude (default) | caveat
GATE_POLICY=calibrated                # calibrated (default) | strict
GATE_MAX_PER_TASK=25
GATE_RANKED_SAMPLE_K=10
WRITER_DEDUP=true
WRITER_DEDUP_THRESHOLD=0.5
WRITER_CLUSTER_MERGE_THRESHOLD=0.8
COST_MODEL_RATES='{"gpt-5.6-terra":{"in":3.0,"out":12.0}}'
```

## Known remaining limitations

- Numeric consistency inside a *conclusion* is not yet checked against its cited quote —
  a model can quote accurately and still mis-state the number in its analysis sentence
  (the citation gate verifies the quote, not the arithmetic of the prose around it).
- `wattHours` overcounts under agent concurrency (per-call durations × TDP are summed).
- Weak models can still argue the wrong side of a clause; the ladder guarantees the
  *record* is honest, not that the *advocacy* is sharp. The model floor is the control.
