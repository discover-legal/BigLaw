// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * RuVector Q-learning layer for agent recruitment.
 *
 * Sits alongside the Qdrant semantic search layer. Once there is enough
 * task history, it biases agent recruitment toward agents that have
 * produced high-confidence, uncontested findings in similar situations.
 *
 * State  = "phase:jurisdiction:workflowType"  (e.g. "research:US:full_bench")
 * Action = agentId string
 * Reward = effective finding confidence (challenged findings penalised ×0.3)
 *
 * The Q-table is persisted to disk between server restarts so learning
 * accumulates over the lifetime of the deployment, not just a session.
 *
 * Uses ruvector's LearningEngine (Q-learning) for policy and FastAgentDB
 * for episode storage + similarity-based episode retrieval.
 */

import { readFile, writeFile, rename } from "fs/promises";
import { logger } from "../logger.js";
import { Config } from "../config.js";

// eslint-disable-next-line @typescript-eslint/no-require-imports
const { LearningEngine, createFastAgentDB } = require("ruvector") as {
  LearningEngine: new () => RuLearningEngine;
  // createFastAgentDB(dimensions?, maxEpisodes?) — synchronous
  createFastAgentDB: (dimensions?: number, maxEpisodes?: number) => RuFastAgentDB;
};

// ─── RuVector type stubs ──────────────────────────────────────────────────────

interface RuLearningEngine {
  configure(task: string, config: {
    algorithm: string;
    learningRate: number;
    discountFactor: number;
    epsilon: number;
  }): void;
  update(task: string, experience: {
    state: string;
    action: string;
    reward: number;
    nextState: string;
    done: boolean;
  }): number;
  getBestAction(task: string, state: string, actions: string[]): { action: string; confidence: number } | null;
  export(): unknown;
  import(data: unknown): void;
  getStats(): unknown;
}

interface RuFastAgentDB {
  storeEpisode(ep: {
    state: number[];
    action: string;
    reward: number;
    nextState: number[];
    done: boolean;
    metadata?: Record<string, unknown>;
  }): Promise<string>;
  searchByState(state: number[], k: number): Promise<Array<{
    episode: { action: string; reward: number; metadata?: Record<string, unknown> };
    similarity: number;
  }>>;
  getStats(): unknown;
}

// ─── Task identifier (scopes the Q-table within LearningEngine) ───────────────

const RECRUITMENT_TASK = "agent-recruitment";

/** Embedding dimensions used by the knowledge store — must match DIMS in embeddings.ts */
const DIMS = Config.embeddings.dimensions;

/** Exploration rate — 20% random recruitment to continue discovering useful agents. */
const EPSILON = 0.2;

// ─── AgentLearningLayer ───────────────────────────────────────────────────────

export class AgentLearningLayer {
  private engine: RuLearningEngine;
  private db!: RuFastAgentDB;
  private readonly persistPath: string;
  private ready = false;

  constructor() {
    this.engine = new LearningEngine();
    this.engine.configure(RECRUITMENT_TASK, {
      algorithm: "q-learning",
      learningRate: 0.15,
      discountFactor: 0.9,
      epsilon: EPSILON,
    });
    this.persistPath = Config.persistence.learningFile;
  }

  async init(): Promise<void> {
    this.db = createFastAgentDB(DIMS, 50_000);
    await this.loadQTable();
    this.ready = true;
    logger.info("Agent Q-learning layer ready");
  }

