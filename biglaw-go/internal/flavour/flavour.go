// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package flavour trims the platform to a practice-area preset. A flavour is a
// JSON file (flavours/<id>.json) declaring which Tier-2 specialists and task
// templates a deployment seats — a family-law solo doesn't need the contract
// playbook stack, and every unseated agent saves a Need/Offer descriptor call
// per DyTopo round. A flavour is a view over the full bench, not a fork: the
// default (no FLAVOUR set) is the complete platform, and switching back is a
// restart away.
//
// Filtering is deliberately conservative: the T0/T1 orchestration spine and
// T3 tool agents are shared infrastructure and always seated (excludeAgents
// can drop a T3, never T0/T1). Only Tier-2 agents are subject to the skill
// filter. This is the flavour analogue of the jurisdiction filter
// (dytopo.JurisdictionMatch) and runs once at startup, upstream of the
// per-task recruitment (semantic search + jurisdiction + Q-rerank).
package flavour

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// Flavour is one practice-area preset.
type Flavour struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Agents      AgentFilter `json:"agents"`
	// Templates lists the task-template IDs this flavour exposes. Empty means
	// all templates.
	Templates []string `json:"templates"`
}

// AgentFilter selects which agents a flavour seats.
type AgentFilter struct {
	// IncludeSkills: when non-empty, a Tier-2 agent must carry at least one
	// matching skill to be seated. A trailing '*' matches by prefix
	// ("contract-*"). Matching is case-insensitive.
	IncludeSkills []string `json:"includeSkills"`
	// IncludeAgents are agent IDs always seated, regardless of skills.
	IncludeAgents []string `json:"includeAgents"`
	// ExcludeAgents are agent IDs always dropped (wins over everything except
	// the T0/T1 spine, which cannot be excluded).
	ExcludeAgents []string `json:"excludeAgents"`
}

// Load resolves nameOrPath to a flavour file and parses it. An empty string or
// "full" returns (nil, nil) — the full bench, no filtering. A value containing
// a path separator or .json suffix is treated as a path; otherwise it is looked
// up as flavours/<name>.json under dir (typically the repo root / cwd).
func Load(dir, nameOrPath string) (*Flavour, error) {
	nameOrPath = strings.TrimSpace(nameOrPath)
	if nameOrPath == "" || strings.EqualFold(nameOrPath, "full") {
		return nil, nil
	}
	path := nameOrPath
	if !strings.ContainsRune(nameOrPath, os.PathSeparator) && !strings.HasSuffix(nameOrPath, ".json") {
		path = filepath.Join(dir, "flavours", nameOrPath+".json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("flavour %q: %w", nameOrPath, err)
	}
	var f Flavour
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("flavour %q: parse: %w", nameOrPath, err)
	}
	if f.ID == "" {
		return nil, fmt.Errorf("flavour %q: missing id", nameOrPath)
	}
	return &f, nil
}

// FilterAgents returns the subset of defs this flavour seats. Rules, in order:
//
//   - Tier 0/1 (orchestrator + domain managers): always seated.
//   - ExcludeAgents: dropped (Tier >= 2 only).
//   - IncludeAgents: seated.
//   - Tier 3 tool agents: seated (generic infrastructure).
//   - Tier 2 with IncludeSkills non-empty: seated iff any skill matches.
//   - Otherwise: seated.
func (f *Flavour) FilterAgents(defs []types.AgentDefinition) []types.AgentDefinition {
	include := idSet(f.Agents.IncludeAgents)
	exclude := idSet(f.Agents.ExcludeAgents)
	out := make([]types.AgentDefinition, 0, len(defs))
	for _, d := range defs {
		if d.Tier <= 1 {
			out = append(out, d)
			continue
		}
		if exclude[strings.ToLower(d.ID)] {
			continue
		}
		if include[strings.ToLower(d.ID)] {
			out = append(out, d)
			continue
		}
		if d.Tier != 2 || len(f.Agents.IncludeSkills) == 0 {
			out = append(out, d)
			continue
		}
		if anySkillMatches(d.Skills, f.Agents.IncludeSkills) {
			out = append(out, d)
		}
	}
	return out
}

// FilterTemplates returns the subset of templates this flavour exposes.
func (f *Flavour) FilterTemplates(ts []types.TaskTemplate) []types.TaskTemplate {
	if len(f.Templates) == 0 {
		return ts
	}
	keep := idSet(f.Templates)
	out := make([]types.TaskTemplate, 0, len(ts))
	for _, t := range ts {
		if keep[strings.ToLower(t.ID)] {
			out = append(out, t)
		}
	}
	return out
}

func idSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[strings.ToLower(strings.TrimSpace(id))] = true
	}
	return m
}

func anySkillMatches(skills, patterns []string) bool {
	for _, s := range skills {
		ls := strings.ToLower(s)
		for _, p := range patterns {
			lp := strings.ToLower(strings.TrimSpace(p))
			if pre, ok := strings.CutSuffix(lp, "*"); ok {
				if strings.HasPrefix(ls, pre) {
					return true
				}
			} else if ls == lp {
				return true
			}
		}
	}
	return false
}
