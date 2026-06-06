// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Lavern adapter — imports agents from github.com/AnttiHero/lavern.
 *
 * Lavern defines 67 agents across 9 workflow types. Each agent has:
 *   - A natural-language role description
 *   - A system prompt (the "agent directive")
 *   - A list of MCP tool permissions
 *   - Optional: jurisdiction, specialty, workflow affiliation
 *
 * This adapter converts Lavern's format to our AgentDefinition and maps:
 *   - Lavern orchestrators → T1 Domain Managers
 *   - Lavern specialist agents → T2 Specialists
 *   - Lavern tool-only agents → T3 Tool Agents
 *
 * Usage:
 *   const adapter = new LavernAdapter();
 *   const agents = await adapter.load('/path/to/lavern/agents');
 *   // or from the Lavern config object directly:
 *   const agents = adapter.fromConfigs(lavernAgentConfigs);
 *
 * The imported agents are tagged with source='lavern' in metadata so
 * they can be filtered or weighted differently in DyTopo rounds.
 */

import { readdir, readFile } from "fs/promises";
import { join, extname, resolve, sep } from "path";
import type { AgentDefinition, AgentTier, AgentDomain } from "../types.js";
import type { AgentHarness } from "./index.js";

// ─── Lavern's native format (from their TypeScript source) ───────────────────

export interface LavernAgentConfig {
  id?: string;
  name: string;
  role: string;
  specialty?: string;
  systemPrompt: string;
  /** MCP tool names Lavern permits this agent to call */
  mcpTools: string[];
  /** Lavern workflow affiliation */
  workflow?: string;
  jurisdiction?: string;
  tier?: "orchestrator" | "specialist" | "reviewer" | "tool";
}

// ─── Adapter ──────────────────────────────────────────────────────────────────

export class LavernAdapter implements AgentHarness {
  readonly name = "lavern";
  readonly version = "0.15.0";

  /**
   * Load Lavern agents from a directory of JSON/TS export files.
   * Each file should export a LavernAgentConfig or array of configs.
   * sourcePath must be within the project working directory.
   */
  async load(sourcePath: string): Promise<AgentDefinition[]> {
    const cwd = process.cwd();
    const resolved = resolve(sourcePath);
    if (!resolved.startsWith(cwd + sep) && resolved !== cwd) {
      throw new Error(`Agent source path '${sourcePath}' must be within the project root`);
    }
    const entries = await readdir(resolved);
    const configs: LavernAgentConfig[] = [];

    for (const entry of entries) {
      if (extname(entry) !== ".json") continue;
      const raw = await readFile(join(resolved, entry), "utf8");
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) {
        configs.push(...parsed);
      } else {
        configs.push(parsed);
      }
    }

