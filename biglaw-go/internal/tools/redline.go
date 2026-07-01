// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// edit_document: propose minimal substitutions to a .docx as Word tracked
// changes. Each edit is anchored by short before/after context and located
// with a progressive fallback — exact contextual match, then curly-quote /
// whitespace normalisation, then context-only localisation — so it survives
// the run splits and smart quotes Word introduces. Edits apply independently;
// the result is written next to the input as <stem>.redlined.docx.

package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/ooxml"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

const defaultRedlineAuthor = "Big Michael"

func (r *Registry) registerTrackedChangesTools() {
	r.Register(r.editDocumentTool())
}

// ─── edit_document ────────────────────────────────────────────────────────────

func (r *Registry) editDocumentTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "edit_document",
		Schema: providers.ToolParam{
			Name:        "edit_document",
			Description: "Propose edits to a .docx as Word tracked changes. Each edit is a precise, minimal substitution of specific words or characters (not a whole-paragraph rewrite), anchored with short before/after context so it can be located unambiguously. Writes a new redlined .docx next to the input and returns per-edit annotations plus the output path.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":   map[string]interface{}{"type": "string", "description": ".docx to edit (absolute, or relative to the document output directory), e.g. one produced by docx_generate"},
					"author": map[string]interface{}{"type": "string", "description": "Tracked-change author (default \"Big Michael\")"},
					"edits": map[string]interface{}{
						"type":        "array",
						"description": "Edits to apply; each is located and applied independently",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"find":           map[string]interface{}{"type": "string", "description": "Exact substring to replace; keep as short as possible"},
								"replace":        map[string]interface{}{"type": "string", "description": "Replacement text; empty string deletes the found text"},
								"context_before": map[string]interface{}{"type": "string", "description": "~40 characters immediately preceding the found text"},
								"context_after":  map[string]interface{}{"type": "string", "description": "~40 characters immediately following the found text"},
								"reason":         map[string]interface{}{"type": "string", "description": "Short explanation for the change card"},
							},
							"required": []string{"find", "replace", "context_before", "context_after"},
						},
					},
				},
				"required": []string{"path", "edits"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			src, err := r.resolveDocxPath(strInput(input, "path"))
			if err != nil {
				return fail(err.Error()), nil
			}
			if !strings.EqualFold(filepath.Ext(src), ".docx") {
				return fail("only .docx files can be edited"), nil
			}
			editsRaw, _ := input["edits"].([]interface{})
			if len(editsRaw) == 0 {
				return fail("edits must be a non-empty array"), nil
			}
			doc, err := ooxml.OpenFile(src)
			if err != nil {
				return fail(fmt.Sprintf("cannot open document: %v", err)), nil
			}

			author := strings.TrimSpace(strInput(input, "author"))
			if author == "" {
				author = defaultRedlineAuthor
			}
			rev := ooxml.NewRevisions(author, time.Now().UTC())

			annotations := []map[string]interface{}{}
			editErrors := []map[string]interface{}{}
			for i, raw := range editsRaw {
				edit, ok := raw.(map[string]interface{})
				if !ok {
					editErrors = append(editErrors, map[string]interface{}{"index": i, "error": "edit must be an object"})
					continue
				}
				find := strInput(edit, "find")
				replace := strInput(edit, "replace")
				before := strInput(edit, "context_before")
				after := strInput(edit, "context_after")
				if find == "" && replace == "" {
					editErrors = append(editErrors, map[string]interface{}{"index": i, "error": "find and replace are both empty"})
					continue
				}
				// Re-read the text each time: a previous edit shifts offsets.
				start, end, strategy, found := locateEdit(doc.Text(), find, before, after)
				if !found {
					editErrors = append(editErrors, map[string]interface{}{
						"index": i, "find": find,
						"error": "could not locate the text: neither the exact string, a quote/whitespace-normalised form, nor the surrounding context matched",
					})
					continue
				}
				if err := doc.ApplyTracked(start, end, replace, rev); err != nil {
					editErrors = append(editErrors, map[string]interface{}{"index": i, "find": find, "error": err.Error()})
					continue
				}
				annotations = append(annotations, map[string]interface{}{
					"index":     i,
					"find":      find,
					"replace":   replace,
					"reason":    strInput(edit, "reason"),
					"author":    author,
					"matchedBy": strategy,
				})
			}

			stem := src[:len(src)-len(filepath.Ext(src))]
			outputPath := stem + ".redlined.docx"
			if err := doc.SaveFile(outputPath); err != nil {
				return fail(fmt.Sprintf("cannot write redlined document: %v", err)), nil
			}
			return map[string]interface{}{
				"ok":           true,
				"outputPath":   outputPath,
				"appliedCount": len(annotations),
				"errorCount":   len(editErrors),
				"annotations":  annotations,
				"errors":       editErrors,
			}, nil
		},
	}
}

