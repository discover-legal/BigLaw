// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * OcgStore — persists and queries Outside Counsel Guidelines documents.
 *
 * Two-phase compliance check per time entry:
 *   1. Mechanical — billing_increments and timing rules are checked with pure
 *      math / date arithmetic (no AI). Fast, deterministic, free.
 *   2. Semantic   — all other rules are evaluated by Haiku. Each OcgRule is
 *      passed as a structured {id, category, text, severity} JSON object so
 *      violations map back to specific rule IDs — not a text dump injected
 *      into a prompt.
 *
 * The compliance check runs independently of description generation.
 * The worker generates a description first, then fires checkEntry() against
 * the rule dictionary as a separate pass.
 */

import { randomUUID } from "node:crypto";
import { readFile, writeFile, rename, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import type { OcgDocument, OcgRule, OcgRuleCategory, OcgRuleStat, OcgSuggestion, TimeEntry } from "../types.js";

// ─── Sanitisation ─────────────────────────────────────────────────────────────

function sanitizeText(s: string): string {
  return s
    .replace(/FINDING:/g, "")
    .replace(/END_FINDING/g, "")
    .replace(/NO_FINDINGS/g, "")
    .replace(/NO_CHALLENGE/g, "")
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/g, "");
}

// ─── Mechanical rule checkers ─────────────────────────────────────────────────

function makeSuggestion(rule: OcgRule, entry: TimeEntry, issue: string): OcgSuggestion {
  return {
    ruleId: rule.id,
    ruleText: rule.text,
    category: rule.category,
    severity: rule.severity,
    issue,
    suggestedDescription: entry.description,
    status: "pending",
  };
}

// Verbs that indicate a distinct billable task; 3+ in one description = block billing.
const TASK_VERBS = [
  "review", "draft", "research", "analyz", "prepar", "attend", "correspond",
  "negotiate", "revise", "edit", "call", "confer", "meet", "discuss",
  "investigat", "file", "respond", "communicat", "strateg",
];

// Generic single-verb descriptions with no specifics.
const VAGUE_PATTERNS = [
  /^(reviewed?|drafted?|researched?|analyzed?|prepared?|attended?|discussed?|met|called?|conferr?ed?)\s*\.?$/i,
  /^(review|draft|research|analysis|preparation|call|meeting)\s*\.?$/i,
];

/**
 * Evaluate rules deterministically — no AI, no network.
 *
 * For rules with a `mechCheck` field (set at ingest by Haiku): use the structured
 * parameters directly.
 * For legacy rules without `mechCheck` (billing_increments / timing categories):
 * fall back to regex parsing for backward compatibility.
 */
