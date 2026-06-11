// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * TypeDB 2.x client wrapper for multi-hop conflict-of-interest graph.
 *
 * TypeDB's n-ary relations and logic-programming inference rules detect conflicts
 * that flat substring matching cannot:
 *   Client A adverse to B, B is a subsidiary of C, we represent C → conflict.
 *
 * TypeDB is entirely optional. If TYPEDB_URL is unset, all methods are no-ops.
 */

import { readFile } from "fs/promises";
import { TypeDB, SessionType, TransactionType, TypeDBOptions } from "typedb-driver";
import type { TypeDBDriver, TypeDBSession, TypeDBTransaction } from "typedb-driver";

const logger = {
  info:  (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "info",  msg, ...ctx })),
  warn:  (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "warn",  msg, ...ctx })),
  error: (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "error", msg, ...ctx })),
  debug: (msg: string, ctx?: object) => console.log(JSON.stringify({ level: "debug", msg, ...ctx })),
};

export interface ConflictReport {
  clientAId:     string;
  clientAName:   string;
  clientBId:     string;
  clientBName:   string;
  matterANumber: string;
  matterBNumber: string;
  conflictPath:  string;
  detectedAt:    string;
}

/** Validates that a client/matter ID is safe for TypeQL string interpolation. */
const SAFE_ID_RE = /^[\w\-]{1,100}$/;

function assertSafeId(id: string, label: string): void {
  if (!SAFE_ID_RE.test(id)) {
    throw new Error(`Unsafe ${label}: must match /^[\\w\\-]{1,100}$/`);
  }
}

