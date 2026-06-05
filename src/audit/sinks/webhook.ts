// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * WebhookSink — generic HTTP POST audit sink.
 *
 * POSTs each event individually as a JSON body to the configured URL.
 * Supports optional Bearer token auth. Works with Datadog Logs API,
 * Azure Monitor Data Collector, custom SIEM webhooks, etc.
 *
 * No npm dependencies — raw fetch only.
 *
 * Security:
 *   - URL is SSRF-validated at construction (http/https only).
 *   - Bearer token is never logged.
 *   - Response body capped at 64 KB.
 */

import type { AuditEntry, AuditSink } from "../index.js";
import { logger } from "../../logger.js";
import { validateSinkUrl } from "./utils.js";

const MAX_RESPONSE_BYTES = 64 * 1024;

export class WebhookSink implements AuditSink {
  readonly name = "webhook";

  private readonly url: string;
  private readonly headers: Record<string, string>;

  constructor(url: string, token?: string) {
    const validated = validateSinkUrl(url, "WebhookSink");
    this.url = validated.href;
    this.headers = {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    };
  }

  write(entry: AuditEntry): void {
    this.post(entry).catch(() => undefined);
  }

  async flush(): Promise<void> {
    // Webhook is per-event — nothing to drain
  }

  private async post(entry: AuditEntry): Promise<void> {
    const ctrl = new AbortController();
    const timeout = setTimeout(() => ctrl.abort(), 30_000);
    try {
      const res = await fetch(this.url, {
        method: "POST",
        headers: this.headers,
        body: JSON.stringify(entry),
        signal: ctrl.signal,
      });
      clearTimeout(timeout);
      if (!res.ok) {
        const text = await readCapped(res, MAX_RESPONSE_BYTES);
        logger.warn("WebhookSink: POST error", { status: res.status, body: text.slice(0, 500) });
      }
    } catch (err) {
      clearTimeout(timeout);
      logger.warn("WebhookSink: fetch error", { error: (err as Error).message });
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
