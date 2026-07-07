// SPDX-License-Identifier: Apache-2.0
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

// get returns a finished section's full text by exact title ("" when absent).
func (b *pagedBoard) get(title string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.full[title]
}

// compactOf returns a finished section's compact handle by exact title ("" when absent).
func (b *pagedBoard) compactOf(title string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.compact[title]
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
		return w.finalizePaged(taskDesc, secs, board)
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
	return w.finalizePaged(taskDesc, secs, board)
}

// finalizePaged is the memo-frame pass over the finished sections: enforce the
// per-respondent exposure roster, assemble losslessly, suppress duplicate blocks, then
// wrap the body in memo structure — an executive summary up top (with the mechanical
// figure roll-ups) and a conclusions/posture close. The frame passes are best-effort
// model calls; the body, roster, and roll-ups are deterministic.
func (w *Writer) finalizePaged(taskDesc string, secs []section, board *pagedBoard) string {
	secs = w.enforceRoster(secs, board)
	doc := dedupeDocBlocks(w.assemblePaged(secs, board))
	if strings.TrimSpace(doc) == "" {
		return doc
	}
	rollups := computeRollups(w.opt.Facts)
	var itemized []string
	if len(rollups) == 0 {
		// No grounded decomposition on the record → never assert arithmetic; present the
		// headline amounts as an itemization, explicitly not totaled.
		itemized = computeItemization(w.opt.Facts)
	}
	// Nesting guard: a section spine (or an earlier compose) may already carry these
	// document-level frames — never wrap a composed doc in a second frame.
	exec, concl := "", ""
	if !hasHeading(doc, "executive summary") {
		exec = w.frameSection(taskDesc, board,
			"Write the EXECUTIVE SUMMARY of the deliverable whose sections are summarized below: 2-3 short paragraphs stating what the matter is, the principal allegations or issues, the headline grounded figures, the parties implicated, and the overall posture. Use ONLY facts stated below — no new facts, figures, or citations. No headings, no bullets, no process commentary.")
	}
	if !hasHeading(doc, "conclusion and posture") && !hasHeading(doc, "conclusion") {
		concl = w.frameSection(taskDesc, board,
			"Write the closing CONCLUSION AND POSTURE section for the deliverable whose sections are summarized below: 1-2 paragraphs assessing the overall position and the recommended next steps, grounded ONLY in what the sections state. No new facts, figures, or citations. No headings, no bullets, no process commentary.")
	}

	var b strings.Builder
	if exec != "" || len(rollups) > 0 || len(itemized) > 0 {
		b.WriteString("## Executive Summary\n\n")
		if exec != "" {
			b.WriteString(exec)
			b.WriteString("\n\n")
		}
		if len(rollups) > 0 {
			b.WriteString("**Figure roll-up (computed from the grounded record — the source states each aggregate and its components):**\n")
			b.WriteString(strings.Join(rollups, "\n"))
			b.WriteString("\n\n")
		} else if len(itemized) > 1 {
			b.WriteString("**Principal grounded amounts (itemized — distinct figures on the record; the record does not state that they sum to any single total, so they are not totaled):**\n")
			b.WriteString(strings.Join(itemized, "\n"))
			b.WriteString("\n\n")
		}
	}
	b.WriteString(doc)
	if concl != "" {
		b.WriteString("\n\n## Conclusion and Posture\n\n")
		b.WriteString(concl)
	}
	return strings.TrimSpace(b.String())
}

// hasHeading reports whether the document already carries a markdown heading with the
// given (case-insensitive) title — the double-frame guard.
func hasHeading(doc, title string) bool {
	for _, ln := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(strings.TrimLeft(t, "# ")), title) {
			return true
		}
	}
	return false
}

