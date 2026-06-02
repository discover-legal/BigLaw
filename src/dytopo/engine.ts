// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * DyTopo Engine — Dynamic Topology Routing for Multi-Agent Reasoning
 *
 * Based on arXiv:2602.06039 (Lu et al., 2026).
 * Each reasoning round:
 *   1. Manager issues a RoundGoal (natural language).
 *   2. Recruited agents emit Need + Offer descriptors conditioned on the goal.
 *   3. Need/Offer embeddings are matched via cosine similarity.
 *   4. A sparse directed communication graph is constructed (edges above threshold).
 *   5. Messages are routed along edges (offering agent's content → needing agent's context).
 *   6. Agents process their context and produce findings.
 *   7. Round state is written to inter-round memory.
 *
 * Extended beyond the paper: agents are recruited from a live vector DB (AgentRegistry)
 * based on semantic match against the round goal, not from a fixed roster.
 */

import { v4 as uuidv4 } from "uuid";
import { Config } from "../config.js";
import { embed, embedBatch, cosineSimilarity } from "../embeddings.js";
import { logger } from "../logger.js";
import { Agent } from "../agents/base.js";
import { AgentRegistry } from "../agents/registry.js";
import { globalToolRegistry } from "../tools/index.js";
import { IntraRoundMemoryStore, InterRoundMemoryStore } from "../memory/index.js";
import { getProvider, resolveModelId } from "../providers/index.js";
import { selectModel } from "../routing/model.js";
import type { KnowledgeStore } from "../knowledge/index.js";
import type {
  AgentDefinition,
  AgentMessage,
  CommunicationEdge,
  Finding,
  NeedDescriptor,
  OfferDescriptor,
  RoundGoal,
  RoundState,
  Task,
} from "../types.js";
import { jurisdictionMatch } from "./jurisdiction.js";

export { jurisdictionMatch } from "./jurisdiction.js";

export interface DyTopoOptions {
  registry: AgentRegistry;
  memory: InterRoundMemoryStore;
  knowledge: KnowledgeStore;
  /** Agents pre-selected for this round (e.g. tier-0 root is always included) */
  pinnedAgents?: AgentDefinition[];
}

export class DyTopoEngine {
  private readonly registry: AgentRegistry;
  private readonly memory: InterRoundMemoryStore;
  private readonly knowledge: KnowledgeStore;
  private readonly pinnedAgents: AgentDefinition[];

  constructor(opts: DyTopoOptions) {
    this.registry = opts.registry;
    this.memory = opts.memory;
    this.knowledge = opts.knowledge;
    this.pinnedAgents = opts.pinnedAgents ?? [];
  }

  /**
   * Execute one round of DyTopo orchestration.
   * Returns the completed RoundState including all messages, edges, and findings.
   */
  async runRound(task: Task, goal: RoundGoal): Promise<RoundState> {
    const roundId = uuidv4();
    const intraMemory = new IntraRoundMemoryStore(roundId);

    logger.info("DyTopo round starting", {
      taskId: task.id,
      round: goal.round,
      phase: goal.phase,
      goal: goal.description.slice(0, 80),
    });

    // ── Step 1: Recruit agents from registry via semantic search on the round goal ──
    const recruitedAgents = await this.recruitAgents(goal, task.id);

    // Merge pinned + recruited, deduplicate
    const agentMap = new Map<string, AgentDefinition>();
    for (const a of [...this.pinnedAgents, ...recruitedAgents]) {
      agentMap.set(a.id, a);
    }

    // Filter out jurisdiction-specific agents whose jurisdiction doesn't match the task.
    // Agents with undefined/empty jurisdictions are always included (jurisdiction-neutral).
    const activeDefinitions = Array.from(agentMap.values())
      .filter((a) => jurisdictionMatch(a, task.jurisdiction))
      .slice(0, Config.dytopo.maxAgentsPerRound);
    const activeAgents = activeDefinitions.map((d) => new Agent(d));

    logger.info("Agents recruited for round", {
      round: goal.round,
      agents: activeDefinitions.map((a) => a.name),
    });

    // ── Step 2: Retrieve inter-round memory for each agent ──
    const agentMemories = await this.fetchAgentMemories(activeDefinitions, task, goal);

    // ── Step 3: Each agent generates Need + Offer descriptors ──
    const needsOffers = await Promise.all(
      activeAgents.map(async (agent) => {
        const memoryEntries = agentMemories.get(agent.definition.id) ?? [];
        return agent.generateNeedOffer({
          roundGoal: goal,
          incomingMessages: [],  // empty at descriptor stage
          memoryEntries,
          taskDescription: task.description,
        });
      }),
    );

    const needs: NeedDescriptor[] = needsOffers.map((no) => no.need);
    const offers: OfferDescriptor[] = needsOffers.map((no) => no.offer);

    // ── Step 4: Embed descriptors and build sparse communication graph ──
    const edges = await this.buildCommGraph(needs, offers, activeDefinitions);

    logger.info("Communication graph built", {
      round: goal.round,
      edges: edges.length,
      threshold: Config.dytopo.similarityThreshold,
    });

    // ── Step 5: Route messages along edges ──
    const messages = this.routeMessages(edges, offers, goal.round);
    for (const msg of messages) {
      intraMemory.recordMessage(msg.to, msg);
    }

    // ── Step 6: Agents process their context and produce findings ──
    const allFindings = await this.processAgents(activeAgents, agentMemories, intraMemory, task, goal);

    // Tag findings with round number and record to intra-round memory
    for (const finding of allFindings) {
      finding.round = goal.round;
      intraMemory.recordFinding(finding.agentId, finding);
    }

    // ── Step 7: Write round memory ──
    await this.persistRoundMemory(task, goal, allFindings, intraMemory);

    const state: RoundState = {
      roundId,
      goal,
      activeAgentIds: activeDefinitions.map((a) => a.id),
      edges,
      messages,
      findings: allFindings,
      status: "complete",
      startedAt: new Date(),
      completedAt: new Date(),
    };

    logger.info("DyTopo round complete", {
      round: goal.round,
      findings: allFindings.length,
      messages: messages.length,
    });

    return state;
  }

