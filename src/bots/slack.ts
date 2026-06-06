// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Slack bot — Events API receiver + Web API sender.
 *
 * Setup:
 *   1. Create a Slack App at https://api.slack.com/apps
 *   2. Add Bot Token Scopes: chat:write, channels:history, search:read,
 *      files:read, users:read
 *   3. Enable Event Subscriptions → Request URL: https://<host>/bots/slack/events
 *      Subscribe to bot events: app_mention, message.channels (optional)
 *   4. Install to workspace → copy Bot User OAuth Token → SLACK_BOT_TOKEN
 *   5. Copy Signing Secret → SLACK_SIGNING_SECRET
 *
 * Matter linking:
 *   SLACK_MATTER_CHANNELS = '{"M-001":"C0123ABCD","M-002":"C0456EFGH"}'
 *   Maps matter numbers to Slack channel IDs for proactive posting.
 *   Also manageable at runtime via POST /bots/slack/matter-link.
 *
 * The router registers:
 *   POST /bots/slack/events        — Events API receiver
 *   POST /bots/slack/notify        — Internal: post to a channel
 *   POST /bots/slack/matter-link   — Associate a matter with a channel
 *
 * Security:
 *   - Slack request signature verified (HMAC-SHA256 of v0:timestamp:body).
 *   - Replay protection: reject requests older than 5 minutes.
 *   - Bot token only in Authorization header, never in logs.
 */

import { createHmac, timingSafeEqual } from "node:crypto";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { dispatch } from "./dispatcher.js";
import type { Orchestrator } from "../orchestrator.js";
import type { FastifyInstance, FastifyRequest } from "fastify";

// ─── Matter → channel map ─────────────────────────────────────────────────────

export interface SlackMatterLink {
  matterNumber: string;
  channelId: string;
  /** Human-readable channel name (optional) */
  channelName?: string;
}

const matterLinks = new Map<string, SlackMatterLink>();

try {
  const raw = process.env.SLACK_MATTER_CHANNELS;
  if (raw) {
    const parsed = JSON.parse(raw) as Record<string, string>;
    for (const [mn, channelId] of Object.entries(parsed)) {
      matterLinks.set(mn, { matterNumber: mn, channelId });
    }
  }
} catch { /* ignore */ }

// ─── Signature verification ───────────────────────────────────────────────────

function verifySlackSignature(
  body: string,
  timestamp: string,
  signature: string,
  secret: string,
): boolean {
  const ts = Number(timestamp);
  if (Math.abs(Date.now() / 1000 - ts) > 300) return false; // replay protection

  const sigBase = `v0:${timestamp}:${body}`;
  const expected = "v0=" + createHmac("sha256", secret).update(sigBase, "utf8").digest("hex");
  try {
    return timingSafeEqual(
      Buffer.from(signature),
      Buffer.from(expected),
    );
  } catch {
    return false;
  }
}

// ─── Slack Web API helpers ─────────────────────────────────────────────────────

const SLACK_API = "https://slack.com/api";
const REQUEST_TIMEOUT_MS = 15_000;

