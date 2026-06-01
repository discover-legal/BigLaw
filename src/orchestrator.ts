// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

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

import { EventEmitter } from "events";
import { readdir, readFile, writeFile } from "fs/promises";
import { join, extname } from "path";
import { v4 as uuidv4 } from "uuid";
import { Config } from "./config.js";
import { logger } from "./logger.js";
import { getProvider, resolveModelId } from "./providers/index.js";
import { selectModel } from "./routing/model.js";
import { auditLogger } from "./audit/index.js";
import { AgentRegistry } from "./agents/registry.js";
import { Agent } from "./agents/base.js";
import { ROOT_ORCHESTRATOR, ALL_AGENT_DEFINITIONS } from "./agents/definitions.js";
import { SettingsStore } from "./settings/index.js";
import { ProfileStore } from "./auth/index.js";
import { ClientStore } from "./clients/index.js";
import { DyTopoEngine } from "./dytopo/engine.js";
import { InterRoundMemoryStore } from "./memory/index.js";
import { KnowledgeStore } from "./knowledge/index.js";
import { TemplateStore } from "./templates/store.js";
import { LavernAdapter, instantiateTemplate, fromExternalConfig, fromMikeOSSWorkflow } from "./adapters/lavern.js";
import type { TaskTemplate, ExternalAgentConfig, MikeOSSWorkflow } from "./adapters/lavern.js";
import {
  applyCitationGate,
  runDebate,
  runVerificationPipeline,
  identifyGateRequests,
} from "./protocols/index.js";
import type {
  Task,
  WorkflowType,
  TaskPhase,
  RoundGoal,
  TaskTable,
} from "./types.js";

const PHASE_SEQUENCES: Record<WorkflowType, TaskPhase[]> = {
  counsel:     ["intake", "research", "drafting", "delivery"],
  roundtable:  ["intake", "research", "analysis", "drafting", "review", "delivery"],
  adversarial: ["intake", "research", "analysis", "review", "verification", "delivery"],
  review:      ["intake", "analysis", "review", "verification", "delivery"],
  tabulate:    ["intake", "analysis", "delivery"],
  full_bench:  ["intake", "research", "analysis", "drafting", "review", "verification", "delivery"],
};

/**
 * Best-effort extraction of a single JSON object from an LLM response.
 * Strips markdown fences and isolates the outermost {...} before parsing.
 */
