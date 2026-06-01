// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * OAuth 2.0 / OpenID Connect login for Google, Microsoft, and LinkedIn.
 *
 * Authorization-code flow, no extra OAuth SDK:
 *   GET  /auth/providers           → which providers are configured (for the UI)
 *   GET  /auth/:provider/login     → redirect to the provider with a signed state
 *   GET  /auth/:provider/callback  → exchange code, fetch userinfo, map to a
 *                                    LawyerProfile (by email), set a signed
 *                                    session cookie, redirect back to the UI
 *   POST /auth/logout              → clear the session cookie
 *
 * The session is a signed, httpOnly cookie holding the SessionUser — stateless,
 * no server-side session store. Profiles are auto-provisioned on first login
 * (partner if the email is in ADMIN_EMAILS, otherwise lawyer).
 *
 * NOTE: this is wired and type-checked but cannot be live-tested without OAuth
 * app credentials registered with each provider (client id/secret + redirect
 * URI). See the README "Deploying with login" section.
 */

import { randomUUID } from "crypto";
import type { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import type { Orchestrator } from "../orchestrator.js";
import type { SessionUser } from "../types.js";

const SESSION_COOKIE = "bm_session";
const STATE_COOKIE = "bm_oauth_state";

type ProviderKey = "google" | "microsoft" | "linkedin";

interface ProviderSpec {
  authUrl: string;
  tokenUrl: string;
  userInfoUrl: string;
  scope: string;
  /** Map the provider's userinfo payload to a normalized identity. */
  mapUser: (info: Record<string, unknown>) => { sub: string; email: string; name: string };
}

const PROVIDERS: Record<ProviderKey, ProviderSpec> = {
  google: {
    authUrl: "https://accounts.google.com/o/oauth2/v2/auth",
    tokenUrl: "https://oauth2.googleapis.com/token",
    userInfoUrl: "https://openidconnect.googleapis.com/v1/userinfo",
    scope: "openid email profile",
    mapUser: (i) => ({ sub: String(i.sub), email: String(i.email ?? ""), name: String(i.name ?? i.email ?? "User") }),
  },
  microsoft: {
    authUrl: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
    tokenUrl: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
    userInfoUrl: "https://graph.microsoft.com/oidc/userinfo",
    scope: "openid email profile",
    mapUser: (i) => ({ sub: String(i.sub), email: String(i.email ?? i.preferred_username ?? ""), name: String(i.name ?? i.email ?? "User") }),
  },
  linkedin: {
    authUrl: "https://www.linkedin.com/oauth/v2/authorization",
    tokenUrl: "https://www.linkedin.com/oauth/v2/accessToken",
    userInfoUrl: "https://api.linkedin.com/v2/userinfo",
    scope: "openid email profile",
    mapUser: (i) => ({ sub: String(i.sub), email: String(i.email ?? ""), name: String(i.name ?? i.email ?? "User") }),
  },
};

const creds = (p: ProviderKey) => Config.auth.providers[p];
const isConfigured = (p: ProviderKey): boolean => !!creds(p).clientId && !!creds(p).clientSecret;
const redirectUri = (p: ProviderKey) => `${Config.auth.baseUrl.replace(/\/$/, "")}/auth/${p}/callback`;

const cookieOpts = (maxAgeSec: number) => ({
  signed: true, httpOnly: true as const, sameSite: "lax" as const,
  secure: Config.auth.baseUrl.startsWith("https"), path: "/", maxAge: maxAgeSec,
});

/** Read the signed session cookie → SessionUser, or null. */
export function readSessionCookie(req: FastifyRequest): SessionUser | null {
  const raw = (req.cookies as Record<string, string> | undefined)?.[SESSION_COOKIE];
  if (!raw) return null;
  const unsigned = (req as unknown as { unsignCookie: (v: string) => { valid: boolean; value: string | null } }).unsignCookie(raw);
  if (!unsigned.valid || !unsigned.value) return null;
  try { return JSON.parse(unsigned.value) as SessionUser; } catch { return null; }
}

export function registerAuthRoutes(app: FastifyInstance, orchestrator: Orchestrator): void {
  app.get("/auth/providers", async () => ({
    google: isConfigured("google"),
    microsoft: isConfigured("microsoft"),
    linkedin: isConfigured("linkedin"),
  }));

  app.get("/auth/:provider/login", async (req, reply) => {
    const provider = (req.params as { provider: string }).provider as ProviderKey;
    const spec = PROVIDERS[provider];
    if (!spec || !isConfigured(provider)) return reply.code(404).send({ error: "Provider not configured" });

    const state = randomUUID();
    reply.setCookie(STATE_COOKIE, state, cookieOpts(600));
    const url = new URL(spec.authUrl);
    url.searchParams.set("response_type", "code");
    url.searchParams.set("client_id", creds(provider).clientId);
    url.searchParams.set("redirect_uri", redirectUri(provider));
    url.searchParams.set("scope", spec.scope);
    url.searchParams.set("state", state);
    return reply.redirect(url.toString());
  });

  app.get("/auth/:provider/callback", async (req, reply) => {
    const provider = (req.params as { provider: string }).provider as ProviderKey;
    const spec = PROVIDERS[provider];
    if (!spec || !isConfigured(provider)) return reply.code(404).send({ error: "Provider not configured" });

    const { code, state } = req.query as { code?: string; state?: string };
    const expected = readSignedCookie(req, STATE_COOKIE);
    if (!code || !state || state !== expected) return reply.code(400).send({ error: "Invalid OAuth state" });
    reply.clearCookie(STATE_COOKIE, { path: "/" });

    try {
      // Exchange the code for an access token.
      const tokenRes = await fetch(spec.tokenUrl, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
        body: new URLSearchParams({
          grant_type: "authorization_code",
          code,
          redirect_uri: redirectUri(provider),
          client_id: creds(provider).clientId,
          client_secret: creds(provider).clientSecret,
        }),
      });
      if (!tokenRes.ok) throw new Error(`token exchange failed (${tokenRes.status})`);
      const token = (await tokenRes.json()) as { access_token?: string };
      if (!token.access_token) throw new Error("no access_token in response");

      // Fetch the user's profile.
      const infoRes = await fetch(spec.userInfoUrl, { headers: { Authorization: `Bearer ${token.access_token}` } });
      if (!infoRes.ok) throw new Error(`userinfo failed (${infoRes.status})`);
      const identity = spec.mapUser((await infoRes.json()) as Record<string, unknown>);
      if (!identity.email) throw new Error("provider returned no email");

      // Map to a lawyer profile (auto-provision on first login).
      let profile = orchestrator.profiles.getByEmail(identity.email);
      if (!profile) {
        const role = Config.auth.adminEmails.includes(identity.email.toLowerCase()) ? "partner" : "lawyer";
        profile = await orchestrator.profiles.create({ name: identity.name, email: identity.email, role });
      }

      const user: SessionUser = { profileId: profile.id, name: profile.name, email: profile.email, role: profile.role };
      reply.setCookie(SESSION_COOKIE, JSON.stringify(user), cookieOpts(60 * 60 * 12));
      logger.info("OAuth login", { provider, email: identity.email, role: profile.role });
      return reply.redirect(Config.auth.uiUrl);
    } catch (err) {
      logger.warn("OAuth callback failed", { provider, error: (err as Error).message });
      return reply.redirect(`${Config.auth.uiUrl}?auth_error=1`);
    }
  });

  app.post("/auth/logout", async (_req, reply) => {
    reply.clearCookie(SESSION_COOKIE, { path: "/" });
    return { ok: true };
  });
}

function readSignedCookie(req: FastifyRequest, name: string): string | null {
  const raw = (req.cookies as Record<string, string> | undefined)?.[name];
  if (!raw) return null;
  const unsigned = (req as unknown as { unsignCookie: (v: string) => { valid: boolean; value: string | null } }).unsignCookie(raw);
  return unsigned.valid ? unsigned.value : null;
}
