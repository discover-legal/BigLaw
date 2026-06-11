# TopoFlow (Go)

A two-level coordination substrate for LLM multi-agent systems, implementing the
TopoFlow spec (AgensFlow `[AF]` + DyTopo `[DT]` with `[NEW]` integration glue),
**natively in Go** to match the rest of `biglaw-go`.

## Provenance — the two source papers

TopoFlow composes two independent published frameworks. Tags throughout the
code (`[AF]`, `[DT]`, `[NEW]`) mark which paper a given construct comes from.

| Tag | Paper | What TopoFlow takes from it |
|---|---|---|
| `[AF]` | **AgensFlow: A Coordination-Policy Substrate for Multi-Agent Systems** — N. Koenigstein, May 2026. | The slow-level substrate: folded task signatures, the legal action space (`invoke`/`skip:X`/`terminate`), the hybrid reward, reliability-aware annealed UCB1, heuristic belief deltas, and the cross-judge auditable RelativeJudge. |
| `[DT]` | **DyTopo: Dynamic Topology Routing for Multi-Agent Reasoning via Semantic Matching** — Y. Lu, Y. Hu, X. Zhao, J. Cao, Feb 2026 (`arXiv:2602.06039`). | The fast-level `DyTopoGenerator`: per-round semantic Need/Offer (query/key) matching, sparse directed-graph induction, the synchronization barrier, and topology-aware message ordering. |
| `[NEW]` | — (integration glue, this repo) | Making the topology generator a **bandit arm**, the τ/k_in/round action buckets, the whole-cell-charges-all-its-tokens reward contract, and verifiable `GroundTruthQ`. |

> `arXiv:2602.06039` is also the basis for the production `internal/dytopo`
> engine that the rest of BigLaw runs; the `topoflow.DyTopoGenerator` is the same
> algorithm re-expressed as a within-trajectory cell the AgensFlow bandit can select.

- **Slow level (cross-trajectory):** a reliability-aware UCB1 contextual bandit
  over a *folded signature* selects skills, model bindings, skips, **and which
  topology generator to run**. There is no neural training — everything "learned"
  is tabular bandit statistics; the semantic encoder is frozen.
- **Fast level (within-trajectory):** a `CompositeTopologyCell` produces the
  actual coordination structure — `LinearWithSkipGenerator` `[AF]` or
  `DyTopoGenerator` `[DT]` (per-round semantic graph induction). To the policy
  graph a whole cell is **one action / one reward**, but it charges **all** its
  inner-round tokens.

## Files (single Go package to avoid import cycles)

| Area | File |
|---|---|
| core types (§3) | `types.go` |
| config + defaults (§12) | `config.go` |
| `Fold` signature (§4) | `signature.go` |
| legal actions A(s) (§5) | `actions.go` |
| UCB1 policy graph (§6) | `policygraph.go` |
| hybrid reward + Q + RelativeJudge (§7) | `reward.go` |
| belief deltas (§10) | `beliefs.go` |
| topology ABC + linear + dytopo (§8) | `topology.go` |
| embed/cosine/threshold/order (§8.3) | `semanticmatch.go` |
| transport (Mock + Anthropic) + roles + tools (§13) | `transport.go`, `roles.go`, `tools.go` |
| macro loop (§9) | `router.go` |
| governance (§9) | `governance.go` |
| trace + RunReport (§11) | `trace.go` |
| harness (8 arms) + datasets + metrics H1–H6 (§14) | `eval.go` |
| acceptance tests M1–M9 | `*_test.go` |

## Components are real, not faked

| Component | Real implementation | Offline test double |
|---|---|---|
| LLM transport | `AnthropicTransport` over `providers.Provider` (JSON-structured) | `MockTransport` |
| encoder | `EmbeddingsAdapter` over the project `embeddings.Client` | `MockEmbedder` (bag-of-words) |
| code Q | `SubprocessCodeRunner` — runs candidate code in a real `python3` subprocess against the HumanEval `check()` harness / APPS asserts, with a per-call timeout | (none; returns 0 if no runner) |
| web search | `SearchProvider` seam | `MockSearchProvider` |
| datasets | `LoadJSONL` + baked-in real HumanEval/MATH samples | — |

The mocks exist only as offline test doubles; pass real components for a live run.

## Running

Offline tests (no network) — all milestones M1–M9, plus the **real** Python
code-execution tests:

```bash
go test ./internal/topoflow/        # add -race for the concurrency-clean check
```

Real evaluation CLI (`cmd/topoflow-eval`):

```bash
# offline: real subprocess CodeRunner + mock transport (no API key needed)
go run ./cmd/topoflow-eval -offline -dataset humaneval -epochs 1 -out report.json

# live: real Anthropic transport + embeddings + code runner (needs ANTHROPIC_API_KEY)
go run ./cmd/topoflow-eval -dataset humaneval -epochs 8 -out report.json
go run ./cmd/topoflow-eval -dataset path/to/dataset.jsonl -epochs 8

# no-skip ablation: force skip:X off across all arms (§6.2); H6 reports the delta
go run ./cmd/topoflow-eval -offline -dataset mixed -epochs 8 -no-skip
```

Embedding it directly:

