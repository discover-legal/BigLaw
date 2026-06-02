// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Debate and verification protocols — inspired by Laverne (github.com/AnttiHero/lavern).
 *
 * Three layers:
 *   1. CitationGate     — no finding passes without a verifiable citation
 *   2. DebateProtocol   — adversarial challenge mechanism (challenger must also cite)
 *   3. VerificationPipeline — 10-pass quality check on resolved findings
 *
 * HumanGate — findings below confidence threshold or with unresolved challenges
 *             are held pending human approval before entering final output.
 */

import { Config } from "../config.js";
import { logger } from "../logger.js";
import { selectModel } from "../routing/model.js";
import { getProvider, resolveModelId } from "../providers/index.js";
import { auditLogger } from "../audit/index.js";
import type {
  Finding,
  Citation,
  Challenge,
  GateRequest,
  VerificationCheck,
  VerificationResult,
} from "../types.js";

// ─── 1. Citation gate ─────────────────────────────────────────────────────────

/**
 * Filter out findings that have no citations (required if citationRequired is true).
 * Mechanically verify that the cited quote string appears in the source text.
 */
export function applyCitationGate(
  findings: Finding[],
  sourceTexts: Map<string, string>,   // documentId → full text
): { passed: Finding[]; rejected: Finding[] } {
  if (!Config.debate.citationRequired) {
    return { passed: findings, rejected: [] };
  }

  const passed: Finding[] = [];
  const rejected: Finding[] = [];

  for (const finding of findings) {
    if (!finding.citations.length) {
      logger.warn("Finding rejected — no citations", { findingId: finding.id });
      rejected.push(finding);
      continue;
    }

    // Mechanical string match
    for (const citation of finding.citations) {
      const sourceText = sourceTexts.get(citation.source);
      if (sourceText) {
        citation.mechanicallyVerified = sourceText.includes(citation.quote);
        if (!citation.mechanicallyVerified) {
          logger.warn("Citation string match failed", {
            findingId: finding.id,
            source: citation.source,
          });
        }
      }
    }

    passed.push(finding);
  }

  return { passed, rejected };
}

// ─── 2. Debate protocol ───────────────────────────────────────────────────────

const CHALLENGER_SYSTEM = `You are the Adversarial Challenger in a legal AI debate protocol.
Your job: challenge the finding below if it is incorrect, overstated, or unsupported.
Your challenge MUST include a verbatim citation from a specific source.
If you believe the finding is correct and well-supported, output: NO_CHALLENGE
Otherwise output:
CHALLENGE:
Content: <your challenge>
Citation: SOURCE=<source> | QUOTE=<verbatim text>
Strength: <1-5>
END_CHALLENGE`;

const RESOLVER_SYSTEM = `You are the Debate Resolver in a legal AI debate protocol.
You receive a finding and a challenge to that finding.
Weigh both. Cite specific reasons for your resolution.
Output:
RESOLUTION: <UPHELD | MODIFIED | OVERTURNED>
REASONING: <one paragraph explaining your resolution, citing both sides>
MODIFIED_CONTENT: <if MODIFIED, the corrected finding content; otherwise leave blank>`;

export async function runDebate(finding: Finding, challengerAgentId: string): Promise<Finding> {
  if (!Config.debate.adversarialEnabled) return finding;

  const debateModel = selectModel({ taskType: "debate" });
  auditLogger.write({ event: "debate.start", data: { findingId: finding.id, model: debateModel } });

  // Cap finding content before LLM insertion to prevent oversized findings from
  // consuming entire context windows or being used as prompt injection payloads.
  const findingSnippet = finding.content.slice(0, 20_000);
  // Generate challenge — Opus (debate routing)
  const challengeText = await callModel(
    CHALLENGER_SYSTEM,
    `FINDING:\n${findingSnippet}\n\nCITATIONS:\n${finding.citations.slice(0, 50).map((c) => `SOURCE=${c.source.slice(0, 200)} | QUOTE=${c.quote.slice(0, 500)}`).join("\n")}`,
    600,
    debateModel,
  );

  if (challengeText.includes("NO_CHALLENGE")) {
    logger.debug("Finding unchallenged", { findingId: finding.id });
    auditLogger.write({ event: "debate.resolved", data: { findingId: finding.id, verdict: "NO_CHALLENGE" } });
    return finding;
  }

  const challenge = parseChallenge(challengeText, challengerAgentId);
  finding.challenged = true;
  finding.challenge = challenge;

  // Resolve debate — Opus
  const resolutionText = await callModel(
    RESOLVER_SYSTEM,
    `FINDING:\n${findingSnippet}\n\nCHALLENGE:\n${challenge.content.slice(0, 10_000)}\nChallenge citations: ${challenge.citations.map((c) => c.quote).join("; ")}`,
    800,
    debateModel,
  );

  const resolution = parseResolution(resolutionText);
  challenge.resolution = resolution.reasoning;
  challenge.resolvedAt = new Date();

  if (resolution.verdict === "MODIFIED" && resolution.modifiedContent) {
    finding.content = resolution.modifiedContent;
  } else if (resolution.verdict === "OVERTURNED") {
    finding.confidence = Math.max(0, finding.confidence - 0.3);
  }

  finding.resolved = true;
  logger.info("Debate resolved", { findingId: finding.id, verdict: resolution.verdict });
  auditLogger.write({ event: "debate.resolved", data: { findingId: finding.id, verdict: resolution.verdict } });

  return finding;
}

// ─── 3. Verification pipeline ─────────────────────────────────────────────────

