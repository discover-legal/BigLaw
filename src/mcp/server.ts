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
import { randomUUID, timingSafeEqual } from "crypto";
import { extractTextFromPdf } from "../tools/pdf.js";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { auditLogger } from "../audit/index.js";
import { Orchestrator } from "../orchestrator.js";
import type { LegalBackend } from "../backend/index.js";
import type { WorkflowType, SessionUser } from "../types.js";
import { MODE_COLORS, MODE_CAPABILITIES } from "../types.js";
import { LOCAL_PARTNER, filterVisible, canViewTask, isPartner, resolveMode } from "../auth/index.js";
import { registerAuthRoutes, readSessionCookie } from "../auth/oauth.js";
import { detectPracticeArea, detectClient } from "../services/classifier.js";
import { analyzeTone } from "../services/toneAnalyzer.js";
import { parseLinkedInExport } from "../linkedin/parser.js";
import { extractWritingSamples } from "../services/writingSamples.js";
import { pluginRegistry } from "../adapters/plugin.js";
import { costStore } from "../cost/index.js";
import { clioClient } from "../integrations/clio.js";

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
          enum: ["counsel", "roundtable", "adversarial", "review", "tabulate", "full_bench", "legal_design", "pre_engagement"],
          description: "Orchestration workflow to use",
        },
        documentIds: {
          type: "array",
          items: { type: "string" },
          description: "IDs of documents already ingested into the knowledge store",
        },
        clientNumber: { type: "string", description: "Optional law-firm client number" },
        matterNumber: { type: "string", description: "Optional law-firm matter number" },
        jurisdiction: {
          type: "string",
          description: "Governing jurisdiction (e.g. 'US', 'US-NY', 'EU', 'UK', 'AU', 'SG'). Filters out jurisdiction-incompatible agents.",
        },
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
    name: "list_plugins",
    description: "List all loaded external plugins (JSON drop-ins and TypeScript adapters), including their contributed tools, agents, and workflow templates.",
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
  {
    name: "get_time_entries",
    description: "Retrieve time entries for billing. Lawyers see their own; partners see all. Supports filtering by profileId, taskId, matterNumber.",
    inputSchema: {
      type: "object",
      properties: {
        profileId: { type: "string" },
        taskId: { type: "string" },
        matterNumber: { type: "string" },
        from: { type: "string", description: "ISO date string" },
        to: { type: "string", description: "ISO date string" },
      },
    },
  },
] as const;

// ─── MCP server ───────────────────────────────────────────────────────────────

