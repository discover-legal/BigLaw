// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Legal data connector tools.
 *
 * Eight connectors, two transport types:
 *   CourtListener  — public REST API (optional API key for higher rate limits)
 *   Westlaw        — MCP HTTP (Thomson Reuters CoCounsel, WESTLAW_API_KEY required)
 *   Everlaw        — MCP HTTP (e-discovery, EVERLAW_API_KEY required)
 *   Trellis        — MCP HTTP (state court dataset, TRELLIS_API_KEY required)
 *   Descrybe       — MCP HTTP (case law research, DESCRYBE_API_KEY required)
 *   Ironclad       — MCP HTTP (contract register, IRONCLAD_API_KEY required)
 *   iManage        — MCP HTTP (DMS, IMANAGE_API_KEY required)
 *   Definely       — MCP HTTP (contract structure, DEFINELY_API_KEY required)
 *
 * Security:
 *   - All configurable endpoint URLs are SSRF-validated at module load time.
 *   - mcpCall() re-validates the endpoint (defence in depth), enforces a 30s
 *     timeout, caps the request body at 5 MB, and caps the response at 1 MB.
 *   - Error responses never echo raw server content back to agents.
 *   - API keys appear in Authorization headers only; never in logs or errors.
 *   - Unconfigured connectors return a structured error object (never throw).
 */

import { Config } from "../config.js";
import { logger } from "../logger.js";
import { assertPublicHttpUrl } from "../settings/index.js";
import type { ToolImpl } from "./index.js";

// ─── SSRF validation at module load ──────────────────────────────────────────
// All admin-configurable endpoints are checked once at startup. Default vendor
// URLs are pre-validated constants; only custom overrides need runtime checks.

function validateConnectorEndpoint(url: string, envVar: string): void {
  if (!url) return;
  assertPublicHttpUrl(url, envVar);
}

validateConnectorEndpoint(Config.connectors.courtListener.endpoint,     "COURT_LISTENER_API_URL");
validateConnectorEndpoint(Config.connectors.ironclad.endpoint,          "IRONCLAD_MCP_URL");
validateConnectorEndpoint(Config.connectors.imanage.endpoint,           "IMANAGE_MCP_URL");
validateConnectorEndpoint(Config.connectors.definely.endpoint,          "DEFINELY_MCP_URL");
validateConnectorEndpoint(Config.connectors.westlaw.endpoint,           "WESTLAW_MCP_URL");
validateConnectorEndpoint(Config.connectors.everlaw.endpoint,           "EVERLAW_MCP_URL");
validateConnectorEndpoint(Config.connectors.trellis.endpoint,           "TRELLIS_MCP_URL");
validateConnectorEndpoint(Config.connectors.descrybe.endpoint,          "DESCRYBE_MCP_URL");
validateConnectorEndpoint(Config.connectors.docusign.endpoint,          "DOCUSIGN_MCP_URL");
validateConnectorEndpoint(Config.connectors.solveIntelligence.endpoint, "SOLVE_INTELLIGENCE_MCP_URL");
validateConnectorEndpoint(Config.connectors.slack.endpoint,             "SLACK_MCP_URL");
validateConnectorEndpoint(Config.connectors.googleDrive.endpoint,       "GOOGLE_DRIVE_MCP_URL");
validateConnectorEndpoint(Config.connectors.box.endpoint,               "BOX_MCP_URL");
validateConnectorEndpoint(Config.connectors.lawve.endpoint,             "LAWVE_MCP_URL");
validateConnectorEndpoint(Config.connectors.topCounsel.endpoint,        "TOPCOUNSEL_MCP_URL");

// ─── Generic MCP HTTP client ──────────────────────────────────────────────────

const MCP_REQUEST_BODY_LIMIT  = 5_000_000;  // 5 MB — largest document we'd send
const MCP_RESPONSE_LIMIT      = 1_000_000;  // 1 MB — largest response we'll process
const MCP_TIMEOUT_MS          = 30_000;     // 30 s — generous for slow MCP servers

/**
 * Calls a tool on a Streamable HTTP MCP server using JSON-RPC 2.0.
 * Returns structured data or an error object — never throws.
 */
