// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

// DocketMonitor — watches CourtListener for new filings on registered dockets.
// Auto-ingests new opinions into the knowledge store and emits SSE alerts.
// Enabled when DOCKET_MONITOR_ENABLED=true. No API key required (optional for rate limits).

import { EventEmitter } from "events";
import { randomUUID } from "crypto";
import { readFile, writeFile, rename } from "fs/promises";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { KnowledgeStore } from "../knowledge/index.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export interface WatchedDocket {
  matterNumber: string;
  docketNumber: string;       // e.g. "1:23-cv-01234"
  court: string;              // CourtListener court slug, e.g. "dcd", "nysd", "ca9"
  caseName?: string;
  addedAt: string;            // ISO
  lastCheckedAt?: string;     // ISO
  lastFilingDate?: string;    // ISO — most recent filing we've seen
  totalFilingsSeen: number;
}

export interface DocketAlert {
  id: string;
  matterNumber: string;
  docketNumber: string;
  court: string;
  caseName: string;
  newFilingCount: number;
  latestFilingDate: string;   // ISO
  courtListenerUrl: string;
  detectedAt: string;         // ISO
}

// ─── CourtListener API shape (partial) ────────────────────────────────────────

interface CourtListenerDocket {
  id: number;
  case_name: string;
  date_filed: string | null;
  date_last_filing: string | null;
  docket_entries_count: number;
}

interface CourtListenerResponse {
  results?: CourtListenerDocket[];
}

// ─── Constants ────────────────────────────────────────────────────────────────

const COURT_LISTENER_API = "https://www.courtlistener.com/api/rest/v4";
const REQUEST_TIMEOUT_MS = 30_000;
const MAX_RESPONSE_BYTES = 1 * 1024 * 1024; // 1 MB

// Validates a docket number: word characters, hyphens, colons, dots, slashes
const DOCKET_NUMBER_RE = /^[\w\-:\.\/]+$/;
// Validates a CourtListener court slug: lowercase alphanumeric only
const COURT_SLUG_RE = /^[a-z0-9]+$/;

// ─── DocketMonitor ────────────────────────────────────────────────────────────

export class DocketMonitor extends EventEmitter {
  private watched: Map<string, WatchedDocket> = new Map();  // key: matterNumber
  private timer: ReturnType<typeof setInterval> | null = null;
  private readonly path: string;          // persistence file
  private writeChain = Promise.resolve(); // serialize writes
  private knowledge: KnowledgeStore | null = null;

  constructor(path: string) {
    super();
    this.path = path;
  }

  /** Attach a KnowledgeStore for auto-ingesting new filings. */
  setKnowledgeStore(store: KnowledgeStore): void {
    this.knowledge = store;
  }

  /** True when DOCKET_MONITOR_ENABLED=true. */
  isEnabled(): boolean {
    return Config.dockets.enabled;
  }

