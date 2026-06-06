// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Email search client — Microsoft Graph (Exchange/O365) + Gmail.
 *
 * Used by the briefing swarm's email spoke to surface correspondence
 * about a client or matter from the firm's email infrastructure.
 *
 * Two providers, both optional — configure whichever your firm uses:
 *
 *  Microsoft Graph  — Exchange Online / Office 365 (most large law firms)
 *    App-only auth: GRAPH_TENANT_ID + GRAPH_CLIENT_ID + GRAPH_CLIENT_SECRET
 *    Dev shortcut:  GRAPH_ACCESS_TOKEN (pre-obtained bearer token)
 *
 *  Gmail            — Google Workspace (boutique / tech-forward firms)
 *    Service account: GMAIL_SA_KEY_JSON + GMAIL_USER_EMAIL
 *    Dev shortcut:    GMAIL_ACCESS_TOKEN (pre-obtained bearer token)
 *
 * Security:
 *   - All HTTP calls use a 15 s timeout and a 512 KB response cap.
 *   - Credentials never appear in logs or error messages.
 *   - SSRF: only microsoft.com and googleapis.com are targeted (hardcoded).
 */

import { Config } from "../config.js";
import { logger } from "../logger.js";

const b64url = (obj: unknown): string =>
  btoa(JSON.stringify(obj)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

const sigB64url = (buf: ArrayBuffer): string =>
  btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

function b64ToStr(b64: string): string {
  return atob(b64.replace(/-/g, "+").replace(/_/g, "/"));
}

function b64ToBytes(b64: string): Uint8Array {
  return Uint8Array.from(b64ToStr(b64), (c) => c.charCodeAt(0));
}

const REQUEST_TIMEOUT_MS = 15_000;
const MAX_RESPONSE_BYTES = 512 * 1024;

// ─── Shared types ─────────────────────────────────────────────────────────────

export interface EmailMessage {
  id: string;
  subject: string;
  from: string;
  /** ISO timestamp */
  receivedAt: string;
  /** 2–4 sentence body preview (never the full body) */
  snippet: string;
  /** Matter or client reference parsed from subject/body if present */
  matterRef?: string;
  provider: "graph" | "gmail";
  hasAttachments: boolean;
}

// ─── Token management ─────────────────────────────────────────────────────────

let cachedGraphToken: { token: string; expiresAt: number } | null = null;
let _graphTokenPromise: Promise<string> | null = null;

async function getGraphToken(): Promise<string> {
  const cfg = Config.email.graph;

  // Pre-obtained token takes priority (dev / single-user mode)
  if (cfg.accessToken) return cfg.accessToken;

  // Return cached token if still valid (with 60 s buffer)
  if (cachedGraphToken && Date.now() < cachedGraphToken.expiresAt - 60_000) {
    return cachedGraphToken.token;
  }

  // In-flight deduplication: only one token request at a time
  _graphTokenPromise ??= (async () => {
    // Client credentials (app-only) flow
    const body = new URLSearchParams({
      client_id: cfg.clientId,
      client_secret: cfg.clientSecret,
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
      if (!res.ok) throw new Error(`Graph token exchange failed: ${res.status}`);
      const data = (await res.json()) as { access_token: string; expires_in: number };
      cachedGraphToken = { token: data.access_token, expiresAt: Date.now() + data.expires_in * 1000 };
      return data.access_token;
    } catch (err) {
      clearTimeout(timer);
      throw err;
    }
  })().finally(() => { _graphTokenPromise = null; });
  return _graphTokenPromise;
}

let cachedGmailToken: { token: string; expiresAt: number } | null = null;
let _gmailTokenPromise: Promise<string> | null = null;

async function getGmailToken(): Promise<string> {
  const cfg = Config.email.gmail;

  // Pre-obtained token takes priority
  if (cfg.accessToken) return cfg.accessToken;

  // Return cached token if still valid
  if (cachedGmailToken && Date.now() < cachedGmailToken.expiresAt - 60_000) {
    return cachedGmailToken.token;
  }

  // In-flight deduplication: only one token request at a time
  _gmailTokenPromise ??= (async () => {
    // Service-account JWT auth (RFC 7523)
    const rawKey = cfg.saKeyJson.startsWith("{")
      ? cfg.saKeyJson
      : b64ToStr(cfg.saKeyJson);
    const sa = JSON.parse(rawKey) as {
      client_email: string; private_key: string; token_uri?: string;
    };

    const now = Math.floor(Date.now() / 1000);
    const header = b64url({ alg: "RS256", typ: "JWT" });
    const payload = b64url({
      iss: sa.client_email,
      sub: cfg.userEmail,
      scope: "https://www.googleapis.com/auth/gmail.readonly",
      aud: sa.token_uri ?? "https://oauth2.googleapis.com/token",
      iat: now,
      exp: now + 3600,
    });

    // Sign with RS256 — requires Node 18+ WebCrypto
    const pemKey = sa.private_key;
    const keyData = pemKey
      .replace(/-----BEGIN PRIVATE KEY-----/, "")
      .replace(/-----END PRIVATE KEY-----/, "")
      .replace(/\s+/g, "");

    const keyBytes = b64ToBytes(keyData);
    const cryptoKey = await crypto.subtle.importKey(
      "pkcs8",
      keyBytes.buffer as ArrayBuffer,
      { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
      false,
      ["sign"],
    );
    const signData = new TextEncoder().encode(`${header}.${payload}`);
    const sig = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", cryptoKey, signData);
    const jwt = `${header}.${payload}.${sigB64url(sig)}`;

    const tokenBody = new URLSearchParams({
      grant_type: "urn:ietf:params:oauth:grant-type:jwt-bearer",
      assertion: jwt,
    });

    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
    try {
      const res = await fetch("https://oauth2.googleapis.com/token", {
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded" },
        body: tokenBody,
        signal: ctrl.signal,
      });
      clearTimeout(timer);
      if (!res.ok) throw new Error(`Gmail token exchange failed: ${res.status}`);
      const data = (await res.json()) as { access_token: string; expires_in: number };
      cachedGmailToken = { token: data.access_token, expiresAt: Date.now() + data.expires_in * 1000 };
      return data.access_token;
    } catch (err) {
      clearTimeout(timer);
      throw err;
    }
  })().finally(() => { _gmailTokenPromise = null; });
  return _gmailTokenPromise;
}

// ─── Shared HTTP helper ───────────────────────────────────────────────────────

async function apiFetch(url: string, token: string): Promise<unknown> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
  try {
    const res = await fetch(url, {
      headers: { authorization: `Bearer ${token}`, accept: "application/json" },
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    if (!res.ok) throw new Error(`API error ${res.status}`);

    const reader = res.body?.getReader();
    if (!reader) return await res.json();

    let bytes = 0;
    const chunks: Uint8Array[] = [];
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      bytes += value.length;
      if (bytes > MAX_RESPONSE_BYTES) throw new Error("Email API response too large");
      chunks.push(value);
    }
    const text = new TextDecoder().decode(
      chunks.reduce((a, c) => { const n = new Uint8Array(a.length + c.length); n.set(a); n.set(c, a.length); return n; }, new Uint8Array(0)),
    );
    return JSON.parse(text);
  } catch (err) {
    clearTimeout(timer);
    throw err;
  }
}

// ─── Microsoft Graph search ───────────────────────────────────────────────────

export async function searchGraphMail(
  query: string,
  opts: { maxResults?: number; daysBack?: number } = {},
): Promise<EmailMessage[]> {
  const cfg = Config.email.graph;
  if (!cfg.enabled) return [];

  const max = opts.maxResults ?? 20;

  const token = await getGraphToken();
  const userEmail = cfg.userEmail;

  // Microsoft Graph Mail search — $search uses OData KQL.
  // NOTE: $search and $filter cannot be combined on the messages endpoint.
  // Sanitize query to prevent KQL injection.
  const safeQuery = query.replace(/["\\]/g, " ").slice(0, 200);
  const qs = new URLSearchParams({
    "$search": `"${safeQuery}"`,
    "$top": String(max),
    "$select": "id,subject,from,receivedDateTime,bodyPreview,hasAttachments",
    "$orderby": "receivedDateTime desc",
  });

  const url = `https://graph.microsoft.com/v1.0/users/${encodeURIComponent(userEmail)}/messages?${qs}`;
  const data = (await apiFetch(url, token)) as { value?: Array<Record<string, unknown>> };

  return (data.value ?? []).map((m): EmailMessage => ({
    id: String(m["id"] ?? ""),
    subject: String(m["subject"] ?? "(no subject)"),
    from: extractGraphEmail(m["from"]),
    receivedAt: String(m["receivedDateTime"] ?? ""),
    snippet: String(m["bodyPreview"] ?? "").slice(0, 400),
    matterRef: extractMatterRef(String(m["subject"] ?? "")),
    provider: "graph",
    hasAttachments: Boolean(m["hasAttachments"]),
  }));
}

function extractGraphEmail(from: unknown): string {
  if (typeof from !== "object" || !from) return "";
  const f = from as Record<string, unknown>;
  const addr = (f["emailAddress"] as Record<string, unknown>)?.["address"];
  return String(addr ?? "");
}

// ─── Gmail search ─────────────────────────────────────────────────────────────

export async function searchGmail(
  query: string,
  opts: { maxResults?: number; daysBack?: number } = {},
): Promise<EmailMessage[]> {
  const cfg = Config.email.gmail;
  if (!cfg.enabled) return [];

  const max = opts.maxResults ?? 20;
  const daysBack = opts.daysBack ?? 90;
  const userEmail = cfg.userEmail || "me";

  const token = await getGmailToken();

  // Gmail search query — include date constraint
  const gmailQuery = `${query} newer_than:${daysBack}d`;
  const listUrl = `https://gmail.googleapis.com/gmail/v1/users/${encodeURIComponent(userEmail)}/messages?` +
    new URLSearchParams({ q: gmailQuery, maxResults: String(max) });

  const listData = (await apiFetch(listUrl, token)) as { messages?: Array<{ id: string }> };
  const messageIds = (listData.messages ?? []).slice(0, max).map((m) => m.id);
  if (messageIds.length === 0) return [];

  // Fetch each message in parallel (capped at 10 concurrent)
  const CONCURRENCY = 10;
  const results: EmailMessage[] = [];
  for (let i = 0; i < messageIds.length; i += CONCURRENCY) {
    const batch = messageIds.slice(i, i + CONCURRENCY);
    const fetched = await Promise.allSettled(
      batch.map((id) =>
        apiFetch(
          `https://gmail.googleapis.com/gmail/v1/users/${encodeURIComponent(userEmail)}/messages/${id}?format=metadata&metadataHeaders=Subject&metadataHeaders=From&metadataHeaders=Date`,
          token,
        ).then((msg): EmailMessage => {
          const m = msg as Record<string, unknown>;
          const headers = (m["payload"] as Record<string, unknown>)?.["headers"] as Array<{ name: string; value: string }> ?? [];
          const h = (name: string) => headers.find((x) => x.name.toLowerCase() === name.toLowerCase())?.value ?? "";
          const snippet = String(m["snippet"] ?? "").slice(0, 400);
          return {
            id: String(m["id"] ?? ""),
            subject: h("subject") || "(no subject)",
            from: h("from"),
            receivedAt: h("date"),
            snippet,
            matterRef: extractMatterRef(h("subject")),
            provider: "gmail",
            hasAttachments: Boolean(
              (m["payload"] as Record<string, unknown>)?.["parts"]
            ),
          };
        }),
      ),
    );
    for (const r of fetched) {
      if (r.status === "fulfilled") results.push(r.value);
    }
  }

  return results;
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function extractMatterRef(subject: string): string | undefined {
  // Common patterns: [M-001], (Matter 1234), RE: ABC-2024-001
  const patterns = [
    /\[([A-Z]{1,5}[-/]\d{3,8})\]/,
    /\((?:matter|file|ref)[:\s]+([A-Z0-9/-]+)\)/i,
    /\b([A-Z]{1,5}[-/]\d{4,8})\b/,
  ];
  for (const p of patterns) {
    const m = subject.match(p);
    if (m?.[1]) return m[1];
  }
  return undefined;
}
