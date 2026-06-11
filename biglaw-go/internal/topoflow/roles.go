// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package topoflow

import "strings"

// Role is an agent role with a skill-card prompt and required output fields [DT].
type Role struct {
	Name           string
	SkillCard      string
	RequiredFields []string
}

var requiredAgentFields = []string{"public", "private", "q_desc", "k_desc"}

// CodeRoles — {Developer, Researcher, Tester, Designer}.
var CodeRoles = []Role{
	{"Developer", "You implement the solution code. Output working code in draft_answer.", requiredAgentFields},
	{"Researcher", "You gather relevant facts, APIs, and edge cases for the problem.", requiredAgentFields},
	{"Tester", "You design and run tests; report pass/fail in verification.", requiredAgentFields},
	{"Designer", "You shape the interface and decomposition of the solution.", requiredAgentFields},
}

// MathRoles — {ProblemParser, Solver, Verifier}.
var MathRoles = []Role{
	{"ProblemParser", "You restate the problem precisely and identify givens/goal.", requiredAgentFields},
	{"Solver", "You derive the solution step by step; put the final answer in draft_answer.", requiredAgentFields},
	{"Verifier", "You check the derivation; report verification as supported/refuted.", requiredAgentFields},
}

// RoleSetFor returns the role set for a domain (truncated to the agent count).
func RoleSetFor(domain string, cfg Config) []Role {
	switch strings.ToLower(domain) {
	case "math":
		return MathRoles[:min(cfg.NAgentsMath, len(MathRoles))]
	default:
		return CodeRoles[:min(cfg.NAgentsCode, len(CodeRoles))]
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
