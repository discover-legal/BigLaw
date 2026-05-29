// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3.
// See <https://www.gnu.org/licenses/gpl-3.0.html>

/**
 * Top-level orchestrator — ties the full system together.
 *
 * Lifecycle per task:
 *   init → plan phases → for each phase: run DyTopo rounds → apply protocols → gate check → next phase
 *   → final synthesis
 *
 * The Root Orchestrator agent (tier 0) generates all RoundGoals via Claude.
 * The DyTopo engine assembles the agent graph per round from the registry.
 * Findings flow through the debate + verification protocols before final output.
 */

import Anthropic from "@anthropic-ai/sdk";
import { EventEmitter } from "events";
import { v4 as uuidv4 } from "uuid";
import { Config } from "./config.js";
import { logger } from "./logger.js";
import { AgentRegistry } from "./agents/registry.js";
import { Agent } from "./agents/base.js";
import { ROOT_ORCHESTRATOR, ALL_AGENT_DEFINITIONS } from "./agents/definitions.js";
import { DyTopoEngine } from "./dytopo/engine.js";
import { InterRoundMemoryStore } from "./memory/index.js";
import { KnowledgeStore } from "./knowledge/index.js";
import {
  applyCitationGate,
  runDebate,
  runVerificationPipeline,
  identifyGateRequests,
} from "./protocols/index.js";
import type {
  Task,
  TaskStatus,
  WorkflowType,
  TaskPhase,
  RoundGoal,
  Finding,
  GateRequest,
} from "./types.js";

const anthropic = new Anthropic({ apiKey: Config.anthropic.apiKey });

const PHASE_SEQUENCES: Record<WorkflowType, TaskPhase[]> = {
  counsel:     ["intake", "research", "drafting", "delivery"],
  roundtable:  ["intake", "research", "analysis", "drafting", "review", "delivery"],
  adversarial: ["intake", "research", "analysis", "review", "verification", "delivery"],
  review:      ["intake", "analysis", "review", "verification", "delivery"],
  tabulate:    ["intake", "analysis", "delivery"],
  full_bench:  ["intake", "research", "analysis", "drafting", "review", "verification", "delivery"],
};

export class Orchestrator {
  readonly registry: AgentRegistry;
  readonly memory: InterRoundMemoryStore;
  readonly knowledge: KnowledgeStore;

  private readonly tasks: Map<string, Task> = new Map();
  private readonly gateEmitter = new EventEmitter();
  private engine!: DyTopoEngine;
  private rootAgent!: Agent;

  constructor() {
    this.registry = new AgentRegistry();
    this.memory = new InterRoundMemoryStore();
    this.knowledge = new KnowledgeStore();
  }

  async init(): Promise<void> {
    await Promise.all([
      this.registry.init(),
      this.memory.init(),
      this.knowledge.init(),
    ]);

    // Seed agent registry if empty
    const existing = await this.registry.listAll();
    if (!existing.length) {
      logger.info("Seeding agent registry with default agents…");
      await this.registry.registerAll(ALL_AGENT_DEFINITIONS);
    }

    this.rootAgent = new Agent(ROOT_ORCHESTRATOR);
    this.engine = new DyTopoEngine({
      registry: this.registry,
      memory: this.memory,
      knowledge: this.knowledge,
      pinnedAgents: [ROOT_ORCHESTRATOR],
    });

    logger.info("Orchestrator ready");
  }

  // ─── Task management ──────────────────────────────────────────────────────

  async submitTask(params: {
    description: string;
    workflowType: WorkflowType;
    documentIds?: string[];
  }): Promise<Task> {
    const phases = PHASE_SEQUENCES[params.workflowType];
    const task: Task = {
      id: uuidv4(),
      description: params.description,
      documentIds: params.documentIds ?? [],
      workflowType: params.workflowType,
      status: "pending",
      currentPhase: phases[0],
      currentRound: 0,
      maxRounds: Config.dytopo.maxRounds,
      activeAgentIds: [],
      rounds: [],
      findings: [],
      pendingGates: [],
      createdAt: new Date(),
      updatedAt: new Date(),
    };

    this.tasks.set(task.id, task);
    logger.info("Task submitted", { taskId: task.id, workflow: params.workflowType });

    // Run asynchronously — callers poll getTask() for status
    this.runTask(task).catch((err) => {
      logger.error("Task execution failed", { taskId: task.id, error: err.message });
      task.status = "failed";
      task.error = err.message;
    });

    return task;
  }

  getTask(taskId: string): Task | null {
    return this.tasks.get(taskId) ?? null;
  }

  listTasks(): Task[] {
    return Array.from(this.tasks.values());
  }

  /**
   * Human approves or rejects a gate request.
   * Approved findings proceed to output; rejected are discarded.
   */
  approveGate(taskId: string, gateId: string, note?: string): void {
    const task = this.tasks.get(taskId);
    if (!task) throw new Error(`Task not found: ${taskId}`);
    const gate = task.pendingGates.find((g) => g.id === gateId);
    if (!gate) throw new Error(`Gate not found: ${gateId}`);
    gate.status = "approved";
    gate.reviewerNote = note;
    gate.reviewedAt = new Date();
    task.updatedAt = new Date();
    this.gateEmitter.emit(`gates:${taskId}`);
  }

  rejectGate(taskId: string, gateId: string, reason: string): void {
    const task = this.tasks.get(taskId);
    if (!task) throw new Error(`Task not found: ${taskId}`);
    const gate = task.pendingGates.find((g) => g.id === gateId);
    if (!gate) throw new Error(`Gate not found: ${gateId}`);
    gate.status = "rejected";
    gate.reviewerNote = reason;
    gate.reviewedAt = new Date();
    task.findings = task.findings.filter((f) => f.id !== gate.findingId);
    task.updatedAt = new Date();
    this.gateEmitter.emit(`gates:${taskId}`);
  }