// frameSection writes one document-level frame passage (executive summary / closing
// posture) from the compacted section handles — bounded input, sanitized and polished
// like any section. Returns "" on any failure: the frame is omitted, never placeholder.
func (w *Writer) frameSection(taskDesc string, board *pagedBoard, instr string) string {
	handles := board.priorBlock()
	if strings.TrimSpace(handles) == "" {
		return ""
	}
	prompt := fmt.Sprintf("TASK: %s\n\n%s\n%s", strings.Join(strings.Fields(taskDesc), " "), instr,
		strutil.TruncateToTokens(handles, w.opt.InputBudgetTokens))
	out, err := w.complete(stitchSystem, prompt, w.opt.DraftMaxTokens, nil)
	if err != nil || isRefusalDraft(out) {
		return ""
	}
	// Despite the "no headings" instruction, a weak model wraps its frame passage in a
	// document skeleton of its own (a title line, an "EXECUTIVE SUMMARY" heading, "---"
	// rules). finalizePaged supplies the frame headings; scaffolding inside the passage
	// is what produced the NESTED document (two title blocks, two exec summaries) — strip
	// it mechanically.
	return polishSection("", stripFrameScaffolding(sanitizeDraft(out)))
}

// stripFrameScaffolding removes document-skeleton lines from a frame passage: markdown
// headings, horizontal rules, short ALL-CAPS title lines, and bare frame-title lines.
func stripFrameScaffolding(s string) string {
	var keep []string
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "#"):
			continue
		case reHrLine.MatchString(t) && t != "":
			continue
		case isTitleCapsLine(t):
			continue
		case strings.EqualFold(t, "executive summary"),
			strings.EqualFold(t, "conclusion"),
			strings.EqualFold(t, "conclusion and posture"):
			continue
		}
		if t == "" && len(keep) > 0 && strings.TrimSpace(keep[len(keep)-1]) == "" {
			continue
		}
		keep = append(keep, ln)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// isTitleCapsLine reports whether a line is a short ALL-CAPS title ("ALLEGATION
// EXTRACTION SUMMARY", "ALLEGATION-EXTRACTION-SUMMARY.DOCX") — letters present, none
// lowercase, at most ten words.
func isTitleCapsLine(t string) bool {
	if t == "" || len(strings.Fields(t)) > 10 {
		return false
	}
	hasLetter := false
	for _, r := range t {
		if r >= 'a' && r <= 'z' {
			return false
		}
		if r >= 'A' && r <= 'Z' {
			hasLetter = true
		}
	}
	return hasLetter
}

// enforceRoster guarantees the per-respondent exposure entries: it locates the exposure
// section (creating one when the spine lacks it) and appends the consolidated roster
// block — one entry per named individual respondent, each either a consolidated grounded
// record or an explicit gap note. A respondent the extraction missed becomes a visible
// hole for review, never a silent omission.
func (w *Writer) enforceRoster(secs []section, board *pagedBoard) []section {
	rb := w.rosterBlock()
	if rb == "" {
		return secs
	}
	idx := -1
	for i, s := range secs {
		lt := strings.ToLower(s.Title)
		if strings.Contains(lt, "exposure") || strings.Contains(lt, "individuals at risk") {
			idx = i
			break
		}
	}
	if idx < 0 {
		secs = append(secs, section{Title: "Individual Exposure"})
		idx = len(secs) - 1
	}
	full := board.get(secs[idx].Title)
	if strings.Contains(full, rosterHeader) {
		return secs
	}
	nf := strings.TrimSpace(strings.TrimSpace(full) + "\n\n" + rb)
	board.put(secs[idx].Title, nf, board.compactOf(secs[idx].Title))
	return secs
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
			if n := strings.TrimSpace(b.Text); !isRefusalDraft(n) {
				return n
			}
			return "" // a refusal is not a critique — treat as no note
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
			// A reviser that argues with the critique ("I appreciate the detailed
			// correction, but I need to clarify my role…") produced dialogue, not a
			// revision — keep the lead's draft.
			if isRefusalDraft(b.Text) {
				return draft
			}
			if out := polishSection(s.Title, sanitizeDraft(b.Text)); out != "" {
				return out
			}
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
