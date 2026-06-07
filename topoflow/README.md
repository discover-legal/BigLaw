# TopoFlow

A two-level coordination substrate for LLM multi-agent systems, implementing the
[TopoFlow spec](../docs) (synthesizing AgensFlow `[AF]` + DyTopo `[DT]` with
`[NEW]` integration glue).

- **Slow level (cross-trajectory):** a reliability-aware UCB1 contextual bandit
  over a *folded signature* selects skills, model bindings, skips, **and which
  topology generator to run**. Everything "learned" is tabular bandit statistics —
  **there is no neural training**; the semantic encoder is frozen.
- **Fast level (within-trajectory):** a `CompositeTopologyCell` produces the
  actual coordination structure. Two implementations: `LinearWithSkipGenerator`
  `[AF]` and `DyTopoGenerator` `[DT]` (per-round semantic graph induction).

To the policy graph, a whole topology cell looks like **one action with one
reward**, but it charges **all** its inner-round tokens — the guardrail that
stops the policy treating DyTopo as free.

## Layout

```
topoflow/
  config.py           defaults (§12)
  types.py            core dataclasses (§3)
  signature.py        fold() : observations -> Signature (§4)
  actions.py          legal-action enumeration A(s) (§5)
  policy_graph.py     PolicyGraph + UCB1 (§6)
  reward/             hybrid reward, RelativeJudge (+cross-judge), pluggable Q (§7)
  beliefs.py          per-role belief-delta rules (§10)
  topology/           base ABC, linear, dytopo, semantic_match (§8)
  agents/             transport (Mock + OpenRouter), roles, web-search tools (§13)
  router.py           macro control loop (§9)
  governance.py       pre-flight + halting (§9)
  trace.py            two-level trace + RunReport (§11)
  eval/               harness (7 arms), datasets, metrics H1–H5 (§14)
  tests/              acceptance tests per milestone (§15)
```

## Running

Offline (no network, no heavy deps) — all milestones M1–M8:

```bash
pip install pytest
python -m pytest topoflow/tests -q
```

Live run (M9) — real models/encoder/search:

```bash
pip install pydantic instructor openai sentence-transformers exa-py tavily-python
export OPENROUTER_API_KEY=...   # and EXA_API_KEY / TAVILY_API_KEY for search
python -c "from topoflow.config import Config; from topoflow.agents.transport import make_transport; \
from topoflow.eval.harness import run_suite; \
print(run_suite(Config(), transport=make_transport(live=True), epochs=1, out_path='topoflow_report.json')['metrics']['H1_selection'])"
```

## Design notes (degrees of freedom live in `[NEW]`)

- The signature is deliberately small (regime + 7-bit handoff mask + 4 bucketed
  beliefs). Topology knobs live in the **action space**, never the signature, so
  the arm count stays ~30 and edges are never arms.
- Quality `Q` is pluggable: `GroundTruthQ` (code: run tests; math: exact match,
  confidence 1.0) or `JudgedQ` (RelativeJudge, relative scoring, cross-judge
  averaging with disagreement-std confidence). Judge confidence scales the
  bandit backup (fractional visits).
- Metrics H1–H5 are emitted by the harness. **H1 is the decisive falsifier:** if
  generator choice does not separate by regime (coordination-heavy C3/C7/C8
  preferring `dytopo`, procedural C1/C6 preferring `linear`), the central claim
  fails — and that negative result is the deliverable.

Defaults are starting points, not validated settings. Let the milestone tests,
not intuition, gate progress.
