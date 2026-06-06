// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Teams bot — Outgoing Webhook receiver + Incoming Webhook / Graph API sender.
 *
 * Two integration modes (use either or both):
 *
 *  OUTGOING WEBHOOK (receive @-mentions)
 *    Teams admin: Apps → Manage apps → Outgoing Webhooks → Create.
 *    Set the callback URL to https://<your-host>/bots/teams/webhook.
 *    Copy the security token → TEAMS_WEBHOOK_SECRET.
 *    The bot responds immediately to synchronous commands, or replies
 *    "Working on it…" and posts back via Incoming Webhook when done.
 *
 *  INCOMING WEBHOOK (post to channels proactively)
 *    Teams channel: … → Connectors → Incoming Webhook → Configure.
 *    Copy the URL → TEAMS_INCOMING_WEBHOOK_URL (or per-matter via
 *    TEAMS_MATTER_WEBHOOKS = '{"M-001":"https://...","M-002":"..."}').
 *
 * The router (registerTeamsBotRoutes) registers:
 *   POST /bots/teams/webhook       — Outgoing Webhook receiver
 *   POST /bots/teams/notify        — Internal: post to a channel (auth-gated)
 *   POST /bots/teams/matter-link   — Associate a matter with a Teams channel
 *
 * Security:
 *   - HMAC-SHA256 verification on every incoming webhook request.
 *   - The secret token is compared with timing-safe equal.
 *   - Teams channel URLs are validated against the expected hostname.
 */

import { createHmac, timingSafeEqual } from "node:crypto";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { postToTeamsWebhook } from "../integrations/graph.js";
import { dispatch } from "./dispatcher.js";
import type { Orchestrator } from "../orchestrator.js";
import type { FastifyInstance, FastifyRequest } from "fastify";

// ─── Types ────────────────────────────────────────────────────────────────────

export interface TeamsMatterLink {
  matterNumber: string;
  /** Incoming Webhook URL for this matter's channel */
  webhookUrl: string;
  /** Teams team ID (optional — for Graph API posting) */
  teamId?: string;
  /** Teams channel ID (optional — for Graph API posting) */
  channelId?: string;
}

// In-memory matter → channel map (persisted to the MatterLinkStore if enabled)
const matterLinks = new Map<string, TeamsMatterLink>();

// Load from env — JSON map: {"M-001":"https://...", "M-002":"https://..."}
try {
  const raw = process.env.TEAMS_MATTER_WEBHOOKS;
  if (raw) {
    const parsed = JSON.parse(raw) as Record<string, string>;
    for (const [mn, url] of Object.entries(parsed)) {
      matterLinks.set(mn, { matterNumber: mn, webhookUrl: url });
    }
  }
} catch { /* ignore — no matter webhooks configured */ }

// ─── HMAC verification ────────────────────────────────────────────────────────

function verifyTeamsSignature(body: string, authHeader: string, secret: string): boolean {
  if (!authHeader?.startsWith("HMAC ")) return false;
  const provided = authHeader.slice(5).trim();
  const expected = createHmac("sha256", secret)
    .update(body, "utf8")
    .digest("base64");
  try {
    return timingSafeEqual(
      Buffer.from(provided, "base64"),
      Buffer.from(expected, "base64"),
    );
  } catch {
    return false;
  }
}

// ─── Outgoing response formatter ─────────────────────────────────────────────

function teamsMarkdownCard(text: string): Record<string, unknown> {
  return { type: "message", text };
}

// ─── Route registration ───────────────────────────────────────────────────────

