// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Document tools — ported from Mike (https://github.com/willchen96/mike,
 * AGPL-3.0) and adapted to Big Michael.
 *
 * The read-oriented tools (list_documents, read_document, fetch_documents,
 * find_in_document) operate over Big Michael's Qdrant KnowledgeStore. The
 * binary tools (edit_document, replicate_document) operate over .docx files on
 * disk — the only place Big Michael holds document bytes (docx_generate output,
 * or any path supplied) — because the KnowledgeStore is text-only.
 *
 * find_in_document reuses Mike's whitespace-tolerant Ctrl+F matcher verbatim
 * (normalizeWithMap / normalizeQuery). edit_document wraps the ported
 * docxTrackedChanges engine so generated documents can be redlined in place.
 */

import { readFile, writeFile, mkdir, access } from "fs/promises";
import { isAbsolute, join, dirname, basename, extname, resolve, sep } from "path";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { applyTrackedEdits, type EditInput } from "./docx-tracked-changes.js";
import type { ToolImpl, ToolContext } from "./index.js";

// ─── .docx path resolution ──────────────────────────────────────────────────

// Docx tools are confined to the configured output directory so that a
// prompt-injected agent cannot read or overwrite arbitrary .docx files
// elsewhere on the filesystem.
const DOCX_ROOT = resolve(Config.pdf.outputDir);

async function exists(p: string): Promise<boolean> {
  try {
    await access(p);
    return true;
  } catch {
    return false;
  }
}

/** Resolve a user-supplied path to a .docx, confined to the output directory. */
async function resolveDocxPath(p: string): Promise<string | null> {
  if (!p) return null;
  // Always resolve relative to the output dir; absolute paths are re-anchored
  // there too so agents cannot escape to arbitrary filesystem locations.
  const base = isAbsolute(p) ? basename(p) : p;
  const candidates = [join(DOCX_ROOT, base)];
  for (const c of candidates) {
    const abs = resolve(c);
    if (!abs.startsWith(DOCX_ROOT + sep) && abs !== DOCX_ROOT) continue;
    if (await exists(abs)) return abs;
  }
  logger.warn("Blocked docx path outside output dir", { requested: p });
  return null;
}

// ─── find_in_document matcher (ported verbatim from Mike) ───────────────────

function normalizeWithMap(text: string): { norm: string; origIdx: number[] } {
  const norm: string[] = [];
  const origIdx: number[] = [];
  let prevSpace = false;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (/\s/.test(ch)) {
      if (!prevSpace) {
        norm.push(" ");
        origIdx.push(i);
        prevSpace = true;
      }
    } else {
      norm.push(ch.toLowerCase());
      origIdx.push(i);
      prevSpace = false;
    }
  }
  return { norm: norm.join(""), origIdx };
}

function normalizeQuery(q: string): string {
  return q.trim().replace(/\s+/g, " ").toLowerCase();
}

// ─── list_documents ─────────────────────────────────────────────────────────

export const listDocumentsTool: ToolImpl = {
  name: "list_documents",
  schema: {
    name: "list_documents",
    description:
      "List all documents available in the knowledge store. Returns each document's ID, title, " +
      "jurisdiction, and type. Call this to discover what documents are available before reading them.",
    input_schema: { type: "object" as const, properties: {} },
  },
  async execute(_input, ctx) {
    const docs = await ctx.knowledge.listDocuments(ctx.ownerId);
    return {
      count: docs.length,
      documents: docs.map((d) => ({
        doc_id: d.id,
        title: d.title,
        jurisdiction: d.jurisdiction ?? null,
        document_type: d.documentType ?? null,
      })),
    };
  },
};

// ─── read_document ──────────────────────────────────────────────────────────

const MAX_DOC_CHARS = 200_000;

async function readDocText(docId: string, ctx: ToolContext): Promise<string | null> {
  return ctx.knowledge.getFullText(docId, ctx.ownerId);
}