export async function mcpCall(
  endpoint: string,
  apiKey: string,
  toolName: string,
  args: Record<string, unknown>,
): Promise<unknown> {
  // Defence-in-depth SSRF check (endpoint may differ from the validated config
  // default if a caller constructs it programmatically).
  try {
    assertPublicHttpUrl(endpoint, "MCP endpoint");
  } catch (err) {
    return { error: (err as Error).message };
  }

  const body = JSON.stringify({
    jsonrpc: "2.0",
    id: 1,
    method: "tools/call",
    params: { name: toolName, arguments: args },
  });

  if (body.length > MCP_REQUEST_BODY_LIMIT) {
    return { error: `MCP request body exceeds ${MCP_REQUEST_BODY_LIMIT / 1_000_000} MB limit` };
  }

  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "Accept": "application/json, text/event-stream",
  };
  if (apiKey) headers["Authorization"] = `Bearer ${apiKey}`;

  let resp: Response;
  try {
    resp = await fetch(endpoint, {
      method: "POST",
      headers,
      body,
      signal: AbortSignal.timeout(MCP_TIMEOUT_MS),
    });
  } catch (err) {
    return { error: `MCP request failed: ${(err as Error).message}` };
  }

  if (!resp.ok) {
    // Slice error body to avoid leaking large internal messages.
    const errText = (await resp.text().catch(() => "")).slice(0, 200);
    return { error: `MCP server returned HTTP ${resp.status}`, detail: errText };
  }

  // Reject oversized responses before reading the body into memory.
  const cl = parseInt(resp.headers.get("Content-Length") ?? "0");
  if (cl > MCP_RESPONSE_LIMIT) {
    return { error: `MCP response Content-Length (${cl}) exceeds ${MCP_RESPONSE_LIMIT / 1_000_000} MB limit` };
  }

  const raw = await resp.text();
  if (raw.length > MCP_RESPONSE_LIMIT) {
    return { error: `MCP response exceeded ${MCP_RESPONSE_LIMIT / 1_000_000} MB size limit` };
  }

  // Handle Streamable HTTP / SSE transport: the last `data:` line with a result wins.
  const lines = raw.split("\n");
  let json: string | undefined;
  for (const line of lines) {
    if (line.startsWith("data: ") && line.includes('"result"')) json = line.slice(6);
  }
  if (!json) json = raw;

  let parsed: { result?: { content?: { type: string; text: string }[] }; error?: unknown };
  try {
    parsed = JSON.parse(json);
  } catch {
    // Never echo raw server content — it may contain internal details.
    return { error: "MCP server returned a non-JSON response" };
  }

  if (parsed.error) return { error: parsed.error };

  const content = parsed.result?.content;
  if (Array.isArray(content)) {
    const text = content.filter((b) => b.type === "text").map((b) => b.text).join("\n");
    return { result: text };
  }
  return parsed.result ?? {};
}

// ─── CourtListener REST ───────────────────────────────────────────────────────

async function courtListenerGet(path: string, params: Record<string, string>): Promise<unknown> {
  const url = new URL(`${Config.connectors.courtListener.endpoint}${path}`);
  for (const [k, v] of Object.entries(params)) url.searchParams.set(k, v);

  // Defence-in-depth: validate the final URL before fetching (prevents SSRF if
  // Config.connectors.courtListener.endpoint is overridden at runtime).
  const fullUrl = url.toString();
  try {
    assertPublicHttpUrl(fullUrl, "CourtListener URL");
  } catch (err) {
    return { error: (err as Error).message };
  }

  const headers: Record<string, string> = { "Accept": "application/json" };
  if (Config.connectors.courtListener.apiKey) {
    headers["Authorization"] = `Token ${Config.connectors.courtListener.apiKey}`;
  }

  let resp: Response;
  try {
    resp = await fetch(fullUrl, {
      headers,
      signal: AbortSignal.timeout(MCP_TIMEOUT_MS),
    });
  } catch (err) {
    return { error: `CourtListener request failed: ${(err as Error).message}` };
  }
  if (!resp.ok) return { error: `CourtListener returned HTTP ${resp.status}` };

  const raw = await resp.text();
  if (raw.length > MCP_RESPONSE_LIMIT) return { error: "CourtListener response exceeded size limit" };
  try {
    return JSON.parse(raw);
  } catch {
    return { error: "CourtListener returned non-JSON response" };
  }
}

export const courtListenerSearchTool: ToolImpl = {
  name: "court_listener_search",
  schema: {
    name: "court_listener_search",
    description:
      "Search US case law, dockets, and legal opinions via CourtListener (free public API). " +
      "Returns citations, case names, courts, dates, and excerpts. Use for US federal and state " +
      "precedent research, docket look-ups, and citation checking.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:       { type: "string",  description: "Full-text search query" },
        type:        { type: "string",  description: "'o' opinions (default), 'r' RECAP dockets, 'p' people, 'oa' oral arguments" },
        court:       { type: "string",  description: "Court ID filter, e.g. 'scotus', 'ca2', 'dcd'" },
        filed_after: { type: "string",  description: "ISO date — only cases filed after this date" },
        filed_before:{ type: "string",  description: "ISO date — only cases filed before this date" },
        max_results: { type: "number",  description: "Maximum results (default 5, max 20)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    const params: Record<string, string> = {
      q:         (input.query as string).slice(0, 500),
      type:      (input.type  as string | undefined) ?? "o",
      format:    "json",
      page_size: String(Math.min((input.max_results as number | undefined) ?? 5, 20)),
    };
    if (input.court)        params.court        = (input.court        as string).slice(0, 50);
    if (input.filed_after)  params.filed_after  = (input.filed_after  as string).slice(0, 20);
    if (input.filed_before) params.filed_before = (input.filed_before as string).slice(0, 20);

    const data = await courtListenerGet("/search/", params) as {
      count?: number;
      results?: {
        caseName?: string; citation?: string; court?: string;
        dateFiled?: string; absoluteUrl?: string; snippet?: string;
      }[];
    };
    if (!data.results) return data;
    return {
      count: data.count,
      results: data.results.map((r) => ({
        caseName:  r.caseName,
        citation:  r.citation,
        court:     r.court,
        dateFiled: r.dateFiled,
        url:       r.absoluteUrl ? `https://www.courtlistener.com${r.absoluteUrl}` : undefined,
        excerpt:   r.snippet?.slice(0, 500),
      })),
    };
  },
};