export async function startMcpServer(backend: LegalBackend): Promise<void> {
  const server = new Server(
    { name: "big-michael", version: "0.1.0" },
    { capabilities: { tools: {} } },
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: TOOLS }));

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;
    try {
      const result = await handleTool(name, args ?? {}, backend);
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

  // Security headers on every response — API only (no HTML), so CSP is strict.
  app.addHook("onSend", (_req, reply, _payload, done) => {
    reply.header("X-Content-Type-Options", "nosniff");
    reply.header("X-Frame-Options", "DENY");
    reply.header("Referrer-Policy", "strict-origin-when-cross-origin");
    reply.header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'");
    if (Config.auth.baseUrl.startsWith("https://")) {
      reply.header("Strict-Transport-Security", "max-age=63072000; includeSubDomains");
    }
    done();
  });

  // Load persisted Clio tokens (no-op if file absent or not configured).
  if (Config.clio.enabled) await clioClient.load();

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
      // Constant-time comparison prevents timing side-channel key enumeration.
      // Pad the provided key to the expected length before comparing so that
      // timingSafeEqual always runs (otherwise a wrong-length key short-circuits
      // the comparison and leaks the expected key length via timing).
      const expected = Buffer.from(Config.api.apiKey);
      const raw = Buffer.from(String(req.headers["x-api-key"] ?? ""));
      const padded = Buffer.alloc(expected.length);
      raw.copy(padded, 0, 0, Math.min(raw.length, expected.length));
      const validLength = raw.length === expected.length;
      const validContent = timingSafeEqual(padded, expected);
      if (!validLength || !validContent) {
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
      clientNumber?: string; matterNumber?: string; jurisdiction?: string;
    };
    // Cap documentIds to 100 entries to prevent memory exhaustion from massive arrays.
    if (Array.isArray(body.documentIds) && body.documentIds.length > 100) {
      return reply.status(400).send({ error: "documentIds exceeds the limit of 100 entries" });
    }
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
  // Capped at 500 to prevent a single response from dumping the full task list.
  app.get("/tasks", async (req) => filterVisible(getUser(req), orchestrator.listTasks()).slice(0, 500));

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
    orchestrator.approveGate(taskId, gateId, note, getUser(req)?.profileId);
    return reply.status(200).send({ ok: true });
  });

  app.post("/tasks/:taskId/gates/:gateId/reject", async (req, reply) => {
    const { taskId, gateId } = req.params as { taskId: string; gateId: string };
    const task = orchestrator.getTask(taskId);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { reason } = (req.body ?? {}) as { reason: string };
    orchestrator.rejectGate(taskId, gateId, reason, getUser(req)?.profileId);
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
      topK: topKNum && Number.isInteger(topKNum) && topKNum > 0 ? Math.min(topKNum, 50) : undefined,
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

  // Inter-round memory query — mirrors the query_memory MCP tool so a thin
  // RemoteBackend client (mcp mode) can reach memory without opening the DB.
  app.post("/memory/query", async (req) => {
    const body = (req.body ?? {}) as { query: string; taskId: string; agentId?: string; topK?: number };
    return orchestrator.memory.query(body.query, {
      taskId: body.taskId,
      agentId: body.agentId,
      topK: body.topK,
    });
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

  // Plugin registry — lists all loaded JSON/adapter plugins (partners only)
  app.get("/plugins", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "forbidden" });
    return pluginRegistry.list();
  });

  // ── Identity + lawyer profiles ──────────────────────────────────────────────
  app.get("/me", async (req) => {
    const user = getUser(req);
    const mode = user?.mode ?? "lite";
    return {
      user,
      authEnabled: Config.auth.enabled,
      // Mode metadata the UI needs to theme itself and gate features.
      mode,
      modeColor: MODE_COLORS[mode],
      capabilities: MODE_CAPABILITIES[mode],
    };
  });

  // Partners see full profiles (including email for contact/admin purposes).
  // Lawyers see only the display fields needed to render the roster UI.
  // toneProfile (which contains injectionSnippet) is only returned to the
  // profile owner and partners — it is stripped from all other responses.
  app.get("/profiles", async (req) => {
    const profiles = orchestrator.profiles.list();
    const user = getUser(req);
    if (isPartner(user)) return profiles;
    // Lawyers: public roster fields only — no toneProfile
    return profiles.map(({ id, name, title, color, role }) => ({ id, name, title, color, role }));
  });

  app.get("/profiles/:id", async (req, reply) => {
    const { id } = req.params as { id: string };
    const profile = orchestrator.profiles.get(id);
    if (!profile) return reply.status(404).send({ error: "Profile not found" });
    const user = getUser(req);
    // Another lawyer viewing a peer: public fields only
    if (!isPartner(user) && user?.profileId !== id) {
      const { id: pid, name, title, color, role } = profile;
      return { id: pid, name, title, color, role };
    }
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
    // Non-partners cannot change role or mode — both are partner-assigned.
    if (!isPartner(user) && patch.role) {
      return reply.status(403).send({ error: "Partner role required to change role" });
    }
    if (!isPartner(user) && patch.mode !== undefined) {
      return reply.status(403).send({ error: "Partner role required to set user mode" });
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

  /**
   * POST /profiles/:id/tone/import
   *
   * Upload any writing sample source to build a tone profile:
   *   - LinkedIn data export ZIP or CSV (Settings → Data privacy → Get a copy of your data)
   *   - Generic CSV (one text-rich column, or all cells joined per row)
   *   - DOCX / Word documents (prose extracted from word/document.xml)
   *   - PDF (text extracted via PyMuPDF backend)
   *   - Plain text / Markdown
   *
   * Format is auto-detected from the file extension and content.
   * Partners can import for any profile; lawyers can only import for themselves.
   */
  app.post("/profiles/:id/tone/import", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    if (!isPartner(user) && user?.profileId !== id) {
      return reply.status(403).send({ error: "You can only import tone for your own profile" });
    }
    const profile = orchestrator.profiles.get(id);
    if (!profile) return reply.status(404).send({ error: "Profile not found" });

    let buf: Buffer;
    let filename = "upload";
    try {
      const data = await req.file();
      if (!data) return reply.status(400).send({ error: "No file uploaded" });
      buf = await data.toBuffer();
      filename = data.filename || "upload";
    } catch {
      return reply.status(400).send({ error: "File upload failed" });
    }

    // Rate limit: reject if a tone profile was generated in the last 60 seconds
    if (profile.toneProfile?.generatedAt) {
      const ageMs = Date.now() - new Date(profile.toneProfile.generatedAt).getTime();
      if (ageMs < 60_000) {
        return reply.status(429).send({ error: "Tone profile was just updated. Please wait before importing again." });
      }
    }

    const { samples, sourceType } = await extractWritingSamples(buf, filename);
    if (!samples.length) {
      return reply.status(422).send({
        error: "No writing samples found in the uploaded file. Accepted formats: LinkedIn export ZIP/CSV, Word (.docx), PDF, plain text, or any CSV with a text column.",
        linkedInExportUrl: "https://www.linkedin.com/mypreferences/d/download-my-data",
      });
    }

    try {
      const tone = await analyzeTone(samples, profile.name, sourceType, id);
      const updated = await orchestrator.profiles.updateTone(id, tone);
      logger.info("Tone profile generated", { profileId: id, sampleCount: samples.length, sourceType });
      auditLogger.write({ event: "profile.tone.imported", data: { profileId: id, sampleCount: samples.length, sourceType, importedBy: user?.profileId } });
      return reply.status(200).send({ toneProfile: updated.toneProfile, samplesAnalysed: samples.length, sourceType });
    } catch (err) {
      logger.error("Tone analysis failed", { profileId: id, error: (err as Error).message });
      return reply.status(500).send({ error: "Tone analysis failed. Please try again." });
    }
  });

  /**
   * POST /profiles/:id/tone/linkedin-import  (backwards-compatible alias)
   *
   * Accepts LinkedIn data export ZIP or CSV.
   * Calls the same logic as /tone/import — kept for API backwards compatibility.
   */
  app.post("/profiles/:id/tone/linkedin-import", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    if (!isPartner(user) && user?.profileId !== id) {
      return reply.status(403).send({ error: "You can only import tone for your own profile" });
    }
    const profile = orchestrator.profiles.get(id);
    if (!profile) return reply.status(404).send({ error: "Profile not found" });

    let buf: Buffer;
    try {
      const data = await req.file();
      if (!data) return reply.status(400).send({ error: "No file uploaded" });
      buf = await data.toBuffer();
    } catch {
      return reply.status(400).send({ error: "File upload failed" });
    }

    if (profile.toneProfile?.generatedAt) {
      const ageMs = Date.now() - new Date(profile.toneProfile.generatedAt).getTime();
      if (ageMs < 60_000) {
        return reply.status(429).send({ error: "Tone profile was just updated. Please wait before importing again." });
      }
    }

    const posts = parseLinkedInExport(buf);
    if (!posts.length) {
      return reply.status(422).send({
        error: "No posts found in export. Upload the ZIP from linkedin.com/mypreferences/d/download-my-data or the extracted Shares.csv / Posts and Articles.csv.",
        exportUrl: "https://www.linkedin.com/mypreferences/d/download-my-data",
      });
    }

    try {
      const tone = await analyzeTone(posts, profile.name, "linkedin_export", id);
      const updated = await orchestrator.profiles.updateTone(id, tone);
      logger.info("Tone profile generated from LinkedIn export", { profileId: id, sampleCount: posts.length });
      auditLogger.write({ event: "profile.tone.imported", data: { profileId: id, sampleCount: posts.length, sourceType: "linkedin_export", importedBy: user?.profileId } });
      return reply.status(200).send({ toneProfile: updated.toneProfile, samplesAnalysed: posts.length, sourceType: "linkedin_export" });
    } catch (err) {
      logger.error("Tone analysis failed", { profileId: id, error: (err as Error).message });
      return reply.status(500).send({ error: "Tone analysis failed. Please try again." });
    }
  });

  /** DELETE /profiles/:id/tone — clear a lawyer's tone profile. */
  app.delete("/profiles/:id/tone", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    if (!isPartner(user) && user?.profileId !== id) {
      return reply.status(403).send({ error: "You can only clear your own tone profile" });
    }
    try {
      const updated = await orchestrator.profiles.clearTone(id);
      auditLogger.write({ event: "profile.tone.cleared", data: { profileId: id, clearedBy: user?.profileId } });
      return reply.status(200).send(updated);
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
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
    const { name } = req.body as { name?: string };
    const trimmed = (typeof name === "string" ? name : "").trim().slice(0, 500);
    if (!trimmed) return reply.status(400).send({ error: "name is required" });
    return orchestrator.clients.checkConflict(trimmed);
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
    const cappedLimit = limitNum && Number.isInteger(limitNum) && limitNum > 0
      ? Math.min(limitNum, 1000) : undefined;
    return auditLogger.readRecent(taskId, cappedLimit).filter(visible);
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

  // ── Time entries ─────────────────────────────────────────────────────────────
  // Lawyers see only their own entries; partners see all.
  app.get("/time-entries", async (req) => {
    const user = getUser(req);
    const { profileId, taskId, matterNumber, from, to } = req.query as Record<string, string>;
    const filter = {
      // Lawyers are restricted to their own entries; partners may filter by any profileId.
      profileId: isPartner(user) ? (profileId || undefined) : user?.profileId,
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
    };
    return orchestrator.time.list(filter);
  });

  app.get("/time-entries/export.json", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { profileId, taskId, matterNumber, from, to } = req.query as Record<string, string>;
    const filter = {
      profileId: profileId || undefined,
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
    };
    return orchestrator.time.exportJson(filter);
  });

  app.get("/time-entries/export.csv", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { profileId, taskId, matterNumber, from, to } = req.query as Record<string, string>;
    const filter = {
      profileId: profileId || undefined,
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
    };
    reply.header("Content-Type", "text/csv; charset=utf-8");
    reply.header("Content-Disposition", "attachment; filename=\"time-entries.csv\"");
    return orchestrator.time.exportCsv(filter);
  });

  // ── NOSLEGAL analytics ────────────────────────────────────────────────────────
  // Aggregates NOSLEGAL facet breakdown across all tasks the caller can see.
  // Partner only — provides firm-wide matter analytics.
  app.get("/analytics/noslegal", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const tasks = filterVisible(getUser(req), orchestrator.listTasks());
    const byAreaOfLaw: Record<string, number> = {};
    const byWorkType: Record<string, number> = {};
    const bySector: Record<string, number> = {};
    const byAssetType: Record<string, number> = {};
    for (const task of tasks) {
      if (!task.noslegal) continue;
      const { areaOfLaw, workType, sector, assetType } = task.noslegal;
      if (areaOfLaw) byAreaOfLaw[areaOfLaw] = (byAreaOfLaw[areaOfLaw] ?? 0) + 1;
      if (workType)  byWorkType[workType]   = (byWorkType[workType]   ?? 0) + 1;
      if (sector)    bySector[sector]       = (bySector[sector]       ?? 0) + 1;
      if (assetType) byAssetType[assetType] = (byAssetType[assetType] ?? 0) + 1;
    }
    return { total: tasks.length, byAreaOfLaw, byWorkType, bySector, byAssetType };
  });

  // ── Cost analytics ────────────────────────────────────────────────────────
  // Aggregate cost summary across all recorded calls — partner only.
  app.get("/cost/summary", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    return costStore.summarise();
  });

  // Per-task cost breakdown. Access-controlled like the task itself.
  app.get("/tasks/:id/cost", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    const entries = costStore.forTask(id);
    return { taskId: id, summary: costStore.summarise(entries), entries };
  });

  // Per-profile cost (tone analysis + any tasks created by this profile).
  // Partners see any profile; lawyers see only their own.
  app.get("/profiles/:id/cost", async (req, reply) => {
    const { id } = req.params as { id: string };
    const user = getUser(req);
    if (!isPartner(user) && user?.profileId !== id) {
      return reply.status(403).send({ error: "You can only view your own cost data" });
    }
    const profile = orchestrator.profiles.get(id);
    if (!profile) return reply.status(404).send({ error: "Profile not found" });
    const entries = costStore.forProfile(id);
    return { profileId: id, summary: costStore.summarise(entries), entries };
  });

  // ── Clio OAuth + matter import ────────────────────────────────────────────────
  const CLIO_STATE_COOKIE = "clio_oauth_state";
  const clioStateCookieOpts = { httpOnly: true as const, signed: true, path: "/", maxAge: 600 };

  app.get("/auth/clio/status", async () => clioClient.status());

  app.get("/auth/clio/connect", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    if (!Config.clio.enabled) return reply.code(503).send({ error: "Clio integration not configured — set CLIO_CLIENT_ID" });
    const state = randomUUID();
    reply.setCookie(CLIO_STATE_COOKIE, state, clioStateCookieOpts);
    return reply.redirect(clioClient.authUrl(state));
  });

  app.get("/auth/clio/callback", async (req, reply) => {
    const { code, state } = req.query as { code?: string; state?: string };
    const raw = (req.cookies as Record<string, string> | undefined)?.[CLIO_STATE_COOKIE];
    const unsigned = raw
      ? (req as unknown as { unsignCookie: (v: string) => { valid: boolean; value: string | null } }).unsignCookie(raw)
      : { valid: false, value: null };
    reply.clearCookie(CLIO_STATE_COOKIE, { path: "/" });
    if (!code || !state || !unsigned.valid || unsigned.value !== state) {
      return reply.redirect(`${Config.auth.uiUrl}?clio=error`);
    }
    try {
      await clioClient.exchangeCode(code);
      logger.info("Clio connected", clioClient.status());
      return reply.redirect(`${Config.auth.uiUrl}?clio=connected`);
    } catch (err) {
      logger.warn("Clio OAuth callback failed", { error: (err as Error).message });
      return reply.redirect(`${Config.auth.uiUrl}?clio=error`);
    }
  });

  app.delete("/auth/clio/disconnect", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    await clioClient.disconnect();
    return { ok: true };
  });

  app.post("/tasks/from-clio-matter", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    if (!clioClient.isConnected()) return reply.code(503).send({ error: "Clio not connected — visit /auth/clio/connect" });
    const { matterId, workflowType } = req.body as { matterId: number; workflowType?: string };
    if (!matterId) return reply.code(400).send({ error: "matterId is required" });

    let matterRaw: unknown;
    try {
      matterRaw = await clioClient.getMatter(matterId);
    } catch (err) {
      return reply.code(502).send({ error: `Clio getMatter failed: ${(err as Error).message}` });
    }

    const matter = (matterRaw as { data?: Record<string, unknown> }).data ?? {};
    const displayNumber = String(matter["display_number"] ?? "");
    const description = String(matter["description"] ?? `Clio matter ${displayNumber}`);
    const clientData = matter["client"] as { id?: number; name?: string } | undefined;
    const clientId = clientData?.id ? String(clientData.id) : undefined;
    const practiceAreaData = matter["practice_area"] as { name?: string } | undefined;
    const clioArea = practiceAreaData?.name ?? "";

    // Best-effort map to Big Michael practice area
    const PRACTICE_AREA_MAP: Record<string, string> = {
      "Corporate": "Corporate", "Employment": "Employment", "Litigation": "Litigation",
      "Real Estate": "Real Estate", "Intellectual Property": "Intellectual Property",
      "Tax": "Tax", "Family": "Family", "Criminal": "Criminal", "Immigration": "Immigration",
      "Bankruptcy": "Bankruptcy", "Estate Planning": "Estate Planning", "Environmental": "Environmental",
      "Healthcare": "Healthcare", "Finance": "Finance", "Compliance": "Compliance",
    };
    const practiceArea = Object.keys(PRACTICE_AREA_MAP).find((k) =>
      clioArea.toLowerCase().includes(k.toLowerCase()),
    ) ?? "Litigation";

    // Ingest documents from Clio (cap at 20)
    let documentsIngested = 0;
    const SUPPORTED_EXT = [".pdf", ".docx", ".doc", ".txt"];
    try {
      const docsRaw = await clioClient.listDocuments(matterId, 20);
      const docs = ((docsRaw as { data?: unknown[] }).data ?? []) as Array<{ id: number; name: string; content_type?: string }>;
      for (const doc of docs.slice(0, 20)) {
        const ext = doc.name ? ("." + doc.name.split(".").pop()!.toLowerCase()) : "";
        if (!SUPPORTED_EXT.includes(ext)) continue;
        try {
          const buf = await clioClient.downloadDocument(doc.id);
          const { samples } = await extractWritingSamples(buf, doc.name);
          const content = samples.join("\n\n").slice(0, 50_000);
          if (content.trim()) {
            await orchestrator.knowledge.ingest({
              title: doc.name,
              content,
              source: "clio",
              documentType: "matter_file",
            });
            documentsIngested++;
          }
        } catch (err) {
          logger.warn("Clio document ingest failed", { docId: doc.id, name: doc.name, error: (err as Error).message });
        }
      }
    } catch (err) {
      logger.warn("Clio listDocuments failed", { matterId, error: (err as Error).message });
    }

    const taskDesc = `[Clio matter ${displayNumber}] ${description} (Practice area: ${practiceArea})`;
    const task = await orchestrator.submitTask({
      description: taskDesc,
      workflowType: (workflowType ?? "roundtable") as WorkflowType,
      // clientNumber deliberately omitted — Clio's internal client ID is not the
      // firm's own client numbering scheme (e.g. "C-001"). Leave unset so Big
      // Michael's classifier can derive the correct client from the description.
      matterNumber: displayNumber || undefined,
    });
    const user = getUser(req);
    if (user) orchestrator.assignLawyers(task.id, [user.profileId]);
    return reply.code(201).send({ task: orchestrator.getTask(task.id) ?? task, documentsIngested });
  });

  app.post("/time-entries/sync-to-clio", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    if (!clioClient.isConnected()) return reply.code(503).send({ error: "Clio not connected — visit /auth/clio/connect" });
    const { clioMatterId, from, to, matterNumber } = req.body as { clioMatterId: number; from?: string; to?: string; matterNumber?: string };
    if (!clioMatterId) return reply.code(400).send({ error: "clioMatterId is required" });

    const allEntries = orchestrator.time.list({
      matterNumber: matterNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
    }).filter((e) => e.durationMs > 0);
    const entries = allEntries.filter((e) => !e.clioSyncedAt);
    const skipped = allEntries.length - entries.length;

    let synced = 0;
    let errors = 0;
    for (const entry of entries) {
      try {
        const durationHours = Math.max(entry.billingUnits * 0.1, entry.durationMs / 3_600_000);
        const dateOn = entry.startedAt.toISOString().slice(0, 10);
        await clioClient.createActivity(clioMatterId, {
          description: entry.description,
          dateOn,
          durationHours: Math.round(durationHours * 100) / 100,
        });
        orchestrator.time.markClioSynced(entry.id);
        synced++;
      } catch (err) {
        logger.warn("Clio sync activity failed", { entryId: entry.id, error: (err as Error).message });
        errors++;
      }
    }
    return { synced, skipped, errors };
  });

  await app.listen({ port: Config.api.port, host: Config.api.host });
  logger.info("REST API started", { port: Config.api.port, host: Config.api.host, auth: Config.api.apiKey ? "x-api-key" : "none" });
}

