// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Microsoft Graph — SharePoint and Teams search.
 *
 * Extends the Graph client to cover the two surfaces that hold the other
 * half of a law firm's institutional knowledge:
 *
 *   SharePoint — document libraries, matter workspaces, precedent repositories.
 *   Teams      — channel conversations, meeting notes, matter-specific team chats.
 *
 * Used by:
 *   • Briefing swarm spokes (pull intel into the chalkboard)
 *   • Channel bot (post findings back to Teams channels)
 *   • Precedent search (find documents in SharePoint matter sites)
 *
 * Auth: reuses the GRAPH_* credentials already configured for email.
 * The same app registration needs additional API permissions:
 *   SharePoint — Sites.Read.All (application)
 *   Teams      — ChannelMessage.Read.All (application)
 *
 * All HTTP calls: 15 s timeout, 512 KB response cap, no credentials in logs.
 */

import { Config } from "../config.js";
import { logger } from "../logger.js";

const GRAPH_BASE = "https://graph.microsoft.com/v1.0";
const REQUEST_TIMEOUT_MS = 15_000;
const MAX_RESPONSE_BYTES = 512 * 1024;

// ─── Token cache (shared with email/client.ts via re-export) ─────────────────

let _cachedToken: { token: string; expiresAt: number } | null = null;
let _graphTokenPromise: Promise<string> | null = null;

export async function getGraphToken(): Promise<string> {
  const cfg = Config.email.graph;
  if (!cfg.enabled) throw new Error("Microsoft Graph not configured");

  if (cfg.accessToken) return cfg.accessToken;
  if (_cachedToken && Date.now() < _cachedToken.expiresAt - 60_000) return _cachedToken.token;

  // In-flight deduplication: only one token request at a time
  _graphTokenPromise ??= (async () => {
    const body = new URLSearchParams({
      client_id: cfg.clientId, client_secret: cfg.clientSecret,
      scope: "https://graph.microsoft.com/.default",
      grant_type: "client_credentials",
    });
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
    try {
      const res = await fetch(
        `https://login.microsoftonline.com/${cfg.tenantId}/oauth2/v2.0/token`,
        { method: "POST", headers: { "content-type": "application/x-www-form-urlencoded" }, body, signal: ctrl.signal },
      );
      clearTimeout(timer);
      if (!res.ok) throw new Error(`Graph token failed: ${res.status}`);
      const data = (await res.json()) as { access_token: string; expires_in: number };
      _cachedToken = { token: data.access_token, expiresAt: Date.now() + data.expires_in * 1000 };
      return data.access_token;
    } catch (err) { clearTimeout(timer); throw err; }
  })().finally(() => { _graphTokenPromise = null; });
  return _graphTokenPromise;
}

// ─── URL validation ───────────────────────────────────────────────────────────

/**
 * Validate that an absolute URL returned from a Graph @odata.nextLink is
 * actually a Graph URL. Prevents SSRF if a malicious response forges the link.
 */
export function validateGraphUrl(url: string): string {
  const parsed = new URL(url);
  if (parsed.hostname !== "graph.microsoft.com") {
    throw new Error(`Blocked non-Graph URL: ${parsed.hostname}`);
  }
  return url;
}

// ─── Shared fetch ─────────────────────────────────────────────────────────────

export async function graphFetch(path: string, token: string): Promise<unknown> {
  // Always build relative to GRAPH_BASE. For @odata.nextLink pagination, callers
  // must pass the result of validateGraphUrl() to prevent SSRF.
  const url = `${GRAPH_BASE}${path}`;
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(url, {
      headers: { authorization: `Bearer ${token}`, accept: "application/json", "Content-Type": "application/json" },
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    if (!res.ok) throw new Error(`Graph API error ${res.status}: ${path}`);
    const reader = res.body?.getReader();
    if (!reader) return res.json();
    let bytes = 0; const chunks: Uint8Array[] = [];
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      bytes += value.length;
      if (bytes > MAX_RESPONSE_BYTES) throw new Error("Graph response too large");
      chunks.push(value);
    }
    const merged = chunks.reduce((a, c) => { const n = new Uint8Array(a.length + c.length); n.set(a); n.set(c, a.length); return n; }, new Uint8Array(0));
    return JSON.parse(new TextDecoder().decode(merged));
  } catch (err) { clearTimeout(timer); throw err; }
}

