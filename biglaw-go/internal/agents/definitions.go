// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agents

import "github.com/discover-legal/biglaw-go/internal/types"

// ALL_AGENT_DEFINITIONS is the complete flat list of all 131+ agent definitions,
// assembled from the tier-specific sub-files at package init time.
var ALL_AGENT_DEFINITIONS []types.AgentDefinition

func init() {
	ALL_AGENT_DEFINITIONS = make([]types.AgentDefinition, 0, 140)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, ROOT_ORCHESTRATOR)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier1Managers...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2Epistemic...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2Conceptual...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2Writing...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier3ToolAgents...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2CommercialSpecialist...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2CorporateSpecialist...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2EmploymentSpecialist...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2PrivacySpecialist...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2ProductLegal...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2RegulatorySpecialist...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2AIGovernance...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2IPSpecialist...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2LitigationOps...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2LawStudent...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, tier2Clinic...)
	ALL_AGENT_DEFINITIONS = append(ALL_AGENT_DEFINITIONS, goliathKillerAgents...)
}
