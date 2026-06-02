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
const { logger }                    = await import("./logger.js");
const { Orchestrator }              = await import("./orchestrator.js");
const { startMcpServer, startRestApi } = await import("./mcp/server.js");

logger.info("Big Michael starting…");

const orchestrator = new Orchestrator();
await orchestrator.init();

await startRestApi(orchestrator);

if (!process.stdin.isTTY) {
  await startMcpServer(orchestrator);
} else {
  logger.info(
    `Interactive terminal — MCP stdio skipped. REST API on port ${process.env.API_PORT ?? "3101"}`,
  );
}
