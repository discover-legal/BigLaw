// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal
// This program is free software: you can redistribute it and/or modify it
// under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. See <https://www.gnu.org/licenses/>.

import type { AgentDefinition } from "../types.js";

/**
 * Returns true when an agent is eligible for a task's jurisdiction.
 *
 * Rules:
 *   - Agent has no jurisdictions (neutral) → always eligible.
 *   - Task has no jurisdiction → all agents eligible.
 *   - Otherwise: at least one of the agent's jurisdictions must be a
 *     case-insensitive prefix of the task jurisdiction (so agent "US"
 *     matches task "US-NY" and "US-CA"; agent "EU" does not match "US").
 */
export function jurisdictionMatch(agent: AgentDefinition, taskJurisdiction?: string): boolean {
  if (!agent.jurisdictions?.length) return true;
  if (!taskJurisdiction) return true;
  const tj = taskJurisdiction.toUpperCase();
  return agent.jurisdictions.some((j) => {
    const aj = j.toUpperCase();
    return tj === aj || tj.startsWith(aj + "-");
  });
}
