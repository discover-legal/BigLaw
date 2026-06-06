// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * MatterHealthMonitor — per-matter composite health score.
 *
 * Replaces Clio Insights. Pure arithmetic — no AI, no network calls.
 * Aggregates five dimensions into a single 0–100 score:
 *
 *   budgetHealth      (30%) — how far through the budget? high burn → low score
 *   deadlineHealth    (25%) — are there overdue/imminent deadline tasks?
 *   activityFreshness (20%) — how recently was work logged on this matter?
 *   gateBacklog       (15%) — how many unresolved human gates are pending?
 *   ocgCompliance     (10%) — what fraction of entries have OCG violations?
 *
 * green ≥ 75   amber ≥ 45   red < 45
 */

import type { TimeStore } from "../time/index.js";
import type { BudgetMonitor } from "../budget/index.js";
import type { Task } from "../types.js";
import type {
  MatterHealthScore,
  MatterRiskFactor,
  HealthSignal,
  HealthTrend,
  PortfolioHealthSummary,
} from "../types.js";

const WEIGHTS = { budget: 0.30, deadline: 0.25, activity: 0.20, gates: 0.15, ocg: 0.10 };

// ─── Dimension calculators ────────────────────────────────────────────────────

function budgetDimension(burn: { burnPct: number } | null): { score: number; risk: MatterRiskFactor | null } {
  if (!burn) return { score: 85, risk: null }; // no budget set — neutral
  const pct = burn.burnPct * 100;
  let score: number;
  let risk: MatterRiskFactor | null = null;
  if (pct >= 100) {
    score = 0;
    risk = { type: "budget_overrun", severity: "high", message: `Matter is ${Math.round(pct)}% through budget (over budget)`, suggestedAction: "File a budget adjustment request and notify the client immediately." };
  } else if (pct >= 80) {
    score = Math.max(0, 100 - (pct - 50) * 2);
    risk = { type: "budget_overrun", severity: "high", message: `Matter is ${Math.round(pct)}% through budget`, suggestedAction: "Notify the partner and prepare a revised budget estimate." };
  } else if (pct >= 50) {
    score = Math.max(0, 100 - (pct - 50) * 2);
    risk = { type: "budget_overrun", severity: "medium", message: `Matter is ${Math.round(pct)}% through budget`, suggestedAction: "Monitor spend closely over the next billing cycle." };
  } else {
    score = 100;
  }
  return { score: Math.round(score), risk };
}

function deadlineDimension(tasks: Task[]): { score: number; risk: MatterRiskFactor | null } {
  const now = Date.now();
  const sevenDays = 7 * 24 * 60 * 60 * 1000;
  let overdue = 0;
  let imminent = 0;
  for (const t of tasks) {
    if (t.status === "failed") overdue++;
    if (t.status === "pending" || t.status === "running") {
      const age = now - t.createdAt.getTime();
      if (age > sevenDays * 2) imminent++; // running for >2 weeks
    }
  }
  let score = 100;
  let risk: MatterRiskFactor | null = null;
  if (overdue > 0) {
    score = Math.max(0, 100 - overdue * 30);
    risk = { type: "task_failure", severity: "high", message: `${overdue} task(s) have failed on this matter`, suggestedAction: "Review failed tasks and re-run or reassign." };
  } else if (imminent > 0) {
    score = Math.max(30, 100 - imminent * 15);
    risk = { type: "deadline_approaching", severity: "medium", message: `${imminent} task(s) have been running for over 2 weeks`, suggestedAction: "Check task progress and ensure no blockers." };
  }
  return { score: Math.round(score), risk };
}

function activityDimension(lastActivityMs: number | null): { score: number; risk: MatterRiskFactor | null } {
  if (!lastActivityMs) return { score: 40, risk: { type: "stale_activity", severity: "medium", message: "No billing activity recorded on this matter", suggestedAction: "Confirm the matter is still active." } };
  const daysSince = (Date.now() - lastActivityMs) / (1000 * 60 * 60 * 24);
  let score: number;
  let risk: MatterRiskFactor | null = null;
  if (daysSince <= 7)       score = 100;
  else if (daysSince <= 14) score = 80;
  else if (daysSince <= 30) score = 60;
  else if (daysSince <= 60) {
    score = 40;
    risk = { type: "stale_activity", severity: "low", message: `No activity in ${Math.round(daysSince)} days`, suggestedAction: "Follow up with the assigned lawyer to confirm matter status." };
  } else {
    score = 15;
    risk = { type: "stale_activity", severity: "medium", message: `No activity in ${Math.round(daysSince)} days`, suggestedAction: "Review whether this matter should be formally closed or reassigned." };
  }
  return { score: Math.round(score), risk };
}

function gateDimension(openGates: number): { score: number; risk: MatterRiskFactor | null } {
  if (openGates === 0) return { score: 100, risk: null };
  const score = Math.max(0, 100 - openGates * 25);
  const risk: MatterRiskFactor = {
    type: "gate_backlog",
    severity: openGates >= 3 ? "high" : "medium",
    message: `${openGates} human gate(s) awaiting review`,
    suggestedAction: "Review and approve or reject pending findings to unblock the matter.",
  };
  return { score: Math.round(score), risk };
}

