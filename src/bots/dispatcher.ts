// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

/**
 * Bot command dispatcher — shared by Teams and Slack bots.
 *
 * Parses `@BigMichael <command> [args]` messages, dispatches to the
 * orchestrator, and returns a formatted Markdown response.
 *
 * Commands:
 *   status  [matter]      → matter health score + active tasks
 *   briefing [client]     → generate client intelligence briefing
 *   search  [query]       → semantic search across the knowledge store
 *   task    [description] → submit a new roundtable task
 *   run     [template]    → run a named workflow template
 *   help                  → list available commands
 *
 * Design: synchronous where possible (Teams Outgoing Webhook needs a
 * response in < 5 s). Long-running commands return "Working on it…" and
 * post back via the channel client once the task completes.
 */

import { logger } from "../logger.js";
import type { Orchestrator } from "../orchestrator.js";

// ─── Types ────────────────────────────────────────────────────────────────────

export type BotPlatform = "teams" | "slack";

export interface BotMessage {
  /** Raw @-mention stripped, trimmed */
  text: string;
  /** Who sent the message */
  senderName: string;
  senderEmail?: string;
  /** Channel or conversation context */
  channelId?: string;
  teamId?: string;
  /** For threading a reply back (Slack thread_ts, Teams reply-to id) */
  threadId?: string;
  platform: BotPlatform;
}

export interface BotResponse {
  /** Immediate reply text (Markdown) — sent in the same turn */
  immediate: string;
  /** If set, dispatch this async work and post back when done */
  asyncWork?: () => Promise<string>;
}

// ─── Dispatcher ───────────────────────────────────────────────────────────────

const BOT_NAME_RE = /^@?big[-_]?michael[\s:,]*/i;

export function parseCommand(raw: string): { command: string; args: string } {
  const text = raw.replace(BOT_NAME_RE, "").trim();
  const space = text.indexOf(" ");
  if (space === -1) return { command: text.toLowerCase(), args: "" };
  return { command: text.slice(0, space).toLowerCase(), args: text.slice(space + 1).trim() };
}

export async function dispatch(
  msg: BotMessage,
  orch: Orchestrator,
): Promise<BotResponse> {
  const { command, args } = parseCommand(msg.text);

  switch (command) {

    case "status": {
      const matterNumber = args.trim();
      if (!matterNumber) {
        return { immediate: "Usage: `@BigMichael status [matter-number]`" };
      }
      const tasks = orch.listTasks().filter((t) => t.matterNumber === matterNumber);
      if (tasks.length === 0) {
        return { immediate: `No tasks found for matter **${matterNumber}**.` };
      }
      const health = orch.matterHealth.compute(
        matterNumber,
        tasks,
        orch.time,
        orch.budgetMonitor,
      );
      const signal = health.signal === "green" ? "🟢" : health.signal === "amber" ? "🟡" : "🔴";
      const lines = [
        `**Matter ${matterNumber}** — Health ${signal} ${health.score}/100`,
        "",
        `Budget: ${health.dimensions.budgetHealth}/100 | Deadline: ${health.dimensions.deadlineHealth}/100 | Activity: ${health.dimensions.activityFreshness}/100`,
        "",
        `**Active tasks:** ${tasks.filter((t) => t.status === "running").length}`,
        `**Pending gates:** ${tasks.reduce((s, t) => s + (t.pendingGates?.length ?? 0), 0)}`,
      ];
      if (health.riskFactors.length > 0) {
        lines.push("", "**Risks:**");
        for (const r of health.riskFactors.slice(0, 3)) lines.push(`• ${r.message}`);
      }
      return { immediate: lines.join("\n") };
    }

    case "briefing": {
      const clientRef = args.trim();
      if (!clientRef) {
        return { immediate: "Usage: `@BigMichael briefing [client-name-or-number]`" };
      }
      const clientRecord = orch.clients.getByClientNumber(clientRef)
        ?? orch.clients.list().find((c) => c.name.toLowerCase().includes(clientRef.toLowerCase()));
      if (!clientRecord) {
        return { immediate: `Client not found: **${clientRef}**. Check the client number or name.` };
      }
      return {
        immediate: `Assembling briefing for **${clientRecord.name}** — scanning all sources…`,
        asyncWork: async () => {
          const allTasks = orch.listTasks();
          const allEntries = await orch.time.list({});
          const briefing = await orch.briefing.generate(
            clientRecord,
            allTasks,
            allEntries as import("../types.js").TimeEntry[],
            { knowledge: orch.knowledge },
          );
          return briefing.document;
        },
      };
    }

    case "search": {
      if (!args) return { immediate: "Usage: `@BigMichael search [query]`" };
      const results = await orch.knowledge.search(args, { topK: 5 });
      const arr = Array.isArray(results) ? results as unknown as Array<Record<string, unknown>> : [];
      if (arr.length === 0) return { immediate: `No results found for: **${args}**` };
      const lines = [`**Knowledge search:** ${args}`, ""];
      for (const r of arr.slice(0, 5)) {
        const title = String(r["title"] ?? r["documentTitle"] ?? "Result");
        const snippet = String(r["content"] ?? r["text"] ?? "").slice(0, 150);
        lines.push(`**${title}**`, snippet, "");
      }
      return { immediate: lines.join("\n") };
    }

    case "task": {
      if (!args) return { immediate: "Usage: `@BigMichael task [description]`" };
      return {
        immediate: `Submitting task: _${args.slice(0, 80)}_…`,
        asyncWork: async () => {
          const task = await orch.submitTask({
            description: args,
            workflowType: "roundtable",
          });
          return `Task submitted ✓\n**ID:** \`${task.id}\`\nUse \`@BigMichael status\` to follow progress.`;
        },
      };
    }

    case "run": {
      const templateId = args.trim();
      if (!templateId) {
        const templates = await orch.listTemplates();
        const list = templates.map((t) => `• \`${t.id}\` — ${t.name}`).join("\n");
        return { immediate: `**Available templates:**\n${list}\n\nUsage: \`@BigMichael run [template-id]\`` };
      }
      return {
        immediate: `Running template \`${templateId}\`…`,
        asyncWork: async () => {
          const task = await orch.submitFromTemplate(templateId);
          return `Template \`${templateId}\` started ✓\n**Task ID:** \`${task.id}\``;
        },
      };
    }

    case "help":
    case "":
    default:
      return {
        immediate: [
          "**Big Michael** — multi-agent legal AI",
          "",
          "| Command | Description |",
          "|---------|-------------|",
          "| `status [matter]` | Matter health score + active tasks |",
          "| `briefing [client]` | Client intelligence briefing (all sources) |",
          "| `search [query]` | Semantic search across the knowledge store |",
          "| `task [description]` | Submit a new roundtable AI task |",
          "| `run [template-id]` | Run a named workflow template |",
          "| `help` | Show this message |",
        ].join("\n"),
      };
  }
}
