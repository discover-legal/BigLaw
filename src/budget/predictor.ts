// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

// BudgetPredictor — predicts final cost of an open matter from historical closed matters.
// Pure arithmetic — no LLM, no network.

import type { TimeStore } from "../time/index.js";
import type { Task } from "../types.js";
import { logger } from "../logger.js";

export interface MatterCostSample {
  matterNumber: string;
  practiceArea: string;
  jurisdiction: string;
  workflowType: string;
  totalBillingUnits: number;
  totalAmountUsd: number;
  entryCount: number;
  closedAt: Date;
}

export interface BudgetPrediction {
  matterNumber: string;
  practiceArea: string;
  spentUsd: number;
  spentBillingUnits: number;
  estimatedTotalUsd: number;
  estimatedRemainingUsd: number;
  completionPct: number;       // 0–200, current spend / predicted total (>100 = over budget)
  overBudget: boolean;         // true when spentUsd exceeds estimatedTotalUsd
  confidence: "high" | "medium" | "low" | "insufficient_data";
  comparableMatterCount: number;
  medianFinalCost: number;
  p25FinalCost: number;
  p75FinalCost: number;
  basedOn: "practice_area+jurisdiction" | "practice_area" | "all_matters";
}

export class BudgetPredictor {
  /**
   * Predict the final cost of an in-progress matter from historical closed-matter data.
   *
   * Returns null if there are no billing entries at all for the matter.
   */
  predict(
    matterNumber: string,
    timeStore: TimeStore,
    taskStore: Map<string, Task>,
  ): BudgetPrediction | null {
    // 1. Get all closed entries for this matterNumber → compute spent totals
    const matterEntries = timeStore.list({ matterNumber }).filter((e) => e.endedAt);
    if (matterEntries.length === 0) return null;

    const spentUsd = matterEntries.reduce((sum, e) => sum + (e.billingAmountUsd ?? 0), 0);
    const spentBillingUnits = matterEntries.reduce((sum, e) => sum + e.billingUnits, 0);

    // 3. Resolve the associated Task (look up by matterNumber)
    let task: Task | undefined;
    for (const t of taskStore.values()) {
      if (t.matterNumber === matterNumber) {
        task = t;
        break;
      }
    }

    const practiceArea = (task as (Task & { practiceArea?: string }) | undefined)?.practiceArea ?? "";
    const jurisdiction = task?.jurisdiction ?? "";

    // 4. Build comparable samples from closed matters, then select the most specific group
    const samples = this.buildSamples(timeStore, taskStore);

    let comparables: MatterCostSample[];
    let basedOn: BudgetPrediction["basedOn"];

    // Try most specific: practiceArea + jurisdiction
    const byAreaJurisdiction = samples.filter(
      (s) =>
        s.matterNumber !== matterNumber &&
        practiceArea !== "" &&
        jurisdiction !== "" &&
        s.practiceArea === practiceArea &&
        s.jurisdiction === jurisdiction,
    );

    if (byAreaJurisdiction.length >= 3) {
      comparables = byAreaJurisdiction;
      basedOn = "practice_area+jurisdiction";
    } else {
      // Fall back to practiceArea alone
      const byArea = samples.filter(
        (s) =>
          s.matterNumber !== matterNumber &&
          practiceArea !== "" &&
          s.practiceArea === practiceArea,
      );
      if (byArea.length >= 3) {
        comparables = byArea;
        basedOn = "practice_area";
      } else {
        // Fall back to all matters
        const allOther = samples.filter((s) => s.matterNumber !== matterNumber);
        comparables = allOther;
        basedOn = "all_matters";
      }
    }

    // 5. Extract totalAmountUsd, sort, compute percentiles
    const costs = comparables.map((s) => s.totalAmountUsd).sort((a, b) => a - b);

    // 9. Confidence level
    const count = comparables.length;
    let confidence: BudgetPrediction["confidence"];
    if (count >= 10) {
      confidence = "high";
    } else if (count >= 5) {
      confidence = "medium";
    } else if (count >= 3) {
      confidence = "low";
    } else {
      confidence = "insufficient_data";
    }

    // 6. estimatedTotalUsd = median
    const medianFinalCost = costs.length > 0 ? this.percentile(costs, 0.5) : 0;
    const p25FinalCost = costs.length > 0 ? this.percentile(costs, 0.25) : 0;
    const p75FinalCost = costs.length > 0 ? this.percentile(costs, 0.75) : 0;
    const estimatedTotalUsd = medianFinalCost;

    // 7–8. completionPct and remaining
    let completionPct: number;
    let estimatedRemainingUsd: number;
    let overBudget = false;
    if (estimatedTotalUsd === 0) {
      // Division by zero guard — return a low-confidence prediction with zeroed estimates
      logger.warn("BudgetPredictor: estimatedTotalUsd is zero, insufficient comparable data", { matterNumber });
      completionPct = 0;
      estimatedRemainingUsd = 0;
    } else {
      completionPct = Math.min(200, (spentUsd / estimatedTotalUsd) * 100);
      estimatedRemainingUsd = Math.max(0, estimatedTotalUsd - spentUsd);
      overBudget = spentUsd > estimatedTotalUsd;
    }

    return {
      matterNumber,
      practiceArea,
      spentUsd: parseFloat(spentUsd.toFixed(2)),
      spentBillingUnits,
      estimatedTotalUsd: parseFloat(estimatedTotalUsd.toFixed(2)),
      estimatedRemainingUsd: parseFloat(estimatedRemainingUsd.toFixed(2)),
      completionPct: parseFloat(completionPct.toFixed(2)),
      overBudget,
      confidence,
      comparableMatterCount: count,
      medianFinalCost: parseFloat(medianFinalCost.toFixed(2)),
      p25FinalCost: parseFloat(p25FinalCost.toFixed(2)),
      p75FinalCost: parseFloat(p75FinalCost.toFixed(2)),
      basedOn,
    };
  }

