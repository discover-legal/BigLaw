// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Precedent Document Generator — firm-specific standard form documents.
 *
 * Thomson Reuters Practical Law and LexisNexis PSL charge £15–25k/yr for
 * access to "market standard" NDA, SPA, facility agreement, and employment
 * contract starting points. The dirty secret: those documents are generic.
 * Your firm's own precedent — shaped by decades of actual negotiations on
 * actual deals — is almost always better and more relevant.
 *
 * This engine turns the firm's knowledge store into a Practical Law replacement:
 *   1. Search the knowledge store for the closest precedent documents
 *   2. Extract the firm's standard clause positions via the playbook cascade
 *   3. Assemble a firm-precedent starting point from real deal language
 *   4. Inject playbook guardrails (red lines, standard positions, fallbacks)
 *
 * The output is a complete first-draft document in the firm's voice, from the
 * firm's actual deal history, calibrated to the specific practice area,
 * jurisdiction, and client — not a generic Practical Law boilerplate.
 *
 * WHAT IT KILLS:
 *   Thomson Reuters Practical Law Standard Documents (£15–25k/yr)
 *   LexisNexis PSL Standard Documents
 *   HighQ precedent vaults
 *   Manual associate drafting from scratch (4–12 hrs per document type)
 */

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import { resolveModelId } from "../providers/index.js";
import { sanitizePromptContent } from "../adapters/lavern.js";
import type { PlaybookStore } from "../playbook/index.js";
import type { KnowledgeStore } from "../knowledge/index.js";

const OPUS_MODEL = "claude-opus-4-8";
const SONNET_MODEL = "claude-sonnet-4-6";
const HAIKU_MODEL = "claude-haiku-4-5-20251001";

// ─── Types ────────────────────────────────────────────────────────────────────

export type PrecedentDocumentType =
  | "nda"              // Confidentiality / NDA
  | "spa"              // Share purchase agreement
  | "asset_purchase"   // Asset purchase agreement
  | "facility"         // Loan / facility agreement
  | "employment"       // Employment contract
  | "service_agreement" // Professional services / MSA
  | "supply_agreement" // Supply / distribution agreement
  | "jv_agreement"     // Joint venture agreement
  | "ip_assignment"    // IP assignment
  | "licence"          // IP / technology licence
  | "settlement"       // Settlement / compromise agreement
  | "term_sheet"       // Heads of terms / term sheet
  | "other";           // Generic (describe in documentType field)

export interface PrecedentClause {
  /** Standard clause heading */
  heading: string;
  /** The firm's standard language for this clause */
  draftText: string;
  /** Which playbook tier supplied this clause */
  source: "client" | "matter" | "personal" | "firm" | "knowledge_store" | "generated";
  /** Whether this clause contains a firm red line */
  hasRedLine: boolean;
  /** Notes for the lawyer on this clause */
  notes?: string;
  /** Acceptable fallback if counterparty pushes back */
  fallback?: string;
}

export interface PrecedentDocument {
  id: string;
  documentType: PrecedentDocumentType | string;
  /** Human-readable label, e.g. "NDA (mutual, English law, M&A)" */
  title: string;
  practiceArea?: string;
  jurisdiction?: string;
  /** Party description — "buyer" / "seller" / "disclosing party" / "lender" etc. */
  actingFor?: string;
  /** Number of precedent documents from the knowledge store used as sources */
  sourcePrecedentCount: number;
  /** Number of playbook positions applied */
  playbookPositionCount: number;
  clauses: PrecedentClause[];
  /** The complete assembled document text */
  document: string;
  /** Drafting notes for the associate */
  draftingNotes: string[];
  generatedAt: string;
}

// ─── PrecedentGenerator ───────────────────────────────────────────────────────

export class PrecedentGenerator {
  private readonly client: Anthropic;

  constructor() {
    this.client = new Anthropic({
      apiKey: Config.anthropic.apiKey,
      ...(Config.anthropic.baseUrl ? { baseURL: Config.anthropic.baseUrl } : {}),
    });
  }

