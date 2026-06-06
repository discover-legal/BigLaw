// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Playbook system — hierarchical clause-position repository.
 *
 * FOUR-TIER AUTHORITY CASCADE — firm → personal → matter → client:
 *
 *   firm      — generic market-standard defaults; widest scope, lowest authority
 *   personal  — individual lawyer's preferred positions (layers over firm)
 *   matter    — positions negotiated / agreed in this specific deal
 *   client    — client's known requirements; narrowest scope, always wins
 *
 * Resolution: client > matter > personal > firm.
 *
 * The mental model: you start from the firm's generic market positions, layer your
 * own preferred approach on top, add what you know about this deal, and finally
 * apply what you know about this client — because client requirements are the most
 * definitive thing you know going into any engagement with them.
 * Personal notes always surface alongside the authoritative answer.
 *
 * WHAT IT KILLS:
 *   Contract Express ($2k+/seat) — no more questionnaire-driven document assembly
 *   Practical Law market standards — firm's own positions supersede generic TR data
 *   HighQ deal rooms — the firm owns its institutional knowledge, not TR
 */

import { randomUUID } from "node:crypto";
import { readFile, writeFile, rename, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { resolveModelId } from "../providers/index.js";
import type { KnowledgeStore } from "../knowledge/index.js";
import type { Playbook, PlaybookEntry } from "../types.js";

export type PlaybookScope = "firm" | "client" | "matter" | "personal";

// Authority order: client (supreme) > matter > personal > firm (generic baseline).
// firm = widest/most generic default; client = narrowest/most authoritative requirement.
// The cascade reads: start from firm defaults, layer personal preferences on top,
// layer matter-specific context on top, apply client requirements last (they win).
const SCOPE_PRIORITY: Record<PlaybookScope, number> = {
  firm:     0, // generic market-standard starting point
  personal: 1, // lawyer's preferred defaults (above firm, below deal context)
  matter:   2, // what actually happened / was agreed in this deal
  client:   3, // client's known requirements — always applied last, always win
};

// ─── Extended types (local to this module) ────────────────────────────────────

export interface ScopedPlaybook extends Playbook {
  scope: PlaybookScope;
  /** clientNumber, matterNumber, or profileId — depends on scope */
  ownerId?: string;
  ownerName?: string;
}

export interface ResolvedClause {
  clauseType: string;
  practiceArea: string;
  effectiveEntry: PlaybookEntry;
  /** Which tier provided the effective entry */
  resolvedFrom: PlaybookScope;
  /** Which tiers had a position for this clause (all tiers, not just winner) */
  availableTiers: PlaybookScope[];
  /** The personal-tier note, if any, layered on top regardless of resolution */
  personalNote?: string;
}

export interface PlaybookQueryResult {
  clauseType: string;
  practiceArea?: string;
  matterNumber?: string;
  clientId?: string;
  profileId?: string;
  resolved: ResolvedClause[];
  cascadeSummary: string;
  queriedAt: string;
}

// ─── Playbook store ───────────────────────────────────────────────────────────

export class PlaybookStore {
  private playbooks: ScopedPlaybook[] = [];
  private readonly path: string;
  private writeChain = Promise.resolve();

  constructor(path: string) {
    this.path = path;
  }

  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      this.playbooks = JSON.parse(raw) as ScopedPlaybook[];
      logger.info("Playbooks loaded", { count: this.playbooks.length });
    } catch {
      this.playbooks = [];
    }
  }

  list(opts?: { scope?: PlaybookScope; ownerId?: string; practiceArea?: string }): ScopedPlaybook[] {
    return this.playbooks.filter((p) => {
      if (opts?.scope && p.scope !== opts.scope) return false;
      if (opts?.ownerId && p.ownerId !== opts.ownerId) return false;
      if (opts?.practiceArea && p.practiceArea !== opts.practiceArea) return false;
      return true;
    });
  }

  getById(id: string): ScopedPlaybook | undefined {
    return this.playbooks.find((p) => p.id === id);
  }

  upsert(playbook: ScopedPlaybook): ScopedPlaybook {
    const idx = this.playbooks.findIndex((p) => p.id === playbook.id);
    if (idx !== -1) {
      this.playbooks[idx] = { ...playbook, updatedAt: new Date().toISOString() };
    } else {
      this.playbooks.push(playbook);
    }
    this.persist().catch((err: Error) => logger.warn("Playbook persist failed", { error: err.message }));
    return playbook;
  }

  delete(id: string): boolean {
    const idx = this.playbooks.findIndex((p) => p.id === id);
    if (idx === -1) return false;
    this.playbooks.splice(idx, 1);
    this.persist().catch((err: Error) => logger.warn("Playbook persist failed", { error: err.message }));
    return true;
  }

  /**
   * Cascade-resolve a clause type across all applicable tiers.
   *
   * Tier selection:
   *   firm      — always included
   *   client    — included if clientId supplied
   *   matter    — included if matterNumber supplied
   *   personal  — included if profileId supplied
   *
   * The winning entry is the one from the highest-specificity tier that has
   * a position for the requested clause.  Personal-tier notes are always
   * surfaced as an annotation regardless of which tier wins.
   */
  resolve(
    clauseType: string,
    opts?: { practiceArea?: string; matterNumber?: string; clientId?: string; profileId?: string },
  ): ResolvedClause | null {
    const scopeFilter: PlaybookScope[] = ["firm"];
    if (opts?.clientId) scopeFilter.push("client");
    if (opts?.matterNumber) scopeFilter.push("matter");
    if (opts?.profileId) scopeFilter.push("personal");

    const ownerMap: Partial<Record<PlaybookScope, string>> = {
      client: opts?.clientId,
      matter: opts?.matterNumber,
      personal: opts?.profileId,
    };

    // Find entries per scope
    const byScope = new Map<PlaybookScope, PlaybookEntry>();

    for (const scope of scopeFilter) {
      const ownerId = ownerMap[scope];
      const candidates = this.playbooks.filter((p) => {
        if (p.scope !== scope) return false;
        if (scope !== "firm" && p.ownerId !== ownerId) return false;
        if (opts?.practiceArea && p.practiceArea !== opts.practiceArea) return false;
        return true;
      });

      for (const pb of candidates) {
        const entry = pb.entries.find(
          (e) => e.clauseType.toLowerCase() === clauseType.toLowerCase(),
        );
        if (entry && !byScope.has(scope)) byScope.set(scope, entry);
      }
    }

    if (byScope.size === 0) return null;

    // Winner = highest-authority scope with an entry.
    // Authority: client (3) > matter (2) > personal (1) > firm (0).
    // firm is the generic baseline; client requirements always win.
    let winner: PlaybookScope = "firm";
    for (const scope of scopeFilter) {
      if (byScope.has(scope)) {
        if (SCOPE_PRIORITY[scope] > SCOPE_PRIORITY[winner]) winner = scope;
      }
    }

    const effectiveEntry = byScope.get(winner)!;
    // Personal note: always surface the personal-tier position as context
    // alongside the authoritative answer, unless personal IS the winning tier.
    const personalEntry = byScope.get("personal");
    const personalNote = personalEntry && winner !== "personal"
      ? personalEntry.standardPosition
      : undefined;

    return {
      clauseType,
      practiceArea: effectiveEntry.practiceArea,
      effectiveEntry,
      resolvedFrom: winner,
      availableTiers: Array.from(byScope.keys()),
      personalNote,
    };
  }

  /**
   * Resolve all clause types found across all applicable playbooks.
   */
  resolveAll(opts?: {
    practiceArea?: string;
    matterNumber?: string;
    clientId?: string;
    profileId?: string;
  }): PlaybookQueryResult {
    const scopeFilter: PlaybookScope[] = ["firm"];
    if (opts?.clientId) scopeFilter.push("client");
    if (opts?.matterNumber) scopeFilter.push("matter");
    if (opts?.profileId) scopeFilter.push("personal");

    const ownerMap: Partial<Record<PlaybookScope, string>> = {
      client: opts?.clientId,
      matter: opts?.matterNumber,
      personal: opts?.profileId,
    };

    // Collect all unique clause types across applicable playbooks
    const allClauseTypes = new Set<string>();
    for (const scope of scopeFilter) {
      const ownerId = ownerMap[scope];
      const pbs = this.playbooks.filter((p) => {
        if (p.scope !== scope) return false;
        if (scope !== "firm" && p.ownerId !== ownerId) return false;
        if (opts?.practiceArea && p.practiceArea !== opts.practiceArea) return false;
        return true;
      });
      for (const pb of pbs) {
        for (const e of pb.entries) allClauseTypes.add(e.clauseType);
      }
    }

    const resolved: ResolvedClause[] = [];
    for (const ct of allClauseTypes) {
      const r = this.resolve(ct, opts);
      if (r) resolved.push(r);
    }

    // Cascade reads base-first: firm → personal → matter → client (client wins).
    const tierLabels = scopeFilter.join(" → ");
    const clientWins = resolved.filter((r) => r.resolvedFrom === "client").length;
    const cascadeSummary = `Resolved ${resolved.length} clause types. Cascade [${tierLabels}]. Client requirements applied in ${clientWins}/${resolved.length} clauses.`;

    return {
      clauseType: "*",
      practiceArea: opts?.practiceArea,
      matterNumber: opts?.matterNumber,
      clientId: opts?.clientId,
      profileId: opts?.profileId,
      resolved,
      cascadeSummary,
      queriedAt: new Date().toISOString(),
    };
  }

  persist(): Promise<void> {
    this.writeChain = this.writeChain.then(() => this.doWrite()).catch(() => this.doWrite());
    return this.writeChain;
  }

  private async doWrite(): Promise<void> {
    await mkdir(dirname(this.path), { recursive: true });
    const tmp = `${this.path}.tmp`;
    await writeFile(tmp, JSON.stringify(this.playbooks, null, 2), "utf8");
    await rename(tmp, this.path);
  }
}

