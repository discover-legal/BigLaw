// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Audit logger — append-only, hash-chained structured log of all system events.
 *
 * Events are:
 *   - Written to a JSONL file (one JSON object per line) for offline analysis
 *   - Kept in a rolling in-memory buffer for fast REST queries
 *   - Emitted on an EventEmitter for live SSE streaming
 *   - Dispatched to registered AuditSink instances (OpenSearch, Splunk, webhook)
 *
 * Each entry carries a SHA-256 prevHash over the preceding entry's JSON, forming
 * a tamper-evident chain. The first entry uses prevHash "genesis".
 *
 * Set AUDIT_ENABLED=false to disable disk writes (in-memory buffer still works).
 */

import { EventEmitter } from "events";
import { createHash } from "crypto";
import { appendFile, readFile } from "fs/promises";
import { Config } from "../config.js";
import { logger } from "../logger.js";

// ─── Actor constants ──────────────────────────────────────────────────────────

/** Actor for events originating from internal orchestration (no human request). */
export const ACTOR_SYSTEM = "system";

/** Actor for unauthenticated inbound requests (auth failure before identity is known). */
export const ACTOR_ANONYMOUS = "anonymous";

// ─── Event types ─────────────────────────────────────────────────────────────

export type AuditEventType =
  // ── Task lifecycle ────────────────────────────────────────────────────────
  | "task.created"
  | "task.started"
  | "task.complete"
  | "task.failed"
  | "task.deleted"
  | "task.assigned"
  // ── DyTopo rounds ─────────────────────────────────────────────────────────
  | "phase.start"
  | "phase.complete"
  | "round.start"
  | "round.complete"
  | "agent.processing"
  | "agent.complete"
  // ── Tool calls ────────────────────────────────────────────────────────────
  | "tool.call"
  | "tool.result"
  // ── Protocol ──────────────────────────────────────────────────────────────
  | "finding.produced"
  | "citation.gate"
  | "debate.start"
  | "debate.resolved"
  | "verification.start"
  | "verification.complete"
  | "gate.created"
  | "gate.approved"
  | "gate.rejected"
  // ── Model calls ───────────────────────────────────────────────────────────
  | "model.call"
  | "model.response"
  // ── Authentication ────────────────────────────────────────────────────────
  | "auth.login"
  | "auth.logout"
  | "auth.failed"
  | "auth.session.expired"
  // ── Authorization ─────────────────────────────────────────────────────────
  | "access.denied"
  // ── Documents ─────────────────────────────────────────────────────────────
  | "document.ingested"
  | "document.uploaded"
  | "document.searched"
  // ── Clients & matters ─────────────────────────────────────────────────────
  | "client.created"
  | "client.updated"
  | "client.deleted"
  | "matter.added"
  | "matter.removed"
  // ── Lawyer profiles ───────────────────────────────────────────────────────
  | "profile.created"
  | "profile.updated"
  | "profile.deleted"
  | "profile.tone.imported"
  | "profile.tone.cleared"
  // ── Billable time ─────────────────────────────────────────────────────────
  | "time.opened"
  | "time.closed"
  | "time.updated"
  | "time.synced"
  // ── Job queue ─────────────────────────────────────────────────────────────
  | "job.enqueued"
  | "job.completed"
  | "job.failed"
  | "job.dead_letter"
  // ── OCG compliance ────────────────────────────────────────────────────────
  | "ocg.violation"
  | "ocg.outcome"
  | "client.ocg.ingested"
  | "client.ocg.deleted"
  | "client.voiceguide.imported"
  | "client.voiceguide.cleared"
  // ── Admin & settings ──────────────────────────────────────────────────────
  | "settings.updated"
  // ── Security ──────────────────────────────────────────────────────────────
  | "security.ssrf_blocked"
  | "security.rate_limited";

// ─── AuditEntry ──────────────────────────────────────────────────────────────

export interface AuditEntry {
  id: string;
  ts: string;                        // ISO-8601 timestamp
  event: AuditEventType;
  /** SHA-256 of the previous entry's JSON string. "genesis" for the first entry. */
  prevHash: string;
  /** Who triggered this event. ProfileId for humans; ACTOR_SYSTEM for internal events;
   *  ACTOR_ANONYMOUS for requests that arrived before identity could be established. */
  actorId: string;
  taskId?: string;
  agentId?: string;
  model?: string;
  durationMs?: number;
  data: Record<string, unknown>;
}

// ─── AuditSink ───────────────────────────────────────────────────────────────

