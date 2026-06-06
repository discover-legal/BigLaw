// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * InvoiceValidator — reverse-OCG invoice review engine.
 *
 * Designed for in-house legal teams and law firms receiving outside-counsel
 * invoices. Parses LEDES 1998B (and plain-text) invoice data, then validates
 * every line item against the client's Outside Counsel Guidelines (OCG).
 *
 * Two-phase check (mirrors OcgStore.checkEntry):
 *   1. Mechanical — rate caps, block billing patterns, prohibited task codes.
 *      Pure arithmetic — fast, free, deterministic.
 *   2. Semantic   — vague descriptions, inappropriate tasks, staffing issues.
 *      One Haiku call per batch of line items.
 *
 * On completion, Sonnet optionally drafts a dispute letter ready to send to
 * the outside-counsel billing partner.
 *
 * WHAT IT KILLS:
 *   Manual OCG-review spreadsheets (lawyers currently spend ~2 hrs/invoice)
 *   Legal e-billing software (BillBlast, TyMetrix, Apperio) — $20–50k/yr
 */

import { randomUUID } from "node:crypto";
import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { resolveModelId } from "../providers/index.js";
import type { OcgDocument, OcgRule } from "../types.js";
import type {
  InvoiceLineItem,
  InvoiceViolation,
  InvoiceValidationResult,
  InvoiceViolationType,
  InvoiceViolationAction,
} from "../types.js";

const HAIKU_MODEL = "claude-haiku-4-5-20251001";
const SONNET_MODEL = "claude-sonnet-4-6";

// ─── LEDES 1998B parser ───────────────────────────────────────────────────────
// Minimal RFC 4180 CSV + LEDES column mapping. Does not throw on bad input.

const LEDES_COLS = [
  "INVOICE_DATE", "INVOICE_NUMBER", "CLIENT_ID", "LAW_FIRM_MATTER_ID",
  "INVOICE_TOTAL", "BILLING_START_DATE", "BILLING_END_DATE", "INVOICE_DESCRIPTION",
  "LINE_ITEM_NUMBER", "EXP/FEE/INV_ADJ_TYPE", "LINE_ITEM_DATE",
  "LINE_ITEM_TASK_CODE", "LINE_ITEM_EXPENSE_CODE", "LINE_ITEM_ACTIVITY_CODE",
  "TIMEKEEPER_ID", "TIMEKEEPER_NAME", "TIMEKEEPER_CLASSIFICATION",
  "LINE_ITEM_DESCRIPTION", "LINE_ITEM_UNIT_COST", "LINE_ITEM_QUANTITY",
  "LINE_ITEM_SUBTOTAL", "LINE_ITEM_TOTAL", "LINE_ITEM_DATE_BILLED",
  "ADJUSTMENT_AMOUNT",
];

function parseCsvLine(line: string): string[] {
  const fields: string[] = [];
  let cur = "";
  let inQuotes = false;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (ch === '"') {
      if (inQuotes && line[i + 1] === '"') { cur += '"'; i++; }
      else inQuotes = !inQuotes;
    } else if (ch === "|" && !inQuotes) {
      // LEDES uses pipe delimiter
      fields.push(cur.trim());
      cur = "";
    } else if (ch === "," && !inQuotes) {
      fields.push(cur.trim());
      cur = "";
    } else {
      cur += ch;
    }
  }
  fields.push(cur.trim());
  return fields;
}

