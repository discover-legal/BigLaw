// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import { EventEmitter } from "events";
import type { TimeStore } from "../time/index.js";
import type { ClientStore } from "../clients/index.js";
import type { BudgetAlert } from "../types.js";
import { logger } from "../logger.js";

export class BudgetMonitor extends EventEmitter {
  constructor(private time: TimeStore, private clients: ClientStore) {
    super();
  }

  checkMatter(matterNumber: string): void {
    const client = this.clients.list().find((c) =>
      c.matters.some((m) => m.matterNumber === matterNumber)
    );
    if (!client) return;
    const matter = client.matters.find((m) => m.matterNumber === matterNumber);
    if (!matter || !matter.budgetUsd) return;

    const entries = this.time.list({ matterNumber }).filter((e) => e.endedAt);
    const burnUsd = entries.reduce((sum, e) => sum + (e.billingAmountUsd ?? 0), 0);
    const burnPct = burnUsd / matter.budgetUsd;

    const thresholds = matter.budgetAlertThresholds ?? [0.5, 0.8, 1.0];
    const normalize = (n: number) => Math.round(n * 1e6) / 1e6;
    const alreadyTriggered = new Set((matter.budgetAlertsTriggered ?? []).map(normalize));

    for (const threshold of thresholds) {
      const tNorm = normalize(threshold);
      if (burnPct >= threshold && !alreadyTriggered.has(tNorm)) {
        alreadyTriggered.add(tNorm);
        const alert: BudgetAlert = {
          matterNumber,
          clientNumber: client.clientNumber,
          budgetUsd: matter.budgetUsd,
          burnUsd: parseFloat(burnUsd.toFixed(2)),
          burnPct: parseFloat(burnPct.toFixed(4)),
          threshold,
          triggeredAt: new Date().toISOString(),
        };
        this.emit("alert", alert);
        logger.info("Budget threshold crossed", alert);
      }
    }

    if (alreadyTriggered.size !== (matter.budgetAlertsTriggered?.length ?? 0)) {
      matter.budgetAlertsTriggered = Array.from(alreadyTriggered);
      this.clients.persist().catch((err: Error) =>
        logger.warn("Failed to persist budget alert state", { error: err.message })
      );
    }
  }

  getBurn(matterNumber: string): { budgetUsd: number; burnUsd: number; burnPct: number; remaining: number } | null {
    const client = this.clients.list().find((c) =>
      c.matters.some((m) => m.matterNumber === matterNumber)
    );
    const matter = client?.matters.find((m) => m.matterNumber === matterNumber);
    if (!matter || !matter.budgetUsd) return null;
    const entries = this.time.list({ matterNumber }).filter((e) => e.endedAt);
    const burnUsd = parseFloat(entries.reduce((sum, e) => sum + (e.billingAmountUsd ?? 0), 0).toFixed(2));
    const burnPct = parseFloat((burnUsd / matter.budgetUsd).toFixed(4));
    return {
      budgetUsd: matter.budgetUsd,
      burnUsd,
      burnPct,
      remaining: parseFloat((matter.budgetUsd - burnUsd).toFixed(2)),
    };
  }
}