  // ─── Private helpers ────────────────────────────────────────────────────────

  private async recruitAgents(goal: RoundGoal, taskId: string): Promise<AgentDefinition[]> {
    const phaseQueries: Record<string, { tier?: 1 | 2 | 3 }> = {
      intake: { tier: 1 },
      research: { tier: 2 },
      analysis: { tier: 2 },
      drafting: { tier: 2 },
      review: { tier: 2 },
      verification: { tier: 2 },
      delivery: { tier: 1 },
    };
    const opts = phaseQueries[goal.phase] ?? {};
    return this.registry.search(goal.description, {
      ...opts,
      topK: Config.dytopo.maxAgentsPerRound - 1,
    });
  }

  private async fetchAgentMemories(
    agents: AgentDefinition[],
    task: Task,
    goal: RoundGoal,
  ): Promise<Map<string, import("../types.js").MemoryEntry[]>> {
    const map = new Map<string, import("../types.js").MemoryEntry[]>();
    await Promise.all(
      agents.map(async (agent) => {
        const entries = await this.memory.query(goal.description, {
          taskId: task.id,
          agentId: agent.id,
          beforeRound: goal.round,
          topK: 6,
        });
        // Also fetch task-level summaries
        const taskEntries = await this.memory.query(goal.description, {
          taskId: task.id,
          beforeRound: goal.round,
          topK: 4,
        });
        map.set(agent.id, [...entries, ...taskEntries]);
      }),
    );
    return map;
  }

  private async buildCommGraph(
    needs: NeedDescriptor[],
    offers: OfferDescriptor[],
    agents: AgentDefinition[],
  ): Promise<CommunicationEdge[]> {
    // Embed all descriptors in batch
    const needTexts = needs.map((n) => n.text);
    const offerTexts = offers.map((o) => o.text);
    const allTexts = [...needTexts, ...offerTexts];

    const embeddings = await embedBatch(allTexts);

    const needEmbeddings = embeddings.slice(0, needs.length).map((e) => e.embedding);
    const offerEmbeddings = embeddings.slice(needs.length).map((e) => e.embedding);

    const edges: CommunicationEdge[] = [];
    const threshold = Config.dytopo.similarityThreshold;

    for (let i = 0; i < needs.length; i++) {
      for (let j = 0; j < offers.length; j++) {
        // An agent does not route messages to itself
        if (needs[i].agentId === offers[j].agentId) continue;
        const sim = cosineSimilarity(needEmbeddings[i], offerEmbeddings[j]);
        if (sim >= threshold) {
          edges.push({
            from: offers[j].agentId,   // offering agent → sends to needing agent
            to: needs[i].agentId,
            similarity: sim,
            offerText: offers[j].text,
          });
        }
      }
    }

    // Sort edges by similarity descending for cleaner logs
    return edges.sort((a, b) => b.similarity - a.similarity);
  }

  private routeMessages(
    edges: CommunicationEdge[],
    offers: OfferDescriptor[],
    round: number,
  ): AgentMessage[] {
    const offerMap = new Map(offers.map((o) => [o.agentId, o.text]));
    return edges.map((edge) => ({
      id: uuidv4(),
      from: edge.from,
      to: edge.to,
      content: `[Offer from ${edge.from}] ${(offerMap.get(edge.from) ?? "").slice(0, 500)}`,
      round,
      timestamp: new Date(),
    }));
  }

