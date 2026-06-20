// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package writer

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
)

// Options tunes the writer. Zero values fall back to sane defaults in New.
type Options struct {
	Temperature       *float64
	MaxToolIterations int                                // agentic loop cap per drafter
	DraftMaxTokens    int                                // output budget per call
	InputBudgetTokens int                                // bound on any single call's input (fit the model window)
	MaxFindingsPerSec int                                // tight-agent cap; bigger clusters sub-fan-out
	MaxClusters       int                                // top-level topic cap
	ClusterThreshold  float64                            // cosine threshold for a finding to join a cluster
	Persona           string                             // optional tone/voice block appended to drafter system prompts
	RecordCost        func(resp *providers.ChatResponse) // optional cost hook
}

// Writer turns a task's findings into the final deliverable via scoped, multi-pass
// fan-out: cluster findings into tight sections (exactly-once partition), name them
// (planner), draft each with a real agentic sub-agent whose search_findings is
// scoped to its section, then stitch. No single call ever sees all findings.
type Writer struct {
	embed *embeddings.Client
	prov  providers.Provider
	model string // bare model id (already resolved)
	opt   Options
}

// New builds a Writer. prov/model is the (already-resolved) synthesis provider and
// model; embed may be nil (search degrades to BM25-only).
func New(embed *embeddings.Client, prov providers.Provider, model string, opt Options) *Writer {
	if opt.MaxToolIterations <= 0 {
		opt.MaxToolIterations = 4
	}
	if opt.DraftMaxTokens <= 0 {
		opt.DraftMaxTokens = 1200
	}
	if opt.InputBudgetTokens <= 0 {
		opt.InputBudgetTokens = 5000
	}
	if opt.MaxFindingsPerSec <= 0 {
		opt.MaxFindingsPerSec = 6
	}
	if opt.MaxClusters <= 0 {
		opt.MaxClusters = 8
	}
	if opt.ClusterThreshold == 0 {
		opt.ClusterThreshold = 0.55
	}
	return &Writer{embed: embed, prov: prov, model: model, opt: opt}
}

// section is one tight, drafter-sized unit: a partition of findings with a title.
type section struct {
	Title      string
	Brief      string
	FindingIDs []string
}

// Write produces the final deliverable. It never returns empty when findings exist:
// every model call has a deterministic fallback (the findings' own conclusions), so
// a flaky local model degrades to a plain grounded summary rather than a blank.
func (w *Writer) Write(taskDesc, workflowType string, findings []Finding) (string, error) {
	if len(findings) == 0 {
		return "", nil
	}
	ix := NewFindingIndex(w.embed, findings)

	// 1. Partition into tight sections (clustering = exactly-once coverage; oversized
	//    clusters sub-fan-out so every drafter stays small).
	secs := w.partition(ix)

	// 2. Planner names + orders the sections from compact labels (no finding dump).
	secs = w.planOutline(taskDesc, workflowType, ix, secs)

	// 3. One tight agentic drafter per section, search_findings scoped to its set.
	drafts := make([]string, len(secs))
	for i, s := range secs {
		drafts[i] = w.draftSection(taskDesc, workflowType, s, ix)
	}

	// 4. Stitch sections into one coherent document.
	return w.stitch(taskDesc, workflowType, secs, drafts), nil
}

// partition turns the finding set into tight sections: cluster, then split any
// cluster larger than MaxFindingsPerSec into sub-sections (two-level fan-out).
func (w *Writer) partition(ix *FindingIndex) []section {
	clusters := cluster(ix, w.opt.ClusterThreshold, w.opt.MaxClusters)
	var secs []section
	for _, c := range clusters {
		for _, part := range chunkFindings(c.Items, w.opt.MaxFindingsPerSec) {
			ids := make([]string, len(part))
			for i, f := range part {
				ids[i] = f.ID
			}
			secs = append(secs, section{Title: c.Label, Brief: c.Label, FindingIDs: ids})
		}
	}
	return secs
}

