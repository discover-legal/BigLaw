// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package ooxml

import (
	"fmt"
	"slices"
	"sort"
	"unicode/utf8"
)

// revisionContext is how much untouched text is reported on each side of a change.
const revisionContext = 40

// fragment is one tracked-change element as scanned, placed in the visible text.
type fragment struct {
	kind         string // ins or del
	id           int
	author, date string
	text         string
	start, end   int // visible offsets: an insertion's span; a deletion's point (start == end)
	order        int // document order
}

// Revisions reports every tracked change in the body in document order. Deleted text adjacent to
// inserted text by the same author (nothing untouched between them, in either order) is one
// substitution; adjacent same-author fragments of one kind (a deletion Word split across runs, a
// deleted paragraph with its mark) are one change. Tracked moves and formatting changes are not
// reported. Offsets index Text(); a deletion's Start and End are its point.
func (d *Document) Revisions() ([]Revision, error) {
	src, _ := d.Part("word/document.xml")
	ix, err := scan(src)
	if err != nil {
		return nil, err
	}
	var frags []fragment
	seen := map[*revEl]int{}
	for i, r := range ix.runs {
		if r.rev == nil {
			continue
		}
		if j, ok := seen[r.rev]; ok { // the element's next run: extend it
			if r.rev.kind == "ins" {
				frags[j].text += r.text
				frags[j].end = r.docStart + len(r.text)
			} else {
				frags[j].text += r.delText
			}
			continue
		}
		f := fragment{kind: r.rev.kind, id: r.rev.id, author: r.rev.author, date: r.rev.date, start: r.docStart, end: r.docStart, order: i}
		if r.rev.kind == "ins" {
			f.text, f.end = r.text, r.docStart+len(r.text)
		} else {
			f.text = r.delText
		}
		seen[r.rev] = len(frags)
		frags = append(frags, f)
	}
	for pi, p := range ix.paras {
		if m := p.markRev; m != nil {
			f := fragment{kind: m.kind, id: m.id, author: m.author, date: m.date, text: "\n", start: p.textEnd, end: p.textEnd, order: len(ix.runs) + pi}
			if m.kind == "ins" {
				f.end++
			}
			frags = append(frags, f)
		}
	}
	sort.SliceStable(frags, func(a, b int) bool {
		if frags[a].start != frags[b].start {
			return frags[a].start < frags[b].start
		}
		return frags[a].order < frags[b].order
	})
	// Group adjacent same-author fragments: the group's inserted text spans from its first
	// insertion, its deletions sit at the group's end (a deletion's point is where the next
	// fragment may start).
	type group struct {
		rev   Revision
		marks int // paragraph-mark fragments in it
		n     int
	}
	var groups []*group
	for _, f := range frags {
		mark := f.order >= len(ix.runs)
		if n := len(groups); n > 0 && groups[n-1].rev.Author == f.author && groups[n-1].rev.End == f.start {
			g := groups[n-1]
			if f.kind == "ins" {
				g.rev.Inserted += f.text
				g.rev.End = f.end
			} else {
				g.rev.Deleted += f.text
			}
			g.rev.IDs = append(g.rev.IDs, f.id)
			g.n++
			if mark {
				g.marks++
			}
			continue
		}
		g := &group{rev: Revision{ID: f.id, Author: f.author, Date: f.date, Start: f.start, End: f.end, IDs: []int{f.id}}, n: 1}
		if f.kind == "ins" {
			g.rev.Inserted = f.text
		} else {
			g.rev.Deleted = f.text
		}
		if mark {
			g.marks = 1
		}
		groups = append(groups, g)
	}
	out := []Revision{}
	for _, g := range groups {
		r := g.rev
		switch {
		case r.Inserted != "" && r.Deleted != "":
			r.Kind = "substitution"
		case r.Inserted != "" && g.n == g.marks:
			r.Kind = "paragraph_insertion"
		case r.Deleted != "" && g.n == g.marks:
			r.Kind = "paragraph_deletion"
		case r.Inserted != "":
			r.Kind = "insertion"
		default:
			r.Kind = "deletion"
		}
		r.Before, r.After = contextAround(ix.text, r.Start, r.End)
		out = append(out, r)
	}
	return out, nil
}

// contextAround is up to revisionContext bytes of text on each side of [start, end), cut at rune
// boundaries.
func contextAround(s string, start, end int) (before, after string) {
	lo := max(start-revisionContext, 0)
	for lo > 0 && lo < len(s) && !utf8.RuneStart(s[lo]) {
		lo++
	}
	hi := min(end+revisionContext, len(s))
	for hi < len(s) && !utf8.RuneStart(s[hi]) {
		hi++
	}
	start, end = min(max(start, 0), len(s)), min(max(end, 0), len(s))
	return s[lo:start], s[end:hi]
}

// Decide is the visitor that accepts (accept true) or rejects the tracked changes whose ids are
// listed, every one when none is. An id is any of a change's IDs as Revisions reports them; an id
// that names no change is an error.
func (d *Document) Decide(accept bool, ids []int) (Visitor, error) {
	revs, err := d.Revisions()
	if err != nil {
		return nil, err
	}
	elements := map[int]bool{}
	for _, want := range ids {
		found := false
		for _, r := range revs {
			if slices.Contains(r.IDs, want) {
				for _, id := range r.IDs {
					elements[id] = true
				}
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("ooxml: no tracked change has id %d", want)
		}
	}
	all := len(ids) == 0
	run, mark := Accept, AcceptMark
	if !accept {
		run, mark = Reject, RejectMark
	}
	return func(r Run) ([]Op, error) {
		var ops []Op
		if r.Revision != nil && (all || elements[r.Revision.ID]) {
			ops = append(ops, Op{Kind: run})
		}
		if r.Mark != nil && (all || elements[r.Mark.ID]) {
			ops = append(ops, Op{Kind: mark})
		}
		return ops, nil
	}, nil
}
