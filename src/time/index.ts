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
import { auditLogger, ACTOR_SYSTEM } from "../audit/index.js";
import { classifyUtbms } from "../billing/utbms.js";
import type { TimeEntry, TimeEventType, OcgSuggestion } from "../types.js";

export type { TimeEntry, TimeEventType };

export interface TimeFilter {
  profileId?: string;
  agentId?: string;
  taskId?: string;
  matterNumber?: string;
  clientNumber?: string;
  from?: Date;
  to?: Date;
  /** When true, only return agent_work entries. When false, exclude them. Omit for all. */
  agentOnly?: boolean;
}

const SIX_MIN_MS = 6 * 60 * 1000; // 360 000 ms

export class TimeStore {
  private readonly path = Config.persistence.timeFile;
  private entries: TimeEntry[] = [];
  private writeChain = Promise.resolve();

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
    } catch (err) {
      logger.warn("Time entries file could not be loaded — starting empty", {
        error: err instanceof Error ? err.message : String(err),
      });
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
    if (entry.billingRate !== undefined) {
      if (!Number.isFinite(entry.billingRate) || entry.billingRate < 0) {
        throw new Error(`Invalid billingRate: ${entry.billingRate} — must be a non-negative finite number`);
      }
    }
    const newEntry: TimeEntry = {
      ...entry,
      id: randomUUID(),
      durationMs: 0,
      billingUnits: 0,
    };
    this.entries.push(newEntry);
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
    auditLogger.write({
      event: "time.opened",
      actorId: newEntry.profileId ?? ACTOR_SYSTEM,
      taskId: newEntry.taskId,
      agentId: newEntry.agentId,
      data: { entryId: newEntry.id, event: newEntry.event, description: newEntry.description.slice(0, 200), matterNumber: newEntry.matterNumber, clientNumber: newEntry.clientNumber },
    });
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
    entry.durationMs = Math.max(0, endedAt.getTime() - entry.startedAt.getTime());
    entry.billingUnits = Math.ceil(entry.durationMs / SIX_MIN_MS);
    if (entry.billingRate) {
      entry.billingAmountUsd = parseFloat((entry.billingUnits * 0.1 * entry.billingRate).toFixed(4));
    }
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
    auditLogger.write({
      event: "time.closed",
      actorId: entry.profileId ?? ACTOR_SYSTEM,
      taskId: entry.taskId,
      agentId: entry.agentId,
      durationMs: entry.durationMs,
      data: { entryId: entry.id, event: entry.event, billingUnits: entry.billingUnits, billingAmountUsd: entry.billingAmountUsd, matterNumber: entry.matterNumber, clientNumber: entry.clientNumber },
    });
    classifyUtbms(entry.description ?? entry.event, entry.event).then(({ taskCode, activityCode }) => {
      entry.utbmsTaskCode = taskCode;
      entry.utbmsActivityCode = activityCode;
      this.persist().catch((err) => logger.warn("Failed to persist time entries after UTBMS", { error: (err as Error).message }));
    }).catch(() => { /* classification failure is non-fatal */ });
    return entry;
  }

  /** Get a single entry by ID. */
  getById(id: string): TimeEntry | undefined {
    return this.entries.find((e) => e.id === id);
  }

  /** Overwrite the description on an entry (used by the queue worker). */
  updateDescription(id: string, description: string): void {
    const entry = this.entries.find((e) => e.id === id);
    if (!entry) return;
    entry.description = description;
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
  }

  /** List entries with optional filtering. */
  list(filter?: TimeFilter): TimeEntry[] {
    return this.entries.filter((e) => matchesFilter(e, filter));
  }

  /** Mark an entry as synced to a Clio matter activity (idempotency guard for sync-to-clio). */
  markClioSynced(id: string): void {
    const entry = this.entries.find((e) => e.id === id);
    if (!entry) return;
    entry.clioSyncedAt = new Date().toISOString();
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
  }

  /** Set OCG suggestions for an entry (replaces any prior suggestions). */
  setSuggestions(entryId: string, suggestions: OcgSuggestion[]): void {
    const entry = this.entries.find((e) => e.id === entryId);
    if (!entry) return;
    entry.ocgSuggestions = suggestions;
    entry.ocgCheckedAt = new Date().toISOString();
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
  }

  /**
   * Accept a suggestion — rewrites the entry description and marks the
   * suggestion accepted. Returns the updated entry, or undefined if not found.
   */
  acceptSuggestion(entryId: string, ruleId: string): TimeEntry | undefined {
    const entry = this.entries.find((e) => e.id === entryId);
    if (!entry || !entry.ocgSuggestions) return undefined;
    const suggestion = entry.ocgSuggestions.find((s) => s.ruleId === ruleId);
    if (!suggestion) return undefined;
    entry.description = suggestion.suggestedDescription;
    suggestion.status = "accepted";
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
    return entry;
  }

  /**
   * Dismiss a suggestion — marks it dismissed without changing the description.
   * Returns the updated entry, or undefined if not found.
   */
  dismissSuggestion(entryId: string, ruleId: string): TimeEntry | undefined {
    const entry = this.entries.find((e) => e.id === entryId);
    if (!entry || !entry.ocgSuggestions) return undefined;
    const suggestion = entry.ocgSuggestions.find((s) => s.ruleId === ruleId);
    if (!suggestion) return undefined;
    suggestion.status = "dismissed";
    this.persist().catch((err) => logger.warn("Failed to persist time entries", { error: (err as Error).message }));
    return entry;
  }

  /** List entries that have at least one pending OCG suggestion. */
  listWithSuggestions(filter?: TimeFilter): TimeEntry[] {
    return this.list(filter).filter(
      (e) => e.ocgSuggestions?.some((s) => s.status === "pending"),
    );
  }

  /** Explicit JSON export — same as list(), for the export endpoint. */
  exportJson(filter?: TimeFilter): TimeEntry[] {
    return this.list(filter);
  }

  /** CSV export with headers. */
  exportCsv(filter?: TimeFilter): string {
    const rows = this.list(filter);
    const header = "id,event,profileId,profileName,agentId,agentName,taskId,matterNumber,clientNumber,description,startedAt,endedAt,durationMs,billingUnits,billingRate,billingAmountUsd,utbmsTaskCode,utbmsActivityCode,clioSyncedAt";
    // Neutralize spreadsheet formula injection: a field beginning with = + - @
    // (or a leading control char) is executed as a formula by Excel/Sheets when
    // the CSV is opened. Prefix such values with a single quote. Several fields
    // (description, names) carry LLM- or user-supplied content.
    const esc = (v: unknown) => {
      let s = String(v ?? "").replace(/[\r\n]+/g, " ");
      if (/^[=+\-@\t]/.test(s)) s = `'${s}`;
      return `"${s.replace(/"/g, '""')}"`;
    };
    const lines = rows.map((e) =>
      [
        esc(e.id),
        esc(e.event),
        esc(e.profileId ?? ""),
        esc(e.profileName ?? ""),
        esc(e.agentId ?? ""),
        esc(e.agentName ?? ""),
        esc(e.taskId),
        esc(e.matterNumber ?? ""),
        esc(e.clientNumber ?? ""),
        esc(e.description),
        esc(e.startedAt.toISOString()),
        esc(e.endedAt?.toISOString() ?? ""),
        esc(e.durationMs),
        esc(e.billingUnits),
        esc(e.billingRate ?? ""),
        esc(e.billingAmountUsd ?? ""),
        esc(e.utbmsTaskCode ?? ""),
        esc(e.utbmsActivityCode ?? ""),
        esc(e.clioSyncedAt ?? ""),
      ].join(","),
    );
    return [header, ...lines].join("\r\n");
  }

  /** Atomic write — serialized through writeChain to prevent concurrent writes. */
  persist(): Promise<void> {
    this.writeChain = this.writeChain.then(() => this.doWrite()).catch(() => this.doWrite());
    return this.writeChain;
  }

  private async doWrite(): Promise<void> {
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
  if (filter.agentOnly === true && entry.event !== "agent_work") return false;
  if (filter.agentOnly === false && entry.event === "agent_work") return false;
  // profileId filter: match lawyer entries for that profile OR agent entries attributed to them
  if (filter.profileId && entry.profileId !== filter.profileId) return false;
  if (filter.agentId && entry.agentId !== filter.agentId) return false;
  if (filter.taskId && entry.taskId !== filter.taskId) return false;
  if (filter.matterNumber && entry.matterNumber !== filter.matterNumber) return false;
  if (filter.clientNumber && entry.clientNumber !== filter.clientNumber) return false;
  if (filter.from) {
    if (isNaN(filter.from.getTime())) return false; // skip entries if filter date is invalid
    if (entry.startedAt < filter.from) return false;
  }
  if (filter.to) {
    if (isNaN(filter.to.getTime())) return false;
    if (entry.startedAt > filter.to) return false;
  }
  return true;
}
