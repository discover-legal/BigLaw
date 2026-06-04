// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Backend abstraction — the seam that lets the MCP surface run either
 * in-process (owning the vector DB) or as a thin client to a separate
 * long-running backend.
 *
 * The orchestrator opens the RuVector HNSW stores under ./data with an
 * EXCLUSIVE single-writer lock and binds the REST API to one port. That makes
 * "two backends at once" impossible — exactly the collision you hit running a
 * browse server alongside the Claude Code MCP.
 *
 * The fix is to let only ONE process own the DB. Everything else talks to it:
 *
 *   LocalBackend  — wraps an in-process Orchestrator (owns the DB). The owner.
 *   RemoteBackend — forwards every operation to the owner's REST API over HTTP.
 *
 * `handleTool` (mcp/server.ts) is written against this interface, so the same
 * MCP tool definitions work whether the process is the owner or a client.
 */

import type { Orchestrator } from "../orchestrator.js";
import type { Task, WorkflowType } from "../types.js";
import type { TaskTemplate } from "../adapters/lavern.js";
import { auditLogger } from "../audit/index.js";
import { pluginRegistry } from "../adapters/plugin.js";

export interface SubmitTaskParams {
  description: string;
  workflowType: WorkflowType;
  documentIds?: string[];
  clientNumber?: string;
  matterNumber?: string;
  jurisdiction?: string;
}

export interface KnowledgeSearchOpts {
  topK?: number;
  jurisdiction?: string;
  documentType?: string;
}

export interface MemoryQueryOpts {
  taskId: string;
  agentId?: string;
  topK?: number;
}

export interface TimeEntryFilter {
  profileId?: string;
  taskId?: string;
  matterNumber?: string;
  from?: string;
  to?: string;
}

/**
 * The exact surface the MCP tools need. Deliberately narrow — this is the MCP
 * contract, not the whole REST API. Web-only routes (auth, profiles, clients,
 * settings, cost, SSE) stay on the REST side and are not part of this seam.
 */
export interface LegalBackend {
  submitTask(p: SubmitTaskParams): Promise<Task>;
  getTask(taskId: string): Promise<Task | null>;
  listTasks(): Promise<Task[]>;
  approveGate(taskId: string, gateId: string, note?: string): Promise<void>;
  rejectGate(taskId: string, gateId: string, reason: string): Promise<void>;
  ingestDocument(doc: Record<string, unknown>): Promise<{ id: string }>;
  searchKnowledge(query: string, opts: KnowledgeSearchOpts): Promise<unknown>;
  listAgents(opts: { tier?: 0 | 1 | 2 | 3; topK?: number }): Promise<unknown>;
  queryMemory(query: string, opts: MemoryQueryOpts): Promise<unknown>;
  listTemplates(): Promise<TaskTemplate[]>;
  listPlugins(): Promise<unknown>;
  submitFromTemplate(
    templateId: string,
    substitutions?: Record<string, string>,
    documentIds?: string[],
  ): Promise<Task>;
  getAudit(taskId?: string, limit?: number): Promise<unknown>;
  listTimeEntries(filter: TimeEntryFilter): Promise<unknown>;
}

// ─── LocalBackend ─── the process that owns the DB ────────────────────────────

export class LocalBackend implements LegalBackend {
  constructor(private readonly orch: Orchestrator) {}

  async submitTask(p: SubmitTaskParams): Promise<Task> {
    return this.orch.submitTask(p);
  }
  async getTask(taskId: string): Promise<Task | null> {
    return this.orch.getTask(taskId);
  }
  async listTasks(): Promise<Task[]> {
    return this.orch.listTasks();
  }
  async approveGate(taskId: string, gateId: string, note?: string): Promise<void> {
    this.orch.approveGate(taskId, gateId, note);
  }
  async rejectGate(taskId: string, gateId: string, reason: string): Promise<void> {
    this.orch.rejectGate(taskId, gateId, reason);
  }
  async ingestDocument(doc: Record<string, unknown>): Promise<{ id: string }> {
    return { id: await this.orch.knowledge.ingest(doc as Parameters<Orchestrator["knowledge"]["ingest"]>[0]) };
  }
  async searchKnowledge(query: string, opts: KnowledgeSearchOpts): Promise<unknown> {
    return this.orch.knowledge.search(query, opts);
  }
  async listAgents(opts: { tier?: 0 | 1 | 2 | 3; topK?: number }): Promise<unknown> {
    return this.orch.registry.search("", { tier: opts.tier, topK: opts.topK ?? 100 });
  }
  async queryMemory(query: string, opts: MemoryQueryOpts): Promise<unknown> {
    return this.orch.memory.query(query, opts);
  }
  async listTemplates(): Promise<TaskTemplate[]> {
    return this.orch.listTemplates();
  }
  async listPlugins(): Promise<unknown> {
    return pluginRegistry.list();
  }
  async submitFromTemplate(
    templateId: string,
    substitutions?: Record<string, string>,
    documentIds?: string[],
  ): Promise<Task> {
    return this.orch.submitFromTemplate(templateId, substitutions, documentIds);
  }
  async getAudit(taskId?: string, limit?: number): Promise<unknown> {
    const all = auditLogger.readRecent(taskId, limit);
    // MCP over stdio runs as the LOCAL_PARTNER — sees everything. Filtering by
    // visible task ids keeps the access intent explicit and matches REST /audit.
    const visibleIds = new Set(this.orch.listTasks().map((t) => t.id));
    return all.filter((e) => !e.taskId || visibleIds.has(e.taskId));
  }
  async listTimeEntries(filter: TimeEntryFilter): Promise<unknown> {
    return this.orch.time.list({
      profileId: filter.profileId,
      taskId: filter.taskId,
      matterNumber: filter.matterNumber,
      from: filter.from ? new Date(filter.from) : undefined,
      to: filter.to ? new Date(filter.to) : undefined,
    });
  }
}

