// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Parse a LinkedIn data export and extract post text.
 *
 * LinkedIn data export instructions:
 *   https://www.linkedin.com/mypreferences/d/download-my-data
 *   → select "Posts & Articles" → request archive → download ZIP
 *
 * The ZIP contains `Shares.csv` (older accounts) or
 * `Posts and Articles.csv` (newer). Upload either the ZIP or the
 * extracted CSV — both are accepted.
 *
 * The relevant column is "Share Commentary" / "Post Commentary".
 */

import { inflateRawSync } from "node:zlib";
import { basename } from "node:path";
import { logger } from "../logger.js";

// ─── CSV parser (RFC 4180) ────────────────────────────────────────────────────

function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let inQuote = false;

  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (inQuote) {
      if (ch === '"' && text[i + 1] === '"') { field += '"'; i++; }
      else if (ch === '"') { inQuote = false; }
      else { field += ch; }
    } else {
      if (ch === '"') { inQuote = true; }
      else if (ch === ',') { row.push(field); field = ""; }
      else if (ch === "\r" && text[i + 1] === "\n") {
        i++;
        row.push(field); field = "";
        rows.push(row); row = [];
      } else if (ch === "\n") {
        row.push(field); field = "";
        rows.push(row); row = [];
      } else { field += ch; }
    }
  }
  if (field || row.length) { row.push(field); rows.push(row); }
  return rows;
}

function extractPostsFromCSV(csv: string): string[] {
  const rows = parseCSV(csv);
  if (rows.length < 2) return [];
  const headers = rows[0].map((h) => h.toLowerCase().trim());
  // LinkedIn uses "Share Commentary" or "Post Commentary" for the post body
  const colIdx = headers.findIndex(
    (h) => h.includes("commentary") || h === "post" || h === "share commentary",
  );
  if (colIdx === -1) return [];
  return rows
    .slice(1)
    .map((r) => (r[colIdx] ?? "").trim())
    .filter((t) => t.length > 20); // skip empty / very short entries
}

// ─── Minimal ZIP reader (flat archive, STORED or DEFLATE) ────────────────────

// Hard cap on inflated output per entry — protects against zip bombs.
const MAX_INFLATED_BYTES = 50 * 1024 * 1024; // 50 MB
const MAX_TOTAL_INFLATED_BYTES = 100 * 1024 * 1024; // 100 MB aggregate

function readZip(buf: Buffer): Map<string, Buffer> {
  const files = new Map<string, Buffer>();
  let offset = 0;
  let totalInflated = 0;

  while (offset + 30 < buf.length) {
    const sig = buf.readUInt32LE(offset);
    if (sig !== 0x04034b50) break; // local file header signature

    const flags        = buf.readUInt16LE(offset + 6);
    const compression  = buf.readUInt16LE(offset + 8);
    const compressedSize   = buf.readUInt32LE(offset + 18);
    const uncompressedSize = buf.readUInt32LE(offset + 22);
    const filenameLen  = buf.readUInt16LE(offset + 26);
    const extraLen     = buf.readUInt16LE(offset + 28);

    const filenameStart = offset + 30;

    // Bounds-check filename + extra before computing dataStart
    if (filenameStart + filenameLen + extraLen > buf.length) break;

    // Strip directory components, null bytes, and backslashes from filename to prevent path traversal
    const rawFilename = buf.subarray(filenameStart, filenameStart + filenameLen).toString("utf8");
    const filename = basename(rawFilename.replace(/[\x00\\/]/g, "_"));

    const dataStart = filenameStart + filenameLen + extraLen;

    // Bounds-check compressed data before reading
    if (dataStart + compressedSize > buf.length) break;

    // Cap each entry by the *smaller* of the per-entry limit and the remaining
    // aggregate budget, so a series of just-under-50 MB entries cannot transiently
    // allocate far past the 100 MB aggregate cap before the post-hoc check trips.
    const remainingBudget = Math.max(0, MAX_TOTAL_INFLATED_BYTES - totalInflated);
    const entryCap = Math.min(MAX_INFLATED_BYTES, remainingBudget);

    let fileData: Buffer;
    if (compression === 0) {
      // STORED — no decompression needed; cap size to prevent oversized stored entries
      if (compressedSize > entryCap) {
        // Skip oversized stored entry
        fileData = Buffer.alloc(0);
      } else {
        fileData = buf.subarray(dataStart, dataStart + compressedSize);
      }
    } else if (compression === 8) {
      // DEFLATE — reject if uncompressed size header exceeds cap.
      // When the data-descriptor flag (bit 3) is set the header size fields are 0;
      // in that case rely solely on maxOutputLength to enforce the cap.
      if (!(flags & 0x8) && uncompressedSize > entryCap) {
        fileData = Buffer.alloc(0); // entry too large — skip
      } else {
        try {
          fileData = inflateRawSync(
            buf.subarray(dataStart, dataStart + compressedSize),
            { maxOutputLength: entryCap },
          );
        } catch (err) {
          logger.warn("ZIP: failed to inflate entry, skipping", { filename: rawFilename });
          fileData = Buffer.alloc(0);
        }
      }
    } else {
      // Unsupported compression — skip
      fileData = Buffer.alloc(0);
    }

    totalInflated += fileData.length;
    if (totalInflated > MAX_TOTAL_INFLATED_BYTES) {
      logger.warn("ZIP aggregate inflation cap exceeded — stopping extraction");
      break;
    }

    files.set(filename, fileData);

    let next = dataStart + compressedSize;
    // Data descriptor: present when bit 3 of flags is set
    if (flags & 0x8) {
      if (next + 4 <= buf.length && buf.readUInt32LE(next) === 0x08074b50) next += 16;
      else next += 12;
    }
    offset = next;
  }

  return files;
}

// ─── Public API ───────────────────────────────────────────────────────────────

/** Low-level RFC 4180 CSV parse — exported for reuse in writingSamples.ts. */
export { parseCSV, readZip };

/**
 * Parse a LinkedIn data export buffer (ZIP or CSV) and return post text samples.
 * Returns an empty array — never throws — if no posts can be extracted.
 */
export function parseLinkedInExport(buf: Buffer): string[] {
  // Detect ZIP by magic bytes PK\x03\x04
  if (buf[0] === 0x50 && buf[1] === 0x4b && buf[2] === 0x03 && buf[3] === 0x04) {
    try {
      const files = readZip(buf);
      for (const [name, data] of files) {
        const lower = name.toLowerCase();
        if (lower.includes("shares") || lower.includes("posts")) {
          const csv = data.toString("utf8").replace(/^﻿/, "");
          const posts = extractPostsFromCSV(csv);
          if (posts.length) return posts;
        }
      }
    } catch {
      return [];
    }
    return [];
  }

  // Treat as raw CSV
  try {
    return extractPostsFromCSV(buf.toString("utf8").replace(/^﻿/, ""));
  } catch {
    return [];
  }
}
