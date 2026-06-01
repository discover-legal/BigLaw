// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * PDF tool implementations — wraps scripts/pdf_tools.py via child_process.
 *
 * Three tools:
 *   pdf_extract_text   — PyMuPDF: text + block structure from any PDF
 *   pdf_extract_tables — Camelot: table extraction (lattice → stream fallback)
 *   pdf_generate       — PyMuPDF Story: create paginated legal PDFs from
 *                         markdown strings or structured section arrays
 */

import { spawn } from "child_process";
import { join, dirname, resolve, basename, sep } from "path";
import { fileURLToPath } from "url";
import { tmpdir } from "os";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import type { ToolImpl } from "./index.js";

const SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "../../scripts/pdf_tools.py");

// ─── Path safety ──────────────────────────────────────────────────────────────
// The read tools accept a caller-supplied file path. Without a guard, an agent
// induced via prompt injection could read arbitrary files (.env, key material,
// system files) and surface their contents in findings. Restrict reads to an
// allow-list of base directories.
const ALLOWED_READ_ROOTS = (
  Config.pdf.allowedDirs.length
    ? Config.pdf.allowedDirs
    : [process.cwd(), tmpdir(), Config.pdf.outputDir]
).map((d) => resolve(d));

export function assertSafeReadPath(p: unknown): string {
  if (typeof p !== "string" || !p.trim()) {
    throw new Error("A file path is required.");
  }
  const abs = resolve(p);
  const ok = ALLOWED_READ_ROOTS.some((root) => abs === root || abs.startsWith(root + sep));
  if (!ok) {
    logger.warn("Blocked PDF tool read outside allowed roots", { requested: p });
    throw new Error(
      `Refusing to read '${p}': path is outside the allowed directories ` +
      `(${ALLOWED_READ_ROOTS.join(", ")}). Set PDF_ALLOWED_DIRS to widen.`,
    );
  }
  return abs;
}

// ─── Shared python runner ─────────────────────────────────────────────────────

async function runPython(operation: string, args: unknown): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const python = Config.pdf.pythonBin;
    const child = spawn(python, [SCRIPT, operation, JSON.stringify(args)]);

    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (d) => { stdout += d; });
    child.stderr.on("data", (d) => { stderr += d; });

    child.on("close", (code) => {
      if (code !== 0) {
        logger.error("pdf_tools.py exited with error", { operation, code, stderr: stderr.slice(0, 500) });
        try {
          resolve(JSON.parse(stdout)); // script writes error JSON to stdout
        } catch {
          reject(new Error(`pdf_tools.py failed (exit ${code}): ${stderr.slice(0, 200)}`));
        }
        return;
      }
      try {
        resolve(JSON.parse(stdout));
      } catch {
        reject(new Error(`Failed to parse pdf_tools.py output: ${stdout.slice(0, 200)}`));
      }
    });

    child.on("error", (err) => {
      reject(new Error(`Failed to spawn ${python}: ${err.message}. Is Python 3 installed?`));
    });
  });
}

/** Extract plain text from a PDF on disk (used by the document-upload route). */
export async function extractTextFromPdf(absPath: string): Promise<string> {
  const result = await runPython("extract_text", { path: assertSafeReadPath(absPath) }) as { pages?: Array<{ text?: string }> };
  return (result.pages ?? []).map((p) => p.text ?? "").join("\n\n").trim();
}

// ─── pdf_extract_text ─────────────────────────────────────────────────────────

export const pdfExtractTextTool: ToolImpl = {
  name: "pdf_extract_text",
  schema: {
    name: "pdf_extract_text",
    description:
      "Extract full text and block structure from a PDF file using PyMuPDF. " +
      "Returns page-by-page text and bounding-box blocks for layout analysis.",
    input_schema: {
      type: "object" as const,
      properties: {
        path: { type: "string", description: "Absolute or relative path to the PDF file" },
        pages: {
          type: "string",
          description: "Optional page range to extract, e.g. '1-5' or '3'. Defaults to all pages.",
        },
      },
      required: ["path"],
    },
  },
  async execute(input, _ctx) {
    return runPython("extract_text", {
      path: assertSafeReadPath(input.path),
      pages: input.pages,
    });
  },
};

// ─── pdf_extract_tables ───────────────────────────────────────────────────────