function checkMechanically(entry: TimeEntry, rules: OcgRule[]): OcgSuggestion[] {
  const violations: OcgSuggestion[] = [];
  const entryHours = entry.durationMs / 3_600_000;
  const entryAgeMs = entry.startedAt instanceof Date
    ? Date.now() - entry.startedAt.getTime() : 0;
  const desc = (entry.description ?? "").trim();

  for (const rule of rules) {
    // ── Structured mechCheck (preferred path) ──────────────────────────────
    if (rule.mechCheck) {
      const { type, value } = rule.mechCheck;
      const safeValue = value !== undefined && Number.isFinite(value) && value > 0 ? value : undefined;

      if (type === "min_duration_hours" && safeValue !== undefined && entry.durationMs > 0) {
        if (entryHours < safeValue) {
          violations.push(makeSuggestion(rule, entry,
            `Duration ${entryHours.toFixed(2)}h is below required minimum ${safeValue}h`));
        }

      } else if (type === "max_duration_hours" && safeValue !== undefined && entry.durationMs > 0) {
        if (entryHours > safeValue) {
          violations.push(makeSuggestion(rule, entry,
            `Duration ${entryHours.toFixed(2)}h exceeds maximum ${safeValue}h per entry`));
        }

      } else if (type === "max_age_days" && safeValue !== undefined && entryAgeMs > 0) {
        const ageDays = entryAgeMs / 86_400_000;
        if (ageDays > safeValue) {
          violations.push(makeSuggestion(rule, entry,
            `Entry is ${Math.floor(ageDays)} days old; must be submitted within ${safeValue} days`));
        }

      } else if (type === "max_billing_rate_usd" && safeValue !== undefined) {
        if (entry.billingRate !== undefined && entry.billingRate > safeValue) {
          violations.push(makeSuggestion(rule, entry,
            `Billing rate $${entry.billingRate}/hr exceeds client cap of $${safeValue}/hr`));
        }

      } else if (type === "min_description_chars" && safeValue !== undefined) {
        if (desc.length < safeValue) {
          violations.push(makeSuggestion(rule, entry,
            `Description is ${desc.length} characters; minimum required is ${safeValue}`));
        }

      } else if (type === "no_block_billing") {
        const matched = TASK_VERBS.filter((v) => desc.toLowerCase().includes(v));
        if (matched.length >= 3) {
          violations.push(makeSuggestion(rule, entry,
            `Description appears to combine ${matched.length} distinct tasks — potential block billing`));
        }

      } else if (type === "no_vague_entries") {
        if (VAGUE_PATTERNS.some((p) => p.test(desc))) {
          violations.push(makeSuggestion(rule, entry,
            `Description "${desc}" is too vague — must specify the subject matter`));
        }

      } else if (type === "require_matter_reference") {
        if (!entry.matterNumber) {
          violations.push(makeSuggestion(rule, entry,
            `Entry is missing a matter number reference`));
        }
      }
      continue;
    }

    // ── Legacy fallback — regex parsing for rules ingested before mechCheck ──
    const t = rule.text.toLowerCase();

    if (rule.category === "billing_increments" && entry.durationMs > 0) {
      let minHours = 0;
      const hMatch = t.match(/(\d+(?:\.\d+)?)\s*-?\s*h(?:ou)?r/);
      const mMatch = t.match(/(\d+(?:\.\d+)?)\s*-?\s*min(?:ute)?/);
      if (hMatch) minHours = parseFloat(hMatch[1]);
      else if (mMatch) minHours = parseFloat(mMatch[1]) / 60;
      else if (t.includes("one-tenth") || t.includes("1/10")) minHours = 0.1;
      if (minHours > 0 && entryHours < minHours) {
        violations.push(makeSuggestion(rule, entry,
          `Duration ${entryHours.toFixed(2)}h below required minimum ${minHours}h`));
      }
    }

    if (rule.category === "timing" && entryAgeMs > 0) {
      const dMatch = t.match(/(\d+)\s*days?/);
      if (dMatch) {
        const maxDays = parseInt(dMatch[1]);
        const ageDays = entryAgeMs / 86_400_000;
        if (ageDays > maxDays) {
          violations.push(makeSuggestion(rule, entry,
            `Entry is ${Math.floor(ageDays)} days old; must be submitted within ${maxDays} days`));
        }
      }
    }
  }

  return violations;
}

// ─── Store ────────────────────────────────────────────────────────────────────

const HAIKU_MODEL = "claude-haiku-4-5-20251001";
const SEMANTIC_BATCH_SIZE = 8;

export class OcgStore {
  private readonly path = Config.persistence.ocgFile;
  /** Map<clientId, OcgDocument> */
  private docs: Map<string, OcgDocument> = new Map();
  // Serialise writes so concurrent recordViolations()/recordOutcome() calls (the
  // queue worker runs jobs in parallel) can't interleave on the shared temp file.
  private writeChain: Promise<void> = Promise.resolve();

  async init(): Promise<void> {
    try {
      await mkdir(dirname(this.path), { recursive: true }).catch(() => {});
      const raw = await readFile(this.path, "utf8");
      const parsed = JSON.parse(raw) as Record<string, unknown>;
      for (const [clientId, raw] of Object.entries(parsed)) {
        const d = raw as OcgDocument;
        this.docs.set(clientId, {
          ...d,
          createdAt: new Date(d.createdAt as unknown as string),
          updatedAt: new Date(d.updatedAt as unknown as string),
        });
      }
      logger.info("OCG store loaded", { count: this.docs.size });
    } catch {
      this.docs = new Map();
    }
  }

  getByClient(clientId: string): OcgDocument | undefined {
    return this.docs.get(clientId);
  }

  async remove(clientId: string): Promise<void> {
    this.docs.delete(clientId);
    await this.persist();
  }

