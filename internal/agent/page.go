// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package agent

import (
	"encoding/json"
	"unicode/utf8"
)

// Page fills one tool output's lists from a byte budget (owner decision, lab-r3 r4: size is part of a
// tool's typed output, never trimmed after the fact). Each list keeps, in order, the items whose JSON
// fits in what is left; the first item a page takes is always kept, so a paged list always moves on
// (an item's own size is bounded by its type). Truncated reports a list that was shortened; the tool's
// output says so and how to get the rest (its next, or a narrower call).
type Page struct {
	Left      int
	Truncated bool
	taken     bool
}

// NewPage is a page of PageBytes, less what the output's other fields (scalars, names, framing) take.
func NewPage() *Page { return &Page{Left: PageBytes - pageFrame} }

const pageFrame = 1_000

// Take is the leading items of items that fit in p.
func Take[T any](p *Page, items []T) []T {
	for i, it := range items {
		b, _ := json.Marshal(it)
		if n := len(b) + 1; n <= p.Left || !p.taken {
			p.Left, p.taken = p.Left-n, true
			continue
		}
		p.Truncated = true
		return items[:i]
	}
	return items
}

// TakeText is the leading runes of s whose JSON encoding fits in p (all of s when it fits), cut at a
// line break when there is one in the page's second half: text is paged by the caller's offset, so the
// rest is the next page, never dropped.
func TakeText(p *Page, s string) string {
	if b, _ := json.Marshal(s); len(b) <= p.Left || p.Left <= 2 {
		if len(b) > p.Left {
			p.Truncated, s = true, ""
		}
		p.Left -= len(b)
		return s
	}
	n, size := 0, 2 // the quotes
	for n < len(s) {
		r, w := utf8.DecodeRuneInString(s[n:])
		b, _ := json.Marshal(string(r))
		if size+len(b)-2 > p.Left {
			break
		}
		size, n = size+len(b)-2, n+w
	}
	for i := n - 1; i > n/2; i-- {
		if s[i] == '\n' {
			n = i + 1
			break
		}
	}
	p.Left, p.Truncated = p.Left-size, true
	return s[:n]
}
