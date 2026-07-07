// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package dytopo

import (
	"strings"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// JurisdictionMatch returns true when an agent is eligible for the given task jurisdiction.
//
// Rules:
//   - Agent has no jurisdictions (neutral) → always eligible.
//   - Task has no jurisdiction → all agents eligible.
//   - Otherwise: at least one of the agent's jurisdictions must be a
//     case-insensitive prefix of the task jurisdiction (so agent "US"
//     matches task "US-NY" and "US-CA"; agent "EU" does not match "US").
func JurisdictionMatch(agent types.AgentDefinition, taskJurisdiction string) bool {
	if len(agent.Jurisdictions) == 0 {
		return true
	}
	if taskJurisdiction == "" {
		return true
	}
	tj := strings.ToUpper(taskJurisdiction)
	for _, j := range agent.Jurisdictions {
		aj := strings.ToUpper(j)
		if tj == aj || strings.HasPrefix(tj, aj+"-") {
			return true
		}
	}
	return false
}