function ocgDimension(violationRate: number): { score: number; risk: MatterRiskFactor | null } {
  // violationRate: fraction of entries (0–1) with at least one hard OCG violation
  const score = Math.round(Math.max(0, (1 - violationRate) * 100));
  const risk: MatterRiskFactor | null = violationRate >= 0.2
    ? {
        type: "ocg_violations",
        severity: violationRate >= 0.5 ? "high" : "medium",
        message: `${Math.round(violationRate * 100)}% of billing entries have OCG violations`,
        suggestedAction: "Review flagged entries in the pre-bill queue before sending the invoice.",
      }
    : null;
  return { score, risk };
}

// ─── Trend detection ──────────────────────────────────────────────────────────

function detectTrend(
  history: Map<string, MatterHealthScore[]>,
  matterNumber: string,
  currentScore: number,
): HealthTrend {
  const prev = history.get(matterNumber);
  if (!prev || prev.length < 2) return "stable";
  const last = prev[prev.length - 1].score;
  const diff = currentScore - last;
  if (diff >= 5) return "improving";
  if (diff <= -5) return "deteriorating";
  return "stable";
}

// ─── MatterHealthMonitor ──────────────────────────────────────────────────────

export class MatterHealthMonitor {
  private readonly history = new Map<string, MatterHealthScore[]>();

  /**
   * Compute the health score for a single matter.
   *
   * @param matterNumber  The matter to score.
   * @param tasks         All tasks whose matterNumber matches.
   * @param time          TimeStore — used for activity freshness and OCG compliance.
   * @param budgetMonitor BudgetMonitor — used for burn rate.
   */
  compute(
    matterNumber: string,
    tasks: Task[],
    time: TimeStore,
    budgetMonitor: BudgetMonitor,
  ): MatterHealthScore {
    const matterTasks = tasks.filter((t) => t.matterNumber === matterNumber);
    const entries = time.list({ matterNumber });
    const closedEntries = entries.filter((e) => e.endedAt);

    // Last activity
    const lastActivityMs = closedEntries.length
      ? Math.max(...closedEntries.map((e) => e.endedAt!.getTime()))
      : null;

    // Open gates
    const openGates = matterTasks.reduce(
      (sum, t) => sum + (t.pendingGates?.filter((g) => g.status === "pending").length ?? 0),
      0,
    );

    // OCG violation rate (hard violations only)
    const totalEntries = closedEntries.length;
    const violatingEntries = closedEntries.filter(
      (e) => e.ocgSuggestions?.some((s) => s.severity === "hard" && s.status === "pending"),
    ).length;
    const violationRate = totalEntries > 0 ? violatingEntries / totalEntries : 0;

    // Burn
    const burn = budgetMonitor.getBurn(matterNumber);

    // Dimensions
    const { score: bScore, risk: bRisk } = budgetDimension(burn);
    const { score: dScore, risk: dRisk } = deadlineDimension(matterTasks);
    const { score: aScore, risk: aRisk } = activityDimension(lastActivityMs);
    const { score: gScore, risk: gRisk } = gateDimension(openGates);
    const { score: oScore, risk: oRisk } = ocgDimension(violationRate);

    const composite = Math.round(
      bScore * WEIGHTS.budget +
      dScore * WEIGHTS.deadline +
      aScore * WEIGHTS.activity +
      gScore * WEIGHTS.gates +
      oScore * WEIGHTS.ocg,
    );

    const signal: HealthSignal = composite >= 75 ? "green" : composite >= 45 ? "amber" : "red";
    const signalLabel = signal === "green"
      ? "On track"
      : signal === "amber"
        ? "Needs attention"
        : "At risk";

    const riskFactors = [bRisk, dRisk, aRisk, gRisk, oRisk].filter(Boolean) as MatterRiskFactor[];
    riskFactors.sort((a, b) => {
      const order = { high: 0, medium: 1, low: 2 };
      return order[a.severity] - order[b.severity];
    });

    const trend = detectTrend(this.history, matterNumber, composite);

    const result: MatterHealthScore = {
      matterNumber,
      score: composite,
      signal,
      signalLabel,
      dimensions: {
        budgetHealth: bScore,
        deadlineHealth: dScore,
        activityFreshness: aScore,
        gateBacklog: gScore,
        ocgCompliance: oScore,
      },
      riskFactors,
      trend,
      computedAt: new Date().toISOString(),
    };

    // Record in history for trend detection.
    const hist = this.history.get(matterNumber) ?? [];
    hist.push(result);
    if (hist.length > 10) hist.shift();
    this.history.set(matterNumber, hist);

    return result;
  }

  /**
   * Compute health scores for all known matters and return a portfolio summary.
   *
   * @param allMatters  All matter numbers to evaluate.
   * @param tasks       All tasks (will be filtered by matterNumber per matter).
   * @param time        TimeStore.
   * @param budget      BudgetMonitor.
   */
  portfolio(
    allMatters: string[],
    tasks: Task[],
    time: TimeStore,
    budget: BudgetMonitor,
  ): PortfolioHealthSummary {
    const scores = allMatters.map((m) => this.compute(m, tasks, time, budget));
    scores.sort((a, b) => a.score - b.score); // worst first

    return {
      totalMatters: scores.length,
      green: scores.filter((s) => s.signal === "green").length,
      amber: scores.filter((s) => s.signal === "amber").length,
      red: scores.filter((s) => s.signal === "red").length,
      matters: scores,
      computedAt: new Date().toISOString(),
    };
  }
}

export const matterHealthMonitor = new MatterHealthMonitor();