export async function graphPost(path: string, token: string, body: unknown): Promise<unknown> {
  const url = `${GRAPH_BASE}${path}`;
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(url, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json", accept: "application/json" },
      body: JSON.stringify(body),
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    if (!res.ok) {
      const text = await res.text().catch(() => "");
      throw new Error(`Graph POST error ${res.status}: ${path} — ${text.slice(0, 200)}`);
    }
    const text = await res.text();
    return text ? JSON.parse(text) : null;
  } catch (err) { clearTimeout(timer); throw err; }
}

// ─── SharePoint ───────────────────────────────────────────────────────────────

export interface SharePointFile {
  id: string;
  name: string;
  webUrl: string;
  lastModified: string;
  size: number;
  siteId: string;
  siteName: string;
  matterRef?: string;
}

/**
 * Search across all SharePoint sites for files matching a query.
 * Uses Graph unified search (entityTypes: driveItem) — covers all sites
 * the app has Sites.Read.All access to.
 */
export async function searchSharePoint(
  query: string,
  opts: { maxResults?: number; daysBack?: number } = {},
): Promise<SharePointFile[]> {
  if (!Config.email.graph.enabled) return [];
  const token = await getGraphToken();
  const max = opts.maxResults ?? 20;

  try {
    const body = {
      requests: [{
        entityTypes: ["driveItem"],
        query: { queryString: query },
        from: 0,
        size: max,
        fields: ["id", "name", "webUrl", "lastModifiedDateTime", "size", "parentReference"],
      }],
    };
    const result = await graphPost("/search/query", token, body) as Record<string, unknown>;
    const hits = (result["value"] as Array<Record<string, unknown>>)?.[0]
      ?.["hitsContainers"] as Array<Record<string, unknown>>;
    if (!Array.isArray(hits)) return [];

    return hits.flatMap((hc) => {
      const items = hc["hits"] as Array<Record<string, unknown>>;
      return Array.isArray(items) ? items.map((h): SharePointFile => {
        const r = h["resource"] as Record<string, unknown>;
        const parent = r["parentReference"] as Record<string, unknown>;
        return {
          id: String(r["id"] ?? ""),
          name: String(r["name"] ?? ""),
          webUrl: String(r["webUrl"] ?? ""),
          lastModified: String(r["lastModifiedDateTime"] ?? ""),
          size: Number(r["size"] ?? 0),
          siteId: String(parent?.["siteId"] ?? ""),
          siteName: String(parent?.["siteName"] ?? ""),
          matterRef: extractMatterRef(String(r["name"] ?? "")),
        };
      }) : [];
    }).slice(0, max);
  } catch (err) {
    logger.warn("SharePoint search failed", { error: (err as Error).message });
    return [];
  }
}

// ─── Teams ────────────────────────────────────────────────────────────────────

export interface TeamsMessage {
  id: string;
  teamName: string;
  channelName: string;
  channelId: string;
  from: string;
  createdAt: string;
  body: string;
  webUrl?: string;
  matterRef?: string;
}

/**
 * Search Teams channel messages across all teams the app can see.
 * Uses Graph unified search (entityTypes: chatMessage).
 */
