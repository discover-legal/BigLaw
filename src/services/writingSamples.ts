// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Generic writing-sample extractor.
 *
 * Accepts any of the following file types and returns a flat array of
 * writing-sample strings suitable for tone analysis:
 *
 *   .zip (LinkedIn export)  → Shares.csv / Posts and Articles.csv commentary column
 *   .zip (DOCX)             → word/document.xml → paragraph chunks
 *   .docx                   → word/document.xml → paragraph chunks
 *   .pdf                    → PyMuPDF text extraction → paragraph chunks
 *   .csv (LinkedIn format)  → commentary column
 *   .csv (generic)          → text-richest column, or all cells joined per row
 *   .txt / .md / other text → paragraph chunks
 *
 * The returned sourceType is "linkedin_export" when LinkedIn columns are
 * detected; "writing_samples" for all other sources.
 */

import { mkdir, writeFile, unlink } from "node:fs/promises";
import { join, extname, basename } from "node:path";
import { randomUUID } from "node:crypto";
import { parseCSV, readZip, parseLinkedInExport } from "../linkedin/parser.js";
import { extractTextFromPdf } from "../tools/pdf.js";
import { Config } from "../config.js";
import type { ToneProfile } from "../types.js";

export type SampleSourceType = ToneProfile["sourceType"];

export interface SampleExtractionResult {
  samples: string[];
  sourceType: SampleSourceType;
}

// Minimum character length for a paragraph to count as a writing sample.
const MIN_PARA_LEN = 80;

// ─── Paragraph splitter ───────────────────────────────────────────────────────

function splitIntoParagraphs(text: string): string[] {
  return text
    .split(/\n{2,}/)
    .map((p) => p.replace(/\s+/g, " ").trim())
    .filter((p) => p.length >= MIN_PARA_LEN);
}

// ─── DOCX extraction ──────────────────────────────────────────────────────────

/**
 * Extract prose paragraphs from a DOCX buffer.
 * DOCX files are ZIPs; we read word/document.xml and strip XML tags,
 * using <w:p> elements as paragraph delimiters.
 */
function extractFromDocx(buf: Buffer): string[] {
  try {
    const files = readZip(buf);
    // Try full path then basename fallback
    const xmlBuf = files.get("document.xml") ?? files.get("word/document.xml");
    if (!xmlBuf || !xmlBuf.length) return [];
    const xml = xmlBuf.toString("utf8");
    // Insert newlines at paragraph boundaries before stripping tags
    const withBreaks = xml
      .replace(/<w:p[ >]/g, "\n\n<w:p ")
      .replace(/<w:br[^/]*/g, "\n");
    const plain = withBreaks.replace(/<[^>]+>/g, "");
    return splitIntoParagraphs(plain);
  } catch {
    return [];
  }
}

// ─── Generic CSV extraction ───────────────────────────────────────────────────

/**
 * Extract writing samples from a generic CSV (non-LinkedIn format).
 *
 * Strategy: find the column whose cells have the highest average character
 * count (skipping header row). If no single column dominates, join all cells
 * in each row as one sample.
 */
function extractFromGenericCsv(text: string): string[] {
  const rows = parseCSV(text);
  if (rows.length < 2) return [];

  const dataRows = rows.slice(1);
  const colCount = Math.max(...dataRows.map((r) => r.length), 0);
  if (!colCount) return [];

  // Score each column by average text length across data rows
  const colScores: number[] = Array.from({ length: colCount }, (_, c) => {
    const lengths = dataRows.map((r) => (r[c] ?? "").trim().length);
    return lengths.reduce((a, b) => a + b, 0) / lengths.length;
  });

  const best = colScores.indexOf(Math.max(...colScores));
  const bestAvg = colScores[best] ?? 0;

  if (bestAvg >= MIN_PARA_LEN) {
    // A dominant text column — use it directly
    return dataRows
      .map((r) => (r[best] ?? "").trim())
      .filter((t) => t.length >= MIN_PARA_LEN);
  }

  // No dominant column — join all cells in each row as one sample
  return dataRows
    .map((r) => r.map((c) => c.trim()).filter(Boolean).join(" "))
    .filter((t) => t.length >= MIN_PARA_LEN);
}

// ─── PDF extraction ───────────────────────────────────────────────────────────

async function extractFromPdf(buf: Buffer): Promise<string[]> {
  const dir = join(Config.pdf.outputDir, "tone-import");
  await mkdir(dir, { recursive: true });
  const tmp = join(dir, `${randomUUID()}.pdf`);
  try {
    await writeFile(tmp, buf);
    const text = await extractTextFromPdf(tmp);
    return splitIntoParagraphs(text);
  } finally {
    await unlink(tmp).catch(() => {});
  }
}

// ─── Public API ───────────────────────────────────────────────────────────────

/**
 * Extract writing samples from an uploaded file buffer.
 *
 * Returns `{ samples, sourceType }`.
 * `sourceType` is "linkedin_export" when LinkedIn post columns are found,
 * "writing_samples" for all other sources.
 *
 * Never throws — errors during extraction return an empty sample list.
 */
export async function extractWritingSamples(
  buf: Buffer,
  filename: string,
): Promise<SampleExtractionResult> {
  const ext = extname(filename).toLowerCase();
  const base = basename(filename).toLowerCase();

  // ── ZIP-based formats ────────────────────────────────────────────────────
  const isZip = buf[0] === 0x50 && buf[1] === 0x4b && buf[2] === 0x03 && buf[3] === 0x04;

  if (isZip && ext !== ".docx") {
    // Try LinkedIn export first (Shares.csv / Posts and Articles.csv)
    const linkedInPosts = parseLinkedInExport(buf);
    if (linkedInPosts.length) return { samples: linkedInPosts, sourceType: "linkedin_export" };

    // Fall back to DOCX-style extraction (word/document.xml)
    const docxSamples = extractFromDocx(buf);
    if (docxSamples.length) return { samples: docxSamples, sourceType: "writing_samples" };

    return { samples: [], sourceType: "writing_samples" };
  }

  if (ext === ".docx" || isZip) {
    return { samples: extractFromDocx(buf), sourceType: "writing_samples" };
  }

  // ── PDF ──────────────────────────────────────────────────────────────────
  if (ext === ".pdf") {
    try {
      return { samples: await extractFromPdf(buf), sourceType: "writing_samples" };
    } catch {
      return { samples: [], sourceType: "writing_samples" };
    }
  }

  // ── CSV ──────────────────────────────────────────────────────────────────
  if (ext === ".csv" || base.endsWith(".csv")) {
    // Try LinkedIn column names first
    const linkedInPosts = parseLinkedInExport(buf);
    if (linkedInPosts.length) return { samples: linkedInPosts, sourceType: "linkedin_export" };

    // Fall back to generic CSV extraction
    try {
      const text = buf.toString("utf8");
      return { samples: extractFromGenericCsv(text), sourceType: "writing_samples" };
    } catch {
      return { samples: [], sourceType: "writing_samples" };
    }
  }

  // ── Plain text / Markdown / anything else ────────────────────────────────
  try {
    const text = buf.toString("utf8");
    return { samples: splitIntoParagraphs(text), sourceType: "writing_samples" };
  } catch {
    return { samples: [], sourceType: "writing_samples" };
  }
}