  /**
   * Build closed-matter cost samples from the TimeStore.
   *
   * A matter is a "closed sample" if:
   *   - It has at least 2 time entries
   *   - All entries have endedAt set (i.e. none are still open)
   *   - Total billing amount > 0
   */
  buildSamples(timeStore: TimeStore, taskStore: Map<string, Task>): MatterCostSample[] {
    // Group all entries by matterNumber
    const allEntries = timeStore.list();
    const grouped = new Map<string, typeof allEntries>();
    for (const entry of allEntries) {
      if (!entry.matterNumber) continue;
      const existing = grouped.get(entry.matterNumber);
      if (existing) {
        existing.push(entry);
      } else {
        grouped.set(entry.matterNumber, [entry]);
      }
    }

    const samples: MatterCostSample[] = [];

    for (const [mn, entries] of grouped) {
      if (entries.length < 2) continue;
      // All entries must be closed
      if (entries.some((e) => !e.endedAt)) continue;
      const totalAmountUsd = entries.reduce((sum, e) => sum + (e.billingAmountUsd ?? 0), 0);
      if (totalAmountUsd <= 0) continue;

      // Look up task for practiceArea, jurisdiction, workflowType
      let task: Task | undefined;
      for (const t of taskStore.values()) {
        if (t.matterNumber === mn) {
          task = t;
          break;
        }
      }

      const closedDates = entries
        .map((e) => e.endedAt!)
        .sort((a, b) => b.getTime() - a.getTime());
      const closedAt = closedDates[0]; // most recent close time

      samples.push({
        matterNumber: mn,
        practiceArea: (task as (Task & { practiceArea?: string }) | undefined)?.practiceArea ?? "",
        jurisdiction: task?.jurisdiction ?? "",
        workflowType: task?.workflowType ?? "",
        totalBillingUnits: entries.reduce((sum, e) => sum + e.billingUnits, 0),
        totalAmountUsd: parseFloat(totalAmountUsd.toFixed(2)),
        entryCount: entries.length,
        closedAt,
      });
    }

    return samples;
  }

  /**
   * Standard linear-interpolation percentile on a sorted array.
   * @param sorted - array of numbers sorted ascending
   * @param p - percentile fraction in [0, 1]
   */
  percentile(sorted: number[], p: number): number {
    if (sorted.length === 0) return 0;
    if (sorted.length === 1) return sorted[0];

    const idx = p * (sorted.length - 1);
    const lo = Math.floor(idx);
    const hi = Math.ceil(idx);
    if (lo === hi) return sorted[lo];

    const frac = idx - lo;
    return sorted[lo] + frac * (sorted[hi] - sorted[lo]);
  }
}
