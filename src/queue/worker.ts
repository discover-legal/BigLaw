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
 * Handlers:
 *   summarize_time_entry — Haiku generates an OCG-compliant description from
 *                          scratch, using full task findings + OCG rules as
 *                          context. Writes the result directly back to the
 *                          TimeEntry, replacing any placeholder description.
 *   ocg_bulk_check       — Enqueues a summarize_time_entry job for every
 *                          unsummarized closed entry belonging to a client.
 */

import Anthropic from "@anthropic-ai/sdk";
import { jobQueue } from "./index.js";
import type { SummarizeTimeEntryPayload, OcgBulkCheckPayload } from "./index.js";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { costStore, calcCostUsd } from "../cost/index.js";
import type { Orchestrator } from "../orchestrator.js";

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

  // Collect task findings for context — what work was actually done.
  const task = taskId ? orch.getTask(taskId) : null;
  const findingsSummary = task?.findings?.length
    ? task.findings
        .slice(0, 20)
        .map((f) => `- ${f.content.slice(0, 300)}`)
        .join("\n")
    : "(no task findings available)";

  // Pull OCG billing rules for this client, if any exist.
  let ocgSection = "";
  if (clientNumber) {
    const client = orch.clients.list().find((c) => c.clientNumber === clientNumber);
    if (client) {
      const ocgDoc = orch.ocg.getByClient(client.id);
      if (ocgDoc?.rules.length) {
        ocgSection = `\nOCG BILLING RULES (all hard rules are mandatory):\n${
          ocgDoc.rules
            .map((r) => `- [${r.severity.toUpperCase()}/${r.category}] ${r.text}`)
            .join("\n")
        }`;
      }
    }
  }

  const durationHours = (entry.durationMs / 3_600_000).toFixed(2);

  const prompt = `You are a legal billing specialist. Write a precise, compliant billable time entry description.

TIMEKEEPER: ${entry.profileName}
EVENT TYPE: ${entry.event}
DURATION: ${durationHours} hours
MATTER: ${entry.matterNumber ?? "unknown"}
ORIGINAL NOTE: ${entry.description || "(none)"}

WORK PERFORMED (task findings):
${findingsSummary}
${ocgSection}

INSTRUCTIONS:
1. Write one concise description (1–3 sentences, max 200 characters) of the billable work.
2. Use active voice and specific legal terminology.
3. Comply strictly with every OCG hard rule listed above (if any).
4. Do NOT include duration, billing codes, rates, or timestamps — description only.
5. Return ONLY the description text. No JSON, no quotes, no preamble.`;

  const anthropic = new Anthropic({ apiKey: Config.anthropic.apiKey });
  const t0 = Date.now();
  let generated = "";
  try {
    const response = await anthropic.messages.create({
      model: HAIKU_MODEL,
      max_tokens: 512,
      messages: [{ role: "user", content: prompt }],
    });
    const durationMs = Date.now() - t0;
    const inputTokens = response.usage.input_tokens;
    const outputTokens = response.usage.output_tokens;
    costStore.record({
      model: HAIKU_MODEL,
      provider: "anthropic",
      inputTokens,
      outputTokens,
      costUsd: calcCostUsd(HAIKU_MODEL, inputTokens, outputTokens),
      estimatedWh: null,
      estimatedWatts: null,
      durationMs,
      context: "entry_summarize",
      taskId: taskId ?? undefined,
    });
    generated = response.content[0].type === "text" ? response.content[0].text.trim() : "";
  } catch (err) {
    throw new Error(`Haiku summarize call failed: ${(err as Error).message}`);
  }

  if (!generated) {
    logger.warn("summarize_time_entry: Haiku returned empty description", { entryId });
    return;
  }

  orch.time.updateDescription(entryId, generated.slice(0, 500));
  logger.info("Time entry description generated", { entryId, chars: generated.length });
}

async function handleOcgBulkCheck(
  payload: OcgBulkCheckPayload,
  orch: Orchestrator,
): Promise<void> {
  const { clientNumber } = payload;

  // Only closed entries that haven't been summarized yet.
  const entries = orch.time
    .list({ clientNumber })
    .filter((e) => e.endedAt && !e.ocgSuggestions);

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
          } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            logger.warn("Job failed", { jobId: job.id, type: job.type, error: msg });
            await jobQueue.fail(job.id, msg);
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