export const readDocumentTool: ToolImpl = {
  name: "read_document",
  schema: {
    name: "read_document",
    description:
      "Read the full text content of a document in the knowledge store. Always call this before " +
      "answering questions about, summarising, or citing from a document.",
    input_schema: {
      type: "object" as const,
      properties: {
        doc_id: { type: "string", description: "The document ID to read" },
      },
      required: ["doc_id"],
    },
  },
  async execute(input, ctx) {
    const docId = String(input.doc_id ?? "");
    const text = await readDocText(docId, ctx);
    if (text === null) return { ok: false, error: `Document '${docId}' not found.` };
    const truncated = text.length > MAX_DOC_CHARS;
    return {
      ok: true,
      doc_id: docId,
      length: text.length,
      truncated,
      content: truncated ? text.slice(0, MAX_DOC_CHARS) : text,
    };
  },
};

// ─── fetch_documents ────────────────────────────────────────────────────────

export const fetchDocumentsTool: ToolImpl = {
  name: "fetch_documents",
  schema: {
    name: "fetch_documents",
    description:
      "Read the full text of multiple documents in a single call. Use this instead of calling " +
      "read_document repeatedly when you need several documents at once.",
    input_schema: {
      type: "object" as const,
      properties: {
        doc_ids: {
          type: "array",
          items: { type: "string" },
          description: "Array of document IDs to read",
        },
      },
      required: ["doc_ids"],
    },
  },
  async execute(input, ctx) {
    const docIds = (input.doc_ids as string[] | undefined) ?? [];
    const documents = await Promise.all(
      docIds.map(async (docId) => {
        const text = await readDocText(docId, ctx);
        return text === null
          ? { doc_id: docId, ok: false as const, error: "Not found" }
          : {
              doc_id: docId,
              ok: true as const,
              length: text.length,
              truncated: text.length > MAX_DOC_CHARS,
              content: text.slice(0, MAX_DOC_CHARS),
            };
      }),
    );
    return { count: documents.length, documents };
  },
};

// ─── find_in_document ───────────────────────────────────────────────────────

export const findInDocumentTool: ToolImpl = {
  name: "find_in_document",
  schema: {
    name: "find_in_document",
    description:
      "Search for specific strings inside a document — a Ctrl+F equivalent. Returns each match with " +
      "surrounding context so you can locate and quote exact text without reading the whole document. " +
      "Matching is case-insensitive and whitespace-tolerant, so 'Section 4.2' matches 'section   4.2'. " +
      "Use for targeted lookups (clause titles, party names, specific phrases).",
    input_schema: {
      type: "object" as const,
      properties: {
        doc_id: { type: "string", description: "The document ID to search" },
        query: { type: "string", description: "The string to search for (case-insensitive, whitespace-tolerant)" },
        max_results: { type: "integer", description: "Maximum matches to return (default 20)" },
        context_chars: { type: "integer", description: "Characters of context on each side of a match (default 80)" },
      },
      required: ["doc_id", "query"],
    },
  },
  async execute(input, ctx) {
    const docId = String(input.doc_id ?? "");
    const query = String(input.query ?? "");
    const maxResults = typeof input.max_results === "number" ? input.max_results : 20;
    const contextChars = typeof input.context_chars === "number" ? input.context_chars : 80;

    if (!query.trim()) return { ok: false, error: "Empty query." };

    const text = await readDocText(docId, ctx);
    if (text === null) return { ok: false, error: `Document '${docId}' not found.` };

    const { norm, origIdx } = normalizeWithMap(text);
    const needle = normalizeQuery(query);
    if (!needle) return { ok: false, error: "Empty query after normalization." };

    const hits: { index: number; excerpt: string; context: string }[] = [];
    let from = 0;
    while (from <= norm.length - needle.length && hits.length < maxResults) {
      const pos = norm.indexOf(needle, from);
      if (pos < 0) break;
      const endNormPos = pos + needle.length;
      const origStart = origIdx[pos] ?? 0;
      const origEnd = endNormPos - 1 < origIdx.length ? origIdx[endNormPos - 1] + 1 : text.length;
      const ctxStart = Math.max(0, origStart - contextChars);
      const ctxEnd = Math.min(text.length, origEnd + contextChars);
      hits.push({
        index: hits.length,
        excerpt: text.slice(origStart, origEnd),
        context:
          (ctxStart > 0 ? "…" : "") +
          text.slice(ctxStart, ctxEnd).replace(/\s+/g, " ").trim() +
          (ctxEnd < text.length ? "…" : ""),
      });
      from = pos + Math.max(1, needle.length);
    }

    // Count total occurrences beyond the cap so the model knows whether to narrow.
    let totalMatches = hits.length;
    if (hits.length >= maxResults) {
      let probe = from;
      while (probe <= norm.length - needle.length) {
        const pos = norm.indexOf(needle, probe);
        if (pos < 0) break;
        totalMatches++;
        probe = pos + Math.max(1, needle.length);
      }
    }

    return {
      ok: true,
      doc_id: docId,
      query,
      total_matches: totalMatches,
      returned: hits.length,
      truncated: totalMatches > hits.length,
      hits,
    };
  },
};

