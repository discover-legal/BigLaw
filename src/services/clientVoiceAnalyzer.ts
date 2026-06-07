// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * clientVoiceAnalyzer — derives a ClientVoiceGuide from writing samples.
 *
 * Uses a single Haiku call to analyse up to 50 samples of client communication
 * and produce a structured guide that drafting agents can inject into prompts.
 */

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { logger } from "../logger.js";
import type { ClientVoiceGuide } from "../types.js";

const HAIKU_MODEL = "claude-haiku-4-5-20251001";

function sanitizeForLLM(s: string): string {
  return s
    .replace(/\bFINDING:/gi, "[FINDING:]")
    .replace(/\bEND_FINDING\b/gi, "[END_FINDING]")
    .replace(/\bNO_FINDINGS\b/gi, "[NO_FINDINGS]")
    .replace(/\bNO_CHALLENGE\b/gi, "[NO_CHALLENGE]")
    .replace(/\bCHALLENGE:/gi, "[CHALLENGE:]")
    .replace(/\bRESOLUTION:/gi, "[RESOLUTION:]")
    .replace(/\x00/g, "")
    .trim();
}

export async function analyzeClientVoice(
  samples: string[],
  clientId?: string,
): Promise<ClientVoiceGuide> {
  const sliced = samples.slice(0, 50);

  const prompt = `Analyze these client communications to understand how this client communicates
and what they expect from their law firm.

Samples:
${sliced.map((s, i) => `--- Sample ${i + 1} ---\n${sanitizeForLLM(s.slice(0, 800))}`).join("\n\n")}

Respond as JSON only:
{
  "preferredFormality": "one of: formal | business-casual | conversational",
  "communicationStyle": "how they like to receive information, e.g. bullet points, executive summary, narrative",
  "terminologyPreferences": "e.g. plain English, legal terminology, industry jargon",
  "reportingPreferences": "what they focus on: risk, cost, timing, strategy, outcomes",
  "signaturePatterns": ["recurring phrase or format 1", "recurring phrase or format 2"],
  "injectionSnippet": "2-3 sentence prompt fragment for use in drafting client communications, describing their communication preferences"
}`;

  const client = new Anthropic({ apiKey: Config.anthropic.apiKey });
  const t0 = Date.now();
  const response = await client.messages.create({
    model: HAIKU_MODEL,
    max_tokens: 1024,
    messages: [{ role: "user", content: prompt }],
  });
  const durationMs = Date.now() - t0;

  const inputTokens = response.usage.input_tokens;
  const outputTokens = response.usage.output_tokens;
  costStore.record({
    model: HAIKU_MODEL,
    provider: "anthropic",
    inputTokens,
    outputTokens,
    costUsd: calcCostUsd(HAIKU_MODEL, inputTokens, outputTokens),
    estimatedWh: null,
    estimatedWatts: null,
    durationMs,
    context: "voice_analysis",
    profileId: clientId,
  });

  const rawText = response.content[0].type === "text" ? response.content[0].text : "{}";

  let parsed: Partial<ClientVoiceGuide> = {};
  try {
    const stripped = rawText.replace(/```(?:json)?/gi, "").trim();
    const start = stripped.indexOf("{");
    const end = stripped.lastIndexOf("}");
    if (start !== -1 && end !== -1) {
      parsed = JSON.parse(stripped.slice(start, end + 1));
    }
  } catch {
    logger.warn("Client voice analysis parse error", { clientId });
  }

  const guide: ClientVoiceGuide = {
    generatedAt: new Date().toISOString(),
    sampleCount: sliced.length,
    preferredFormality: String(parsed.preferredFormality ?? "formal").trim().slice(0, 200),
    communicationStyle: String(parsed.communicationStyle ?? "").trim().slice(0, 500),
    terminologyPreferences: String(parsed.terminologyPreferences ?? "").trim().slice(0, 300),
    reportingPreferences: String(parsed.reportingPreferences ?? "").trim().slice(0, 300),
    signaturePatterns: Array.isArray(parsed.signaturePatterns)
      ? (parsed.signaturePatterns as unknown[]).map(String).filter(Boolean).map(s => s.slice(0, 200)).slice(0, 5)
      : [],
    injectionSnippet: String(parsed.injectionSnippet ?? "").trim().slice(0, 1000),
  };

  logger.info("Client voice guide generated", { clientId, sampleCount: sliced.length });
  return guide;
}