// ─── PlaybookBuilder (AI extraction from firm precedents) ─────────────────────

const SONNET_MODEL = "claude-sonnet-4-6";
const HAIKU_MODEL = "claude-haiku-4-5-20251001";

const CLAUSE_TYPES_BY_PRACTICE_AREA: Record<string, string[]> = {
  "Corporate & M&A": [
    "MAC/MAE definition", "Representations and warranties", "Indemnification cap",
    "Indemnification basket/deductible", "Survival period", "Non-compete", "Non-solicitation",
    "Exclusivity", "Break fee", "Reverse break fee", "No-shop / no-talk",
    "Condition precedent to closing", "Regulatory approval condition",
    "Material contracts definition", "Earnout mechanism",
  ],
  "Banking & Finance": [
    "Financial covenants", "Events of default", "Cross-default", "Change of control",
    "Prepayment mechanics", "Margin call", "Negative pledge", "Pari passu",
    "Restricted payments", "Permitted disposals", "Clean-up period",
  ],
  "Employment & Labour": [
    "Garden leave", "Post-termination restrictions", "Notice period",
    "Confidentiality obligation", "IP assignment", "Bonus clawback",
    "Dispute resolution mechanism", "Jurisdiction clause",
  ],
  "Real Estate": [
    "Rent review mechanism", "Break clause", "Alienation provisions",
    "Service charge cap", "Dilapidations regime", "SDLT treatment",
    "Lease length", "Repair obligation",
  ],
  "Intellectual Property": [
    "IP ownership", "Background IP", "Foreground IP", "Licence scope",
    "Sub-licensing rights", "Royalty rate", "Audit rights", "Infringement indemnity",
  ],
  "Data Privacy & Cybersecurity": [
    "Data processing agreement terms", "Sub-processor provisions",
    "Breach notification timeline", "Data retention periods",
    "Cross-border transfer mechanism", "Data subject rights handling",
  ],
};

