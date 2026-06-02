// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Generic legal tool adapter — the universal shape for plugging any external
 * legal agentic tool into Big Michael.
 *
 * Two integration paths:
 *
 * 1. JSON plugin (zero code required):
 *    Drop a *.json file in adapters/external/. Big Michael will:
 *      - Register your agent and workflow definitions
 *      - Create MCP-passthrough executors for any declared tools
 *      - Wire credentials from env vars (never from the plugin file itself)
 *      - Log every tool call and response through the audit system
 *
 *    Minimal JSON shape:
 *    {
 *      "id": "my-legal-tool",
 *      "name": "My Legal Tool",
 *      "version": "1.0.0",
 *      "description": "...",
 *      "auth": {
 *        "type": "api-key",
 *        "apiKeyEnvVar": "MY_TOOL_API_KEY",
 *        "endpointEnvVar": "MY_TOOL_MCP_URL"
 *      },
 *      "tools": [
 *        {
 *          "name": "my_tool_search",
 *          "description": "Search legal documents",
 *          "inputSchema": { "type": "object", "properties": { "query": { "type": "string" } }, "required": ["query"] },
 *          "requiresAuth": true
 *        }
 *      ],
 *      "agents": [
 *        {
 *          "id": "my-tool-specialist",
 *          "name": "My Tool Specialist",
 *          "tier": 2,
 *          "domain": "research",
 *          "description": "...",
 *          "systemPrompt": "...",
 *          "allowedTools": ["my_tool_search"]
 *        }
 *      ],
 *      "workflows": [
 *        {
 *          "id": "my-workflow",
 *          "name": "My Workflow",
 *          "description": "...",
 *          "workflowType": "roundtable",
 *          "promptTemplate": "Analyse {{description}} using this tool."
 *        }
 *      ]
 *    }
 *
 * 2. TypeScript plugin (full power):
 *    Implement LegalToolAdapter and call pluginRegistry.register(adapter).
 *    You control the tool executors and can call any external API.
 *
 * Security:
 *   - Endpoint URLs always come from env vars, never from plugin JSON directly.
 *   - All endpoints are SSRF-validated at registration time.
 *   - Agent tool allowlists are enforced against the registered tool set.
 *   - Plugin IDs and tool names are slug-validated before registration.
 */

import { readdir, readFile } from "fs/promises";
import { join, extname, resolve, sep } from "path";
import type { AgentDefinition, AgentDomain, WorkflowType } from "../types.js";
import type { TaskTemplate } from "./lavern.js";
import type { ExternalAgentConfig } from "./lavern.js";
import { fromExternalConfig } from "./lavern.js";
import { assertPublicHttpUrl } from "../settings/index.js";
import { mcpCall } from "../tools/connectors.js";
import { logger } from "../logger.js";

// ─── Public interfaces ────────────────────────────────────────────────────────

/** Auth declaration in a JSON plugin (credentials come from env vars, not the file). */
export interface PluginAuth {
  type: "api-key" | "bearer-token" | "oauth2" | "none";
  /** Name of the env var holding the API key or bearer token. */
  apiKeyEnvVar?: string;
  /** Name of the env var holding the MCP server endpoint URL. */
  endpointEnvVar?: string;
}

/**
 * A tool definition inside a JSON plugin.
 * Tools call an MCP server; the endpoint and credentials come from env vars declared in auth.
 */
export interface PluginToolDef {
  /** Internal tool name — must be unique across the registry, snake_case recommended. */
  name: string;
  description: string;
  /** JSON Schema object for the tool's input. */
  inputSchema: {
    type: "object";
    properties: Record<string, { type: string; description?: string; enum?: string[] }>;
    required?: string[];
  };
  /** Tool name on the remote MCP server (defaults to this tool's name). */
  remoteName?: string;
  /** Whether this tool needs the plugin's auth credentials. */
  requiresAuth?: boolean;
}

/** A workflow template definition inside a JSON plugin. */
export interface PluginWorkflowDef {
  id: string;
  name: string;
  description: string;
  workflowType: WorkflowType;
  promptTemplate: string;
  preferredDomains?: AgentDomain[];
  jurisdiction?: string;
  specialty?: string;
}

/**
 * The JSON shape that any external legal tool author drops in adapters/external/.
 * All fields are serialisable — no functions, no code.
 */
export interface LegalToolPlugin {
  id: string;
  name: string;
  version: string;
  description: string;

  /** MCP server auth/endpoint config. Credentials always come from env vars. */
  auth?: PluginAuth;

  /** Tool stubs — each becomes a ToolImpl with an MCP-passthrough executor. */
  tools?: PluginToolDef[];

