// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// RegPulseMonitor — watches for new rules/rulings affecting open matters.
// Tavily search + Haiku relevance gate. Emits "alert" (RegulationAlert).
// Entirely optional — disabled when TAVILY_API_KEY or REG_PULSE_ENABLED != "true".

import { EventEmitter } from "events";
import { randomUUID } from "crypto";
import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { assertPublicHttpUrl } from "../settings/index.js";
import type { Task, RegulationAlert } from "../types.js";

// ─── Constants ────────────────────────────────────────────────────────────────

const POLL_INTERVAL_MS = Config.regulatory.pollIntervalMs;
const PER_MATTER_COOLDOWN_MS = 60 * 60 * 1_000; // don't re-check same matter within 1 hr
const MAX_TAVILY_RESULTS = 5;
const HAIKU_MODEL = "claude-haiku-4-5-20251001";
const TAVILY_URL = "https://api.tavily.com/search";
const MAX_RESPONSE_BYTES = 2 * 1024 * 1024; // 2 MB cap on Tavily response body
const REQUEST_TIMEOUT_MS = 30_000;

// ─── Internal types ───────────────────────────────────────────────────────────

interface TavilyResult {
  url: string;
  title: string;
  content: string; // snippet
  score: number;
}

interface TavilyResponse {
  results?: TavilyResult[];
}

// ─── Sanitization ─────────────────────────────────────────────────────────────

/**
 * Strip structural prompt markers and control characters from external content
 * before embedding in any model prompt. Prevents crafted results from
 * injecting fake FINDING blocks or overriding prompt instructions.
 */
function sanitizeForHaiku(s: string): string {
  return s
    .replace(/\bFINDING:/gi, "[FINDING:]")
    .replace(/\bEND_FINDING\b/gi, "[END_FINDING]")
    .replace(/\bNO_FINDINGS\b/gi, "[NO_FINDINGS]")
    .replace(/\bNO_CHALLENGE\b/gi, "[NO_CHALLENGE]")
    .replace(/[\x00-\x08\x0b-\x1f\x7f]/g, ""); // ASCII control chars except tab/newline
}

// ─── RegPulseMonitor ──────────────────────────────────────────────────────────

export class RegPulseMonitor extends EventEmitter {
  /** matterNumber (or task id when no matterNumber) → last-checked timestamp */
  private readonly lastChecked = new Map<string, number>();
  private timer: ReturnType<typeof setInterval> | null = null;
  private readonly client: Anthropic;

  constructor() {
    super();
    this.client = new Anthropic({ apiKey: Config.anthropic.apiKey });
  }

  /** True when both TAVILY_API_KEY and REG_PULSE_ENABLED=true are set. */
  isEnabled(): boolean {
    return Boolean(Config.regulatory.tavilyApiKey) && Config.regulatory.enabled;
  }

  /** Start the background polling loop. Calls checkAll on the first tick and every POLL_INTERVAL_MS. */
  start(getTasks: () => Task[]): void {
    if (this.timer) return; // already running
    // Wrap checkAll so we can call it without passing tasks each time
    const tick = async () => {
      try {
        await this.checkAll(getTasks());
      } catch (err) {
        logger.warn("RegPulseMonitor: unhandled error in checkAll", { error: (err as Error).message });
      }
    };
    this.timer = setInterval(tick, POLL_INTERVAL_MS);
    this.timer.unref();
    // Run immediately on first start (non-blocking)
    tick().catch(() => undefined);
  }

  /** Stop the background polling loop. */
  stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  /**
   * Check all open matters for new regulations. Filters to running/pending tasks
   * and calls checkMatter for each.
   * Returns the collected alerts for the /regulatory/check-now endpoint.
   */
  async checkAll(tasks: Task[]): Promise<RegulationAlert[]> {
    const open = tasks.filter((t) => t.status === "running" || t.status === "pending");
    const allAlerts: RegulationAlert[] = [];
    for (const task of open) {
      try {
        const alerts = await this.checkMatter(task);
        allAlerts.push(...alerts);
      } catch (err) {
        logger.warn("RegPulseMonitor: error checking matter", {
          taskId: task.id,
          error: (err as Error).message,
        });
      }
    }
    return allAlerts;
  }

  /**
   * Check a single matter for new regulations.
   * Skips matters without practiceArea/jurisdiction or within the cooldown window.
   * Emits "alert" for each relevant result.
   */
  async checkMatter(task: Task): Promise<RegulationAlert[]> {
    // 1. Skip matters without the fields we need for a meaningful query.
    // Practice area is sourced from noslegal.areaOfLaw (auto-detected at submission time).
    const practiceArea = task.noslegal?.areaOfLaw;
    const jurisdiction = task.jurisdiction;

    if (!practiceArea || !jurisdiction) {
      logger.debug("RegPulseMonitor: skipping matter without practiceArea or jurisdiction", {
        taskId: task.id,
      });
      return [];
    }

    // 2. Cooldown: use matterNumber if present, else taskId as key
    const cooldownKey = task.matterNumber ?? task.id;
    const lastMs = this.lastChecked.get(cooldownKey) ?? 0;
    if (Date.now() - lastMs < PER_MATTER_COOLDOWN_MS) {
      logger.debug("RegPulseMonitor: matter within cooldown window, skipping", { cooldownKey });
      return [];
    }

    // 3. Record last-checked time (do this before the async calls so parallel
    //    invocations don't both slip through the cooldown check)
    this.lastChecked.set(cooldownKey, Date.now());

    // 4. Sanitize practiceArea and jurisdiction before using in queries and prompts
    const safePracticeArea = practiceArea.replace(/[^A-Za-z0-9 ,./()-]/g, " ").slice(0, 100);
    const safeJurisdiction = jurisdiction.replace(/[^A-Za-z0-9 ,./()-]/g, " ").slice(0, 50);

    // 5. Search → filter → emit
    const query = this.buildQuery(safePracticeArea, safeJurisdiction);
    const results = await this.searchTavily(query);
    const alerts = await this.filterRelevant(results, safePracticeArea, safeJurisdiction, task.matterNumber);

    for (const alert of alerts) {
      this.emit("alert", alert);
    }

    if (alerts.length > 0) {
      logger.info("RegPulseMonitor: emitted regulation alerts", {
        taskId: task.id,
        matterNumber: task.matterNumber,
        count: alerts.length,
      });
    }

    return alerts;
  }

