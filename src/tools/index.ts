// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Tool system — implementations of every capability agents can call.
 *
 * Each tool has:
 *   - A name (matches agent.allowedTools entries)
 *   - An Anthropic tool schema (passed to Claude in tool_use requests)
 *   - An execute() function (called when Claude emits a tool_use block)
 *
 * The ToolRegistry is injected into each Agent at process-time so tools
 * can access shared services (KnowledgeStore, InterRoundMemoryStore).
 */

import { tavily } from "@tavily/core";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { getProvider, resolveModelId } from "../providers/index.js";
import type { ProviderTool } from "../providers/index.js";
import { selectModel } from "../routing/model.js";
import { auditLogger } from "../audit/index.js";
import type { KnowledgeStore } from "../knowledge/index.js";
import type { InterRoundMemoryStore } from "../memory/index.js";
import { pdfExtractTextTool, pdfExtractTablesTool, pdfGenerateTool, pdfOcrTool } from "./pdf.js";
import { docusealListTemplatesTool, docusealSendForSigningTool, docusealSubmissionStatusTool } from "./docuseal.js";
import { docxGenerateTool } from "./docx.js";
import { tabularReviewTool } from "./tabular.js";
import {
  listDocumentsTool,
  readDocumentTool,
  fetchDocumentsTool,
  findInDocumentTool,
  editDocumentTool,
  replicateDocumentTool,
  readTableCellsTool,
} from "./documents.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export interface ToolContext {
  knowledge: KnowledgeStore;
  memory: InterRoundMemoryStore;
  taskId: string;
  /** ProfileId of the task creator — used to scope document searches to the owner's
   *  documents only. Undefined for partner-submitted tasks (see all documents). */
  ownerId?: string;
}

export interface ToolImpl {
  /** Name must match agent.allowedTools entries */
  name: string;
  /** Tool schema — passed to the provider's chat() call as a ProviderTool */
  schema: ProviderTool;
  /** Execute the tool given parsed input from the model */
  execute(input: Record<string, unknown>, ctx: ToolContext): Promise<unknown>;
}

// ─── web_search ──────────────────────────────────────────────────────────────

const webSearchTool: ToolImpl = {
  name: "web_search",
  schema: {
    name: "web_search",
    description:
      "Search the web for legal information. Prioritise official legislation portals, court and regulator sites, and reputable legal databases for the relevant jurisdiction.",
    input_schema: {
      type: "object" as const,
      properties: {
        query: { type: "string", description: "Search query" },
        max_results: { type: "number", description: "Maximum results to return (default 5)" },
      },
      required: ["query"],
    },
  },
  async execute(input, _ctx) {
    const query = input.query as string;
    const maxResults = (input.max_results as number | undefined) ?? 5;

    if (!Config.search.tavilyApiKey) {
      logger.warn("TAVILY_API_KEY not set — web_search returning empty");
      return { results: [], warning: "Web search unavailable: TAVILY_API_KEY not configured" };
    }

    try {
      const client = tavily({ apiKey: Config.search.tavilyApiKey });
      const response = await client.search(query, {
        maxResults,
        includeAnswer: false,
        searchDepth: "advanced",
      });

      return {
        results: response.results.map((r) => ({
          url: r.url,
          title: r.title,
          content: r.content.slice(0, 800),
          score: r.score,
          publishedDate: r.publishedDate,
        })),
      };
    } catch (err) {
      logger.error("web_search failed", { error: (err as Error).message });
      return { results: [], error: (err as Error).message };
    }
  },
};

// ─── search_knowledge ─────────────────────────────────────────────────────────

const searchKnowledgeTool: ToolImpl = {
  name: "search_knowledge",
  schema: {
    name: "search_knowledge",
    description: "Semantic search across ingested documents in the knowledge store.",
    input_schema: {
      type: "object" as const,
      properties: {
        query: { type: "string", description: "Search query" },
        top_k: { type: "number", description: "Number of results (default 6)" },
        jurisdiction: { type: "string", description: "Optional: filter by jurisdiction (e.g. 'US-NY', 'England & Wales', 'EU', 'SG')" },
        document_type: { type: "string", description: "Optional: filter by document type" },
      },
      required: ["query"],
    },
  },
  async execute(input, ctx) {
    return ctx.knowledge.search(input.query as string, {
      topK: (input.top_k as number | undefined) ?? 6,
      jurisdiction: input.jurisdiction as string | undefined,
      documentType: input.document_type as string | undefined,
      ownerId: ctx.ownerId,
    });
  },
};

