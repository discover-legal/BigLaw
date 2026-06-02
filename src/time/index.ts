// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * TimeStore — automatic billable time tracking.
 *
 * Records open/close time entries for task execution and gate reviews.
 * Persists to a JSON file (atomic tmp+rename like other stores).
 * Billing units are 6-minute increments (0.1 hr each), rounded UP.
 */

import { randomUUID } from "crypto";
import { readFile, writeFile, rename } from "fs/promises";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import type { TimeEntry, TimeEventType } from "../types.js";

export type { TimeEntry, TimeEventType };

export interface TimeFilter {
  profileId?: string;
  taskId?: string;
  matterNumber?: string;
  clientNumber?: string;
  from?: Date;
  to?: Date;
}

const SIX_MIN_MS = 6 * 60 * 1000; // 360 000 ms

export class TimeStore {
  private readonly path = Config.persistence.timeFile;
  private entries: TimeEntry[] = [];

  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      const parsed = JSON.parse(raw) as Array<Record<string, unknown>>;
      this.entries = parsed.map((e) => ({
        ...e,
        startedAt: new Date(e.startedAt as string),
        endedAt: e.endedAt ? new Date(e.endedAt as string) : undefined,
      })) as TimeEntry[];
      logger.info("Time entries loaded", { count: this.entries.length });
    } catch {
      this.entries = [];
    }
  }

  /**
   * Open a new time entry. The entry is open (durationMs=0, billingUnits=0)
   * until `close()` is called.
   */
  open(
    entry: Omit<TimeEntry, "id" | "endedAt" | "durationMs" | "billingUnits">,
  ): TimeEntry {
    const newEntry: TimeEntry = {
      ...entry,
      id: randomUUID(),
      durationMs: 0,
      billingUnits: 0,
    };
    this.entries.push(newEntry);
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
    return newEntry;
  }

  /**
   * Close an open time entry. Computes durationMs and billingUnits.
   * Returns undefined if the entry is not found.
   */
  close(id: string): TimeEntry | undefined {
    const entry = this.entries.find((e) => e.id === id);
    if (!entry) return undefined;
    const endedAt = new Date();
    entry.endedAt = endedAt;
    entry.durationMs = endedAt.getTime() - entry.startedAt.getTime();
    entry.billingUnits = Math.ceil(entry.durationMs / SIX_MIN_MS);
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
    return entry;
  }

  /** List entries with optional filtering. */
  list(filter?: TimeFilter): TimeEntry[] {
    return this.entries.filter((e) => matchesFilter(e, filter));
  }

  /** Explicit JSON export — same as list(), for the export endpoint. */
  exportJson(filter?: TimeFilter): TimeEntry[] {
    return this.list(filter);
  }

  /** CSV export with headers. */
  exportCsv(filter?: TimeFilter): string {
    const rows = this.list(filter);
    const header = "id,profileId,profileName,taskId,matterNumber,clientNumber,description,event,startedAt,endedAt,durationMs,billingUnits";
    const esc = (v: unknown) => `"${String(v ?? "").replace(/"/g, '""')}"`;
    const lines = rows.map((e) =>
      [
        esc(e.id),
        esc(e.profileId),
        esc(e.profileName),
        esc(e.taskId),
        esc(e.matterNumber ?? ""),
        esc(e.clientNumber ?? ""),
        esc(e.description),
        esc(e.event),
        esc(e.startedAt.toISOString()),
        esc(e.endedAt?.toISOString() ?? ""),
        esc(e.durationMs),
        esc(e.billingUnits),
      ].join(","),
    );
    return [header, ...lines].join("\r\n");
  }

  /** Atomic write — tmp file then rename. */
  async persist(): Promise<void> {
    const tmp = `${this.path}.tmp`;
    const serialisable = this.entries.map((e) => ({
      ...e,
      startedAt: e.startedAt.toISOString(),
      endedAt: e.endedAt?.toISOString(),
    }));
    await writeFile(tmp, JSON.stringify(serialisable, null, 2), "utf8");
    await rename(tmp, this.path);
  }
}

function matchesFilter(entry: TimeEntry, filter?: TimeFilter): boolean {
  if (!filter) return true;
  if (filter.profileId && entry.profileId !== filter.profileId) return false;
  if (filter.taskId && entry.taskId !== filter.taskId) return false;
  if (filter.matterNumber && entry.matterNumber !== filter.matterNumber) return false;
  if (filter.clientNumber && entry.clientNumber !== filter.clientNumber) return false;
  if (filter.from && entry.startedAt < filter.from) return false;
  if (filter.to && entry.startedAt > filter.to) return false;
  return true;
}
