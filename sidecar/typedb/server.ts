// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * TypeDB conflict-graph sidecar.
 *
 * Communicates with the Go core via a Unix domain socket — no TCP port,
 * no accidental network exposure, lower latency than loopback.
 *
 * Socket path: TYPEDB_SOCKET (default /run/biglaw/typedb.sock)
 * TypeDB address: TYPEDB_URL  (required, host:port e.g. 0.0.0.0:1729)
 *
 * API:
 *   GET  /health                    → { ok, connected }
 *   POST /sync                      → { clients, matters }
 *   GET  /conflicts?clientId=xxx    → ConflictReport[]
 *   POST /check-new-matter          → { clientId, adversaryIds } → ConflictReport[]
 */

import { existsSync, mkdirSync, unlinkSync } from "fs";
import { dirname } from "path";
import Fastify from "fastify";
import { TypeDBConflictGraph, type ConflictReport } from "./typedb.js";

const socketPath = process.env.TYPEDB_SOCKET ?? "/run/biglaw/typedb.sock";
const typedbUrl  = process.env.TYPEDB_URL ?? "";

if (!typedbUrl) {
  console.error(JSON.stringify({ level: "error", msg: "TYPEDB_URL is required" }));
  process.exit(1);
}

// Ensure socket directory exists and remove stale socket from previous run.
mkdirSync(dirname(socketPath), { recursive: true });
if (existsSync(socketPath)) unlinkSync(socketPath);

const graph = new TypeDBConflictGraph();
let connected = false;

const app = Fastify({ logger: false });

// ─── Health ───────────────────────────────────────────────────────────────────

app.get("/health", async () => ({ ok: true, connected }));

// ─── Sync ─────────────────────────────────────────────────────────────────────

interface SyncBody {
  clients: Array<{
    id: string;
    name: string;
    adversaries: string[];
    matters: Array<{ matterNumber: string; practiceArea?: string }>;
  }>;
  matters: Array<{
    matterNumber: string;
    practiceArea?: string;
    jurisdiction?: string;
    status?: string;
  }>;
}

app.post<{ Body: SyncBody }>("/sync", async (req, reply) => {
  if (!connected) return reply.status(503).send({ error: "TypeDB not connected" });
  try {
    await graph.syncFromClients(req.body.clients, req.body.matters);
    return { ok: true };
  } catch (err) {
    return reply.status(500).send({ error: (err as Error).message });
  }
});

// ─── Query conflicts ──────────────────────────────────────────────────────────

app.get<{ Querystring: { clientId?: string } }>("/conflicts", async (req, reply) => {
  if (!connected) return reply.status(503).send({ error: "TypeDB not connected" });
  try {
    return await graph.queryConflicts(req.query.clientId) as ConflictReport[];
  } catch (err) {
    return reply.status(500).send({ error: (err as Error).message });
  }
});

// ─── Check new matter ─────────────────────────────────────────────────────────

interface CheckBody { clientId: string; adversaryIds: string[] }

app.post<{ Body: CheckBody }>("/check-new-matter", async (req, reply) => {
  if (!connected) return reply.status(503).send({ error: "TypeDB not connected" });
  try {
    const { clientId, adversaryIds } = req.body;
    const out: ConflictReport[] = [];
    for (const advId of adversaryIds) {
      for (const c of await graph.queryConflicts(advId)) {
        if (
          (c.clientAId === clientId && c.clientBId === advId) ||
          (c.clientBId === clientId && c.clientAId === advId)
        ) out.push(c);
      }
    }
    return out;
  } catch (err) {
    return reply.status(500).send({ error: (err as Error).message });
  }
});

// ─── Startup ──────────────────────────────────────────────────────────────────

async function start(): Promise<void> {
  try {
    await graph.connect(typedbUrl);
    connected = true;
    console.log(JSON.stringify({ level: "info", msg: "TypeDB connected", url: typedbUrl }));
  } catch (err) {
    console.log(JSON.stringify({
      level: "warn",
      msg: "TypeDB connect failed — will retry on next call",
      err: (err as Error).message,
    }));
  }

  await app.listen({ path: socketPath });
  console.log(JSON.stringify({ level: "info", msg: "TypeDB sidecar listening", socket: socketPath }));
}

process.on("SIGTERM", async () => {
  await graph.close();
  await app.close();
  if (existsSync(socketPath)) unlinkSync(socketPath);
  process.exit(0);
});

start().catch((err) => {
  console.error(JSON.stringify({ level: "fatal", msg: String(err) }));
  process.exit(1);
});
