// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Memory layer — intra-round and inter-round.
 *
 * Intra-round: IntraRoundMemoryStore (in-process, reset per round)
 * Inter-round: InterRoundMemoryStore (RuVector native HNSW, persists across rounds and tasks)
 *
 * Agents call query() to retrieve relevant memories before generating Need/Offer descriptors
 * or processing their round. The orchestrator writes new memories after each round completes.
 */

import { v4 as uuidv4 } from "uuid";
import { Config } from "../config.js";
import { embed } from "../embeddings.js";
import { logger } from "../logger.js";
import type {
  IntraRoundMemory,
  InterRoundMemory,
  MemoryEntry,
  AgentMessage,
  Finding,
  TaskPhase,
} from "../types.js";

// eslint-disable-next-line @typescript-eslint/no-require-imports
const { VectorDb } = require("ruvector") as { VectorDb: new (o: RvOptions) => RvDb };

interface RvOptions { dimensions: number; storagePath?: string; distanceMetric?: string }
interface RvEntry  { id?: string; vector: number[] | Float32Array; metadata?: Record<string, unknown> }
interface RvHit    { id: string; score: number; metadata?: Record<string, unknown> }
interface RvDb {
  insert(e: RvEntry): Promise<string>;
  search(q: { vector: number[] | Float32Array; k: number; filter?: Record<string, unknown> }): Promise<RvHit[]>;
  delete(id: string): Promise<boolean>;
}

const DIMS = Config.embeddings.dimensions;

// Small non-zero vector used for "fetch by filter" queries without a semantic query.
const SMALL_VEC: number[] = new Array(DIMS).fill(Number.EPSILON);

// ─── Intra-round memory ───────────────────────────────────────────────────────

export class IntraRoundMemoryStore {
  private store: IntraRoundMemory;

  constructor(roundId: string) {
    this.store = {
      roundId,
      receivedMessages: {},
      agentFindings: {},
      sharedContext: [],
    };
  }

  recordMessage(agentId: string, message: AgentMessage): void {
    if (!this.store.receivedMessages[agentId]) {
      this.store.receivedMessages[agentId] = [];
    }
    this.store.receivedMessages[agentId].push(message);
  }

  recordFinding(agentId: string, finding: Finding): void {
    if (!this.store.agentFindings[agentId]) {
      this.store.agentFindings[agentId] = [];
    }
    this.store.agentFindings[agentId].push(finding);
  }

  addSharedContext(text: string): void {
    this.store.sharedContext.push(text);
  }

  getMessagesFor(agentId: string): AgentMessage[] {
    return this.store.receivedMessages[agentId] ?? [];
  }

  getFindingsFor(agentId: string): Finding[] {
    return this.store.agentFindings[agentId] ?? [];
  }

  getAllFindings(): Finding[] {
    return Object.values(this.store.agentFindings).flat();
  }

  getSharedContext(): string[] {
    return this.store.sharedContext;
  }

  snapshot(): IntraRoundMemory {
    return { ...this.store };
  }
}

// ─── Inter-round memory ───────────────────────────────────────────────────────

export class InterRoundMemoryStore {
  private readonly db: RvDb;
  private ready = false;

  constructor() {
    this.db = new VectorDb({
      dimensions: DIMS,
      distanceMetric: "Cosine",
      storagePath: `${Config.vectorDb.dataDir}/memory.rvdb`,
    });
  }

  async init(): Promise<void> {
    this.ready = true;
    logger.info("Memory store ready (RuVector native)");
  }

  /**
   * Persist a memory entry. Called by the orchestrator at end of each round.
   */
  async write(entry: Omit<MemoryEntry, "id" | "embedding">): Promise<MemoryEntry> {
    this.assertReady();
    const { embedding } = await embed(entry.content);
    const full: MemoryEntry = { ...entry, id: uuidv4(), embedding };

    await this.db.insert({
      id: full.id,
      vector: embedding,
      metadata: {
        taskId:    full.taskId,
        round:     full.round,
        phase:     full.phase,
        agentId:   full.agentId ?? null,
        content:   full.content,
        tags:      full.tags,
        createdAt: full.createdAt.toISOString(),
      },
    });

    logger.debug("Memory entry written", { id: full.id, round: full.round, agentId: full.agentId });
    return full;
  }

  /**
   * Semantic query: retrieve the most relevant memories for an agent given a query.
   * Scoped to the current task; can optionally filter by agentId.
   * beforeRound is applied as a post-filter (range queries not supported natively).
   */
  async query(
    query: string,
    opts: {
      taskId: string;
      agentId?: string;
      topK?: number;
      beforeRound?: number;
    },
  ): Promise<MemoryEntry[]> {
    this.assertReady();
    const { embedding } = await embed(query);

    const filter: Record<string, unknown> = { taskId: opts.taskId };
    if (opts.agentId) filter.agentId = opts.agentId;

    // Fetch extra results so we can post-filter by round (native has no range filter).
    const fetchK = opts.beforeRound !== undefined ? 500 : Math.min(opts.topK ?? 8, 100);
    const results = await this.db.search({ vector: embedding, k: fetchK, filter });

    const filtered = opts.beforeRound !== undefined
      ? results.filter((r) => ((r.metadata?.round as number) ?? 0) < opts.beforeRound!)
      : results;

    return filtered.slice(0, opts.topK ?? 8).map((r) => {
      const m = r.metadata as Record<string, unknown>;
      return {
        id:        r.id,
        taskId:    m.taskId as string,
        round:     m.round as number,
        phase:     m.phase as TaskPhase,
        agentId:   (m.agentId as string | null) ?? undefined,
        content:   m.content as string,
        tags:      (m.tags as string[]) ?? [],
        createdAt: new Date(m.createdAt as string),
      };
    });
  }

  /**
   * Write a round summary — called after every round completes.
   */
  async writeRoundSummary(params: {
    taskId: string;
    round: number;
    phase: TaskPhase;
    summary: string;
    findingCount: number;
  }): Promise<void> {
    await this.write({
      taskId: params.taskId,
      round:  params.round,
      phase:  params.phase,
      content: `Round ${params.round} summary (${params.phase}): ${params.summary}. Findings produced: ${params.findingCount}.`,
      tags:   ["round-summary", `round-${params.round}`, params.phase],
      createdAt: new Date(),
    });
  }

  /**
   * Write agent-specific finding as a memory entry.
   */
  async writeFindingMemory(params: {
    taskId: string;
    round: number;
    phase: TaskPhase;
    agentId: string;
    finding: Finding;
  }): Promise<void> {
    await this.write({
      taskId:  params.taskId,
      round:   params.round,
      phase:   params.phase,
      agentId: params.agentId,
      content: params.finding.content,
      tags:    ["finding", `round-${params.round}`, params.agentId, params.phase],
      createdAt: new Date(),
    });
  }

  /**
   * Delete all memory entries for a task.
   * Queries by taskId filter, then deletes each entry individually.
   */
  async deleteByTaskId(taskId: string): Promise<void> {
    this.assertReady();
    const found = await this.db.search({ vector: SMALL_VEC, k: 5000, filter: { taskId } });
    await Promise.allSettled(found.map((r) => this.db.delete(r.id)));
    logger.debug("Memory entries deleted for task", { taskId, count: found.length });
  }

  private assertReady(): void {
    if (!this.ready) throw new Error("InterRoundMemoryStore not initialised — call init() first");
  }
}
