// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// Skills (README § Skills): named bundles of instructions an admin writes and a lawyer reviews,
// built in under skills/<name>/SKILL.md (front matter: name, description, status, reviewedBy,
// tools) with optional reference files beside it, or added in Manage. Progressive disclosure: the
// lead's brief lists each skill's name and description only (skillIndex, bounded by maxSkills and
// maxSkillIndex); use_skill returns a body or a reference file, a page at a time. The lead is its
// only built-in caller (MemoVisible); an admin may name it in a subagent's allowed tools. A skill
// grants no tool: its tools list only points at tools its caller may already call. A deliverable
// that used a candidate skill says so in Limitations (skillCandidates).

// kSkill is the ledger's record of the skills a run read (key: name), committed with the step.
const kSkill = "skill"

// SkillUse is one skill a run read: its status then, and the override version it ran (0: built in).
type SkillUse struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version int    `json:"version"`
}

// skillTool is use_skill over the run's spec set.
func (e *Engine) skillTool(sp *Specs) agent.Tool {
	return agent.GoTool{S: sp.Spec("use_skill"), Fn: func(ctx context.Context, c agent.Call) (any, agent.Usage, error) {
		var in struct {
			Name   string
			File   *string
			Offset *int
		}
		if err := agent.Decode(c, &in); err != nil {
			return nil, agent.Usage{}, err
		}
		k, ok := sp.skill(in.Name)
		if !ok {
			var names []string
			for _, x := range sp.skills {
				names = append(names, x.Name)
			}
			return nil, agent.Usage{}, agent.Fail(fmt.Sprintf("use_skill: no skill is named %q; the skills are: %s", in.Name, strings.Join(names, ", ")))
		}
		text := k.Body
		if in.File != nil {
			if !slices.Contains(k.Files, *in.File) {
				return nil, agent.Usage{}, agent.Fail(fmt.Sprintf("use_skill: skill %s has no file %q; its files are: %s", k.Name, *in.File, strings.Join(k.Files, ", ")))
			}
			text = string(sp.files[skillsDir+k.Name+"/"+*in.File])
		}
		off := 0
		if in.Offset != nil {
			off = *in.Offset
		}
		if off > len(text) || off < len(text) && !utf8.RuneStart(text[off]) {
			return nil, agent.Usage{}, agent.Fail(fmt.Sprintf("use_skill: offset %d is not a page start (the answer's next)", off))
		}
		pg := agent.NewPage()
		page := agent.TakeText(pg, text[off:])
		var next *int
		if pg.Truncated {
			n := off + len(page)
			next = &n
		}
		if err := e.write(ctx, c.TaskID, kSkill, k.Name, SkillUse{k.Name, k.Status, sp.versions["skill."+k.Name]}, true); err != nil {
			return nil, agent.Usage{}, err
		}
		return map[string]any{"name": k.Name, "description": k.Description, "status": k.Status, "tools": append([]string{}, k.Tools...),
			"files": append([]string{}, k.Files...), "file": in.File, "text": page, "truncated": pg.Truncated, "next": next}, agent.Usage{}, nil
	}}
}

// skillCandidates is the Limitations line for skills the run read that no named lawyer has reviewed.
func skillCandidates(used []SkillUse) string {
	var names []string
	for _, u := range used {
		if u.Status != "reviewed" {
			names = append(names, u.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "This deliverable used candidate skills (" + strings.Join(names, ", ") + ") that no named lawyer has reviewed yet."
}
