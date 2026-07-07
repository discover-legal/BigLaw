[Docs](../index.md) › Architecture & internals › **Model routing**

# Model routing

Three cost/latency tiers (plus a vision tier), chosen per agent tier + task type — or routed
entirely to local inference. The *roles* are fixed; which concrete model fills each role is set
by `MODEL_STACK` (see [Models, persistence & documents](../deployment/models-and-persistence.md)).

| Role | What routes there | Default stack (`qwen`) | Anthropic-stack equivalent |
|---|---|---|---|
| **Heavy** | T0 root orchestrator · debate · synthesis · high complexity | `qwen-max` | Opus |
| **Mid** | T1 managers · T2 specialists · drafting · extraction reconcile | `qwen-plus` | Sonnet |
| **Light** | T3 tool agents · descriptors · extraction · translation · verification passes · classification | `qwen-turbo` | Haiku |
| **Vision** | images · scanned / handwritten documents | `qwen-vl-max` | — |

> Older docs describe the tiers by their Anthropic names (Opus/Sonnet/Haiku). Those are the
> *roles*, not a hard dependency — the same routing applies to whichever family `MODEL_STACK`
> selects.

## Local overrides

| Condition | Effect |
|---|---|
| `OLLAMA_ENABLED=true` + `OLLAMA_TIERS=3` | T3 tool agents → local Ollama |
| `LOCAL_INFERENCE_TIERS=all` | Everything → LM Studio / vLLM / Jan |

Correctness-critical paths (debate, synthesis, T0) stay on cloud unless **all** tiers are
explicitly routed local. Setup details: [Local inference](../deployment/local-inference.md).

Related: [Architecture overview](overview.md) · [Cost tracking](../operations/cost-tracking.md)
