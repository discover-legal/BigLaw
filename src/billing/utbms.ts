// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { costStore, calcCostUsd } from "../cost/index.js";

const HAIKU_MODEL = "claude-haiku-4-5-20251001";

const TASK_CODES = [
  "L110 Fact Investigation/Development",
  "L120 Analysis/Strategy",
  "L130 Experts/Consultants",
  "L140 Document/File Management",
  "L150 Budgeting",
  "L160 Settlement/Non-Binding ADR",
  "L190 Other Case Assessment",
  "L210 Pleadings",
  "L220 Preliminary Injunctions/TROs",
  "L230 Court Mandated Conferences",
  "L240 Dispositive Motions",
  "L250 Other Written Motions/Submissions",
  "L260 Class Action Certification",
  "L310 Written Discovery",
  "L320 Document Production",
  "L330 Depositions",
  "L340 Expert Discovery",
  "L350 Discovery Motions",
  "L390 Other Discovery",
  "L410 Fact Witnesses",
  "L420 Expert Witnesses",
  "L430 Trial Preparation",
  "L440 Trial",
  "L450 Post-Trial Motions",
  "L460 Appellate Proceedings",
  "L510 Project Management",
  "L520 Litigation Counseling",
  "L530 Contract/Agreement Drafting",
  "L540 Due Diligence",
  "L550 Regulatory Compliance",
];

const ACTIVITY_CODES = [
  "A101 Plan and Prepare",
  "A102 Research",
  "A103 Draft/Revise",
  "A104 Review/Analyze",
  "A105 Communicate (in firm)",
  "A106 Communicate (with client)",
  "A107 Communicate (other outside)",
  "A108 Appear for/Attend",
  "A109 Obtain/compile/index/organize",
  "A110 Other",
];

const FALLBACK = { taskCode: "L190", activityCode: "A110" };

const VALID_TASK = new Set(TASK_CODES.map((c) => c.slice(0, 4)));
const VALID_ACTIVITY = new Set(ACTIVITY_CODES.map((c) => c.slice(0, 4)));

const MAX_DESC_CHARS = 2_000;

function sanitizeDesc(s: string): string {
  return s
    .replace(/[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]/g, "")
    .slice(0, MAX_DESC_CHARS);
}

export async function classifyUtbms(
  description: string,
  event: string,
): Promise<{ taskCode: string; activityCode: string }> {
  const safeDesc = sanitizeDesc(description);
  const safeEvent = sanitizeDesc(event);
  const prompt =
    `Classify this legal time entry with exactly one UTBMS task code and one activity code. ` +
    `Reply with JSON only: {"taskCode": "LXXX", "activityCode": "AXXX"}.\n\n` +
    `Description: ${safeDesc}\nEvent type: ${safeEvent}\n\n` +
    `Task codes:\n${TASK_CODES.join("\n")}\n\n` +
    `Activity codes:\n${ACTIVITY_CODES.join("\n")}`;

  const anthropic = new Anthropic({ apiKey: Config.anthropic.apiKey });
  const t0 = Date.now();
  try {
    const response = await anthropic.messages.create({
      model: HAIKU_MODEL,
      max_tokens: 64,
      messages: [{ role: "user", content: prompt }],
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
      context: "classification",
    });
    const text = response.content[0].type === "text" ? response.content[0].text.trim() : "";
    const parsed = JSON.parse(text) as { taskCode?: string; activityCode?: string };
    const taskCode = typeof parsed.taskCode === "string" && VALID_TASK.has(parsed.taskCode)
      ? parsed.taskCode
      : FALLBACK.taskCode;
    const activityCode = typeof parsed.activityCode === "string" && VALID_ACTIVITY.has(parsed.activityCode)
      ? parsed.activityCode
      : FALLBACK.activityCode;
    return { taskCode, activityCode };
  } catch {
    return FALLBACK;
  }
}
