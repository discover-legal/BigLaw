// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Queue worker — dequeues jobs and dispatches them to handlers.
 *
 * `startWorker(orchestrator)` returns a stop function. Call it on shutdown.
 *
 * summarize_time_entry — Two separate passes per entry:
 *
 *   Pass 1 — Description generation (Haiku):
 *     Haiku writes what happened from task findings. OCG rules ARE injected
 *     as context so the model can take a best-effort pass at compliance.
 *     This is guidance only — not a guarantee.
 *
 *   Pass 2 — Structured OCG compliance check (OcgStore.checkEntry):
 *     The generated description is checked rule-by-rule against the client's
 *     OcgRule dictionary. Rules are evaluated as structured objects:
 *       - billing_increments / timing → mechanical (pure math, no AI)
 *       - all other categories       → Haiku, rules passed as JSON objects
 *                                      {id, category, text, severity}
 *     Violations map back to specific ruleIds. Stored as OcgSuggestion[].
 *     Hit counts recorded to ocgStore.recordViolations() for stats tracking.
 *
 *   These passes are intentionally separate: the description AI describes
 *   the work; the compliance checker enforces the rule dictionary.
 *
 * ocg_bulk_check — Enqueues a summarize_time_entry job for every unsummarized
 *                  closed entry belonging to a client.
 */

import Anthropic from "@anthropic-ai/sdk";
import { jobQueue } from "./index.js";
import type { SummarizeTimeEntryPayload, OcgBulkCheckPayload } from "./index.js";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { auditLogger, ACTOR_SYSTEM } from "../audit/index.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import type { Orchestrator } from "../orchestrator.js";
import type { OcgDocument } from "../types.js";
import { sanitizePromptContent } from "../adapters/lavern.js";

const HAIKU_MODEL = "claude-haiku-4-5-20251001";

// ─── Handlers ────────────────────────────────────────────────────────────────

async function handleSummarizeTimeEntry(
  payload: SummarizeTimeEntryPayload,
  orch: Orchestrator,
): Promise<void> {
  const { entryId, taskId, clientNumber } = payload;

  const entry = orch.time.getById(entryId);
  if (!entry) {
    logger.warn("summarize_time_entry: entry not found", { entryId });
    return;
  }

  // Collect task findings — what work was actually done.
  const task = taskId ? orch.getTask(taskId) : null;
  const findingsSummary = task?.findings?.length
    ? task.findings
        .slice(0, 20)
        .map((f) => `- ${sanitizePromptContent(f.content.slice(0, 300))}`)
        .join("\n")
    : "(no task findings available)";

  // Resolve client + OCG document once (used in both passes).
  let ocgDoc: OcgDocument | undefined;
  let clientId: string | undefined;
  if (clientNumber) {
    const client = orch.clients.list().find((c) => c.clientNumber === clientNumber);
    if (client) {
      clientId = client.id;
      ocgDoc = orch.ocg.getByClient(client.id);
    }
  }

  const durationHours = (entry.durationMs / 3_600_000).toFixed(2);

  // Build OCG hint block — injected as best-effort context for the description
  // model. The structured compliance check (Pass 2) is the authoritative gate.
  const ocgHint = ocgDoc?.rules.length
    ? `\nOCG GUIDANCE (attempt to comply; a separate check will enforce rules):\n${
        ocgDoc.rules
          .map((r) => `- [${r.severity.toUpperCase()}/${r.category}] ${r.text}`)
          .join("\n")
      }`
    : "";

  // Sanitize all user-controlled strings before embedding in the prompt.
  const safeDescription = sanitizePromptContent(entry.description ?? "");
  const safeEvent = sanitizePromptContent(entry.event ?? "");
  const safeProfileName = sanitizePromptContent(entry.profileName ?? "");
  const safeOcgHint = sanitizePromptContent(ocgHint ?? "");

  // ── Pass 1: Description generation ───────────────────────────────────────
  const descriptionPrompt = `You are a legal billing specialist. Write a precise billable time entry description.

TIMEKEEPER: ${safeProfileName}
EVENT TYPE: ${safeEvent}
DURATION: ${durationHours} hours
MATTER: ${entry.matterNumber ?? "unknown"}
ORIGINAL NOTE: ${safeDescription || "(none)"}

WORK PERFORMED (task findings):
${findingsSummary}
${safeOcgHint}

INSTRUCTIONS:
1. Write one concise description (1–3 sentences, max 200 characters) of the work performed.
2. Use active voice and specific legal terminology.
3. Do NOT include duration, billing codes, rates, or timestamps.
4. Return ONLY the description text. No JSON, no quotes, no preamble.`;

  const anthropic = new Anthropic({ apiKey: Config.anthropic.apiKey });
  const t0 = Date.now();
  let generated = "";
  try {
    const response = await anthropic.messages.create({
      model: HAIKU_MODEL,
      max_tokens: 512,
      messages: [{ role: "user", content: descriptionPrompt }],
    });
    const durationMs = Date.now() - t0;
    costStore.record({
      model: HAIKU_MODEL,
      provider: "anthropic",
      inputTokens: response.usage.input_tokens,
      outputTokens: response.usage.output_tokens,
      costUsd: calcCostUsd(HAIKU_MODEL, response.usage.input_tokens, response.usage.output_tokens),
      estimatedWh: null,
      estimatedWatts: null,
      durationMs,
      context: "entry_summarize",
      taskId: taskId ?? undefined,
    });
    generated = response.content[0].type === "text" ? response.content[0].text.trim() : "";
  } catch (err) {
    const safeMsg = (err as Error).message
      .replace(/\bsk-ant-[A-Za-z0-9_-]+/g, "[REDACTED]")
      .replace(/\bBearer\s+[A-Za-z0-9._-]+/g, "Bearer [REDACTED]");
    throw new Error(`Haiku describe call failed: ${safeMsg}`);
  }

  if (!generated) {
    logger.warn("summarize_time_entry: Haiku returned empty description", { entryId });
    return;
  }

  orch.time.updateDescription(entryId, generated.slice(0, 500));
  logger.info("Time entry description generated", { entryId, chars: generated.length });

  // ── Pass 2: Structured OCG compliance check ───────────────────────────────
  // Runs independently of Pass 1. Checks the written description against each
  // OcgRule object — mechanical rules by math, semantic rules via Haiku with
  // rules passed as structured JSON objects keyed by their IDs.
  if (!ocgDoc || !clientId) return;

  // Re-fetch so the updated description is present for the check.
  const updatedEntry = orch.time.getById(entryId);
  if (!updatedEntry) return;

  try {
    const suggestions = await orch.ocg.checkEntry(updatedEntry, ocgDoc);

    if (suggestions.length) {
      orch.time.setSuggestions(entryId, suggestions);
      orch.ocg.recordViolations(clientId, suggestions);
      logger.info("OCG violations found", {
        entryId,
        clientNumber,
        total: suggestions.length,
        hard: suggestions.filter((s) => s.severity === "hard").length,
        byCategory: suggestions.reduce<Record<string, number>>((acc, s) => {
          acc[s.category] = (acc[s.category] ?? 0) + 1;
          return acc;
        }, {}),
      });
    } else {
      logger.debug("OCG check passed — no violations", { entryId });
    }
  } catch (err) {
    // OCG check failure is non-fatal — description is already written.
    logger.warn("OCG compliance check error (non-fatal)", {
      entryId,
      error: (err as Error).message,
    });
  }
}

