// SPDX-License-Identifier: GPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3.
// See <https://www.gnu.org/licenses/gpl-3.0.html>

import { Orchestrator } from "./orchestrator.js";
import { startMcpServer, startRestApi } from "./mcp/server.js";
import { logger } from "./logger.js";

async function main(): Promise<void> {
  logger.info("fac-eu-brief starting…");

  const orchestrator = new Orchestrator();
  await orchestrator.init();

  // Run REST API always; MCP stdio only when not in a TTY (i.e. when invoked by an MCP client)
  await startRestApi(orchestrator);

  if (!process.stdin.isTTY) {
    await startMcpServer(orchestrator);
  } else {
    logger.info(`Interactive terminal detected — MCP stdio server skipped. Use REST API on port ${process.env.API_PORT ?? "3101"}`);
  }
}

main().catch((err) => {
  logger.error("Fatal startup error", { error: err.message, stack: err.stack });
  process.exit(1);
});