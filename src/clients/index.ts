// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

import { randomUUID } from "crypto";
import { readFile } from "fs/promises";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { atomicWriteJson } from "../utils.js";
import type { Client, ClientMatter, ConflictCheckResult } from "../types.js";

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

  /** Check whether onboarding `newClientName` conflicts with existing clients. */
  checkConflict(newClientName: string): ConflictCheckResult {
    const name = newClientName.toLowerCase().trim();
    if (!name) return { hasConflict: false };
    for (const c of this.clients) {
      for (const adv of c.adversaries) {
        const advLower = adv.toLowerCase();
        if (!advLower) continue;
        if (advLower.includes(name) || name.includes(advLower)) {
          return {
            hasConflict: true,
            conflictingClientId: c.id,
            conflictingClientName: c.name,
            matchedAdversary: adv,
          };
        }
      }
      // Also check if an existing client's name matches a known adversary the new client might have
      if (c.name.toLowerCase().includes(name) || name.includes(c.name.toLowerCase())) {
        // Same client — no conflict
      }
    }
    return { hasConflict: false };
  }

  private async persist(): Promise<void> {
    await atomicWriteJson(this.path, this.clients);
  }
}
