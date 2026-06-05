// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import { randomUUID } from "crypto";
import { readFile, writeFile, rename } from "fs/promises";
import { logger } from "../logger.js";
import type { PreBill, PreBillEntry, PreBillStatus, TimeEntry } from "../types.js";

export class PreBillStore {
  private readonly path: string;
  private bills: PreBill[] = [];
  private writeChain = Promise.resolve();

  constructor(path: string) {
    this.path = path;
  }

  async init(): Promise<void> {
    try {
      const raw = await readFile(this.path, "utf8");
      this.bills = JSON.parse(raw) as PreBill[];
      logger.info("Pre-bills loaded", { count: this.bills.length });
    } catch {
      this.bills = [];
    }
  }

  create(
    matterNumber: string,
    entries: TimeEntry[],
    createdByProfileId: string,
    clientNumber?: string,
  ): PreBill {
    const prebillEntries: PreBillEntry[] = entries.map((e) => {
      const ext = e as unknown as Record<string, unknown>;
      const ocgSuggestions = ext["ocgSuggestions"] as Array<{ status: string }> | undefined;
      return {
        entryId: e.id,
        description: e.description ?? "",
        billingUnits: e.billingUnits,
        billingRate: ext["billingRate"] as number | undefined,
        billingAmountUsd: ext["billingAmountUsd"] as number | undefined,
        utbmsTaskCode: ext["utbmsTaskCode"] as string | undefined,
        utbmsActivityCode: ext["utbmsActivityCode"] as string | undefined,
        profileName: ext["profileName"] as string | undefined,
        agentName: ext["agentName"] as string | undefined,
        startedAt: e.startedAt.toISOString(),
        endedAt: e.endedAt?.toISOString(),
        ocgSuggestionCount: ocgSuggestions?.filter((s) => s.status === "pending").length ?? 0,
      };
    });

    const totalBillingUnits = prebillEntries.reduce((s, e) => s + e.billingUnits, 0);
    const totalAmountUsd = parseFloat(
      prebillEntries.reduce((s, e) => s + (e.billingAmountUsd ?? 0), 0).toFixed(2),
    );

    const bill: PreBill = {
      id: randomUUID(),
      matterNumber,
      clientNumber,
      status: "draft",
      createdByProfileId,
      createdAt: new Date().toISOString(),
      entries: prebillEntries,
      totalBillingUnits,
      totalAmountUsd,
    };

    this.bills.push(bill);
    this.persist().catch((err: Error) => logger.warn("Failed to persist pre-bills", { error: err.message }));
    return bill;
  }

  list(matterNumber?: string): PreBill[] {
    if (matterNumber) return this.bills.filter((b) => b.matterNumber === matterNumber);
    return [...this.bills];
  }

  getById(id: string): PreBill | undefined {
    return this.bills.find((b) => b.id === id);
  }

  updateEntryDescription(billId: string, entryId: string, description: string): PreBill | undefined {
    const bill = this.bills.find((b) => b.id === billId);
    if (!bill || bill.status === "approved" || bill.status === "invoiced") return undefined;
    const entry = bill.entries.find((e) => e.entryId === entryId);
    if (!entry) return undefined;
    entry.description = description.slice(0, 500);
    this.persist().catch((err: Error) => logger.warn("Failed to persist pre-bills", { error: err.message }));
    return bill;
  }

  transition(billId: string, toStatus: PreBillStatus): PreBill | undefined {
    const bill = this.bills.find((b) => b.id === billId);
    if (!bill) return undefined;
    const valid: Record<PreBillStatus, PreBillStatus[]> = {
      draft: ["reviewed"],
      reviewed: ["approved", "draft"],
      approved: ["invoiced"],
      invoiced: [],
    };
    if (!valid[bill.status].includes(toStatus)) return undefined;
    bill.status = toStatus;
    const now = new Date().toISOString();
    if (toStatus === "reviewed") bill.reviewedAt = now;
    if (toStatus === "approved") bill.approvedAt = now;
    if (toStatus === "invoiced") bill.invoicedAt = now;
    this.persist().catch((err: Error) => logger.warn("Failed to persist pre-bills", { error: err.message }));
    return bill;
  }

  setNotes(billId: string, notes: string): PreBill | undefined {
    const bill = this.bills.find((b) => b.id === billId);
    if (!bill) return undefined;
    bill.notes = notes.slice(0, 2000);
    this.persist().catch((err: Error) => logger.warn("Failed to persist pre-bills", { error: err.message }));
    return bill;
  }

  private persist(): Promise<void> {
    this.writeChain = this.writeChain.then(() => this.doWrite()).catch(() => this.doWrite());
    return this.writeChain;
  }

  private async doWrite(): Promise<void> {
    const tmp = `${this.path}.tmp`;
    await writeFile(tmp, JSON.stringify(this.bills, null, 2), "utf8");
    await rename(tmp, this.path);
  }
}
