// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * MCP Server — exposes the full orchestration system over the Model Context Protocol.
 *
 * Any MCP-compatible client (Claude Desktop, Mike OSS, Laverne, custom) can connect
 * and invoke these tools. This is the primary integration surface.
 *
 * Also starts a Fastify REST API on a separate port for web frontends that
 * prefer HTTP over stdio MCP.
 */

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import Fastify from "fastify";
import type { FastifyRequest } from "fastify";
import cors from "@fastify/cors";
import cookie from "@fastify/cookie";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { auditLogger } from "../audit/index.js";
import { Orchestrator } from "../orchestrator.js";
import type { WorkflowType, SessionUser } from "../types.js";
import { LOCAL_PARTNER, filterVisible, canViewTask, isPartner } from "../auth/index.js";
import { registerAuthRoutes, readSessionCookie } from "../auth/oauth.js";

// ─── Tool schemas ─────────────────────────────────────────────────────────────

const TOOLS = [
  {
    name: "submit_task",
    description: "Submit a new legal task for multi-agent processing.",
    inputSchema: {
      type: "object",
      properties: {
        description: { type: "string", description: "Full description of the legal task" },
        workflowType: {
          type: "string",
          enum: ["counsel", "roundtable", "adversarial", "review", "tabulate", "full_bench"],
          description: "Orchestration workflow to use",
        },
        documentIds: {
          type: "array",
          items: { type: "string" },
          description: "IDs of documents already ingested into the knowledge store",
        },
        clientNumber: { type: "string", description: "Optional law-firm client number" },
        matterNumber: { type: "string", description: "Optional law-firm matter number" },
      },
      required: ["description", "workflowType"],
    },
  },
  {
    name: "get_task",
    description: "Get the current state of a task including findings, round history, and pending gates.",
    inputSchema: {
      type: "object",
      properties: { taskId: { type: "string" } },
      required: ["taskId"],
    },
  },
  {
    name: "list_tasks",
    description: "List all tasks and their current statuses.",
    inputSchema: { type: "object", properties: {} },
  },
  {
    name: "approve_gate",
    description: "Approve a human review gate, allowing the finding to proceed to final output.",
    inputSchema: {
      type: "object",
      properties: {
        taskId: { type: "string" },
        gateId: { type: "string" },
        note: { type: "string", description: "Optional reviewer note" },
      },
      required: ["taskId", "gateId"],
    },
  },
  {
    name: "reject_gate",
    description: "Reject a human review gate, removing the finding from the output.",
    inputSchema: {
      type: "object",
      properties: {
        taskId: { type: "string" },
        gateId: { type: "string" },
        reason: { type: "string" },
      },
      required: ["taskId", "gateId", "reason"],
    },
  },
  {
    name: "ingest_document",
    description: "Ingest a document into the knowledge store for use in tasks.",
    inputSchema: {
      type: "object",
      properties: {
        title: { type: "string" },
        content: { type: "string" },
        source: { type: "string" },
        jurisdiction: { type: "string" },
        documentType: { type: "string" },
      },
      required: ["title", "content"],
    },
  },
  {
    name: "search_knowledge",
    description: "Semantic search across the document knowledge store.",
    inputSchema: {
      type: "object",
      properties: {
        query: { type: "string" },
        topK: { type: "number" },
        jurisdiction: { type: "string" },
        documentType: { type: "string" },
      },
      required: ["query"],
    },
  },
  {
    name: "list_agents",
    description: "List all agents in the registry with their tier, domain, and capabilities.",
    inputSchema: {
      type: "object",
      properties: {
        tier: { type: "number", enum: [0, 1, 2, 3] },
        domain: { type: "string" },
      },
    },
  },
  {
    name: "query_memory",
    description: "Query inter-round memory for a task.",
    inputSchema: {
      type: "object",
      properties: {
        query: { type: "string" },
        taskId: { type: "string" },
        agentId: { type: "string" },
        topK: { type: "number" },
      },
      required: ["query", "taskId"],
    },
  },
  {
    name: "list_templates",
    description: "List available TaskTemplates (pre-built workflow presets).",
    inputSchema: { type: "object", properties: {} },
  },
  {
    name: "submit_from_template",
    description: "Instantiate a TaskTemplate and submit it as a new task.",
    inputSchema: {
      type: "object",
      properties: {
        templateId: { type: "string", description: "Template ID from list_templates" },
        substitutions: {
          type: "object",
          description: "Key-value pairs to substitute {{placeholders}} in the template",
        },
        documentIds: { type: "array", items: { type: "string" } },
      },
      required: ["templateId"],
    },
  },
  {
    name: "get_round",
    description: "Get the full RoundState for a specific round of a task.",
    inputSchema: {
      type: "object",
      properties: {
        taskId: { type: "string" },
        round: { type: "number", description: "Round number (1-based)" },
      },
      required: ["taskId", "round"],
    },
  },
  {
    name: "get_audit",
    description: "Retrieve recent audit log entries. Filter by taskId and/or limit results.",
    inputSchema: {
      type: "object",
      properties: {
        taskId: { type: "string", description: "Optional: filter to a specific task" },
        limit: { type: "number", description: "Maximum entries to return (default 200)" },
      },
    },
  },
] as const;

