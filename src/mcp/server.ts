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
import multipart from "@fastify/multipart";
import { mkdir, writeFile, unlink } from "fs/promises";
import { join, extname, basename } from "path";
import { randomUUID } from "crypto";
import { extractTextFromPdf } from "../tools/pdf.js";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { auditLogger } from "../audit/index.js";
import { Orchestrator } from "../orchestrator.js";
import type { WorkflowType, SessionUser } from "../types.js";
import { LOCAL_PARTNER, filterVisible, canViewTask, isPartner } from "../auth/index.js";
import { registerAuthRoutes, readSessionCookie } from "../auth/oauth.js";
import { detectPracticeArea, detectClient } from "../services/classifier.js";

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
  await app.register(multipart, { limits: { fileSize: 25 * 1024 * 1024, files: 1 } });
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
    const user = getUser(req);
    // Partners see all documents; lawyers are scoped to their own documents in agent tools.
    const createdByProfileId = isPartner(user) ? undefined : user?.profileId;
    const task = await orchestrator.submitTask({ ...body, createdByProfileId });
    // The creator is assigned to their own matter so they can see it under the
    // access rule. Partners see everything regardless.
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
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
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
    const task = orchestrator.getTask(taskId);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { note } = (req.body ?? {}) as { note?: string };
    orchestrator.approveGate(taskId, gateId, note);
    return reply.status(200).send({ ok: true });
  });

  app.post("/tasks/:taskId/gates/:gateId/reject", async (req, reply) => {
    const { taskId, gateId } = req.params as { taskId: string; gateId: string };
    const task = orchestrator.getTask(taskId);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { reason } = (req.body ?? {}) as { reason: string };
    orchestrator.rejectGate(taskId, gateId, reason);
    return reply.status(200).send({ ok: true });
  });

  app.post("/documents", async (req, reply) => {
    const body = req.body as { title: string; content: string; source?: string; jurisdiction?: string; documentType?: string; practiceArea?: string };
    const clients = orchestrator.clients.list();
    const [practiceArea, detectedClient] = await Promise.all([
      body.practiceArea ? Promise.resolve(body.practiceArea) : detectPracticeArea(body.title, body.content),
      detectClient(body.title, body.content, clients),
    ]);
    const docId = await orchestrator.knowledge.ingest({
      ...body,
      practiceArea: practiceArea ?? undefined,
      detectedClientNumber: detectedClient?.clientNumber,
      ownerId: getUser(req)?.profileId,
    });
    const suggestedLawyers = practiceArea
      ? orchestrator.profiles.list().filter((p) => p.practiceAreas?.includes(practiceArea))
      : [];
    return reply.status(201).send({
      id: docId,
      practiceArea,
      detectedClient,
      suggestedLawyers: suggestedLawyers.map((p) => ({ id: p.id, name: p.name, email: p.email })),
    });
  });

  // Upload an actual file (PDF or text), extract its text, and ingest it.
  app.post("/documents/upload", async (req, reply) => {
    const data = await (req as unknown as { file: () => Promise<{ filename: string; mimetype: string; toBuffer: () => Promise<Buffer> } | undefined> }).file();
    if (!data) return reply.status(400).send({ error: "No file uploaded" });
    const filename = data.filename || "document";
    const ext = extname(filename).toLowerCase();
    const buf = await data.toBuffer();
    const TEXT_EXT = [".txt", ".md", ".markdown", ".csv", ".json", ".log", ".text", ".rtf"];

    let content = "";
    try {
      if (ext === ".pdf") {
        const dir = join(Config.pdf.outputDir, "uploads");
        await mkdir(dir, { recursive: true });
        const tmp = join(dir, `${randomUUID()}.pdf`);
        await writeFile(tmp, buf);
        try { content = await extractTextFromPdf(tmp); } finally { await unlink(tmp).catch(() => {}); }
      } else if (TEXT_EXT.includes(ext) || (data.mimetype || "").startsWith("text/")) {
        content = buf.toString("utf8");
      } else {
        return reply.status(415).send({ error: `Unsupported file type '${ext || data.mimetype}'. Upload a PDF or text file (or paste the text in the Library).` });
      }
    } catch (err) {
      return reply.status(422).send({ error: `Could not read ${filename}: ${(err as Error).message}` });
    }
    if (!content.trim()) {
      return reply.status(422).send({ error: `No extractable text found in ${filename} (a scanned image PDF needs OCR, which isn't wired to upload yet).` });
    }

    const title = basename(filename, ext);
    const clients = orchestrator.clients.list();
    const [practiceArea, detectedClient] = await Promise.all([
      detectPracticeArea(title, content),
      detectClient(title, content, clients),
    ]);
    const id = await orchestrator.knowledge.ingest({
      title, content, documentType: ext.replace(".", "") || undefined, source: "upload",
      ownerId: getUser(req)?.profileId,
      practiceArea: practiceArea ?? undefined,
      detectedClientNumber: detectedClient?.clientNumber,
    });
    const suggestedLawyers = practiceArea
      ? orchestrator.profiles.list().filter((p) => p.practiceAreas?.includes(practiceArea))
      : [];
    return reply.status(201).send({
      id, title, practiceArea, detectedClient,
      suggestedLawyers: suggestedLawyers.map((p) => ({ id: p.id, name: p.name, email: p.email })),
    });
  });

  // A lawyer sees only documents they uploaded; partners see the whole library.
  const docOwnerScope = (req: FastifyRequest) => (isPartner(getUser(req)) ? undefined : getUser(req)?.profileId);

  app.get("/documents", async (req) => orchestrator.knowledge.listDocuments(docOwnerScope(req)));

  app.get("/documents/search", async (req) => {
    const { query, topK, jurisdiction, documentType } = req.query as Record<string, string>;
    const topKNum = topK ? parseInt(topK, 10) : undefined;
    return orchestrator.knowledge.search(query, {
      topK: topKNum && Number.isInteger(topKNum) && topKNum > 0 ? topKNum : undefined,
      jurisdiction,
      documentType,
      ownerId: docOwnerScope(req),
    });
  });

  app.get("/agents", async (req) => {
    const { tier } = req.query as Record<string, string>;
    if (tier) {
      const tierNum = parseInt(tier, 10);
      const validTier = ([0, 1, 2, 3] as const).find((t) => t === tierNum);
      return orchestrator.registry.search("", { tier: validTier, topK: 100 });
    }
    return orchestrator.registry.listAll();
  });

  // T17: SSE streaming endpoint
  const MAX_SSE_LISTENERS_PER_TASK = 50;
  app.get("/tasks/:id/stream", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });

    if (orchestrator.progressEmitter.listenerCount(`task:${id}`) >= MAX_SSE_LISTENERS_PER_TASK) {
      return reply.status(429).send({ error: "Too many concurrent streams for this task" });
    }

    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();

    const send = (type: string, data: unknown) => {
      const safeType = type.replace(/[\r\n]/g, " ");
      reply.raw.write(`event: ${safeType}\ndata: ${JSON.stringify(data)}\n\n`);
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

  // Partners see full profiles (including email for contact/admin purposes).
  // Lawyers see only the display fields needed to render the roster UI.
  app.get("/profiles", async (req) => {
    const profiles = orchestrator.profiles.list();
    if (isPartner(getUser(req))) return profiles;
    return profiles.map(({ id, name, title, color, role }) => ({ id, name, title, color, role }));
  });

  app.get("/profiles/:id", async (req, reply) => {
    const { id } = req.params as { id: string };
    const profile = orchestrator.profiles.get(id);
    if (!profile) return reply.status(404).send({ error: "Profile not found" });
    return profile;
  });

  app.post("/profiles", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    try {
      return reply.status(201).send(await orchestrator.profiles.create(req.body as {
        name: string; email: string; role?: string; title?: string; color?: string;
        practiceAreas?: string[]; bio?: string;
      }));
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  // Partners can update any profile; lawyers can only update their own (but not change role).
  app.patch("/profiles/:id", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    const patch = req.body as Record<string, unknown>;
    if (!isPartner(user) && user?.profileId !== id) {
      return reply.status(403).send({ error: "You can only edit your own profile" });
    }
    // Non-partners cannot change role.
    if (!isPartner(user) && patch.role) {
      return reply.status(403).send({ error: "Partner role required to change role" });
    }
    try {
      return await orchestrator.profiles.update(id, patch as Record<string, never>);
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

  // ── Clients ─────────────────────────────────────────────────────────────────
  app.get("/clients", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    return orchestrator.clients.list();
  });

  app.post("/clients", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    try {
      const body = req.body as { name: string; clientNumber: string; adversaries?: string[]; notes?: string };
      const conflict = orchestrator.clients.checkConflict(body.name);
      const client = await orchestrator.clients.create(body);
      return reply.status(201).send({ ...client, conflict });
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.patch("/clients/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      return await orchestrator.clients.update(id, req.body as Record<string, never>);
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  app.delete("/clients/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const ok = await orchestrator.clients.remove(id);
      return ok ? reply.status(200).send({ ok: true }) : reply.status(404).send({ error: "Client not found" });
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.post("/clients/:id/matters", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const body = req.body as { matterNumber: string; description: string; practiceArea?: string };
      const matter = await orchestrator.clients.addMatter(id, body);
      return reply.status(201).send(matter);
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.delete("/clients/:id/matters/:matterNumber", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id, matterNumber } = req.params as { id: string; matterNumber: string };
    try {
      const ok = await orchestrator.clients.removeMatter(id, matterNumber);
      return ok ? reply.status(200).send({ ok: true }) : reply.status(404).send({ error: "Matter not found" });
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.post("/clients/check-conflict", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { name } = req.body as { name: string };
    return orchestrator.clients.checkConflict(name);
  });

  // ── Admin settings (presentation mode, DyTopo depth, debate, DocuSeal) ──────
  // Both GET and PUT are partner-only: GET exposes the DocuSeal URL and
  // enabled state; PUT can redirect DocuSeal requests (SSRF) or weaken
  // debate/gate settings.
  app.get("/settings", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    return orchestrator.settings.get();
  });
  app.put("/settings", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
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
    const user = getUser(req);
    const createdByProfileId = isPartner(user) ? undefined : user?.profileId;
    const task = await orchestrator.submitFromTemplate(body.templateId, body.substitutions, body.documentIds,
      { clientNumber: body.clientNumber, matterNumber: body.matterNumber, createdByProfileId });
    if (user) orchestrator.assignLawyers(task.id, [user.profileId]);
    return reply.status(201).send(orchestrator.getTask(task.id) ?? task);
  });

  // T19: get_round REST route
  app.get("/tasks/:taskId/rounds/:round", async (req, reply) => {
    const { taskId, round } = req.params as { taskId: string; round: string };
    const roundNum = parseInt(round, 10);
    if (!Number.isInteger(roundNum) || roundNum < 1) return reply.status(400).send({ error: "Invalid round number" });
    const task = orchestrator.getTask(taskId);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    const roundState = task.rounds[roundNum - 1];
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

  // The audit log spans every matter, so it is filtered to the matters the
  // principal may see. Partners see all (incl. system events with no taskId);
  // a lawyer sees only audit entries for their own matters.
  const auditVisible = (req: FastifyRequest) => {
    const user = getUser(req);
    if (isPartner(user)) return () => true;
    const ids = new Set(filterVisible(user, orchestrator.listTasks()).map((t) => t.id));
    return (e: { taskId?: string }) => !!e.taskId && ids.has(e.taskId);
  };

  // Audit REST routes
  app.get("/audit", async (req) => {
    const { taskId, limit } = req.query as Record<string, string>;
    const visible = auditVisible(req);
    const limitNum = limit ? parseInt(limit, 10) : undefined;
    return auditLogger.readRecent(taskId, limitNum && Number.isInteger(limitNum) && limitNum > 0 ? limitNum : undefined).filter(visible);
  });

  // Live audit SSE stream
  const MAX_AUDIT_SSE_LISTENERS = 50;
  app.get("/audit/stream", async (req, reply) => {
    if (auditLogger.listenerCount() >= MAX_AUDIT_SSE_LISTENERS) {
      return reply.status(429).send({ error: "Too many concurrent audit streams" });
    }
    const visible = auditVisible(req);
    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();

    const send = (entry: { taskId?: string }) => {
      if (visible(entry)) reply.raw.write(`data: ${JSON.stringify(entry)}\n\n`);
    };

    // Replay recent entries so a new subscriber catches up
    const recent = auditLogger.readRecent(undefined, 100);
    for (const e of recent) send(e);

    const unsub = auditLogger.subscribe(send as (e: unknown) => void);
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

    case "get_audit": {
      // MCP runs over stdio as the LOCAL_PARTNER (full partner access).
      // Apply the same visibility filter as the REST endpoint so the pattern
      // is explicit and safe if the transport is ever changed.
      const allEntries = auditLogger.readRecent(
        args.taskId as string | undefined,
        args.limit as number | undefined,
      );
      // LOCAL_PARTNER is a partner — sees every entry. Filter is a no-op but
      // makes the access intent explicit and consistent with the REST audit route.
      const visibleIds = new Set(orch.listTasks().map((t) => t.id));
      return allEntries.filter((e) => !e.taskId || visibleIds.has(e.taskId));
    }

    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}