  // ─── Internal task runner ─────────────────────────────────────────────────

  private async runTask(task: Task): Promise<void> {
    task.status = "running";
    const phases = PHASE_SEQUENCES[task.workflowType];

    for (const phase of phases) {
      task.currentPhase = phase;
      task.updatedAt = new Date();
      await this.runPhase(task, phase);

      // Wait for any pending gates before continuing
      if (task.pendingGates.some((g) => g.status === "pending")) {
        task.status = "awaiting_gate";
        await this.waitForGates(task);
        task.status = "running";
      }
    }

    // Final synthesis by root orchestrator
    task.output = await this.synthesise(task);
    task.status = "complete";
    task.completedAt = new Date();
    task.updatedAt = new Date();

    logger.info("Task complete", { taskId: task.id, findings: task.findings.length });
  }

  private async runPhase(task: Task, phase: TaskPhase): Promise<void> {
    logger.info("Phase starting", { taskId: task.id, phase });

    // Root orchestrator generates the round goal for this phase
    const goal = await this.generateRoundGoal(task, phase);
    goal.round = ++task.currentRound;

    // Run DyTopo round
    const roundState = await this.engine.runRound(task, goal);
    task.rounds.push(roundState);

    // Build source-text map for citation gate (from knowledge store)
    const sourceTexts = await this.buildSourceTextMap(task.documentIds);

    // Apply protocols to raw findings
    const rawFindings = roundState.findings;
    const { passed } = applyCitationGate(rawFindings, sourceTexts);

    // Debate each passing finding
    const debated = await Promise.all(
      passed.map((f) => runDebate(f, "adversarial-challenger")),
    );

    // Verification pipeline
    const verified = await Promise.all(
      debated.map((f) => runVerificationPipeline(f)),
    );

    // Add findings to task
    task.findings.push(...debated);

    // Identify gate requests
    const gates = identifyGateRequests(task.id, debated);
    task.pendingGates.push(...gates);

    task.updatedAt = new Date();
    logger.info("Phase complete", {
      taskId: task.id,
      phase,
      findings: debated.length,
      gates: gates.length,
    });
  }

  private async generateRoundGoal(task: Task, phase: TaskPhase): Promise<RoundGoal> {
    const priorPhases = task.rounds.map((r) => r.goal.phase);
    const prompt = `TASK: ${task.description}

WORKFLOW: ${task.workflowType}
CURRENT PHASE: ${phase}
PRIOR PHASES COMPLETED: ${priorPhases.join(", ") || "none"}
FINDINGS SO FAR: ${task.findings.length}

Generate a specific, actionable round goal for the ${phase} phase.
Format:
DESCRIPTION: <one paragraph describing what agents should do this round>
EXPECTED_OUTPUT_1: <first expected output>
EXPECTED_OUTPUT_2: <second expected output>
EXPECTED_OUTPUT_3: <third expected output>`;

    const msg = await anthropic.messages.create({
      model: Config.anthropic.model,
      max_tokens: 600,
      system: ROOT_ORCHESTRATOR.systemPrompt,
      messages: [{ role: "user", content: prompt }],
    });

    const text = msg.content[0].type === "text" ? msg.content[0].text : "";
    const descMatch = text.match(/DESCRIPTION:\s*([\s\S]+?)(?=EXPECTED_OUTPUT|$)/i);
    const outputMatches = [...text.matchAll(/EXPECTED_OUTPUT_\d+:\s*(.+)/gi)];

    return {
      id: uuidv4(),
      round: task.currentRound,
      phase,
      description: descMatch?.[1]?.trim() ?? `Execute the ${phase} phase for: ${task.description}`,
      expectedOutputs: outputMatches.map((m) => m[1].trim()),
    };
  }

  private async synthesise(task: Task): Promise<string> {
    const findingsSummary = task.findings
      .filter((f) => !task.pendingGates.some((g) => g.findingId === f.id && g.status === "rejected"))
      .map((f, i) => `[${i + 1}] (${f.agentName}, Round ${f.round}) ${f.content}`)
      .join("\n\n");

    const prompt = `TASK: ${task.description}

ALL FINDINGS FROM ALL ROUNDS:
${findingsSummary}

Produce the final legal output for this task. Structure appropriately for the workflow type: ${task.workflowType}.
Every claim must trace to a specific finding number from the list above.`;

    const msg = await anthropic.messages.create({
      model: Config.anthropic.model,
      max_tokens: 4000,
      system: ROOT_ORCHESTRATOR.systemPrompt,
      messages: [{ role: "user", content: prompt }],
    });

    return msg.content[0].type === "text" ? msg.content[0].text : "";
  }

  private async buildSourceTextMap(docIds: string[]): Promise<Map<string, string>> {
    const map = new Map<string, string>();
    await Promise.all(
      docIds.map(async (id) => {
        const text = await this.knowledge.getFullText(id);
        if (text) map.set(id, text);
      }),
    );
    return map;
  }

  private waitForGates(task: Task): Promise<void> {
    return new Promise((resolve) => {
      if (task.pendingGates.every((g) => g.status !== "pending")) {
        resolve();
        return;
      }
      const handler = () => {
        if (task.pendingGates.every((g) => g.status !== "pending")) {
          this.gateEmitter.off(`gates:${task.id}`, handler);
          resolve();
        }
      };
      this.gateEmitter.on(`gates:${task.id}`, handler);
    });
  }
}