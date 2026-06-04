// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Clio practice management integration.
 *
 * Authentication: OAuth 2.0 authorization code grant.
 * Tokens (access + refresh) are persisted to Config.clio.tokensFile.
 * The access token is auto-refreshed whenever it is within 60 seconds of expiry.
 *
 * Region routing: Clio operates four data regions — us, eu, ca, au.
 * Set CLIO_REGION to the firm's region. The base URL is derived from a
 * hard-coded allowlist, so a malformed env var cannot cause SSRF.
 *
 * Security: API key (client secret) never appears in logs; all requests go to
 * hard-coded Clio domains (not configurable); response bodies are capped at 2 MB.
 */

import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname } from "node:path";
import { Config } from "../config.js";
import { logger } from "../logger.js";

const CLIO_REGIONS: Record<string, string> = {
  us: "https://app.clio.com",
  eu: "https://eu.app.clio.com",
  ca: "https://ca.app.clio.com",
  au: "https://au.app.clio.com",
};

const RESPONSE_CAP = 2_000_000; // 2 MB

export interface ClioTokens {
  accessToken: string;
  refreshToken: string;
  expiresAt: number;   // unix timestamp in ms
  tokenType: string;
  firmId?: string;
  firmName?: string;
  connectedAt: string; // ISO timestamp
}

export class ClioClient {
  private tokens: ClioTokens | null = null;
  private readonly base: string;

  constructor() {
    const region = Config.clio.region;
    const base = CLIO_REGIONS[region];
    if (!base) throw new Error(`Unknown CLIO_REGION "${region}". Valid values: us, eu, ca, au`);
    this.base = base;
  }

  // ── OAuth helpers ────────────────────────────────────────────────────────

  /** Build the Clio authorization URL to redirect the user to. */
  authUrl(state: string): string {
    const p = new URLSearchParams({
      response_type: "code",
      client_id: Config.clio.clientId,
      redirect_uri: Config.clio.redirectUri,
      state,
    });
    // Include scopes if configured. Clio v4 falls back to the app's portal-level
    // permissions when scope is omitted, so this is optional but recommended for
    // apps that declare explicit scopes in the developer portal.
    if (Config.clio.scopes) p.set("scope", Config.clio.scopes);
    return `${this.base}/oauth/authorize?${p}`;
  }