export const pdfExtractTablesTool: ToolImpl = {
  name: "pdf_extract_tables",
  schema: {
    name: "pdf_extract_tables",
    description:
      "Extract tables from a PDF using Camelot. Returns each table as an array of rows " +
      "with headers and data cells. Automatically falls back from lattice to stream mode " +
      "if no bordered tables are found.",
    input_schema: {
      type: "object" as const,
      properties: {
        path: { type: "string", description: "Absolute or relative path to the PDF file" },
        pages: {
          type: "string",
          description: "Pages to scan, e.g. 'all', '1', '1-3'. Defaults to 'all'.",
        },
        flavor: {
          type: "string",
          enum: ["lattice", "stream"],
          description:
            "lattice (default): bordered tables with ruled lines. " +
            "stream: whitespace-separated columns (no visible borders).",
        },
      },
      required: ["path"],
    },
  },
  async execute(input, _ctx) {
    return runPython("extract_tables", {
      path: assertSafeReadPath(input.path),
      pages: input.pages ?? "all",
      flavor: input.flavor ?? "lattice",
    });
  },
};

// ─── pdf_generate ─────────────────────────────────────────────────────────────

export const pdfGenerateTool: ToolImpl = {
  name: "pdf_generate",
  schema: {
    name: "pdf_generate",
    description:
      "Generate a properly formatted legal PDF document using PyMuPDF. " +
      "Accepts either a markdown string or a structured sections array. " +
      "Handles automatic pagination, justified text, headings, and bullet lists. " +
      "Returns the output file path and page count.",
    input_schema: {
      type: "object" as const,
      properties: {
        title: { type: "string", description: "Document title (rendered as H1 on first page)" },
        filename: {
          type: "string",
          description: "Output filename, e.g. 'competition-brief-2026.pdf'",
        },
        content: {
          description:
            "Document body as a markdown string, " +
            "or a structured array: [{heading, content, subsections?}]",
        },
        author: { type: "string", description: "Optional author / firm name for the metadata line" },
        confidential: {
          type: "boolean",
          description: "If true, adds a CONFIDENTIAL — LEGALLY PRIVILEGED banner",
        },
      },
      required: ["title", "filename", "content"],
    },
  },
  async execute(input, _ctx) {
    return runPython("generate", {
      title: input.title,
      // Sanitise to a bare filename so the output can't escape output_dir via
      // path separators or traversal sequences in a caller-supplied name.
      filename: basename(String(input.filename)),
      content: input.content,
      output_dir: Config.pdf.outputDir,
      author: input.author,
      confidential: input.confidential ?? false,
    });
  },
};

// ─── pdf_ocr ──────────────────────────────────────────────────────────────────

export const pdfOcrTool: ToolImpl = {
  name: "pdf_ocr",
  schema: {
    name: "pdf_ocr",
    description:
      "OCR a scanned PDF or image file using Tesseract 5. " +
      "PDF pages are rasterised at 300 DPI via PyMuPDF before OCR so quality " +
      "is consistent for any scan resolution. Returns full text and per-page breakdown. " +
      "Useful for scanned regulatory filings, court documents, and client contracts " +
      "that contain no embedded selectable text.",
    input_schema: {
      type: "object" as const,
      properties: {
        path: { type: "string", description: "Absolute path to the PDF or image file (PNG/JPEG/TIFF)" },
        lang: {
          type: "string",
          description:
            "Tesseract language code(s). Default 'eng'. " +
            "Multi-language: 'eng+fra', 'eng+deu', etc. " +
            "Available: eng, fra, deu, ita, spa, nld, por",
        },
        pages: {
          type: "string",
          description: "Page range for PDFs, e.g. '1-3' or '2'. Defaults to all pages.",
        },
        dpi: {
          type: "number",
          description: "Rasterisation DPI for PDF pages. Default 300. Higher = better OCR, slower.",
        },
      },
      required: ["path"],
    },
  },
  async execute(input, _ctx) {
    const rawLang = String(input.lang ?? "eng");
    // Tesseract lang codes are lowercase alpha + optional "+"-joined codes (e.g. "eng+fra").
    // Reject anything that doesn't match to prevent unexpected values reaching the binary.
    const lang = /^[a-z]{2,8}(\+[a-z]{2,8})*$/.test(rawLang) ? rawLang : "eng";
    return runPython("ocr", {
      path: assertSafeReadPath(input.path),
      lang,
      pages: input.pages,
      dpi: input.dpi ?? 300,
    });
  },
};