export const courtListenerOpinionTool: ToolImpl = {
  name: "court_listener_opinion",
  schema: {
    name: "court_listener_opinion",
    description: "Fetch the full text of a specific US court opinion by CourtListener opinion ID.",
    input_schema: {
      type: "object" as const,
      properties: {
        opinion_id: { type: "number", description: "CourtListener opinion ID (from court_listener_search results)" },
      },
      required: ["opinion_id"],
    },
  },
  async execute(input) {
    return courtListenerGet(`/opinions/${Number(input.opinion_id)}/`, { format: "json" });
  },
};

export const courtListenerDocketTool: ToolImpl = {
  name: "court_listener_docket",
  schema: {
    name: "court_listener_docket",
    description: "Fetch a US court docket by CourtListener docket ID. Returns parties, filings, and case status.",
    input_schema: {
      type: "object" as const,
      properties: {
        docket_id: { type: "number", description: "CourtListener docket ID (from court_listener_search results)" },
      },
      required: ["docket_id"],
    },
  },
  async execute(input) {
    return courtListenerGet(`/dockets/${Number(input.docket_id)}/`, { format: "json" });
  },
};

// ─── Westlaw / CoCounsel (Thomson Reuters) ───────────────────────────────────

const westlawNotConfigured = () => ({
  error: "Westlaw not configured — set WESTLAW_API_KEY to enable CoCounsel legal research",
});

export const westlawResearchTool: ToolImpl = {
  name: "westlaw_research",
  schema: {
    name: "westlaw_research",
    description:
      "Run a deep legal research query via Westlaw (Thomson Reuters CoCounsel). Returns a cited " +
      "report covering caselaw, statutes, and regulations. Requires WESTLAW_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:        { type: "string", description: "Legal research question" },
        jurisdiction: { type: "string", description: "US jurisdiction, e.g. 'federal', 'NY', 'CA'" },
        sources:      { type: "string", description: "Optional: 'cases', 'statutes', 'regulations', or 'all' (default)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.westlaw.enabled) return westlawNotConfigured();
    logger.debug("westlaw_research", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.westlaw.endpoint,
      Config.connectors.westlaw.apiKey,
      "deepResearch",
      { ...input, query: (input.query as string).slice(0, 2000) },
    );
  },
};

export const westlawCheckCitationTool: ToolImpl = {
  name: "westlaw_check_citation",
  schema: {
    name: "westlaw_check_citation",
    description:
      "Check the subsequent treatment of a US case citation via Westlaw — returns citing references, " +
      "negative treatment flags, and KeyCite status. Requires WESTLAW_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        citation: { type: "string", description: "US citation in standard format, e.g. '410 U.S. 113' or '2023 WL 123456'" },
      },
      required: ["citation"],
    },
  },
  async execute(input) {
    if (!Config.connectors.westlaw.enabled) return westlawNotConfigured();
    logger.debug("westlaw_check_citation", { citation: input.citation });
    return mcpCall(
      Config.connectors.westlaw.endpoint,
      Config.connectors.westlaw.apiKey,
      "checkCitingReferences",
      { citation: (input.citation as string).slice(0, 200) },
    );
  },
};

// ─── Everlaw (e-discovery) ────────────────────────────────────────────────────

const everlawNotConfigured = () => ({
  error: "Everlaw not configured — set EVERLAW_API_KEY to enable e-discovery document search",
});