// ─── Anchoring ────────────────────────────────────────────────────────────────

// locateEdit finds the byte span of find within text, disambiguated by the
// surrounding context, falling back through progressively more tolerant
// strategies:
//
//  1. "exact"      — occurrences of find scored by context overlap;
//  2. "normalized" — the same after straightening curly quotes and collapsing
//     whitespace runs in both the document and the edit;
//  3. "context"    — locate context_before and context_after alone and take
//     the span between them (also how a pure insertion, find == "", lands).
//
// Returns the span in original-text byte offsets plus the strategy name.
func locateEdit(text, find, before, after string) (start, end int, strategy string, ok bool) {
	if find != "" {
		if p, ok := bestOccurrence(text, find, before, after); ok {
			return p, p + len(find), "exact", true
		}
	}

	normText, backMap := normalizeWithMap(text)
	normFind := normalizeText(find)
	normBefore := normalizeText(before)
	normAfter := normalizeText(after)

	if normFind != "" {
		if p, ok := bestOccurrence(normText, normFind, normBefore, normAfter); ok {
			return backMap[p], backMap[p+len(normFind)], "normalized", true
		}
	}

	// Context-only localisation: the span between the two anchors.
	nb := strings.TrimSpace(normBefore)
	na := strings.TrimSpace(normAfter)
	if nb == "" || na == "" {
		return 0, 0, "", false
	}
	const slack = 80
	bestDiff := -1
	var bs, be int
	for i := 0; ; {
		j := strings.Index(normText[i:], nb)
		if j < 0 {
			break
		}
		gapStart := i + j + len(nb)
		window := min(len(normText), gapStart+len(normFind)+slack+len(na))
		if g := strings.Index(normText[gapStart:window], na); g >= 0 {
			if diff := absInt(g - len(normFind)); bestDiff < 0 || diff < bestDiff {
				bestDiff, bs, be = diff, gapStart, gapStart+g
			}
		}
		i += j + 1
	}
	if bestDiff < 0 {
		return 0, 0, "", false
	}
	return backMap[bs], backMap[be], "context", true
}

// bestOccurrence returns the position of the occurrence of needle whose
// surroundings best match the given context. A unique occurrence is accepted
// as-is; multiple occurrences are scored by context overlap.
func bestOccurrence(text, needle, before, after string) (int, bool) {
	if needle == "" {
		return 0, false
	}
	var positions []int
	for i := 0; len(positions) < 1000; {
		j := strings.Index(text[i:], needle)
		if j < 0 {
			break
		}
		positions = append(positions, i+j)
		i += j + 1
	}
	if len(positions) == 0 {
		return 0, false
	}
	if len(positions) == 1 {
		return positions[0], true
	}
	best, bestScore := positions[0], -1
	for _, p := range positions {
		score := commonSuffixLen(text[:p], before) + commonPrefixLen(text[p+len(needle):], after)
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	return best, true
}

// commonSuffixLen counts how many trailing bytes of a match the trailing
// bytes of b.
func commonSuffixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

// commonPrefixLen counts how many leading bytes of a match the leading bytes
// of b.
func commonPrefixLen(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// normalizeWithMap straightens curly quotes and collapses each whitespace run
// to a single space. It returns the normalised string plus a map from every
// byte of it (and one past the end) back to a byte offset in the original, so
// a match in normalised space converts back to an original-text span.
func normalizeWithMap(s string) (string, []int) {
	var b strings.Builder
	backMap := make([]int, 0, len(s)+1)
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			startAt := i
			for i < len(s) {
				r2, s2 := utf8.DecodeRuneInString(s[i:])
				if !unicode.IsSpace(r2) {
					break
				}
				i += s2
			}
			b.WriteByte(' ')
			backMap = append(backMap, startAt)
			continue
		}
		switch r {
		case '‘', '’', '‚', '‛': // curly single quotes
			b.WriteByte('\'')
			backMap = append(backMap, i)
		case '“', '”', '„', '‟': // curly double quotes
			b.WriteByte('"')
			backMap = append(backMap, i)
		default:
			b.WriteString(s[i : i+size])
			for k := 0; k < size; k++ {
				backMap = append(backMap, i)
			}
		}
		i += size
	}
	backMap = append(backMap, len(s))
	return b.String(), backMap
}

// normalizeText is normalizeWithMap without the offset map.
func normalizeText(s string) string {
	out, _ := normalizeWithMap(s)
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