// ─── MCP server ───────────────────────────────────────────────────────────────

export async function startMcpServer(orchestrator: Orchestrator): Promise<void> {
  const server = new Server(
    { name: "big-michael", version: "0.1.0" },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: TOOLS }));

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;
    try {
      const result = await handleTool(name, args ?? {}, orchestrator);
      return {
        content: [{ type: "text", text: JSON.stringify(result, null, 2) }],
      };
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      logger.error("MCP tool error", { tool: name, error: message });
      return {
        isError: true,
        content: [{ type: "text", text: `Error: ${message}` }],
      };
    }
  });

  const transport = new StdioServerTransport();
  await server.connect(transport);
  logger.info("MCP server started (stdio)");
}

// ─── REST API (Fastify) ───────────────────────────────────────────────────────

export async function startRestApi(orchestrator: Orchestrator): Promise<void> {
  // bodyLimit bounds request bodies (document ingestion can be large but must
  // not be unbounded). x-powered-by/server header is off by default in Fastify.
  const app = Fastify({ logger: false, bodyLimit: 25 * 1024 * 1024 });

  // CORS — restrict the browser origins allowed to call the API. Local Vite
  // proxies same-origin so this mainly governs cross-origin/deployed UIs.
  // credentials:true so session cookies ride along once OAuth is live.
  await app.register(cors, { origin: Config.auth.allowedOrigins, credentials: true });
  await app.register(cookie, { secret: Config.auth.sessionSecret });
  registerAuthRoutes(app, orchestrator);

  // Resolve the principal for a request. Auth OFF (local) → the LOCAL_PARTNER
  // who sees everything. Auth ON → the signed session cookie from OAuth login.
  const getUser = (req: FastifyRequest): SessionUser | null => {
    if (!Config.auth.enabled) return LOCAL_PARTNER;
    return readSessionCookie(req);
  };

  // Optional shared-secret auth. When API_KEY is set, every request except the
  // health check must present it as `x-api-key`. This is defence-in-depth for
  // anyone who runs the API off loopback; on 127.0.0.1 it can be left unset.
  if (Config.api.apiKey) {
    app.addHook("onRequest", async (req, reply) => {
      if (req.url === "/health") return;
      if (req.headers["x-api-key"] !== Config.api.apiKey) {
        return reply.code(401).send({ error: "Unauthorized" });
      }
    });
  }

  // When auth is enabled, require an authenticated principal on everything
  // except health + the OAuth login/callback routes. (OAuth routes land next.)
  if (Config.auth.enabled) {
    app.addHook("onRequest", async (req, reply) => {
      if (req.url === "/health" || req.url.startsWith("/auth/")) return;
      if (!getUser(req)) return reply.code(401).send({ error: "Authentication required" });
    });
  }

  app.post("/tasks", async (req, reply) => {
    const body = req.body as {
      description: string; workflowType: WorkflowType; documentIds?: string[];
      clientNumber?: string; matterNumber?: string;
    };
    const task = await orchestrator.submitTask(body);
    // The creator is assigned to their own matter so they can see it under the
    // access rule. Partners see everything regardless.
    const user = getUser(req);
    if (user) orchestrator.assignLawyers(task.id, [user.profileId]);
    return reply.status(201).send(orchestrator.getTask(task.id) ?? task);
  });

  // Only matters the principal may see (partner → all; lawyer → assigned only).
  app.get("/tasks", async (req) => filterVisible(getUser(req), orchestrator.listTasks()));

  app.get("/tasks/:id", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task) return reply.status(404).send({ error: "Task not found" });
    // Don't reveal existence of matters the principal can't see.
    if (!canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    return task;
  });

  // Delete a matter (must be visible to the principal).
  app.delete("/tasks/:id", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    orchestrator.deleteTask(id);
    return reply.status(200).send({ ok: true });
  });

  // Assign lawyer(s) to a matter — partner only (controls cross-lawyer sharing).
  app.post("/tasks/:id/assign", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const { lawyerIds } = req.body as { lawyerIds: string[] };
    const task = orchestrator.assignLawyers(id, Array.isArray(lawyerIds) ? lawyerIds : []);
    if (!task) return reply.status(404).send({ error: "Task not found" });
    return task;
  });

  // Structured tabulate output as downloadable CSV
  app.get("/tasks/:id/table.csv", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task) return reply.status(404).send({ error: "Task not found" });
    if (!task.table) return reply.status(404).send({ error: "No table available for this task" });

    const esc = (v: string) => `"${String(v).replace(/"/g, '""')}"`;
    const { columns, rows } = task.table;
    const hasConf = rows.some((r) => r._confidence);
    const outCols = hasConf ? [...columns, "Confidence", "Sources"] : columns;
    const cellFor = (r: Record<string, string>, c: string) =>
      c === "Confidence" ? (r._confidence ?? "") : c === "Sources" ? (r._sources ?? "") : (r[c] ?? "");
    const lines = [
      outCols.map(esc).join(","),
      ...rows.map((r) => outCols.map((c) => esc(cellFor(r, c))).join(",")),
    ];

    reply.header("Content-Type", "text/csv; charset=utf-8");
    reply.header("Content-Disposition", `attachment; filename="big-michael-${id}.csv"`);
    return lines.join("\r\n");
  });

  app.post("/tasks/:taskId/gates/:gateId/approve", async (req, reply) => {
    const { taskId, gateId } = req.params as { taskId: string; gateId: string };
    const { note } = (req.body ?? {}) as { note?: string };
    orchestrator.approveGate(taskId, gateId, note);
    return reply.status(200).send({ ok: true });
  });

  app.post("/tasks/:taskId/gates/:gateId/reject", async (req, reply) => {
    const { taskId, gateId } = req.params as { taskId: string; gateId: string };
    const { reason } = (req.body ?? {}) as { reason: string };
    orchestrator.rejectGate(taskId, gateId, reason);
    return reply.status(200).send({ ok: true });
  });

  app.post("/documents", async (req, reply) => {
    const body = req.body as { title: string; content: string; source?: string; jurisdiction?: string; documentType?: string };
    const docId = await orchestrator.knowledge.ingest(body);
    return reply.status(201).send({ id: docId });
  });

  app.get("/documents", async () => orchestrator.knowledge.listDocuments());

  app.get("/documents/search", async (req) => {
    const { query, topK, jurisdiction, documentType } = req.query as Record<string, string>;
    return orchestrator.knowledge.search(query, {
      topK: topK ? parseInt(topK) : undefined,
      jurisdiction,
      documentType,
    });
  });

  app.get("/agents", async (req) => {
    const { tier, domain } = req.query as Record<string, string>;
    if (tier || domain) {
      return orchestrator.registry.search("", {
        tier: tier ? parseInt(tier) as 0 | 1 | 2 | 3 : undefined,
        topK: 100,
      });
    }
    return orchestrator.registry.listAll();
  });

  // T17: SSE streaming endpoint
  app.get("/tasks/:id/stream", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task) return reply.status(404).send({ error: "Task not found" });

    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();

    const send = (type: string, data: unknown) => {
      reply.raw.write(`event: ${type}\ndata: ${JSON.stringify(data)}\n\n`);
    };

    // Send current snapshot immediately
    send("snapshot", task);

    const handler = ({ type, data }: { type: string; data: unknown }) => {
      send(type, data);
      if (type === "complete" || type === "failed") {
        reply.raw.end();
        orchestrator.progressEmitter.off(`task:${id}`, handler);
      }
    };

    orchestrator.progressEmitter.on(`task:${id}`, handler);
    req.raw.on("close", () => {
      orchestrator.progressEmitter.off(`task:${id}`, handler);
    });

    return reply;
  });

  // T18: Template REST routes
  app.get("/templates", async () => orchestrator.listTemplates());

  // ── Identity + lawyer profiles ──────────────────────────────────────────────
  app.get("/me", async (req) => {
    const user = getUser(req);
    return { user, authEnabled: Config.auth.enabled };
  });

  app.get("/profiles", async () => orchestrator.profiles.list());

  app.post("/profiles", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    try {
      return reply.status(201).send(await orchestrator.profiles.create(req.body as { name: string; email: string; role?: string; title?: string; color?: string }));
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.patch("/profiles/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      return await orchestrator.profiles.update(id, req.body as Record<string, string>);
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  app.delete("/profiles/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const ok = await orchestrator.profiles.remove(id);
      return ok ? reply.status(200).send({ ok: true }) : reply.status(404).send({ error: "Profile not found" });
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  // ── Admin settings (presentation mode, DyTopo depth, debate, DocuSeal) ──────
  app.get("/settings", async () => orchestrator.settings.get());
  app.put("/settings", async (req, reply) => {
    try {
      const updated = await orchestrator.settings.update(req.body as Record<string, unknown>);
      return updated;
    } catch (err) {
      return reply.code(400).send({ error: (err as Error).message });
    }
  });

  app.post("/tasks/from-template", async (req, reply) => {
    const body = req.body as {
      templateId: string; substitutions?: Record<string, string>; documentIds?: string[];
      clientNumber?: string; matterNumber?: string;
    };
    const task = await orchestrator.submitFromTemplate(body.templateId, body.substitutions, body.documentIds,
      { clientNumber: body.clientNumber, matterNumber: body.matterNumber });
    return reply.status(201).send(task);
  });

  // T19: get_round REST route
  app.get("/tasks/:taskId/rounds/:round", async (req, reply) => {
    const { taskId, round } = req.params as { taskId: string; round: string };
    const task = orchestrator.getTask(taskId);
    if (!task) return reply.status(404).send({ error: "Task not found" });
    const roundState = task.rounds[parseInt(round) - 1];
    if (!roundState) return reply.status(404).send({ error: "Round not found" });
    return roundState;
  });

  // Health check
  app.get("/health", async () => {
    const tasks = orchestrator.listTasks();
    return {
      status: "ok",
      version: "0.1.0",
      uptime: Math.floor(process.uptime()),
      tasks: {
        total: tasks.length,
        running: tasks.filter((t) => t.status === "running").length,
        awaiting_gate: tasks.filter((t) => t.status === "awaiting_gate").length,
        complete: tasks.filter((t) => t.status === "complete").length,
      },
    };
  });

  // Audit REST routes
  app.get("/audit", async (req) => {
    const { taskId, limit } = req.query as Record<string, string>;
    return auditLogger.readRecent(taskId, limit ? parseInt(limit) : undefined);
  });

  // Live audit SSE stream
  app.get("/audit/stream", async (req, reply) => {
    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();

    const send = (entry: unknown) => {
      reply.raw.write(`data: ${JSON.stringify(entry)}\n\n`);
    };

    // Replay recent entries so a new subscriber catches up
    const recent = auditLogger.readRecent(undefined, 100);
    for (const e of recent) send(e);

    const unsub = auditLogger.subscribe(send);
    req.raw.on("close", unsub);
    return reply;
  });

  await app.listen({ port: Config.api.port, host: Config.api.host });
  logger.info("REST API started", { port: Config.api.port, host: Config.api.host, auth: Config.api.apiKey ? "x-api-key" : "none" });
}

// ─── Tool handler ─────────────────────────────────────────────────────────────

async function handleTool(
  name: string,
  args: Record<string, unknown>,
  orch: Orchestrator,
): Promise<unknown> {
  switch (name) {
    case "submit_task":
      return orch.submitTask({
        description: args.description as string,
        workflowType: args.workflowType as WorkflowType,
        documentIds: args.documentIds as string[] | undefined,
        clientNumber: args.clientNumber as string | undefined,
        matterNumber: args.matterNumber as string | undefined,
      });

    case "get_task": {
      const task = orch.getTask(args.taskId as string);
      if (!task) throw new Error(`Task not found: ${args.taskId}`);
      return task;
    }

    case "list_tasks":
      return orch.listTasks();

    case "approve_gate":
      orch.approveGate(args.taskId as string, args.gateId as string, args.note as string | undefined);
      return { ok: true };

    case "reject_gate":
      orch.rejectGate(args.taskId as string, args.gateId as string, args.reason as string);
      return { ok: true };

    case "ingest_document":
      return { id: await orch.knowledge.ingest(args as Parameters<typeof orch.knowledge.ingest>[0]) };

    case "search_knowledge":
      return orch.knowledge.search(args.query as string, {
        topK: args.topK as number | undefined,
        jurisdiction: args.jurisdiction as string | undefined,
        documentType: args.documentType as string | undefined,
      });

    case "list_agents":
      return orch.registry.search("", {
        tier: args.tier as 0 | 1 | 2 | 3 | undefined,
        topK: 100,
      });

    case "query_memory":
      return orch.memory.query(args.query as string, {
        taskId: args.taskId as string,
        agentId: args.agentId as string | undefined,
        topK: args.topK as number | undefined,
      });

    case "list_templates":
      return orch.listTemplates();

    case "submit_from_template":
      return orch.submitFromTemplate(
        args.templateId as string,
        args.substitutions as Record<string, string> | undefined,
        args.documentIds as string[] | undefined,
      );

    case "get_round": {
      const task = orch.getTask(args.taskId as string);
      if (!task) throw new Error(`Task not found: ${args.taskId}`);
      const roundState = task.rounds[(args.round as number) - 1];
      if (!roundState) throw new Error(`Round ${args.round} not found`);
      return roundState;
    }

    case "get_audit":
      return auditLogger.readRecent(
        args.taskId as string | undefined,
        args.limit as number | undefined,
      );

    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}