  /**
   * Ingest OCG text for a client: call Haiku to extract structured rules,
   * store the resulting OcgDocument, return it.
   */
  async ingest(clientId: string, title: string, text: string): Promise<OcgDocument> {
    const sanitized = sanitizeText(text).slice(0, 60_000);
    const excerpt = sanitized.slice(0, 500);

    const prompt = `You are extracting billing rules from an Outside Counsel Guidelines document.
Return a JSON array of rules. Each rule must have:
  - category: one of billing_increments | entry_specificity | prohibited_tasks | rate_limits | staffing | description_format | timing | other
  - text: the rule in plain English, concise (max 200 chars)
  - severity: "hard" (billing violation, will be rejected) or "soft" (style preference)
  - mechCheck: (optional) a structured object for rules that can be checked with pure math or string analysis:
      {"type":"min_duration_hours","value":0.1}      bill in minimum 0.1-hour (6-min) increments
      {"type":"max_duration_hours","value":8}        no single entry may exceed 8 hours
      {"type":"max_age_days","value":30}             entries must be submitted within 30 days
      {"type":"max_billing_rate_usd","value":750}    rate cap $750/hr
      {"type":"min_description_chars","value":50}    description must be at least 50 characters
      {"type":"no_block_billing"}                    no combining multiple tasks in one entry
      {"type":"no_vague_entries"}                    description must be specific, not just "review" or "call"
      {"type":"require_matter_reference"}            entry must reference a matter number
    Omit mechCheck entirely for rules that require judgment or context to evaluate.

Focus only on billing and time-entry rules. Ignore unrelated provisions.

OCG text:
${sanitized}

Respond with ONLY a valid JSON array, no markdown, no prose:
[{"category":"...","text":"...","severity":"..."},...]`;

    const client = new Anthropic({ apiKey: Config.anthropic.apiKey });
    const t0 = Date.now();
    const response = await client.messages.create({
      model: HAIKU_MODEL,
      max_tokens: 4096,
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
      context: "ocg_extraction",
    });

    const rawText = response.content[0].type === "text" ? response.content[0].text : "[]";
    let rawRules: Array<{ category: string; text: string; severity: string }> = [];
    try {
      const cleaned = rawText.replace(/```(?:json)?/gi, "").trim();
      const start = cleaned.indexOf("[");
      const end = cleaned.lastIndexOf("]");
      if (start !== -1 && end !== -1) {
        rawRules = JSON.parse(cleaned.slice(start, end + 1));
      }
    } catch {
      logger.warn("OCG rule extraction parse error — no rules extracted", { clientId });
    }

    const validCategories = new Set<OcgRuleCategory>([
      "billing_increments", "entry_specificity", "prohibited_tasks",
      "rate_limits", "staffing", "description_format", "timing", "other",
    ]);

    const validMechCheckTypes = new Set<import("../types.js").OcgMechCheckType>([
      "min_duration_hours", "max_duration_hours", "max_age_days",
      "max_billing_rate_usd", "min_description_chars",
      "no_block_billing", "no_vague_entries", "require_matter_reference",
    ]);

    const rules: OcgRule[] = rawRules
      .filter((r) => r && typeof r.text === "string" && r.text.trim())
      .map((r) => {
        const raw = r as Record<string, unknown>;
        const rule: OcgRule = {
          id: randomUUID(),
          category: validCategories.has(r.category as OcgRuleCategory)
            ? (r.category as OcgRuleCategory)
            : "other",
          text: String(r.text).trim().slice(0, 200),
          severity: r.severity === "hard" ? "hard" : "soft",
        };
        const mc = raw.mechCheck as { type?: string; value?: unknown } | undefined;
        if (mc && typeof mc.type === "string" && validMechCheckTypes.has(mc.type as import("../types.js").OcgMechCheckType)) {
          rule.mechCheck = {
            type: mc.type as import("../types.js").OcgMechCheckType,
            ...(typeof mc.value === "number" ? { value: mc.value } : {}),
          };
        }
        return rule;
      });

    const now = new Date();
    const existing = this.docs.get(clientId);
    const doc: OcgDocument = {
      id: existing?.id ?? randomUUID(),
      clientId,
      title: title.trim().slice(0, 200),
      rules,
      excerpt,
      createdAt: existing?.createdAt ?? now,
      updatedAt: now,
    };

    this.docs.set(clientId, doc);
    await this.persist();
    logger.info("OCG ingested", { clientId, title, ruleCount: rules.length });
    return doc;
  }

  // ─── Single-entry structured compliance check ─────────────────────────────

  /**
   * Check one time entry against all rules in an OCG document.
   *
   * Pass 1 — Mechanical (no AI):
   *   billing_increments  → duration math
   *   timing              → age-in-days math
   *
   * Pass 2 — Semantic (Haiku, batches of SEMANTIC_BATCH_SIZE rules):
   *   All other categories. Rules are passed as structured JSON objects
   *   {id, category, text, severity} — not a text dump. Violations are
   *   returned keyed by ruleId, so each maps back to a specific OcgRule.
   *
   * Returns OcgSuggestion[] — empty if the entry passes everything.
   */
  async checkEntry(entry: TimeEntry, ocgDoc: OcgDocument): Promise<OcgSuggestion[]> {
    if (!ocgDoc.rules.length) return [];

    // Mechanical: rules with an explicit mechCheck + legacy billing_increments/timing rules
    const LEGACY_MECHANICAL: OcgRuleCategory[] = ["billing_increments", "timing"];
    const mechanicalRules = ocgDoc.rules.filter(
      (r) => r.mechCheck || LEGACY_MECHANICAL.includes(r.category),
    );
    const mechanicalIds = new Set(mechanicalRules.map((r) => r.id));
    const semanticRules = ocgDoc.rules.filter((r) => !mechanicalIds.has(r.id));

    const mechanical = checkMechanically(entry, mechanicalRules);
    const semantic = await this.checkSemantically(entry, semanticRules);

    return [...mechanical, ...semantic];
  }

  private async checkSemantically(
    entry: TimeEntry,
    rules: OcgRule[],
  ): Promise<OcgSuggestion[]> {
    if (!rules.length) return [];

    const ruleDict = new Map(rules.map((r) => [r.id, r]));
    const suggestions: OcgSuggestion[] = [];
    const anthropic = new Anthropic({ apiKey: Config.anthropic.apiKey });

    for (let i = 0; i < rules.length; i += SEMANTIC_BATCH_SIZE) {
      const batch = rules.slice(i, i + SEMANTIC_BATCH_SIZE);
      const batchSuggestions = await this.evaluateRuleBatch(entry, batch, ruleDict, anthropic);
      suggestions.push(...batchSuggestions);
    }

    return suggestions;
  }

  private async evaluateRuleBatch(
    entry: TimeEntry,
    rules: OcgRule[],
    ruleDict: Map<string, OcgRule>,
    anthropic: Anthropic,
  ): Promise<OcgSuggestion[]> {
    const entryData = {
      description: entry.description || "(no description)",
      durationHours: (entry.durationMs / 3_600_000).toFixed(2),
      event: entry.event,
      billingUnits: entry.billingUnits,
    };

    const rulesData = rules.map((r) => ({
      id: r.id,
      category: r.category,
      text: r.text,
      severity: r.severity,
    }));

    const prompt = `You are an Outside Counsel Guidelines (OCG) compliance checker.

Evaluate the time entry against each rule in the RULES array.
Return ONLY rules that are violated. Skip rules the entry already satisfies.

TIME ENTRY:
${JSON.stringify(entryData)}

RULES (each object has a unique "id" field):
${JSON.stringify(rulesData, null, 2)}

For each violated rule return an object with EXACTLY these fields:
{
  "ruleId": "<exact id value from the rule object>",
  "issue": "<what the entry does wrong, max 120 chars>",
  "suggestedDescription": "<rewritten description that would comply, max 300 chars>"
}

Return a JSON array. Use [] if no violations. ONLY the array — no markdown, no prose.`;

    const t0 = Date.now();
    let responseText = "[]";
    try {
      const response = await anthropic.messages.create({
        model: HAIKU_MODEL,
        max_tokens: 1024,
        messages: [{ role: "user", content: prompt }],
      });
      const durationMs = Date.now() - t0;
      costStore.record({
        model: HAIKU_MODEL,
        provider: "anthropic",
        inputTokens: response.usage.input_tokens,
        outputTokens: response.usage.output_tokens,
        costUsd: calcCostUsd(HAIKU_MODEL, response.usage.input_tokens, response.usage.output_tokens),
        estimatedWh: null,
        estimatedWatts: null,
        durationMs,
        context: "ocg_check",
      });
      responseText = response.content[0].type === "text" ? response.content[0].text : "[]";
    } catch (err) {
      logger.warn("OCG semantic check failed", { error: (err as Error).message });
      return [];
    }

    type RawViolation = { ruleId?: string; issue?: string; suggestedDescription?: string };
    let rawViolations: RawViolation[] = [];
    try {
      const cleaned = responseText.replace(/```(?:json)?/gi, "").trim();
      const start = cleaned.indexOf("[");
      const end = cleaned.lastIndexOf("]");
      if (start !== -1 && end !== -1) {
        rawViolations = JSON.parse(cleaned.slice(start, end + 1));
      }
    } catch {
      logger.warn("OCG semantic check parse error");
      return [];
    }

    const suggestions: OcgSuggestion[] = [];
    for (const v of rawViolations) {
      if (!v.ruleId || !v.issue) continue;
      const rule = ruleDict.get(v.ruleId);
      if (!rule) continue;
      suggestions.push({
        ruleId: rule.id,
        ruleText: rule.text,
        category: rule.category,
        severity: rule.severity,
        issue: String(v.issue).trim().slice(0, 120),
        suggestedDescription: String(v.suggestedDescription ?? "").trim().slice(0, 300),
        status: "pending",
      });
    }

    return suggestions;
  }