  /** Agents this plugin contributes. Must only reference tools in this plugin or native tools. */
  agents?: ExternalAgentConfig[];

  /** Workflow templates this plugin contributes. */
  workflows?: PluginWorkflowDef[];
}

/**
 * TypeScript adapter interface — for richer integrations where you write code.
 * Register via pluginRegistry.register(adapter).
 */
export interface LegalToolAdapter {
  readonly id: string;
  readonly name: string;
  readonly version: string;
  readonly description: string;

  /** Returns ToolImpl objects (import from tools/index.ts). */
  tools?(): ResolvedTool[];

  /** Returns agent definitions this adapter contributes. */
  agents?(): AgentDefinition[];

  /** Returns workflow templates this adapter contributes. */
  workflows?(): TaskTemplate[];

  /** Called once at startup. Throw to abort registration (e.g. missing auth). */
  initialize?(): Promise<void>;

  /** Health probe. Resolve to { ok: false, reason } if unhealthy. */
  health?(): Promise<{ ok: boolean; latencyMs?: number; reason?: string }>;
}

// ─── Internal resolution types ────────────────────────────────────────────────

/** Minimal ToolImpl shape (mirrors tools/index.ts without the circular import). */
export interface ResolvedTool {
  name: string;
  schema: {
    name: string;
    description: string;
    input_schema: Record<string, unknown>;
  };
  execute(input: Record<string, unknown>, ctx: unknown): Promise<unknown>;
}

interface ResolvedPlugin {
  id: string;
  name: string;
  source: "json" | "adapter";
  tools: ResolvedTool[];
  agents: AgentDefinition[];
  workflows: TaskTemplate[];
}

// ─── Validation ───────────────────────────────────────────────────────────────

const VALID_WORKFLOW_TYPES = new Set<string>([
  "counsel", "roundtable", "adversarial", "review", "tabulate",
  "full_bench", "legal_design", "pre_engagement",
]);

const VALID_DOMAINS = new Set<string>([
  "research", "analysis", "drafting", "review", "compliance",
  "investigation", "tool", "orchestration",
]);

const SLUG_RE = /^[a-z0-9][a-z0-9_:-]*[a-z0-9]$|^[a-z0-9]$/;

function validatePlugin(raw: unknown): LegalToolPlugin {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error("Plugin must be a JSON object");
  }
  const p = raw as Record<string, unknown>;

  if (!p.id || typeof p.id !== "string" || !SLUG_RE.test(p.id)) {
    throw new Error(`Plugin id '${p.id}' must be a non-empty slug (a-z, 0-9, _, :, -)`)
  }
  if (!p.name || typeof p.name !== "string") throw new Error(`Plugin '${p.id}' missing name`);
  if (!p.version || typeof p.version !== "string") throw new Error(`Plugin '${p.id}' missing version`);
  if (!p.description || typeof p.description !== "string") throw new Error(`Plugin '${p.id}' missing description`);
  if (p.description.length > 500) throw new Error(`Plugin '${p.id}' description exceeds 500 chars`);

  if (p.auth !== undefined) {
    const auth = p.auth as Record<string, unknown>;
    if (!["api-key", "bearer-token", "oauth2", "none"].includes(auth.type as string)) {
      throw new Error(`Plugin '${p.id}' auth.type must be api-key | bearer-token | oauth2 | none`);
    }
    if (auth.apiKeyEnvVar !== undefined && typeof auth.apiKeyEnvVar !== "string") {
      throw new Error(`Plugin '${p.id}' auth.apiKeyEnvVar must be a string`);
    }
    if (auth.endpointEnvVar !== undefined && typeof auth.endpointEnvVar !== "string") {
      throw new Error(`Plugin '${p.id}' auth.endpointEnvVar must be a string`);
    }
  }

  if (Array.isArray(p.tools)) {
    for (const t of p.tools as unknown[]) {
      validateToolDef(p.id as string, t);
    }
  }

  if (Array.isArray(p.agents)) {
    for (const a of p.agents as unknown[]) {
      validateAgentConfig(p.id as string, a);
    }
  }

  if (Array.isArray(p.workflows)) {
    for (const w of p.workflows as unknown[]) {
      validateWorkflowDef(p.id as string, w);
    }
  }

  return p as unknown as LegalToolPlugin;
}