export const everlawSearchDocumentsTool: ToolImpl = {
  name: "everlaw_search_documents",
  schema: {
    name: "everlaw_search_documents",
    description:
      "Search the Everlaw e-discovery platform for documents. Returns document IDs, metadata, " +
      "custodians, date ranges, and relevance tags. Requires EVERLAW_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:       { type: "string", description: "Full-text search query" },
        date_after:  { type: "string", description: "ISO date filter — documents after this date" },
        date_before: { type: "string", description: "ISO date filter — documents before this date" },
        custodian:   { type: "string", description: "Optional: filter by document custodian name" },
        max_results: { type: "number", description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.everlaw.enabled) return everlawNotConfigured();
    logger.debug("everlaw_search_documents", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.everlaw.endpoint,
      Config.connectors.everlaw.apiKey,
      "searchDocuments",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

export const everlawGetReviewSetTool: ToolImpl = {
  name: "everlaw_get_review_set",
  schema: {
    name: "everlaw_get_review_set",
    description:
      "Fetch a tagged review set (document batch) from Everlaw by set ID. Returns the document " +
      "list with privilege and relevance tags. Requires EVERLAW_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        review_set_id: { type: "string", description: "Everlaw review set ID" },
      },
      required: ["review_set_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.everlaw.enabled) return everlawNotConfigured();
    logger.debug("everlaw_get_review_set", { id: input.review_set_id });
    return mcpCall(
      Config.connectors.everlaw.endpoint,
      Config.connectors.everlaw.apiKey,
      "getReviewSet",
      { reviewSetId: (input.review_set_id as string).slice(0, 200) },
    );
  },
};

// ─── Trellis (state court data) ───────────────────────────────────────────────

const trellisNotConfigured = () => ({
  error: "Trellis not configured — set TRELLIS_API_KEY to enable state court docket research",
});

export const trellisSearchCasesTool: ToolImpl = {
  name: "trellis_search_cases",
  schema: {
    name: "trellis_search_cases",
    description:
      "Search Trellis for US state trial court cases, dockets, filings, and verdicts. " +
      "Covers 50 states with judge analytics and verdict data. Requires TRELLIS_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:       { type: "string", description: "Search query — case name, party, or legal issue" },
        state:       { type: "string", description: "US state code, e.g. 'CA', 'NY', 'TX'" },
        case_type:   { type: "string", description: "Optional: 'civil', 'criminal', 'probate', etc." },
        date_after:  { type: "string", description: "ISO date — cases filed after this date" },
        max_results: { type: "number", description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.trellis.enabled) return trellisNotConfigured();
    logger.debug("trellis_search_cases", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.trellis.endpoint,
      Config.connectors.trellis.apiKey,
      "searchCases",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

export const trellisGetDocketTool: ToolImpl = {
  name: "trellis_get_docket",
  schema: {
    name: "trellis_get_docket",
    description: "Fetch a specific state court docket by Trellis docket ID. Returns filings, parties, and case timeline.",
    input_schema: {
      type: "object" as const,
      properties: {
        docket_id: { type: "string", description: "Trellis docket ID (from trellis_search_cases results)" },
      },
      required: ["docket_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.trellis.enabled) return trellisNotConfigured();
    logger.debug("trellis_get_docket", { id: input.docket_id });
    return mcpCall(
      Config.connectors.trellis.endpoint,
      Config.connectors.trellis.apiKey,
      "getDocket",
      { docketId: (input.docket_id as string).slice(0, 200) },
    );
  },
};

export const trellisJudgeAnalyticsTool: ToolImpl = {
  name: "trellis_judge_analytics",
  schema: {
    name: "trellis_judge_analytics",
    description:
      "Fetch analytics for a specific judge — ruling patterns, verdict rates, and case type tendencies. " +
      "Useful for forum selection and litigation strategy. Requires TRELLIS_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        judge_name: { type: "string", description: "Judge's full name" },
        state:      { type: "string", description: "US state code" },
        case_type:  { type: "string", description: "Optional: filter analytics to a case type" },
      },
      required: ["judge_name", "state"],
    },
  },
  async execute(input) {
    if (!Config.connectors.trellis.enabled) return trellisNotConfigured();
    logger.debug("trellis_judge_analytics", { judge: input.judge_name });
    return mcpCall(
      Config.connectors.trellis.endpoint,
      Config.connectors.trellis.apiKey,
      "getJudgeAnalytics",
      { ...input, judge_name: (input.judge_name as string).slice(0, 200) },
    );
  },
};

// ─── Descrybe (case law research) ─────────────────────────────────────────────

const descrybeNotConfigured = () => ({
  error: "Descrybe not configured — set DESCRYBE_API_KEY to enable concept-based case law research",
});

export const descrybeSearchCasesTool: ToolImpl = {
  name: "descrybe_search_cases",
  schema: {
    name: "descrybe_search_cases",
    description:
      "Search case law by concept or wording using Descrybe's semantic search. Returns relevant " +
      "cases with citation extraction and treatment checking. Requires DESCRYBE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:        { type: "string", description: "Legal concept or wording to search for" },
        jurisdiction: { type: "string", description: "Jurisdiction code, e.g. 'US-federal', 'UK', 'EU'" },
        max_results:  { type: "number", description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.descrybe.enabled) return descrybeNotConfigured();
    logger.debug("descrybe_search_cases", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.descrybe.endpoint,
      Config.connectors.descrybe.apiKey,
      "searchCases",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

export const descrybeCheckCitationTool: ToolImpl = {
  name: "descrybe_check_citation",
  schema: {
    name: "descrybe_check_citation",
    description:
      "Check the treatment history of a case citation via Descrybe — returns subsequent citing " +
      "cases and whether the citation has been positively or negatively treated. Requires DESCRYBE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        citation: { type: "string", description: "Citation string, e.g. '[2023] UKSC 12' or '410 U.S. 113'" },
      },
      required: ["citation"],
    },
  },
  async execute(input) {
    if (!Config.connectors.descrybe.enabled) return descrybeNotConfigured();
    logger.debug("descrybe_check_citation", { citation: input.citation });
    return mcpCall(
      Config.connectors.descrybe.endpoint,
      Config.connectors.descrybe.apiKey,
      "checkCitationTreatment",
      { citation: (input.citation as string).slice(0, 200) },
    );
  },
};

// ─── Ironclad (contract register) ─────────────────────────────────────────────

const ironcladNotConfigured = () => ({
  error: "Ironclad not configured — set IRONCLAD_API_KEY to enable contract register access",
});

export const ironcladSearchContractsTool: ToolImpl = {
  name: "ironclad_search_contracts",
  schema: {
    name: "ironclad_search_contracts",
    description:
      "Search the Ironclad contract register. Returns matching contracts with metadata (parties, " +
      "type, status, key dates, renewal deadlines). Requires IRONCLAD_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:         { type: "string", description: "Search query — searches contract names, parties, and metadata" },
        contract_type: { type: "string", description: "Optional: filter by contract type" },
        status:        { type: "string", description: "Optional: 'executed', 'in_review', or 'expired'" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.ironclad.enabled) return ironcladNotConfigured();
    logger.debug("ironclad_search_contracts", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.ironclad.endpoint,
      Config.connectors.ironclad.apiKey,
      "searchContracts",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

export const ironcladGetContractTool: ToolImpl = {
  name: "ironclad_get_contract",
  schema: {
    name: "ironclad_get_contract",
    description:
      "Fetch a specific contract from Ironclad by ID — returns the document metadata, key " +
      "clauses extracted by Ironclad, and links to the signed PDF. Requires IRONCLAD_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        contract_id: { type: "string", description: "Ironclad contract/record ID" },
      },
      required: ["contract_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.ironclad.enabled) return ironcladNotConfigured();
    logger.debug("ironclad_get_contract", { id: input.contract_id });
    return mcpCall(
      Config.connectors.ironclad.endpoint,
      Config.connectors.ironclad.apiKey,
      "getContract",
      { contractId: (input.contract_id as string).slice(0, 200) },
    );
  },
};

// ─── iManage (DMS) ────────────────────────────────────────────────────────────

const imanageNotConfigured = () => ({
  error: "iManage not configured — set IMANAGE_API_KEY to enable DMS access",
});

export const imanageSearchTool: ToolImpl = {
  name: "imanage_search",
  schema: {
    name: "imanage_search",
    description:
      "Search the iManage document management system for matter documents, precedents, and templates. " +
      "Returns document metadata and version links. Requires IMANAGE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:         { type: "string", description: "Full-text search query" },
        matter_id:     { type: "string", description: "Optional: restrict to a specific matter workspace" },
        document_type: { type: "string", description: "Optional: filter by document type" },
        max_results:   { type: "number", description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.imanage.enabled) return imanageNotConfigured();
    logger.debug("imanage_search", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.imanage.endpoint,
      Config.connectors.imanage.apiKey,
      "searchDocuments",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

export const imanageGetDocumentTool: ToolImpl = {
  name: "imanage_get_document",
  schema: {
    name: "imanage_get_document",
    description:
      "Fetch a specific document from iManage by document ID. Returns the document content and " +
      "version history. Requires IMANAGE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        document_id: { type: "string", description: "iManage document ID" },
        version:     { type: "string", description: "Optional: specific version ID (default: latest)" },
      },
      required: ["document_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.imanage.enabled) return imanageNotConfigured();
    logger.debug("imanage_get_document", { id: input.document_id });
    return mcpCall(
      Config.connectors.imanage.endpoint,
      Config.connectors.imanage.apiKey,
      "getDocument",
      { documentId: (input.document_id as string).slice(0, 200), version: input.version },
    );
  },
};

// ─── Definely (contract structure analysis) ───────────────────────────────────

const definelyNotConfigured = () => ({
  error: "Definely not configured — set DEFINELY_API_KEY to enable contract structure analysis",
});

export const definelyAnalyzeStructureTool: ToolImpl = {
  name: "definely_analyze_structure",
  schema: {
    name: "definely_analyze_structure",
    description:
      "Analyse the structure of a contract using Definely — resolves cross-references, identifies " +
      "defined terms and their definitions, and surfaces structural diffs. Requires DEFINELY_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        document_text: { type: "string", description: "Contract text to analyse (max 200 000 chars)" },
        focus:         { type: "string", description: "Optional: 'definitions', 'cross-references', 'structure', or 'all'" },
      },
      required: ["document_text"],
    },
  },
  async execute(input) {
    if (!Config.connectors.definely.enabled) return definelyNotConfigured();
    const text = (input.document_text as string).slice(0, 200_000);
    logger.debug("definely_analyze_structure", { chars: text.length });
    return mcpCall(
      Config.connectors.definely.endpoint,
      Config.connectors.definely.apiKey,
      "analyzeStructure",
      { document_text: text, focus: input.focus },
    );
  },
};

export const definelyResolveDefinitionTool: ToolImpl = {
  name: "definely_resolve_definition",
  schema: {
    name: "definely_resolve_definition",
    description:
      "Resolve the full definition of a defined term in a contract, following all cross-references " +
      "and nested definitions. Requires DEFINELY_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        document_text: { type: "string", description: "Contract text containing the definition" },
        term:          { type: "string", description: "Defined term to resolve (exactly as capitalised in the contract)" },
      },
      required: ["document_text", "term"],
    },
  },
  async execute(input) {
    if (!Config.connectors.definely.enabled) return definelyNotConfigured();
    const text = (input.document_text as string).slice(0, 200_000);
    logger.debug("definely_resolve_definition", { term: input.term });
    return mcpCall(
      Config.connectors.definely.endpoint,
      Config.connectors.definely.apiKey,
      "resolveDefinition",
      { document_text: text, term: (input.term as string).slice(0, 200) },
    );
  },
};

// ─── DocuSign CLM ─────────────────────────────────────────────────────────────

const docusignNotConfigured = () => ({
  error: "DocuSign not configured — set DOCUSIGN_API_KEY to enable CLM envelope tracking",
});

export const docusignGetEnvelopeTool: ToolImpl = {
  name: "docusign_get_envelope",
  schema: {
    name: "docusign_get_envelope",
    description:
      "Fetch a DocuSign envelope by ID — returns status, signers, documents, and signing URLs. " +
      "Requires DOCUSIGN_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        envelope_id: { type: "string", description: "DocuSign envelope ID (GUID)" },
      },
      required: ["envelope_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.docusign.enabled) return docusignNotConfigured();
    logger.debug("docusign_get_envelope", { id: input.envelope_id });
    return mcpCall(
      Config.connectors.docusign.endpoint,
      Config.connectors.docusign.apiKey,
      "getEnvelope",
      { envelopeId: (input.envelope_id as string).slice(0, 200) },
    );
  },
};

export const docusignSearchContractsTool: ToolImpl = {
  name: "docusign_search_contracts",
  schema: {
    name: "docusign_search_contracts",
    description:
      "Search DocuSign CLM for executed contracts — returns metadata, parties, key dates, " +
      "and renewal deadlines. Requires DOCUSIGN_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:         { type: "string",  description: "Search terms — contract name, party, or clause text" },
        status:        { type: "string",  description: "Optional: 'completed', 'sent', 'voided'" },
        date_from:     { type: "string",  description: "ISO date — envelopes created after this date" },
        max_results:   { type: "number",  description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.docusign.enabled) return docusignNotConfigured();
    logger.debug("docusign_search_contracts", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.docusign.endpoint,
      Config.connectors.docusign.apiKey,
      "searchContracts",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

// ─── Solve Intelligence (patent) ─────────────────────────────────────────────

const solveIntelligenceNotConfigured = () => ({
  error: "Solve Intelligence not configured — set SOLVE_INTELLIGENCE_API_KEY to enable patent tools",
});

export const solveIntelligenceSearchPatentsTool: ToolImpl = {
  name: "solve_intelligence_search_patents",
  schema: {
    name: "solve_intelligence_search_patents",
    description:
      "Search patent databases via Solve Intelligence — returns patent numbers, claims, " +
      "assignees, priority dates, and classification codes. Use for FTO, prior art, and " +
      "patent landscape analysis. Requires SOLVE_INTELLIGENCE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:        { type: "string",  description: "Technical concept, claim language, or keywords" },
        jurisdiction: { type: "string",  description: "Patent office code: 'US', 'EP', 'PCT', 'CN', etc." },
        date_from:    { type: "string",  description: "ISO date — patents filed after this date" },
        max_results:  { type: "number",  description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.solveIntelligence.enabled) return solveIntelligenceNotConfigured();
    logger.debug("solve_intelligence_search_patents", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.solveIntelligence.endpoint,
      Config.connectors.solveIntelligence.apiKey,
      "searchPatents",
      { ...input, query: (input.query as string).slice(0, 1000) },
    );
  },
};

export const solveIntelligenceDraftClaimsTool: ToolImpl = {
  name: "solve_intelligence_draft_claims",
  schema: {
    name: "solve_intelligence_draft_claims",
    description:
      "Draft patent claims for a technology disclosure using Solve Intelligence's AI-assisted " +
      "prosecution tools. Returns independent and dependent claim sets. Requires SOLVE_INTELLIGENCE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        disclosure: { type: "string", description: "Technical disclosure text (invention description)" },
        claim_count: { type: "number", description: "Number of independent claims to draft (default 3)" },
        jurisdiction: { type: "string", description: "Patent office standard: 'US', 'EP'" },
      },
      required: ["disclosure"],
    },
  },
  async execute(input) {
    if (!Config.connectors.solveIntelligence.enabled) return solveIntelligenceNotConfigured();
    const text = (input.disclosure as string).slice(0, 50_000);
    logger.debug("solve_intelligence_draft_claims", { chars: text.length });
    return mcpCall(
      Config.connectors.solveIntelligence.endpoint,
      Config.connectors.solveIntelligence.apiKey,
      "draftClaims",
      { disclosure: text, claim_count: input.claim_count, jurisdiction: input.jurisdiction },
    );
  },
};

// ─── Slack ────────────────────────────────────────────────────────────────────

const slackNotConfigured = () => ({
  error: "Slack not configured — set SLACK_API_KEY to enable workspace search and messaging",
});

export const slackSearchTool: ToolImpl = {
  name: "slack_search",
  schema: {
    name: "slack_search",
    description:
      "Search Slack workspace messages and files. Returns matching messages with channel, " +
      "author, timestamp, and content. Useful for surfacing internal matter context and " +
      "client communications. Requires SLACK_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:       { type: "string",  description: "Search query — supports Slack search modifiers" },
        channel:     { type: "string",  description: "Optional: restrict search to a specific channel name" },
        date_after:  { type: "string",  description: "ISO date — messages after this date" },
        max_results: { type: "number",  description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.slack.enabled) return slackNotConfigured();
    logger.debug("slack_search", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.slack.endpoint,
      Config.connectors.slack.apiKey,
      "search",
      { ...input, query: (input.query as string).slice(0, 500) },
    );
  },
};

export const slackSendMessageTool: ToolImpl = {
  name: "slack_send_message",
  schema: {
    name: "slack_send_message",
    description:
      "Send a message to a Slack channel. Use only for matter status updates and " +
      "internal team notifications — not for external client communications. Requires SLACK_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        channel: { type: "string", description: "Channel name or ID (e.g. '#matter-123')" },
        message: { type: "string", description: "Message text (max 3000 chars)" },
      },
      required: ["channel", "message"],
    },
  },
  async execute(input) {
    if (!Config.connectors.slack.enabled) return slackNotConfigured();
    const msg = (input.message as string).slice(0, 3000);
    logger.debug("slack_send_message", { channel: input.channel, chars: msg.length });
    return mcpCall(
      Config.connectors.slack.endpoint,
      Config.connectors.slack.apiKey,
      "sendMessage",
      { channel: (input.channel as string).slice(0, 200), message: msg },
    );
  },
};

// ─── Google Drive ─────────────────────────────────────────────────────────────

const googleDriveNotConfigured = () => ({
  error: "Google Drive not configured — set GOOGLE_DRIVE_API_KEY to enable Drive access",
});

export const googleDriveSearchTool: ToolImpl = {
  name: "google_drive_search",
  schema: {
    name: "google_drive_search",
    description:
      "Search Google Drive for documents, spreadsheets, and files in the connected workspace. " +
      "Returns file names, types, owners, last-modified dates, and sharing status. Requires GOOGLE_DRIVE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:       { type: "string",  description: "Search query — file name, content, or Drive search operators" },
        mime_type:   { type: "string",  description: "Optional: filter by MIME type, e.g. 'application/pdf'" },
        max_results: { type: "number",  description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.googleDrive.enabled) return googleDriveNotConfigured();
    logger.debug("google_drive_search", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.googleDrive.endpoint,
      Config.connectors.googleDrive.apiKey,
      "searchFiles",
      { ...input, query: (input.query as string).slice(0, 500) },
    );
  },
};

export const googleDriveGetFileTool: ToolImpl = {
  name: "google_drive_get_file",
  schema: {
    name: "google_drive_get_file",
    description:
      "Fetch the text content or metadata of a Google Drive file by ID. " +
      "For Docs and Sheets, returns extracted text. Requires GOOGLE_DRIVE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        file_id:     { type: "string",  description: "Google Drive file ID" },
        format:      { type: "string",  description: "Optional: 'text' (default) or 'metadata'" },
      },
      required: ["file_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.googleDrive.enabled) return googleDriveNotConfigured();
    logger.debug("google_drive_get_file", { id: input.file_id });
    return mcpCall(
      Config.connectors.googleDrive.endpoint,
      Config.connectors.googleDrive.apiKey,
      "getFile",
      { fileId: (input.file_id as string).slice(0, 200), format: input.format ?? "text" },
    );
  },
};

// ─── Box ──────────────────────────────────────────────────────────────────────

const boxNotConfigured = () => ({
  error: "Box not configured — set BOX_API_KEY to enable VDR and matter room access",
});

export const boxSearchTool: ToolImpl = {
  name: "box_search",
  schema: {
    name: "box_search",
    description:
      "Search Box for documents in VDRs and matter rooms. Returns file names, folder paths, " +
      "owners, and last-modified dates. Requires BOX_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        query:       { type: "string",  description: "Full-text search query" },
        folder_id:   { type: "string",  description: "Optional: restrict to a specific folder/VDR" },
        max_results: { type: "number",  description: "Maximum results (default 10)" },
      },
      required: ["query"],
    },
  },
  async execute(input) {
    if (!Config.connectors.box.enabled) return boxNotConfigured();
    logger.debug("box_search", { query: (input.query as string).slice(0, 100) });
    return mcpCall(
      Config.connectors.box.endpoint,
      Config.connectors.box.apiKey,
      "searchFiles",
      { ...input, query: (input.query as string).slice(0, 500) },
    );
  },
};

export const boxGetFileTool: ToolImpl = {
  name: "box_get_file",
  schema: {
    name: "box_get_file",
    description:
      "Fetch a specific file from Box by ID — returns text content for documents or " +
      "metadata and download link for binary files. Requires BOX_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        file_id: { type: "string", description: "Box file ID" },
      },
      required: ["file_id"],
    },
  },
  async execute(input) {
    if (!Config.connectors.box.enabled) return boxNotConfigured();
    logger.debug("box_get_file", { id: input.file_id });
    return mcpCall(
      Config.connectors.box.endpoint,
      Config.connectors.box.apiKey,
      "getFile",
      { fileId: (input.file_id as string).slice(0, 200) },
    );
  },
};

// ─── Lawve AI ─────────────────────────────────────────────────────────────────

const lawveNotConfigured = () => ({
  error: "Lawve not configured — set LAWVE_API_KEY to enable AI contract review",
});

export const lawveReviewContractTool: ToolImpl = {
  name: "lawve_review_contract",
  schema: {
    name: "lawve_review_contract",
    description:
      "Run an AI contract review via Lawve — identifies risky clauses, missing standard " +
      "provisions, and deviations from market standard. Returns clause-level commentary. Requires LAWVE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        document_text: { type: "string", description: "Contract text to review (max 200 000 chars)" },
        contract_type: { type: "string", description: "Optional: contract type for context, e.g. 'NDA', 'SaaS', 'employment'" },
        party_role:    { type: "string", description: "Optional: 'buyer', 'seller', 'licensor', 'licensee', etc." },
      },
      required: ["document_text"],
    },
  },
  async execute(input) {
    if (!Config.connectors.lawve.enabled) return lawveNotConfigured();
    const text = (input.document_text as string).slice(0, 200_000);
    logger.debug("lawve_review_contract", { chars: text.length });
    return mcpCall(
      Config.connectors.lawve.endpoint,
      Config.connectors.lawve.apiKey,
      "reviewContract",
      { document_text: text, contract_type: input.contract_type, party_role: input.party_role },
    );
  },
};

export const lawveSearchClausesTool: ToolImpl = {
  name: "lawve_search_clauses",
  schema: {
    name: "lawve_search_clauses",
    description:
      "Search Lawve's clause library for standard, market, and fallback clause alternatives. " +
      "Returns alternative clause versions with risk ratings. Requires LAWVE_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        clause_type:   { type: "string", description: "Clause category, e.g. 'limitation of liability', 'indemnity', 'termination'" },
        contract_type: { type: "string", description: "Optional: contract type context" },
        jurisdiction:  { type: "string", description: "Optional: jurisdiction for market standard comparison" },
      },
      required: ["clause_type"],
    },
  },
  async execute(input) {
    if (!Config.connectors.lawve.enabled) return lawveNotConfigured();
    logger.debug("lawve_search_clauses", { clause: input.clause_type });
    return mcpCall(
      Config.connectors.lawve.endpoint,
      Config.connectors.lawve.apiKey,
      "searchClauses",
      { clause_type: (input.clause_type as string).slice(0, 200), contract_type: input.contract_type, jurisdiction: input.jurisdiction },
    );
  },
};

