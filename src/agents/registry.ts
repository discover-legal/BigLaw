// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

// Agent registry backed by RuVector's native in-process HNSW store.
// VectorDb persists to disk via storagePath; no external service required.

import { v5 as uuidv5 } from "uuid";
import { Config } from "../config.js";
import { embed, embedBatch } from "../embeddings.js";
import { logger } from "../logger.js";
import type { AgentDefinition, AgentTier, AgentDomain } from "../types.js";

// eslint-disable-next-line @typescript-eslint/no-require-imports
const { VectorDb } = require("ruvector") as { VectorDb: new (o: RvOptions) => RvDb };

interface RvOptions { dimensions: number; storagePath?: string; distanceMetric?: string }
interface RvEntry  { id?: string; vector: number[] | Float32Array; metadata?: Record<string, unknown> }
interface RvHit    { id: string; score: number; metadata?: Record<string, unknown> }
interface RvRecord { id?: string; vector: Float32Array; metadata?: Record<string, unknown> }
interface RvDb {
  insert(e: RvEntry): Promise<string>;
  insertBatch(es: RvEntry[]): Promise<string[]>;
  search(q: { vector: number[] | Float32Array; k: number; filter?: Record<string, unknown> }): Promise<RvHit[]>;
  get(id: string): Promise<RvRecord | null>;
  delete(id: string): Promise<boolean>;
  len(): Promise<number>;
}

const DIMS = Config.embeddings.dimensions;

// Stable namespace for agent string-ID → UUID v5 mapping.
// Same agent ID always maps to the same point ID across restarts.
const AGENT_NS = "6ba7b810-9dad-11d1-80b4-00c04fd430c8";

// Small non-zero vector used for "list all" style queries — avoids the
// cosine-of-zero edge case in the native HNSW implementation.
const SMALL_VEC: number[] = new Array(DIMS).fill(Number.EPSILON);

export class AgentRegistry {
  private readonly db: RvDb;
  private ready = false;

  constructor() {
    this.db = new VectorDb({
      dimensions: DIMS,
      distanceMetric: "Cosine",
      storagePath: `${Config.vectorDb.dataDir}/agents.rvdb`,
    });
  }

  async init(): Promise<void> {
    this.ready = true;
    logger.info("Agent registry ready (RuVector native)");
  }

  async register(definition: AgentDefinition): Promise<void> {
    this.assertReady();
    const { embedding } = await embed(definition.description);
    await this.db.insert({
      id: this.toPointId(definition.id),
      vector: embedding,
      metadata: this.toPayload(definition),
    });
    logger.debug("Agent registered", { id: definition.id, name: definition.name });
  }

  async registerAll(definitions: AgentDefinition[]): Promise<void> {
    this.assertReady();
    const texts = definitions.map((d) => d.description);
    const embeddings = await embedBatch(texts);
    await this.db.insertBatch(
      definitions.map((def, i) => ({
        id: this.toPointId(def.id),
        vector: embeddings[i].embedding,
        metadata: this.toPayload(def),
      })),
    );
    logger.info("Agent batch registered", { count: definitions.length });
  }

  /**
   * Semantic search: find agents whose capabilities match the query.
   * Optionally filter by tier or domain.
   */
  async search(
    query: string,
    opts: { tier?: AgentTier; domain?: AgentDomain; topK?: number } = {},
  ): Promise<AgentDefinition[]> {
    this.assertReady();
    const { embedding } = await embed(query);
    const filter: Record<string, unknown> = {};
    if (opts.tier !== undefined) filter.tier = opts.tier;
    if (opts.domain !== undefined) filter.domain = opts.domain;
    const results = await this.db.search({
      vector: embedding,
      k: opts.topK ?? 10,
      ...(Object.keys(filter).length ? { filter } : {}),
    });
    return results.map((r) => this.toDefinition(r.metadata ?? {}));
  }

  /**
   * Recommendation-based recruitment: semantic search + Q-learning reranking
   * (handled upstream by AgentLearningLayer). VectorDb HNSW doesn't support
   * collaborative filtering natively so this is a semantic search alias.
   */
  async recommend(
    query: string,
    opts: { positive: string[]; negative?: string[]; tier?: AgentTier; topK?: number },
  ): Promise<AgentDefinition[]> {
    return this.search(query, { tier: opts.tier, topK: opts.topK });
  }

  /**
   * Record agent task outcome (updates successScore on the stored vector).
   * Called after task completion; high confidence → positive signal.
   */
  async recordOutcome(agentIds: string[], avgConfidence: number): Promise<void> {
    this.assertReady();
    const score = Math.max(0, Math.min(1, avgConfidence));
    await Promise.allSettled(
      agentIds.map(async (id) => {
        const pointId = this.toPointId(id);
        const existing = await this.db.get(pointId);
        if (!existing) return;
        await this.db.insert({
          id: pointId,
          vector: existing.vector,
          metadata: { ...(existing.metadata ?? {}), successScore: score },
        });
      }),
    );
    logger.debug("Agent outcome recorded", { count: agentIds.length, avgConfidence });
  }

  async getById(id: string): Promise<AgentDefinition | null> {
    this.assertReady();
    const entry = await this.db.get(this.toPointId(id));
    if (!entry) return null;
    return this.toDefinition(entry.metadata ?? {});
  }

  async listAll(): Promise<AgentDefinition[]> {
    this.assertReady();
    const results = await this.db.search({ vector: SMALL_VEC, k: 500 });
    return results.map((r) => this.toDefinition(r.metadata ?? {}));
  }

  // Deterministic UUID v5: same agentId always → same point ID across restarts.
  private toPointId(agentId: string): string {
    return uuidv5(agentId, AGENT_NS);
  }

  private toPayload(def: AgentDefinition): Record<string, unknown> {
    return {
      id:           def.id,
      name:         def.name,
      tier:         def.tier,
      type:         def.type,
      domain:       def.domain,
      description:  def.description,
      systemPrompt: def.systemPrompt,
      skills:       def.skills,
      allowedTools: def.allowedTools,
      jurisdictions: def.jurisdictions ?? null,
      metadata:     def.metadata ?? null,
    };
  }

  private toDefinition(payload: Record<string, unknown>): AgentDefinition {
    return {
      id:           payload.id as string,
      name:         payload.name as string,
      tier:         payload.tier as AgentDefinition["tier"],
      type:         payload.type as AgentDefinition["type"],
      domain:       payload.domain as AgentDefinition["domain"],
      description:  payload.description as string,
      systemPrompt: payload.systemPrompt as string,
      skills:       (payload.skills as string[]) ?? [],
      allowedTools: (payload.allowedTools as string[]) ?? [],
      // jurisdictions is stored as array or null; restore to undefined when null
      jurisdictions: Array.isArray(payload.jurisdictions)
        ? (payload.jurisdictions as string[])
        : undefined,
      metadata: (payload.metadata as Record<string, unknown>) ?? undefined,
    };
  }

  private assertReady(): void {
    if (!this.ready) throw new Error("AgentRegistry not initialised — call init() first");
  }
}
