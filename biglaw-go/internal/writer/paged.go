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

	// DyTopo drafting: Phase 1 — concurrent writing huddles (independent per section);
	// Phase 2 — sequential paged compose (compact + lossless assemble). Generation is
	// parallel; coherence is the sequential paged pass over the results.
	if w.opt.DyTopoDrafting && len(w.opt.DraftingAgents) > 0 {
		drafts := make([]string, len(secs))
		var wg sync.WaitGroup
		conc := draftingConcurrency
		if conc > len(secs) {
			conc = len(secs)
		}
		sem := make(chan struct{}, conc)
		for i, s := range secs {
			wg.Add(1)
			go func(i int, s section) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				d := w.huddleSection(taskDesc, workflowType, s, ix)
				if strings.TrimSpace(d) == "" {
					d = w.fallbackSection(s, ix)
				}
				drafts[i] = d
			}(i, s)
		}
		wg.Wait()
		for i, s := range secs { // Phase 2: compact, then lossless assemble
			board.put(s.Title, drafts[i], w.compactSection(s.Title, drafts[i]))
		}
		return w.assemblePaged(secs, board)
	}

	// Single-drafter paging: author in order, each aware of prior compacted sections.
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

// draftingConcurrency bounds Phase-1 huddle parallelism. Sections are independent, so this
// is safe; the cap keeps a local model server from being swamped.
const draftingConcurrency = 4

// huddleSection produces one section via a bounded writing huddle: the lead drafts, then
// each contributor critiques against the section's findings and offers grounded additions,
// and the lead revises — DyTopo's draft→offer→revise collaboration, bounded by DraftingRounds.
func (w *Writer) huddleSection(taskDesc, workflowType string, s section, ix *FindingIndex) string {
	lead := ""
	if len(w.opt.DraftingAgents) > 0 {
		lead = w.opt.DraftingAgents[0]
	}
	contributors := w.opt.DraftingAgents[min(1, len(w.opt.DraftingAgents)):]

	draft := w.draftSection(taskDesc, workflowType, s, ix, draftExtra{system: lead})
	rounds := w.opt.DraftingRounds
	if rounds < 1 {
		rounds = 1
	}
	for r := 1; r < rounds && len(contributors) > 0; r++ {
		var notes []string
		for _, c := range contributors {
			if n := w.critiqueSection(s, draft, ix, c); strings.TrimSpace(n) != "" && !strings.Contains(strings.ToUpper(n), "COMPLETE") {
				notes = append(notes, n)
			}
		}
		if len(notes) == 0 {
			break // huddle converged
		}
		draft = w.reviseSection(taskDesc, workflowType, s, draft, strings.Join(notes, "\n"), lead)
	}
	return draft
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// critiqueSection is a contributor's offer in the huddle: it reads the lead's draft against
// the section's own findings and returns ONLY concrete grounded gaps (missing/wrong facts,
// figures, parties, citations), or "COMPLETE". No tools — a single focused pass.
func (w *Writer) critiqueSection(s section, draft string, ix *FindingIndex, voice string) string {
	var ev strings.Builder
	for i, id := range s.FindingIDs {
		if i >= 20 {
			break
		}
		if f, ok := ix.Get(id); ok {
			ev.WriteString("- ")
			ev.WriteString(oneLine(f.Content))
			ev.WriteString("\n")
		}
	}
	if strings.TrimSpace(ev.String()) == "" {
		return ""
	}
	system := "You review a draft section against its source findings and list ONLY concrete, grounded specifics the findings support but the draft OMITS or states WRONGLY — missing dollar amounts, percentages, dates, parties, account numbers, or citations. Quote the exact value from the findings. Be brief; one bullet each. If the draft already covers the findings, reply exactly: COMPLETE."
	if voice != "" {
		system += "\n\n" + voice
	}
	user := fmt.Sprintf("SECTION: %s\n\nDRAFT:\n%s\n\nSOURCE FINDINGS:\n%s\n\nMissing/incorrect grounded specifics (bullets), or COMPLETE:", s.Title, oneLine(draft), ev.String())
	resp, err := w.prov.Chat(providers.ChatParams{
		Model:       w.model,
		MaxTokens:   400,
		System:      system,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		Temperature: w.opt.Temperature,
	})
	if err != nil {
		return ""
	}
	if w.opt.RecordCost != nil {
		w.opt.RecordCost(resp)
	}
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return strings.TrimSpace(b.Text)
		}
	}
	return ""
}

// reviseSection has the lead integrate the contributors' grounded additions into the draft,
// keeping flowing prose and adding only what the findings support.
func (w *Writer) reviseSection(taskDesc, workflowType string, s section, draft, notes string, lead string) string {
	system := drafterSystem
	if lead != "" {
		system += "\n\n" + lead
	}
	if w.opt.Persona != "" {
		system += "\n\n" + w.opt.Persona
	}
	user := fmt.Sprintf("Revise the section \"%s\" to incorporate the grounded additions/corrections below. Keep it flowing prose; ADD only what the additions state (they are grounded in the source); do not remove correct content; state any figure exactly as given. Output only the revised section prose.\n\nCURRENT DRAFT:\n%s\n\nGROUNDED ADDITIONS/CORRECTIONS:\n%s",
		s.Title, draft, notes)
	resp, err := w.prov.Chat(providers.ChatParams{
		Model:       w.model,
		MaxTokens:   w.opt.DraftMaxTokens,
		System:      system,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		Temperature: w.opt.Temperature,
	})
	if err != nil {
		return draft // keep the lead's draft on failure
	}
	if w.opt.RecordCost != nil {
		w.opt.RecordCost(resp)
	}
	for _, b := range resp.Content {
		if b.Type == providers.BlockText && strings.TrimSpace(b.Text) != "" {
			return sanitizeDraft(b.Text)
		}
	}
	return draft
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
