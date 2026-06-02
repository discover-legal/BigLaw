// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Tabular review tool — multi-document, column-based extraction into a matrix.
 *
 * Ported from Mike (https://github.com/willchen96/mike, AGPL-3.0) — its tabular
 * review / extraction engine — and adapted to Big Michael's provider abstraction
 * and knowledge store.
 *
 * Each (document × column) cell is an independent extraction returning a RAG flag
 * (green / grey / yellow / red), a cited summary, and reasoning. Pinpoint citations
 * use Mike's [[page:N||quote:...]] format. Because each cell is a discrete Finding,
 * cells can be routed through Big Michael's debate + verification protocols — the
 * combination that makes this stronger than either upstream alone.
 */

import { randomUUID } from "crypto";
import { writeFile, mkdir } from "fs/promises";
import { getProvider, resolveModelId } from "../providers/index.js";
import { selectModel } from "../routing/model.js";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { tabularReviewPath } from "./documents.js";
import type { ToolImpl } from "./index.js";

// EXTRACTION_SYSTEM is taken from Mike (AGPL-3.0); see NOTICE.
const EXTRACTION_SYSTEM = `You are a legal document analyst. Return ONLY valid JSON:
{"summary": string, "flag": "green"|"grey"|"yellow"|"red", "reasoning": string}

The "summary" and "reasoning" field values may use markdown formatting — the values are still plain JSON strings (escape newlines as \\n).

The "summary" field must contain only the extracted value with inline citations — no explanation or reasoning. Every factual claim in "summary" must be followed immediately by a citation in the format [[page:N||quote:exact quoted text]], where N is the page number and the quote is a short verbatim excerpt (≤ 25 words), narrowly scoped to the specific claim it supports. Do not have multiple claims share the same long quote. All reasoning and explanation belongs in "reasoning" only.

Flag meaning: green = clearly addressed/favourable; grey = not addressed / not found; yellow = present but qualified, unusual, or needs review; red = problematic, onerous, or non-market.`;

const FLAGS = ["green", "grey", "yellow", "red"] as const;
type Flag = (typeof FLAGS)[number];

interface Column { name: string; prompt: string }
interface Cell { column: string; summary: string; flag: Flag; reasoning: string }

function parseCell(raw: string): { summary: string; flag: Flag; reasoning: string } {
  try {
    const cleaned = raw.replace(/^```(?:json)?\n?/i, "").replace(/\n?```$/, "").trim();
    const p = JSON.parse(cleaned) as { summary?: unknown; value?: unknown; flag?: unknown; reasoning?: unknown };
    return {
      summary: String(p.summary ?? p.value ?? "").trim() || "Not addressed",
      flag: (FLAGS as readonly string[]).includes(p.flag as string) ? (p.flag as Flag) : "grey",
      reasoning: String(p.reasoning ?? ""),
    };
  } catch {
    return { summary: raw.trim().slice(0, 500) || "Not addressed", flag: "grey", reasoning: "" };
  }
}

export const tabularReviewTool: ToolImpl = {
  name: "tabular_review",
  schema: {
    name: "tabular_review",
    description:
      "Run a tabular review across one or more documents. Define columns (each a question/field to extract); " +
      "for every document × column the tool extracts a cited answer with a RAG flag (green/grey/yellow/red) and reasoning. " +
      "Returns a matrix suitable for due-diligence, CP checklists, or comparison tables.",
    input_schema: {
      type: "object" as const,
      properties: {
        documentIds: { type: "array", items: { type: "string" }, description: "Knowledge-store document IDs to review (rows; capped at 50)" },
        columns: {
          type: "array",
          description: "Columns (fields) to extract. Each has a name and an extraction prompt (capped at 30).",
          items: {
            type: "object",
            properties: {
              name: { type: "string", description: "Column header" },
              prompt: { type: "string", description: "What to extract for this column" },
            },
            required: ["name", "prompt"],
          },
        },
      },
      required: ["documentIds", "columns"],
    },
  },
  async execute(input, ctx) {
    const MAX_DOCS = 50;
    const MAX_COLS = 30;
    const documentIds = ((input.documentIds as string[] | undefined) ?? []).slice(0, MAX_DOCS);
    const columns = ((input.columns as Column[] | undefined) ?? []).slice(0, MAX_COLS);
    if (!documentIds.length || !columns.length) {
      return { error: "tabular_review requires documentIds and columns", rows: [] };
    }

    const model = selectModel({ taskType: "extraction" });
    const provider = getProvider(model);
    const resolved = resolveModelId(model);

    const rows: { documentId: string; document: string; cells: Cell[] }[] = [];

    for (const docId of documentIds) {
      const text = (await ctx.knowledge.getFullText(docId)) ?? "";
      const docLabel = text ? docId : `${docId} (not found)`;
      const cells = await Promise.all(
        columns.map(async (col): Promise<Cell> => {
          if (!text) return { column: col.name, summary: "Document not found", flag: "grey", reasoning: "" };
          try {
            const response = await provider.chat({
              model: resolved,
              maxTokens: 1200,
              system: EXTRACTION_SYSTEM,
              messages: [{
                role: "user",
                content: `Document: ${docId}\n\n${text.slice(0, 120_000)}\n\n---\nInstruction: ${col.prompt} If not found, state "Not Found". Leave all reasoning in the "reasoning" field only.`,
              }],
            });
            const block = response.content.find((b) => b.type === "text");
            const raw = block?.type === "text" ? block.text : "";
            return { column: col.name, ...parseCell(raw) };
          } catch (err) {
            logger.warn("tabular_review cell failed", { docId, column: col.name, error: (err as Error).message });
            return { column: col.name, summary: "Extraction failed", flag: "grey", reasoning: (err as Error).message };
          }
        }),
      );
      rows.push({ documentId: docId, document: docLabel, cells });
    }

    const flagTally = rows.flatMap((r) => r.cells).reduce((a, c) => ((a[c.flag] = (a[c.flag] ?? 0) + 1), a), {} as Record<string, number>);

    // Persist the matrix so read_table_cells can read col/row subsets later in the run.
    const reviewId = randomUUID();
    let persisted = false;
    try {
      await mkdir(Config.pdf.outputDir, { recursive: true });
      await writeFile(
        tabularReviewPath(reviewId),
        JSON.stringify({ reviewId, columns: columns.map((c) => c.name), rows }),
        "utf8",
      );
      persisted = true;
    } catch (err) {
      logger.warn("tabular_review: failed to persist result — read_table_cells will not work", { error: (err as Error).message });
    }

    return {
      // Only expose reviewId when the file was actually written; a null reviewId
      // prevents a confusing "review not found" error from read_table_cells.
      reviewId: persisted ? reviewId : null,
      columns: columns.map((c) => c.name),
      rows,
      flagTally,
      legend: { green: "addressed/favourable", grey: "not found", yellow: "qualified/review", red: "problematic/non-market" },
    };
  },
};