// planOutline asks the model to name + order the sections from a compact summary
// (label + count + one sample conclusion each). Coverage is unaffected — the
// finding partition is fixed; only titles/order/brief change. Falls back to the
// keyword labels on any failure, so it never breaks the document.
func (w *Writer) planOutline(taskDesc, workflowType string, ix *FindingIndex, secs []section) []section {
	if len(secs) <= 1 {
		return secs
	}
	var b strings.Builder
	for i, s := range secs {
		sample := ""
		if len(s.FindingIDs) > 0 {
			if f, ok := ix.Get(s.FindingIDs[0]); ok {
				sample = oneLine(strutil.TruncateToTokens(f.Content, 40))
			}
		}
		fmt.Fprintf(&b, "[%d] (%d findings; keywords: %s) e.g. %s\n", i+1, len(s.FindingIDs), s.Title, sample)
	}
	prompt := fmt.Sprintf(`TASK: %s
WORKFLOW: %s

Below are the topic groups discovered in the findings. For EACH group, give a clear section heading and a one-line brief of what it should cover. Keep the same group numbers. Output exactly one line per group, in the order they should appear in the final document:
[n] HEADING — one-line brief

GROUPS:
%s`, oneLine(taskDesc), workflowType, b.String())

	out, err := w.complete(plannerSystem, prompt, 800, nil)
	if err != nil || strings.TrimSpace(out) == "" {
		return secs
	}
	// Parse "[n] Heading — brief"; reorder by appearance, keep unmatched at the end.
	type named struct {
		idx         int
		title, desc string
	}
	var ordered []named
	used := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		n, title, desc := parsePlanLine(line)
		if n >= 1 && n <= len(secs) && !used[n-1] {
			used[n-1] = true
			ordered = append(ordered, named{n - 1, title, desc})
		}
	}
	for i := range secs {
		if !used[i] {
			ordered = append(ordered, named{i, secs[i].Title, secs[i].Brief})
		}
	}
	res := make([]section, 0, len(secs))
	for _, o := range ordered {
		s := secs[o.idx]
		if o.title != "" {
			s.Title = o.title
		}
		if o.desc != "" {
			s.Brief = o.desc
		}
		res = append(res, s)
	}
	return res
}

// draftSection runs ONE tight agentic sub-agent: a real multi-turn loop where the
// model calls search_findings (scoped to this section's findings) to pull its
// evidence, then writes the section. Falls back to a grounded bullet list of the
// section's findings if the model returns nothing.
func (w *Writer) draftSection(taskDesc, workflowType string, s section, ix *FindingIndex) string {
	allow := make(map[string]bool, len(s.FindingIDs))
	for _, id := range s.FindingIDs {
		allow[id] = true
	}
	tool := providers.ToolParam{
		Name:        "search_findings",
		Description: "Search the findings assigned to YOUR section. Returns each finding's conclusion plus its verbatim evidence and citation source. Only your section's findings are visible.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "What aspect of this section to retrieve"},
			},
			"required": []string{"query"},
		},
	}
	system := drafterSystem
	if w.opt.Persona != "" {
		system += "\n\n" + w.opt.Persona
	}
	user := fmt.Sprintf(`TASK: %s
WORKFLOW: %s

Write the section "%s" of the final deliverable. Brief: %s

Call search_findings to retrieve the findings for this section, then write the section grounded ONLY in what they say — do not invent facts, figures, or citations. If a finding is marked UNVERIFIED, either omit it or caveat it explicitly. Write clean client-ready prose: no finding numbers, agent names, or placeholder tokens. Output only the section text (no heading).`,
		oneLine(taskDesc), workflowType, s.Title, s.Brief)

	msgs := []providers.Message{{Role: "user", Content: user}}
	final := ""
	searched := false
	for it := 0; it < w.opt.MaxToolIterations; it++ {
		resp, err := w.prov.Chat(providers.ChatParams{
			Model:       w.model,
			MaxTokens:   w.opt.DraftMaxTokens,
			System:      system,
			Tools:       []providers.ToolParam{tool},
			Messages:    msgs,
			CacheSystem: true,
			Temperature: w.opt.Temperature,
		})
		if err != nil {
			break
		}
		if w.opt.RecordCost != nil {
			w.opt.RecordCost(resp)
		}
		for _, b := range resp.Content {
			if b.Type == providers.BlockText && strings.TrimSpace(b.Text) != "" {
				final = b.Text
			}
		}
		if resp.StopReason == providers.StopToolUse {
			msgs = append(msgs, providers.Message{Role: "assistant", Content: resp.Content})
			var results []providers.ContentBlock
			for _, b := range resp.Content {
				if b.Type != providers.BlockToolUse {
					continue
				}
				searched = true
				q, _ := b.Input["query"].(string)
				hits := ix.SearchScoped(q, w.opt.MaxFindingsPerSec, allow)
				raw, _ := json.Marshal(map[string]interface{}{"findings": findingsToJSON(hits)})
				results = append(results, providers.ContentBlock{Type: providers.BlockToolResult, ToolUseID: b.ID, Content: string(raw)})
			}
			msgs = append(msgs, providers.Message{Role: "user", Content: results})
			continue
		}
		// Nudge a weak model to actually pull its findings before finishing.
		if !searched && it < w.opt.MaxToolIterations-1 {
			msgs = append(msgs, providers.Message{Role: "assistant", Content: resp.Content})
			msgs = append(msgs, providers.Message{Role: "user", Content: "Call search_findings first to retrieve this section's findings, then write the section."})
			continue
		}
		break
	}
	if strings.TrimSpace(final) == "" {
		return w.fallbackSection(s, ix) // never blank
	}
	return strings.TrimSpace(final)
}

