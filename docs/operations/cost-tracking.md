[Docs](../index.md) › Deploy & operate › **Cost tracking**

# Cost visibility

Every model call is recorded and persisted to `./data/costs.jsonl` (override the path with
`COST_LOG_FILE`). Pricing is cache-aware: cache writes bill at 1.25× the input rate, cache
reads at 0.10×.

**Pricing table (per million tokens, input / output):**

| Model | Input | Output |
|---|---|---|
| Qwen-Turbo (light) | $0.05 | $0.20 |
| Qwen-Plus (mid) | $0.40 | $1.20 |
| Qwen-Max (heavy) | $1.60 | $6.40 |

Override per model family via env: `COST_QWEN_IN/OUT`, `COST_DEEPSEEK_IN/OUT`,
`COST_GLM_IN/OUT` (USD per MTok). The built-in table prices the major global families.

**Local power estimate:** set `LOCAL_INFERENCE_WATTS` to your GPU's TDP (default 250 W) —
local-inference calls record estimated watt-hours instead of USD.

**REST endpoints:**

```
GET  /cost/summary          aggregate cost across all tasks (partner only)
GET  /tasks/:id/cost        cost breakdown for a single task
GET  /profiles/:id/cost     cost attributed to a lawyer's tasks
```

The **Admin → Cost** tab (partner only) shows stat cards, a stacked token breakdown,
cost-by-model and cost-by-context charts, and a per-model detail table.

Related: [Model routing](../architecture/model-routing.md) · [Local inference](../deployment/local-inference.md) · [Billing, LPM & monitors](../features/billing-and-lpm.md)