  // ─── Batch check (backward compat) ───────────────────────────────────────

  /**
   * Check multiple entries against an OCG document.
   * @deprecated Use checkEntry() per-entry in new code.
   */
  async checkEntries(
    entries: TimeEntry[],
    ocgDoc: OcgDocument,
  ): Promise<Map<string, OcgSuggestion[]>> {
    const result = new Map<string, OcgSuggestion[]>();
    for (const entry of entries) {
      const suggestions = await this.checkEntry(entry, ocgDoc);
      if (suggestions.length) result.set(entry.id, suggestions);
    }
    return result;
  }

  // ─── Stats tracking ────────────────────────────────────────────────────────

  /** Increment violation counts for every rule that fired on a given entry. */
  recordViolations(clientId: string, suggestions: OcgSuggestion[]): void {
    const doc = this.docs.get(clientId);
    if (!doc || !suggestions.length) return;
    if (!doc.ruleStats) doc.ruleStats = {};
    for (const s of suggestions) {
      const stat = doc.ruleStats[s.ruleId] ?? { violations: 0, accepted: 0, dismissed: 0 };
      stat.violations++;
      doc.ruleStats[s.ruleId] = stat;
    }
    this.persist().catch((err) => logger.warn("Failed to persist OCG stats", { error: (err as Error).message }));
  }