/**
 * AuditSink — pluggable backend for forwarding audit events.
 *
 * Implementations:
 *   OpenSearchSink  (src/audit/sinks/opensearch.ts) — self-hosted, batched bulk
 *   SplunkSink      (src/audit/sinks/splunk.ts)     — Splunk HEC, batched
 *   WebhookSink     (src/audit/sinks/webhook.ts)    — generic HTTP POST
 *
 * write() must never throw — swallow errors and optionally buffer for retry.
 * flush() is called on graceful shutdown to drain any pending batches.
 */
export interface AuditSink {
  readonly name: string;
  write(entry: AuditEntry): void;
  flush(): Promise<void>;
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function sanitizeAuditData(data: Record<string, unknown>): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(data)) {
    if (typeof v === "string" && v.length > 500) {
      result[k] = v.slice(0, 500) + "...[truncated]";
    } else {
      result[k] = v;
    }
  }
  return result;
}

// ─── AuditLogger ─────────────────────────────────────────────────────────────

export class AuditLogger {
  private readonly buffer: AuditEntry[] = [];
  private readonly maxBuffer = 10_000;
  private readonly emitter = new EventEmitter();
  private readonly sinks: AuditSink[] = [];
  private lastHash = "genesis";
  private writeChain: Promise<void> = Promise.resolve();

  /** Register an external sink. Call before first write(). */
  registerSink(sink: AuditSink): void {
    this.sinks.push(sink);
  }

  /**
   * Record an audit event. Fire-and-forget — never throws; disk write errors
   * are silently swallowed so a log failure never kills a task.
   */
  write(partial: Omit<AuditEntry, "id" | "ts" | "prevHash"> & { actorId: string }): void {
    const entry: AuditEntry = {
      id: crypto.randomUUID(),
      ts: new Date().toISOString(),
      prevHash: this.lastHash,
      ...partial,
      data: sanitizeAuditData(partial.data ?? {}),
    };

    // Advance the hash chain
    this.lastHash = createHash("sha256").update(JSON.stringify(entry)).digest("hex");

    // Rolling in-memory buffer
    this.buffer.push(entry);
    if (this.buffer.length > this.maxBuffer) this.buffer.shift();

    // Live event stream
    this.emitter.emit("entry", entry);

    // Serialized JSONL disk write — chained to preserve hash-chain ordering
    if (Config.audit.enabled) {
      this.writeChain = this.writeChain.then(async () => {
        const line = JSON.stringify(entry) + "\n";
        await appendFile(Config.audit.logFile, line, "utf8");
      }).catch((err) => {
        logger.error("Audit log write failed", { error: (err as Error).message });
      });
    }

    // Forward to registered sinks (fire-and-forget; each sink handles its own errors)
    for (const sink of this.sinks) {
      try {
        sink.write(entry);
      } catch {
        // Sink errors must never propagate — audit is observational, not critical path
      }
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

  /** Current number of active SSE subscribers (used by the server to enforce a cap). */
  listenerCount(): number {
    return this.emitter.listenerCount("entry");
  }

  /**
   * Reload the most recent entries from the JSONL file into the in-memory buffer.
   * Call once at startup so the REST endpoint and UI show historical events rather
   * than "waiting for activity" after every restart.  The hash chain continues from
   * the last loaded entry so new writes remain tamper-evident.
   */
  async restoreFromFile(): Promise<void> {
    if (!Config.audit.enabled) return;
    let raw: string;
    try {
      raw = await readFile(Config.audit.logFile, "utf8");
    } catch {
      return; // no file yet — first run
    }
    const lines = raw.trim().split("\n").filter(Boolean);
    const toLoad = lines.slice(-this.maxBuffer);
    for (const line of toLoad) {
      try {
        const entry = JSON.parse(line) as AuditEntry;
        this.buffer.push(entry);
      } catch {
        // skip malformed lines
      }
    }
    if (this.buffer.length > this.maxBuffer) {
      this.buffer.splice(0, this.buffer.length - this.maxBuffer);
    }
    // Advance hash chain from the last restored entry so new events chain correctly
    const last = this.buffer[this.buffer.length - 1];
    if (last) {
      this.lastHash = createHash("sha256").update(JSON.stringify(last)).digest("hex");
    }
  }

  /** Flush all sinks — call on graceful shutdown to drain pending batches. */
  async flushSinks(): Promise<void> {
    await Promise.allSettled(this.sinks.map((s) => s.flush()));
  }
}

export const auditLogger = new AuditLogger();