// stitch assembles the section drafts under their headings and merges them into one
// coherent deliverable. The merge is HIERARCHICAL so it dedups at any scale: when
// the assembled sections exceed the input budget, they are batched, each batch is
// coherence-merged (removing repetition within bounds), and the results recurse
// until the whole thing fits one final polish pass. Never empty.
func (w *Writer) stitch(taskDesc, workflowType string, secs []section, drafts []string) string {
	var blocks []string
	for i, s := range secs {
		body := strings.TrimSpace(drafts[i])
		if body == "" {
			continue
		}
		blocks = append(blocks, fmt.Sprintf("## %s\n\n%s", s.Title, body))
	}
	if len(blocks) == 0 {
		return ""
	}
	return w.mergeBlocks(taskDesc, workflowType, blocks, 0)
}

// mergeBlocks reduces titled section blocks to one coherent document. If they fit
// the budget (or recursion is capped), it runs the final polish pass; otherwise it
// batches them to budget-sized groups, dedup-merges each, and recurses.
func (w *Writer) mergeBlocks(taskDesc, workflowType string, blocks []string, depth int) string {
	joined := strings.Join(blocks, "\n\n")
	if strutil.EstimateTokens(joined) <= w.opt.InputBudgetTokens || depth >= 3 {
		if out := w.coherenceMerge(taskDesc, workflowType, joined, true); out != "" {
			return out
		}
		return strings.TrimSpace(joined) // never empty: fall back to the assembly
	}
	batches := batchByTokens(blocks, w.opt.InputBudgetTokens)
	if len(batches) >= len(blocks) {
		return strings.TrimSpace(joined) // can't reduce further; stay non-empty
	}
	merged := make([]string, 0, len(batches))
	for _, batch := range batches {
		bt := strings.Join(batch, "\n\n")
		if len(batch) == 1 {
			merged = append(merged, bt)
			continue
		}
		if m := w.coherenceMerge(taskDesc, workflowType, bt, false); m != "" {
			merged = append(merged, m)
		} else {
			merged = append(merged, bt)
		}
	}
	return w.mergeBlocks(taskDesc, workflowType, merged, depth+1)
}