  /** Record that a correction suggestion was accepted or dismissed by a lawyer. */
  recordOutcome(clientId: string, ruleId: string, outcome: "accepted" | "dismissed"): void {
    const doc = this.docs.get(clientId);
    if (!doc) return;
    if (!doc.ruleStats) doc.ruleStats = {};
    const stat = doc.ruleStats[ruleId] ?? { violations: 0, accepted: 0, dismissed: 0 };
    if (outcome === "accepted") stat.accepted++;
    else stat.dismissed++;
    doc.ruleStats[ruleId] = stat;
    this.persist().catch((err) => logger.warn("Failed to persist OCG stats", { error: (err as Error).message }));
  }

  /**
   * Per-rule violation and correction-acceptance stats for a client's OCG.
   * Sorted by violation count descending — highest-friction rules first.
   * acceptanceRate = accepted / (accepted + dismissed); null if no decisions yet.
   */
  getStats(clientId: string): {
    totalRules: number;
    ruleStats: Array<OcgRuleStat & {
      ruleId: string;
      category: OcgRuleCategory;
      text: string;
      severity: "hard" | "soft";
      acceptanceRate: number | null;
    }>;
  } | null {
    const doc = this.docs.get(clientId);
    if (!doc) return null;
    const raw = doc.ruleStats ?? {};
    return {
      totalRules: doc.rules.length,
      ruleStats: doc.rules
        .map((r) => {
          const s: OcgRuleStat = raw[r.id] ?? { violations: 0, accepted: 0, dismissed: 0 };
          const decided = s.accepted + s.dismissed;
          return {
            ruleId: r.id,
            category: r.category,
            text: r.text,
            severity: r.severity,
            violations: s.violations,
            accepted: s.accepted,
            dismissed: s.dismissed,
            acceptanceRate: decided > 0 ? s.accepted / decided : null,
          };
        })
        .sort((a, b) => b.violations - a.violations),
    };
  }

  /** Atomic, serialised write — tmp file then rename, chained to avoid races. */
  persist(): Promise<void> {
    // Snapshot synchronously so the serialised write captures state at call time.
    const obj: Record<string, unknown> = {};
    for (const [clientId, doc] of this.docs) {
      obj[clientId] = {
        ...doc,
        createdAt: doc.createdAt.toISOString(),
        updatedAt: doc.updatedAt.toISOString(),
      };
    }
    this.writeChain = this.writeChain.then(async () => {
      const tmp = `${this.path}.tmp`;
      await mkdir(dirname(this.path), { recursive: true }).catch(() => {});
      await writeFile(tmp, JSON.stringify(obj, null, 2), "utf8");
      await rename(tmp, this.path);
    });
    return this.writeChain;
  }
}

export const ocgStore = new OcgStore();