function parseJsonObject(text: string): unknown | undefined {
  const stripped = text.replace(/```(?:json)?/gi, "").trim();
  const start = stripped.indexOf("{");
  const end = stripped.lastIndexOf("}");
  if (start === -1 || end === -1 || end <= start) return undefined;
  try {
    return JSON.parse(stripped.slice(start, end + 1));
  } catch {
    return undefined;
  }
}

export class Orchestrator {
  readonly registry: AgentRegistry;
  readonly memory: InterRoundMemoryStore;
  readonly knowledge: KnowledgeStore;
  readonly templates: TemplateStore;
  readonly settings: SettingsStore;
  readonly profiles: ProfileStore;
  readonly clients: ClientStore;

  private readonly tasks: Map<string, Task> = new Map();
  private readonly gateEmitter = new EventEmitter();
  readonly progressEmitter = new EventEmitter();
  private engine!: DyTopoEngine;
  private rootAgent!: Agent;

  constructor() {
    this.registry = new AgentRegistry();
    this.memory = new InterRoundMemoryStore();
    this.knowledge = new KnowledgeStore();
    this.templates = new TemplateStore();
    this.settings = new SettingsStore();
    this.profiles = new ProfileStore();
    this.clients = new ClientStore();
  }

  async init(): Promise<void> {
    // Load persisted admin settings first so they apply before any task runs.
    await this.settings.init();
    await this.profiles.init();
    await this.clients.init();
    await Promise.all([
      this.registry.init(),
      this.memory.init(),
      this.knowledge.init(),
      this.templates.load(),
    ]);

    // Seed agent registry if empty
    const existing = await this.registry.listAll();
    if (!existing.length) {
      logger.info("Seeding agent registry with default agents…");
      await this.registry.registerAll(ALL_AGENT_DEFINITIONS);
    }

    // Load external and Lavern agents from filesystem
    await this.loadExternalAgents();

    // Load MikeOSS workflow presets (native format) and register as templates
    await this.loadMikeOSSWorkflows();

    // Restore persisted tasks
    await this.restoreTasks();

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

  private static readonly MAX_CONCURRENT_TASKS = 10;
  private static readonly MAX_DESCRIPTION_CHARS = 20_000;

  async submitTask(params: {
    description: string;
    workflowType: WorkflowType;
    documentIds?: string[];
    clientNumber?: string;
    matterNumber?: string;
    createdByProfileId?: string;
  }): Promise<Task> {
    if (params.description.length > Orchestrator.MAX_DESCRIPTION_CHARS) {
      throw new Error(
        `Task description exceeds the ${Orchestrator.MAX_DESCRIPTION_CHARS.toLocaleString()} character limit ` +
        `(${params.description.length.toLocaleString()} received). Please shorten the description.`,
      );
    }
    const running = Array.from(this.tasks.values()).filter((t) => t.status === "running").length;
    if (running >= Orchestrator.MAX_CONCURRENT_TASKS) {
      throw new Error(`Server at capacity: ${running} tasks already running. Please wait for one to complete.`);
    }
    const phases = PHASE_SEQUENCES[params.workflowType];
    const task: Task = {
      id: uuidv4(),
      description: params.description,
      clientNumber: params.clientNumber?.trim() || undefined,
      matterNumber: params.matterNumber?.trim() || undefined,
      documentIds: params.documentIds ?? [],
      createdByProfileId: params.createdByProfileId,
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
    auditLogger.write({ event: "task.created", taskId: task.id, data: { description: params.description, workflowType: params.workflowType } });

    // Run asynchronously — callers poll getTask() for status
    this.runTask(task).catch((err) => {
      logger.error("Task execution failed", { taskId: task.id, error: err.message });
      task.status = "failed";
      task.error = err.message;
      this.emit(task.id, "failed", { error: err.message });
      auditLogger.write({ event: "task.failed", taskId: task.id, data: { error: err.message } });
    });

    return task;
  }

  getTask(taskId: string): Task | null {
    return this.tasks.get(taskId) ?? null;
  }

  listTasks(): Task[] {
    return Array.from(this.tasks.values());
  }

  /** Delete a matter and its Qdrant memory entries. Returns false if it didn't exist. */
  deleteTask(taskId: string): boolean {
    const existed = this.tasks.delete(taskId);
    if (existed) {
      this.persistTasks().catch((err) => logger.warn("Failed to persist tasks", { error: err.message }));
      // Clean up orphaned inter-round memory vectors so deleted task data
      // cannot be surfaced by future semantic memory queries.
      this.memory.deleteByTaskId(taskId).catch((err) =>
        logger.warn("Failed to delete task memory from Qdrant", { taskId, error: (err as Error).message }),
      );
      auditLogger.write({ event: "task.deleted", taskId, data: {} });
      logger.info("Task deleted", { taskId });
    }
    return existed;
  }

  /** Set the lawyer(s) assigned to a matter (a partner action). */
  assignLawyers(taskId: string, lawyerIds: string[]): Task | null {
    const task = this.tasks.get(taskId);
    if (!task) return null;
    const valid = [...new Set(lawyerIds)].filter((id) => this.profiles.get(id));
    task.assignedLawyerIds = valid;
    task.updatedAt = new Date();
    this.persistTasks().catch((err) => logger.warn("Failed to persist tasks", { error: err.message }));
    auditLogger.write({ event: "task.assigned", taskId, data: { lawyerIds: valid } });
    return task;
  }

  listTemplates(): TaskTemplate[] {
    return this.templates.list();
  }

  async submitFromTemplate(
    templateId: string,
    substitutions: Record<string, string> = {},
    documentIds?: string[],
    refs?: { clientNumber?: string; matterNumber?: string; createdByProfileId?: string },
  ): Promise<Task> {
    const template = this.templates.get(templateId);
    if (!template) throw new Error(`Template not found: ${templateId}`);
    const { description, workflowType } = instantiateTemplate(template, substitutions);
    return this.submitTask({ description, workflowType, documentIds, ...refs });
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
    auditLogger.write({ event: "gate.approved", taskId, data: { gateId, note } });
    this.gateEmitter.emit(`gates:${taskId}`);
    this.persistTasks().catch((err) => logger.warn("Failed to persist tasks", { error: err.message }));
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
    auditLogger.write({ event: "gate.rejected", taskId, data: { gateId, reason } });
    this.gateEmitter.emit(`gates:${taskId}`);
    this.persistTasks().catch((err) => logger.warn("Failed to persist tasks", { error: err.message }));
  }

  // ─── External agent loader ────────────────────────────────────────────────

  private async loadExternalAgents(): Promise<void> {
    const dirs: Array<{ path: string; type: "external" | "lavern" }> = [
      { path: join(process.cwd(), "agents", "external"), type: "external" },
      { path: join(process.cwd(), "agents", "lavern"), type: "lavern" },
    ];

    const lavernAdapter = new LavernAdapter();

    for (const { path: dir, type } of dirs) {
      let entries: string[];
      try {
        entries = await readdir(dir);
      } catch {
        continue; // directory doesn't exist or isn't readable — skip silently
      }

      const defs = [];
      for (const entry of entries) {
        if (extname(entry) !== ".json") continue;
        try {
          const raw = await readFile(join(dir, entry), "utf8");
          const parsed = JSON.parse(raw);
          const items = Array.isArray(parsed) ? parsed : [parsed];
          if (type === "lavern") {
            defs.push(...lavernAdapter.fromConfigs(items));
          } else {
            defs.push(...(items as ExternalAgentConfig[]).map(fromExternalConfig));
          }
        } catch (err) {
          logger.warn("Failed to load external agent file", { file: entry, error: (err as Error).message });
        }
      }

      if (defs.length) {
        await this.registry.registerAll(defs);
        logger.info("External agents registered", { source: type, count: defs.length });
      }
    }
  }

  // ─── MikeOSS workflow loader ──────────────────────────────────────────────

  /**
   * Load MikeOSS workflow presets (native MikeOSSWorkflow format) from
   * workflows/mikeoss/ and register each as a TaskTemplate via the adapter.
   * MikeOSS workflows are task specifications, not agents — our agent system
   * executes them. Files may contain a single workflow or an array.
   */
  private async loadMikeOSSWorkflows(): Promise<void> {
    const dir = join(process.cwd(), "workflows", "mikeoss");
    let entries: string[];
    try {
      entries = await readdir(dir);
    } catch {
      return; // directory doesn't exist — skip silently
    }

    let loaded = 0;
    for (const entry of entries) {
      if (extname(entry) !== ".json") continue;
      try {
        const raw = await readFile(join(dir, entry), "utf8");
        const parsed = JSON.parse(raw) as MikeOSSWorkflow | MikeOSSWorkflow[];
        const items = Array.isArray(parsed) ? parsed : [parsed];
        for (const wf of items) {
          this.templates.add(fromMikeOSSWorkflow(wf));
          loaded++;
        }
      } catch (err) {
        logger.warn("Failed to load MikeOSS workflow file", { file: entry, error: (err as Error).message });
      }
    }

    if (loaded) logger.info("MikeOSS workflows registered as templates", { count: loaded });
  }

  // ─── Internal task runner ─────────────────────────────────────────────────

  private emit(taskId: string, type: string, data: unknown): void {
    this.progressEmitter.emit(`task:${taskId}`, { type, data });
  }

  private async runTask(task: Task): Promise<void> {
    task.status = "running";
    this.emit(task.id, "started", { taskId: task.id, workflowType: task.workflowType });
    auditLogger.write({ event: "task.started", taskId: task.id, data: { workflowType: task.workflowType } });
    const phases = PHASE_SEQUENCES[task.workflowType];

    for (const phase of phases) {
      task.currentPhase = phase;
      task.updatedAt = new Date();
      this.emit(task.id, "phase", { phase });
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

    // For tabulate workflows, also produce a structured spreadsheet-style table.
    if (task.workflowType === "tabulate") {
      try {
        task.table = await this.tabulate(task);
      } catch (err) {
        logger.warn("Tabulation failed; falling back to text output only", {
          taskId: task.id,
          error: (err as Error).message,
        });
      }
    }

    task.status = "complete";
    task.completedAt = new Date();
    task.updatedAt = new Date();
    this.emit(task.id, "complete", { findings: task.findings.length, output: task.output?.slice(0, 200) });
    auditLogger.write({ event: "task.complete", taskId: task.id, data: { findings: task.findings.length } });
    this.persistTasks().catch((err) => logger.warn("Failed to persist tasks", { error: err.message }));

    logger.info("Task complete", { taskId: task.id, findings: task.findings.length });
  }

  private async runPhase(task: Task, phase: TaskPhase): Promise<void> {
    logger.info("Phase starting", { taskId: task.id, phase });
    auditLogger.write({ event: "phase.start", taskId: task.id, data: { phase } });

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

    // Verification pipeline — mutates each finding in place, attaching its
    // verificationResult (read downstream by identifyGateRequests).
    await Promise.all(
      debated.map((f) => runVerificationPipeline(f)),
    );

    // Add findings to task
    task.findings.push(...debated);

    // Identify gate requests
    const gates = identifyGateRequests(task.id, debated);
    task.pendingGates.push(...gates);

    task.updatedAt = new Date();
    this.emit(task.id, "round", {
      round: task.currentRound,
      phase,
      findings: debated.length,
      gates: gates.length,
    });
    auditLogger.write({ event: "phase.complete", taskId: task.id, data: { phase, findings: debated.length, gates: gates.length } });
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

    const model = selectModel({ tier: 0, taskType: "synthesis" });
    const provider = getProvider(model);
    const response = await provider.chat({
      model: resolveModelId(model),
      maxTokens: 600,
      system: ROOT_ORCHESTRATOR.systemPrompt,
      messages: [{ role: "user", content: prompt }],
    });

    const textBlock = response.content.find((b) => b.type === "text");
    const text = textBlock?.type === "text" ? textBlock.text : "";
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

    const model = selectModel({ tier: 0, taskType: "synthesis" });
    const provider = getProvider(model);
    const response = await provider.chat({
      model: resolveModelId(model),
      maxTokens: 4000,
      system: ROOT_ORCHESTRATOR.systemPrompt,
      messages: [{ role: "user", content: prompt }],
    });

    const textBlock = response.content.find((b) => b.type === "text");
    return textBlock?.type === "text" ? textBlock.text : "";
  }

  /**
   * Extract the task's findings into a structured table for spreadsheet-style
   * review. The root orchestrator chooses appropriate columns for the subject
   * matter and maps each row back to a source finding via `_findingId`.
   */
  private async tabulate(task: Task): Promise<TaskTable | undefined> {
    const findings = task.findings.filter(
      (f) => !task.pendingGates.some((g) => g.findingId === f.id && g.status === "rejected"),
    );
    if (findings.length === 0) return undefined;

    const findingsSummary = findings
      .map((f) => `id=${f.id} | ${f.agentName} (R${f.round}, conf ${f.confidence.toFixed(2)}): ${f.content}`)
      .join("\n\n");

    const prompt = `TASK: ${task.description}

FINDINGS:
${findingsSummary}

Extract these findings into a structured table suitable for a spreadsheet review grid.
If the TASK above names the columns it wants, use exactly those column names and order.
Otherwise choose 3–6 columns that best capture the structured content for THIS subject matter.

Respond with ONLY valid JSON (no prose, no markdown fences) in exactly this shape:
{
  "columns": ["Column A", "Column B"],
  "rows": [
    { "Column A": "value", "Column B": "value", "_findingIds": ["<source finding id>", "..."] }
  ]
}

Rules:
- MERGE findings that make the same substantive point into a SINGLE row.
- Keep findings that make genuinely DIFFERENT points as SEPARATE rows, even when they concern the same clause or topic.
- "_findingIds" MUST list the bare id(s) of every finding that contributes to that row (no "id=" prefix or brackets).
- Do NOT add columns for source ids or confidence — those are attached automatically.
- Do NOT use column names beginning with an underscore.
- Every column listed in "columns" must appear as a key in every row.
- Keep cell values concise (a phrase or sentence, not paragraphs).`;

    const model = selectModel({ tier: 0, taskType: "synthesis" });
    const provider = getProvider(model);
    const response = await provider.chat({
      model: resolveModelId(model),
      maxTokens: 4000,
      system: ROOT_ORCHESTRATOR.systemPrompt,
      messages: [{ role: "user", content: prompt }],
    });

    const textBlock = response.content.find((b) => b.type === "text");
    const text = textBlock?.type === "text" ? textBlock.text : "";

    const parsed = parseJsonObject(text) as
      | { columns?: unknown; rows?: unknown }
      | undefined;
    if (!parsed || !Array.isArray(parsed.columns) || !Array.isArray(parsed.rows)) {
      logger.warn("Tabulation produced unparseable output", { taskId: task.id });
      return undefined;
    }

    const confByFinding = new Map(findings.map((f) => [f.id, f.confidence]));
    const validIds = new Set(findings.map((f) => f.id));
    // Display columns never include underscore-prefixed internal keys.
    const columns = parsed.columns.map((c) => String(c)).filter((c) => !c.startsWith("_"));

    const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
    // Collect every known finding id referenced anywhere in the row (the model
    // may use `_findingIds`, `_findingId`, or embed ids inside a stray value).
    const contributorsOf = (r: Record<string, unknown>): string[] => {
      const found = new Set<string>();
      const pool: string[] = [];
      const raw = (r._findingIds ?? r._findingId) as unknown;
      if (Array.isArray(raw)) pool.push(...raw.map(String));
      else if (raw != null) pool.push(String(raw));
      for (const v of Object.values(r)) pool.push(String(v ?? ""));
      for (const s of pool) for (const m of s.matchAll(UUID)) if (validIds.has(m[0])) found.add(m[0]);
      return [...found];
    };

    const byConfDesc = (a: string, b: string) => (confByFinding.get(b) ?? 0) - (confByFinding.get(a) ?? 0);

    const rawRows = (parsed.rows as Array<Record<string, unknown>>)
      .filter((r) => r && typeof r === "object")
      .map((r) => {
        const row: Record<string, string> = {};
        for (const col of columns) row[col] = r[col] != null ? String(r[col]) : "";
        const contributors = contributorsOf(r).sort(byConfDesc);
        const confs = contributors.map((id) => confByFinding.get(id)).filter((c): c is number => c != null);
        row._findingIds = contributors.join(",");
        row._findingId = contributors[0] ?? "";
        row._confidence = confs.length ? Math.max(...confs).toFixed(2) : "";
        row._sources = String(contributors.length);
        return row;
      });

    // Safety net: collapse rows whose visible cells are identical (the model
    // missed a merge). Genuinely distinct points differ in their cells and are
    // preserved — each keeps its own confidence.
    const merged = new Map<string, Record<string, string>>();
    for (const row of rawRows) {
      const key = columns.map((c) => (row[c] ?? "").trim().toLowerCase()).join("");
      const existing = merged.get(key);
      if (!existing) { merged.set(key, { ...row }); continue; }
      const ids = [...new Set([
        ...existing._findingIds.split(",").filter(Boolean),
        ...row._findingIds.split(",").filter(Boolean),
      ])].sort(byConfDesc);
      existing._findingIds = ids.join(",");
      existing._findingId = ids[0] ?? existing._findingId;
      existing._sources = String(ids.length);
      const maxConf = Math.max(parseFloat(existing._confidence || "0"), parseFloat(row._confidence || "0"));
      existing._confidence = maxConf ? maxConf.toFixed(2) : "";
    }

    const rows = [...merged.values()].sort(
      (a, b) => parseFloat(b._confidence || "0") - parseFloat(a._confidence || "0"),
    );
    const sourceFindingIds = [...new Set(rows.flatMap((r) => r._findingIds.split(",").filter(Boolean)))];

    return { columns, rows, sourceFindingIds, generatedAt: new Date() };
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

  // ─── Persistence ──────────────────────────────────────────────────────────

  async persistTasks(): Promise<void> {
    const path = Config.persistence.tasksFile;
    const serialisable = Array.from(this.tasks.values()).map((t) => ({
      ...t,
      createdAt: t.createdAt.toISOString(),
      updatedAt: t.updatedAt.toISOString(),
      completedAt: t.completedAt?.toISOString(),
    }));
    await writeFile(path, JSON.stringify(serialisable, null, 2), "utf8");
    logger.debug("Tasks persisted", { count: this.tasks.size, path });
  }

  async restoreTasks(): Promise<void> {
    const path = Config.persistence.tasksFile;
    let raw: string;
    try {
      raw = await readFile(path, "utf8");
    } catch {
      return; // no file yet — first run
    }

    try {
      const items = JSON.parse(raw) as Array<Record<string, unknown>>;
      for (const item of items) {
        const task = {
          ...item,
          createdAt: new Date(item.createdAt as string),
          updatedAt: new Date(item.updatedAt as string),
          completedAt: item.completedAt ? new Date(item.completedAt as string) : undefined,
        } as Task;
        this.tasks.set(task.id, task);
      }
      logger.info("Tasks restored from disk", { count: this.tasks.size, path });
    } catch (err) {
      logger.warn("Failed to restore tasks", { error: (err as Error).message });
    }
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