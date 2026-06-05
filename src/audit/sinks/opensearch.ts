// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * OpenSearchSink — batched bulk-indexing audit sink for OpenSearch / Elasticsearch.
 *
 * Entries are buffered and flushed via _bulk every FLUSH_INTERVAL_MS or when the
 * batch reaches BATCH_SIZE, whichever comes first. Each entry lands in a monthly
 * index: `big-michael-audit-YYYY.MM`.
 *
 * No npm dependencies — raw fetch only.
 *
 * Security:
 *   - URL is SSRF-validated at construction time (http/https only, no private ranges).
 *   - API key is never logged.
 *   - Response body read is capped at 64 KB.
 */

import type { AuditEntry, AuditSink } from "../index.js";
import { logger } from "../../logger.js";
import { validateSinkUrl } from "./utils.js";

const BATCH_SIZE = 100;
const FLUSH_INTERVAL_MS = 1_000;
const MAX_RESPONSE_BYTES = 64 * 1024;
const MAX_BACKLOG = BATCH_SIZE * 10;

function monthlyIndex(): string {
  const now = new Date();
  const yyyy = now.getUTCFullYear();
  const mm = String(now.getUTCMonth() + 1).padStart(2, "0");
  return `big-michael-audit-${yyyy}.${mm}`;
}

export class OpenSearchSink implements AuditSink {
  readonly name = "opensearch";

  private readonly baseUrl: string;
  private readonly headers: Record<string, string>;
  private batch: AuditEntry[] = [];
  private timer: ReturnType<typeof setTimeout> | null = null;
  private flushing = false;

  constructor(url: string, apiKey: string) {
    const validated = validateSinkUrl(url, "OpenSearchSink");
    this.baseUrl = validated.origin;
    this.headers = {
      "Content-Type": "application/x-ndjson",
      ...(apiKey ? { Authorization: `ApiKey ${apiKey}` } : {}),
    };
    this.scheduleFlush();
  }

  write(entry: AuditEntry): void {
    this.batch.push(entry);
    if (this.batch.length >= BATCH_SIZE) {
      this.triggerFlush();
    }
  }

  async flush(): Promise<void> {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    await this.doFlush();
  }

  private scheduleFlush(): void {
    this.timer = setTimeout(() => {
      this.triggerFlush();
      this.scheduleFlush();
    }, FLUSH_INTERVAL_MS);
    // Unref so the timer doesn't keep the process alive on shutdown
    if (this.timer.unref) this.timer.unref();
  }

  private triggerFlush(): void {
    if (this.flushing || this.batch.length === 0) return;
    this.doFlush().catch(() => undefined);
  }

  private async doFlush(): Promise<void> {
    if (this.batch.length === 0) return;
    this.flushing = true;
    const toFlush = this.batch.splice(0, this.batch.length);
    let failed = false;
    try {
      const index = monthlyIndex();
      const lines: string[] = [];
      for (const entry of toFlush) {
        lines.push(JSON.stringify({ index: { _index: index, _id: entry.id } }));
        lines.push(JSON.stringify(entry));
      }
      const body = lines.join("\n") + "\n";

      const ctrl = new AbortController();
      const timeout = setTimeout(() => ctrl.abort(), 30_000);
      try {
        const res = await fetch(`${this.baseUrl}/_bulk`, {
          method: "POST",
          headers: this.headers,
          body,
          signal: ctrl.signal,
        });
        clearTimeout(timeout);
        if (!res.ok) {
          const text = await readCapped(res, MAX_RESPONSE_BYTES);
          logger.warn("OpenSearchSink: bulk index error", { status: res.status, body: text.slice(0, 500) });
          failed = true;
        }
      } catch (err) {
        clearTimeout(timeout);
        logger.warn("OpenSearchSink: fetch error", { error: (err as Error).message });
        failed = true;
      }
    } finally {
      if (failed && this.batch.length < MAX_BACKLOG) {
        this.batch.unshift(...toFlush);
      }
      this.flushing = false;
    }
  }
}

async function readCapped(res: Response, maxBytes: number): Promise<string> {
  const reader = res.body?.getReader();
  if (!reader) return "";
  const chunks: Uint8Array[] = [];
  let total = 0;
  while (true) {
    const { done, value } = await reader.read();
    if (done || !value) break;
    chunks.push(value);
    total += value.byteLength;
    if (total >= maxBytes) { reader.cancel().catch(() => undefined); break; }
  }
  return Buffer.concat(chunks).toString("utf-8");
}
