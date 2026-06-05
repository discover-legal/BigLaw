// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * SplunkSink — Splunk HTTP Event Collector (HEC) sink.
 *
 * Batches events and POSTs them to `/services/collector/event` as a sequence
 * of JSON HEC event objects. Flushes every FLUSH_INTERVAL_MS or BATCH_SIZE,
 * whichever comes first.
 *
 * No npm dependencies — raw fetch only.
 *
 * Security:
 *   - URL is SSRF-validated at construction (http/https only).
 *   - HEC token is never logged.
 *   - Response body capped at 64 KB.
 */

import type { AuditEntry, AuditSink } from "../index.js";
import { logger } from "../../logger.js";
import { validateSinkUrl } from "./utils.js";

const BATCH_SIZE = 100;
const FLUSH_INTERVAL_MS = 1_000;
const MAX_RESPONSE_BYTES = 64 * 1024;
const MAX_BACKLOG = BATCH_SIZE * 10;
const SOURCE_TYPE = "big_michael:audit";

interface HecEvent {
  time: number;
  sourcetype: string;
  event: AuditEntry;
}

export class SplunkSink implements AuditSink {
  readonly name = "splunk";

  private readonly hecUrl: string;
  private readonly headers: Record<string, string>;
  private batch: AuditEntry[] = [];
  private timer: ReturnType<typeof setTimeout> | null = null;
  private flushing = false;

  constructor(url: string, token: string, index?: string) {
    const validated = validateSinkUrl(url, "SplunkSink");
    // Splunk HEC endpoint
    this.hecUrl = `${validated.origin}/services/collector/event`;
    this.headers = {
      "Content-Type": "application/json",
      Authorization: `Splunk ${token}`,
    };
    if (index) this.headers["X-Splunk-Request-Channel"] = index;
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
      // HEC accepts a sequence of concatenated JSON objects (not an array)
      const body = toFlush
        .map((entry): HecEvent => ({
          time: Date.parse(entry.ts) / 1000,
          sourcetype: SOURCE_TYPE,
          event: entry,
        }))
        .map((e) => JSON.stringify(e))
        .join("\n");

      const ctrl = new AbortController();
      const timeout = setTimeout(() => ctrl.abort(), 30_000);
      try {
        const res = await fetch(this.hecUrl, {
          method: "POST",
          headers: this.headers,
          body,
          signal: ctrl.signal,
        });
        clearTimeout(timeout);
        if (!res.ok) {
          const text = await readCapped(res, MAX_RESPONSE_BYTES);
          logger.warn("SplunkSink: HEC error", { status: res.status, body: text.slice(0, 500) });
          failed = true;
        }
      } catch (err) {
        clearTimeout(timeout);
        logger.warn("SplunkSink: fetch error", { error: (err as Error).message });
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
