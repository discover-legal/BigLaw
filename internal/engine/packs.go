// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"context"
	"embed"
	"slices"
	"strings"
)

//go:embed packs
var packFS embed.FS

// Pack is a jurisdiction or practice pack (jurisdiction and practice guidance; skills are skills.go's):
// short Markdown built into the binary, with a front matter of code, status (candidate until a named
// lawyer reviews it), reviewedBy and checklist. An admin may override one (specset.go).
type Pack struct {
	Code, Status, ReviewedBy, Body string
	Practice                       bool
	Checklist                      []string
}

// SelectPacks is the packs for a task: jurisdiction packs by prefix (US-NY loads us and us-ny),
// then the practice pack named.
func SelectPacks(jurisdiction, practice string) []Pack {
	return builtinSpecs.selectPacks(jurisdiction, practice)
}

// selectPacks is SelectPacks over the set's packs (a run's: packsFor).
func (s *Specs) selectPacks(jurisdiction, practice string) []Pack {
	j := strings.ToLower(jurisdiction)
	var out []Pack
	for _, p := range s.packs {
		if !p.Practice && (j == p.Code || strings.HasPrefix(j, p.Code+"-")) || p.Practice && p.Code == strings.ToLower(practice) {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b Pack) int { // jurisdiction before practice, broad before narrow
		return cmp.Or(cmp.Compare(btoi(a.Practice), btoi(b.Practice)), cmp.Compare(len(a.Code), len(b.Code)))
	})
	return out
}

// Checklist is the selected packs' checklists, in order.
func Checklist(jurisdiction, practice string) []string {
	return checklistOf(SelectPacks(jurisdiction, practice))
}

// packsFor is the packs of ctx's spec set (a run's snapshot) for a task.
func packsFor(ctx context.Context, jurisdiction, practice string) []Pack {
	return specsOf(ctx).selectPacks(jurisdiction, practice)
}

func checklistOf(ps []Pack) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Checklist...)
	}
	return out
}

// candidates is the Limitations line for packs no lawyer has reviewed.
func candidates(ps []Pack) string {
	var names []string
	for _, p := range ps {
		if p.Status != "reviewed" {
			names = append(names, p.Code)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "This deliverable used candidate practice or jurisdiction packs (" + strings.Join(names, ", ") + ") that no named lawyer has reviewed yet."
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