  /**
   * Generate a firm-precedent starting-point document.
   *
   * @param documentType  Type of document to generate.
   * @param knowledge     Knowledge store — searched for firm precedent.
   * @param playbookStore PlaybookStore — cascade resolved per clause.
   * @param opts          Context: practice area, jurisdiction, acting-for side.
   */
  async generate(
    documentType: PrecedentDocumentType | string,
    knowledge: KnowledgeStore,
    playbookStore: PlaybookStore,
    opts: {
      practiceArea?: string;
      jurisdiction?: string;
      actingFor?: string;
      matterNumber?: string;
      clientId?: string;
      profileId?: string;
      specialInstructions?: string;
      taskId?: string;
    } = {},
  ): Promise<PrecedentDocument> {
    const start = Date.now();

    // Step 1 — Find the closest firm precedent documents (Haiku search)
    const precedents = await this.findPrecedents(documentType, knowledge, opts);

    // Step 2 — Determine the clause structure for this document type (Haiku)
    const clauseTypes = await this.determineClauseStructure(documentType, opts);

    // Step 3 — Resolve playbook positions for each clause type
    const playbookPositions = clauseTypes.map((ct) => ({
      clauseType: ct,
      resolved: playbookStore.resolve(ct, {
        practiceArea: opts.practiceArea,
        matterNumber: opts.matterNumber,
        clientId: opts.clientId,
        profileId: opts.profileId,
      }),
    }));
    const playbookPositionCount = playbookPositions.filter((p) => p.resolved?.effectiveEntry).length;

    // Step 4 — Draft the document (Opus — this is a primary deliverable)
    const { clauses, document, draftingNotes } = await this.draftDocument(
      documentType, precedents, playbookPositions, opts,
    );

    const title = this.buildTitle(documentType, opts);

    const result: PrecedentDocument = {
      id: crypto.randomUUID(),
      documentType,
      title,
      practiceArea: opts.practiceArea,
      jurisdiction: opts.jurisdiction,
      actingFor: opts.actingFor,
      sourcePrecedentCount: precedents.length,
      playbookPositionCount,
      clauses,
      document,
      draftingNotes,
      generatedAt: new Date().toISOString(),
    };

    logger.info("Precedent document generated", {
      id: result.id,
      type: documentType,
      clauses: clauses.length,
      playbook: playbookPositionCount,
      precedents: precedents.length,
    });

    return result;
  }

  // ─── Step 1: precedent search ──────────────────────────────────────────────

  private async findPrecedents(
    documentType: string,
    knowledge: KnowledgeStore,
    opts: { practiceArea?: string; jurisdiction?: string; taskId?: string },
  ): Promise<Array<{ title?: string; content: string }>> {
    try {
      const query = `${documentType} ${opts.practiceArea ?? ""} ${opts.jurisdiction ?? ""} precedent standard form`;
      const results = await knowledge.search(query.trim(), {
        topK: 5,
        jurisdiction: opts.jurisdiction,
        documentType: "precedent",
      });

      if (!Array.isArray(results)) return [];
      return (results as unknown as Array<Record<string, unknown>>).map((r) => ({
        title: (r["title"] ?? r["documentTitle"]) as string | undefined,
        content: String(r["content"] ?? r["text"] ?? "").slice(0, 3000),
      })).filter((r) => r.content.length > 50);
    } catch {
      return [];
    }
  }

  // ─── Step 2: clause structure ──────────────────────────────────────────────