// coherenceMerge runs one bounded dedup/polish pass over a set of section blocks.
// final=true also adds an opening and smooths transitions for the whole document.
func (w *Writer) coherenceMerge(taskDesc, workflowType, draft string, final bool) string {
	instr := "Combine the sections below into coherent prose, REMOVING any repetition across them while keeping every distinct factual point and the section headings (## ). Do not add new facts, figures, or citations."
	if final {
		instr = "Polish the sections below into one coherent, client-ready deliverable: add a brief executive opening, smooth the transitions, REMOVE duplication across sections, and keep every distinct factual point and the section headings (## ). Do not add new facts, figures, or citations."
	}
	prompt := fmt.Sprintf("TASK: %s\nWORKFLOW: %s\n\n%s\n\nSECTIONS:\n%s", oneLine(taskDesc), workflowType, instr, draft)
	out, err := w.complete(stitchSystem, prompt, w.opt.DraftMaxTokens*2, nil)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// batchByTokens greedily packs blocks into groups each within budget (a block
// larger than budget becomes its own group).
func batchByTokens(blocks []string, budget int) [][]string {
	var out [][]string
	var cur []string
	curTok := 0
	for _, b := range blocks {
		t := strutil.EstimateTokens(b)
		if len(cur) > 0 && curTok+t > budget {
			out = append(out, cur)
			cur, curTok = nil, 0
		}
		cur = append(cur, b)
		curTok += t
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// fallbackSection renders a section as a grounded bullet list of its findings'
// conclusions — used when the drafter model returns nothing, so output is never blank.
func (w *Writer) fallbackSection(s section, ix *FindingIndex) string {
	var b strings.Builder
	for _, id := range s.FindingIDs {
		f, ok := ix.Get(id)
		if !ok {
			continue
		}
		c := oneLine(f.Content)
		if !f.Grounded {
			c += " (unverified — requires confirmation)"
		}
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return strings.TrimSpace(b.String())
}

// complete is a single, tool-less model call (planner / stitch passes).
func (w *Writer) complete(system, user string, maxTokens int, _ any) (string, error) {
	resp, err := w.prov.Chat(providers.ChatParams{
		Model:       w.model,
		MaxTokens:   maxTokens,
		System:      system,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		Temperature: w.opt.Temperature,
	})
	if err != nil {
		return "", err
	}
	if w.opt.RecordCost != nil {
		w.opt.RecordCost(resp)
	}
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return b.Text, nil
		}
	}
	return "", nil
}

const (
	plannerSystem = "You organise legal findings into a clean document outline. You output only the requested headings and briefs, nothing else."
	drafterSystem = "You are a legal writer drafting one section of a client deliverable. You ground every statement in the findings retrieved via search_findings and never invent facts, figures, or citations. You write clear, professional prose."
	stitchSystem  = "You are a senior legal editor assembling section drafts into one coherent client-ready deliverable. You never introduce facts the drafts do not contain."
)

// findingsToJSON shapes findings for a search_findings tool result.
func findingsToJSON(fs []Finding) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(fs))
	for _, f := range fs {
		m := map[string]interface{}{"conclusion": f.Content, "evidence": f.Evidence, "source": f.Source}
		if !f.Grounded {
			m["status"] = "UNVERIFIED — caveat or omit"
		}
		out = append(out, m)
	}
	return out
}

// chunkFindings splits a finding slice into runs of at most n (tight-agent cap).
func chunkFindings(fs []Finding, n int) [][]Finding {
	if n <= 0 || len(fs) <= n {
		return [][]Finding{fs}
	}
	var out [][]Finding
	for i := 0; i < len(fs); i += n {
		end := i + n
		if end > len(fs) {
			end = len(fs)
		}
		out = append(out, fs[i:end])
	}
	return out
}

// parsePlanLine accepts the planner's heading lines in any of the common shapes a
// weaker model emits: "[1] H — b", "1. H", "1) H", "- 1: H", "**1.** H". It pulls
// the leading number and splits an optional brief off the heading.
func parsePlanLine(line string) (n int, title, desc string) {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "-*• \t")
	line = strings.TrimPrefix(line, "[")
	// Read the leading integer, then skip its trailing delimiter (]./):).
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, "", ""
	}
	fmt.Sscanf(line[:i], "%d", &n)
	rest := strings.TrimLeft(line[i:], "]).:*— -\t")
	rest = strings.TrimSpace(rest)
	for _, sep := range []string{" — ", " - ", ": ", " – ", " | "} {
		if j := strings.Index(rest, sep); j >= 0 {
			return n, cleanHeading(rest[:j]), strings.TrimSpace(rest[j+len(sep):])
		}
	}
	return n, cleanHeading(rest), ""
}

// cleanHeading strips markdown emphasis and trailing punctuation from a heading.
func cleanHeading(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "*_#")
	return strings.TrimSpace(strings.TrimRight(s, ".:—- "))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
