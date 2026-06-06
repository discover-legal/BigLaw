// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Headnote Generator — automated extraction of legal holdings and principles.
 *
 * Direct replacement for Westlaw's proprietary headnote database and Key Numbers
 * classification system, which lock practitioners into $15–20k/seat/yr contracts.
 *
 * Given any court opinion (full text or excerpt), produces:
 *   - Numbered headnotes: the precise legal proposition each passage stands for
 *   - Key holdings: the binding ratio decidendi, separated from obiter
 *   - Legal principles index: NOSLEGAL-tagged holdings for cross-case search
 *   - Distinguishing factors: the specific facts that limit the holding's scope
 *
 * The output is searchable via the knowledge store — every headnote is ingested
 * as a separate vector document, building the firm's own precedent database over
 * time. That's the moat compounded: every opinion processed enriches the index.
 *
 * WHAT IT KILLS:
 *   Westlaw Key Numbers / headnote database — $15–20k/seat/yr
 *   LexisNexis headnote classification
 *   Manual law clerk headnoting (2–4 hrs per long opinion)
 */

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { resolveModelId } from "../providers/index.js";

const SONNET_MODEL = "claude-sonnet-4-6";
const HAIKU_MODEL = "claude-haiku-4-5-20251001";

// ─── Types ────────────────────────────────────────────────────────────────────

export type HoldingType = "ratio" | "obiter" | "procedural" | "statutory";

export interface Headnote {
  /** Sequential number within the opinion, 1-indexed */
  number: number;
  /** The legal proposition in plain language — one sentence */
  proposition: string;
  /** The verbatim passage from the opinion that the headnote summarises */
  sourceText: string;
  /** Approximate paragraph or section reference */
  location?: string;
  /** Whether this is ratio decidendi, obiter, procedural, or statutory interpretation */
  holdingType: HoldingType;
  /** Specific facts that constrain this holding's applicability */
  distinguishingFactors: string[];
  /** NOSLEGAL area-of-law tag */
  areaOfLaw?: string;
  /** Confidence 0–1 that this is a genuine legal proposition (not background) */
  confidence: number;
}

export interface HeadnoteReport {
  id: string;
  caseName: string;
  citation?: string;
  court?: string;
  dateFiled?: string;
  jurisdiction?: string;
  /** The core ratio decidendi in one paragraph */
  keyHolding: string;
  /** All extracted headnotes, numbered */
  headnotes: Headnote[];
  /** Cross-references to related legal principles */
  relatedPrinciples: string[];
  /** Practice areas this opinion is relevant to */
  practiceAreas: string[];
  /** Suggested NOSLEGAL area-of-law tag */
  noslegalArea?: string;
  totalHeadnotes: number;
  ratioCount: number;
  obiterCount: number;
  generatedAt: string;
}

// ─── HeadnoteEngine ───────────────────────────────────────────────────────────

export class HeadnoteEngine {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  /**
   * Extract headnotes and key holdings from a court opinion.
   *
   * @param opinionText   Full text of the court opinion.
   * @param opts          Metadata and cost-tracking context.
   */
  async generate(
    opinionText: string,
    opts: {
      caseName?: string;
      citation?: string;
      court?: string;
      dateFiled?: string;
      jurisdiction?: string;
      taskId?: string;
    } = {},
  ): Promise<HeadnoteReport> {
    const start = Date.now();

    // Step 1 — Extract structured headnotes (Sonnet — needs legal precision)
    const headnotes = await this.extractHeadnotes(opinionText, opts);

    // Step 2 — Synthesise key holding + practice area tags (Haiku — summary only)
    const meta = await this.synthesiseMeta(opinionText, headnotes, opts);

    const report: HeadnoteReport = {
      id: crypto.randomUUID(),
      caseName: opts.caseName ?? meta.caseName ?? "Unknown",
      citation: opts.citation ?? meta.citation,
      court: opts.court ?? meta.court,
      dateFiled: opts.dateFiled,
      jurisdiction: opts.jurisdiction,
      keyHolding: meta.keyHolding,
      headnotes,
      relatedPrinciples: meta.relatedPrinciples,
      practiceAreas: meta.practiceAreas,
      noslegalArea: meta.noslegalArea,
      totalHeadnotes: headnotes.length,
      ratioCount: headnotes.filter((h) => h.holdingType === "ratio").length,
      obiterCount: headnotes.filter((h) => h.holdingType === "obiter").length,
      generatedAt: new Date().toISOString(),
    };

    logger.info("Headnote report generated", {
      id: report.id,
      case: report.caseName,
      headnotes: report.totalHeadnotes,
      ratio: report.ratioCount,
    });

    return report;
  }

  // ─── Step 1: headnote extraction ──────────────────────────────────────────

