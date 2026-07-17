// SPDX-License-Identifier: Apache-2.0
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

import { chmodSync, existsSync, mkdirSync, unlinkSync } from "fs";
import { dirname } from "path";
import Fastify from "fastify";
import { TypeDBConflictGraph, type ConflictReport } from "./typedb.js";

const socketPath = process.env.TYPEDB_SOCKET ?? "/run/biglaw/typedb.sock";
const typedbUrl  = process.env.TYPEDB_URL ?? "";
const typedbUsername = process.env.TYPEDB_USERNAME?.trim() ?? "";
const typedbPassword = process.env.TYPEDB_PASSWORD ?? "";

const missingTypeDbConfig = [
  ["TYPEDB_URL", typedbUrl],
  ["TYPEDB_USERNAME", typedbUsername],
  ["TYPEDB_PASSWORD", typedbPassword],
].filter(([, value]) => !value).map(([name]) => name);

if (missingTypeDbConfig.length > 0) {
  console.error(JSON.stringify({
    level: "error",
    msg: `${missingTypeDbConfig.join(", ")} ${missingTypeDbConfig.length === 1 ? "is" : "are"} required`,
  }));
  process.exit(1);
}

// Ensure socket directory exists and remove stale socket from previous run.
mkdirSync(dirname(socketPath), { recursive: true });
if (existsSync(socketPath)) unlinkSync(socketPath);

const graph = new TypeDBConflictGraph();
let connected = false;
let connecting: Promise<void> | null = null;

/**
 * Lazily (re)connect to TypeDB. Concurrent callers share one attempt.
 * Returns the current connection state.
 */
async function ensureConnected(): Promise<boolean> {
  if (connected) return true;
  connecting ??= graph
    .connect(typedbUrl, typedbUsername, typedbPassword)
    .then(() => {
      connected = true;
      console.log(JSON.stringify({ level: "info", msg: "TypeDB connected", url: typedbUrl }));
    })
    .catch((err: Error) => {
      console.log(JSON.stringify({ level: "warn", msg: "TypeDB reconnect failed", err: err.message }));
    })
    .finally(() => {
      connecting = null;
    });
  await connecting;
  return connected;
}

const app = Fastify({ logger: false });

// ─── Health ───────────────────────────────────────────────────────────────────

app.get("/health", async () => ({ ok: true, connected: await ensureConnected() }));

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
  if (!(await ensureConnected())) return reply.status(503).send({ error: "TypeDB not connected" });
  try {
    // The Go core marshals empty slices as null — normalise at the boundary.
    const clients = (req.body.clients ?? []).map((c) => ({
      ...c,
      adversaries: c.adversaries ?? [],
      matters: c.matters ?? [],
    }));
    await graph.syncFromClients(clients, req.body.matters ?? []);
    return { ok: true };
  } catch (err) {
    return reply.status(500).send({ error: (err as Error).message });
  }
});

// ─── Query conflicts ──────────────────────────────────────────────────────────

app.get<{ Querystring: { clientId?: string } }>("/conflicts", async (req, reply) => {
  if (!(await ensureConnected())) return reply.status(503).send({ error: "TypeDB not connected" });
  try {
    return await graph.queryConflicts(req.query.clientId) as ConflictReport[];
  } catch (err) {
    return reply.status(500).send({ error: (err as Error).message });
  }
});

// ─── Check new matter ─────────────────────────────────────────────────────────

interface CheckBody { clientId: string; adversaryIds: string[] }

app.post<{ Body: CheckBody }>("/check-new-matter", async (req, reply) => {
  if (!(await ensureConnected())) return reply.status(503).send({ error: "TypeDB not connected" });
  try {
    // For each proposed adversary, surface any existing client it collides with —
    // directly or transitively through the corporate-control tree (the reach a
    // flat adversary list cannot see). Read-only; writes nothing to the graph.
    const { adversaryIds } = req.body;
    const out: ConflictReport[] = [];
    for (const advId of adversaryIds ?? []) {
      out.push(...(await graph.checkProposedAdverse(advId)));
    }
    return out;
  } catch (err) {
    return reply.status(500).send({ error: (err as Error).message });
  }
});

// ─── Startup ──────────────────────────────────────────────────────────────────

async function start(): Promise<void> {
  // First attempt up-front; ensureConnected() retries on every later call.
  await ensureConnected();

  await app.listen({ path: socketPath });
  // The Go core may run as a different uid (separate container, shared
  // volume); connecting to a unix socket requires write permission on it.
  chmodSync(socketPath, 0o666);
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
