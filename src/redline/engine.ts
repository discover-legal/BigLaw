// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Contract Redline Engine — automated playbook-driven contract negotiation.
 *
 * Takes a counterparty draft and the firm's playbook cascade, then generates
 * a position-by-position analysis: accept / redline / escalate per clause.
 *
 * The output is a structured redline report plus, optionally, a DOCX with
 * tracked changes ready for the associate to review (not send directly).
 *
 * WHAT IT KILLS:
 *   Definely ($3k+/seat) — automated contract structure analysis
 *   Kira / Luminance — ML contract review
 *   Manual associate markup (4-8 hrs per counterparty draft)
 *   Practical Law Standard Clauses — firm's own precedent is always more relevant
 */

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { resolveModelId } from "../providers/index.js";
import { sanitizePromptContent } from "../adapters/lavern.js";
import type { KnowledgeStore } from "../knowledge/index.js";
import type { PlaybookStore } from "../playbook/index.js";
import type { PlaybookEntry } from "../types.js";

const SONNET_MODEL = "claude-sonnet-4-6";
const HAIKU_MODEL = "claude-haiku-4-5-20251001";

// ─── Types ────────────────────────────────────────────────────────────────────

export type RedlineAction = "accept" | "redline" | "escalate" | "delete" | "no_position";

export interface RedlineIssue {
  clauseType: string;
  counterpartyText: string;
  /** The firm's authoritative position from the playbook cascade */
  firmPosition: string;
  /** Which tier of the cascade supplied the firm position */
  positionSource: "client" | "matter" | "personal" | "firm" | "none";
  action: RedlineAction;
  /** Proposed replacement language (for 'redline' action) */
  proposedText?: string;
  /** Brief rationale for the action */
  rationale: string;
  /** From playbook: is this a red line? */
  isRedLine: boolean;
  /** Severity — drives escalation and review prioritisation */
  severity: "critical" | "high" | "medium" | "low";
}

export interface RedlineReport {
  id: string;
  documentId?: string;
  documentTitle?: string;
  practiceArea?: string;
  jurisdiction?: string;
  totalClauses: number;
  acceptCount: number;
  redlineCount: number;
  escalateCount: number;
  deleteCount: number;
  criticalCount: number;
  issues: RedlineIssue[];
  executiveSummary: string;
  generatedAt: string;
}

// ─── RedlineEngine ────────────────────────────────────────────────────────────

export class RedlineEngine {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  /**
   * Generate a redline report for a counterparty draft.
   *
   * @param documentText  Full text of the counterparty draft.
   * @param playbookStore PlaybookStore — resolved for this matter/client/profile.
   * @param opts          Context for cascade resolution and cost attribution.
   */
  async redline(
    documentText: string,
    playbookStore: PlaybookStore,
    opts: {
      practiceArea?: string;
      jurisdiction?: string;
      matterNumber?: string;
      clientId?: string;
      profileId?: string;
      documentId?: string;
      documentTitle?: string;
      taskId?: string;
    } = {},
  ): Promise<RedlineReport> {
    const start = Date.now();

    // Step 1 — Extract clauses from the counterparty draft (Haiku, cheap).
    const clauses = await this.extractClauses(documentText, opts.practiceArea, opts.taskId);
    if (clauses.length === 0) {
      return this.emptyReport(opts);
    }

    // Step 2 — Resolve playbook cascade for each clause type.
    const issues: RedlineIssue[] = [];
    const BATCH = 8;

    for (let i = 0; i < clauses.length; i += BATCH) {
      const batch = clauses.slice(i, i + BATCH);
      const batchIssues = await this.analyseBatch(batch, playbookStore, opts);
      issues.push(...batchIssues);
    }

    // Step 3 — Executive summary (Sonnet).
    const summary = await this.generateSummary(issues, opts);

    const report: RedlineReport = {
      id: crypto.randomUUID(),
      documentId: opts.documentId,
      documentTitle: opts.documentTitle,
      practiceArea: opts.practiceArea,
      jurisdiction: opts.jurisdiction,
      totalClauses: issues.length,
      acceptCount: issues.filter((i) => i.action === "accept").length,
      redlineCount: issues.filter((i) => i.action === "redline").length,
      escalateCount: issues.filter((i) => i.action === "escalate").length,
      deleteCount: issues.filter((i) => i.action === "delete").length,
      criticalCount: issues.filter((i) => i.severity === "critical").length,
      issues,
      executiveSummary: summary,
      generatedAt: new Date().toISOString(),
    };

    logger.info("Redline report generated", {
      id: report.id,
      clauses: report.totalClauses,
      redlines: report.redlineCount,
      criticals: report.criticalCount,
    });

    return report;
  }

