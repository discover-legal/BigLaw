[Docs](../index.md) › Deploy & operate › **Local inference**

# Local inference (Ollama / LM Studio / vLLM / Jan)

For air-gapped or maximally confidential deployments, local inference is the only option that
gives you complete data control — data never leaves your infrastructure, and no BAA is needed
(see [Legal notices — confidentiality](../legal-notices.md#confidentiality-and-data-security)).

```bash
# LM Studio / vLLM / Jan — all tiers local
LOCAL_INFERENCE_URL=http://localhost:1234/v1
LOCAL_INFERENCE_MODEL=llama-3.2-3b-instruct
LOCAL_INFERENCE_TIERS=all

# Ollama — tool-agent tier (T3) only
OLLAMA_ENABLED=true
OLLAMA_MODEL=llama3.2
OLLAMA_TIERS=3
```

Correctness-critical paths (debate, synthesis, the root orchestrator) stay on the cloud stack
unless **all** tiers are explicitly routed local — see [Model routing](../architecture/model-routing.md).

**VRAM note:** the agent model must *fit in VRAM* — a 14B that spills to CPU runs ~10× slower
and trips round timeouts; prefer a model whose weights + KV cache fit your GPU.

**What to expect on accuracy:** the grounding stack was engineered specifically so that small
local models produce verbatim-grounded citations — on `qwen2.5-7b` (fits an 8 GB GPU) it took
grounded citations from ~0% to **94%**. See [Grounding & coverage](../architecture/grounding.md)
and the [local accuracy journey](../local-accuracy-journey.md).

**Power metering:** set `LOCAL_INFERENCE_WATTS` to your GPU's TDP (default 250 W) — local
calls record estimated watt-hours instead of USD. See [Cost tracking](../operations/cost-tracking.md).

Related: [Models, persistence & documents](models-and-persistence.md)