function validateToolDef(pluginId: string, raw: unknown): void {
  if (!raw || typeof raw !== "object") throw new Error(`Plugin '${pluginId}': tool must be an object`);
  const t = raw as Record<string, unknown>;
  if (!t.name || typeof t.name !== "string" || !SLUG_RE.test(t.name as string)) {
    throw new Error(`Plugin '${pluginId}': tool name '${t.name}' must be a slug`);
  }
  if (!t.description || typeof t.description !== "string") {
    throw new Error(`Plugin '${pluginId}': tool '${t.name}' missing description`);
  }
  if (!t.inputSchema || typeof t.inputSchema !== "object") {
    throw new Error(`Plugin '${pluginId}': tool '${t.name}' missing inputSchema`);
  }
}

function validateAgentConfig(pluginId: string, raw: unknown): void {
  if (!raw || typeof raw !== "object") throw new Error(`Plugin '${pluginId}': agent must be an object`);
  const a = raw as Record<string, unknown>;
  if (!a.id || typeof a.id !== "string") throw new Error(`Plugin '${pluginId}': agent missing id`);
  if (!a.name || typeof a.name !== "string") throw new Error(`Plugin '${pluginId}': agent '${a.id}' missing name`);
  if (![0, 1, 2, 3].includes(a.tier as number)) {
    throw new Error(`Plugin '${pluginId}': agent '${a.id}' tier must be 0–3`);
  }
  if (!VALID_DOMAINS.has(a.domain as string)) {
    throw new Error(`Plugin '${pluginId}': agent '${a.id}' domain '${a.domain}' is not valid`);
  }
  if (!a.systemPrompt || typeof a.systemPrompt !== "string") {
    throw new Error(`Plugin '${pluginId}': agent '${a.id}' missing systemPrompt`);
  }
  if ((a.systemPrompt as string).length > 8000) {
    throw new Error(`Plugin '${pluginId}': agent '${a.id}' systemPrompt exceeds 8000 chars`);
  }
}

function validateWorkflowDef(pluginId: string, raw: unknown): void {
  if (!raw || typeof raw !== "object") throw new Error(`Plugin '${pluginId}': workflow must be an object`);
  const w = raw as Record<string, unknown>;
  if (!w.id || typeof w.id !== "string") throw new Error(`Plugin '${pluginId}': workflow missing id`);
  if (!w.name || typeof w.name !== "string") throw new Error(`Plugin '${pluginId}': workflow '${w.id}' missing name`);
  if (!VALID_WORKFLOW_TYPES.has(w.workflowType as string)) {
    throw new Error(`Plugin '${pluginId}': workflow '${w.id}' workflowType '${w.workflowType}' is not valid`);
  }
  if (!w.promptTemplate || typeof w.promptTemplate !== "string") {
    throw new Error(`Plugin '${pluginId}': workflow '${w.id}' missing promptTemplate`);
  }
  if ((w.promptTemplate as string).length > 10000) {
    throw new Error(`Plugin '${pluginId}': workflow '${w.id}' promptTemplate exceeds 10000 chars`);
  }
}

// ─── Plugin → ResolvedPlugin conversion ──────────────────────────────────────

function resolvePlugin(plugin: LegalToolPlugin): ResolvedPlugin {
  // Resolve auth credentials from env vars
  const apiKey = plugin.auth?.apiKeyEnvVar ? (process.env[plugin.auth.apiKeyEnvVar] ?? "") : "";
  let endpoint = "";
  if (plugin.auth?.endpointEnvVar) {
    endpoint = process.env[plugin.auth.endpointEnvVar] ?? "";
    if (endpoint) {
      // SSRF-validate at registration time; log and skip rather than crash the whole server
      try {
        assertPublicHttpUrl(endpoint, plugin.auth.endpointEnvVar);
      } catch (err) {
        logger.warn(`Plugin '${plugin.id}': skipping SSRF-invalid endpoint`, {
          envVar: plugin.auth.endpointEnvVar,
          error: (err as Error).message,
        });
        endpoint = "";
      }
    }
  }

  // Build MCP-passthrough ToolImpl objects
  const tools: ResolvedTool[] = (plugin.tools ?? []).map((def) => {
    const toolEndpoint = endpoint;
    const toolApiKey = def.requiresAuth ? apiKey : "";
    const remoteName = def.remoteName ?? def.name;

    return {
      name: def.name,
      schema: {
        name: def.name,
        description: `[${plugin.name}] ${def.description}`,
        input_schema: def.inputSchema,
      },
      async execute(input: Record<string, unknown>): Promise<unknown> {
        if (!toolEndpoint) {
          return { error: `Plugin '${plugin.id}' tool '${def.name}' not configured: ${plugin.auth?.endpointEnvVar ?? "no endpoint"} is not set` };
        }
        return mcpCall(toolEndpoint, toolApiKey, remoteName, input);
      },
    };
  });

  // Convert agent configs
  const agents: AgentDefinition[] = (plugin.agents ?? []).map((cfg) =>
    fromExternalConfig({ ...cfg, source: `plugin:${plugin.id}` }),
  );

  // Convert workflow defs to TaskTemplates
  const workflows: TaskTemplate[] = (plugin.workflows ?? []).map((w) => ({
    id: `plugin:${plugin.id}:${w.id}`,
    name: `[${plugin.name}] ${w.name}`,
    description: [w.description, w.specialty, w.jurisdiction].filter(Boolean).join(" — "),
    taskDescriptionTemplate: w.promptTemplate,
    workflowType: w.workflowType,
    preferredDomains: w.preferredDomains,
    source: "custom" as const,
    metadata: {
      pluginId: plugin.id,
      jurisdiction: w.jurisdiction,
      specialty: w.specialty,
    },
  }));

  return { id: plugin.id, name: plugin.name, source: "json", tools, agents, workflows };
}

