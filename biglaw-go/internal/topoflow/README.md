# TopoFlow (Go)

A two-level coordination substrate for LLM multi-agent systems, implementing the
TopoFlow spec (AgensFlow `[AF]` + DyTopo `[DT]` with `[NEW]` integration glue),
**natively in Go** to match the rest of `biglaw-go`.

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
| harness (7 arms) + datasets + metrics (§14) | `eval.go` |
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
- The harness emits metrics H1–H5. **H1 is the decisive falsifier:** if generator
  choice does not separate by regime (coordination-heavy C3/C7/C8 preferring
  `dytopo`, procedural C1/C6 preferring `linear`), the central claim fails — and
  that negative result is the deliverable.

Defaults are starting points, not validated settings.