// ─── RemoteBackend ─── a thin client to the owner's REST API ──────────────────

class HttpError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

export class RemoteBackend implements LegalBackend {
  constructor(
    private readonly baseUrl: string,
    private readonly apiKey?: string,
  ) {}

  private async req(method: string, path: string, body?: unknown): Promise<unknown> {
    const headers: Record<string, string> = { "content-type": "application/json" };
    if (this.apiKey) headers["x-api-key"] = this.apiKey;
    const res = await fetch(new URL(path, this.baseUrl), {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    const text = await res.text();
    const data = text ? safeJson(text) : null;
    if (!res.ok) {
      const msg = (data as { error?: string } | null)?.error ?? `${method} ${path} → HTTP ${res.status}`;
      throw new HttpError(msg, res.status);
    }
    return data;
  }

  private qs(params: Record<string, string | number | undefined>): string {
    const sp = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") sp.set(k, String(v));
    }
    const s = sp.toString();
    return s ? `?${s}` : "";
  }

  async submitTask(p: SubmitTaskParams): Promise<Task> {
    return this.req("POST", "/tasks", p) as Promise<Task>;
  }
  async getTask(taskId: string): Promise<Task | null> {
    try {
      return (await this.req("GET", `/tasks/${encodeURIComponent(taskId)}`)) as Task;
    } catch (err) {
      if (err instanceof HttpError && err.status === 404) return null;
      throw err;
    }
  }
  async listTasks(): Promise<Task[]> {
    return this.req("GET", "/tasks") as Promise<Task[]>;
  }
  async approveGate(taskId: string, gateId: string, note?: string): Promise<void> {
    await this.req(
      "POST",
      `/tasks/${encodeURIComponent(taskId)}/gates/${encodeURIComponent(gateId)}/approve`,
      { note },
    );
  }
  async rejectGate(taskId: string, gateId: string, reason: string): Promise<void> {
    await this.req(
      "POST",
      `/tasks/${encodeURIComponent(taskId)}/gates/${encodeURIComponent(gateId)}/reject`,
      { reason },
    );
  }
  async ingestDocument(doc: Record<string, unknown>): Promise<{ id: string }> {
    const data = (await this.req("POST", "/documents", doc)) as { id: string };
    return { id: data.id };
  }
  async searchKnowledge(query: string, opts: KnowledgeSearchOpts): Promise<unknown> {
    return this.req(
      "GET",
      `/documents/search${this.qs({ query, topK: opts.topK, jurisdiction: opts.jurisdiction, documentType: opts.documentType })}`,
    );
  }
  async listAgents(opts: { tier?: 0 | 1 | 2 | 3; topK?: number }): Promise<unknown> {
    return this.req("GET", `/agents${this.qs({ tier: opts.tier })}`);
  }
  async queryMemory(query: string, opts: MemoryQueryOpts): Promise<unknown> {
    return this.req("POST", "/memory/query", { query, ...opts });
  }
  async listTemplates(): Promise<TaskTemplate[]> {
    return this.req("GET", "/templates") as Promise<TaskTemplate[]>;
  }
  async listPlugins(): Promise<unknown> {
    return this.req("GET", "/plugins");
  }
  async submitFromTemplate(
    templateId: string,
    substitutions?: Record<string, string>,
    documentIds?: string[],
  ): Promise<Task> {
    return this.req("POST", "/tasks/from-template", { templateId, substitutions, documentIds }) as Promise<Task>;
  }
  async getAudit(taskId?: string, limit?: number): Promise<unknown> {
    return this.req("GET", `/audit${this.qs({ taskId, limit })}`);
  }
  async listTimeEntries(filter: TimeEntryFilter): Promise<unknown> {
    return this.req("GET", `/time-entries${this.qs({ ...filter })}`);
  }
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

/**
 * Best-effort probe: is an owner backend already serving on `baseUrl`?
 * Used by auto mode so a second process attaches as a client instead of
 * fighting over the DB lock. Returns false on any network/timeout error.
 */
export async function probeBackend(baseUrl: string, timeoutMs = 1500): Promise<boolean> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(new URL("/health", baseUrl), { signal: ctrl.signal });
    return res.ok;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}