// ─── query_memory ─────────────────────────────────────────────────────────────

const queryMemoryTool: ToolImpl = {
  name: "query_memory",
  schema: {
    name: "query_memory",
    description: "Query inter-round memory for findings and summaries from earlier rounds of this task.",
    input_schema: {
      type: "object" as const,
      properties: {
        query: { type: "string", description: "What to look for in memory" },
        top_k: { type: "number", description: "Number of memory entries to retrieve (default 6)" },
        agent_id: { type: "string", description: "Optional: restrict to a specific agent's memories" },
      },
      required: ["query"],
    },
  },
  async execute(input, ctx) {
    return ctx.memory.query(input.query as string, {
      taskId: ctx.taskId,
      agentId: input.agent_id as string | undefined,
      topK: (input.top_k as number | undefined) ?? 6,
    });
  },
};

// ─── extract_from_document ────────────────────────────────────────────────────

const extractFromDocumentTool: ToolImpl = {
  name: "extract_from_document",
  schema: {
    name: "extract_from_document",
    description: "Extract structured data from a specific ingested document.",
    input_schema: {
      type: "object" as const,
      properties: {
        doc_id: { type: "string", description: "Document ID to extract from" },
        extract_type: {
          type: "string",
          enum: ["clauses", "parties", "dates", "obligations", "defined_terms", "amounts", "full_text"],
          description: "Type of extraction",
        },
        context_query: {
          type: "string",
          description: "Optional: focus extraction on content relevant to this query",
        },
      },
      required: ["doc_id", "extract_type"],
    },
  },
  async execute(input, ctx) {
    const docId = input.doc_id as string;
    const extractType = input.extract_type as string;

    if (extractType === "full_text") {
      const text = await ctx.knowledge.getFullText(docId, ctx.ownerId);
      return { docId, extractType, text: text ?? "Document not found" };
    }

    // For structured extraction, retrieve full text and return relevant chunks
    const query = (input.context_query as string | undefined) ?? extractType;
    const results = await ctx.knowledge.search(query, { topK: 10, ownerId: ctx.ownerId });
    const docResults = results.filter((r) => r.document.id === docId);

    return {
      docId,
      extractType,
      excerpts: docResults.map((r) => ({
        excerpt: r.excerpt,
        score: r.score,
      })),
      note: `Focused on '${extractType}' — use full_text for complete document`,
    };
  },
};

// ─── translate ────────────────────────────────────────────────────────────────

const translateTool: ToolImpl = {
  name: "translate",
  schema: {
    name: "translate",
    description: "Translate legal text across languages, preserving legal terms of art.",
    input_schema: {
      type: "object" as const,
      properties: {
        text: { type: "string", description: "Text to translate" },
        source_language: { type: "string", description: "Source language code (e.g. 'FR', 'DE', 'IT')" },
        target_language: { type: "string", description: "Target language code (e.g. 'EN')" },
      },
      required: ["text", "target_language"],
    },
  },
  async execute(input, _ctx) {
    const source = (input.source_language as string | undefined) ?? "auto-detect";
    const target = input.target_language as string;
    const text = input.text as string;

    const model = selectModel({ taskType: "translation" });
    const provider = getProvider(model);
    const response = await provider.chat({
      model: resolveModelId(model),
      maxTokens: 2000,
      system: "You are a legal translation specialist. Preserve all legal terms of art.",
      messages: [
        {
          role: "user",
          content: `Translate the following legal text from ${source} to ${target}.
Preserve all legal terms of art. Note where a term has a different legal meaning in the target language.
Output: translated text, then a glossary of key legal terms translated.

TEXT:
${text}`,
        },
      ],
    });

    const block = response.content.find((b) => b.type === "text");
    return { translation: block?.type === "text" ? block.text : "", sourceLang: source, targetLang: target };
  },
};