  // ─── Step 1: clause extraction ─────────────────────────────────────────────

  private async extractClauses(
    text: string,
    practiceArea: string | undefined,
    taskId: string | undefined,
  ): Promise<Array<{ clauseType: string; text: string }>> {
    const start = Date.now();

    const systemPrompt = `You are a contract analysis assistant.
Extract every distinct legal clause from the contract text. For each clause:
- Identify its type (e.g. "MAC/MAE definition", "Indemnification cap", "Non-compete")
- Extract the full verbatim text of the clause

Return a JSON array:
[{"clauseType": "...", "text": "..."}]

Focus on ${practiceArea ?? "transactional"} clauses. Include up to 40 clauses. Skip boilerplate recitals.`;

    try {
      const response = await this.client.messages.create({
        model: HAIKU_MODEL,
        max_tokens: 3000,
        system: [{ type: "text", text: systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [{ role: "user", content: `Contract text:\n${text.slice(0, 12000)}` }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(HAIKU_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        cacheWriteTokens: (usage as Record<string, unknown>)["cache_creation_input_tokens"] as number | undefined,
        cacheReadTokens: (usage as Record<string, unknown>)["cache_read_input_tokens"] as number | undefined,
        costUsd: calcCostUsd(resolveModelId(HAIKU_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "redline", taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "[]";
      const s = raw.indexOf("["), e = raw.lastIndexOf("]");
      if (s === -1 || e <= s) return [];
      return JSON.parse(raw.slice(s, e + 1)) as Array<{ clauseType: string; text: string }>;
    } catch (err) {
      logger.warn("RedlineEngine: clause extraction failed", { error: (err as Error).message });
      return [];
    }
  }

  // ─── Step 2: batch analysis ─────────────────────────────────────────────────

  private async analyseBatch(
    clauses: Array<{ clauseType: string; text: string }>,
    playbookStore: PlaybookStore,
    opts: {
      practiceArea?: string; matterNumber?: string; clientId?: string;
      profileId?: string; taskId?: string;
    },
  ): Promise<RedlineIssue[]> {
    const start = Date.now();

    // Resolve playbook for each clause type in this batch.
    const positions: Array<{ clauseType: string; entry: PlaybookEntry | null; source: string }> = [];
    for (const c of clauses) {
      const resolved = playbookStore.resolve(c.clauseType, {
        practiceArea: opts.practiceArea,
        matterNumber: opts.matterNumber,
        clientId: opts.clientId,
        profileId: opts.profileId,
      });
      positions.push({
        clauseType: c.clauseType,
        entry: resolved?.effectiveEntry ?? null,
        source: resolved?.resolvedFrom ?? "none",
      });
    }

    // Build prompt for Sonnet analysis.
    const clauseBlocks = clauses.map((c, idx) => {
      const p = positions[idx];
      const entry = p.entry;
      return `--- CLAUSE ${idx + 1}: ${sanitizePromptContent(c.clauseType)} ---
COUNTERPARTY TEXT: ${sanitizePromptContent(c.text.slice(0, 600))}
FIRM POSITION (${p.source}): ${entry?.standardPosition ?? "No playbook position — use judgment"}
FALLBACK: ${entry?.fallbackPosition ?? "N/A"}
RED LINES: ${entry?.redLines?.join("; ") ?? "None recorded"}`;
    }).join("\n\n");

    const systemPrompt = `You are a senior transactional lawyer reviewing a counterparty draft against your firm's playbook positions.

For each clause, determine:
- action: "accept" (counterparty text is acceptable), "redline" (needs amendment), "escalate" (partner must decide), "delete" (clause should be removed), or "no_position" (no firm position, flag for review)
- severity: "critical" (red line crossed or fundamental issue), "high" (significant commercial risk), "medium" (standard negotiation point), "low" (minor drafting preference)
- proposedText: replacement language for "redline" action (verbatim, clause-ready)
- rationale: 1-2 sentences explaining the decision
- isRedLine: true if the counterparty text crosses a firm red line

Return a JSON array — one object per clause in input order:
[{"clauseType":"...","action":"...","severity":"...","proposedText":"...","rationale":"...","isRedLine":false}]`;

    try {
      const response = await this.client.messages.create({
        model: SONNET_MODEL,
        max_tokens: 4096,
        system: [{ type: "text", text: systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [{ role: "user", content: clauseBlocks }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(SONNET_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        cacheWriteTokens: (usage as Record<string, unknown>)["cache_creation_input_tokens"] as number | undefined,
        cacheReadTokens: (usage as Record<string, unknown>)["cache_read_input_tokens"] as number | undefined,
        costUsd: calcCostUsd(resolveModelId(SONNET_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "redline", taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "[]";
      const s = raw.indexOf("["), e = raw.lastIndexOf("]");
      if (s === -1 || e <= s) return this.fallbackIssues(clauses, positions);

      const parsed = JSON.parse(raw.slice(s, e + 1)) as Array<Record<string, unknown>>;
      return parsed.map((p, idx): RedlineIssue => {
        const clause = clauses[idx] ?? clauses[0];
        const pos = positions[idx] ?? positions[0];
        return {
          clauseType: clause.clauseType,
          counterpartyText: clause.text.slice(0, 500),
          firmPosition: pos.entry?.standardPosition ?? "No position recorded",
          positionSource: (pos.source as RedlineIssue["positionSource"]) ?? "none",
          action: (p["action"] as RedlineAction) ?? "escalate",
          proposedText: typeof p["proposedText"] === "string" ? p["proposedText"] : undefined,
          rationale: String(p["rationale"] ?? ""),
          isRedLine: Boolean(p["isRedLine"]),
          severity: (p["severity"] as RedlineIssue["severity"]) ?? "medium",
        };
      });
    } catch (err) {
      logger.warn("RedlineEngine: batch analysis failed", { error: (err as Error).message });
      return this.fallbackIssues(clauses, positions);
    }
  }

  // ─── Step 3: executive summary ──────────────────────────────────────────────

  private async generateSummary(
    issues: RedlineIssue[],
    opts: { practiceArea?: string; documentTitle?: string; taskId?: string },
  ): Promise<string> {
    const start = Date.now();
    const criticals = issues.filter((i) => i.severity === "critical");
    const redlines = issues.filter((i) => i.action === "redline");
    const accepts = issues.filter((i) => i.action === "accept");

    const safeDocTitle = sanitizePromptContent(opts.documentTitle ?? "counterparty draft");
    const prompt = `Write a 3-paragraph executive summary of a contract redline review.

Document: ${safeDocTitle}
Practice area: ${opts.practiceArea ?? "transactional"}
Total clauses: ${issues.length}
Accepted: ${accepts.length} | Redlined: ${redlines.length} | Escalate: ${issues.filter((i) => i.action === "escalate").length}
Critical issues (${criticals.length}): ${criticals.map((i) => sanitizePromptContent(i.clauseType)).join(", ") || "none"}

Red lines crossed: ${issues.filter((i) => i.isRedLine).map((i) => sanitizePromptContent(i.clauseType)).join(", ") || "none"}

Key redlines: ${redlines.slice(0, 5).map((i) => `${sanitizePromptContent(i.clauseType)}: ${sanitizePromptContent(i.rationale)}`).join(" | ")}

Write as if briefing a senior partner before a negotiation call. Concise, specific, commercial.`;

    try {
      const response = await this.client.messages.create({
        model: SONNET_MODEL, max_tokens: 400,
        messages: [{ role: "user", content: prompt }],
      });
      const usage = response.usage;
      costStore.record({
        model: resolveModelId(SONNET_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        costUsd: calcCostUsd(resolveModelId(SONNET_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "redline", taskId: opts.taskId,
      });
      return response.content[0]?.type === "text" ? response.content[0].text : "";
    } catch {
      return `Redline review complete. ${criticals.length} critical issue(s), ${redlines.length} clause(s) require amendment.`;
    }
  }

  private fallbackIssues(
    clauses: Array<{ clauseType: string; text: string }>,
    positions: Array<{ clauseType: string; entry: PlaybookEntry | null; source: string }>,
  ): RedlineIssue[] {
    return clauses.map((c, idx) => ({
      clauseType: c.clauseType,
      counterpartyText: c.text.slice(0, 500),
      firmPosition: positions[idx]?.entry?.standardPosition ?? "No position recorded",
      positionSource: (positions[idx]?.source as RedlineIssue["positionSource"]) ?? "none",
      action: "escalate" as RedlineAction,
      rationale: "Analysis failed — requires manual review",
      isRedLine: false,
      severity: "medium" as const,
    }));
  }

  private emptyReport(opts: { documentId?: string; documentTitle?: string; practiceArea?: string; jurisdiction?: string }): RedlineReport {
    return {
      id: crypto.randomUUID(),
      ...opts,
      totalClauses: 0, acceptCount: 0, redlineCount: 0, escalateCount: 0, deleteCount: 0, criticalCount: 0,
      issues: [],
      executiveSummary: "No clauses extracted from the provided document.",
      generatedAt: new Date().toISOString(),
    };
  }
}

export const redlineEngine = new RedlineEngine();