function getClauseTypesForPracticeArea(practiceArea: string): string[] {
  return (
    CLAUSE_TYPES_BY_PRACTICE_AREA[practiceArea] ??
    CLAUSE_TYPES_BY_PRACTICE_AREA["Corporate & M&A"]
  );
}

export class PlaybookBuilder {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  /**
   * Build a playbook from the firm's precedent library.
   *
   * @param knowledge   The knowledge store to search for precedents.
   * @param store       The playbook store to persist the result.
   * @param opts        Options: scope, ownerId, practiceArea, etc.
   */
  async build(
    knowledge: KnowledgeStore,
    store: PlaybookStore,
    opts: {
      scope: PlaybookScope;
      ownerId?: string;
      ownerName?: string;
      practiceArea: string;
      jurisdiction?: string;
      name: string;
      description?: string;
      clauseTypes?: string[];
      taskId?: string;
    },
  ): Promise<ScopedPlaybook> {
    const clauseTypes = opts.clauseTypes ?? getClauseTypesForPracticeArea(opts.practiceArea);
    const entries: PlaybookEntry[] = [];

    // Search for relevant precedent documents
    const searchResults = await knowledge.search(
      `${opts.practiceArea} ${opts.jurisdiction ?? ""} contract precedent clauses positions`.trim(),
      { topK: 30, ownerId: undefined }, // firm-wide search
    );

    const docCount = new Set(searchResults.map((r) => r.document.id)).size;
    const excerpts = searchResults.slice(0, 15).map((r) => r.excerpt).join("\n\n---\n\n");

    if (clauseTypes.length === 0 || searchResults.length === 0) {
      logger.warn("PlaybookBuilder: no clause types or no documents found", {
        practiceArea: opts.practiceArea,
        docCount: searchResults.length,
      });
    }

    // Chunked extraction — Haiku per clause type batch
    const BATCH_SIZE = 5;
    for (let i = 0; i < clauseTypes.length; i += BATCH_SIZE) {
      const batch = clauseTypes.slice(i, i + BATCH_SIZE);
      const batchEntries = await this.extractBatch(batch, excerpts, opts, docCount);
      entries.push(...batchEntries);
    }

    // Synthesis pass (Sonnet) — write the injectionSnippet summary per entry
    const playbook: ScopedPlaybook = {
      id: randomUUID(),
      scope: opts.scope,
      ownerId: opts.ownerId,
      ownerName: opts.ownerName,
      name: opts.name,
      description: opts.description,
      practiceArea: opts.practiceArea,
      jurisdiction: opts.jurisdiction,
      clauseTypes: entries.map((e) => e.clauseType),
      entries,
      documentCount: docCount,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      generatedByTaskId: opts.taskId,
    };

    store.upsert(playbook);
    logger.info("Playbook built", {
      id: playbook.id,
      scope: opts.scope,
      practiceArea: opts.practiceArea,
      entries: entries.length,
      docCount,
    });
    return playbook;
  }

