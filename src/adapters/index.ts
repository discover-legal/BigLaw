// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

/**
 * Adapter / harness interface.
 *
 * Big Michael acts as a meta-orchestrator harness. External agent systems
 * (Lavern, Mike OSS, custom) expose their agents via adapters that convert
 * their native formats into AgentDefinition records.
 *
 * Once imported, external agents are seeded into the RuVector/Qdrant registry
 * and participate in DyTopo rounds like any native agent.
 *
 * Usage:
 *   const lavern = new LavernAdapter();
 *   const agents  = await lavern.load('./lavern/src/agents');
 *   await registry.registerAll(agents);
 */

import type { AgentDefinition } from "../types.js";

export interface AgentHarness {
  /** Unique identifier for this harness (e.g. "lavern", "mikeoss") */
  readonly name: string;
  readonly version: string;

  /**
   * Load agents from this harness and return them as AgentDefinitions.
   * Implementations decide how to read their native format (file path, URL, etc.)
   */
  load(source: string): Promise<AgentDefinition[]>;
}

/**
 * Merge agents from multiple sources into a single deduplicated list.
 * Later sources override earlier ones for the same agent ID.
 */
export function mergeAgents(...sources: AgentDefinition[][]): AgentDefinition[] {
  const map = new Map<string, AgentDefinition>();
  for (const batch of sources) {
    for (const agent of batch) {
      map.set(agent.id, agent);
    }
  }
  return Array.from(map.values());
}

// Re-export the generic plugin adapter types so consumers can import from one place.
export type {
  LegalToolPlugin,
  LegalToolAdapter,
  PluginAuth,
  PluginToolDef,
  PluginWorkflowDef,
  ResolvedTool,
} from "./plugin.js";
export { PluginRegistry, pluginRegistry } from "./plugin.js";