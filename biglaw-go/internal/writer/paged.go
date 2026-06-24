// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package writer

import (
	"fmt"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// compactTokenBudget bounds a section's compacted handle. A section already smaller than
// this is its own handle (no compaction call).
const compactTokenBudget = 220

// draftExtra carries the paging context into draftSection: the DyTopo writing agent's
// system prompt to author as, the compacted handles of already-written sections to seed,
// and the board that backs expand_section. The zero value is the classic (non-paged) path.
type draftExtra struct {
	system         string
	priorCompacted string
	board          *pagedBoard
}

// pagedBoard is the synthesis-time evidence blackboard for context paging: every finished
// section is held at FULL detail and as a COMPACT handle. Later section authors see only
// the handles (small), and call expand_section to uncompact one on demand. Final assembly
// reads the full forms — so nothing is ever lost, only paged out of working context.
type pagedBoard struct {
	mu      sync.Mutex
	order   []string
	full    map[string]string
	compact map[string]string
}

func newPagedBoard() *pagedBoard {
	return &pagedBoard{full: map[string]string{}, compact: map[string]string{}}
}

func (b *pagedBoard) put(title, full, compact string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.full[title]; !ok {
		b.order = append(b.order, title)
	}
	b.full[title] = full
	b.compact[title] = compact
}

// expand returns a finished section's full text by title — exact match first, then a
// loose contains-match (a weak model may paraphrase the title in its tool call).
func (b *pagedBoard) expand(title string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.full[title]; ok {
		return v
	}
	lt := strings.ToLower(strings.TrimSpace(title))
	if lt == "" {
		return ""
	}
	for t, v := range b.full {
		lo := strings.ToLower(t)
		if strings.Contains(lo, lt) || strings.Contains(lt, lo) {
			return v
		}
	}
	return ""
}

// priorBlock renders the compacted handles of all finished sections, for seeding the next
// author's prompt.
func (b *pagedBoard) priorBlock() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.order) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\nSECTIONS ALREADY WRITTEN (COMPACTED summaries — call expand_section with a title to read one in full). Do NOT repeat their content; cross-reference instead, and keep any shared figures, parties, and citations CONSISTENT with them:\n")
	for _, t := range b.order {
		fmt.Fprintf(&sb, "\n### %s\n%s\n", t, b.compact[t])
	}
	return sb.String()
}

// writePaged authors each section in order AS the chosen DyTopo writing agent, compacting
// each finished section so it stops consuming context, then assembles the full (uncompacted)
// sections losslessly. This lets a small-context model produce a deliverable far larger than
// its window without dropping content (the failure mode of the compressing stitch).
func (w *Writer) writePaged(taskDesc, workflowType string, secs []section, ix *FindingIndex) string {
	board := newPagedBoard()
	for _, s := range secs {
		full := w.draftSection(taskDesc, workflowType, s, ix, draftExtra{
			system:         w.opt.WriterSystem,
			priorCompacted: board.priorBlock(),
			board:          board,
		})
		if strings.TrimSpace(full) == "" {
			full = w.fallbackSection(s, ix) // never blank
		}
		board.put(s.Title, full, w.compactSection(s.Title, full))
	}
	return w.assemblePaged(secs, board)
}

// compactSection shrinks a finished section to a fact-preserving handle: a section already
// within the budget is its own handle; otherwise one cheap model call keeps every party,
// verbatim figure, and citation while dropping prose. The FULL text stays on the board for
// expand_section, so this never loses information — it only frees working context.
func (w *Writer) compactSection(title, full string) string {
	full = strings.TrimSpace(full)
	if full == "" {
		return ""
	}
	if strutil.EstimateTokens(full) <= compactTokenBudget {
		return full
	}
	prompt := fmt.Sprintf("Compact the section below into a SHORT reference handle for a writer working on the matter's other sections. PRESERVE: the allegation/topic, every named party, entity, fund, and account, every figure VERBATIM (dollar amounts, percentages, rates, counts, dates, account numbers), and every legal citation. Drop only explanatory prose. 4-8 bullet points, no preamble.\n\nSECTION \"%s\":\n%s", title, full)
	resp, err := w.prov.Chat(providers.ChatParams{
		Model:       w.model,
		MaxTokens:   400,
		System:      "You compress a written legal section into a compact, fact-preserving reference handle. Keep all parties, figures (verbatim), and citations; output only the bullets.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: w.opt.Temperature,
	})
	if err != nil {
		return strutil.TruncateToTokens(full, compactTokenBudget)
	}
	if w.opt.RecordCost != nil {
		w.opt.RecordCost(resp)
	}
	for _, b := range resp.Content {
		if b.Type == providers.BlockText && strings.TrimSpace(b.Text) != "" {
			return strings.TrimSpace(b.Text)
		}
	}
	return strutil.TruncateToTokens(full, compactTokenBudget)
}

// assemblePaged concatenates the full (uncompacted) sections under their headings —
// lossless by construction: no merge/compress pass that could delete a fact.
func (w *Writer) assemblePaged(secs []section, board *pagedBoard) string {
	var sb strings.Builder
	for _, s := range secs {
		full := strings.TrimSpace(board.full[s.Title])
		if full == "" {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n\n%s\n\n", s.Title, full)
	}
	return strings.TrimSpace(sb.String())
}
