// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3.
// See <https://www.gnu.org/licenses/gpl-3.0.html>

import { QdrantClient } from "@qdrant/js-client-rest";
import { v4 as uuidv4, v5 as uuidv5 } from "uuid";
import { Config } from "../config.js";
import { embed, embedBatch } from "../embeddings.js";
import { logger } from "../logger.js";
import type { AgentDefinition, AgentTier, AgentDomain } from "../types.js";

// Backed by Qdrant in dev; drop in RuVector HTTP API for production.
// RuVector: https://github.com/ruvnet/RuVector — compatible REST API, add GNN self-learning.

const COLLECTION = Config.vectorDb.collections.agents;
const DIMS = Config.embeddings.dimensions;

// Stable namespace for agent string-ID → UUID v5 mapping.
// Same agent ID always maps to the same Qdrant point ID across restarts.
const AGENT_NS = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"; // UUID namespace (DNS)

export class AgentRegistry {
  private readonly qdrant: QdrantClient;
  private ready = false;

  constructor() {
    this.qdrant = new QdrantClient({
      url: Config.vectorDb.url,
      apiKey: Config.vectorDb.apiKey,
    });
  }

  async init(): Promise<void> {
    const { collections } = await this.qdrant.getCollections();
    const exists = collections.some((c) => c.name === COLLECTION);
    if (!exists) {
      await this.qdrant.createCollection(COLLECTION, {
        vectors: { size: DIMS, distance: "Cosine" },
      });
      logger.info("Agent registry collection created", { collection: COLLECTION });
    }
    this.ready = true;
  }

  async register(definition: AgentDefinition): Promise<void> {
    this.assertReady();
    const { embedding } = await embed(definition.description);
    await this.qdrant.upsert(COLLECTION, {
      wait: true,
      points: [
        {
          id: this.toPointId(definition.id),
          vector: embedding,
          payload: {
            ...definition,
            // Store allowedTools as a JSON string for Qdrant compat
            allowedToolsJson: JSON.stringify(definition.allowedTools),
            skillsJson: JSON.stringify(definition.skills),
          },
        },
      ],
    });
    logger.debug("Agent registered", { id: definition.id, name: definition.name });
  }

  async registerAll(definitions: AgentDefinition[]): Promise<void> {
    this.assertReady();
    const texts = definitions.map((d) => d.description);
    const embeddings = await embedBatch(texts);
    const points = definitions.map((def, i) => ({
      id: this.toPointId(def.id),
      vector: embeddings[i].embedding,
      payload: {
        ...def,
        allowedToolsJson: JSON.stringify(def.allowedTools),
        skillsJson: JSON.stringify(def.skills),
      },
    }));
    await this.qdrant.upsert(COLLECTION, { wait: true, points });
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
    const must: unknown[] = [];
    if (opts.tier !== undefined) {
      must.push({ key: "tier", match: { value: opts.tier } });
    }
    if (opts.domain !== undefined) {
      must.push({ key: "domain", match: { value: opts.domain } });
    }
    if (must.length) filter.must = must;

    const results = await this.qdrant.search(COLLECTION, {
      vector: embedding,
      limit: opts.topK ?? 10,
      filter: must.length ? filter : undefined,
      with_payload: true,
    });

    return results.map((r) => this.toDefinition(r.payload as Record<string, unknown>));
  }

  async getById(id: string): Promise<AgentDefinition | null> {
    this.assertReady();
    const results = await this.qdrant.retrieve(COLLECTION, {
      ids: [this.toPointId(id)],
      with_payload: true,
    });
    if (!results.length) return null;
    return this.toDefinition(results[0].payload as Record<string, unknown>);
  }

  async listAll(): Promise<AgentDefinition[]> {
    this.assertReady();
    const result = await this.qdrant.scroll(COLLECTION, {
      limit: 500,
      with_payload: true,
    });
    return result.points.map((p) => this.toDefinition(p.payload as Record<string, unknown>));
  }

  // Deterministic UUID v5: same agentId always → same Qdrant point ID.
  private toPointId(agentId: string): string {
    return uuidv5(agentId, AGENT_NS);
  }

  private toDefinition(payload: Record<string, unknown>): AgentDefinition {
    return {
      id: payload.id as string,
      name: payload.name as string,
      tier: payload.tier as AgentDefinition["tier"],
      type: payload.type as AgentDefinition["type"],
      domain: payload.domain as AgentDefinition["domain"],
      description: payload.description as string,
      systemPrompt: payload.systemPrompt as string,
      skills: JSON.parse((payload.skillsJson as string) ?? "[]"),
      allowedTools: JSON.parse((payload.allowedToolsJson as string) ?? "[]"),
      metadata: (payload.metadata as Record<string, unknown>) ?? undefined,
    };
  }

  private assertReady(): void {
    if (!this.ready) throw new Error("AgentRegistry not initialised — call init() first");
  }
}