  /**
   * Build a Tavily search query for a given practice area and jurisdiction.
   * Exposed as a protected method so tests can call it directly.
   */
  buildQuery(practiceArea: string, jurisdiction: string): string {
    const year = new Date().getFullYear();
    return `new regulation OR ruling OR guidance "${practiceArea}" "${jurisdiction}" ${year}`;
  }

  // ─── Private helpers ────────────────────────────────────────────────────────

  /** POST to Tavily search API. Returns [] on any error — never throws. */
  private async searchTavily(query: string): Promise<TavilyResult[]> {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);

    try {
      const res = await fetch(TAVILY_URL, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          api_key: Config.regulatory.tavilyApiKey, // never logged — read from Config only
          query,
          search_depth: "basic",
          max_results: MAX_TAVILY_RESULTS,
          include_answer: false,
        }),
        signal: controller.signal,
      });

      if (!res.ok) {
        logger.warn("RegPulseMonitor: Tavily search returned non-200", { status: res.status });
        return [];
      }

      // Cap response body to prevent memory exhaustion
      const buf = await res.arrayBuffer();
      if (buf.byteLength > MAX_RESPONSE_BYTES) {
        logger.warn("RegPulseMonitor: Tavily response body exceeds 2 MB cap, discarding");
        return [];
      }

      const json = JSON.parse(new TextDecoder().decode(buf)) as TavilyResponse;
      return json.results ?? [];
    } catch (err) {
      const msg = (err as Error).message ?? String(err);
      // Don't log the query (may contain partial API key from stringify edge cases)
      logger.warn("RegPulseMonitor: Tavily search failed", { error: msg });
      return [];
    } finally {
      clearTimeout(timeout);
    }
  }

  /**
   * Ask Haiku whether each result is materially relevant to the matter.
   * Returns only results where Haiku says relevant: true.
   */
  private async filterRelevant(
    results: TavilyResult[],
    practiceArea: string,
    jurisdiction: string,
    matterNumber?: string,
  ): Promise<RegulationAlert[]> {
    const alerts: RegulationAlert[] = [];

    for (const result of results) {
      // Validate URL from Tavily to prevent SSRF via stored/forwarded URLs
      let safeUrl = result.url ?? "";
      try {
        assertPublicHttpUrl(safeUrl, "Tavily result URL");
      } catch {
        safeUrl = ""; // discard unsafe URLs
      }

      // Sanitize external content before prompt injection
      const safeTitle = sanitizeForHaiku(result.title).slice(0, 200);
      const safeContent = sanitizeForHaiku(result.content).slice(0, 800);

      const systemPrompt =
        'You are a legal relevance filter. Reply with JSON only: {"relevant": true/false, "reason": "..."}.';
      const userPrompt =
        `Is this legal news/ruling/regulation materially relevant to a matter involving "${practiceArea}" law in "${jurisdiction}"?\n\n` +
        `Result: ${safeTitle}\n${safeContent}`;

      const t0 = Date.now();
      let response: Anthropic.Message;
      try {
        response = await this.client.messages.create({
          model: HAIKU_MODEL,
          max_tokens: 200,
          system: systemPrompt,
          messages: [{ role: "user", content: userPrompt }],
        });
      } catch (err) {
        logger.warn("RegPulseMonitor: Haiku relevance call failed", { error: (err as Error).message });
        continue;
      }

      // Record cost
      costStore.record({
        model: HAIKU_MODEL,
        provider: "anthropic",
        inputTokens: response.usage.input_tokens,
        outputTokens: response.usage.output_tokens,
        costUsd: calcCostUsd(HAIKU_MODEL, response.usage.input_tokens, response.usage.output_tokens),
        estimatedWh: null,
        estimatedWatts: null,
        durationMs: Date.now() - t0,
        context: "classification",
      });

      // Parse Haiku's JSON response
      const rawText = ((response.content[0] as { type: string; text: string }).text ?? "").trim();
      let parsed: { relevant?: boolean; reason?: string } = {};
      try {
        // Strip markdown fences if present
        const stripped = rawText.replace(/```(?:json)?/gi, "").trim();
        const start = stripped.indexOf("{");
        const end = stripped.lastIndexOf("}");
        if (start !== -1 && end > start) {
          parsed = JSON.parse(stripped.slice(start, end + 1)) as typeof parsed;
        }
      } catch {
        logger.debug("RegPulseMonitor: failed to parse Haiku relevance response", { rawText });
        continue;
      }

      if (parsed.relevant === true) {
        alerts.push({
          id: randomUUID(),
          matterNumber,
          practiceArea,
          jurisdiction,
          headline: safeTitle,
          url: safeUrl,
          summary: parsed.reason ?? "Relevant regulatory development detected.",
          detectedAt: new Date().toISOString(),
          source: "tavily",
        });
      }
    }

    return alerts;
  }
}

/** Singleton instance — wired up by the orchestrator. */
export const regPulseMonitor = new RegPulseMonitor();
