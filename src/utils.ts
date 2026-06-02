// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import { writeFile, rename } from "fs/promises";
import type { ProviderContentBlock } from "./providers/types.js";

/**
 * Atomic JSON write: serialise data to a .tmp file then rename it into place.
 * A crash mid-write never leaves a partially-written file.
 * @param pretty - true (default) for 2-space indentation; false for compact.
 */
export async function atomicWriteJson(path: string, data: unknown, pretty = true): Promise<void> {
  const tmp = `${path}.tmp`;
  await writeFile(tmp, JSON.stringify(data, null, pretty ? 2 : undefined), "utf8");
  await rename(tmp, path);
}

/**
 * Extract the text from the first text block in a provider content array.
 * Returns an empty string when no text block is present.
 */
export function extractFirstText(content: ProviderContentBlock[]): string {
  const block = content.find((b) => b.type === "text");
  return block?.type === "text" ? block.text : "";
}

/**
 * Best-effort extraction of a single JSON object from an LLM response.
 * Strips markdown fences and isolates the outermost {...} before parsing.
 */
export function parseJsonObject(text: string): unknown | undefined {
  const stripped = text.replace(/```(?:json)?/gi, "").trim();
  const start = stripped.indexOf("{");
  const end = stripped.lastIndexOf("}");
  if (start === -1 || end === -1 || end <= start) return undefined;
  try {
    return JSON.parse(stripped.slice(start, end + 1));
  } catch {
    return undefined;
  }
}