// ─── PluginRegistry ───────────────────────────────────────────────────────────

/**
 * Registry that aggregates all external legal tool plugins and adapters.
 * Big Michael discovers plugins at startup; callers can also register
 * TypeScript adapters programmatically.
 */
export class PluginRegistry {
  private readonly plugins = new Map<string, ResolvedPlugin>();

  /**
   * Load all *.json plugin files from dirPath.
   * Invalid files are logged and skipped; they do not abort startup.
   * dirPath must be within the project root.
   */
  async loadDirectory(dirPath: string): Promise<void> {
    const cwd = process.cwd();
    const resolved = resolve(dirPath);
    if (!resolved.startsWith(cwd + sep) && resolved !== cwd) {
      throw new Error(`Plugin directory '${dirPath}' must be within the project root`);
    }

    let entries: string[];
    try {
      entries = await readdir(resolved);
    } catch {
      return;  // directory absent — not an error
    }

    for (const entry of entries) {
      if (extname(entry) !== ".json") continue;
      const filePath = join(resolved, entry);
      try {
        const raw = await readFile(filePath, "utf8");
        const parsed = JSON.parse(raw);
        const plugins = Array.isArray(parsed) ? parsed : [parsed];
        for (const p of plugins) {
          const validated = validatePlugin(p);
          const rp = resolvePlugin(validated);
          if (this.plugins.has(rp.id)) {
            logger.warn(`Plugin id '${rp.id}' already registered — skipping duplicate from ${entry}`);
            continue;
          }
          this.plugins.set(rp.id, rp);
          logger.info(`Loaded plugin '${rp.name}' from ${entry}`, {
            tools: rp.tools.length,
            agents: rp.agents.length,
            workflows: rp.workflows.length,
          });
        }
      } catch (err) {
        logger.warn(`Skipping invalid plugin file '${entry}'`, { error: (err as Error).message });
      }
    }
  }

  /**
   * Register a TypeScript adapter programmatically.
   * Call initialize() before registering if the adapter requires async setup.
   */
  register(adapter: LegalToolAdapter): void {
    if (this.plugins.has(adapter.id)) {
      logger.warn(`Plugin id '${adapter.id}' already registered — skipping duplicate`);
      return;
    }
    this.plugins.set(adapter.id, {
      id: adapter.id,
      name: adapter.name,
      source: "adapter",
      tools: adapter.tools?.() ?? [],
      agents: adapter.agents?.() ?? [],
      workflows: adapter.workflows?.() ?? [],
    });
    logger.info(`Registered adapter '${adapter.name}'`);
  }

  /** Returns all ToolImpl objects from all registered plugins/adapters. */
  allTools(): ResolvedTool[] {
    return Array.from(this.plugins.values()).flatMap((p) => p.tools);
  }

  /** Returns all AgentDefinitions from all registered plugins/adapters. */
  allAgents(): AgentDefinition[] {
    return Array.from(this.plugins.values()).flatMap((p) => p.agents);
  }

  /** Returns all TaskTemplates from all registered plugins/adapters. */
  allWorkflows(): TaskTemplate[] {
    return Array.from(this.plugins.values()).flatMap((p) => p.workflows);
  }

  /** Returns a summary of all registered plugins. */
  list(): Array<{ id: string; name: string; source: string; tools: number; agents: number; workflows: number }> {
    return Array.from(this.plugins.values()).map((p) => ({
      id: p.id,
      name: p.name,
      source: p.source,
      tools: p.tools.length,
      agents: p.agents.length,
      workflows: p.workflows.length,
    }));
  }

  get size(): number {
    return this.plugins.size;
  }
}

/** Singleton plugin registry — populated at startup via loadDirectory() + register(). */
export const pluginRegistry = new PluginRegistry();