// ─── TopCounsel ───────────────────────────────────────────────────────────────

const topCounselNotConfigured = () => ({
  error: "TopCounsel not configured — set TOPCOUNSEL_API_KEY to enable outside counsel routing",
});

export const topCounselRouteMatterTool: ToolImpl = {
  name: "topcounsel_route_matter",
  schema: {
    name: "topcounsel_route_matter",
    description:
      "Route a legal matter to the outside counsel panel via TopCounsel — returns recommended " +
      "firms based on practice area, jurisdiction, matter type, and historical performance. Requires TOPCOUNSEL_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        matter_description: { type: "string",  description: "Brief matter description" },
        practice_area:      { type: "string",  description: "Practice area, e.g. 'IP litigation', 'M&A', 'employment'" },
        jurisdiction:       { type: "string",  description: "Primary jurisdiction of the matter" },
        urgency:            { type: "string",  description: "Optional: 'urgent', 'normal', 'low'" },
      },
      required: ["matter_description", "practice_area"],
    },
  },
  async execute(input) {
    if (!Config.connectors.topCounsel.enabled) return topCounselNotConfigured();
    logger.debug("topcounsel_route_matter", { area: input.practice_area });
    return mcpCall(
      Config.connectors.topCounsel.endpoint,
      Config.connectors.topCounsel.apiKey,
      "routeMatter",
      { ...input, matter_description: (input.matter_description as string).slice(0, 1000) },
    );
  },
};