// ─── Tool handler ─────────────────────────────────────────────────────────────

async function handleTool(
  name: string,
  args: Record<string, unknown>,
  backend: LegalBackend,
): Promise<unknown> {
  switch (name) {
    case "submit_task":
      return backend.submitTask({
        description: args.description as string,
        workflowType: args.workflowType as WorkflowType,
        documentIds: args.documentIds as string[] | undefined,
        clientNumber: args.clientNumber as string | undefined,
        matterNumber: args.matterNumber as string | undefined,
        jurisdiction: args.jurisdiction as string | undefined,
      });

    case "get_task": {
      const task = await backend.getTask(args.taskId as string);
      if (!task) throw new Error(`Task not found: ${args.taskId}`);
      return task;
    }

    case "list_tasks":
      return backend.listTasks();

    case "approve_gate":
      await backend.approveGate(args.taskId as string, args.gateId as string, args.note as string | undefined);
      return { ok: true };

    case "reject_gate":
      await backend.rejectGate(args.taskId as string, args.gateId as string, args.reason as string);
      return { ok: true };

    case "ingest_document":
      return backend.ingestDocument(args);

    case "search_knowledge":
      return backend.searchKnowledge(args.query as string, {
        topK: args.topK as number | undefined,
        jurisdiction: args.jurisdiction as string | undefined,
        documentType: args.documentType as string | undefined,
      });

    case "list_agents":
      return backend.listAgents({
        tier: args.tier as 0 | 1 | 2 | 3 | undefined,
        topK: 100,
      });

    case "query_memory":
      return backend.queryMemory(args.query as string, {
        taskId: args.taskId as string,
        agentId: args.agentId as string | undefined,
        topK: args.topK as number | undefined,
      });

    case "list_templates":
      return backend.listTemplates();

    case "list_plugins":
      return backend.listPlugins();

    case "submit_from_template":
      return backend.submitFromTemplate(
        args.templateId as string,
        args.substitutions as Record<string, string> | undefined,
        args.documentIds as string[] | undefined,
      );

    case "get_round": {
      const task = await backend.getTask(args.taskId as string);
      if (!task) throw new Error(`Task not found: ${args.taskId}`);
      const roundState = task.rounds[(args.round as number) - 1];
      if (!roundState) throw new Error(`Round ${args.round} not found`);
      return roundState;
    }

    case "get_audit":
      // MCP runs over stdio as the LOCAL_PARTNER (full partner access). The
      // backend applies the same visibility filter as the REST endpoint.
      return backend.getAudit(
        args.taskId as string | undefined,
        args.limit as number | undefined,
      );

    case "get_time_entries":
      // MCP runs as LOCAL_PARTNER (full access). Partners see all time entries.
      return backend.listTimeEntries({
        profileId: args.profileId as string | undefined,
        taskId: args.taskId as string | undefined,
        matterNumber: args.matterNumber as string | undefined,
        from: args.from as string | undefined,
        to: args.to as string | undefined,
      });

    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}