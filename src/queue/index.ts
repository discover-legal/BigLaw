// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Job queue — the event bus that drives Big Michael's agentic OS behaviour.
 *
 * Architecture:
 *   Events (time entry closed, OCG ingested) → enqueue Job
 *   Worker loop (src/queue/worker.ts)         → dequeue + dispatch
 *   Handlers                                  → Haiku calls, state mutations
 *
 * The QueueAdapter interface is intentionally swappable:
 *   InMemoryQueue  — JSON-persisted, default, works anywhere
 *   RedisStreamQueue (future) — XADD/XREADGROUP, enables horizontal K8s workers
 *
 * Jobs are persisted to Config.persistence.jobsFile so the queue survives
 * server restarts. Atomic write (tmp + rename) same as all other stores.
 */

import { randomUUID } from "node:crypto";
import { readFile, writeFile, rename, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { Config } from "../config.js";
import { logger } from "../logger.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export type JobType =
  | "summarize_time_entry"  // AI-generate a compliant description from task context + OCG rules
  | "ocg_bulk_check";       // queue summarize jobs for all entries belonging to a client

export type JobStatus = "pending" | "running" | "done" | "failed" | "dead_letter";

export interface SummarizeTimeEntryPayload {
  entryId: string;
  taskId: string;
  clientNumber?: string;
  matterNumber?: string;
}

export interface OcgBulkCheckPayload {
  clientId: string;
  clientNumber: string;
}

export interface Job {
  id: string;
  type: JobType;
  payload: Record<string, unknown>;
  status: JobStatus;
  createdAt: string;
  startedAt?: string;
  completedAt?: string;
  retries: number;
  maxRetries: number;
  error?: string;
}

// ─── QueueAdapter — swappable between InMemory and Redis ─────────────────────

export interface QueueStats {
  pending: number;
  running: number;
  done: number;
  failed: number;
  dead_letter: number;
}

export interface QueueAdapter {
  init(): Promise<void>;
  enqueue(type: JobType, payload: Record<string, unknown>, maxRetries?: number): Promise<Job>;
  /** Atomically claim one pending job — sets status to "running". */
  dequeue(types?: JobType[]): Promise<Job | null>;
  ack(jobId: string): Promise<void>;
  /** Increment retries; promotes to dead_letter when maxRetries exceeded. */
  fail(jobId: string, error: string): Promise<void>;
  /** Manually re-queue a failed or dead_letter job. */
  retry(jobId: string): Promise<Job>;
  list(opts?: { status?: JobStatus; limit?: number; offset?: number }): Promise<Job[]>;
  stats(): Promise<QueueStats>;
}

// ─── InMemoryQueue ────────────────────────────────────────────────────────────

export class InMemoryQueue implements QueueAdapter {
  private jobs: Job[] = [];
  private readonly path = Config.persistence.jobsFile;

  async init(): Promise<void> {
    try {
      await mkdir(dirname(this.path), { recursive: true }).catch(() => {});
      const raw = await readFile(this.path, "utf8");
      const parsed = JSON.parse(raw) as Job[];
      // On restart: any job that was "running" crashed mid-flight → reset to pending
      this.jobs = parsed.map((j) =>
        j.status === "running" ? { ...j, status: "pending" as JobStatus } : j,
      );
      // Prune done jobs older than 7 days to keep file manageable
      const cutoff = Date.now() - 7 * 24 * 60 * 60 * 1000;
      this.jobs = this.jobs.filter(
        (j) => j.status !== "done" || new Date(j.createdAt).getTime() > cutoff,
      );
      logger.info("Job queue loaded", {
        pending: this.jobs.filter((j) => j.status === "pending").length,
        total: this.jobs.length,
      });
    } catch {
      this.jobs = [];
    }
  }

  async enqueue(type: JobType, payload: Record<string, unknown>, maxRetries = Config.queue.maxRetries): Promise<Job> {
    const job: Job = {
      id: randomUUID(),
      type,
      payload,
      status: "pending",
      createdAt: new Date().toISOString(),
      retries: 0,
      maxRetries,
    };
    this.jobs.push(job);
    this.persist();
    logger.debug("Job enqueued", { jobId: job.id, type });
    return job;
  }

  async dequeue(types?: JobType[]): Promise<Job | null> {
    const job = this.jobs.find(
      (j) => j.status === "pending" && (!types || types.includes(j.type)),
    );
    if (!job) return null;
    job.status = "running";
    job.startedAt = new Date().toISOString();
    this.persist();
    return job;
  }

  async ack(jobId: string): Promise<void> {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) return;
    job.status = "done";
    job.completedAt = new Date().toISOString();
    this.persist();
  }

  async fail(jobId: string, error: string): Promise<void> {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) return;
    job.retries += 1;
    job.error = error;
    job.status = job.retries >= job.maxRetries ? "dead_letter" : "pending";
    if (job.status === "dead_letter") {
      logger.warn("Job moved to dead_letter", { jobId, type: job.type, error });
    }
    this.persist();
  }

  async retry(jobId: string): Promise<Job> {
    const job = this.jobs.find((j) => j.id === jobId);
    if (!job) throw new Error(`Job not found: ${jobId}`);
    job.status = "pending";
    job.error = undefined;
    this.persist();
    return job;
  }

  async list(opts: { status?: JobStatus; limit?: number; offset?: number } = {}): Promise<Job[]> {
    let result = opts.status ? this.jobs.filter((j) => j.status === opts.status) : [...this.jobs];
    result = result.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
    const offset = opts.offset ?? 0;
    return result.slice(offset, offset + (opts.limit ?? 100));
  }

  async stats(): Promise<QueueStats> {
    const counts: QueueStats = { pending: 0, running: 0, done: 0, failed: 0, dead_letter: 0 };
    for (const j of this.jobs) {
      if (j.status === "pending") counts.pending++;
      else if (j.status === "running") counts.running++;
      else if (j.status === "done") counts.done++;
      else if (j.status === "failed") counts.failed++;
      else if (j.status === "dead_letter") counts.dead_letter++;
    }
    return counts;
  }

  private persist(): void {
    const tmp = `${this.path}.tmp`;
    writeFile(tmp, JSON.stringify(this.jobs, null, 2), "utf8")
      .then(() => rename(tmp, this.path))
      .catch((err) => logger.warn("Failed to persist job queue", { error: (err as Error).message }));
  }
}

// ─── Singleton ────────────────────────────────────────────────────────────────

export const jobQueue: QueueAdapter = new InMemoryQueue();
