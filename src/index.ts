// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Application entry point — Big Michael.
 *
 * Startup order:
 *   1. dotenv (static import) — loads optional .env into process.env for local dev.
 *      In production, environment variables come from the host (container env,
 *      Kubernetes secrets, etc.) — no .env file is required or expected.
 *   2. Infisical loader (static import, awaited) — authenticates with Infisical
 *      using INFISICAL_CLIENT_ID / CLIENT_SECRET / PROJECT_ID and overlays ALL
 *      managed secrets into process.env BEFORE config.ts is ever evaluated.
 *   3. Everything else via dynamic import() — config.ts's require() calls only
 *      run after step 2 completes, so every secret (including ANTHROPIC_API_KEY)
 *      can live exclusively in Infisical with no .env fallback needed.
 *
 * Infisical is open-source and self-hostable: https://infisical.com
 * Self-host: docker compose -f docker-compose.prod.yml up -d (Infisical repo)
 *
 * Bootstrap: only INFISICAL_CLIENT_ID, INFISICAL_CLIENT_SECRET, and
 * INFISICAL_PROJECT_ID need to reach the process (via host env or a minimal
 * .env). Everything else — ANTHROPIC_API_KEY, connector keys, session secrets —
 * comes from Infisical. If Infisical is not configured, dotenv/.env is used as-is.
 */

// ─── Step 1: dotenv ─── static, runs first ────────────────────────────────────
// Loads .env if present. A no-op in production where env vars come from the host.
import "dotenv/config";

// ─── Step 2: Infisical ─── static import, awaited before anything else ────────
// secrets/index.ts has no dependency on config.ts so it is safe to import
// statically. The await ensures ALL secrets are in process.env before step 3.
import { loadSecrets } from "./secrets/index.js";
await loadSecrets();

// ─── Step 3: Application ─── dynamic imports so config.ts evaluates NOW ───────
// config.ts calls require() at module evaluation time. By using dynamic import()
// here, we guarantee config.ts is evaluated only after Infisical secrets are
// loaded — so ANTHROPIC_API_KEY and every other secret can live in Infisical.
const { logger }                       = await import("./logger.js");
const { Config }                       = await import("./config.js");
const { Orchestrator }                 = await import("./orchestrator.js");
const { startMcpServer, startRestApi } = await import("./mcp/server.js");
const { costStore }                    = await import("./cost/index.js");
const { LocalBackend, RemoteBackend, probeBackend } = await import("./backend/index.js");

// ─── Run mode ─────────────────────────────────────────────────────────────────
// The RuVector stores under ./data take an EXCLUSIVE single-writer lock and the
// REST API binds one port, so only ONE process can own them. To run a browse
// server and the Claude Code MCP at the same time, only one owns the DB and the
// other attaches as a thin client over the REST API.
//
//   BIG_MICHAEL_MODE=backend     own DB + REST, never MCP (a dedicated service)
//   BIG_MICHAEL_MODE=mcp         pure MCP client — requires a reachable backend
//   BIG_MICHAEL_MODE=standalone  classic single process (own DB + REST + MCP)
//   BIG_MICHAEL_MODE=auto (def)  own the DB if free; otherwise attach as a client
//
// BIG_MICHAEL_API sets the owner URL a client connects to (default the local API).
const mode    = (process.env.BIG_MICHAEL_MODE ?? "auto").toLowerCase();
const apiUrl  = process.env.BIG_MICHAEL_API ?? `http://${Config.api.host}:${Config.api.port}`;
const isStdio = !process.stdin.isTTY;

logger.info("Big Michael starting…", { mode, apiUrl, stdio: isStdio });

// Constructing the Orchestrator opens the RuVector stores, which take an
// exclusive lock. On a fast restart (e.g. tsx watch) the previous instance may
// still be releasing it, so retry briefly on a lock error before giving up.
async function newOrchestrator(attempts = 6): Promise<Orchestrator> {
  for (let i = 0; ; i++) {
    try {
      return new Orchestrator();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      if (i >= attempts - 1 || !/already open|acquire lock|\block\b/i.test(msg)) throw err;
      logger.warn(`Vector DB locked (attempt ${i + 1}/${attempts}) — retrying in 400ms…`);
      await new Promise((r) => setTimeout(r, 400));
    }
  }
}

// The owner: opens the vector DB and serves the REST API (and MCP when on stdio).
async function startOwner(withMcp: boolean): Promise<void> {
  await costStore.init();
  const orchestrator = await newOrchestrator();
  await orchestrator.init();
  await startRestApi(orchestrator);
  if (withMcp) await startMcpServer(new LocalBackend(orchestrator));
  logger.info(
    `Big Michael ready (owner) — REST on ${Config.api.host}:${Config.api.port}` +
      (withMcp ? " + MCP stdio" : ""),
  );
}

// A thin MCP client: no DB, no REST — forwards every tool to the owner's API.
async function startClient(): Promise<void> {
  await startMcpServer(new RemoteBackend(apiUrl, Config.api.apiKey || undefined));
  logger.info(`Big Michael ready (MCP client) — proxying tools to ${apiUrl}`);
}

switch (mode) {
  case "backend":
    await startOwner(false);
    break;

  case "mcp":
    if (!(await probeBackend(apiUrl))) {
      logger.error(
        `No Big Michael backend reachable at ${apiUrl}. ` +
          `Start one first (e.g. 'npm run serve', or BIG_MICHAEL_MODE=backend).`,
      );
      process.exit(1);
    }
    await startClient();
    break;

  case "standalone":
    await startOwner(isStdio);
    break;

  case "auto":
  default:
    if (await probeBackend(apiUrl)) {
      if (isStdio) {
        // A backend already owns the DB — attach as a client so the MCP works
        // alongside it without fighting over the single-writer lock.
        await startClient();
      } else {
        logger.info(
          `A Big Michael backend is already running at ${apiUrl} — not starting a ` +
            `duplicate. Point the UI/clients at it, or set BIG_MICHAEL_MODE=standalone ` +
            `to force a separate instance.`,
        );
        process.exit(0);
      }
    } else {
      // Nothing running — become the owner. (Classic single-process behavior.)
      await startOwner(isStdio);
    }
    break;
}