// ─── citation_check ───────────────────────────────────────────────────────────

const citationCheckTool: ToolImpl = {
  name: "citation_check",
  schema: {
    name: "citation_check",
    description: "Verify a citation by checking whether the quoted text appears verbatim in the source document.",
    input_schema: {
      type: "object" as const,
      properties: {
        source: { type: "string", description: "Document ID or URL to check against" },
        quote: { type: "string", description: "The exact quoted text to verify" },
      },
      required: ["source", "quote"],
    },
  },
  async execute(input, ctx) {
    const source = input.source as string;
    const quote = input.quote as string;

    // Try knowledge store first (doc ID)
    const fullText = await ctx.knowledge.getFullText(source);
    if (fullText) {
      const verified = fullText.includes(quote);
      return {
        source,
        quote,
        verdict: verified ? "VERIFIED" : "NOT_FOUND",
        method: "exact_string_match",
        note: verified
          ? "Quote found verbatim in document"
          : "Quote not found verbatim — may be paraphrased or source is incorrect",
      };
    }

    // Fall back to semantic search for context
    const results = await ctx.knowledge.search(quote, { topK: 3 });
    const topResult = results[0];
    if (topResult && topResult.score > 0.85) {
      const excerptMatch = topResult.document.content.includes(quote);
      return {
        source,
        quote,
        verdict: excerptMatch ? "VERIFIED" : "PARAPHRASE",
        nearestSource: topResult.document.title,
        nearestExcerpt: topResult.excerpt,
        score: topResult.score,
      };
    }

    return { source, quote, verdict: "NOT_FOUND", note: "Source not found in knowledge store" };
  },
};

// ─── ToolRegistry ─────────────────────────────────────────────────────────────

const ALL_TOOLS: ToolImpl[] = [
  webSearchTool,
  searchKnowledgeTool,
  queryMemoryTool,
  extractFromDocumentTool,
  translateTool,
  citationCheckTool,
  pdfExtractTextTool,
  pdfExtractTablesTool,
  pdfGenerateTool,
  pdfOcrTool,
  docusealListTemplatesTool,
  docusealSendForSigningTool,
  docusealSubmissionStatusTool,
  docxGenerateTool,
  tabularReviewTool,
  listDocumentsTool,
  readDocumentTool,
  fetchDocumentsTool,
  findInDocumentTool,
  editDocumentTool,
  replicateDocumentTool,
  readTableCellsTool,
];

export class ToolRegistry {
  private readonly tools: Map<string, ToolImpl>;

  constructor() {
    this.tools = new Map(ALL_TOOLS.map((t) => [t.name, t]));
  }

  /** Return tool schemas for a given set of allowed tool names */
  schemasFor(allowedTools: string[]): ProviderTool[] {
    return allowedTools
      .map((name) => this.tools.get(name)?.schema)
      .filter((s): s is ProviderTool => s !== undefined);
  }

  /** Execute a tool by name, returning the result as a serialisable object */
  async execute(
    name: string,
    input: Record<string, unknown>,
    ctx: ToolContext,
  ): Promise<unknown> {
    const tool = this.tools.get(name);
    if (!tool) throw new Error(`Unknown tool: ${name}`);
    const start = Date.now();
    auditLogger.write({ event: "tool.call", taskId: ctx.taskId, data: { tool: name, input } });
    logger.debug("Tool executing", { tool: name });
    try {
      const result = await tool.execute(input, ctx);
      auditLogger.write({
        event: "tool.result",
        taskId: ctx.taskId,
        durationMs: Date.now() - start,
        data: { tool: name, ok: true },
      });
      return result;
    } catch (err) {
      auditLogger.write({
        event: "tool.result",
        taskId: ctx.taskId,
        durationMs: Date.now() - start,
        data: { tool: name, ok: false, error: (err as Error).message },
      });
      throw err;
    }
  }

  has(name: string): boolean {
    return this.tools.has(name);
  }
}

export const globalToolRegistry = new ToolRegistry();