// ─── edit_document (tracked changes) ────────────────────────────────────────

export const editDocumentTool: ToolImpl = {
  name: "edit_document",
  schema: {
    name: "edit_document",
    description:
      "Propose edits to a .docx file as Word tracked changes. Each edit is a precise, minimal " +
      "substitution of specific words/characters (NOT a whole-paragraph replacement). Anchor each edit " +
      "with short before/after context so it can be located unambiguously. Operates on a .docx file by " +
      "path (e.g. one produced by docx_generate, relative to the output dir, or an absolute path). " +
      "Writes a new redlined .docx and returns per-edit annotations plus the output path.",
    input_schema: {
      type: "object" as const,
      properties: {
        path: {
          type: "string",
          description: "Path to the .docx to edit (absolute, or relative to the document output dir).",
        },
        author: { type: "string", description: "Tracked-change author name (default 'Big Michael')." },
        edits: {
          type: "array",
          description: "List of precise substitutions.",
          items: {
            type: "object",
            properties: {
              find: { type: "string", description: "Exact substring to replace (keep it as short as possible)." },
              replace: { type: "string", description: "Replacement text. Empty string = pure deletion." },
              context_before: { type: "string", description: "~40 chars immediately preceding `find`." },
              context_after: { type: "string", description: "~40 chars immediately following `find`." },
              reason: { type: "string", description: "Short explanation shown on the change card." },
            },
            required: ["find", "replace", "context_before", "context_after"],
          },
        },
      },
      required: ["path", "edits"],
    },
  },
  async execute(input) {
    const rawPath = String(input.path ?? "");
    const editsRaw = (input.edits as EditInput[] | undefined) ?? [];
    const author = input.author ? String(input.author) : undefined;

    const resolved = await resolveDocxPath(rawPath);
    if (!resolved) return { ok: false, error: `File not found: '${rawPath}'.` };
    if (extname(resolved).toLowerCase() !== ".docx") {
      return { ok: false, error: "edit_document only supports .docx files." };
    }
    if (!editsRaw.length) return { ok: false, error: "No edits supplied." };

    let bytes: Buffer;
    try {
      bytes = await readFile(resolved);
    } catch (err) {
      return { ok: false, error: `Could not read file: ${(err as Error).message}` };
    }

    try {
      const { bytes: outBytes, changes, errors } = await applyTrackedEdits(bytes, editsRaw, { author });
      const dir = dirname(resolved);
      const stem = basename(resolved, ".docx");
      const outPath = join(dir, `${stem}.redlined.docx`);
      await mkdir(dir, { recursive: true });
      await writeFile(outPath, outBytes);
      return {
        ok: true,
        outputPath: outPath,
        appliedCount: changes.length,
        errorCount: errors.length,
        annotations: changes,
        errors,
      };
    } catch (err) {
      logger.error("edit_document failed", { path: resolved, error: (err as Error).message });
      return { ok: false, error: (err as Error).message };
    }
  },
};

// ─── replicate_document ─────────────────────────────────────────────────────

