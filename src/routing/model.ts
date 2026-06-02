// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Smart model router.
 *
 * Routes LLM calls to the appropriate Claude model based on:
 *   - Agent tier (T0 → Opus, T3 → Haiku)
 *   - Task type (synthesis / reasoning → Opus; extraction / routing → Haiku)
 *   - Declared complexity override
 *
 * Keeps costs proportionate: Need/Offer descriptors (cheap, many calls) use Haiku;
 * adversarial debate and final synthesis (critical, few calls) use Opus.
 */

import { Config } from "../config.js";
import type { AgentTier, AgentType } from "../types.js";
import { isOllamaModel, isLocalModel } from "../providers/index.js";

export type TaskType =
  | "synthesis"     // final output generation, root orchestrator reasoning
  | "reasoning"     // complex legal analysis, multi-step logical chains
  | "drafting"      // producing structured legal prose
  | "debate"        // adversarial challenge + resolution
  | "verification"  // checking correctness
  | "descriptor"    // generating Need/Offer descriptors (lightweight)
  | "extraction"    // structured data extraction from documents
  | "routing"       // classification, decision routing
  | "translation";  // language conversion

export type Complexity = "high" | "medium" | "low";

const OPUS   = "claude-opus-4-8";
const SONNET = "claude-sonnet-4-6";
const HAIKU  = "claude-haiku-4-5-20251001";

/** Returns the "ollama:<model>" model ID for Ollama routing. */
function ollamaModel(): string {
  return `ollama:${Config.local.ollamaModel}`;
}

/** Returns the "local:<model>" model ID for generic local inference routing. */
function localModel(): string {
  return `local:${Config.local.localInferenceModel}`;
}

/** Parsed set of tiers that should route to Ollama when OLLAMA_ENABLED=true. */
function ollamaTierSet(): Set<number> {
  return new Set(
    Config.local.ollamaTiers
      .split(",")
      .map((s) => parseInt(s.trim()))
      .filter((n) => !isNaN(n)),
  );
}

/**
 * Parsed tier set for the generic local inference server.
 * Returns "all" when LOCAL_INFERENCE_TIERS=all (route everything locally).
 */
function localInferenceTierSet(): Set<number> | "all" {
  const val = Config.local.localInferenceTiers.trim().toLowerCase();
  if (!val) return new Set();
  if (val === "all") return "all";
  return new Set(
    val.split(",").map((s) => parseInt(s.trim())).filter((n) => !isNaN(n)),
  );
}

/**
 * Select the appropriate Claude model for a given agent + task combination.
 *
 * Decision matrix:
 *   T3 (tool) agents          → Haiku  (simple, high-volume, single-tool tasks)
 *   descriptor generation     → Haiku  (one-sentence need/offer, cheap per-round)
 *   extraction / routing      → Haiku  (structured, no deep reasoning)
 *   T0 (root) + synthesis     → Opus   (final output, irreversible decisions)
 *   debate / high-complexity  → Opus   (adversarial correctness critical)
 *   T1 managers + T2 analysts → Sonnet (primary workhorse for legal reasoning)
 *   drafting (long-form)      → Sonnet (quality prose without Opus cost)
 */
export function selectModel(params: {
  tier?: AgentTier;
  type?: AgentType;
  taskType: TaskType;
  complexity?: Complexity;
}): string {
  const { tier, taskType, complexity } = params;

  // ── Generic local inference routing (LM Studio, Jan, vLLM, llama.cpp) ────
  // LOCAL_INFERENCE_URL must be set. LOCAL_INFERENCE_TIERS=all routes everything;
  // a comma list routes specific tiers only.
  if (Config.local.localInferenceUrl) {
    const localTiers = localInferenceTierSet();
    if (localTiers === "all") return localModel();
    if (tier !== undefined && (localTiers as Set<number>).has(tier)) return localModel();
  }

  // ── Ollama routing ────────────────────────────────────────────────────────
  // When OLLAMA_ENABLED=true, lightweight tasks for configured tiers go local.
  // Debate, synthesis, and T0 always stay on cloud (correctness-critical).
  if (Config.local.ollamaEnabled && taskType !== "debate" && taskType !== "synthesis" && tier !== 0) {
    const ollamaTiers = ollamaTierSet();
    const lightweightForOllama = taskType === "descriptor" || taskType === "extraction" || taskType === "routing" || taskType === "translation";
    if (tier !== undefined && ollamaTiers.has(tier)) return ollamaModel();
    if (lightweightForOllama && (tier === undefined || ollamaTiers.has(tier ?? 99))) return ollamaModel();
  }

  // ── Cloud model selection ─────────────────────────────────────────────────

  // Always Haiku for lightweight tasks regardless of tier
  if (taskType === "descriptor") return HAIKU;
  if (taskType === "extraction") return HAIKU;
  if (taskType === "routing")    return HAIKU;
  if (taskType === "translation") return HAIKU;

  // T3 tool agents always Haiku — they wrap a single tool call
  if (tier === 3) return HAIKU;

  // Root orchestrator and debate always Opus
  if (tier === 0)                 return OPUS;
  if (taskType === "synthesis")   return OPUS;
  if (taskType === "debate")      return OPUS;
  if (complexity === "high")      return OPUS;

  // Everything else (T1 managers, T2 specialists, drafting, verification) → Sonnet
  return SONNET;
}

/**
 * Estimate task complexity from a prompt string.
 * Simple heuristic: multi-step, multi-jurisdiction, novel legal theory → high.
 */
export function estimateComplexity(text: string): Complexity {
  const lower = text.toLowerCase();
  const highSignals = [
    "novel",
    "unprecedented",
    "balance",
    "proportionality",
    "multi-jurisdict",
    "conflict",
    "fundamental right",
    "constitutional",
    "antitrust",
    "merger control",
    "sanctions",
  ];
  const lowSignals = ["extract", "list", "identify", "translate", "summarise", "count"];

  const highScore = highSignals.filter((s) => lower.includes(s)).length;
  const lowScore  = lowSignals.filter((s) => lower.includes(s)).length;

  if (highScore >= 2) return "high";
  if (lowScore >= 2)  return "low";
  return "medium";
}

/**
 * Returns true when extended thinking should be requested for a call.
 * Only Anthropic cloud models (Opus/Sonnet) support thinking; local models don't.
 * Only synthesis, debate, and high-complexity reasoning benefit enough to justify
 * the additional latency and cost.
 */
export function shouldUseThinking(params: {
  modelId: string;
  taskType: TaskType;
  tier?: AgentTier;
  complexity?: Complexity;
}): boolean {
  const { modelId, taskType, tier, complexity } = params;
  if (isOllamaModel(modelId) || isLocalModel(modelId)) return false;
  if (modelId.includes("haiku")) return false;
  return (
    taskType === "synthesis" ||
    taskType === "debate" ||
    tier === 0 ||
    (taskType === "reasoning" && complexity === "high")
  );
}

export const ModelLabels: Record<string, string> = {
  [OPUS]:   "Opus 4.8",
  [SONNET]: "Sonnet 4.6",
  [HAIKU]:  "Haiku 4.5",
};

/** Human-readable label for any model ID including ollama:* IDs. */
export function modelLabel(modelId: string): string {
  return ModelLabels[modelId] ?? modelId;
}