async function slackApi(method: string, payload: Record<string, unknown>): Promise<Record<string, unknown>> {
  const token = process.env.SLACK_BOT_TOKEN ?? Config.connectors?.slack?.apiKey ?? "";
  if (!token) throw new Error("SLACK_BOT_TOKEN not configured");

  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(`${SLACK_API}/${method}`, {
      method: "POST",
      headers: {
        authorization: `Bearer ${token}`,
        "content-type": "application/json; charset=utf-8",
      },
      body: JSON.stringify(payload),
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    const data = (await res.json()) as Record<string, unknown>;
    if (!data["ok"]) throw new Error(`Slack API error: ${String(data["error"] ?? "unknown")}`);
    return data;
  } catch (err) { clearTimeout(timer); throw err; }
}

export async function postToSlackChannel(
  channelId: string,
  text: string,
  opts: { threadTs?: string; unfurlLinks?: boolean } = {},
): Promise<void> {
  await slackApi("chat.postMessage", {
    channel: channelId,
    text,
    mrkdwn: true,
    unfurl_links: opts.unfurlLinks ?? false,
    ...(opts.threadTs ? { thread_ts: opts.threadTs } : {}),
  });
}

export async function searchSlack(
  query: string,
  opts: { count?: number } = {},
): Promise<Array<{ text: string; channel: string; ts: string; permalink: string }>> {
  try {
    const data = await slackApi("search.messages", {
      query,
      count: opts.count ?? 20,
      sort: "timestamp",
      sort_dir: "desc",
    });
    const matches = (data["messages"] as Record<string, unknown>)?.["matches"] as Array<Record<string, unknown>>;
    if (!Array.isArray(matches)) return [];
    return matches.map((m) => ({
      text: String(m["text"] ?? "").slice(0, 400),
      channel: String((m["channel"] as Record<string, unknown>)?.["id"] ?? ""),
      ts: String(m["ts"] ?? ""),
      permalink: String(m["permalink"] ?? ""),
    }));
  } catch (err) {
    logger.warn("Slack search failed", { error: (err as Error).message });
    return [];
  }
}

// ─── Route registration ───────────────────────────────────────────────────────

export function registerSlackBotRoutes(app: FastifyInstance, orch: Orchestrator): void {
  const signingSecret = process.env.SLACK_SIGNING_SECRET ?? "";
  const defaultChannelId = process.env.SLACK_DEFAULT_CHANNEL ?? "";

  // ── Events API receiver ───────────────────────────────────────────────────
  app.post("/bots/slack/events", async (req: FastifyRequest, reply) => {
    if (!signingSecret) {
      return reply.status(503).send({ error: "Slack bot not configured" });
    }

    const rawBody = JSON.stringify(req.body);
    const timestamp = String(req.headers["x-slack-request-timestamp"] ?? "");
    const signature = String(req.headers["x-slack-signature"] ?? "");

    if (!verifySlackSignature(rawBody, timestamp, signature, signingSecret)) {
      logger.warn("Slack events: invalid signature");
      return reply.status(401).send({ error: "Invalid signature" });
    }

    const body = req.body as Record<string, unknown>;

    // URL verification challenge (Slack sends this once when you first configure the URL)
    if (body["type"] === "url_verification") {
      return { challenge: body["challenge"] };
    }

    if (body["type"] !== "event_callback") {
      return reply.status(200).send({ ok: true });
    }

    const event = body["event"] as Record<string, unknown>;

    // Only handle app_mention events (or direct messages)
    if (event["type"] !== "app_mention" && event["type"] !== "message") {
      return reply.status(200).send({ ok: true });
    }
    // Skip bot's own messages
    if (event["bot_id"] || event["subtype"] === "bot_message") {
      return reply.status(200).send({ ok: true });
    }

    const text = String(event["text"] ?? "").replace(/<@[A-Z0-9]+>/g, "").trim();
    const userId = String(event["user"] ?? "");
    const channelId = String(event["channel"] ?? "");
    const threadTs = String(event["thread_ts"] ?? event["ts"] ?? "");

    logger.info("Slack bot mention", { userId, command: text.slice(0, 60) });

    // Respond immediately (Slack requires acknowledgement within 3 s)
    reply.status(200).send({ ok: true });

    // Dispatch in background
    setImmediate(async () => {
      try {
        const response = await dispatch(
          { text, senderName: userId, channelId, platform: "slack" },
          orch,
        );
        await postToSlackChannel(channelId, response.immediate, { threadTs });

        if (response.asyncWork) {
          try {
            const result = await response.asyncWork();
            await postToSlackChannel(channelId, result, { threadTs });
          } catch (err) {
            await postToSlackChannel(channelId, `Error: ${(err as Error).message}`, { threadTs });
          }
        }
      } catch (err) {
        logger.error("Slack event dispatch failed", { error: (err as Error).message });
      }
    });
  });

  // ── Internal: post to a channel ───────────────────────────────────────────
  app.post("/bots/slack/notify", async (req: FastifyRequest, reply) => {
    const { matterNumber, channelId, text, threadTs } = req.body as {
      matterNumber?: string; channelId?: string; text: string; threadTs?: string;
    };

    const target = channelId
      ?? (matterNumber ? matterLinks.get(matterNumber)?.channelId : undefined)
      ?? defaultChannelId;

    if (!target) return reply.status(400).send({ error: "No channel configured" });

    await postToSlackChannel(target, text, { threadTs });
    return { ok: true };
  });

  // ── Matter → channel link management ─────────────────────────────────────
  app.post("/bots/slack/matter-link", async (req: FastifyRequest, reply) => {
    const { matterNumber, channelId, channelName } = req.body as SlackMatterLink;
    if (!matterNumber || !channelId) {
      return reply.status(400).send({ error: "matterNumber and channelId required" });
    }
    matterLinks.set(matterNumber, { matterNumber, channelId, channelName });
    logger.info("Slack matter link set", { matterNumber, channelId });
    return { ok: true, matterNumber };
  });

  app.get("/bots/slack/matter-link/:matterNumber", async (req: FastifyRequest, reply) => {
    const { matterNumber } = req.params as { matterNumber: string };
    const link = matterLinks.get(matterNumber);
    if (!link) return reply.status(404).send({ error: "No Slack channel linked to this matter" });
    return link;
  });

  app.delete("/bots/slack/matter-link/:matterNumber", async (req: FastifyRequest, reply) => {
    const { matterNumber } = req.params as { matterNumber: string };
    matterLinks.delete(matterNumber);
    return { ok: true };
  });
}

// ─── Proactive: post when a task completes ────────────────────────────────────

export function attachSlackTaskNotifier(
  orch: Orchestrator,
  defaultChannelId?: string,
): void {
  orch.progressEmitter.on("task:*", async (event: { type: string; data: Record<string, unknown> }, taskId: string) => {
    if (event.type !== "complete") return;
    try {
      const task = await orch.getTask(taskId);
      if (!task?.matterNumber) return;
      const link = matterLinks.get(task.matterNumber);
      const channelId = link?.channelId ?? defaultChannelId;
      if (!channelId) return;

      const text = [
        `*Matter ${task.matterNumber} — Task Complete* ✓`,
        task.output?.slice(0, 500) ?? "No output.",
        `_Task ID: \`${task.id}\` | Workflow: ${task.workflowType} | Findings: ${task.findings?.length ?? 0}_`,
      ].join("\n\n");

      await postToSlackChannel(channelId, text);
    } catch (err) {
      logger.warn("Slack task notifier failed", { error: (err as Error).message });
    }
  });
}

export { matterLinks };