export function parseLedes(text: string): InvoiceLineItem[] {
  const lines = text.split(/\r?\n/).filter((l) => l.trim() && !l.startsWith("LEDES"));
  if (lines.length === 0) return [];

  // Auto-detect delimiter
  const firstLine = lines[0];
  const isPipe = firstLine.includes("|");

  // Detect if first line is a header
  const firstLower = firstLine.toLowerCase();
  let startIdx = 0;
  let colMap: number[] = [];

  if (firstLower.includes("line_item") || firstLower.includes("timekeeper") || firstLower.includes("invoice")) {
    const headers = parseCsvLine(firstLine).map((h) => h.toUpperCase().replace(/[^A-Z0-9_/]/g, "_"));
    colMap = [
      headers.indexOf("LINE_ITEM_NUMBER"),
      headers.indexOf("LINE_ITEM_DATE"),
      headers.indexOf("TIMEKEEPER_NAME"),
      headers.indexOf("TIMEKEEPER_CLASSIFICATION"),
      headers.indexOf("LINE_ITEM_TASK_CODE"),
      headers.indexOf("LINE_ITEM_ACTIVITY_CODE"),
      headers.indexOf("LINE_ITEM_DESCRIPTION"),
      headers.indexOf("LINE_ITEM_QUANTITY"),
      headers.indexOf("LINE_ITEM_UNIT_COST"),
      headers.indexOf("LINE_ITEM_TOTAL"),
    ];
    startIdx = 1;
  } else {
    // Default LEDES column order
    colMap = [8, 10, 15, 16, 11, 13, 17, 19, 18, 21];
  }

  const items: InvoiceLineItem[] = [];
  for (const line of lines.slice(startIdx)) {
    const fields = parseCsvLine(line);
    if (fields.length < 3) continue;
    const g = (idx: number) => (idx >= 0 && idx < fields.length ? fields[idx] : "");
    const qty = parseFloat(g(colMap[7]).replace(",", ".")) || undefined;
    const rate = parseFloat(g(colMap[8]).replace(",", ".")) || undefined;
    const amount = parseFloat(g(colMap[9]).replace(",", ".").replace(/[^0-9.-]/g, "")) || undefined;
    items.push({
      lineId: g(colMap[0]) || randomUUID(),
      date: g(colMap[1]) || undefined,
      timekeeperName: g(colMap[2]) || undefined,
      timekeeperClass: g(colMap[3]) || undefined,
      taskCode: g(colMap[4]) || undefined,
      activityCode: g(colMap[5]) || undefined,
      description: g(colMap[6]) || "",
      hours: qty,
      rate,
      amount,
    });
  }
  return items.filter((i) => i.description || i.hours);
}

// ─── Mechanical rule checkers ─────────────────────────────────────────────────

const TASK_VERB_RE = /\b(review|draft|research|analyz|prepar|attend|correspond|negotiate|revise|edit|call|confer|meet|discuss|investigat|file|respond|communicat|strateg)\b/gi;

function checkBlockBilling(item: InvoiceLineItem, rule?: OcgRule): InvoiceViolation | null {
  if (!item.description) return null;
  const verbs = item.description.match(TASK_VERB_RE) ?? [];
  const unique = new Set(verbs.map((v) => v.toLowerCase()));
  if (unique.size >= 3) {
    const reduction = item.amount ? item.amount * 0.2 : undefined;
    return {
      lineId: item.lineId,
      ruleId: rule?.id,
      ruleText: rule?.text,
      type: "block_billing",
      severity: rule?.severity ?? "hard",
      message: `Block billing detected: ${unique.size} distinct tasks combined in one entry ("${item.description.slice(0, 120)}...")`,
      suggestedAction: "request_detail",
      suggestedReduction: reduction ? parseFloat(reduction.toFixed(2)) : undefined,
    };
  }
  return null;
}

function checkRateCap(item: InvoiceLineItem, maxRate?: number): InvoiceViolation | null {
  if (!item.rate || !maxRate) return null;
  if (item.rate > maxRate) {
    const excessHours = item.hours ?? 1;
    const reduction = parseFloat(((item.rate - maxRate) * excessHours).toFixed(2));
    return {
      lineId: item.lineId,
      type: "rate_exceeded",
      severity: "hard",
      message: `Timekeeper rate $${item.rate}/hr exceeds OCG cap of $${maxRate}/hr for ${item.timekeeperClass ?? "this classification"}`,
      suggestedAction: "reduce",
      suggestedReduction: reduction,
    };
  }
  return null;
}