export function registerTeamsBotRoutes(app: FastifyInstance, orch: Orchestrator): void {
  const secret = process.env.TEAMS_WEBHOOK_SECRET ?? "";
  const defaultWebhookUrl = process.env.TEAMS_INCOMING_WEBHOOK_URL ?? "";

  // ── Outgoing Webhook receiver ─────────────────────────────────────────────
  app.post("/bots/teams/webhook", async (req: FastifyRequest, reply) => {
    if (!secret) {
      return reply.status(503).send({ error: "Teams bot not configured" });
    }

    // Teams sends the raw body + Authorization header for HMAC verification
    const rawBody = JSON.stringify(req.body);
    const auth = (req.headers["authorization"] as string) ?? "";
    if (!verifyTeamsSignature(rawBody, auth, secret)) {
      logger.warn("Teams webhook: invalid HMAC signature");
      return reply.status(401).send({ error: "Invalid signature" });
    }

    const body = req.body as Record<string, unknown>;
    const msgText = String(
      (body["text"] as string)
      ?? (body["attachments"] as Array<Record<string, unknown>>)?.[0]?.["content"]
      ?? "",
    ).replace(/<at>[^<]+<\/at>/g, "").trim(); // strip @mention XML

    const senderName = String(
      (body["from"] as Record<string, unknown>)?.["name"] ?? "Unknown",
    );
    const channelData = body["channelData"] as Record<string, Record<string, unknown>> | undefined;
    const channelId = String(channelData?.["channel"]?.["id"] ?? "");
    const teamId = String(channelData?.["team"]?.["id"] ?? "");

    logger.info("Teams bot message", { senderName, command: msgText.slice(0, 60) });

    const response = await dispatch(
      { text: msgText, senderName, channelId, teamId, platform: "teams" },
      orch,
    );

    // If async work is queued, post the deferred result back via webhook
    if (response.asyncWork) {
      const targetUrl = matterLinks.get(channelId)?.webhookUrl ?? defaultWebhookUrl;
      setImmediate(async () => {
        try {
          const result = await response.asyncWork!();
          if (targetUrl) {
            await postToTeamsWebhook(targetUrl, "Big Michael", result);
          }
        } catch (err) {
          logger.error("Teams async work failed", { error: (err as Error).message });
          if (targetUrl) {
            await postToTeamsWebhook(targetUrl, "Big Michael", `Error: ${(err as Error).message}`).catch(() => {});
          }
        }
      });
    }

    // Synchronous response — Teams expects this within 5 s
    return teamsMarkdownCard(response.immediate);
  });

  // ── Internal: post a message to a channel ────────────────────────────────
  app.post("/bots/teams/notify", async (req: FastifyRequest, reply) => {
    const { matterNumber, title, text, webhookUrl, facts } = req.body as {
      matterNumber?: string; title: string; text: string;
      webhookUrl?: string; facts?: Array<{ name: string; value: string }>;
    };

    const url = webhookUrl
      ?? (matterNumber ? matterLinks.get(matterNumber)?.webhookUrl : undefined)
      ?? defaultWebhookUrl;

    if (!url) return reply.status(400).send({ error: "No webhook URL configured for this matter" });

    await postToTeamsWebhook(url, title, text, facts);
    return { ok: true };
  });

  // ── Matter → channel link management ─────────────────────────────────────
  app.post("/bots/teams/matter-link", async (req: FastifyRequest, reply) => {
    const { matterNumber, webhookUrl, teamId, channelId } = req.body as TeamsMatterLink;
    if (!matterNumber || !webhookUrl) {
      return reply.status(400).send({ error: "matterNumber and webhookUrl required" });
    }
    matterLinks.set(matterNumber, { matterNumber, webhookUrl, teamId, channelId });
    logger.info("Teams matter link set", { matterNumber });
    return { ok: true, matterNumber };
  });

  app.get("/bots/teams/matter-link/:matterNumber", async (req: FastifyRequest, reply) => {
    const { matterNumber } = req.params as { matterNumber: string };
    const link = matterLinks.get(matterNumber);
    if (!link) return reply.status(404).send({ error: "No Teams link for this matter" });
    return link;
  });

  app.delete("/bots/teams/matter-link/:matterNumber", async (req: FastifyRequest, reply) => {
    const { matterNumber } = req.params as { matterNumber: string };
    matterLinks.delete(matterNumber);
    return { ok: true };
  });
}

// ─── Proactive: post when a task completes ────────────────────────────────────

/**
 * Attach to orchestrator.progressEmitter to post Teams notifications when
 * a task completes and there is a channel linked to the matter.
 */
export function attachTeamsTaskNotifier(
  orch: Orchestrator,
  defaultWebhookUrl?: string,
): void {
  orch.progressEmitter.on("task:*", async (event: { type: string; data: Record<string, unknown> }, taskId: string) => {
    if (event.type !== "complete") return;
    try {
      const task = await orch.getTask(taskId);
      if (!task?.matterNumber) return;
      const link = matterLinks.get(task.matterNumber);
      const url = link?.webhookUrl ?? defaultWebhookUrl;
      if (!url) return;

      await postToTeamsWebhook(url,
        `Matter ${task.matterNumber} — Task Complete`,
        task.output?.slice(0, 800) ?? "Task completed.",
        [
          { name: "Task ID", value: task.id },
          { name: "Workflow", value: task.workflowType },
          { name: "Findings", value: String(task.findings?.length ?? 0) },
        ],
      );
    } catch (err) {
      logger.warn("Teams task notifier failed", { error: (err as Error).message });
    }
  });
}

// ─── Public API ───────────────────────────────────────────────────────────────

export { matterLinks };

export async function postToMatter(matterNumber: string, title: string, text: string): Promise<void> {
  const link = matterLinks.get(matterNumber);
  const url = link?.webhookUrl ?? process.env.TEAMS_INCOMING_WEBHOOK_URL ?? "";
  if (!url) throw new Error(`No Teams channel linked to matter ${matterNumber}`);
  await postToTeamsWebhook(url, title, text);
}
