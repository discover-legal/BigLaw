// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3.
// See <https://www.gnu.org/licenses/gpl-3.0.html>

/**
 * Audit logger — append-only structured log of all system events.
 *
 * Events are:
 *   - Written to a JSONL file (one JSON object per line) for offline analysis
 *   - Kept in a rolling in-memory buffer for fast REST queries
 *   - Emitted on an EventEmitter for live SSE streaming
 *
 * Set AUDIT_ENABLED=false to disable disk writes (in-memory buffer still works).
 */

import { EventEmitter } from "events";
import { appendFile } from "fs/promises";
import { Config } from "../config.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export type AuditEventType =
  | "task.created"
  | "task.started"
  | "task.complete"
  | "task.failed"
  | "phase.start"
  | "phase.complete"
  | "round.start"
  | "round.complete"
  | "agent.processing"
  | "agent.complete"
  | "tool.call"
  | "tool.result"
  | "finding.produced"
  | "citation.gate"
  | "debate.start"
  | "debate.resolved"
  | "verification.start"
  | "verification.complete"
  | "gate.created"
  | "gate.approved"
  | "gate.rejected"
  | "model.call"
  | "model.response";

export interface AuditEntry {
  id: string;
  ts: string;                        // ISO-8601 timestamp
  event: AuditEventType;
  taskId?: string;
  agentId?: string;
  model?: string;
  durationMs?: number;
  data: Record<string, unknown>;
}

// ─── AuditLogger ──────────────────────────────────────────────────────────────

export class AuditLogger {
  private readonly buffer: AuditEntry[] = [];
  private readonly maxBuffer = 10_000;
  private readonly emitter = new EventEmitter();

  /**
   * Record an audit event. Fire-and-forget — never throws; disk write errors
   * are silently swallowed so a log failure never kills a task.
   */
  write(partial: Omit<AuditEntry, "id" | "ts">): void {
    const entry: AuditEntry = {
      id: crypto.randomUUID(),
      ts: new Date().toISOString(),
      ...partial,
    };

    // Rolling in-memory buffer
    this.buffer.push(entry);
    if (this.buffer.length > this.maxBuffer) this.buffer.shift();

    // Live event stream
    this.emitter.emit("entry", entry);

    // Async JSONL disk write — errors swallowed intentionally
    if (Config.audit.enabled) {
      appendFile(Config.audit.logFile, JSON.stringify(entry) + "\n").catch(() => undefined);
    }
  }

  /**
   * Return the most recent audit entries.
   * @param taskId - When provided, filter to entries for that task.
   * @param limit  - Maximum entries to return (default 500).
   */
  readRecent(taskId?: string, limit = 500): AuditEntry[] {
    const src = taskId
      ? this.buffer.filter((e) => e.taskId === taskId)
      : this.buffer;
    return src.slice(-limit);
  }

  /**
   * Subscribe to live audit events. Returns an unsubscribe function.
   */
  subscribe(listener: (entry: AuditEntry) => void): () => void {
    this.emitter.on("entry", listener);
    return () => this.emitter.off("entry", listener);
  }
}

export const auditLogger = new AuditLogger();