  /** Exchange an authorization code for tokens and persist them. */
  async exchangeCode(code: string): Promise<void> {
    const res = await fetch(`${this.base}/oauth/token`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
      body: new URLSearchParams({
        grant_type: "authorization_code",
        code,
        redirect_uri: Config.clio.redirectUri,
        client_id: Config.clio.clientId,
        client_secret: Config.clio.clientSecret,
      }),
    });
    if (!res.ok) throw new Error(`Clio token exchange failed: ${res.status}`);
    const body = await res.json() as { access_token: string; refresh_token: string; expires_in: number; token_type: string };
    this.tokens = {
      accessToken: body.access_token,
      refreshToken: body.refresh_token,
      expiresAt: Date.now() + body.expires_in * 1000,
      tokenType: body.token_type,
      connectedAt: new Date().toISOString(),
    };
    // Fetch firm info to store firmName
    try {
      const me = await this.get("/api/v4/users/who_am_i.json", { fields: "id,name,account{id,name}" }) as { data?: { id?: number; name?: string; account?: { id?: number; name?: string } } };
      if (me?.data?.account) {
        this.tokens.firmId = String(me.data.account.id ?? "");
        this.tokens.firmName = me.data.account.name ?? "";
      }
    } catch { /* non-fatal */ }
    await this.save();
  }

  /** Refresh the access token using the refresh token. */
  private async refresh(): Promise<void> {
    if (!this.tokens) throw new Error("Clio not connected");
    const res = await fetch(`${this.base}/oauth/token`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
      body: new URLSearchParams({
        grant_type: "refresh_token",
        refresh_token: this.tokens.refreshToken,
        client_id: Config.clio.clientId,
        client_secret: Config.clio.clientSecret,
      }),
    });
    if (!res.ok) {
      // Refresh token invalid — clear stored tokens
      this.tokens = null;
      await this.save();
      throw new Error(`Clio token refresh failed: ${res.status} — reconnect required`);
    }
    const body = await res.json() as { access_token: string; refresh_token?: string; expires_in: number; token_type: string };
    this.tokens = {
      ...this.tokens,
      accessToken: body.access_token,
      refreshToken: body.refresh_token ?? this.tokens.refreshToken,
      expiresAt: Date.now() + body.expires_in * 1000,
      tokenType: body.token_type,
    };
    await this.save();
  }

  /** Return a valid access token, refreshing if within 60s of expiry. */
  private async ensureValid(): Promise<string> {
    if (!this.tokens) throw new Error("Clio not connected — visit /auth/clio/connect");
    if (Date.now() >= this.tokens.expiresAt - 60_000) await this.refresh();
    return this.tokens.accessToken;
  }

  // ── Persistence ──────────────────────────────────────────────────────────

  async load(): Promise<void> {
    try {
      const raw = await readFile(Config.clio.tokensFile, "utf8");
      this.tokens = JSON.parse(raw) as ClioTokens;
    } catch {
      this.tokens = null;
    }
  }

  private async save(): Promise<void> {
    await mkdir(dirname(Config.clio.tokensFile), { recursive: true });
    await writeFile(Config.clio.tokensFile, JSON.stringify(this.tokens ?? null, null, 2), "utf8");
  }

  async disconnect(): Promise<void> {
    this.tokens = null;
    await this.save();
    logger.info("Clio disconnected");
  }

  // ── Status ───────────────────────────────────────────────────────────────

  isConnected(): boolean { return this.tokens !== null; }

  status(): { connected: boolean; firmName?: string; firmId?: string; connectedAt?: string } {
    if (!this.tokens) return { connected: false };
    return { connected: true, firmName: this.tokens.firmName, firmId: this.tokens.firmId, connectedAt: this.tokens.connectedAt };
  }

  // ── HTTP helpers ─────────────────────────────────────────────────────────

  async get(path: string, params?: Record<string, string>): Promise<unknown> {
    const token = await this.ensureValid();
    const url = new URL(path, this.base);
    if (params) for (const [k, v] of Object.entries(params)) url.searchParams.set(k, v);
    const res = await fetch(url, {
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      signal: AbortSignal.timeout(30_000),
    });
    if (!res.ok) throw new Error(`Clio GET ${path} failed: ${res.status}`);
    const text = await res.text();
    if (text.length > RESPONSE_CAP) throw new Error("Clio response exceeded 2 MB cap");
    return JSON.parse(text);
  }

  async getBuffer(path: string): Promise<Buffer> {
    const token = await this.ensureValid();
    const url = new URL(path, this.base);
    const res = await fetch(url, {
      headers: { Authorization: `Bearer ${token}` },
      signal: AbortSignal.timeout(60_000),
    });
    if (!res.ok) throw new Error(`Clio download ${path} failed: ${res.status}`);
    const ab = await res.arrayBuffer();
    if (ab.byteLength > RESPONSE_CAP) throw new Error("Clio download exceeded 2 MB cap");
    return Buffer.from(ab);
  }

  async post(path: string, body: Record<string, unknown>): Promise<unknown> {
    const token = await this.ensureValid();
    const res = await fetch(new URL(path, this.base), {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json", Accept: "application/json" },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(30_000),
    });
    if (!res.ok) throw new Error(`Clio POST ${path} failed: ${res.status}`);
    const text = await res.text();
    if (text.length > RESPONSE_CAP) throw new Error("Clio response exceeded 2 MB cap");
    return JSON.parse(text);
  }

  // ── Matter API ───────────────────────────────────────────────────────────

  async listMatters(opts: { status?: string; limit?: number; page?: number } = {}): Promise<unknown> {
    const params: Record<string, string> = {
      fields: "id,display_number,description,status,client{id,name},practice_area{name},open_date,close_date",
      limit: String(opts.limit ?? 50),
      page: String(opts.page ?? 1),
    };
    if (opts.status && opts.status !== "all") params["status[]"] = opts.status;
    return this.get("/api/v4/matters.json", params);
  }

  async getMatter(id: number): Promise<unknown> {
    return this.get(`/api/v4/matters/${id}.json`, {
      fields: "id,display_number,description,status,client{id,name,email_addresses},practice_area{name},open_date,close_date,custom_fields{value,field_name},responsible_attorney{id,name},originating_attorney{id,name}",
    });
  }

  async listDocuments(matterId: number, limit = 50): Promise<unknown> {
    return this.get("/api/v4/documents.json", {
      "matter_id": String(matterId),
      fields: "id,name,content_type,latest_document_version{id,fully_uploaded}",
      limit: String(limit),
    });
  }

  async downloadDocument(documentId: number): Promise<Buffer> {
    return this.getBuffer(`/api/v4/documents/${documentId}/download`);
  }

  async createActivity(matterId: number, opts: { description: string; dateOn: string; durationHours: number; userId?: number }): Promise<unknown> {
    return this.post("/api/v4/activities.json", {
      data: {
        type: "TimeEntry",
        date: opts.dateOn,
        quantity_in_hours: opts.durationHours,
        note: opts.description,
        matter: { id: matterId },
        ...(opts.userId ? { user: { id: opts.userId } } : {}),
      },
    });
  }

  async createNote(matterId: number, subject: string, detail: string): Promise<unknown> {
    return this.post("/api/v4/notes.json", {
      data: {
        subject,
        detail,
        matter: { id: matterId },
      },
    });
  }

  async listContacts(opts: { type?: string; limit?: number } = {}): Promise<unknown> {
    const params: Record<string, string> = {
      fields: "id,name,type,email_addresses,phone_numbers",
      limit: String(opts.limit ?? 50),
    };
    if (opts.type) params["type"] = opts.type;
    return this.get("/api/v4/contacts.json", params);
  }
}

/** Singleton — loaded once at startup, reused across all requests. */
export const clioClient = new ClioClient();