/** Validates TYPEDB_URL is in host:port format — no protocol injection. */
function assertSafeTypeDbUrl(url: string): string {
  if (!url) throw new Error("TYPEDB_URL is empty");
  // Must be host:port — no slashes, no protocol prefix
  if (/[/\\?#@]/.test(url)) {
    throw new Error(`TYPEDB_URL must be host:port format (e.g. 0.0.0.0:1729), got: ${url}`);
  }
  const parts = url.split(":");
  if (parts.length !== 2 || !parts[0] || !parts[1] || !/^\d+$/.test(parts[1])) {
    throw new Error(`TYPEDB_URL must be host:port format (e.g. 0.0.0.0:1729), got: ${url}`);
  }
  const port = parseInt(parts[1], 10);
  if (port < 1 || port > 65535) {
    throw new Error(`TYPEDB_URL port ${port} is out of range [1, 65535]`);
  }
  return url;
}

/** Extract a string attribute value from a ConceptMap result. */
function strVal(concept: unknown): string {
  return String((concept as { value: unknown }).value ?? "");
}

export class TypeDBConflictGraph {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  private driver: TypeDBDriver | null = null;
  private readonly db = "big_michael_legal";

  async connect(address: string): Promise<void> {
    try {
      const safeAddr = assertSafeTypeDbUrl(address);
      this.driver = await TypeDB.coreDriver(safeAddr);
      await this.ensureDb();
      logger.info("TypeDB conflict graph connected", { address: safeAddr, db: this.db });
    } catch (err) {
      logger.error("TypeDB connection failed — conflict graph disabled", { err: (err as Error).message });
      this.driver = null;
      throw err;
    }
  }

  async close(): Promise<void> {
    if (this.driver) {
      await this.driver.close();
      this.driver = null;
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

  async upsertMatter(
    matterNumber: string,
    practiceArea: string,
    jurisdiction: string,
    status: string,
  ): Promise<void> {
    assertSafeId(matterNumber, "matter-number");
    if (!this.driver) return;
    try {
      // Check if matter exists
      const exists = await this.withReadTx(async (tx) => {
        const stream = tx.query.get(`match $m isa matter, has matter-number "${matterNumber}"; get;`);
        const results = await stream.collect();
        return results.length > 0;
      });

      if (!exists) {
        await this.withWriteTx(async (tx) => {
          const safePa = practiceArea.replace(/"/g, "'");
          const safeJur = jurisdiction.replace(/"/g, "'");
          const safeSt = status.replace(/"/g, "'");
          await tx.query.insert(
            `insert $m isa matter, has matter-number "${matterNumber}", ` +
            `has practice-area "${safePa}", has jurisdiction "${safeJur}", ` +
            `has matter-status "${safeSt}";`,
          );
          await tx.commit();
        });
      }
    } catch (err) {
      logger.warn("TypeDB upsertMatter failed", { matterNumber, err: (err as Error).message });
    }
  }

  async setRepresents(clientId: string, matterNumber: string): Promise<void> {
    assertSafeId(clientId, "clientId");
    assertSafeId(matterNumber, "matterNumber");
    if (!this.driver) return;
    try {
      // Check if relation already exists
      const exists = await this.withReadTx(async (tx) => {
        const stream = tx.query.get(
          `match $c isa legal-party, has external-id "${clientId}"; ` +
          `$m isa matter, has matter-number "${matterNumber}"; ` +
          `(client: $c, matter: $m) isa represents; get;`,
        );
        const results = await stream.collect();
        return results.length > 0;
      });
      if (!exists) {
        await this.withWriteTx(async (tx) => {
          await tx.query.insert(
            `match $c isa legal-party, has external-id "${clientId}"; ` +
            `$m isa matter, has matter-number "${matterNumber}"; ` +
            `insert (client: $c, matter: $m) isa represents;`,
          );
          await tx.commit();
        });
      }
    } catch (err) {
      logger.warn("TypeDB setRepresents failed", { clientId, matterNumber, err: (err as Error).message });
    }
  }

  async setAdverseTo(clientId1: string, clientId2: string): Promise<void> {
    assertSafeId(clientId1, "clientId1");
    assertSafeId(clientId2, "clientId2");
    if (!this.driver) return;
    try {
      // Check both orderings — symmetric relation
      const exists = await this.withReadTx(async (tx) => {
        const stream = tx.query.get(
          `match $c1 isa legal-party, has external-id "${clientId1}"; ` +
          `$c2 isa legal-party, has external-id "${clientId2}"; ` +
          `(party: $c1, party: $c2) isa adverse-to; get;`,
        );
        const results = await stream.collect();
        return results.length > 0;
      });
      if (!exists) {
        await this.withWriteTx(async (tx) => {
          await tx.query.insert(
            `match $c1 isa legal-party, has external-id "${clientId1}"; ` +
            `$c2 isa legal-party, has external-id "${clientId2}"; ` +
            `insert (party: $c1, party: $c2) isa adverse-to;`,
          );
          await tx.commit();
        });
      }
    } catch (err) {
      logger.warn("TypeDB setAdverseTo failed", { clientId1, clientId2, err: (err as Error).message });
    }
  }

  async setSubsidiaryOf(childId: string, parentId: string): Promise<void> {
    assertSafeId(childId, "childId");
    assertSafeId(parentId, "parentId");
    if (!this.driver) return;
    try {
      const exists = await this.withReadTx(async (tx) => {
        const stream = tx.query.get(
          `match $child isa legal-party, has external-id "${childId}"; ` +
          `$parent isa legal-party, has external-id "${parentId}"; ` +
          `(child: $child, parent: $parent) isa subsidiary-of; get;`,
        );
        const results = await stream.collect();
        return results.length > 0;
      });
      if (!exists) {
        await this.withWriteTx(async (tx) => {
          await tx.query.insert(
            `match $child isa legal-party, has external-id "${childId}"; ` +
            `$parent isa legal-party, has external-id "${parentId}"; ` +
            `insert (child: $child, parent: $parent) isa subsidiary-of;`,
          );
          await tx.commit();
        });
      }
    } catch (err) {
      logger.warn("TypeDB setSubsidiaryOf failed", { childId, parentId, err: (err as Error).message });
    }
  }

  async queryConflicts(clientId?: string): Promise<ConflictReport[]> {
    if (!this.driver) return [];
    if (clientId) assertSafeId(clientId, "clientId");
    try {
      const clientFilter = clientId
        ? `{ $ca has external-id "${clientId}"; } or { $cb has external-id "${clientId}"; };`
        : "";

      const query =
        `match ` +
        `$conf (client-a: $ca, client-b: $cb, matter-a: $ma, matter-b: $mb) isa conflict; ` +
        `$ca has external-id $ca-id; $ca has entity-name $ca-name; ` +
        `$cb has external-id $cb-id; $cb has entity-name $cb-name; ` +
        `$ma has matter-number $ma-num; ` +
        `$mb has matter-number $mb-num; ` +
        (clientFilter ? clientFilter + " " : "") +
        `get $ca-id, $ca-name, $cb-id, $cb-name, $ma-num, $mb-num;`;

      return await this.withReadTx(async (tx) => {
        const stream = tx.query.get(query);
        const results = await stream.collect();
        return results.map((cm) => {
          return {
            clientAId:     strVal(cm.get("ca-id")),
            clientAName:   strVal(cm.get("ca-name")),
            clientBId:     strVal(cm.get("cb-id")),
            clientBName:   strVal(cm.get("cb-name")),
            matterANumber: strVal(cm.get("ma-num")),
            matterBNumber: strVal(cm.get("mb-num")),
            conflictPath:  "inferred",
            detectedAt:    new Date().toISOString(),
          } satisfies ConflictReport;
        });
      }, true /* infer */);
    } catch (err) {
      logger.warn("TypeDB queryConflicts failed", { clientId, err: (err as Error).message });
      return [];
    }
  }

  async syncFromClients(
    clients: Array<{ id: string; name: string; adversaries: string[]; matters: Array<{ matterNumber: string; practiceArea?: string }> }>,
    matters: Array<{ matterNumber: string; practiceArea?: string; jurisdiction?: string; status?: string }>,
  ): Promise<void> {
    if (!this.driver) return;
    logger.info("TypeDB: syncing conflict graph", { clients: clients.length, matters: matters.length });

    // Per-item isolation: one malformed id (assertSafeId throws before the
    // inner try/catch in the upsert helpers) must not abort the whole sync.
    const safely = async (label: string, fn: () => Promise<void>): Promise<void> => {
      try {
        await fn();
      } catch (err) {
        logger.warn(`TypeDB sync: skipped ${label}`, { err: (err as Error).message });
      }
    };

    // Upsert all matters first
    for (const m of matters) {
      await safely(`matter ${m.matterNumber}`, () =>
        this.upsertMatter(m.matterNumber, m.practiceArea ?? "", m.jurisdiction ?? "", m.status ?? "active"));
    }

    // Upsert all clients as companies
    for (const c of clients) {
      await safely(`client ${c.id}`, () => this.upsertCompany(c.id, c.name));
    }

    // Build represents + adverse-to relations
    for (const c of clients) {
      for (const m of c.matters) {
        await safely(`represents ${c.id}/${m.matterNumber}`, () => this.setRepresents(c.id, m.matterNumber));
      }
      // Adversaries: upsert as natural-person if not already in graph, then setAdverseTo
      for (const adversaryName of c.adversaries) {
        // Use a deterministic id: slug the name to a safe id
        const adversaryId = slugId(adversaryName);
        await safely(`adversary ${adversaryId}`, async () => {
          await this.upsertPerson(adversaryId, adversaryName);
          await this.setAdverseTo(c.id, adversaryId);
        });
      }
    }

    logger.info("TypeDB: conflict graph sync complete");
  }

  // ── Private helpers ──────────────────────────────────────────────────────────

  private async ensureDb(): Promise<void> {
    if (!this.driver) return;
    const exists = await this.driver.databases.contains(this.db);
    if (!exists) {
      await this.driver.databases.create(this.db);
      logger.info("TypeDB: database created", { db: this.db });
      await this.defineSchema();
    }
  }

  private async defineSchema(): Promise<void> {
    if (!this.driver) return;
    const schemaUrl = new URL("./schema.tql", import.meta.url);
    const schema = await readFile(schemaUrl, "utf8");
    const session: TypeDBSession = await this.driver.session(this.db, SessionType.SCHEMA);
    try {
      const tx: TypeDBTransaction = await session.transaction(TransactionType.WRITE);
      try {
        await tx.query.define(schema);
        await tx.commit();
        logger.info("TypeDB: schema defined");
      } catch (err) {
        await tx.close();
        throw err;
      }
    } finally {
      await session.close();
    }
  }

  private async withWriteTx<T>(fn: (tx: TypeDBTransaction) => Promise<T>): Promise<T> {
    if (!this.driver) throw new Error("TypeDB driver not connected");
    const session: TypeDBSession = await this.driver.session(this.db, SessionType.DATA);
    try {
      const tx: TypeDBTransaction = await session.transaction(TransactionType.WRITE);
      try {
        return await fn(tx);
      } catch (err) {
        await tx.close();
        throw err;
      }
    } finally {
      await session.close();
    }
  }

  private async withReadTx<T>(fn: (tx: TypeDBTransaction) => Promise<T>, infer = false): Promise<T> {
    if (!this.driver) throw new Error("TypeDB driver not connected");
    const session: TypeDBSession = await this.driver.session(this.db, SessionType.DATA);
    try {
      const opts = new TypeDBOptions();
      if (infer) opts.infer = true;
      const tx: TypeDBTransaction = await session.transaction(TransactionType.READ, opts);
      try {
        return await fn(tx);
      } finally {
        await tx.close();
      }
    } finally {
      await session.close();
    }
  }

  private async upsertParty(kind: "company" | "natural-person", id: string, name: string): Promise<void> {
    if (!this.driver) return;
    try {
      const exists = await this.withReadTx(async (tx) => {
        const stream = tx.query.get(`match $e isa legal-party, has external-id "${id}"; get;`);
        const results = await stream.collect();
        return results.length > 0;
      });
      if (!exists) {
        await this.withWriteTx(async (tx) => {
          const safeName = name.replace(/"/g, "'");
          await tx.query.insert(
            `insert $e isa ${kind}, has external-id "${id}", has entity-name "${safeName}";`,
          );
          await tx.commit();
        });
      }
    } catch (err) {
      logger.warn(`TypeDB upsert${kind} failed`, { id, err: (err as Error).message });
    }
  }
}

/** Slugify a name into a safe external-id (alphanumeric + hyphens, max 100 chars). */
function slugId(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 100) || "unknown";
}