  private async determineClauseStructure(
    documentType: string,
    opts: { practiceArea?: string; jurisdiction?: string; taskId?: string },
  ): Promise<string[]> {
    const start = Date.now();

    const prompt = `List the standard clause headings for a ${documentType} agreement.
Context: ${opts.practiceArea ?? "transactional"}, ${opts.jurisdiction ?? "English law"}.
Return a JSON array of clause type strings:
["Parties","Recitals","Definitions","..."]
Include 8–20 clauses appropriate for this document type. No other text.`;

    try {
      const response = await this.client.messages.create({
        model: HAIKU_MODEL, max_tokens: 500,
        messages: [{ role: "user", content: prompt }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(HAIKU_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        costUsd: calcCostUsd(resolveModelId(HAIKU_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "precedent_structure", taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "[]";
      const s = raw.indexOf("["), e = raw.lastIndexOf("]");
      if (s === -1 || e <= s) return this.defaultClauseTypes(documentType);
      return JSON.parse(raw.slice(s, e + 1)) as string[];
    } catch {
      return this.defaultClauseTypes(documentType);
    }
  }

  private defaultClauseTypes(documentType: string): string[] {
    const defaults: Record<string, string[]> = {
      nda: ["Parties", "Recitals", "Definitions", "Confidentiality obligation", "Permitted disclosures",
        "No licence", "Return / destruction", "Duration", "Governing law", "Dispute resolution"],
      spa: ["Parties", "Recitals", "Definitions", "Sale and purchase", "Consideration",
        "Conditions", "Completion", "Warranties", "Indemnification", "Limitation on claims",
        "MAC/MAE", "Non-compete", "Governing law", "Dispute resolution"],
      employment: ["Parties", "Appointment", "Duties", "Remuneration", "Benefits",
        "Confidentiality", "IP assignment", "Non-compete", "Non-solicitation",
        "Termination", "Garden leave", "Governing law"],
    };
    return defaults[documentType] ?? ["Parties", "Recitals", "Definitions", "Operative clauses",
      "Representations and warranties", "Covenants", "Termination", "General provisions", "Governing law"];
  }

  // ─── Step 4: document drafting (Opus) ─────────────────────────────────────

  private async draftDocument(
    documentType: string,
    precedents: Array<{ title?: string; content: string }>,
    playbookPositions: Array<{
      clauseType: string;
      resolved: ReturnType<PlaybookStore["resolve"]>;
    }>,
    opts: {
      practiceArea?: string; jurisdiction?: string; actingFor?: string;
      specialInstructions?: string; taskId?: string;
    },
  ): Promise<{ clauses: PrecedentClause[]; document: string; draftingNotes: string[] }> {
    const start = Date.now();

    const precedentBlock = precedents.length > 0
      ? `FIRM PRECEDENT EXTRACTS (${precedents.length} documents):\n` +
        precedents.map((p, i) => `--- Precedent ${i + 1}${p.title ? `: ${sanitizePromptContent(p.title)}` : ""} ---\n${sanitizePromptContent(p.content)}`).join("\n\n")
      : "FIRM PRECEDENT: None found in knowledge store — draft from market standard.";

    const playbookBlock = playbookPositions
      .map((p) => {
        const e = p.resolved?.effectiveEntry;
        if (!e) return `${p.clauseType}: No playbook position`;
        return `${p.clauseType} [${p.resolved?.resolvedFrom ?? "none"}]:
  Standard: ${e.standardPosition ?? "—"}
  Fallback: ${e.fallbackPosition ?? "—"}
  Red lines: ${e.redLines?.join("; ") ?? "none"}`;
      })
      .join("\n\n");

    const safeInstructions = opts.specialInstructions
      ? sanitizePromptContent(opts.specialInstructions).slice(0, 500)
      : "";
    const systemPrompt = `You are a senior transactional lawyer at a major law firm drafting a ${documentType} agreement.

Your task: produce a complete, clause-ready ${documentType} from the firm's own precedent and playbook positions.

ACTING FOR: ${opts.actingFor ?? "our client (party position TBC)"}
JURISDICTION: ${opts.jurisdiction ?? "English law"}
PRACTICE AREA: ${opts.practiceArea ?? "transactional"}
${safeInstructions ? `SPECIAL INSTRUCTIONS: ${safeInstructions}` : ""}

DRAFTING RULES:
1. Prefer firm precedent language — extract verbatim where the passage is appropriate
2. Where playbook positions exist, embed the STANDARD POSITION as the draft text
3. Where a clause has RED LINES, embed a comment in square brackets: [FIRM RED LINE: ...]
4. Use clean, modern drafting — no archaic formulations
5. Mark any placeholder the lawyer must complete as [INSERT: description]
6. Produce output as a JSON object with three keys:
   "clauses": array of clause objects (see schema)
   "document": the full assembled document as a Markdown string
   "draftingNotes": array of 3–8 short notes for the associate

Clause object schema:
{"heading":"...","draftText":"...","source":"firm|knowledge_store|generated","hasRedLine":false,"notes":"...","fallback":"..."}`;

    const userContent = `${precedentBlock}\n\nPLAYBOOK POSITIONS:\n${playbookBlock}`;

    try {
      const response = await this.client.messages.create({
        model: OPUS_MODEL,
        max_tokens: 8000,
        system: [{ type: "text", text: systemPrompt, cache_control: { type: "ephemeral" } }],
        messages: [{ role: "user", content: userContent }],
      });

      const usage = response.usage;
      costStore.record({
        model: resolveModelId(OPUS_MODEL), provider: "anthropic",
        inputTokens: usage.input_tokens, outputTokens: usage.output_tokens,
        cacheWriteTokens: (usage as Record<string, unknown>)["cache_creation_input_tokens"] as number | undefined,
        cacheReadTokens: (usage as Record<string, unknown>)["cache_read_input_tokens"] as number | undefined,
        costUsd: calcCostUsd(resolveModelId(OPUS_MODEL), usage.input_tokens, usage.output_tokens),
        estimatedWh: null, estimatedWatts: null,
        durationMs: Date.now() - start, context: "precedent_draft", taskId: opts.taskId,
      });

      const raw = response.content[0]?.type === "text" ? response.content[0].text : "{}";
      const s = raw.indexOf("{"), e = raw.lastIndexOf("}");
      if (s === -1 || e <= s) return this.fallbackDraft(documentType, playbookPositions);
      const parsed = JSON.parse(raw.slice(s, e + 1)) as {
        clauses?: PrecedentClause[];
        document?: string;
        draftingNotes?: string[];
      };
      return {
        clauses: parsed.clauses ?? [],
        document: parsed.document ?? "(draft generation failed — see clauses array)",
        draftingNotes: parsed.draftingNotes ?? [],
      };
    } catch (err) {
      logger.warn("PrecedentGenerator: draft failed", { error: (err as Error).message });
      return this.fallbackDraft(documentType, playbookPositions);
    }
  }

  private fallbackDraft(
    documentType: string,
    positions: Array<{ clauseType: string; resolved: ReturnType<PlaybookStore["resolve"]> }>,
  ): { clauses: PrecedentClause[]; document: string; draftingNotes: string[] } {
    const clauses: PrecedentClause[] = positions.map((p) => ({
      heading: p.clauseType,
      draftText: p.resolved?.effectiveEntry?.standardPosition ?? "[INSERT standard position]",
      source: p.resolved?.resolvedFrom ? (p.resolved.resolvedFrom as PrecedentClause["source"]) : "generated",
      hasRedLine: (p.resolved?.effectiveEntry?.redLines?.length ?? 0) > 0,
      notes: "Draft generation failed — manually complete this clause.",
      fallback: p.resolved?.effectiveEntry?.fallbackPosition,
    }));
    return {
      clauses,
      document: `# ${documentType.toUpperCase()}\n\n` +
        clauses.map((c) => `## ${c.heading}\n\n${c.draftText}`).join("\n\n"),
      draftingNotes: ["Automatic draft generation failed. Review each clause manually against the playbook."],
    };
  }

  private buildTitle(documentType: string, opts: { practiceArea?: string; jurisdiction?: string; actingFor?: string }): string {
    const labels: Record<string, string> = {
      nda: "Confidentiality Agreement (NDA)",
      spa: "Share Purchase Agreement",
      asset_purchase: "Asset Purchase Agreement",
      facility: "Facility Agreement",
      employment: "Contract of Employment",
      service_agreement: "Professional Services Agreement",
      supply_agreement: "Supply and Distribution Agreement",
      jv_agreement: "Joint Venture Agreement",
      ip_assignment: "IP Assignment Agreement",
      licence: "Licence Agreement",
      settlement: "Settlement Agreement",
      term_sheet: "Heads of Terms",
    };
    const base = labels[documentType] ?? `${documentType} Agreement`;
    const parts = [base];
    if (opts.jurisdiction) parts.push(opts.jurisdiction);
    if (opts.actingFor) parts.push(`(acting for ${opts.actingFor})`);
    return parts.join(" — ");
  }
}

export const precedentGenerator = new PrecedentGenerator();