export async function searchTeamsMessages(
  query: string,
  opts: { maxResults?: number; daysBack?: number } = {},
): Promise<TeamsMessage[]> {
  if (!Config.email.graph.enabled) return [];
  const token = await getGraphToken();
  const max = opts.maxResults ?? 20;

  try {
    const body = {
      requests: [{
        entityTypes: ["chatMessage"],
        query: { queryString: query },
        from: 0,
        size: max,
      }],
    };
    const result = await graphPost("/search/query", token, body) as Record<string, unknown>;
    const hits = (result["value"] as Array<Record<string, unknown>>)?.[0]
      ?.["hitsContainers"] as Array<Record<string, unknown>>;
    if (!Array.isArray(hits)) return [];

    return hits.flatMap((hc) => {
      const items = hc["hits"] as Array<Record<string, unknown>>;
      return Array.isArray(items) ? items.map((h): TeamsMessage => {
        const r = h["resource"] as Record<string, unknown>;
        const from = r["from"] as Record<string, unknown>;
        const fromName = String(
          (from?.["user"] as Record<string, unknown>)?.["displayName"]
          ?? (from?.["application"] as Record<string, unknown>)?.["displayName"]
          ?? "Unknown",
        );
        const bodyText = String((r["body"] as Record<string, unknown>)?.["content"] ?? "").replace(/<[^>]+>/g, " ").slice(0, 400);
        return {
          id: String(r["id"] ?? ""),
          teamName: String((r["channelIdentity"] as Record<string, unknown>)?.["teamId"] ?? ""),
          channelName: String((r["channelIdentity"] as Record<string, unknown>)?.["channelId"] ?? ""),
          channelId: String((r["channelIdentity"] as Record<string, unknown>)?.["channelId"] ?? ""),
          from: fromName,
          createdAt: String(r["createdDateTime"] ?? ""),
          body: bodyText,
          webUrl: String(r["webUrl"] ?? "") || undefined,
          matterRef: extractMatterRef(bodyText),
        };
      }) : [];
    }).slice(0, max);
  } catch (err) {
    logger.warn("Teams search failed", { error: (err as Error).message });
    return [];
  }
}

/**
 * Post a message to a Teams channel via the Graph API.
 * Requires ChannelMessage.Send (delegated) or use incoming webhook URL.
 */
export async function postToTeamsChannel(
  channelId: string,
  teamId: string,
  content: string,
): Promise<void> {
  const token = await getGraphToken();
  await graphPost(
    `/teams/${encodeURIComponent(teamId)}/channels/${encodeURIComponent(channelId)}/messages`,
    token,
    { body: { contentType: "html", content } },
  );
}

/**
 * Post to a Teams channel via an Incoming Webhook URL (simpler, no app auth needed).
 * TEAMS_INCOMING_WEBHOOK_URL must be set.
 */
export async function postToTeamsWebhook(
  webhookUrl: string,
  title: string,
  text: string,
  facts?: Array<{ name: string; value: string }>,
): Promise<void> {
  // Validate URL is a genuine Teams webhook before sending any data
  try {
    const u = new URL(webhookUrl);
    if (!["outlook.office.com", "outlook.office365.com"].some(
      (host) => u.hostname === host || u.hostname.endsWith(".webhook.office.com"),
    )) {
      throw new Error(`Not a valid Teams webhook hostname: ${u.hostname}`);
    }
    if (u.protocol !== "https:") throw new Error("Teams webhook URL must use HTTPS");
  } catch (err) {
    logger.warn("Blocked invalid Teams webhook URL", { error: (err as Error).message });
    throw new Error(`Invalid Teams webhook URL: ${(err as Error).message}`);
  }

  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
  const card = {
    "@type": "MessageCard",
    "@context": "https://schema.org/extensions",
    themeColor: "0076D7",
    summary: title,
    sections: [{
      activityTitle: title,
      activityText: text,
      facts: facts ?? [],
    }],
  };
  try {
    const res = await fetch(webhookUrl, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(card),
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    if (!res.ok) throw new Error(`Teams webhook POST failed: ${res.status}`);
  } catch (err) { clearTimeout(timer); throw err; }
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function extractMatterRef(text: string): string | undefined {
  const patterns = [/\[([A-Z]{1,5}[-/]\d{3,8})\]/, /\b([A-Z]{1,5}[-/]\d{4,8})\b/];
  for (const p of patterns) {
    const m = text.match(p);
    if (m?.[1]) return m[1];
  }
  return undefined;
}