function checkVagueDescription(item: InvoiceLineItem): InvoiceViolation | null {
  if (!item.description) {
    return {
      lineId: item.lineId,
      type: "vague_description",
      severity: "soft",
      message: "Billing entry has no description",
      suggestedAction: "request_detail",
    };
  }
  const words = item.description.trim().split(/\s+/).length;
  if (words < 5) {
    return {
      lineId: item.lineId,
      type: "vague_description",
      severity: "soft",
      message: `Billing description is too vague (${words} words): "${item.description}"`,
      suggestedAction: "request_detail",
    };
  }
  return null;
}

// ─── InvoiceValidator ─────────────────────────────────────────────────────────

export class InvoiceValidator {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  /**
   * Validate an invoice (LEDES text or plain line-item array) against an OCG document.
   *
   * @param invoiceText   Raw LEDES 1998B text (or empty string if items supplied directly).
   * @param items         Pre-parsed line items (used if invoiceText is empty).
   * @param ocgDoc        The client's OCG document to validate against (optional).
   * @param opts          Metadata: clientId, submittedByFirm, matterNumber, generateLetter.
   */
  async validate(
    invoiceText: string,
    items: InvoiceLineItem[] | undefined,
    ocgDoc: OcgDocument | null,
    opts: {
      clientId?: string;
      submittedByFirm?: string;
      matterNumber?: string;
      generateDisputeLetter?: boolean;
      taskId?: string;
    } = {},
  ): Promise<InvoiceValidationResult> {
    const lineItems: InvoiceLineItem[] = items ?? (invoiceText ? parseLedes(invoiceText) : []);
    if (lineItems.length === 0) {
      return {
        id: randomUUID(),
        clientId: opts.clientId,
        submittedByFirm: opts.submittedByFirm,
        matterNumber: opts.matterNumber,
        totalOriginalAmount: 0,
        totalSuggestedReduction: 0,
        totalApprovedAmount: 0,
        lineCount: 0,
        violationCount: 0,
        hardViolationCount: 0,
        violations: [],
        validatedAt: new Date().toISOString(),
      };
    }

    const violations: InvoiceViolation[] = [];

    // Resolve OCG rate caps
    const rateCaps = this.extractRateCaps(ocgDoc);
    const blockBillingRule = ocgDoc?.rules.find((r) => r.category === "billing_increments");

    // Pass 1 — Mechanical checks (per line)
    for (const item of lineItems) {
      const vague = checkVagueDescription(item);
      if (vague) violations.push(vague);

      const block = checkBlockBilling(item, blockBillingRule ?? undefined);
      if (block) violations.push(block);

      const classCap = rateCaps[item.timekeeperClass?.toLowerCase() ?? ""] ??
        rateCaps["default"] ?? undefined;
      const rateViol = checkRateCap(item, classCap);
      if (rateViol) violations.push(rateViol);
    }

    // Pass 2 — Semantic check (Haiku, batch)
    const semanticViolations = await this.semanticCheck(lineItems, ocgDoc, opts.taskId);
    violations.push(...semanticViolations);

    // Totals
    const totalOriginalAmount = lineItems.reduce((s, i) => s + (i.amount ?? 0), 0);
    const totalReduction = violations.reduce((s, v) => s + (v.suggestedReduction ?? 0), 0);
    const hardCount = violations.filter((v) => v.severity === "hard").length;

    // Deduplicate by lineId + type
    const seen = new Set<string>();
    const dedupedViolations = violations.filter((v) => {
      const key = `${v.lineId}::${v.type}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });

    let disputeLetter: string | undefined;
    if (opts.generateDisputeLetter && hardCount > 0) {
      disputeLetter = await this.generateDisputeLetter(
        lineItems, dedupedViolations, ocgDoc,
        opts.submittedByFirm, opts.matterNumber, opts.taskId,
      );
    }

    return {
      id: randomUUID(),
      clientId: opts.clientId,
      submittedByFirm: opts.submittedByFirm,
      matterNumber: opts.matterNumber,
      totalOriginalAmount: parseFloat(totalOriginalAmount.toFixed(2)),
      totalSuggestedReduction: parseFloat(totalReduction.toFixed(2)),
      totalApprovedAmount: parseFloat((totalOriginalAmount - totalReduction).toFixed(2)),
      lineCount: lineItems.length,
      violationCount: dedupedViolations.length,
      hardViolationCount: hardCount,
      violations: dedupedViolations,
      disputeLetter,
      validatedAt: new Date().toISOString(),
    };
  }

  private extractRateCaps(ocgDoc: OcgDocument | null): Record<string, number> {
    if (!ocgDoc) return {};
    const caps: Record<string, number> = {};
    for (const rule of ocgDoc.rules) {
      if (rule.category !== "rate_limits") continue;
      const m = rule.text.match(/\$?([\d,]+)\s*(?:per|\/)\s*hour/i);
      const cap = m ? parseFloat(m[1].replace(",", "")) : null;
      if (!cap) continue;
      const classMatch = rule.text.match(/\b(partner|associate|counsel|paralegal|senior|junior)\b/i);
      caps[classMatch ? classMatch[1].toLowerCase() : "default"] = cap;
    }
    return caps;
  }

  private async semanticCheck(
    items: InvoiceLineItem[],
    ocgDoc: OcgDocument | null,
    taskId?: string,
  ): Promise<InvoiceViolation[]> {
    if (items.length === 0) return [];

    const start = Date.now();
    const ocgRules = ocgDoc?.rules
      .filter((r) => r.category !== "billing_increments" && r.category !== "rate_limits")
      .slice(0, 15)
      .map((r) => `[${r.id}] (${r.category}, ${r.severity}) ${r.text}`)
      .join("\n") ?? "(no OCG rules provided)";

    // Batch into groups of 15
    const BATCH = 15;
    const allViolations: InvoiceViolation[] = [];

    for (let i = 0; i < items.length; i += BATCH) {
      const batch = items.slice(i, i + BATCH);
      const itemsText = batch.map((item, idx) =>
        `[LINE-${item.lineId}] Date:${item.date ?? "?"} Timekeeper:${item.timekeeperName ?? "?"} (${item.timekeeperClass ?? "?"}) | TaskCode:${item.taskCode ?? "?"} ActivityCode:${item.activityCode ?? "?"} | Hours:${item.hours ?? "?"} Rate:${item.rate ?? "?"} Amt:${item.amount ?? "?"} | Desc:"${item.description}"`
      ).join("\n");

      const systemPrompt = `You are an in-house legal billing auditor reviewing outside counsel invoices against OCG (Outside Counsel Guidelines).

For each billing line item, identify semantic violations NOT already caught by mechanical checks (block billing, rate caps, vague descriptions).

Look for:
- Unauthorised task types (tasks not in scope per the engagement letter or OCG)
- Excessive hours relative to the described task
- Inappropriate staffing (senior timekeeper performing clerical/junior work)
- Internal firm administrative tasks billed to client
- Duplicate entries for the same work
- Research that should have been done as part of another billable task

OCG RULES IN FORCE:
${ocgRules}

For each violation found, return JSON:
{"lineId":"LINE-xxx","type":"unauthorized_task|excessive_hours|staffing_violation|other","severity":"hard|soft","message":"brief explanation","suggestedAction":"reject|reduce|request_detail","suggestedReduction":USD_or_null}

Return a JSON array of violations. Return [] if none found.`;

      try {
        const response = await this.client.messages.create({
          model: HAIKU_MODEL,
          max_tokens: 1024,
          system: [{ type: "text", text: systemPrompt, cache_control: { type: "ephemeral" } }],
          messages: [{ role: "user", content: `Invoice line items:\n${itemsText}` }],
        });

        const durationMs = Date.now() - start;
        const usage = response.usage;
        costStore.record({
          model: resolveModelId(HAIKU_MODEL),
          provider: "anthropic",
          inputTokens: usage.input_tokens,
          outputTokens: usage.output_tokens,
          cacheWriteTokens: (usage as Record<string, unknown>)["cache_creation_input_tokens"] as number | undefined,
          cacheReadTokens: (usage as Record<string, unknown>)["cache_read_input_tokens"] as number | undefined,
          costUsd: calcCostUsd(resolveModelId(HAIKU_MODEL), usage.input_tokens, usage.output_tokens),
          estimatedWh: null, estimatedWatts: null,
          durationMs,
          context: "invoice_validation",
          taskId,
        });

        const raw = response.content[0]?.type === "text" ? response.content[0].text : "[]";
        const jsonStart = raw.indexOf("[");
        const jsonEnd = raw.lastIndexOf("]");
        if (jsonStart === -1 || jsonEnd <= jsonStart) continue;

        const parsed = JSON.parse(raw.slice(jsonStart, jsonEnd + 1)) as Array<Record<string, unknown>>;
        for (const v of parsed) {
          const lineId = String(v["lineId"] ?? "").replace("LINE-", "");
          if (!lineId) continue;
          allViolations.push({
            lineId,
            type: (v["type"] as InvoiceViolationType) ?? "other",
            severity: (v["severity"] as "hard" | "soft") ?? "soft",
            message: String(v["message"] ?? ""),
            suggestedAction: (v["suggestedAction"] as InvoiceViolationAction) ?? "request_detail",
            suggestedReduction: typeof v["suggestedReduction"] === "number" ? v["suggestedReduction"] : undefined,
          });
        }
      } catch (err) {
        logger.warn("InvoiceValidator semantic check failed", { error: (err as Error).message });
      }
    }

    return allViolations;
  }

  private async generateDisputeLetter(
    items: InvoiceLineItem[],
    violations: InvoiceViolation[],
    ocgDoc: OcgDocument | null,
    submittedByFirm?: string,
    matterNumber?: string,
    taskId?: string,
  ): Promise<string> {
    const start = Date.now();
    const totalReduction = violations.reduce((s, v) => s + (v.suggestedReduction ?? 0), 0);
    const hardViolations = violations.filter((v) => v.severity === "hard");
    const violationSummary = hardViolations.slice(0, 10).map((v) =>
      `- Line ${v.lineId}: [${v.type}] ${v.message} → ${v.suggestedAction}${v.suggestedReduction ? ` ($${v.suggestedReduction.toFixed(2)} reduction)` : ""}`
    ).join("\n");

    const prompt = `Draft a professional but firm dispute letter to outside counsel billing department.

Recipient firm: ${submittedByFirm ?? "Outside Counsel"}
Matter: ${matterNumber ?? "as referenced in invoice"}
Total suggested reduction: $${totalReduction.toFixed(2)}
Governing OCG: ${ocgDoc?.title ?? "our Outside Counsel Guidelines"}

Violations to dispute:
${violationSummary}

Requirements:
- Professional tone; factual, not adversarial
- Cite the specific OCG provisions violated
- Request revised invoice or detailed justification within 14 business days
- Sign off as "[Senior Billing Counsel]"
- Under 400 words`;

    try {
      const response = await this.client.messages.create({
        model: SONNET_MODEL,
        max_tokens: 600,
        messages: [{ role: "user", content: prompt }],
      });

      const durationMs = Date.now() - start;
      const usage = response.usage;
      costStore.record({
        model: resolveModelId(SONNET_MODEL),
        provider: "anthropic",
        inputTokens: usage.input_tokens,
        outputTokens: usage.output_tokens,
        costUsd: calcCostUsd(resolveModelId(SONNET_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs,
        context: "invoice_validation",
        taskId,
      });

      return response.content[0]?.type === "text" ? response.content[0].text : "";
    } catch (err) {
      logger.warn("InvoiceValidator dispute letter generation failed", { error: (err as Error).message });
      return "";
    }
  }
}

export const invoiceValidator = new InvoiceValidator();