    return this.fromConfigs(configs);
  }

  /**
   * Convert an array of Lavern agent configs directly (no file I/O).
   * Use this when you have Lavern's configs in memory.
   */
  fromConfigs(configs: LavernAgentConfig[]): AgentDefinition[] {
    return configs.map((c) => this.convert(c));
  }

  private convert(c: LavernAgentConfig): AgentDefinition {
    const tier = this.inferTier(c);
    const domain = this.inferDomain(c);

    return {
      id: c.id ?? `lavern:${slugify(c.name)}`,
      name: `[Lavern] ${c.name}`,
      tier,
      type: tier === 0 ? "root" : tier === 1 ? "manager" : tier === 3 ? "tool" : "specialist",
      domain,
      description: [c.role, c.specialty, c.jurisdiction].filter(Boolean).join(" — "),
      systemPrompt: c.systemPrompt,
      allowedTools: this.mapTools(c.mcpTools),
      skills: extractSkills(c),
      // Preserve jurisdiction tags so DyTopo can filter agents for non-matching matters.
      jurisdictions: c.jurisdiction ? [c.jurisdiction] : undefined,
      metadata: {
        source: "lavern",
        lavernTier: c.tier,
        lavernWorkflow: c.workflow,
      },
    };
  }

  private inferTier(c: LavernAgentConfig): AgentTier {
    if (c.tier === "orchestrator") return 1;
    if (c.tier === "tool") return 3;
    if (c.tier === "reviewer") return 2;
    if (c.tier === "specialist") return 2;
    // Heuristic from role text
    if (/orchestrat|coordinator|manager|lead/i.test(c.role)) return 1;
    if (/tool|search|retrieve|extract|translat/i.test(c.role)) return 3;
    return 2;
  }

  private inferDomain(c: LavernAgentConfig): AgentDomain {
    const text = `${c.role} ${c.specialty ?? ""} ${c.workflow ?? ""}`.toLowerCase();
    if (/research|investigat|find|search/i.test(text)) return "research";
    if (/draft|writ|memo|brief|plead/i.test(text)) return "drafting";
    if (/review|check|verif|audit|challenge/i.test(text)) return "review";
    if (/compli|regulat|gdpr|dma|dsa/i.test(text)) return "compliance";
    if (/analys|assess|evaluat/i.test(text)) return "analysis";
    if (/tool|search|extract|translat/i.test(text)) return "tool";
    return "investigation";
  }

  /**
   * Map Lavern MCP tool names to our internal tool identifiers.
   * Any tool name not in the explicit allowlist is dropped so a crafted
   * Laverne config cannot grant arbitrary internal tools to an agent.
   */
  private mapTools(mcpTools: string[]): string[] {
    const toolMap: Record<string, string> = {
      "mcp_search":          "web_search",
      "mcp_retrieve":        "search_knowledge",
      "mcp_extract":         "extract_from_document",
      "mcp_translate":       "translate",
      "mcp_verify_citation": "citation_check",
      "mcp_draft":           "query_memory",
      "mcp_memory":          "query_memory",
    };
    const PERMITTED_TOOLS = new Set([
      // Core tools
      "web_search", "search_knowledge", "query_memory", "extract_from_document",
      "translate", "citation_check", "list_documents", "read_document",
      "fetch_documents", "find_in_document",
      // Case law & court record connectors
      "court_listener_search", "court_listener_opinion", "court_listener_docket",
      "westlaw_research", "westlaw_check_citation",
      "everlaw_search_documents", "everlaw_get_review_set",
      "trellis_search_cases", "trellis_get_docket", "trellis_judge_analytics",
      "descrybe_search_cases", "descrybe_check_citation",
      // Contract & document management connectors
      "ironclad_search_contracts", "ironclad_get_contract",
      "imanage_search", "imanage_get_document",
      "definely_analyze_structure", "definely_resolve_definition",
    ]);
    return mcpTools
      .map((t) => toolMap[t] ?? t)
      .filter((t) => PERMITTED_TOOLS.has(t));
  }
}

// ─── Lavern workflow adapter ──────────────────────────────────────────────────

/**
 * Lavern defines 9 workflow types as step pipelines with gates and evaluators.
 * We convert them to TaskTemplates so they appear in the template picker and
 * can be submitted via /tasks/from-template.
 *
 * Drop Lavern workflow JSON files in workflows/laverne/ — each file may contain
 * a single LavernWorkflowConfig or an array.
 */
export interface LavernWorkflowConfig {
  id: string;
  name: string;
  description: string;
  /** Lavern's own type name — mapped to our WorkflowType */
  type:
    | "adversarial"
    | "counsel"
    | "full-bench"
    | "legal-design"
    | "pre-engagement"
    | "review"
    | "roundtable"
    | "tabulate"
    | "verification";
  /** Optional: prompt template with {{document}} / {{description}} placeholders */
  promptTemplate?: string;
  /** Which domains the workflow engages */
  preferredDomains?: import("../types.js").AgentDomain[];
  jurisdiction?: string;
  specialty?: string;
}

const LAVERN_WORKFLOW_TYPE_MAP: Record<string, import("../types.js").WorkflowType> = {
  "adversarial":   "adversarial",
  "counsel":       "counsel",
  "full-bench":    "full_bench",
  "legal-design":  "legal_design",
  "pre-engagement":"pre_engagement",
  "review":        "review",
  "roundtable":    "roundtable",
  "tabulate":      "tabulate",
  "verification":  "adversarial",  // closest: adversarial includes verification phases
};