  /**
   * Reorder candidates by Q-learning policy.
   *
   * Merges the Qdrant semantic search result (already sorted by embedding
   * similarity) with the learned Q-values for the current state. The best
   * Q-learning candidate is promoted to the front when the engine has enough
   * confidence (> 0.25). This keeps semantic diversity (we never drop
   * candidates) while biasing toward proven performers.
   */
  rankCandidates(
    phase: string,
    jurisdiction: string | undefined,
    workflowType: string,
    candidateIds: string[],
  ): string[] {
    if (!this.ready || candidateIds.length < 2) return candidateIds;
    const state = this.stateKey(phase, jurisdiction, workflowType);
    try {
      const result = this.engine.getBestAction(RECRUITMENT_TASK, state, candidateIds);
      if (result && result.confidence > 0.25) {
        return [result.action, ...candidateIds.filter((id) => id !== result.action)];
      }
    } catch (err) {
      logger.warn("Q-learning getBestAction failed", { error: (err as Error).message });
    }
    return candidateIds;
  }

  /**
   * Record the outcome of an agent's participation in a round.
   * Drives the Q-table update and stores the episode in FastAgentDB.
   *
   * @param stateEmbedding - embedding vector of the round goal (for FastAgentDB similarity search)
   */
  async recordEpisode(params: {
    phase: string;
    nextPhase: string;
    jurisdiction?: string;
    workflowType: string;
    agentId: string;
    reward: number;       // 0–1 effective confidence
    done: boolean;        // true on the final phase
    stateEmbedding?: number[];
  }): Promise<void> {
    if (!this.ready) return;
    const state     = this.stateKey(params.phase, params.jurisdiction, params.workflowType);
    const nextState = this.stateKey(params.nextPhase, params.jurisdiction, params.workflowType);

    // Q-learning update — adjusts the value of (state, agentId) pair
    try {
      this.engine.update(RECRUITMENT_TASK, {
        state,
        action: params.agentId,
        reward: params.reward,
        nextState,
        done: params.done,
      });
    } catch (err) {
      logger.warn("Q-learning update failed", { error: (err as Error).message });
    }

    // Episode storage — enables similarity-based retrieval from FastAgentDB
    if (params.stateEmbedding?.length) {
      try {
        await this.db.storeEpisode({
          state: params.stateEmbedding,
          action: params.agentId,
          reward: params.reward,
          nextState: params.stateEmbedding,
          done: params.done,
          metadata: {
            phase: params.phase,
            jurisdiction: params.jurisdiction ?? "any",
            workflowType: params.workflowType,
          },
        });
      } catch (err) {
        logger.warn("FastAgentDB storeEpisode failed", { error: (err as Error).message });
      }
    }

    // Persist Q-table after each update (fire-and-forget)
    this.persistQTable().catch((err) =>
      logger.warn("Q-table persist failed", { error: (err as Error).message }),
    );
  }

  /**
   * Find high-performing agents from similar past situations using
   * FastAgentDB vector similarity — complements Q-table lookup.
   */
  async similarEpisodes(
    stateEmbedding: number[],
    topK: number = 5,
  ): Promise<Array<{ agentId: string; reward: number }>> {
    if (!this.ready || !stateEmbedding.length) return [];
    try {
      const results = await this.db.searchByState(stateEmbedding, topK);
      return results.map((r) => ({ agentId: r.episode.action, reward: r.episode.reward }));
    } catch {
      return [];
    }
  }

  getStats(): unknown {
    return this.ready ? this.engine.getStats() : null;
  }

  // ─── Persistence ─────────────────────────────────────────────────────────

  private async loadQTable(): Promise<void> {
    try {
      const raw = await readFile(this.persistPath, "utf8");
      this.engine.import(JSON.parse(raw));
      logger.info("Q-table loaded from disk", { path: this.persistPath });
    } catch {
      // No prior Q-table — starts from scratch (expected on first run)
    }
  }

  private async persistQTable(): Promise<void> {
    const tmp = `${this.persistPath}.tmp`;
    await writeFile(tmp, JSON.stringify(this.engine.export()), "utf8");
    await rename(tmp, this.persistPath);
  }

  private stateKey(phase: string, jurisdiction?: string, workflowType?: string): string {
    return `${phase}:${jurisdiction ?? "any"}:${workflowType ?? "any"}`;
  }
}

export const agentLearning = new AgentLearningLayer();
