// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"cmp"
	"embed"
	"io/fs"
	"path"
	"slices"
	"strings"
)

//go:embed packs
var packFS embed.FS

// Pack is a jurisdiction or practice pack: short Markdown built into the binary, with a front
// matter of code, status (candidate until a named lawyer reviews it), reviewedBy and checklist.
type Pack struct {
	Code, Status, ReviewedBy, Body string
	Practice                       bool
	Checklist                      []string
}

var packs = func() (out []Pack) {
	files, _ := fs.Glob(packFS, "packs/*/*.md")
	for _, f := range files {
		data, _ := packFS.ReadFile(f)
		_, rest, _ := strings.Cut(string(data), "---\n")
		head, body, _ := strings.Cut(rest, "\n---\n")
		p := Pack{Body: strings.TrimSpace(body), Practice: path.Base(path.Dir(f)) == "practice"}
		for _, ln := range strings.Split(head, "\n") {
			k, v, _ := strings.Cut(ln, ":")
			switch k = strings.TrimSpace(k); {
			case strings.HasPrefix(ln, "- "):
				p.Checklist = append(p.Checklist, strings.TrimPrefix(ln, "- "))
			case k == "code":
				p.Code = strings.TrimSpace(v)
			case k == "status":
				p.Status = strings.TrimSpace(v)
			case k == "reviewedBy":
				p.ReviewedBy = strings.TrimSpace(v)
			}
		}
		out = append(out, p)
	}
	return out
}()

// SelectPacks is the packs for a task: jurisdiction packs by prefix (US-NY loads us and us-ny),
// then the practice pack named.
func SelectPacks(jurisdiction, practice string) []Pack {
	j := strings.ToLower(jurisdiction)
	var out []Pack
	for _, p := range packs {
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
	var out []string
	for _, p := range SelectPacks(jurisdiction, practice) {
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