  private async extractHeadnotes(
    text: string,
    opts: { jurisdiction?: string; taskId?: string },
  ): Promise<Headnote[]> {
    const start = Date.now();

    const systemPrompt = `You are a legal headnote writer working for a law firm.
Your task: extract every distinct legal proposition from the court opinion below.

For each proposition, output a headnote object:
- number: sequential integer (1, 2, 3...)
- proposition: the legal rule in ONE sentence, stated as a general principle
- sourceText: the verbatim passage (up to 200 words) that establishes the proposition
- location: paragraph or page reference if identifiable
- holdingType: "ratio" (binding), "obiter" (non-binding), "procedural" (process only), or "statutory" (interpreting a statute)
- distinguishingFactors: array of specific facts that limit when this holding applies (may be empty)
- areaOfLaw: NOSLEGAL area of law (e.g. "Contract Law", "Tort - Negligence", "Corporate Finance")
- confidence: 0.0–1.0 (1.0 = clearly a legal holding, 0.5 = marginal)

Rules:
- Only include genuine legal propositions (not background facts, procedural history, or summaries)
- Separate ratio from obiter — if the court said something unnecessary to the decision, mark it obiter
- Do NOT invent propositions not in the text
- Up to 20 headnotes per opinion

Return a JSON array: [{"number":1,"proposition":"...","sourceText":"...","location":"...","holdingType":"ratio","distinguishingFactors":[],"areaOfLaw":"...","confidence":0.9}, ...]`;

    try {
      const response = await this.client.messages.create({
        model: SONNET_MODEL,
        max_tokens: 6000,
        system: [{ type: "text", text: systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [{ role: "user", content: `Court opinion:\n${text.slice(0, 15000)}` }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(SONNET_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        cacheWriteTokens: (usage as Record<string, unknown>)["cache_creation_input_tokens"] as number | undefined,
        cacheReadTokens: (usage as Record<string, unknown>)["cache_read_input_tokens"] as number | undefined,
        costUsd: calcCostUsd(resolveModelId(SONNET_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "headnote_extract", taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "[]";
      const s = raw.indexOf("["), e = raw.lastIndexOf("]");
      if (s === -1 || e <= s) return [];
      return JSON.parse(raw.slice(s, e + 1)) as Headnote[];
    } catch (err) {
      logger.warn("HeadnoteEngine: extraction failed", { error: (err as Error).message });
      return [];
    }
  }

  // ─── Step 2: key holding synthesis ────────────────────────────────────────

  private async synthesiseMeta(
    opinionText: string,
    headnotes: Headnote[],
    opts: { caseName?: string; citation?: string; court?: string; taskId?: string },
  ): Promise<{
    caseName: string; citation?: string; court?: string;
    keyHolding: string; relatedPrinciples: string[];
    practiceAreas: string[]; noslegalArea?: string;
  }> {
    const start = Date.now();

    const ratioNotes = headnotes
      .filter((h) => h.holdingType === "ratio")
      .slice(0, 5)
      .map((h) => `${h.number}. ${h.proposition}`)
      .join("\n");

    const prompt = `Given this court opinion excerpt and its ratio decidendi headnotes, produce a JSON object:
{
  "caseName": "...",            // full case name from the opinion
  "citation": "...",            // neutral citation or report citation
  "court": "...",               // court name
  "keyHolding": "...",          // the core ratio in 1-2 sentences
  "relatedPrinciples": [...],   // 3-5 related legal principles this opinion touches
  "practiceAreas": [...],       // practice areas (e.g. ["M&A", "Contract Law"])
  "noslegalArea": "..."         // single NOSLEGAL area of law
}

Ratio headnotes:
${ratioNotes || "(none extracted)"}

Opinion excerpt:
${opinionText.slice(0, 3000)}`;

    try {
      const response = await this.client.messages.create({
        model: HAIKU_MODEL, max_tokens: 600,
        messages: [{ role: "user", content: prompt }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(HAIKU_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        costUsd: calcCostUsd(resolveModelId(HAIKU_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "headnote_meta", taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "{}";
      const s = raw.indexOf("{"), e = raw.lastIndexOf("}");
      if (s === -1 || e <= s) return this.fallbackMeta(opts);
      return JSON.parse(raw.slice(s, e + 1));
    } catch {
      return this.fallbackMeta(opts);
    }
  }

  private fallbackMeta(opts: { caseName?: string; citation?: string; court?: string }) {
    return {
      caseName: opts.caseName ?? "Unknown",
      citation: opts.citation,
      court: opts.court,
      keyHolding: "Key holding could not be synthesised — see headnotes.",
      relatedPrinciples: [],
      practiceAreas: [],
    };
  }
}

export const headnoteEngine = new HeadnoteEngine();
