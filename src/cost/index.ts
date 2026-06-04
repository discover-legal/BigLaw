// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Model cost and power tracking.
 *
 * Every provider call records a CostEntry — token counts, USD cost, and (for
 * local models) an estimated power draw. Entries persist to costs.jsonl and
 * are queryable by task, profile, or aggregate summary.
 *
 * Pricing is taken from the PRICING table below. Override individual models
 * via environment variables:
 *   COST_<NORMALISED_MODEL_ID>_IN=3.00   (USD per million input tokens)
 *   COST_<NORMALISED_MODEL_ID>_OUT=15.00 (USD per million output tokens)
 * where NORMALISED_MODEL_ID is the model string uppercased with hyphens/dots
 * replaced by underscores, e.g. COST_CLAUDE_SONNET_4_6_IN=3.00
 *
 * Local inference power consumption is estimated from wall-clock duration and
 * the configured LOCAL_INFERENCE_WATTS value (default 250 W for a typical GPU).
 * Set LOCAL_INFERENCE_WATTS=30 for Apple Silicon / low-power devices.
 */

import { appendFile, readFile, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { randomUUID } from "node:crypto";
import { logger } from "../logger.js";
import { calcEmissions } from "./emissions.js";
import { Config } from "../config.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export type CostContext =
  | "task"             // agent processing within a DyTopo round
  | "descriptor"       // Need/Offer generation (Haiku, many parallel)
  | "synthesis"        // root orchestrator final synthesis
  | "tabulate"         // structured table extraction
  | "round_goal"       // round goal generation
  | "protocol_debate"  // adversarial debate (Opus)
  | "protocol_verify"  // verification pipeline (Haiku ×N)
  | "tone_analysis"    // LinkedIn tone analysis chain
  | "classification";  // practice area / client / NOSLEGAL detection

export interface CostEntry {
  id: string;
  ts: string;
  model: string;
  provider: "anthropic" | "ollama" | "local";
  inputTokens: number;
  outputTokens: number;
  /** Prompt-cache write tokens (Anthropic only). Priced at 1.25× input rate. */
  cacheWriteTokens?: number;
  /** Prompt-cache read tokens (Anthropic only). Priced at 0.10× input rate. */
  cacheReadTokens?: number;
  /** USD cost, or null for local models with no API charge. */
  costUsd: number | null;
  /** Estimated power draw in watt-hours (local models only). */
  estimatedWh: number | null;
  /** Configured watts used for the estimate. */
  estimatedWatts: number | null;
  /** CO₂ emissions in grams for this call (local inference only, from CO2.js grid data). */
  co2Grams: number | null;
  /** Estimated electricity cost in USD (local inference only, IEA 2024 tariff data). */
  electricityCostUsd: number | null;
  durationMs: number;
  context: CostContext;
  taskId?: string;
  profileId?: string;
  agentId?: string;
}

export interface CostSummary {
  totalUsd: number;
  totalInputTokens: number;
  totalOutputTokens: number;
  totalCacheWriteTokens: number;
  totalCacheReadTokens: number;
  totalWh: number;
  totalCo2Grams: number;
  totalElectricityCostUsd: number;
  byModel: Record<string, {
    usd: number;
    inputTokens: number;
    outputTokens: number;
    cacheWriteTokens: number;
    cacheReadTokens: number;
    wh: number;
    co2Grams: number;
    electricityCostUsd: number;
    calls: number;
  }>;
  byContext: Record<string, { usd: number; inputTokens: number; outputTokens: number; calls: number }>;
  entryCount: number;
}

// ─── Pricing ──────────────────────────────────────────────────────────────────

interface ModelPrice { in: number; out: number }

// USD per million tokens (input / output).
// These reflect Anthropic list pricing as of mid-2026; adjust via env if needed.
const BASE_PRICING: Record<string, ModelPrice> = {
  "claude-haiku-4-5-20251001":   { in: 1.00,  out: 5.00  },
  "claude-haiku-4-5":            { in: 1.00,  out: 5.00  },
  "claude-sonnet-4-6":           { in: 3.00,  out: 15.00 },
  "claude-opus-4-8":             { in: 15.00, out: 75.00 },
  "claude-opus-4-5":             { in: 15.00, out: 75.00 },
  "claude-3-5-haiku-20241022":   { in: 1.00,  out: 5.00  },
  "claude-3-5-sonnet-20241022":  { in: 3.00,  out: 15.00 },
  "claude-3-haiku-20240307":     { in: 0.25,  out: 1.25  },
  "claude-3-opus-20240229":      { in: 15.00, out: 75.00 },
};

function normaliseModelKey(model: string): string {
  return model.toUpperCase().replace(/[-./]/g, "_");
}

function loadPricing(): Record<string, ModelPrice> {
  const pricing = { ...BASE_PRICING };
  for (const [raw, p] of Object.entries(BASE_PRICING)) {
    const key = normaliseModelKey(raw);
    const envIn  = process.env[`COST_${key}_IN`];
    const envOut = process.env[`COST_${key}_OUT`];
    if (envIn || envOut) {
      pricing[raw] = {
        in:  envIn  ? parseFloat(envIn)  : p.in,
        out: envOut ? parseFloat(envOut) : p.out,
      };
    }
  }
  return pricing;
}

const PRICING = loadPricing();

/**
 * Calculate USD cost for a model call.
 *
 * Anthropic uses three token buckets with different rates:
 *   - inputTokens:      100% of input rate  (non-cached)
 *   - cacheWriteTokens: 125% of input rate  (written to prompt cache)
 *   - cacheReadTokens:   10% of input rate  (served from prompt cache)
 *   - outputTokens:     output rate
 *
 * Returns null if the model is not in the pricing table (e.g. unknown local model).
 */
export function calcCostUsd(
  model: string,
  inputTokens: number,
  outputTokens: number,
  cacheWriteTokens = 0,
  cacheReadTokens = 0,
): number | null {
  const p = PRICING[model];
  if (!p) return null;
  return (
    inputTokens      * p.in         +
    outputTokens     * p.out        +
    cacheWriteTokens * p.in * 1.25  +
    cacheReadTokens  * p.in * 0.10
  ) / 1_000_000;
}

export function calcWattHours(watts: number, durationMs: number): number {
  return (watts * durationMs) / 3_600_000;
}

// ─── Store ────────────────────────────────────────────────────────────────────

const COST_FILE = process.env.COST_LOG_FILE ?? "./data/costs.jsonl";

export class CostStore {
  private entries: CostEntry[] = [];
  // Serialise writes through a promise chain to prevent interleaved appends.
  private writeChain: Promise<void> = Promise.resolve();

  async init(): Promise<void> {
    try {
      await mkdir(dirname(COST_FILE), { recursive: true });
      const raw = await readFile(COST_FILE, "utf8");
      this.entries = raw
        .trim()
        .split("\n")
        .filter(Boolean)
        .map((line: string) => JSON.parse(line) as CostEntry);
      logger.info("Cost log loaded", { entries: this.entries.length, file: COST_FILE });
    } catch {
      this.entries = [];
    }
  }

  record(entry: Omit<CostEntry, "id" | "ts" | "co2Grams" | "electricityCostUsd">): void {
    const emissions = entry.estimatedWh != null
      ? calcEmissions(entry.estimatedWh, Config.local.inferenceRegion)
      : null;
    const full: CostEntry = {
      id: randomUUID(),
      ts: new Date().toISOString(),
      co2Grams: emissions?.co2Grams ?? null,
      electricityCostUsd: emissions?.electricityCostUsd ?? null,
      ...entry,
    };
    this.entries.push(full);
    this.writeChain = this.writeChain
      .then(() => appendFile(COST_FILE, JSON.stringify(full) + "\n", "utf8"))
      .catch((err) => logger.warn("Cost log write failed", { error: (err as Error).message }));
  }

  forTask(taskId: string): CostEntry[] {
    return this.entries.filter((e) => e.taskId === taskId);
  }

  forProfile(profileId: string): CostEntry[] {
    return this.entries.filter((e) => e.profileId === profileId);
  }

  summarise(entries: CostEntry[] = this.entries): CostSummary {
    const byModel: CostSummary["byModel"] = {};
    const byContext: CostSummary["byContext"] = {};
    let totalUsd = 0;
    let totalInputTokens = 0;
    let totalOutputTokens = 0;
    let totalCacheWriteTokens = 0;
    let totalCacheReadTokens = 0;
    let totalWh = 0;
    let totalCo2Grams = 0;
    let totalElectricityCostUsd = 0;

    for (const e of entries) {
      const usd  = e.costUsd ?? 0;
      const wh   = e.estimatedWh ?? 0;
      const cw   = e.cacheWriteTokens ?? 0;
      const cr   = e.cacheReadTokens ?? 0;
      const co2  = e.co2Grams ?? 0;
      const elec = e.electricityCostUsd ?? 0;
      totalUsd                 += usd;
      totalInputTokens         += e.inputTokens;
      totalOutputTokens        += e.outputTokens;
      totalCacheWriteTokens    += cw;
      totalCacheReadTokens     += cr;
      totalWh                  += wh;
      totalCo2Grams            += co2;
      totalElectricityCostUsd  += elec;

      const m = byModel[e.model] ?? { usd: 0, inputTokens: 0, outputTokens: 0, cacheWriteTokens: 0, cacheReadTokens: 0, wh: 0, co2Grams: 0, electricityCostUsd: 0, calls: 0 };
      m.usd              += usd;
      m.inputTokens      += e.inputTokens;
      m.outputTokens     += e.outputTokens;
      m.cacheWriteTokens += cw;
      m.cacheReadTokens  += cr;
      m.wh               += wh;
      m.co2Grams         += co2;
      m.electricityCostUsd += elec;
      m.calls            += 1;
      byModel[e.model] = m;

      const c = byContext[e.context] ?? { usd: 0, inputTokens: 0, outputTokens: 0, calls: 0 };
      c.usd          += usd;
      c.inputTokens  += e.inputTokens;
      c.outputTokens += e.outputTokens;
      c.calls        += 1;
      byContext[e.context] = c;
    }

    return {
      totalUsd, totalInputTokens, totalOutputTokens,
      totalCacheWriteTokens, totalCacheReadTokens,
      totalWh, totalCo2Grams, totalElectricityCostUsd,
      byModel, byContext, entryCount: entries.length,
    };
  }
}

export const costStore = new CostStore();
