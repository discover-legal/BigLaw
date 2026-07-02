// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * TypeDB 3.x client for the conflict-of-interest graph.
 *
 * Talks to TypeDB 3.x over its HTTP /v1 API using Node's global `fetch` — ZERO
 * runtime dependencies. This deliberately does not use a TypeDB driver package:
 * the 2.x `typedb-driver` pulled vulnerable grpc-js/uuid, and even the 3.x
 * `@typedb/driver-http` is an extra dependency. The HTTP surface we use here
 * (signin → one-shot query) was validated end-to-end against typedb 3.11.5.
 *
 * Why TypeDB at all: its n-ary relations and — in 3.x — recursive FUNCTIONS
 * detect conflicts flat substring matching cannot, e.g. a party several hops up
 * a corporate-control tree from a client (see `descendants` in schema.tql).
 *
 * 3.x notes baked in:
 *   - Rules were removed; conflict logic is inlined match patterns + the
 *     `descendants` function (transitive corporate family).
 *   - Auth is required: signin returns a bearer token (cached, refreshed on 401).
 *   - TYPEDB_URL is a full http(s) URL to the HTTP endpoint (3.x default :8000).
 *
 * TypeDB is entirely optional. If unreachable, the sidecar reports
 * disconnected and the Go core degrades silently.
 */

import { readFile } from "fs/promises";

const logger = {
  info: (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "info", msg, ...ctx })),
  warn: (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "warn", msg, ...ctx })),
  error: (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "error", msg, ...ctx })),
};

export interface ConflictReport {
  clientAId: string;
  clientAName: string;
  clientBId: string;
  clientBName: string;
  matterANumber: string;
  matterBNumber: string;
  conflictPath: string;
  detectedAt: string;
}

/** Validates a client/matter ID is safe for TypeQL string interpolation. */
const SAFE_ID_RE = /^[\w\-]{1,100}$/;
function assertSafeId(id: string, label: string): void {
  if (!SAFE_ID_RE.test(id)) {
    throw new Error(`Unsafe ${label}: must match /^[\\w\\-]{1,100}$/`);
  }
}

/**
 * Validates TYPEDB_URL and returns it normalised to an `http(s)://host:port`
 * origin. Accepts a bare `host:port` (assumes http) or a full URL. Rejects
 * anything with a path/query/fragment or injection characters. ReDoS-safe:
 * uses URL parsing + a bounded character check, no backtracking regex.
 */
export function assertSafeTypeDbUrl(url: string): string {
  if (!url) throw new Error("TYPEDB_URL is empty");
  if (url.length > 255) throw new Error("TYPEDB_URL too long");
  const withScheme = /^https?:\/\//i.test(url) ? url : `http://${url}`;
  let parsed: URL;
  try {
    parsed = new URL(withScheme);
  } catch {
    throw new Error(`TYPEDB_URL is not a valid host:port or URL: ${url}`);
  }
  if (parsed.pathname !== "/" && parsed.pathname !== "") {
    throw new Error(`TYPEDB_URL must not contain a path: ${url}`);
  }
  if (parsed.search || parsed.hash || parsed.username || parsed.password) {
    throw new Error(`TYPEDB_URL must be a bare origin (no query/credentials): ${url}`);
  }
  if (!parsed.hostname || !parsed.port || !/^\d+$/.test(parsed.port)) {
    throw new Error(`TYPEDB_URL must be host:port: ${url}`);
  }
  const port = Number(parsed.port);
  if (port < 1 || port > 65535) throw new Error(`TYPEDB_URL port out of range: ${port}`);
  return `${parsed.protocol}//${parsed.host}`;
}

