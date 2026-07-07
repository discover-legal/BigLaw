// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package textdiff is an in-house word-level diff. Both inputs are tokenised
// into whitespace-delimited words (so whitespace runs, line wrapping, and
// paragraph reflow never register as changes) and aligned with a longest-
// common-subsequence pass; the result is a sequence of hunks — equal, insert,
// delete — each carrying the affected text, approximate character offsets
// into the original inputs, and ~40 characters of surrounding context.
//
// The API is deliberately generic: the integrity checker diffs a sent draft
// against a received document's baseline, and the planned "Redtime" feature
// will diff successive document versions with the same call.
package textdiff

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind classifies a hunk.
type Kind string

const (
	Equal  Kind = "equal"
	Insert Kind = "insert"
	Delete Kind = "delete"
)

// contextChars is how much original text is captured on each side of a hunk.
const contextChars = 40

// maxDPCells bounds the LCS dynamic-programming table (~16 MB of int32 at the
// cap). Inputs are trimmed to their differing middle first, so ordinary
// document revisions — long texts, localised edits — stay far below it. A
// pathological pair whose middle still exceeds the cap degrades gracefully to
// one coarse delete+insert covering the whole middle.
const maxDPCells = 4 << 20

// Hunk is one aligned span of the two inputs. OurText is the span in the
// first input ("ours" — e.g. the draft we sent), TheirText the span in the
// second ("theirs" — e.g. the version received back). Equal hunks carry both;
// a delete carries only OurText; an insert only TheirText. Offsets are byte
// offsets into the ORIGINAL input strings (approximate for an insert's
// OurOffset / a delete's TheirOffset, which mark the anchor point in the
// other text). Context comes from whichever input the hunk has text in.
type Hunk struct {
	Kind          Kind   `json:"kind"`
	OurText       string `json:"ourText,omitempty"`
	TheirText     string `json:"theirText,omitempty"`
	OurOffset     int    `json:"ourOffset"`
	TheirOffset   int    `json:"theirOffset"`
	ContextBefore string `json:"contextBefore,omitempty"`
	ContextAfter  string `json:"contextAfter,omitempty"`
}

// Diff aligns a ("ours") and b ("theirs") word-by-word and returns the full
// hunk sequence, equal spans included, in document order. Identical inputs
// yield a single equal hunk (or none when both are empty). A substitution
// appears as a delete hunk immediately followed by an insert hunk.
func Diff(a, b string) []Hunk {
	ta, tb := tokenize(a), tokenize(b)

	// Trim the common prefix and suffix so the quadratic LCS pass only sees
	// the differing middle.
	p := 0
	for p < len(ta) && p < len(tb) && ta[p].text == tb[p].text {
		p++
	}
	s := 0
	for s < len(ta)-p && s < len(tb)-p && ta[len(ta)-1-s].text == tb[len(tb)-1-s].text {
		s++
	}

	ops := make([]Kind, 0, len(ta)+len(tb))
	for i := 0; i < p; i++ {
		ops = append(ops, Equal)
	}
	ops = append(ops, lcsOps(ta[p:len(ta)-s], tb[p:len(tb)-s])...)
	for i := 0; i < s; i++ {
		ops = append(ops, Equal)
	}

	return assemble(a, b, ta, tb, ops)
}

// Changes returns only the non-equal hunks of Diff — the change report.
// Identical inputs yield no hunks.
func Changes(a, b string) []Hunk {
	var out []Hunk
	for _, h := range Diff(a, b) {
		if h.Kind != Equal {
			out = append(out, h)
		}
	}
	return out
}

// ─── Tokenisation ─────────────────────────────────────────────────────────────

// token is one whitespace-delimited word with its byte span in the original.
type token struct {
	text       string
	start, end int
}

func tokenize(s string) []token {
	var toks []token
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				toks = append(toks, token{text: s[start:i], start: start, end: i})
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		toks = append(toks, token{text: s[start:], start: start, end: len(s)})
	}
	return toks
}

// ─── LCS alignment ────────────────────────────────────────────────────────────