export const replicateDocumentTool: ToolImpl = {
  name: "replicate_document",
  schema: {
    name: "replicate_document",
    description:
      "Make byte-for-byte copies of an existing .docx file as new files. Use when you want standalone " +
      "copies to edit (e.g. 'use this NDA as a template', 'give me three drafts I can adapt') without " +
      "modifying the original. Pass `count` to create multiple copies in one call. Returns the new file " +
      "paths so you can immediately call edit_document on them.",
    input_schema: {
      type: "object" as const,
      properties: {
        path: { type: "string", description: "Path to the source .docx (absolute, or relative to the output dir)." },
        count: { type: "integer", description: "How many copies to create (default 1, max 20).", minimum: 1, maximum: 20 },
        new_filename: { type: "string", description: "Optional base filename for the copies (extension forced to .docx)." },
      },
      required: ["path"],
    },
  },
  async execute(input) {
    const rawPath = String(input.path ?? "");
    const count = Math.min(Math.max(Number(input.count ?? 1) || 1, 1), 20);
    const resolved = await resolveDocxPath(rawPath);
    if (!resolved) return { ok: false, error: `File not found: '${rawPath}'.` };
    if (extname(resolved).toLowerCase() !== ".docx") {
      return { ok: false, error: "replicate_document only supports .docx files." };
    }

    let bytes: Buffer;
    try {
      bytes = await readFile(resolved);
    } catch (err) {
      return { ok: false, error: `Could not read file: ${(err as Error).message}` };
    }

    const dir = dirname(resolved);
    const base = input.new_filename
      ? basename(String(input.new_filename), ".docx")
      : basename(resolved, ".docx");
    await mkdir(dir, { recursive: true });

    const copies: { path: string; filename: string }[] = [];
    for (let i = 1; i <= count; i++) {
      const filename = count > 1 ? `${base} (${i}).docx` : `${base}.docx`;
      const outPath = join(dir, filename);
      await writeFile(outPath, bytes);
      copies.push({ path: outPath, filename });
    }
    return { ok: true, count: copies.length, copies };
  },
};

// ─── read_table_cells (reads a persisted tabular_review result) ─────────────

interface PersistedReview {
  reviewId: string;
  columns: string[];
  rows: { documentId: string; document: string; cells: { column: string; summary: string; flag: string; reasoning: string }[] }[];
}

export function tabularReviewPath(reviewId: string): string {
  const safe = reviewId.replace(/[^a-zA-Z0-9_-]/g, "");
  return join(Config.pdf.outputDir, `tabular-review-${safe}.json`);
}

export const readTableCellsTool: ToolImpl = {
  name: "read_table_cells",
  schema: {
    name: "read_table_cells",
    description:
      "Read extracted cells from a prior tabular_review (by its review_id). Each cell holds the value " +
      "extracted for a specific column from a specific document, with its RAG flag and reasoning. Pass " +
      "col_indices and/or row_indices (0-based) to read a subset; omit either to read all columns or rows.",
    input_schema: {
      type: "object" as const,
      properties: {
        review_id: { type: "string", description: "The review_id returned by tabular_review." },
        col_indices: { type: "array", items: { type: "integer" }, description: "0-based column indices (omit for all)." },
        row_indices: { type: "array", items: { type: "integer" }, description: "0-based document (row) indices (omit for all)." },
      },
      required: ["review_id"],
    },
  },
  async execute(input) {
    const reviewId = String(input.review_id ?? "");
    const colIndices = input.col_indices as number[] | undefined;
    const rowIndices = input.row_indices as number[] | undefined;

    let review: PersistedReview;
    try {
      review = JSON.parse(await readFile(tabularReviewPath(reviewId), "utf8")) as PersistedReview;
    } catch {
      return { ok: false, error: `Review '${reviewId}' not found. Run tabular_review first.` };
    }

    const cols = colIndices?.length
      ? review.columns.map((name, i) => ({ name, i })).filter((c) => colIndices.includes(c.i))
      : review.columns.map((name, i) => ({ name, i }));
    const rows = rowIndices?.length
      ? review.rows.map((r, i) => ({ r, i })).filter((x) => rowIndices.includes(x.i))
      : review.rows.map((r, i) => ({ r, i }));

    const cells: Record<string, unknown>[] = [];
    for (const col of cols) {
      for (const row of rows) {
        const cell = row.r.cells.find((c) => c.column === col.name);
        cells.push({
          col: col.i,
          column: col.name,
          row: row.i,
          document: row.r.document,
          summary: cell?.summary ?? "(not generated)",
          flag: cell?.flag ?? "grey",
          reasoning: cell?.reasoning ?? "",
        });
      }
    }
    return { ok: true, review_id: reviewId, columns: cols.map((c) => c.name), cellCount: cells.length, cells };
  },
};