/** Escapes a string value for safe embedding inside a TypeQL double-quoted literal. */
function q(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

interface QueryRow {
  data: Record<string, { value?: unknown }>;
}

export class TypeDBConflictGraph {
  private origin = "";
  private user = "";
  private pass = "";
  private token: string | null = null;
  private readonly db = "big_michael_legal";

  async connect(address: string, username = "admin", password = "password"): Promise<void> {
    this.origin = assertSafeTypeDbUrl(address);
    this.user = username;
    this.pass = password;
    await this.signin();
    await this.ensureDb();
    logger.info("TypeDB 3.x conflict graph connected", { origin: this.origin, db: this.db });
  }

  async close(): Promise<void> {
    this.token = null;
  }

  // ── HTTP layer ────────────────────────────────────────────────────────────

  private async signin(): Promise<void> {
    const res = await fetch(`${this.origin}/v1/signin`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: this.user, password: this.pass }),
    });
    if (!res.ok) throw new Error(`TypeDB signin failed: HTTP ${res.status}`);
    const body = (await res.json()) as { token?: string };
    if (!body.token) throw new Error("TypeDB signin returned no token");
    this.token = body.token;
  }

  /** POST a one-shot query. Refreshes the token once on a 401 and retries. */
  private async query(
    txType: "read" | "write" | "schema",
    query: string,
    commit: boolean,
    retry = true,
  ): Promise<QueryRow[]> {
    if (!this.token) await this.signin();
    const res = await fetch(`${this.origin}/v1/query`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${this.token}` },
      body: JSON.stringify({ databaseName: this.db, transactionType: txType, query, commit }),
    });
    if (res.status === 401 && retry) {
      await this.signin();
      return this.query(txType, query, commit, false);
    }
    const body = (await res.json()) as {
      answerType?: string;
      answers?: QueryRow[];
      code?: string;
      message?: string;
    };
    if (body.code) throw new Error(`${body.code}: ${(body.message ?? "").split("\n")[0]}`);
    return body.answers ?? [];
  }

  private async ensureDb(): Promise<void> {
    // Creating an existing database returns a non-2xx; only load the schema on a
    // fresh create so we don't redefine types on every connect.
    const res = await fetch(`${this.origin}/v1/databases/${this.db}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${this.token}` },
    });
    if (res.status >= 200 && res.status < 300) {
      const schemaUrl = new URL("./schema.tql", import.meta.url);
      const schema = await readFile(schemaUrl, "utf8");
      await this.query("schema", schema, true);
      logger.info("TypeDB: database created + schema loaded", { db: this.db });
    }
  }

  private static val(row: QueryRow, name: string): string {
    return String(row.data[name]?.value ?? "");
  }

  // ── Writes (enriched conflicts model) ───────────────────────────────────────

  private async upsertParty(kind: "company" | "natural-person", id: string, name: string): Promise<void> {
    const existing = await this.query("read", `match $e isa legal-party, has external-id "${q(id)}"; select $e;`, false);
    if (existing.length === 0) {
      await this.query("write", `insert $e isa ${kind}, has external-id "${q(id)}", has entity-name "${q(name)}";`, true);
    }
  }

  async upsertCompany(id: string, name: string): Promise<void> {
    assertSafeId(id, "company id");
    await this.upsertParty("company", id, name);
  }

  async upsertPerson(id: string, name: string): Promise<void> {
    assertSafeId(id, "person id");
    await this.upsertParty("natural-person", id, name);
  }

  async upsertMatter(matterNumber: string, practiceArea: string, status: string): Promise<void> {
    assertSafeId(matterNumber, "matter-number");
    const existing = await this.query(
      "read",
      `match $m isa matter, has matter-number "${q(matterNumber)}"; select $m;`,
      false,
    );
    if (existing.length === 0) {
      await this.query(
        "write",
        `insert $m isa matter, has matter-number "${q(matterNumber)}", ` +
          `has practice-area "${q(practiceArea)}", has matter-status "${q(status)}";`,
        true,
      );
    }
  }

  /** A party's role in a matter, with alignment side and whether the firm acts for it. */
  async addParticipation(
    partyId: string,
    matterNumber: string,
    role: string,
    side: string,
    actsForFirm: boolean,
  ): Promise<void> {
    assertSafeId(partyId, "partyId");
    assertSafeId(matterNumber, "matterNumber");
    const exists = await this.query(
      "read",
      `match $p isa legal-party, has external-id "${q(partyId)}"; ` +
        `$m isa matter, has matter-number "${q(matterNumber)}"; ` +
        `(participant: $p, in-matter: $m) isa participation; select $p;`,
      false,
    );
    if (exists.length === 0) {
      await this.query(
        "write",
        `match $p isa legal-party, has external-id "${q(partyId)}"; ` +
          `$m isa matter, has matter-number "${q(matterNumber)}"; ` +
          `insert (participant: $p, in-matter: $m) isa participation, ` +
          `has party-role "${q(role)}", has side "${q(side)}", has acts-for-firm ${actsForFirm};`,
        true,
      );
    }
  }

  /** Standing/blanket declared adversity (no specific matter) — the old adversaries[] list. */
  async setAdverseDecl(id1: string, id2: string): Promise<void> {
    assertSafeId(id1, "id1");
    assertSafeId(id2, "id2");
    const exists = await this.query(
      "read",
      `match $a isa legal-party, has external-id "${q(id1)}"; ` +
        `$b isa legal-party, has external-id "${q(id2)}"; ` +
        `(party: $a, party: $b) isa adverse-decl; select $a;`,
      false,
    );
    if (exists.length === 0) {
      await this.query(
        "write",
        `match $a isa legal-party, has external-id "${q(id1)}"; ` +
          `$b isa legal-party, has external-id "${q(id2)}"; ` +
          `insert (party: $a, party: $b) isa adverse-decl;`,
        true,
      );
    }
  }

  /** Corporate control: parent owns/controls child (walked transitively by descendants()). */
  async setControl(childId: string, parentId: string): Promise<void> {
    assertSafeId(childId, "childId");
    assertSafeId(parentId, "parentId");
    const exists = await this.query(
      "read",
      `match $c isa legal-party, has external-id "${q(childId)}"; ` +
        `$p isa legal-party, has external-id "${q(parentId)}"; ` +
        `(parent: $p, child: $c) isa control; select $c;`,
      false,
    );
    if (exists.length === 0) {
      await this.query(
        "write",
        `match $c isa legal-party, has external-id "${q(childId)}"; ` +
          `$p isa legal-party, has external-id "${q(parentId)}"; ` +
          `insert (parent: $p, child: $c) isa control;`,
        true,
      );
    }
  }

  // ── Conflict detection ──────────────────────────────────────────────────────

  /**
   * Conflicts where the firm acts for two parties who are adverse — directly, or
   * because a member of one party's corporate family is declared adverse to a
   * member of the other's. The corporate-family reach is the recursive
   * descendants() function: this is what a flat adversary list cannot do.
   */
  async queryConflicts(clientId?: string): Promise<ConflictReport[]> {
    if (clientId) assertSafeId(clientId, "clientId");
    const clientFilter = clientId
      ? `{ $a has external-id "${q(clientId)}"; } or { $b has external-id "${q(clientId)}"; };`
      : "";
    const query =
      `match ` +
      `$a isa legal-party, has external-id $aid, has entity-name $aname; ` +
      `$b isa legal-party, has external-id $bid, has entity-name $bname; ` +
      `(participant: $a, in-matter: $ma) isa participation, has acts-for-firm true; ` +
      `(participant: $b, in-matter: $mb) isa participation, has acts-for-firm true; ` +
      `$ma has matter-number $man; $mb has matter-number $mbn; ` +
      `not { $a is $b; }; ` +
      `$p isa legal-party; $r isa legal-party; ` +
      `{ $p is $a; } or { let $p in descendants($a); } or { let $a in descendants($p); }; ` +
      `{ $r is $b; } or { let $r in descendants($b); } or { let $b in descendants($r); }; ` +
      `(party: $p, party: $r) isa adverse-decl; ` +
      clientFilter +
      `select $aid, $aname, $bid, $bname, $man, $mbn;`;
    const rows = await this.query("read", query, false);
    const seen = new Set<string>();
    const out: ConflictReport[] = [];
    for (const row of rows) {
      const v = (n: string) => TypeDBConflictGraph.val(row, n);
      // Symmetric: collapse (A,B) and (B,A) into one report.
      const key = [v("aid"), v("man"), v("bid"), v("mbn")].sort().join("|");
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({
        clientAId: v("aid"),
        clientAName: v("aname"),
        clientBId: v("bid"),
        clientBName: v("bname"),
        matterANumber: v("man"),
        matterBNumber: v("mbn"),
        conflictPath: "inferred",
        detectedAt: new Date().toISOString(),
      });
    }
    return out;
  }

  /**
   * Read-only check for a PROPOSED new adversary: would being adverse to
   * `adversaryId` collide with an existing client, directly or through the
   * corporate family? Writes nothing. This is the corporate-tree showcase:
   * the adversary need not be a client or on any list — only related to one.
   */
  async checkProposedAdverse(adversaryId: string): Promise<ConflictReport[]> {
    assertSafeId(adversaryId, "adversaryId");
    const query =
      `match ` +
      `$client isa legal-party, has external-id $cid, has entity-name $cname; ` +
      `(participant: $client, in-matter: $m) isa participation, has acts-for-firm true; ` +
      `$m has matter-number $mn; ` +
      `$target isa legal-party, has external-id "${q(adversaryId)}", has entity-name $tname; ` +
      `not { $client is $target; }; ` +
      `$root isa legal-party; ` +
      `{ $client is $root; } or { let $client in descendants($root); }; ` +
      `{ $target is $root; } or { let $target in descendants($root); }; ` +
      `select $cid, $cname, $mn, $tname;`;
    const rows = await this.query("read", query, false);
    const seen = new Set<string>();
    const out: ConflictReport[] = [];
    for (const row of rows) {
      const v = (n: string) => TypeDBConflictGraph.val(row, n);
      const key = `${v("cid")}|${v("mn")}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({
        clientAId: v("cid"),
        clientAName: v("cname"),
        clientBId: adversaryId,
        clientBName: v("tname"),
        matterANumber: v("mn"),
        matterBNumber: "(proposed)",
        conflictPath: "corporate-family",
        detectedAt: new Date().toISOString(),
      });
    }
    return out;
  }

  // ── Sync ────────────────────────────────────────────────────────────────────

  /**
   * Maps the Go core's client/matter data into the enriched schema:
   *   client                 → company (legal-party)
   *   client represents M    → participation(acts-for-firm = true, role "client")
   *   client.adversaries[]   → adverse-decl (standing declared adversity)
   * Corporate family (control/affiliation), per-matter sides, and JDAs are
   * populated by the richer intake path when present; this baseline keeps the
   * existing flat conflict screen working on the 3.x stack.
   */
  async syncFromClients(
    clients: Array<{ id: string; name: string; adversaries: string[]; matters: Array<{ matterNumber: string; practiceArea?: string }> }>,
    matters: Array<{ matterNumber: string; practiceArea?: string; jurisdiction?: string; status?: string }>,
  ): Promise<void> {
    logger.info("TypeDB: syncing conflict graph", { clients: clients.length, matters: matters.length });

    const safely = async (label: string, fn: () => Promise<void>): Promise<void> => {
      try {
        await fn();
      } catch (err) {
        logger.warn(`TypeDB sync: skipped ${label}`, { err: (err as Error).message });
      }
    };

    for (const m of matters) {
      await safely(`matter ${m.matterNumber}`, () =>
        this.upsertMatter(m.matterNumber, m.practiceArea ?? "", m.status ?? "active"));
    }
    for (const c of clients) {
      await safely(`client ${c.id}`, () => this.upsertCompany(c.id, c.name));
    }
    for (const c of clients) {
      for (const m of c.matters) {
        await safely(`participation ${c.id}/${m.matterNumber}`, () =>
          this.addParticipation(c.id, m.matterNumber, "client", `client:${c.id}`, true));
      }
      for (const adversaryName of c.adversaries) {
        const adversaryId = slugId(adversaryName);
        await safely(`adversary ${adversaryId}`, async () => {
          await this.upsertPerson(adversaryId, adversaryName);
          await this.setAdverseDecl(c.id, adversaryId);
        });
      }
    }
    logger.info("TypeDB: conflict graph sync complete");
  }
}

/** Slugify a name into a safe external-id (alphanumeric + hyphens, max 100 chars). */
export function slugId(name: string): string {
  return name
    .slice(0, 200) // bound input before any regex — ReDoS guard on uncontrolled names
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-") // collapses runs, so no consecutive hyphens remain
    .replace(/^-|-$/g, "") // single-char trim (no quantifier) — can't backtrack
    .slice(0, 100) || "unknown";
}