```go
tx := topoflow.NewAnthropicTransport(provReg.MustGet(routing.ModelHaiku), 4000)
emb := topoflow.NewEmbeddingsAdapter(embeddings.NewClient(cfg))
report, _ := topoflow.RunSuite(topoflow.DefaultConfig(), topoflow.SuiteOptions{
    Transport: tx, Embedder: emb, CodeRunner: topoflow.NewSubprocessCodeRunner(),
    Tasks: topoflow.RealHumanEvalSample(), Epochs: 8, OutPath: "report.json",
})
```

> **Security:** `SubprocessCodeRunner` executes model-generated code. It enforces
> a timeout but has no namespace/seccomp isolation — sandbox it (container) in
> production. Tests pass trusted fixtures only.

## Design notes (degrees of freedom live in `[NEW]`)

- The signature stays small (regime + 7-bit handoff mask + 4 bucketed beliefs);
  topology knobs live in the **action space**, never the signature — arm count
  ~30, edges are never arms.
- Quality `Q` is pluggable: `GroundTruthQ` (math exact-match; code via an
  injectable `CodeRunner`, confidence 1.0) or `JudgedQ` (RelativeJudge, relative
  scoring, cross-judge averaging with disagreement-std confidence that scales the
  bandit backup as fractional visits).
- The harness emits metrics H1–H6. **H1 is the decisive falsifier:** if generator
  choice does not separate by regime (coordination-heavy C3/C7/C8 preferring
  `dytopo`, procedural C1/C6 preferring `linear`), the central claim fails — and
  that negative result is the deliverable. **H6** reproduces AgensFlow's no-skip
  ablation (below).

Defaults are starting points, not validated settings.

## No-skip ablation (`SkipEnabled`) `[AF]`

AgensFlow §6.2 evaluates a **no-skip ablation** arm — the full learning substrate
with `skip:X` *forced off* — to show that letting the policy omit cells is doing
real work, not riding along on skill/model selection. TopoFlow exposes this as a
single config flag:

```go
cfg := topoflow.DefaultConfig()
cfg.SkipEnabled = false   // remove skip:X from A(s); the no-skip ablation
```

- **Where it bites:** `legalActions()` (`actions.go`) only enumerates `skip:X`
  candidates when `cfg.SkipEnabled` is true. Nothing else in `A(s)` changes, so
  the ablation isolates exactly the skip mechanism and the run stays finishable.
- **Harness arm:** `8_no_skip_ablation` (`eval.go`) is arm `3_pure_agensflow`
  (linear, learning, skip-on) with `skipOff: true`. Comparing the two is the
  ablation.
- **Metric `H6_skip_ablation`:** reports `skip_on` vs `skip_off` mean quality and
  mean tokens, their deltas, and `skip_compresses` (skip-on reached its operating
  point at ≤ the no-skip token cost). Per the paper, `skip:X` should compress
  token cost without sacrificing quality on coordination-heavy classes.

## DyTopo generator `[DT]` — `arXiv:2602.06039`

The `DyTopoGenerator` (`topology.go`) is a within-trajectory `CompositeTopologyCell`:
to the AgensFlow bandit it is one action with one reward, but internally it runs
DyTopo's manager-guided multi-round loop and charges **all** its inner-round tokens.
The mapping from the DyTopo paper (Lu et al.) to the code:

| DyTopo paper | Code |
|---|---|
| Per-round single-pass agent inference, eq (1)–(2) — role + round goal + local memory → `⟨public, private, query(need), key(offer)⟩` | Phase 1 loop in `DyTopoGenerator.Run`; roles + required fields `q_desc`/`k_desc` in `roles.go` |
| Semantic alignment `r_{i,j} = cos(q̂_i, k̂_j)`, eq (4)–(5) | `relevanceMatrix()` (ℓ2-normalize + dot) in `semanticmatch.go` |
| Sparse hard-threshold adjacency `A_{j→i}=1[r>τ_edge]·(1−δ_ij)`, eq (6) | `buildEdges()` with `tau`; self-loops excluded |
| Max in-degree budget `K_in` (Appendix B hyperparameters) | top-`k_in` providers per recipient in `buildEdges()` |
| Synchronization barrier — induce graph + route, *then* update memory, eq (3) | Phases 2→4 in `Run`: memory is rebuilt into `newMem` only after all routing |
| Topology-aware ordering: topo-sort if acyclic, else greedy min-restricted-in-degree cycle break, eq (8)–(11) | `topoOrCycleBreak()` in `semanticmatch.go` |
| Recipient message ordering by descending relevance | `orderIncoming()` in `semanticmatch.go` |
| Manager round goal + halt decision, eq (12)–(14) | round-goal + `complete`/budget termination (Phase 5) in `Run` |
| Frozen encoder `all-MiniLM-L6-v2` | `Config.Encoder`; live `EmbeddingsAdapter`, offline `MockEmbedder` |
| Roles — code {Developer, Researcher, Tester, Designer}, math {ProblemParser, Solver, Verifier} | `CodeRoles` / `MathRoles` in `roles.go` |
| τ and round count are task-sensitive knobs (ablations, §5.4) | `TauBuckets` / `RoundBuckets` — exposed to the bandit as `[NEW]` action buckets, not fixed |

The `[NEW]` departure from the paper: DyTopo runs τ_edge, K_in, and round count as
**fixed hyperparameters**; TopoFlow promotes them to `dytopo(τ, k_in, round)`
action buckets so the AgensFlow bandit *learns* per-regime topology settings, while
keeping them out of the signature (topology knobs live in the action space).