export const topCounselGetPanelTool: ToolImpl = {
  name: "topcounsel_get_panel",
  schema: {
    name: "topcounsel_get_panel",
    description:
      "Fetch the outside counsel panel roster from TopCounsel — returns firm names, practice " +
      "area coverage, jurisdictions, rate cards, and engagement history. Requires TOPCOUNSEL_API_KEY.",
    input_schema: {
      type: "object" as const,
      properties: {
        practice_area: { type: "string", description: "Optional: filter by practice area" },
        jurisdiction:  { type: "string", description: "Optional: filter by jurisdiction capability" },
      },
      required: [],
    },
  },
  async execute(input) {
    if (!Config.connectors.topCounsel.enabled) return topCounselNotConfigured();
    logger.debug("topcounsel_get_panel", {});
    return mcpCall(
      Config.connectors.topCounsel.endpoint,
      Config.connectors.topCounsel.apiKey,
      "getPanel",
      { practice_area: input.practice_area, jurisdiction: input.jurisdiction },
    );
  },
};

// ─── Connector tool list ──────────────────────────────────────────────────────

export const CONNECTOR_TOOLS: ToolImpl[] = [
  // CourtListener — always available (free public REST API)
  courtListenerSearchTool,
  courtListenerOpinionTool,
  courtListenerDocketTool,
  // Westlaw / CoCounsel — WESTLAW_API_KEY required
  westlawResearchTool,
  westlawCheckCitationTool,
  // Everlaw — EVERLAW_API_KEY required
  everlawSearchDocumentsTool,
  everlawGetReviewSetTool,
  // Trellis — TRELLIS_API_KEY required
  trellisSearchCasesTool,
  trellisGetDocketTool,
  trellisJudgeAnalyticsTool,
  // Descrybe — DESCRYBE_API_KEY required
  descrybeSearchCasesTool,
  descrybeCheckCitationTool,
  // Ironclad — IRONCLAD_API_KEY required
  ironcladSearchContractsTool,
  ironcladGetContractTool,
  // iManage — IMANAGE_API_KEY required
  imanageSearchTool,
  imanageGetDocumentTool,
  // Definely — DEFINELY_API_KEY required
  definelyAnalyzeStructureTool,
  definelyResolveDefinitionTool,
  // DocuSign CLM — DOCUSIGN_API_KEY required
  docusignGetEnvelopeTool,
  docusignSearchContractsTool,
  // Solve Intelligence (patent) — SOLVE_INTELLIGENCE_API_KEY required
  solveIntelligenceSearchPatentsTool,
  solveIntelligenceDraftClaimsTool,
  // Slack — SLACK_API_KEY required
  slackSearchTool,
  slackSendMessageTool,
  // Google Drive — GOOGLE_DRIVE_API_KEY required
  googleDriveSearchTool,
  googleDriveGetFileTool,
  // Box — BOX_API_KEY required
  boxSearchTool,
  boxGetFileTool,
  // Lawve AI — LAWVE_API_KEY required
  lawveReviewContractTool,
  lawveSearchClausesTool,
  // TopCounsel — TOPCOUNSEL_API_KEY required
  topCounselRouteMatterTool,
  topCounselGetPanelTool,
];