export class LavernWorkflowAdapter {
  /**
   * Load Lavern workflow configs from a directory of JSON files.
   * sourcePath must be within the project working directory.
   */
  async load(sourcePath: string): Promise<TaskTemplate[]> {
    const cwd = process.cwd();
    const resolved = resolve(sourcePath);
    if (!resolved.startsWith(cwd + sep) && resolved !== cwd) {
      throw new Error(`Workflow source path '${sourcePath}' must be within the project root`);
    }
    let entries: string[];
    try {
      entries = await readdir(resolved);
    } catch {
      return [];  // directory absent — not an error
    }

    const configs: LavernWorkflowConfig[] = [];
    for (const entry of entries) {
      if (extname(entry) !== ".json") continue;
      const raw = await readFile(join(resolved, entry), "utf8");
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed)) configs.push(...parsed);
      else configs.push(parsed);
    }

    return this.fromConfigs(configs);
  }

  fromConfigs(configs: LavernWorkflowConfig[]): TaskTemplate[] {
    return configs.map((c) => {
      this.validate(c);
      return this.convert(c);
    });
  }

  private validate(c: LavernWorkflowConfig): void {
    if (!c.id || typeof c.id !== "string") throw new Error("LavernWorkflowConfig: missing or invalid id");
    if (!c.name || typeof c.name !== "string") throw new Error(`LavernWorkflowConfig '${c.id}': missing name`);
    if (!c.description || typeof c.description !== "string") throw new Error(`LavernWorkflowConfig '${c.id}': missing description`);
    const validTypes = ["adversarial","counsel","full-bench","legal-design","pre-engagement","review","roundtable","tabulate","verification"];
    if (!validTypes.includes(c.type)) {
      throw new Error(`LavernWorkflowConfig '${c.id}': invalid type '${c.type}' — must be one of ${validTypes.join(", ")}`);
    }
    if (c.promptTemplate !== undefined) {
      if (typeof c.promptTemplate !== "string") throw new Error(`LavernWorkflowConfig '${c.id}': promptTemplate must be a string`);
      if (c.promptTemplate.length > 10000) throw new Error(`LavernWorkflowConfig '${c.id}': promptTemplate exceeds 10000 chars`);
    }
  }

  private convert(c: LavernWorkflowConfig): TaskTemplate {
    const workflowType = LAVERN_WORKFLOW_TYPE_MAP[c.type] ?? "roundtable";
    const basePrompt = c.promptTemplate ??
      `Complete the following legal task using the ${c.name} workflow.\n\nMatter: {{description}}\n\nDocuments provided: {{document}}`;

    return {
      id: `lavern:${c.id}`,
      name: `[Lavern] ${c.name}`,
      description: [c.description, c.specialty, c.jurisdiction].filter(Boolean).join(" — "),
      taskDescriptionTemplate: basePrompt,
      workflowType,
      preferredDomains: c.preferredDomains,
      source: "lavern",
      metadata: {
        lavernType: c.type,
        jurisdiction: c.jurisdiction,
        specialty: c.specialty,
      },
    };
  }
}

// ─── Mike OSS workflow adapter ────────────────────────────────────────────────

/**
 * Mike OSS Workflows are NOT agents — they are reusable prompt templates (presets)
 * that define a legal task and how to run it. A lawyer picks one, uploads documents,
 * and the platform executes it in a single pass.
 *
 * In our system, Mike OSS workflows map to TaskTemplates. Our T1 managers and T2
 * specialists operate ON these templates — the workflow defines the task; the agents
 * perform it. This is the correct model: the workflow is the task specification,
 * not the actor.
 *
 * A TaskTemplate is stored separately from the agent registry. When a user selects
 * a Mike OSS workflow, it instantiates a Task with the template's description,
 * workflowType, and any structural constraints — and our agent system executes it.
 */
export interface MikeOSSWorkflow {
  id: string;
  name: string;
  /** Short description shown to users in the workflow picker */
  description: string;
  /**
   * The prompt template — defines what the task IS, not who does it.
   * May contain {{document}} placeholders for injecting uploaded documents.
   */
  promptTemplate: string;
  /** Which of our WorkflowType orchestration modes suits this preset */
  workflowType?: import("../types.js").WorkflowType;
  /** Optional: restrict to specific agent domains for this workflow */
  preferredDomains?: import("../types.js").AgentDomain[];
}

export interface TaskTemplate {
  id: string;
  name: string;
  description: string;
  /** Expanded task description — used as Task.description when instantiated */
  taskDescriptionTemplate: string;
  workflowType: import("../types.js").WorkflowType;
  preferredDomains?: import("../types.js").AgentDomain[];
  source: "mikeoss" | "lavern" | "custom";
  metadata?: Record<string, unknown>;
}

