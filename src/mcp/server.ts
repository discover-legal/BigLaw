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
import type { WorkflowType, SessionUser, DocketAlert } from "../types.js";
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
import { twentyClient } from "../integrations/twenty.js";
import { ocgStore } from "../ocg/index.js";
import { analyzeClientVoice } from "../services/clientVoiceAnalyzer.js";
import { jobQueue } from "../queue/index.js";
import { startWorker } from "../queue/worker.js";
import { exportLedes1998B } from "../billing/ledes.js";
import { generateStatusReport } from "../reports/status.js";
import type { StatusReportOptions } from "../reports/status.js";
import { registerTeamsBotRoutes, attachTeamsTaskNotifier } from "../bots/teams.js";
import { registerSlackBotRoutes, attachSlackTaskNotifier } from "../bots/slack.js";

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
  // ── Goliath killer tools ──────────────────────────────────────────────────
  {
    name: "check_citation",
    description: "Check whether a case citation is still good law. Returns a KeyCite-equivalent green/yellow/red signal. Replaces Westlaw KeyCite and LexisNexis Shepard's.",
    inputSchema: {
      type: "object",
      properties: {
        query: { type: "string", description: "Case citation or case name, e.g. '410 U.S. 113', 'Roe v. Wade', '[2024] EWHC 123 (Comm)'" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
      required: ["query"],
    },
  },
  {
    name: "get_matter_health",
    description: "Compute the health score (0–100) for a matter. Returns signal (green/amber/red), dimension scores, risk factors, and trend. Replaces Clio Insights.",
    inputSchema: {
      type: "object",
      properties: {
        matterNumber: { type: "string", description: "The matter number to score" },
      },
      required: ["matterNumber"],
    },
  },
  {
    name: "get_portfolio_health",
    description: "Compute health scores for all matters and return a portfolio summary sorted by risk. Partner only. Replaces Clio Insights portfolio view.",
    inputSchema: { type: "object", properties: {} },
  },
  {
    name: "build_playbook",
    description: "Build a scoped clause playbook from the firm's precedent library (firm / client / matter / personal). Replaces Contract Express and Practical Law market standards.",
    inputSchema: {
      type: "object",
      properties: {
        scope: { type: "string", enum: ["firm", "client", "matter", "personal"], description: "Playbook scope in the four-tier cascade" },
        ownerId: { type: "string", description: "clientNumber, matterNumber, or profileId — depends on scope. Omit for firm scope." },
        ownerName: { type: "string", description: "Human-readable name for the owner" },
        practiceArea: { type: "string", description: "Practice area (e.g. 'Corporate & M&A', 'Banking & Finance')" },
        jurisdiction: { type: "string", description: "Governing jurisdiction (optional)" },
        name: { type: "string", description: "Name for this playbook" },
        clauseTypes: { type: "array", items: { type: "string" }, description: "Specific clause types to extract (optional — uses practice-area defaults if omitted)" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
      required: ["scope", "practiceArea", "name"],
    },
  },
  {
    name: "query_playbook",
    description: "Resolve the four-tier playbook cascade (firm → client → matter → personal) for a clause type. Returns the most specific position available plus personal notes.",
    inputSchema: {
      type: "object",
      properties: {
        clauseType: { type: "string", description: "The clause type to look up, or '*' for all clause types" },
        practiceArea: { type: "string", description: "Filter to a specific practice area" },
        matterNumber: { type: "string", description: "Include the matter-tier playbook" },
        clientId: { type: "string", description: "Include the client-tier playbook" },
        profileId: { type: "string", description: "Include the personal-tier playbook" },
      },
      required: ["clauseType"],
    },
  },
  {
    name: "validate_invoice",
    description: "Validate an outside-counsel invoice against the client's OCG. Returns violations, suggested reductions, and an optional dispute letter. Replaces BillBlast / TyMetrix / Apperio.",
    inputSchema: {
      type: "object",
      properties: {
        invoiceText: { type: "string", description: "LEDES 1998B invoice text (pipe or comma delimited). Leave empty to use lineItems." },
        clientId: { type: "string", description: "Client ID — used to load the client's OCG" },
        submittedByFirm: { type: "string", description: "Name of the outside counsel firm" },
        matterNumber: { type: "string", description: "Matter reference" },
        generateDisputeLetter: { type: "boolean", description: "Generate an AI-drafted dispute letter if hard violations are found (default false)" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
      required: ["invoiceText"],
    },
  },
  {
    name: "redline_contract",
    description: "Run automated playbook-driven contract redlining on a counterparty draft. Returns a structured redline report — clause-by-clause accept/redline/escalate/delete dispositions, proposed replacement language, and an executive summary. Replaces Definely, Kira/Luminance, and 4–8 hrs of manual associate markup per draft.",
    inputSchema: {
      type: "object",
      properties: {
        documentText: { type: "string", description: "Full text of the counterparty draft to redline" },
        practiceArea: { type: "string", description: "Practice area context (e.g. M&A, finance, employment)" },
        jurisdiction: { type: "string", description: "Governing law jurisdiction (e.g. UK, US-DE)" },
        matterNumber: { type: "string", description: "Matter number — loads matter-specific playbook tier" },
        clientId: { type: "string", description: "Client ID — loads client-specific playbook tier" },
        profileId: { type: "string", description: "Lawyer profile ID — loads personal playbook tier" },
        documentId: { type: "string", description: "Document ID for cross-referencing (optional)" },
        documentTitle: { type: "string", description: "Document title for the report (optional)" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
      required: ["documentText"],
    },
  },
  {
    name: "generate_headnotes",
    description: "Extract structured headnotes and key holdings from a court opinion. Returns numbered propositions (ratio/obiter), distinguishing factors, NOSLEGAL tags, and the core ratio decidendi. Replaces Westlaw Key Numbers, LexisNexis headnotes, and 2–4 hrs of manual law clerk annotation per opinion.",
    inputSchema: {
      type: "object",
      properties: {
        opinionText: { type: "string", description: "Full text of the court opinion" },
        caseName: { type: "string", description: "Case name (optional — extracted from text if omitted)" },
        citation: { type: "string", description: "Neutral or report citation (optional)" },
        court: { type: "string", description: "Court name (optional)" },
        dateFiled: { type: "string", description: "ISO date the decision was filed (optional)" },
        jurisdiction: { type: "string", description: "Jurisdiction (e.g. UK, US, AU) — optional" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
      required: ["opinionText"],
    },
  },
  {
    name: "get_client_briefing",
    description: "Generate a pre-call partner briefing for a client. Returns matter status, billing posture, open items, relationship notes, and a drafted Markdown briefing document. Replaces Clio Grow / CRM, Clio Insights client reports, and 30 min of manual partner prep.",
    inputSchema: {
      type: "object",
      properties: {
        clientId: { type: "string", description: "Client ID (UUID) from the client roster" },
        clientNumber: { type: "string", description: "Client number — used if clientId is not provided" },
        briefingDate: { type: "string", description: "ISO date for the briefing (defaults to today)" },
        industryContext: { type: "string", description: "Optional industry or regulatory context to include" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
    },
  },
  {
    name: "generate_precedent",
    description: "Generate a firm-specific precedent document (NDA, SPA, employment contract, etc.) from the firm's own knowledge store and playbook cascade. Returns a complete first-draft document in the firm's voice. Replaces Thomson Reuters Practical Law Standard Documents and LexisNexis PSL (£15–25k/yr).",
    inputSchema: {
      type: "object",
      properties: {
        documentType: {
          type: "string",
          enum: ["nda", "spa", "asset_purchase", "facility", "employment", "service_agreement",
            "supply_agreement", "jv_agreement", "ip_assignment", "licence", "settlement", "term_sheet", "other"],
          description: "Type of document to generate",
        },
        practiceArea: { type: "string", description: "Practice area context" },
        jurisdiction: { type: "string", description: "Governing law jurisdiction (e.g. 'English law', 'US-DE')" },
        actingFor: { type: "string", description: "Which party the firm acts for (e.g. 'buyer', 'disclosing party')" },
        matterNumber: { type: "string", description: "Matter number — loads matter-specific playbook tier" },
        clientId: { type: "string", description: "Client ID — loads client-specific playbook tier" },
        profileId: { type: "string", description: "Lawyer profile ID — loads personal playbook tier" },
        specialInstructions: { type: "string", description: "Special drafting instructions (e.g. deal-specific carve-outs)" },
        taskId: { type: "string", description: "Optional task ID for cost tracking" },
      },
      required: ["documentType"],
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

  // Preserve the raw request body for routes that must verify an HMAC over the
  // exact bytes the client sent (Teams / Slack webhook signatures). Fastify's
  // default JSON parser discards the raw body; re-serializing req.body with
  // JSON.stringify does NOT reproduce the original bytes (key order, spacing,
  // unicode escaping all differ), which both breaks legitimate signatures and
  // anchors verification to server-canonicalized content. This parser keeps the
  // raw string on req.rawBody while still producing the parsed object as req.body.
  app.addContentTypeParser("application/json", { parseAs: "string" }, (req, body, done) => {
    (req as unknown as { rawBody?: string }).rawBody = body as string;
    const s = body as string;
    if (!s || s.length === 0) { done(null, {}); return; }
    try {
      done(null, JSON.parse(s));
    } catch (err) {
      (err as { statusCode?: number }).statusCode = 400;
      done(err as Error, undefined);
    }
  });

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

  // ── Channel bots (Teams Outgoing Webhook + Slack Events API) ─────────────
  // These routes verify HMAC signatures before touching the orchestrator.
  // No auth middleware applied — the bots have their own signature checks.
  registerTeamsBotRoutes(app, orchestrator);
  registerSlackBotRoutes(app, orchestrator);

  // Attach proactive task-complete notifiers to the orchestrator event stream
  attachTeamsTaskNotifier(orchestrator, Config.bots.teams.incomingWebhookUrl);
  attachSlackTaskNotifier(orchestrator, Config.bots.slack.defaultChannel);

  // Resolve the principal for a request. Auth OFF (local) → the LOCAL_PARTNER
  // who sees everything. Auth ON → the signed session cookie from OAuth login.
  const getUser = (req: FastifyRequest): SessionUser | null => {
    if (!Config.auth.enabled) return LOCAL_PARTNER;
    return readSessionCookie(req);
  };

  // Resolve the actor ID for audit events. LOCAL_PARTNER.profileId when auth
  // is disabled; authenticated profileId when auth is enabled; "anonymous" if
  // a request somehow arrives with no session (should not happen on protected routes).
  const actorOf = (req: FastifyRequest): string => getUser(req)?.profileId ?? "anonymous";

  // Emit access.denied / auth.session.expired for all 403 / 401 responses.
  app.addHook("onSend", async (req, reply, payload) => {
    if (reply.statusCode === 403) {
      auditLogger.write({
        event: "access.denied",
        actorId: actorOf(req),
        data: { method: req.method, url: req.url },
      });
    } else if (reply.statusCode === 401) {
      auditLogger.write({
        event: "auth.session.expired",
        actorId: actorOf(req),
        data: { url: req.url },
      });
    }
    return payload;
  });

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
      if (
        req.url === "/health" ||
        req.url.startsWith("/auth/") ||
        req.url === "/bots/teams/webhook" ||
        req.url === "/bots/slack/events"
      ) return;
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
    return reply.status(204).send();
  });

  // Assign lawyer(s) to a matter — partner only (controls cross-lawyer sharing).
  app.post("/tasks/:id/assign", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const { lawyerIds } = req.body as { lawyerIds: string[] };
    const task = orchestrator.assignLawyers(id, Array.isArray(lawyerIds) ? lawyerIds : [], actorOf(req));
    if (!task) return reply.status(404).send({ error: "Task not found" });
    return task;
  });

  // Structured tabulate output as downloadable CSV
  app.get("/tasks/:id/table.csv", async (req, reply) => {
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task || !canViewTask(getUser(req), task)) return reply.status(404).send({ error: "Task not found" });
    if (!task.table) return reply.status(404).send({ error: "No table available for this task" });

    // Neutralize spreadsheet formula injection (see time/index.ts exportCsv):
    // a field starting with = + - @ or a control char is executed as a formula.
    const esc = (v: string) => {
      let s = String(v);
      if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`;
      return `"${s.replace(/"/g, '""')}"`;
    };
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
    auditLogger.write({
      event: "document.ingested",
      actorId: actorOf(req),
      data: { docId, title: body.title, practiceArea, detectedClientNumber: detectedClient?.clientNumber },
    });
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

    const title = basename(filename, ext).slice(0, 255);
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
    auditLogger.write({
      event: "document.uploaded",
      actorId: actorOf(req),
      data: { docId: id, title, filename, practiceArea, detectedClientNumber: detectedClient?.clientNumber },
    });
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
    const results = await orchestrator.knowledge.search(query, {
      topK: topKNum && Number.isInteger(topKNum) && topKNum > 0 ? Math.min(topKNum, 50) : undefined,
      jurisdiction,
      documentType,
      ownerId: docOwnerScope(req),
    });
    auditLogger.write({
      event: "document.searched",
      actorId: actorOf(req),
      data: { query: query ?? "", resultCount: results.length, jurisdiction, documentType },
    });
    return results;
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
  app.post("/memory/query", async (req, reply) => {
    const body = (req.body ?? {}) as { query: string; taskId?: string; agentId?: string; topK?: number };
    // If taskId is provided, verify the caller can view that task
    if (body.taskId) {
      const task = orchestrator.getTask(body.taskId);
      if (!task || !canViewTask(getUser(req), task)) {
        return reply.status(403).send({ error: "Forbidden" });
      }
    }
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
      const profile = await orchestrator.profiles.create(req.body as {
        name: string; email: string; role?: string; title?: string; color?: string;
        practiceAreas?: string[]; bio?: string;
      });
      auditLogger.write({
        event: "profile.created",
        actorId: actorOf(req),
        data: { profileId: profile.id, email: profile.email, role: profile.role },
      });
      return reply.status(201).send(profile);
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
      const updated = await orchestrator.profiles.update(id, patch as Record<string, never>);
      auditLogger.write({
        event: "profile.updated",
        actorId: actorOf(req),
        data: { profileId: id, fields: Object.keys(patch) },
      });
      return updated;
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  app.delete("/profiles/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const ok = await orchestrator.profiles.remove(id);
      if (ok) {
        auditLogger.write({
          event: "profile.deleted",
          actorId: actorOf(req),
          data: { profileId: id },
        });
      }
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
      auditLogger.write({ event: "profile.tone.imported", actorId: actorOf(req), data: { profileId: id, sampleCount: samples.length, sourceType } });
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
      auditLogger.write({ event: "profile.tone.imported", actorId: actorOf(req), data: { profileId: id, sampleCount: posts.length, sourceType: "linkedin_export" } });
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
      auditLogger.write({ event: "profile.tone.cleared", actorId: actorOf(req), data: { profileId: id } });
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
      const conflict = orchestrator.clients.checkConflict(body.name, body.adversaries ?? []);
      const client = await orchestrator.clients.create(body);
      auditLogger.write({
        event: "client.created",
        actorId: actorOf(req),
        data: { clientId: client.id, clientNumber: client.clientNumber, name: client.name },
      });
      return reply.status(201).send({ ...client, conflict });
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.patch("/clients/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const updated = await orchestrator.clients.update(id, req.body as Record<string, never>);
      auditLogger.write({
        event: "client.updated",
        actorId: actorOf(req),
        data: { clientId: id, fields: Object.keys(req.body as object) },
      });
      return updated;
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  app.delete("/clients/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const ok = await orchestrator.clients.remove(id);
      if (ok) {
        auditLogger.write({
          event: "client.deleted",
          actorId: actorOf(req),
          data: { clientId: id },
        });
      }
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
      auditLogger.write({
        event: "matter.added",
        actorId: actorOf(req),
        data: { clientId: id, matterNumber: matter.matterNumber, practiceArea: matter.practiceArea },
      });
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
      if (ok) {
        auditLogger.write({
          event: "matter.removed",
          actorId: actorOf(req),
          data: { clientId: id, matterNumber },
        });
      }
      return ok ? reply.status(200).send({ ok: true }) : reply.status(404).send({ error: "Matter not found" });
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.post("/clients/check-conflict", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { name, adversaries } = req.body as { name?: string; adversaries?: string[] };
    const trimmed = (typeof name === "string" ? name : "").trim().slice(0, 500);
    if (!trimmed) return reply.status(400).send({ error: "name is required" });
    const advs = Array.isArray(adversaries) ? adversaries.slice(0, 200).map((a) => String(a)) : [];
    return orchestrator.clients.checkConflict(trimmed, advs);
  });

  // ── Docket monitoring ─────────────────────────────────────────────────────────

  app.post("/dockets/watch", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber, docketNumber, court, caseName } = req.body as {
      matterNumber: string; docketNumber: string; court: string; caseName?: string;
    };
    if (!matterNumber || !docketNumber || !court) return reply.status(400).send({ error: "matterNumber, docketNumber, court required" });
    try {
      const entry = orchestrator.docketMonitor.watch(matterNumber, docketNumber, court, caseName);
      return reply.status(201).send(entry);
    } catch (err) {
      return reply.status(400).send({ error: (err as Error).message });
    }
  });

  app.delete("/dockets/watch/:matterNumber", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber } = req.params as { matterNumber: string };
    const removed = orchestrator.docketMonitor.unwatch(matterNumber);
    if (!removed) return reply.status(404).send({ error: "No watched docket for this matter" });
    return { ok: true };
  });

  app.get("/dockets", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    return orchestrator.docketMonitor.list();
  });

  app.post("/dockets/check-now", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (!orchestrator.docketMonitor.isEnabled()) return reply.status(503).send({ error: "Docket monitoring not enabled (set DOCKET_MONITOR_ENABLED=true)" });
    await orchestrator.docketMonitor.checkAll();
    return { ok: true, watching: orchestrator.docketMonitor.list().length };
  });

  const MAX_DOCKET_SSE_LISTENERS = 20;
  app.get("/dockets/alerts/stream", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (orchestrator.docketMonitor.listenerCount("alert") >= MAX_DOCKET_SSE_LISTENERS) {
      return reply.status(429).send({ error: "Too many concurrent docket streams" });
    }
    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();
    const send = (alert: DocketAlert) => reply.raw.write(`data: ${JSON.stringify(alert)}\n\n`);
    orchestrator.docketMonitor.on("alert", send);
    req.raw.on("close", () => orchestrator.docketMonitor.off("alert", send));
  });

  // ── Outside Counsel Guidelines ────────────────────────────────────────────────

  /** POST /clients/:id/ocg — ingest OCG text for a client (JSON body or multipart file). */
  app.post("/clients/:id/ocg", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const client = orchestrator.clients.get(id);
    if (!client) return reply.status(404).send({ error: "Client not found" });

    // Rate-limit: disallow re-ingestion within 60 s of last update
    const existing = ocgStore.getByClient(id);
    if (existing) {
      const ageMs = Date.now() - new Date(existing.updatedAt).getTime();
      if (ageMs < 60_000) {
        return reply.status(429).send({ error: "OCG was just updated. Please wait before re-ingesting." });
      }
    }

    let title = "Outside Counsel Guidelines";
    let text = "";

    const ct = req.headers["content-type"] ?? "";
    if (ct.includes("multipart/form-data")) {
      const parts: Array<import("@fastify/multipart").MultipartFile> = [];
      for await (const part of req.parts()) {
        if ("file" in part) {
          parts.push(part as import("@fastify/multipart").MultipartFile);
        } else {
          const field = part as { fieldname: string; value: string };
          if (field.fieldname === "title") title = String(field.value).trim() || title;
        }
      }
      if (!parts.length) return reply.status(400).send({ error: "No file uploaded" });
      const buf = await parts[0].toBuffer();
      const { samples } = await extractWritingSamples(buf, parts[0].filename ?? "ocg");
      text = samples.join("\n\n");
    } else {
      const body = req.body as { title?: string; text?: string };
      if (body.title) title = String(body.title).trim() || title;
      text = String(body.text ?? "").trim();
    }

    if (!text) return reply.status(400).send({ error: "OCG text is required" });

    try {
      const ocgDoc = await ocgStore.ingest(id, title, text);
      await orchestrator.clients.setOcg(id, ocgDoc.id);
      auditLogger.write({ event: "client.ocg.ingested", actorId: actorOf(req), data: { clientId: id, ruleCount: ocgDoc.rules.length } });
      return reply.status(200).send({ ocg: ocgDoc, ruleCount: ocgDoc.rules.length });
    } catch (err) {
      logger.error("OCG ingestion failed", { clientId: id, error: (err as Error).message });
      return reply.status(500).send({ error: "OCG ingestion failed. Please try again." });
    }
  });

  /** GET /clients/:id/ocg — retrieve the OCG document for a client. */
  app.get("/clients/:id/ocg", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const ocgDoc = ocgStore.getByClient(id);
    if (!ocgDoc) return reply.status(404).send({ error: "No OCG document found for this client" });
    return ocgDoc;
  });

  /** DELETE /clients/:id/ocg — remove the OCG document for a client. */
  app.delete("/clients/:id/ocg", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      await ocgStore.remove(id);
      await orchestrator.clients.clearOcg(id);
      auditLogger.write({ event: "client.ocg.deleted", actorId: actorOf(req), data: { clientId: id } });
      return reply.status(200).send({ ok: true });
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  // ── Client voice guide ────────────────────────────────────────────────────────

  /** POST /clients/:id/voice-guide/import — build a ClientVoiceGuide from writing samples. */
  app.post("/clients/:id/voice-guide/import", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const client = orchestrator.clients.get(id);
    if (!client) return reply.status(404).send({ error: "Client not found" });

    // Rate-limit: 60 s since last voice guide generation
    if (client.voiceGuide?.generatedAt) {
      const ageMs = Date.now() - new Date(client.voiceGuide.generatedAt).getTime();
      if (ageMs < 60_000) {
        return reply.status(429).send({ error: "Voice guide was just generated. Please wait before re-importing." });
      }
    }

    let buf: Buffer;
    let filename = "samples";
    try {
      const data = await req.file();
      if (!data) return reply.status(400).send({ error: "No file uploaded" });
      filename = data.filename ?? filename;
      buf = await data.toBuffer();
    } catch {
      return reply.status(400).send({ error: "File upload failed" });
    }

    try {
      const { samples } = await extractWritingSamples(buf, filename);
      if (!samples.length) return reply.status(422).send({ error: "No text samples found in uploaded file" });
      const guide = await analyzeClientVoice(samples, id);
      await orchestrator.clients.setVoiceGuide(id, guide);
      auditLogger.write({ event: "client.voiceguide.imported", actorId: actorOf(req), data: { clientId: id, sampleCount: samples.length } });
      return reply.status(200).send({ voiceGuide: guide, samplesAnalysed: samples.length });
    } catch (err) {
      logger.error("Client voice analysis failed", { clientId: id, error: (err as Error).message });
      return reply.status(500).send({ error: "Voice guide generation failed. Please try again." });
    }
  });

  /** DELETE /clients/:id/voice-guide — clear the voice guide for a client. */
  app.delete("/clients/:id/voice-guide", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      await orchestrator.clients.clearVoiceGuide(id);
      auditLogger.write({ event: "client.voiceguide.cleared", actorId: actorOf(req), data: { clientId: id } });
      return reply.status(200).send({ ok: true });
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  // ── Matter budget tracking ───────────────────────────────────────────────────

  app.put("/clients/:id/matters/:matterNumber/budget", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id, matterNumber } = req.params as { id: string; matterNumber: string };
    const { budgetUsd, thresholds } = req.body as { budgetUsd: number; thresholds?: number[] };
    if (!budgetUsd || budgetUsd <= 0) return reply.status(400).send({ error: "budgetUsd must be positive" });
    if (thresholds !== undefined && (
      !Array.isArray(thresholds) ||
      !thresholds.every((t) => Number.isFinite(t) && t > 0 && t <= 1)
    )) {
      return reply.status(400).send({ error: "thresholds must be an array of numbers between 0 (exclusive) and 1 (inclusive)" });
    }
    const matter = orchestrator.clients.setMatterBudget(id, matterNumber, budgetUsd, thresholds);
    if (!matter) return reply.status(404).send({ error: "Client or matter not found" });
    return matter;
  });

  app.get("/clients/:id/matters/:matterNumber/budget", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber } = req.params as { id: string; matterNumber: string };
    const burn = orchestrator.budgetMonitor.getBurn(matterNumber);
    if (!burn) return reply.status(404).send({ error: "No budget set for this matter" });
    return { matterNumber, ...burn };
  });

  app.post("/clients/:id/matters/:matterNumber/budget/check", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber } = req.params as { id: string; matterNumber: string };
    orchestrator.budgetMonitor.checkMatter(matterNumber);
    return { ok: true };
  });

  const MAX_BUDGET_SSE_LISTENERS = 20;
  app.get("/budget/alerts/stream", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (orchestrator.budgetMonitor.listenerCount("alert") >= MAX_BUDGET_SSE_LISTENERS) {
      return reply.status(429).send({ error: "Too many concurrent budget streams" });
    }
    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();

    const send = (alert: import("../types.js").BudgetAlert) => {
      reply.raw.write(`data: ${JSON.stringify(alert)}\n\n`);
    };
    orchestrator.budgetMonitor.on("alert", send);
    req.raw.on("close", () => orchestrator.budgetMonitor.off("alert", send));
  });

  // ── Budget prediction ────────────────────────────────────────────────────────

  app.get("/matters/:matterNumber/budget-prediction", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber } = req.params as { matterNumber: string };
    const allTasks = orchestrator.listTasks();
    const taskMap = new Map(
      allTasks.flatMap((t) =>
        t.matterNumber ? [[t.matterNumber, t] as [string, import("../types.js").Task]] : []
      )
    );
    const prediction = orchestrator.budgetPredictor.predict(matterNumber, orchestrator.time, taskMap);
    if (!prediction) return reply.status(404).send({ error: "No billing data found for this matter" });
    return prediction;
  });

  // ── Conflict graph ───────────────────────────────────────────────────────────

  app.post("/graph/sync", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (!orchestrator.conflictGraph.isEnabled()) return reply.status(503).send({ error: "TypeDB not configured (set TYPEDB_URL)" });
    await orchestrator.conflictGraph.sync(orchestrator.clients, orchestrator.time);
    return { ok: true, message: "Conflict graph synced" };
  });

  app.get("/clients/:id/conflicts", async (req, reply) => {
    const user = getUser(req);
    if (!isPartner(user)) return reply.status(403).send({ error: "Partner role required" });
    if (!orchestrator.conflictGraph.isEnabled()) return reply.status(503).send({ error: "TypeDB not configured" });
    const { id } = req.params as { id: string };
    const conflicts = await orchestrator.conflictGraph.checkClient(id);
    return conflicts;
  });

  app.post("/clients/check-conflict-graph", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (!orchestrator.conflictGraph.isEnabled()) return reply.status(503).send({ error: "TypeDB not configured" });
    const { clientId, adversaryIds } = req.body as { clientId: string; adversaryIds: string[] };
    if (!clientId) return reply.status(400).send({ error: "clientId required" });
    const conflicts = await orchestrator.conflictGraph.checkNewMatter(clientId, adversaryIds ?? []);
    return { conflicts, hasConflict: conflicts.length > 0 };
  });

  // ── Regulatory pulse ──────────────────────────────────────────────────────────

  const MAX_REG_SSE_LISTENERS = 20;
  app.get("/regulatory/alerts/stream", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (!orchestrator.regPulse.isEnabled()) return reply.status(503).send({ error: "Regulatory pulse not enabled (set REG_PULSE_ENABLED=true and TAVILY_API_KEY)" });
    if (orchestrator.regPulse.listenerCount("alert") >= MAX_REG_SSE_LISTENERS) {
      return reply.status(429).send({ error: "Too many concurrent regulatory streams" });
    }
    reply.raw.setHeader("Content-Type", "text/event-stream");
    reply.raw.setHeader("Cache-Control", "no-cache");
    reply.raw.setHeader("Connection", "keep-alive");
    reply.raw.flushHeaders();

    const send = (alert: import("../types.js").RegulationAlert) => {
      reply.raw.write(`data: ${JSON.stringify(alert)}\n\n`);
    };
    orchestrator.regPulse.on("alert", send);
    req.raw.on("close", () => orchestrator.regPulse.off("alert", send));
  });

  app.post("/regulatory/check-now", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    if (!orchestrator.regPulse.isEnabled()) return reply.status(503).send({ error: "Regulatory pulse not enabled" });
    const tasks = orchestrator.listTasks();
    const alerts = await orchestrator.regPulse.checkAll(tasks);
    return { checked: tasks.length, alerts };
  });

  // ── Client status reports ─────────────────────────────────────────────────────

  app.post("/tasks/:id/status-report", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    const task = orchestrator.getTask(id);
    if (!task) return reply.status(404).send({ error: "Task not found" });
    if (!canViewTask(user, task)) return reply.status(403).send({ error: "Access denied" });
    const {
      format = "markdown",
      includeTimeEntries = true,
      includeBudgetBurn = true,
      includeOcgFlags = false,
      customNote,
    } = (req.body ?? {}) as Partial<StatusReportOptions>;
    if (format !== "html" && format !== "markdown") return reply.status(400).send({ error: "format must be html or markdown" });

    const timeEntries = includeTimeEntries
      ? orchestrator.time.list({ taskId: id })
      : [];

    // Resolve budget burn via budgetMonitor if available on the orchestrator
    const budgetBurn = includeBudgetBurn && task.matterNumber && orchestrator.budgetMonitor
      ? orchestrator.budgetMonitor.getBurn(task.matterNumber)
      : null;

    // Resolve the submitting lawyer's tone profile if available
    const assignedId = task.assignedLawyerIds?.[0] ?? task.createdByProfileId;
    const profile = assignedId ? orchestrator.profiles.get(assignedId) : null;

    const report = await generateStatusReport(
      task,
      timeEntries,
      budgetBurn ?? undefined,
      { taskId: id, format, includeTimeEntries, includeBudgetBurn, includeOcgFlags, customNote },
      profile ?? undefined,
    );

    if (format === "html") {
      reply.header("Content-Type", "text/html; charset=utf-8");
      return reply.send(report.content);
    }
    return report;
  });

  // ── Deadline calculator ───────────────────────────────────────────────────────

  app.get("/deadlines/rules", async (_req, _reply) => {
    return orchestrator.deadlines.listJurisdictions();
  });

  app.post("/deadlines/compute", async (req, reply) => {
    const { jurisdiction, triggerEvent, triggerDate } = req.body as {
      jurisdiction: string; triggerEvent: string; triggerDate: string;
    };
    if (!jurisdiction || !triggerEvent || !triggerDate) {
      return reply.status(400).send({ error: "jurisdiction, triggerEvent, triggerDate required" });
    }
    if (isNaN(Date.parse(triggerDate))) {
      return reply.status(400).send({ error: "triggerDate must be a valid ISO date string" });
    }
    try {
      return orchestrator.deadlines.compute(jurisdiction, triggerEvent, triggerDate);
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  app.post("/matters/:matterNumber/deadlines", async (req, reply) => {
    const { matterNumber } = req.params as { matterNumber: string };
    const { triggerEvent, triggerDate } = req.body as { triggerEvent: string; triggerDate: string };
    if (!triggerEvent || !triggerDate) return reply.status(400).send({ error: "triggerEvent and triggerDate required" });
    // Find this matter's jurisdiction from any associated task
    const tasks = orchestrator.listTasks();
    const task = tasks.find((t) => t.matterNumber === matterNumber);
    const jurisdiction = task?.jurisdiction;
    if (!jurisdiction) return reply.status(404).send({ error: "No task with jurisdiction found for this matter" });
    if (isNaN(Date.parse(triggerDate))) return reply.status(400).send({ error: "triggerDate must be a valid ISO date string" });
    try {
      return orchestrator.deadlines.compute(jurisdiction, triggerEvent, triggerDate);
    } catch (err) {
      return reply.status(404).send({ error: (err as Error).message });
    }
  });

  // ── Citation validity (KeyCite / Shepard's replacement) ─────────────────────

  app.get("/citations/check", async (req, reply) => {
    const { q, taskId } = req.query as { q?: string; taskId?: string };
    if (!q) return reply.status(400).send({ error: "q (citation string) required" });
    return orchestrator.citations.check(q, taskId);
  });

  app.post("/citations/check", async (req, reply) => {
    const { query, taskId } = req.body as { query?: string; taskId?: string };
    if (!query) return reply.status(400).send({ error: "query required" });
    return orchestrator.citations.check(query, taskId);
  });

  // ── Matter health (Clio Insights replacement) ─────────────────────────────

  app.get("/matters/:matterNumber/health", async (req, reply) => {
    const { matterNumber } = req.params as { matterNumber: string };
    const user = getUser(req);
    const visibleTasks = orchestrator.listTasks().filter((t) =>
      t.matterNumber === matterNumber && canViewTask(user, t),
    );
    if (visibleTasks.length === 0) return reply.status(404).send({ error: "Matter not found" });
    return orchestrator.matterHealth.compute(
      matterNumber, visibleTasks, orchestrator.time, orchestrator.budgetMonitor,
    );
  });

  app.get("/analytics/portfolio-health", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const tasks = orchestrator.listTasks();
    const allMatters = Array.from(
      new Set(tasks.map((t) => t.matterNumber).filter(Boolean) as string[]),
    );
    if (allMatters.length === 0) return { totalMatters: 0, green: 0, amber: 0, red: 0, matters: [], computedAt: new Date().toISOString() };
    return orchestrator.matterHealth.portfolio(
      allMatters, tasks, orchestrator.time, orchestrator.budgetMonitor,
    );
  });

  // ── Playbook (Contract Express / Practical Law replacement) ──────────────

  // Playbooks hold confidential negotiation positions and absolute red lines
  // (client, matter, and per-lawyer tiers). Restrict reads to partners, matching
  // the partner-only guard already on /playbooks/build and DELETE /playbooks/:id.
  app.get("/playbooks", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { scope, ownerId, practiceArea } = req.query as { scope?: string; ownerId?: string; practiceArea?: string };
    return orchestrator.playbookStore.list({
      scope: scope as import("../playbook/index.js").PlaybookScope | undefined,
      ownerId,
      practiceArea,
    });
  });

  app.get("/playbooks/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const pb = orchestrator.playbookStore.getById(id);
    if (!pb) return reply.status(404).send({ error: "Playbook not found" });
    return pb;
  });

  app.post("/playbooks/build", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const {
      scope, ownerId, ownerName, practiceArea, jurisdiction, name, description, clauseTypes, taskId,
    } = req.body as {
      scope?: string; ownerId?: string; ownerName?: string; practiceArea?: string;
      jurisdiction?: string; name?: string; description?: string;
      clauseTypes?: string[]; taskId?: string;
    };
    if (!practiceArea || !name) return reply.status(400).send({ error: "practiceArea and name required" });
    const validScopes = ["firm", "client", "matter", "personal"];
    const resolvedScope = (scope && validScopes.includes(scope) ? scope : "firm") as import("../playbook/index.js").PlaybookScope;
    const pb = await orchestrator.playbookBuilder.build(
      orchestrator.knowledge,
      orchestrator.playbookStore,
      { scope: resolvedScope, ownerId, ownerName, practiceArea, jurisdiction, name, description, clauseTypes, taskId },
    );
    return reply.status(201).send(pb);
  });

  app.get("/playbooks/resolve/:clauseType", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { clauseType } = req.params as { clauseType: string };
    const { practiceArea, matterNumber, clientId, profileId } = req.query as {
      practiceArea?: string; matterNumber?: string; clientId?: string; profileId?: string;
    };
    if (clauseType === "*") {
      return orchestrator.playbookStore.resolveAll({ practiceArea, matterNumber, clientId, profileId });
    }
    const resolved = orchestrator.playbookStore.resolve(clauseType, { practiceArea, matterNumber, clientId, profileId });
    if (!resolved) return { clauseType, resolved: null, message: "No playbook entry found for this clause type" };
    return resolved;
  });

  app.delete("/playbooks/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const deleted = orchestrator.playbookStore.delete(id);
    if (!deleted) return reply.status(404).send({ error: "Playbook not found" });
    return { deleted: true };
  });

  // ── Invoice validation (reverse-OCG; in-house billing killer) ────────────

  app.post("/invoices/validate", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const {
      invoiceText, clientId, submittedByFirm, matterNumber, generateDisputeLetter, taskId,
    } = req.body as {
      invoiceText?: string; clientId?: string; submittedByFirm?: string;
      matterNumber?: string; generateDisputeLetter?: boolean; taskId?: string;
    };
    if (!invoiceText && !clientId) {
      return reply.status(400).send({ error: "invoiceText required" });
    }
    const ocgDoc = clientId ? orchestrator.ocg.getByClient(clientId) : null;
    return orchestrator.invoiceValidator.validate(
      invoiceText ?? "",
      undefined,
      ocgDoc ?? null,
      { clientId, submittedByFirm, matterNumber, generateDisputeLetter: generateDisputeLetter ?? false, taskId },
    );
  });

  app.post("/invoices/upload", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const user = getUser(req);
    const parts: Array<import("@fastify/multipart").MultipartFile | import("@fastify/multipart").MultipartValue> = [];
    const data = await req.file();
    if (!data) return reply.status(400).send({ error: "No file uploaded" });
    const buf = await data.toBuffer();
    const invoiceText = buf.toString("utf8");

    const { clientId, submittedByFirm, matterNumber, generateDisputeLetter, taskId } = req.query as {
      clientId?: string; submittedByFirm?: string; matterNumber?: string;
      generateDisputeLetter?: string; taskId?: string;
    };
    const ocgDoc = clientId ? orchestrator.ocg.getByClient(clientId) : null;
    return orchestrator.invoiceValidator.validate(
      invoiceText,
      undefined,
      ocgDoc ?? null,
      {
        clientId, submittedByFirm, matterNumber,
        generateDisputeLetter: generateDisputeLetter === "true",
        taskId,
      },
    );
  });

  // ── Contract redline (Definely / Kira / manual markup replacement) ──────────

  app.post("/redline", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const {
      documentText, practiceArea, jurisdiction, matterNumber, clientId,
      profileId, documentId, documentTitle, taskId,
    } = req.body as {
      documentText: string; practiceArea?: string; jurisdiction?: string;
      matterNumber?: string; clientId?: string; profileId?: string;
      documentId?: string; documentTitle?: string; taskId?: string;
    };
    if (!documentText) return reply.status(400).send({ error: "documentText required" });
    return orchestrator.redline.redline(documentText, orchestrator.playbookStore, {
      practiceArea, jurisdiction, matterNumber, clientId, profileId,
      documentId, documentTitle, taskId,
    });
  });

  // ── Headnote generator (Westlaw Key Numbers replacement) ─────────────────────

  app.post("/headnotes/generate", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const {
      opinionText, caseName, citation, court, dateFiled, jurisdiction, taskId,
    } = req.body as {
      opinionText: string; caseName?: string; citation?: string; court?: string;
      dateFiled?: string; jurisdiction?: string; taskId?: string;
    };
    if (!opinionText) return reply.status(400).send({ error: "opinionText required" });
    return orchestrator.headnotes.generate(opinionText, {
      caseName, citation, court, dateFiled, jurisdiction, taskId,
    });
  });

  // ── Client intelligence briefing (Clio Grow / CRM replacement) ────────────

  app.get("/clients/:id/briefing", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const { briefingDate, industryContext, taskId } = req.query as {
      briefingDate?: string; industryContext?: string; taskId?: string;
    };
    const clientRecord = orchestrator.clients.get(id) ?? orchestrator.clients.getByClientNumber(id);
    if (!clientRecord) return reply.status(404).send({ error: "Client not found" });
    const allTasks = orchestrator.listTasks();
    const allEntries = await orchestrator.time.list({});
    return orchestrator.briefing.generate(
      clientRecord, allTasks, allEntries as import("../types.js").TimeEntry[],
      { knowledge: orchestrator.knowledge, briefingDate, industryContext, taskId },
    );
  });

  // ── Precedent document generator (Practical Law / PSL replacement) ────────

  app.post("/precedents/generate", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const {
      documentType, practiceArea, jurisdiction, actingFor, matterNumber,
      clientId, profileId, specialInstructions, taskId,
    } = req.body as {
      documentType: string; practiceArea?: string; jurisdiction?: string;
      actingFor?: string; matterNumber?: string; clientId?: string;
      profileId?: string; specialInstructions?: string; taskId?: string;
    };
    if (!documentType) return reply.status(400).send({ error: "documentType required" });
    return orchestrator.precedents.generate(
      documentType as import("../precedent/generator.js").PrecedentDocumentType,
      orchestrator.knowledge,
      orchestrator.playbookStore,
      { practiceArea, jurisdiction, actingFor, matterNumber, clientId, profileId, specialInstructions, taskId },
    );
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
      auditLogger.write({
        event: "settings.updated",
        actorId: actorOf(req),
        data: { fields: Object.keys(req.body as object) },
      });
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
    const { profileId, agentId, taskId, matterNumber, clientNumber, from, to, agentOnly } = req.query as Record<string, string>;
    const filter = {
      // Lawyers are restricted to their own entries; partners may filter by any profileId.
      profileId: isPartner(user) ? (profileId || undefined) : user?.profileId,
      agentId: isPartner(user) ? (agentId || undefined) : undefined,
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      clientNumber: clientNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
      agentOnly: agentOnly === "true" ? true : agentOnly === "false" ? false : undefined,
    };
    return orchestrator.time.list(filter);
  });

  /** GET /time-entries/agent-summary — per-agent billing totals. Partner only. */
  app.get("/time-entries/agent-summary", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { taskId, matterNumber, clientNumber, from, to } = req.query as Record<string, string>;
    const entries = orchestrator.time.list({
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      clientNumber: clientNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
      agentOnly: true,
    });
    const byAgent = new Map<string, { agentId: string; agentName: string; entries: number; billingUnits: number; billingAmountUsd: number }>();
    for (const e of entries) {
      if (!e.agentId) continue;
      const key = e.agentId;
      const existing = byAgent.get(key) ?? { agentId: e.agentId, agentName: e.agentName ?? e.agentId, entries: 0, billingUnits: 0, billingAmountUsd: 0 };
      existing.entries++;
      existing.billingUnits += e.billingUnits;
      existing.billingAmountUsd += e.billingAmountUsd ?? 0;
      byAgent.set(key, existing);
    }
    return Array.from(byAgent.values()).sort((a, b) => b.billingAmountUsd - a.billingAmountUsd);
  });

  app.get("/time-entries/export.json", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { profileId, agentId, taskId, matterNumber, from, to, agentOnly } = req.query as Record<string, string>;
    const filter = {
      profileId: profileId || undefined,
      agentId: agentId || undefined,
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
      agentOnly: agentOnly === "true" ? true : agentOnly === "false" ? false : undefined,
    };
    return orchestrator.time.exportJson(filter);
  });

  app.get("/time-entries/export.csv", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { profileId, agentId, taskId, matterNumber, from, to, agentOnly } = req.query as Record<string, string>;
    const filter = {
      profileId: profileId || undefined,
      agentId: agentId || undefined,
      taskId: taskId || undefined,
      matterNumber: matterNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
      agentOnly: agentOnly === "true" ? true : agentOnly === "false" ? false : undefined,
    };
    reply.header("Content-Type", "text/csv; charset=utf-8");
    reply.header("Content-Disposition", "attachment; filename=\"time-entries.csv\"");
    return orchestrator.time.exportCsv(filter);
  });

  app.get("/time-entries/export.ledes", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber, clientNumber, from, to, invoiceNumber } = req.query as Record<string, string>;
    if (!matterNumber && !clientNumber) return reply.status(400).send({ error: "matterNumber or clientNumber required" });
    const entries = orchestrator.time.list({
      matterNumber: matterNumber || undefined,
      clientNumber: clientNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
    }).filter((e) => e.endedAt);
    const invoice = invoiceNumber || `${matterNumber ?? clientNumber}-${new Date().toISOString().slice(0, 10)}`;
    const ledes = exportLedes1998B(entries, { invoiceNumber: invoice });
    reply.header("Content-Type", "application/edi-x12");
    const safeFilename = invoice.replace(/[^\w\-\.]/g, "_");
    reply.header("Content-Disposition", `attachment; filename="${safeFilename}.ledes"`);
    return ledes;
  });

  // ── OCG time-entry compliance ─────────────────────────────────────────────────

  app.get("/time-entries/suggestions", async (req) => {
    const user = getUser(req);
    const { profileId, clientNumber, matterNumber } = req.query as Record<string, string>;
    const filter = {
      profileId: isPartner(user) ? (profileId || undefined) : user?.profileId,
      clientNumber: clientNumber || undefined,
      matterNumber: matterNumber || undefined,
    };
    return orchestrator.time.listWithSuggestions(filter);
  });

  app.post("/time-entries/run-ocg-check", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { clientNumber, matterNumber, limit } = req.body as { clientNumber?: string; matterNumber?: string; limit?: number };
    const cap = Math.min(limit ?? 100, 500);

    const allEntries = orchestrator.time.list({
      clientNumber: clientNumber || undefined,
      matterNumber: matterNumber || undefined,
    }).slice(0, cap);

    const byClientId = new Map<string, typeof allEntries>();
    for (const entry of allEntries) {
      if (!entry.clientNumber) continue;
      const client = orchestrator.clients.getByClientNumber(entry.clientNumber);
      if (!client) continue;
      const ocgDoc = ocgStore.getByClient(client.id);
      if (!ocgDoc) continue;
      const arr = byClientId.get(client.id) ?? [];
      arr.push(entry);
      byClientId.set(client.id, arr);
    }

    let checked = 0;
    let withSuggestions = 0;

    for (const [clientId, entries] of byClientId) {
      const ocgDoc = ocgStore.getByClient(clientId);
      if (!ocgDoc) continue;
      try {
        const suggestions = await ocgStore.checkEntries(entries, ocgDoc);
        for (const [entryId, sug] of suggestions) {
          orchestrator.time.setSuggestions(entryId, sug);
          if (sug.length) withSuggestions++;
        }
        checked += entries.length;
      } catch (err) {
        logger.warn("OCG check batch failed", { clientId, error: (err as Error).message });
      }
    }

    return { checked, withSuggestions };
  });

  app.post("/time-entries/:id/suggestions/accept", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    const { ruleId } = req.body as { ruleId: string };
    if (!ruleId) return reply.status(400).send({ error: "ruleId is required" });

    const entries = orchestrator.time.list();
    const entry = entries.find((e) => e.id === id);
    if (!entry) return reply.status(404).send({ error: "Time entry not found" });
    if (!isPartner(user) && user?.profileId !== entry.profileId) {
      return reply.status(403).send({ error: "Access denied" });
    }

    const updated = orchestrator.time.acceptSuggestion(id, ruleId);
    if (!updated) return reply.status(404).send({ error: "Suggestion not found" });
    if (entry.clientNumber) {
      const client = orchestrator.clients.list().find((c) => c.clientNumber === entry.clientNumber);
      if (client) ocgStore.recordOutcome(client.id, ruleId, "accepted");
    }
    return updated;
  });

  app.post("/time-entries/:id/suggestions/dismiss", async (req, reply) => {
    const user = getUser(req);
    const { id } = req.params as { id: string };
    const { ruleId } = req.body as { ruleId: string };
    if (!ruleId) return reply.status(400).send({ error: "ruleId is required" });

    const entries = orchestrator.time.list();
    const entry = entries.find((e) => e.id === id);
    if (!entry) return reply.status(404).send({ error: "Time entry not found" });
    if (!isPartner(user) && user?.profileId !== entry.profileId) {
      return reply.status(403).send({ error: "Access denied" });
    }

    const updated = orchestrator.time.dismissSuggestion(id, ruleId);
    if (!updated) return reply.status(404).send({ error: "Suggestion not found" });
    if (entry.clientNumber) {
      const client = orchestrator.clients.list().find((c) => c.clientNumber === entry.clientNumber);
      if (client) ocgStore.recordOutcome(client.id, ruleId, "dismissed");
    }
    return updated;
  });

  app.get("/clients/:id/ocg/stats", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const client = orchestrator.clients.get(id);
    if (!client) return reply.status(404).send({ error: "Client not found" });
    const stats = ocgStore.getStats(id);
    if (!stats) return reply.status(404).send({ error: "No OCG document for this client" });
    return stats;
  });

  // ── Pre-bill review workflow ──────────────────────────────────────────────────

  app.post("/pre-bills", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber, clientNumber, from, to } = req.body as {
      matterNumber: string; clientNumber?: string; from?: string; to?: string;
    };
    if (!matterNumber) return reply.status(400).send({ error: "matterNumber required" });
    const entries = orchestrator.time.list({
      matterNumber,
      clientNumber: clientNumber || undefined,
      from: from ? new Date(from) : undefined,
      to: to ? new Date(to) : undefined,
    }).filter((e) => e.endedAt);
    if (!entries.length) return reply.status(422).send({ error: "No closed entries found for this matter" });
    const bill = orchestrator.preBills.create(matterNumber, entries, actorOf(req), clientNumber);
    return reply.status(201).send(bill);
  });

  app.get("/pre-bills", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { matterNumber } = req.query as Record<string, string>;
    return orchestrator.preBills.list(matterNumber || undefined);
  });

  app.get("/pre-bills/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const bill = orchestrator.preBills.getById(id);
    if (!bill) return reply.status(404).send({ error: "Pre-bill not found" });
    return bill;
  });

  app.patch("/pre-bills/:id", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.status(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    const body = req.body as {
      status?: import("../types.js").PreBillStatus;
      notes?: string;
      entryEdit?: { entryId: string; description: string };
    };
    let bill = orchestrator.preBills.getById(id);
    if (!bill) return reply.status(404).send({ error: "Pre-bill not found" });
    if (body.entryEdit) {
      bill = orchestrator.preBills.updateEntryDescription(id, body.entryEdit.entryId, body.entryEdit.description) ?? bill;
    }
    if (body.notes !== undefined) {
      bill = orchestrator.preBills.setNotes(id, body.notes) ?? bill;
    }
    if (body.status) {
      const updated = orchestrator.preBills.transition(id, body.status);
      if (!updated) return reply.status(422).send({ error: `Invalid transition from ${bill.status} to ${body.status}` });
      bill = updated;
    }
    return bill;
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

  // ─── Twenty CRM ─────────────────────────────────────────────────────────────

  app.get("/auth/twenty/status", async () => twentyClient.status());

  /**
   * POST /clients/:id/sync-to-twenty
   *
   * Upsert a Big Michael client as a Twenty Company. Searches Twenty by exact
   * name first to avoid duplicates; creates if not found. Partner only.
   */
  app.post("/clients/:id/sync-to-twenty", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    if (!twentyClient.isConfigured()) return reply.code(503).send({ error: "Twenty not configured — set TWENTY_API_URL and TWENTY_API_KEY" });
    const { id } = req.params as { id: string };
    const client = orchestrator.clients.get(id);
    if (!client) return reply.code(404).send({ error: "Client not found" });
    try {
      const company = await twentyClient.upsertClientAsCompany(client);
      return { ok: true, twentyCompanyId: company.id, name: company.name };
    } catch (err) {
      return reply.code(502).send({ error: (err as Error).message });
    }
  });

  /**
   * POST /tasks/:id/push-to-twenty
   *
   * Push a completed task's synthesis output to Twenty as a Note on the
   * specified company. Requires the task to have a synthesis finding.
   * Partners and the assigned lawyer may push. Partner only if no assignment.
   */
  app.post("/tasks/:id/push-to-twenty", async (req, reply) => {
    if (!twentyClient.isConfigured()) return reply.code(503).send({ error: "Twenty not configured — set TWENTY_API_URL and TWENTY_API_KEY" });
    const user = getUser(req);
    const { id } = req.params as { id: string };
    const task = await orchestrator.backend.getTask(id);
    if (!task) return reply.code(404).send({ error: "Task not found" });
    if (!canViewTask(user, task)) return reply.code(403).send({ error: "Access denied" });

    const { twentyCompanyId } = req.body as { twentyCompanyId?: string };

    const synthesis = task.findings?.find((f) => f.type === "synthesis") ?? task.findings?.[task.findings.length - 1];
    const body = synthesis?.content ?? task.description;
    const title = `Big Michael: ${task.description.slice(0, 120)}`;

    try {
      const note = await twentyClient.createNote({
        title,
        body,
        companyId: twentyCompanyId,
      });
      return { ok: true, noteId: note.id, createdAt: note.createdAt };
    } catch (err) {
      return reply.code(502).send({ error: (err as Error).message });
    }
  });

  // ─── Job queue monitoring routes (partner only) ──────────────────────────────

  app.get("/jobs", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    const { status, limit, offset } = req.query as {
      status?: string;
      limit?: string;
      offset?: string;
    };
    return jobQueue.list({
      status: status as Parameters<typeof jobQueue.list>[0] extends { status?: infer S } ? S : never,
      limit: limit ? parseInt(limit) : 50,
      offset: offset ? parseInt(offset) : 0,
    });
  });

  app.get("/jobs/stats", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    return jobQueue.stats();
  });

  app.post("/jobs/:id/retry", async (req, reply) => {
    if (!isPartner(getUser(req))) return reply.code(403).send({ error: "Partner role required" });
    const { id } = req.params as { id: string };
    try {
      const job = await jobQueue.retry(id);
      return { ok: true, job };
    } catch (err) {
      return reply.code(404).send({ error: (err as Error).message });
    }
  });

  // ─── Start background worker ──────────────────────────────────────────────────

  startWorker(orchestrator);

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
      const roundNum = Number(args.round);
      if (!Number.isInteger(roundNum) || roundNum < 1) {
        throw new Error("round must be a positive integer");
      }
      const task = await backend.getTask(args.taskId as string);
      if (!task) throw new Error(`Task not found: ${args.taskId}`);
      const roundState = task.rounds[roundNum - 1];
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

    // ── Goliath killer tools ──────────────────────────────────────────────────
    case "check_citation": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("check_citation requires local backend");
      return orch.citations.check(args.query as string, args.taskId as string | undefined);
    }

    case "get_matter_health": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("get_matter_health requires local backend");
      const mn = args.matterNumber as string;
      const tasks = orch.listTasks().filter((t) => t.matterNumber === mn);
      return orch.matterHealth.compute(mn, tasks, orch.time, orch.budgetMonitor);
    }

    case "get_portfolio_health": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("get_portfolio_health requires local backend");
      const tasks = orch.listTasks();
      const matters = Array.from(new Set(tasks.map((t) => t.matterNumber).filter(Boolean) as string[]));
      if (matters.length === 0) return { totalMatters: 0, green: 0, amber: 0, red: 0, matters: [], computedAt: new Date().toISOString() };
      return orch.matterHealth.portfolio(matters, tasks, orch.time, orch.budgetMonitor);
    }

    case "build_playbook": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("build_playbook requires local backend");
      const validScopes = ["firm", "client", "matter", "personal"];
      const scope = (validScopes.includes(args.scope as string) ? args.scope : "firm") as import("../playbook/index.js").PlaybookScope;
      return orch.playbookBuilder.build(orch.knowledge, orch.playbookStore, {
        scope,
        ownerId: args.ownerId as string | undefined,
        ownerName: args.ownerName as string | undefined,
        practiceArea: args.practiceArea as string,
        jurisdiction: args.jurisdiction as string | undefined,
        name: args.name as string,
        clauseTypes: args.clauseTypes as string[] | undefined,
        taskId: args.taskId as string | undefined,
      });
    }

    case "query_playbook": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("query_playbook requires local backend");
      const clauseType = args.clauseType as string;
      const opts = {
        practiceArea: args.practiceArea as string | undefined,
        matterNumber: args.matterNumber as string | undefined,
        clientId: args.clientId as string | undefined,
        profileId: args.profileId as string | undefined,
      };
      if (clauseType === "*") return orch.playbookStore.resolveAll(opts);
      return orch.playbookStore.resolve(clauseType, opts) ?? { clauseType, resolved: null };
    }

    case "validate_invoice": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("validate_invoice requires local backend");
      const clientId = args.clientId as string | undefined;
      const ocgDoc = clientId ? orch.ocg.getByClient(clientId) : null;
      return orch.invoiceValidator.validate(
        args.invoiceText as string ?? "",
        undefined,
        ocgDoc ?? null,
        {
          clientId,
          submittedByFirm: args.submittedByFirm as string | undefined,
          matterNumber: args.matterNumber as string | undefined,
          generateDisputeLetter: args.generateDisputeLetter as boolean | undefined,
          taskId: args.taskId as string | undefined,
        },
      );
    }

    case "redline_contract": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("redline_contract requires local backend");
      return orch.redline.redline(
        args.documentText as string,
        orch.playbookStore,
        {
          practiceArea: args.practiceArea as string | undefined,
          jurisdiction: args.jurisdiction as string | undefined,
          matterNumber: args.matterNumber as string | undefined,
          clientId: args.clientId as string | undefined,
          profileId: args.profileId as string | undefined,
          documentId: args.documentId as string | undefined,
          documentTitle: args.documentTitle as string | undefined,
          taskId: args.taskId as string | undefined,
        },
      );
    }

    case "generate_headnotes": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("generate_headnotes requires local backend");
      return orch.headnotes.generate(args.opinionText as string, {
        caseName: args.caseName as string | undefined,
        citation: args.citation as string | undefined,
        court: args.court as string | undefined,
        dateFiled: args.dateFiled as string | undefined,
        jurisdiction: args.jurisdiction as string | undefined,
        taskId: args.taskId as string | undefined,
      });
    }

    case "get_client_briefing": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("get_client_briefing requires local backend");
      const clientId = args.clientId as string | undefined;
      const clientNumber = args.clientNumber as string | undefined;
      const clientRecord = clientId
        ? orch.clients.get(clientId)
        : clientNumber ? orch.clients.getByClientNumber(clientNumber) : undefined;
      if (!clientRecord) throw new Error("Client not found — provide a valid clientId or clientNumber");
      const allTasks = orch.listTasks();
      const allEntries = await orch.time.list({});
      return orch.briefing.generate(clientRecord, allTasks, allEntries as import("../types.js").TimeEntry[], {
        knowledge: orch.knowledge,
        briefingDate: args.briefingDate as string | undefined,
        industryContext: args.industryContext as string | undefined,
        taskId: args.taskId as string | undefined,
      });
    }

    case "generate_precedent": {
      const orch = (backend as import("../backend/index.js").LocalBackend).orchestrator;
      if (!orch) throw new Error("generate_precedent requires local backend");
      return orch.precedents.generate(
        args.documentType as import("../precedent/generator.js").PrecedentDocumentType,
        orch.knowledge,
        orch.playbookStore,
        {
          practiceArea: args.practiceArea as string | undefined,
          jurisdiction: args.jurisdiction as string | undefined,
          actingFor: args.actingFor as string | undefined,
          matterNumber: args.matterNumber as string | undefined,
          clientId: args.clientId as string | undefined,
          profileId: args.profileId as string | undefined,
          specialInstructions: args.specialInstructions as string | undefined,
          taskId: args.taskId as string | undefined,
        },
      );
    }

    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}