async function handleOcgBulkCheck(
  payload: OcgBulkCheckPayload,
  orch: Orchestrator,
): Promise<void> {
  const { clientNumber } = payload;

  const entries = orch.time
    .list({ clientNumber })
    .filter((e) => e.endedAt && !e.ocgSuggestions && !e.agentId);

  if (!entries.length) {
    logger.info("ocg_bulk_check: no eligible entries", { clientNumber });
    return;
  }

  for (const entry of entries) {
    await jobQueue.enqueue("summarize_time_entry", {
      entryId: entry.id,
      taskId: entry.taskId,
      clientNumber,
    } satisfies SummarizeTimeEntryPayload);
  }

  logger.info("ocg_bulk_check: enqueued summarize jobs", {
    clientNumber,
    count: entries.length,
  });
}

// ─── Worker loop ──────────────────────────────────────────────────────────────

export function startWorker(orch: Orchestrator): () => void {
  let running = true;
  let timer: ReturnType<typeof setTimeout> | null = null;

  async function tick(): Promise<void> {
    const concurrency = Config.queue.concurrency;
    const promises: Promise<void>[] = [];

    for (let i = 0; i < concurrency; i++) {
      const job = await jobQueue.dequeue();
      if (!job) break;

      promises.push(
        (async () => {
          const jobStart = Date.now();
          try {
            switch (job.type) {
              case "summarize_time_entry":
                await handleSummarizeTimeEntry(
                  job.payload as unknown as SummarizeTimeEntryPayload,
                  orch,
                );
                break;
              case "ocg_bulk_check":
                await handleOcgBulkCheck(
                  job.payload as unknown as OcgBulkCheckPayload,
                  orch,
                );
                break;
              default:
                logger.warn("Unknown job type — skipping", { type: job.type, jobId: job.id });
            }
            await jobQueue.ack(job.id);
            auditLogger.write({
              event: "job.completed",
              actorId: ACTOR_SYSTEM,
              durationMs: Date.now() - jobStart,
              data: { jobId: job.id, type: job.type, retries: job.retries },
            });
          } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            logger.warn("Job failed", { jobId: job.id, type: job.type, error: msg });
            await jobQueue.fail(job.id, msg);
            const isDead = job.retries + 1 >= job.maxRetries;
            auditLogger.write({
              event: isDead ? "job.dead_letter" : "job.failed",
              actorId: ACTOR_SYSTEM,
              durationMs: Date.now() - jobStart,
              data: { jobId: job.id, type: job.type, retries: job.retries + 1, error: msg },
            });
          }
        })(),
      );
    }

    if (promises.length) await Promise.allSettled(promises);
  }

  function schedule(): void {
    if (!running) return;
    timer = setTimeout(async () => {
      try {
        await tick();
      } catch (err) {
        logger.warn("Worker tick error", { error: (err as Error).message });
      }
      schedule();
    }, Config.queue.pollIntervalMs);
  }

  schedule();
  logger.info("Job worker started", {
    concurrency: Config.queue.concurrency,
    pollIntervalMs: Config.queue.pollIntervalMs,
  });

  return () => {
    running = false;
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
    logger.info("Job worker stopped");
  };
}
