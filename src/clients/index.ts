// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

import { randomUUID } from "crypto";
import { readFile, writeFile, rename } from "fs/promises";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import type { Client, ClientMatter, ClientVoiceGuide, ConflictCheckResult } from "../types.js";

export class ClientStore {
  private readonly path = Config.persistence.clientsFile;
  private clients: Client[] = [];

  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      this.clients = (JSON.parse(raw) as Client[]).map((c) => ({
        ...c,
        createdAt: new Date(c.createdAt),
        updatedAt: new Date(c.updatedAt),
        matters: (c.matters || []).map((m) => ({ ...m, openedAt: new Date(m.openedAt) })),
      }));
      logger.info("Clients loaded", { count: this.clients.length });
    } catch {
      this.clients = [];
    }
  }

  list(): Client[] { return [...this.clients]; }
  get(id: string): Client | undefined { return this.clients.find((c) => c.id === id); }
  getByClientNumber(clientNumber: string): Client | undefined {
    return this.clients.find((c) => c.clientNumber.toLowerCase() === clientNumber.toLowerCase());
  }

  async create(input: {
    name: string;
    clientNumber: string;
    adversaries?: string[];
    notes?: string;
  }): Promise<Client> {
    const name = (input.name || "").trim().slice(0, 500);
    const clientNumber = (input.clientNumber || "").trim().slice(0, 50);
    if (!name || !clientNumber) throw new Error("name and clientNumber are required");
    if (this.getByClientNumber(clientNumber)) throw new Error(`Client number ${clientNumber} already exists`);
    const now = new Date();
    const client: Client = {
      id: randomUUID(),
      name,
      clientNumber,
      matters: [],
      adversaries: (input.adversaries || []).slice(0, 200).map((a) => a.trim().slice(0, 300)).filter(Boolean),
      notes: input.notes?.trim().slice(0, 2000) || undefined,
      createdAt: now,
      updatedAt: now,
    };
    this.clients.push(client);
    await this.persist();
    return client;
  }

  async update(id: string, patch: Partial<Pick<Client, "name" | "adversaries" | "notes">>): Promise<Client> {
    const c = this.get(id);
    if (!c) throw new Error("Client not found");
    if (typeof patch.name === "string" && patch.name.trim()) c.name = patch.name.trim().slice(0, 500);
    if (Array.isArray(patch.adversaries)) c.adversaries = patch.adversaries.slice(0, 200).map((a) => a.trim().slice(0, 300)).filter(Boolean);
    if (typeof patch.notes === "string") c.notes = patch.notes.trim().slice(0, 2000) || undefined;
    c.updatedAt = new Date();
    await this.persist();
    return c;
  }

  async addMatter(clientId: string, input: {
    matterNumber: string;
    description: string;
    practiceArea?: string;
  }): Promise<ClientMatter> {
    const c = this.get(clientId);
    if (!c) throw new Error("Client not found");
    if (c.matters.some((m) => m.matterNumber === input.matterNumber)) {
      throw new Error(`Matter ${input.matterNumber} already exists for this client`);
    }
    const matter: ClientMatter = {
      matterNumber: input.matterNumber.trim().slice(0, 50),
      description: (input.description || "").trim().slice(0, 2000),
      practiceArea: input.practiceArea?.trim().slice(0, 200) || undefined,
      openedAt: new Date(),
    };
    c.matters.push(matter);
    c.updatedAt = new Date();
    await this.persist();
    return matter;
  }

  async removeMatter(clientId: string, matterNumber: string): Promise<boolean> {
    const c = this.get(clientId);
    if (!c) throw new Error("Client not found");
    const before = c.matters.length;
    c.matters = c.matters.filter((m) => m.matterNumber !== matterNumber);
    if (c.matters.length === before) return false;
    c.updatedAt = new Date();
    await this.persist();
    return true;
  }

  async remove(id: string): Promise<boolean> {
    const before = this.clients.length;
    this.clients = this.clients.filter((c) => c.id !== id);
    if (this.clients.length === before) return false;
    await this.persist();
    return true;
  }

  async setOcg(clientId: string, ocgId: string): Promise<Client> {
    const c = this.get(clientId);
    if (!c) throw new Error("Client not found");
    c.ocgId = ocgId;
    c.updatedAt = new Date();
    await this.persist();
    return c;
  }

  async clearOcg(clientId: string): Promise<Client> {
    const c = this.get(clientId);
    if (!c) throw new Error("Client not found");
    delete c.ocgId;
    c.updatedAt = new Date();
    await this.persist();
    return c;
  }

  async setVoiceGuide(clientId: string, guide: ClientVoiceGuide): Promise<Client> {
    const c = this.get(clientId);
    if (!c) throw new Error("Client not found");
    c.voiceGuide = guide;
    c.updatedAt = new Date();
    await this.persist();
    return c;
  }

  async clearVoiceGuide(clientId: string): Promise<Client> {
    const c = this.get(clientId);
    if (!c) throw new Error("Client not found");
    delete c.voiceGuide;
    c.updatedAt = new Date();
    await this.persist();
    return c;
  }

  setMatterBudget(
    clientId: string,
    matterNumber: string,
    budgetUsd: number,
    thresholds?: number[],
  ): ClientMatter | undefined {
    if (!Number.isFinite(budgetUsd) || budgetUsd <= 0) {
      throw new Error(`Invalid budget: ${budgetUsd} — must be a positive finite number`);
    }
    const client = this.clients.find((c) => c.id === clientId);
    if (!client) return undefined;
    const matter = client.matters.find((m) => m.matterNumber === matterNumber);
    if (!matter) return undefined;
    matter.budgetUsd = budgetUsd;
    matter.budgetAlertThresholds = thresholds ?? [0.5, 0.8, 1.0];
    matter.budgetAlertsTriggered = [];
    this.persist().catch((err: Error) =>
      logger.warn("Failed to persist matter budget", { error: err.message })
    );
    return matter;
  }

  /**
   * Normalize an entity name for conflict matching: lowercase, strip punctuation
   * and common corporate suffixes (Inc/LLC/Ltd/…), collapse whitespace. This lets
   * "Acme Inc." match "Acme Corporation". Conflict checks should err toward
   * flagging, so over-matching here is acceptable (it just triggers human review).
   */
  private static norm(s: string): string {
    return s
      .toLowerCase()
      .replace(/[.,&]/g, " ")
      .replace(/\b(inc|llc|llp|ltd|limited|corp|corporation|co|company|plc|gmbh|sa|nv|lp|group|holdings)\b/g, " ")
      .replace(/\s+/g, " ")
      .trim();
  }

  /**
   * Check whether onboarding `newClientName` (optionally with the new client's
   * own `newAdversaries`) conflicts with the existing roster. Checks both
   * directions:
   *   1. the new client appears on an existing client's adversary list, and
   *   2. an adversary of the new client is itself an existing client (taking the
   *      matter would put us adverse to a current client).
   */
  checkConflict(newClientName: string, newAdversaries: string[] = []): ConflictCheckResult {
    const name = ClientStore.norm(newClientName);
    if (!name) return { hasConflict: false };

    // 1. New client name vs existing clients' adversary lists.
    for (const c of this.clients) {
      for (const adv of c.adversaries) {
        const a = ClientStore.norm(adv);
        if (!a || a.length < 3) continue;
        if (a.includes(name) || name.includes(a)) {
          return { hasConflict: true, conflictingClientId: c.id, conflictingClientName: c.name, matchedAdversary: adv };
        }
      }
    }

    // 2. New client's adversaries vs existing client names.
    const advNorms = newAdversaries
      .map((a) => ({ raw: a, norm: ClientStore.norm(a) }))
      .filter((a) => a.norm.length >= 3);
    for (const c of this.clients) {
      const cn = ClientStore.norm(c.name);
      if (!cn || cn.length < 3) continue;
      for (const adv of advNorms) {
        if (adv.norm.includes(cn) || cn.includes(adv.norm)) {
          return { hasConflict: true, conflictingClientId: c.id, conflictingClientName: c.name, matchedAdversary: adv.raw };
        }
      }
    }

    return { hasConflict: false };
  }

  async persist(): Promise<void> {
    const tmp = `${this.path}.tmp`;
    await writeFile(tmp, JSON.stringify(this.clients, null, 2), "utf8");
    await rename(tmp, this.path);
  }
}