  /** Load persisted watched dockets from disk. */
  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      const entries = JSON.parse(raw) as WatchedDocket[];
      for (const entry of entries) {
        this.watched.set(entry.matterNumber, entry);
      }
      logger.info("DocketMonitor: loaded watched dockets", { count: this.watched.size });
    } catch {
      // No persistence file yet — start empty
      this.watched = new Map();
    }
  }

  /** Start the background polling loop. */
  start(): void {
    if (this.timer) return; // already running
    const intervalMs = Config.dockets.pollIntervalMs;

    const tick = async () => {
      try {
        await this.checkAll();
      } catch (err) {
        logger.warn("DocketMonitor: checkAll error", { error: (err as Error).message });
      }
    };

    this.timer = setInterval(() => { void tick(); }, intervalMs);
    // Allow Node.js to exit if this is the only remaining handle
    if (this.timer.unref) this.timer.unref();
    logger.info("DocketMonitor: started", { intervalMs, watching: this.watched.size });
  }

  /** Stop the background polling loop. */
  stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
      logger.info("DocketMonitor: stopped");
    }
  }

  // ─── Watch management ──────────────────────────────────────────────────────

  /**
   * Register a docket for monitoring.
   * Validates docketNumber and court slug before accepting.
   */
  watch(
    matterNumber: string,
    docketNumber: string,
    court: string,
    caseName?: string,
  ): WatchedDocket {
    if (!matterNumber || typeof matterNumber !== "string") {
      throw new Error("matterNumber is required");
    }
    if (!docketNumber || !DOCKET_NUMBER_RE.test(docketNumber) || docketNumber.length > 50) {
      throw new Error(
        "docketNumber must match /^[\\w\\-:\\.\\/ ]+$/ and be at most 50 characters",
      );
    }
    if (!court || !COURT_SLUG_RE.test(court) || court.length > 20) {
      throw new Error(
        "court must be a lowercase alphanumeric CourtListener court slug (max 20 chars), e.g. 'dcd', 'nysd', 'ca9'",
      );
    }

    const entry: WatchedDocket = {
      matterNumber,
      docketNumber,
      court,
      caseName,
      addedAt: new Date().toISOString(),
      totalFilingsSeen: 0,
    };

    this.watched.set(matterNumber, entry);
    this.persist().catch((err: Error) =>
      logger.warn("DocketMonitor: persist failed", { error: err.message }),
    );
    logger.info("DocketMonitor: watching docket", { matterNumber, docketNumber, court });
    return entry;
  }

  /**
   * Unregister a docket from monitoring.
   * Returns true if the entry was found and removed, false if not found.
   */
  unwatch(matterNumber: string): boolean {
    const had = this.watched.has(matterNumber);
    if (had) {
      this.watched.delete(matterNumber);
      this.persist().catch((err: Error) =>
        logger.warn("DocketMonitor: persist failed", { error: err.message }),
      );
      logger.info("DocketMonitor: unwatched docket", { matterNumber });
    }
    return had;
  }

  /** Return all watched dockets as an array. */
  list(): WatchedDocket[] {
    return Array.from(this.watched.values());
  }

  // ─── Polling ───────────────────────────────────────────────────────────────

  /** Check every watched docket for new filings. */
  async checkAll(): Promise<void> {
    const entries = Array.from(this.watched.values());
    if (!entries.length) return;
    logger.info("DocketMonitor: checking all dockets", { count: entries.length });
    await Promise.allSettled(
      entries.map((w) =>
        this.checkDocket(w).catch((err: Error) =>
          logger.warn("DocketMonitor: error checking docket", {
            matterNumber: w.matterNumber,
            docketNumber: w.docketNumber,
            error: err.message,
          }),
        ),
      ),
    );
  }

  /**
   * Check a single docket against CourtListener.
   * Updates lastCheckedAt, lastFilingDate, and totalFilingsSeen.
   * Emits "alert" and auto-ingests into the knowledge store when new filings are found.
   * Returns a DocketAlert on new filings, or null if nothing changed.
   */
  async checkDocket(w: WatchedDocket): Promise<DocketAlert | null> {
    const apiKey = process.env.COURT_LISTENER_API_KEY ?? "";

    const url =
      `${COURT_LISTENER_API}/dockets/?` +
      `docket_number=${encodeURIComponent(w.docketNumber)}` +
      `&court=${w.court}` +
      `&fields=id,case_name,date_filed,date_last_filing,docket_entries_count`;

    const headers: Record<string, string> = {
      Accept: "application/json",
    };
    if (apiKey) {
      headers["Authorization"] = `Token ${apiKey}`;
    }

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);

    let data: CourtListenerResponse;
    try {
      const response = await fetch(url, { headers, signal: controller.signal });
      clearTimeout(timeout);

      if (!response.ok) {
        logger.warn("DocketMonitor: CourtListener API error", {
          status: response.status,
          docketNumber: w.docketNumber,
        });
        return null;
      }

      // Cap at 1 MB
      const text = await this.readCapped(response, MAX_RESPONSE_BYTES);
      if (text === null) {
        logger.warn("DocketMonitor: response exceeded 1 MB cap", { docketNumber: w.docketNumber });
        return null;
      }

      data = JSON.parse(text) as CourtListenerResponse;
    } catch (err) {
      clearTimeout(timeout);
      if ((err as Error).name === "AbortError") {
        logger.warn("DocketMonitor: CourtListener request timed out", { docketNumber: w.docketNumber });
      } else {
        logger.warn("DocketMonitor: fetch error", {
          docketNumber: w.docketNumber,
          error: (err as Error).message,
        });
      }
      return null;
    }

    const results = data.results ?? [];
    if (!results.length) {
      logger.info("DocketMonitor: no results from CourtListener", {
        docketNumber: w.docketNumber,
        court: w.court,
      });
      w.lastCheckedAt = new Date().toISOString();
      this.persist().catch(() => undefined);
      return null;
    }

    const docket = results[0];
    const latestFilingDate = docket.date_last_filing ?? docket.date_filed ?? null;
    const totalEntries = docket.docket_entries_count ?? 0;
    const caseName = docket.case_name ?? w.caseName ?? w.docketNumber;

    const hasNewFilingDate =
      latestFilingDate !== null &&
      (w.lastFilingDate === undefined || latestFilingDate > w.lastFilingDate);
    const hasMoreEntries = totalEntries > w.totalFilingsSeen;

    w.lastCheckedAt = new Date().toISOString();

    if (!hasNewFilingDate && !hasMoreEntries) {
      // No change — just persist the updated lastCheckedAt
      this.persist().catch(() => undefined);
      return null;
    }

    // New filings detected
    const newFilingCount = hasMoreEntries
      ? totalEntries - w.totalFilingsSeen
      : 1;

    // Update state
    if (latestFilingDate) w.lastFilingDate = latestFilingDate;
    w.totalFilingsSeen = totalEntries;
    if (w.caseName === undefined && docket.case_name) w.caseName = docket.case_name;

    this.persist().catch(() => undefined);

    const courtListenerUrl = `https://www.courtlistener.com/docket/${docket.id}/`;

    const alert: DocketAlert = {
      id: randomUUID(),
      matterNumber: w.matterNumber,
      docketNumber: w.docketNumber,
      court: w.court,
      caseName,
      newFilingCount,
      latestFilingDate: latestFilingDate ?? new Date().toISOString(),
      courtListenerUrl,
      detectedAt: new Date().toISOString(),
    };

    logger.info("DocketMonitor: new filings detected", {
      matterNumber: w.matterNumber,
      docketNumber: w.docketNumber,
      newFilingCount,
      caseName,
    });

    // Auto-ingest into the knowledge store
    if (this.knowledge) {
      try {
        const content = [
          `Case: ${caseName}`,
          `Docket: ${w.docketNumber}`,
          `Court: ${w.court}`,
          `New filings detected: ${newFilingCount}`,
          `Latest filing date: ${alert.latestFilingDate}`,
          `CourtListener URL: ${courtListenerUrl}`,
          `Matter number: ${w.matterNumber}`,
          `Detected at: ${alert.detectedAt}`,
        ].join("\n");

        await this.knowledge.ingest({
          title: `Docket Alert: ${caseName} (${w.docketNumber}) — ${newFilingCount} new filing(s)`,
          content,
          source: courtListenerUrl,
          documentType: "docket_alert",
        });

        logger.info("DocketMonitor: auto-ingested docket alert", {
          matterNumber: w.matterNumber,
          docketNumber: w.docketNumber,
        });
      } catch (err) {
        logger.warn("DocketMonitor: failed to auto-ingest docket alert", {
          matterNumber: w.matterNumber,
          error: (err as Error).message,
        });
      }
    }

    // Emit SSE alert
    this.emit("alert", alert);

    return alert;
  }

  // ─── Persistence ───────────────────────────────────────────────────────────

  private persist(): Promise<void> {
    this.writeChain = this.writeChain.then(() => this.doWrite()).catch(() => this.doWrite());
    return this.writeChain;
  }

  private async doWrite(): Promise<void> {
    const entries = Array.from(this.watched.values());
    const tmp = `${this.path}.tmp`;
    await writeFile(tmp, JSON.stringify(entries, null, 2), "utf8");
    await rename(tmp, this.path);
  }

  // ─── Utility ───────────────────────────────────────────────────────────────

  /**
   * Read a Response body up to maxBytes.
   * Returns null if the body exceeds the cap.
   */
  private async readCapped(response: Response, maxBytes: number): Promise<string | null> {
    const reader = response.body?.getReader();
    if (!reader) return await response.text();

    const chunks: Uint8Array[] = [];
    let total = 0;

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      if (value) {
        total += value.byteLength;
        if (total > maxBytes) {
          reader.cancel().catch(() => undefined);
          return null;
        }
        chunks.push(value);
      }
    }

    const merged = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      merged.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return new TextDecoder().decode(merged);
  }
}