  private async extractBatch(
    clauseTypes: string[],
    excerpts: string,
    opts: { practiceArea: string; jurisdiction?: string; scope: PlaybookScope; taskId?: string },
    docCount: number,
  ): Promise<PlaybookEntry[]> {
    const start = Date.now();
    const clauseList = clauseTypes.map((c, i) => `${i + 1}. ${c}`).join("\n");

    const systemPrompt = `You are a senior ${opts.practiceArea} transactional lawyer extracting the firm's market positions from precedent documents.

SCOPE: ${opts.scope.toUpperCase()} — ${opts.scope === "firm" ? "firm-wide defaults" : opts.scope === "client" ? "client-specific positions" : opts.scope === "matter" ? "deal-specific negotiated positions" : "personal lawyer preferences"}.

For each clause type, extract:
- standardPosition: the firm's typical opening position (1–3 sentences, specific language where possible)
- fallbackPosition: the acceptable compromise position (1–2 sentences)
- redLines: list of 2–4 absolute limits the firm will not cross
- dealPoints: list of 2–4 key negotiating observations / market context
- exampleLanguage: up to 2 short verbatim excerpt fragments from the source documents

Return a JSON array. One object per clause type:
[
  {
    "clauseType": "...",
    "standardPosition": "...",
    "fallbackPosition": "...",
    "redLines": ["...", "..."],
    "dealPoints": ["...", "..."],
    "exampleLanguage": ["...", "..."]
  }
]

If the source documents contain insufficient information for a clause type, still include it with:
  standardPosition: "Insufficient precedent data — apply firm standard market position for ${opts.jurisdiction ?? "the governing jurisdiction"}."
  redLines: ["Do not agree without partner sign-off"]`;

    const userMsg = `Extract firm positions for these ${opts.practiceArea} clause types:
${clauseList}

Source precedent excerpts (${docCount} documents):
${excerpts.slice(0, 8000)}`;

    try {
      const response = await this.client.messages.create({
        model: HAIKU_MODEL,
        max_tokens: 2048,
        system: [{ type: "text", text: systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [{ role: "user", content: userMsg }],
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
        estimatedWh: null,
        estimatedWatts: null,
        durationMs,
        context: "playbook_build",
        taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "[]";
      const jsonStart = raw.indexOf("[");
      const jsonEnd = raw.lastIndexOf("]");
      if (jsonStart === -1 || jsonEnd <= jsonStart) return this.fallbackEntries(clauseTypes, opts.practiceArea);

      const parsed = JSON.parse(raw.slice(jsonStart, jsonEnd + 1)) as Array<Record<string, unknown>>;
      return parsed.map((p): PlaybookEntry => ({
        clauseType: String(p["clauseType"] ?? ""),
        practiceArea: opts.practiceArea,
        standardPosition: String(p["standardPosition"] ?? ""),
        fallbackPosition: typeof p["fallbackPosition"] === "string" ? p["fallbackPosition"] : undefined,
        redLines: Array.isArray(p["redLines"]) ? (p["redLines"] as string[]) : [],
        dealPoints: Array.isArray(p["dealPoints"]) ? (p["dealPoints"] as string[]) : [],
        exampleLanguage: Array.isArray(p["exampleLanguage"]) ? (p["exampleLanguage"] as string[]) : undefined,
        sourceDocumentCount: docCount,
        lastUpdated: new Date().toISOString(),
      }));
    } catch (err) {
      logger.warn("PlaybookBuilder batch extraction failed", { error: (err as Error).message });
      return this.fallbackEntries(clauseTypes, opts.practiceArea);
    }
  }

  private fallbackEntries(clauseTypes: string[], practiceArea: string): PlaybookEntry[] {
    return clauseTypes.map((clauseType) => ({
      clauseType,
      practiceArea,
      standardPosition: "Extraction failed — review source documents manually.",
      redLines: ["Do not agree without partner sign-off"],
      dealPoints: [],
      sourceDocumentCount: 0,
      lastUpdated: new Date().toISOString(),
    }));
  }
}

export const playbookBuilder = new PlaybookBuilder();