// lcsOps returns the op sequence (Equal / Delete / Insert) aligning token
// slices a and b, deletes emitted before inserts within a changed region.
// When the DP table would exceed maxDPCells the middle collapses to one
// coarse delete-all + insert-all.
func lcsOps(a, b []token) []Kind {
	m, n := len(a), len(b)
	if m == 0 && n == 0 {
		return nil
	}
	if m == 0 || n == 0 || (m+1)*(n+1) > maxDPCells {
		ops := make([]Kind, 0, m+n)
		for i := 0; i < m; i++ {
			ops = append(ops, Delete)
		}
		for j := 0; j < n; j++ {
			ops = append(ops, Insert)
		}
		return ops
	}

	// dp[i*(n+1)+j] = LCS length of a[i:], b[j:] (suffix form → forward walk).
	width := n + 1
	dp := make([]int32, (m+1)*width)
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if a[i].text == b[j].text {
				dp[i*width+j] = dp[(i+1)*width+j+1] + 1
			} else if dp[(i+1)*width+j] >= dp[i*width+j+1] {
				dp[i*width+j] = dp[(i+1)*width+j]
			} else {
				dp[i*width+j] = dp[i*width+j+1]
			}
		}
	}

	ops := make([]Kind, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case a[i].text == b[j].text:
			ops = append(ops, Equal)
			i++
			j++
		case dp[(i+1)*width+j] >= dp[i*width+j+1]:
			ops = append(ops, Delete)
			i++
		default:
			ops = append(ops, Insert)
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, Delete)
	}
	for ; j < n; j++ {
		ops = append(ops, Insert)
	}
	return ops
}

// ─── Hunk assembly ────────────────────────────────────────────────────────────

// assemble groups consecutive same-kind ops into hunks and fills in text,
// offsets, and context windows from the original strings.
func assemble(a, b string, ta, tb []token, ops []Kind) []Hunk {
	var hunks []Hunk
	i, j := 0, 0 // token cursors into ta / tb
	for k := 0; k < len(ops); {
		kind := ops[k]
		aFrom, bFrom := i, j
		for k < len(ops) && ops[k] == kind {
			switch kind {
			case Equal:
				i++
				j++
			case Delete:
				i++
			case Insert:
				j++
			}
			k++
		}

		h := Hunk{Kind: kind}
		if aFrom < i { // has a-side tokens (equal / delete)
			h.OurText = a[ta[aFrom].start:ta[i-1].end]
			h.OurOffset = ta[aFrom].start
		} else {
			h.OurOffset = anchorOffset(a, ta, aFrom)
		}
		if bFrom < j { // has b-side tokens (equal / insert)
			h.TheirText = b[tb[bFrom].start:tb[j-1].end]
			h.TheirOffset = tb[bFrom].start
		} else {
			h.TheirOffset = anchorOffset(b, tb, bFrom)
		}

		// Context from whichever side the hunk has text in — ours for equal
		// and delete, theirs for insert.
		if kind == Insert {
			h.ContextBefore, h.ContextAfter = contextAround(b, tb[bFrom].start, tb[j-1].end)
		} else if aFrom < i {
			h.ContextBefore, h.ContextAfter = contextAround(a, ta[aFrom].start, ta[i-1].end)
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// anchorOffset is the byte offset in s where the token at index idx would
// begin — the anchor point for a zero-width span (an insert's position in
// ours, a delete's position in theirs).
func anchorOffset(s string, toks []token, idx int) int {
	if idx < len(toks) {
		return toks[idx].start
	}
	return len(s)
}

// contextAround returns up to contextChars bytes of s on each side of
// [start, end), snapped inward to rune boundaries so a window never splits a
// multibyte character.
func contextAround(s string, start, end int) (before, after string) {
	b := start - contextChars
	if b < 0 {
		b = 0
	}
	for b < start && !utf8.RuneStart(s[b]) {
		b++
	}
	a := end + contextChars
	if a > len(s) {
		a = len(s)
	}
	for a > end && a < len(s) && !utf8.RuneStart(s[a]) {
		a--
	}
	return strings.TrimLeft(s[b:start], " "), strings.TrimRight(s[end:a], " ")
}