  /**
   * Two-wave intra-round processing:
   *
   * Wave 1 (parallel) — every agent runs its full agentic loop and produces findings.
   * Broadcast build    — all Wave 1 findings are written to the shared context (the
   *                      "intra-round whiteboard") and published to IntraRoundMemoryStore.
   * Wave 2 (parallel) — every agent receives the full shared context and runs a
   *                      lightweight Haiku review pass to challenge errors or add
   *                      missing analysis. No tool loop, short token budget (400).
   *
   * Combined findings from both waves feed the citation gate, debate, and verification
   * pipeline downstream. Wave 2 errors are swallowed — a bad model response never
   * crashes the round.
   */
  private async processAgents(
    agents: Agent[],
    agentMemories: Map<string, import("../types.js").MemoryEntry[]>,
    intraMemory: IntraRoundMemoryStore,
    task: Task,
    goal: RoundGoal,
  ): Promise<Finding[]> {
    // ── Wave 1: full agentic-loop processing ──────────────────────────────────
    const wave1Results = await Promise.all(
      agents.map(async (agent) => {
        const memoryEntries = agentMemories.get(agent.definition.id) ?? [];
        const incomingMessages = intraMemory.getMessagesFor(agent.definition.id);
        return agent.process({
          roundGoal: goal,
          incomingMessages,
          memoryEntries,
          taskDescription: task.description,
          taskId: task.id,
          toolRegistry: globalToolRegistry,
          knowledge: this.knowledge,
          memory: this.memory,
          ownerId: task.createdByProfileId,
        });
      }),
    );
    const wave1Findings = wave1Results.flat();

    // ── Build shared context from Wave 1 findings ─────────────────────────────
    // Each finding is written as a single-line summary to the intra-round
    // whiteboard. Cap at 200 chars per finding to keep the broadcast prompt tight.
    for (const f of wave1Findings) {
      const line = `[${f.agentName}] ${f.content.replace(/\s+/g, " ").slice(0, 200)}`;
      intraMemory.addSharedContext(line);
    }
    const sharedContext = intraMemory.getSharedContext();

    if (sharedContext.length) {
      logger.debug("Intra-round broadcast context built", {
        round: goal.round,
        entries: sharedContext.length,
      });
    }

    // ── Wave 2: broadcast review (Haiku, lightweight) ─────────────────────────
    const wave2Results = await Promise.all(
      agents.map((agent) => {
        const memoryEntries = agentMemories.get(agent.definition.id) ?? [];
        return agent.reviewWithBroadcast({
          roundGoal: goal,
          incomingMessages: [],
          memoryEntries,
          taskDescription: task.description,
          sharedContext,
        });
      }),
    );
    const wave2Findings = wave2Results.flat();

    if (wave2Findings.length) {
      logger.debug("Wave 2 broadcast review produced findings", {
        round: goal.round,
        count: wave2Findings.length,
      });
    }

    return [...wave1Findings, ...wave2Findings];
  }

  /**
   * Persist intra-round findings as individual memory entries, then synthesize
   * a round-level rollup via Haiku. The rollup is a 2-3 sentence digest of the
   * round's key conclusions — much richer than a string truncation.
   */
  private async persistRoundMemory(
    task: Task,
    goal: RoundGoal,
    findings: Finding[],
    intraMemory: IntraRoundMemoryStore,
  ): Promise<void> {
    // Write individual finding memories (in parallel — Qdrant upserts are idempotent)
    await Promise.all(
      findings.map((f) =>
        this.memory.writeFindingMemory({
          taskId: task.id,
          round: goal.round,
          phase: goal.phase,
          agentId: f.agentId,
          finding: f,
        }),
      ),
    );

    // Build the round rollup — Haiku synthesis of all findings, falling back to
    // the naive concatenation if the model call fails so memory always gets written.
    let summaryContent: string;
    if (findings.length) {
      const bulletList = findings
        .slice(0, 12)
        .map((f) => `- [${f.agentName}] ${f.content.replace(/\s+/g, " ").slice(0, 150)}`)
        .join("\n");
      try {
        const model = selectModel({ tier: 3, type: "tool", taskType: "descriptor" });
        const provider = getProvider(model);
        const response = await provider.chat({
          model: resolveModelId(model),
          maxTokens: 300,
          system: "You are a legal analysis synthesizer. Produce a concise inter-round memory digest.",
          messages: [{
            role: "user",
            content: `Round ${goal.round} (${goal.phase}) findings:\n${bulletList}\n\nSummarise the key legal conclusions from this round in 2-3 sentences. Be specific — name parties, statutes, or doctrines where present. This summary will be retrieved as memory by agents in the next round.`,
          }],
        });
        const textBlock = response.content.find((b) => b.type === "text");
        summaryContent = textBlock?.type === "text" ? textBlock.text.trim() : bulletList;
      } catch {
        summaryContent = `Round ${goal.round} key findings: ${findings.slice(0, 3).map((f) => f.content.slice(0, 100)).join("; ")}`;
      }
    } else {
      summaryContent = `Round ${goal.round} (${goal.phase}): No findings produced.`;
    }

    await this.memory.writeRoundSummary({
      taskId: task.id,
      round: goal.round,
      phase: goal.phase,
      summary: summaryContent,
      findingCount: findings.length,
    });
  }
}