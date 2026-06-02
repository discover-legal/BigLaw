// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

import Anthropic from "@anthropic-ai/sdk";
import { Config } from "../config.js";
import { logger } from "../logger.js";
import { PRACTICE_AREAS } from "../types.js";
import type { Client, NosLegalTags } from "../types.js";

const client = new Anthropic({ apiKey: Config.anthropic.apiKey });

/** Detect the primary practice area from a document's title + first ~2000 chars. */
export async function detectPracticeArea(title: string, content: string): Promise<string | null> {
  // Strip newlines from title to prevent prompt structure injection.
  const safeTitle = title.replace(/[\r\n]/g, " ").slice(0, 300);
  const snippet = content.slice(0, 2000);
  const prompt = `You are a legal categorisation assistant. Given a document title and excerpt, identify the single most relevant practice area from the list below. Reply with ONLY the exact practice area name, or "Unknown" if none fits.

Practice areas:
${PRACTICE_AREAS.join("\n")}

Document title: ${safeTitle}
Document excerpt:
${snippet}`;

  try {
    const msg = await client.messages.create({
      model: "claude-haiku-4-5-20251001",
      max_tokens: 64,
      messages: [{ role: "user", content: prompt }],
    });
    const text = (msg.content[0] as { type: string; text: string }).text?.trim();
    if (!text || text === "Unknown") return null;
    const match = PRACTICE_AREAS.find((pa) => pa.toLowerCase() === text.toLowerCase());
    return match ?? null;
  } catch (err) {
    logger.warn("Practice area detection failed", { error: (err as Error).message });
    return null;
  }
}

/** Identify which client (if any) a document likely relates to, based on known clients. */
export async function detectClient(
  title: string,
  content: string,
  clients: Client[],
): Promise<{ clientNumber: string; clientName: string } | null> {
  if (!clients.length) return null;
  const safeTitle = title.replace(/[\r\n]/g, " ").slice(0, 300);
  const snippet = content.slice(0, 3000);
  const clientList = clients.map((c) => `- ${c.clientNumber}: ${c.name}`).join("\n");
  const prompt = `You are a legal matter assistant. Given a document and a list of clients, identify which client the document most likely relates to. Reply with ONLY the client number (e.g. "C-001"), or "None" if no clear match.

Clients:
${clientList}

Document title: ${safeTitle}
Document excerpt:
${snippet}`;

  try {
    const msg = await client.messages.create({
      model: "claude-haiku-4-5-20251001",
      max_tokens: 32,
      messages: [{ role: "user", content: prompt }],
    });
    const text = (msg.content[0] as { type: string; text: string }).text?.trim();
    if (!text || text === "None") return null;
    const found = clients.find((c) => c.clientNumber.toLowerCase() === text.toLowerCase());
    return found ? { clientNumber: found.clientNumber, clientName: found.name } : null;
  } catch (err) {
    logger.warn("Client detection failed", { error: (err as Error).message });
    return null;
  }
}

/**
 * Detect NOSLEGAL v4 taxonomy tags from a task title/description.
 * Returns an empty object on any error (LLM unavailable, parse failure) — never throws.
 */
export async function detectNosLegal(title: string, content: string): Promise<NosLegalTags> {
  const safeTitle = title.replace(/[\r\n]/g, " ").slice(0, 300);
  const snippet = content.slice(0, 2000);
  const prompt = `You are a legal taxonomy assistant. Given a task title and description, classify it into NOSLEGAL v4 taxonomy facets. Respond with ONLY valid JSON (no prose, no markdown fences) using exactly this shape:
{
  "areaOfLaw": "<string or omit>",
  "workType": "<Advisory|Transactional|Litigious|Regulatory|Other or omit>",
  "sector": "<string or omit>",
  "assetType": "<string or omit>"
}

Common areaOfLaw values: "Corporate Finance", "Employment", "Intellectual Property", "Real Estate", "Competition", "Tax", "Banking & Finance", "Insolvency", "Data Privacy", "Immigration", "Insurance", "Environmental"
Common sector values: "Financial Services", "Technology", "Healthcare", "Real Estate", "Energy", "Retail", "Media & Entertainment", "Transport", "Government", "Manufacturing"
Common assetType values: "Agreement", "Opinion", "Pleading", "Memorandum", "Report", "Correspondence", "Regulation"

Omit a field entirely if it does not clearly apply. Each value must be under 60 characters.

Task title: ${safeTitle}
Task description:
${snippet}`;

  try {
    const msg = await client.messages.create({
      model: "claude-haiku-4-5-20251001",
      max_tokens: 256,
      messages: [{ role: "user", content: prompt }],
    });
    const text = (msg.content[0] as { type: string; text: string }).text?.trim() ?? "";
    // Strip markdown fences and parse JSON
    const stripped = text.replace(/```(?:json)?/gi, "").trim();
    const start = stripped.indexOf("{");
    const end = stripped.lastIndexOf("}");
    if (start === -1 || end === -1 || end <= start) return {};
    const parsed = JSON.parse(stripped.slice(start, end + 1)) as Record<string, unknown>;
    const result: NosLegalTags = {};
    if (typeof parsed.areaOfLaw === "string" && parsed.areaOfLaw) result.areaOfLaw = parsed.areaOfLaw.slice(0, 60);
    if (typeof parsed.workType === "string" && parsed.workType) result.workType = parsed.workType.slice(0, 60);
    if (typeof parsed.sector === "string" && parsed.sector) result.sector = parsed.sector.slice(0, 60);
    if (typeof parsed.assetType === "string" && parsed.assetType) result.assetType = parsed.assetType.slice(0, 60);
    return result;
  } catch (err) {
    logger.warn("NOSLEGAL detection failed", { error: (err as Error).message });
    return {};
  }
}