/** Convert a Mike OSS workflow to a TaskTemplate. */
export function fromMikeOSSWorkflow(workflow: MikeOSSWorkflow): TaskTemplate {
  return {
    id: `mikeoss:${workflow.id}`,
    name: workflow.name,
    description: workflow.description,
    taskDescriptionTemplate: workflow.promptTemplate,
    workflowType: workflow.workflowType ?? "roundtable",
    preferredDomains: workflow.preferredDomains,
    source: "mikeoss",
    metadata: { originalId: workflow.id },
  };
}

/**
 * Instantiate a TaskTemplate into a task submission payload.
 * The caller substitutes {{document}} and other placeholders before calling this.
 */
export function instantiateTemplate(
  template: TaskTemplate,
  substitutions: Record<string, string> = {},
): { description: string; workflowType: import("../types.js").WorkflowType } {
  let description = template.taskDescriptionTemplate;
  for (const [key, value] of Object.entries(substitutions)) {
    // Neutralise FINDING markers so an attacker cannot inject fake findings
    // into agent prompts by crafting a substitution value.
    description = description.replaceAll(`{{${key}}}`, sanitizePromptContent(value));
  }
  return { description, workflowType: template.workflowType };
}

/**
 * Strip structural markers from user-supplied strings before they are
 * interpolated into agent prompts.  The agent output parser splits on
 * "FINDING:" / "END_FINDING" — injecting those keywords would let a
 * crafted task description or memory entry manufacture fake findings.
 */
export function sanitizePromptContent(s: string): string {
  return s
    .replace(/\bFINDING:/gi, "[FINDING:]")
    .replace(/\bEND_FINDING\b/gi, "[END_FINDING]")
    .replace(/\bNO_FINDINGS\b/gi, "[NO_FINDINGS]")
    .replace(/\bNO_CHALLENGE\b/gi, "[NO_CHALLENGE]")
    .replace(/\bCHALLENGE:/gi, "[CHALLENGE:]")
    .replace(/\bEND_CHALLENGE\b/gi, "[END_CHALLENGE]")
    .replace(/\bRESOLUTION:/gi, "[RESOLUTION:]")
    .replace(/\bDESCRIPTION:/gi, "[DESCRIPTION:]")
    .replace(/\bEXPECTED_OUTPUT_\d+:/gi, "[EXPECTED_OUTPUT:]");
}

// ─── Generic external agent format ───────────────────────────────────────────

/**
 * Minimal format for importing agents from any external system as JSON.
 * Drop a JSON file in /agents/external/ and it will be imported on startup.
 */
export interface ExternalAgentConfig {
  id: string;
  name: string;
  tier: 0 | 1 | 2 | 3;
  domain: AgentDomain;
  description: string;
  systemPrompt: string;
  allowedTools?: string[];
  skills?: string[];
  source?: string;
  /**
   * Jurisdictions this agent is optimised for (e.g. ["US"], ["EU", "UK"]).
   * Undefined / empty = jurisdiction-neutral.
   */
  jurisdictions?: string[];
}

export function fromExternalConfig(c: ExternalAgentConfig): AgentDefinition {
  if (![0, 1, 2, 3].includes(c.tier as number)) {
    throw new Error(`Invalid tier ${JSON.stringify(c.tier)} for external agent '${c.id}' — must be 0, 1, 2, or 3`);
  }
  return {
    id: c.id,
    name: c.name,
    tier: c.tier,
    type: c.tier === 0 ? "root" : c.tier === 1 ? "manager" : c.tier === 3 ? "tool" : "specialist",
    domain: c.domain,
    description: c.description,
    systemPrompt: c.systemPrompt,
    allowedTools: c.allowedTools ?? [],
    skills: c.skills ?? [],
    jurisdictions: c.jurisdictions?.length ? c.jurisdictions : undefined,
    metadata: { source: c.source ?? "external" },
  };
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function slugify(name: string): string {
  return name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

function extractSkills(c: LavernAgentConfig): string[] {
  const skills: string[] = [];
  if (c.specialty) skills.push(slugify(c.specialty));
  if (c.jurisdiction) skills.push(`jurisdiction:${c.jurisdiction}`);
  if (c.workflow) skills.push(`workflow:${c.workflow}`);
  return skills;
}