const VERIFICATION_CHECKS = [
  "Context: Is the finding grounded in the stated context and not taken out of scope?",
  "Accuracy: Are all legal propositions correctly stated per the cited authority?",
  "Completeness: Does the finding address all aspects of the question it purports to answer?",
  "Clarity: Is the finding expressed clearly and unambiguously?",
  "Structure: Is the finding logically structured?",
  "Citations: Are all citations present, specific, and relevant?",
  "Risk: Does the finding appropriately flag relevant risks or uncertainties?",
  "Jurisdiction: Is the jurisdictional scope of the finding explicitly stated?",
  "Timeliness: Are the sources current? Are any materials superseded?",
  "Proportionality: Is the conclusion proportionate to the evidence cited?",
];

export async function runVerificationPipeline(finding: Finding): Promise<VerificationResult> {
  const passes = Config.debate.verificationPasses;
  const checksToRun = VERIFICATION_CHECKS.slice(0, passes);

  const verifyModel = selectModel({ taskType: "extraction" }); // Haiku — fast, many parallel calls
  auditLogger.write({ event: "verification.start", data: { findingId: finding.id, checks: checksToRun.length, model: verifyModel } });

  // Same 20k cap as the debate path — prevents each of the N parallel
  // verification calls from receiving an unbounded finding payload.
  const verifySnippet = finding.content.slice(0, 20_000);
  const checks: VerificationCheck[] = await Promise.all(
    checksToRun.map(async (checkDesc) => {
      const response = await callModel(
        `You are a legal verification specialist. Assess the following finding against this criterion: ${checkDesc}\nRespond with: PASS or FAIL followed by a one-line note.`,
        `FINDING:\n${verifySnippet}\n\nCITATIONS:\n${finding.citations.slice(0, 50).map((c) => `${c.source.slice(0, 200)}: "${c.quote.slice(0, 500)}"`).join("\n")}`,
        150,
        verifyModel,
      );
      const passed = response.toUpperCase().includes("PASS");
      const notes = response.replace(/^(PASS|FAIL)\s*/i, "").trim();
      return { name: checkDesc.split(":")[0], passed, notes };
    }),
  );

  const passed = checks.every((c) => c.passed);
  const result: VerificationResult = {
    findingId: finding.id,
    checks,
    passed,
    completedAt: new Date(),
  };

  finding.verificationResult = result;
  finding.resolved = passed;

  const failedChecks = checks.filter((c) => !c.passed).map((c) => c.name);
  logger.info("Verification complete", { findingId: finding.id, passed, failedChecks });
  auditLogger.write({ event: "verification.complete", data: { findingId: finding.id, passed, failedChecks } });

  return result;
}

// ─── 4. Human gate ────────────────────────────────────────────────────────────

/**
 * Identify findings that require human review before entering final output.
 * Criteria: low confidence OR failed verification OR unresolved challenge.
 */
export function identifyGateRequests(taskId: string, findings: Finding[]): GateRequest[] {
  const threshold = Config.debate.gateConfidenceThreshold;
  const gates: GateRequest[] = [];

  for (const finding of findings) {
    const needsGate =
      finding.confidence < threshold ||
      (finding.challenged && !finding.resolved) ||
      finding.verificationResult?.passed === false;

    if (needsGate) {
      gates.push({
        id: crypto.randomUUID(),
        taskId,
        findingId: finding.id,
        finding,
        status: "pending",
        createdAt: new Date(),
      });
    }
  }

  if (gates.length) {
    logger.info("Human gate requests created", { count: gates.length, taskId });
  }

  return gates;
}

// ─── Utility ──────────────────────────────────────────────────────────────────

async function callModel(
  system: string,
  user: string,
  maxTokens: number,
  model?: string,
  opts?: { thinking?: { budgetTokens: number } },
): Promise<string> {
  const m = model ?? Config.anthropic.model;
  const provider = getProvider(m);
  const response = await provider.chat({
    model: resolveModelId(m),
    maxTokens,
    system,
    messages: [{ role: "user", content: user }],
    cacheSystem: true,
    ...(opts?.thinking && { thinking: opts.thinking }),
  });
  const block = response.content.find((b) => b.type === "text");
  if (!block || block.type !== "text") throw new Error("Unexpected content type from model");
  return block.text;
}

function parseChallenge(text: string, challengerId: string): Challenge {
  const contentMatch = text.match(/Content:\s*([\s\S]+?)(?=Citation:|Strength:|END_CHALLENGE)/i);
  const citationMatch = text.match(/Citation:\s*SOURCE=(.+?)\s*\|\s*QUOTE=(.+?)(?=\n|END_CHALLENGE)/i);
  const strengthMatch = text.match(/Strength:\s*(\d)/i);

  const citations: Citation[] = citationMatch
    ? [{ source: citationMatch[1].trim(), quote: citationMatch[2].trim(), mechanicallyVerified: false }]
    : [];

  return {
    challengerId,
    challengerName: "Adversarial Challenger",
    content: contentMatch?.[1]?.trim() ?? text,
    citations,
  };
}

function parseResolution(text: string): {
  verdict: "UPHELD" | "MODIFIED" | "OVERTURNED";
  reasoning: string;
  modifiedContent?: string;
} {
  const verdictMatch = text.match(/RESOLUTION:\s*(UPHELD|MODIFIED|OVERTURNED)/i);
  const reasoningMatch = text.match(/REASONING:\s*([\s\S]+?)(?=MODIFIED_CONTENT:|$)/i);
  const modifiedMatch = text.match(/MODIFIED_CONTENT:\s*([\s\S]+)/i);

  return {
    verdict: (verdictMatch?.[1]?.toUpperCase() as "UPHELD" | "MODIFIED" | "OVERTURNED") ?? "UPHELD",
    reasoning: reasoningMatch?.[1]?.trim() ?? "",
    modifiedContent: modifiedMatch?.[1]?.trim() || undefined,
  };
}