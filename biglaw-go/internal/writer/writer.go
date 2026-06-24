// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package writer

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
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
	// Specifics, when set, pulls figure-dense source passages (the document-backed
	// extract_specifics) for a topic. Section drafters call it AT SYNTHESIS — both
	// seeded into the opening prompt and available as a tool — to ground a section's
	// exact numbers (amounts, %, dates, counts, account #s, statute cites) without
	// pre-stuffing every figure into findings. Returns verbatim row hits.
	Specifics func(topic string, topK int) []SpecificHit
	// RequiredSections, when non-empty, is the TOP-DOWN coverage spine: the matter's
	// own enumerated topics (e.g. the referral's allegation categories). Each becomes
	// a GUARANTEED section with findings mapped into it — so no required category can
	// silently vanish through clustering variance. Empty → fall back to clustering.
	RequiredSections []string
	// WriterSystem, when set, is the system prompt of the DyTopo writing agent chosen
	// for this deliverable (e.g. the Due-Diligence Report Drafter). The section authors
	// then write AS that agent — its drafting expertise composing from the evidence
	// blackboard — instead of a generic drafter. Empty → the built-in drafterSystem.
	WriterSystem string
	// Paged enables context-paging synthesis: each section is authored in order, then
	// COMPACTED to a handle so it stops consuming the model's context; later section
	// authors see the compacted handles and can call expand_section to UNCOMPACT any
	// finished section on demand if they need its detail. Final assembly uncompacts
	// everything — lossless. This lets a small-context model produce a deliverable that
	// far exceeds its window. Requires RequiredSections (the section spine).
	Paged bool
}

// SpecificHit is one figure-bearing source passage: the verbatim row (to state
// exactly), its document source, and optional table column context.
type SpecificHit struct {
	Text    string
	Source  string
	Context string
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
	// Collapse near-duplicate findings first. The rounds + sweep + reconciliation can
	// surface the same passage many times; left in, the duplicates both bloat the
	// writer (the merge then compresses the whole document to a stub — a real
	// regression at high finding counts) and litter the deliverable with repetition.
	findings = dedupeFindings(findings)
	ix := NewFindingIndex(w.embed, findings)

	// 1. Build the section set. With a coverage spine (the matter's enumerated topics)
	//    every required category is GUARANTEED a section, findings mapped in top-down;
	//    otherwise fall back to bottom-up clustering + planner naming.
	var secs []section
	if len(w.opt.RequiredSections) > 0 {
		secs = w.spineSections(ix, w.opt.RequiredSections)
	} else {
		secs = w.partition(ix)
		secs = w.planOutline(taskDesc, workflowType, ix, secs)
	}

	// Paged synthesis: the DyTopo writing agent authors each section from the evidence
	// blackboard, compacting finished sections out of working context (uncompactable on
	// demand) and assembling losslessly — no compressing stitch. Lets a small-context
	// model produce a deliverable larger than its window without dropping allegations.
	if w.opt.Paged && len(secs) > 0 {
		return w.writePaged(taskDesc, workflowType, secs, ix), nil
	}

	// 2. One tight agentic drafter per section, search_findings scoped to its set,
	//    figures pulled per section at synthesis.
	drafts := make([]string, len(secs))
	for i, s := range secs {
		drafts[i] = w.draftSection(taskDesc, workflowType, s, ix, draftExtra{})
	}

	// 3. Coverage critic: re-draft any required section that came out thin/empty so a
	//    guaranteed category is never left blank.
	w.repairCoverage(taskDesc, workflowType, secs, drafts, ix)

	// 4. Stitch sections into one coherent document.
	return w.stitch(taskDesc, workflowType, secs, drafts), nil
}

// dedupeFindings collapses near-duplicate findings (same normalized leading ~90
// alphanumerics) to the first occurrence, preserving order. Cheap and order-stable —
// enough to kill the repeated-paragraph problem without an embedding pass.
func dedupeFindings(fs []Finding) []Finding {
	seen := map[string]bool{}
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		key := dedupKey(f.Content)
		if key != "" && seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

func dedupKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 90 {
				break
			}
		}
	}
	return b.String()
}

// spineSections builds one section per required topic (guaranteed coverage) and
// maps each finding to its nearest topic — by embedding cosine when available, else
// keyword overlap. Findings that match no topic are NOT dumped into a flat catch-all:
// they are clustered bottom-up into their own labeled sections (the spine ∪ clustering
// union). So even when the spine under-enumerates an allegation, the findings produced
// for it surface as a real, named section rather than vanishing or being buried in an
// unstructured "Other Findings" bucket.
func (w *Writer) spineSections(ix *FindingIndex, required []string) []section {
	secs := make([]section, len(required))
	for i, t := range required {
		secs[i] = section{Title: t, Brief: t}
	}
	// Precompute topic vectors when an embedder is present.
	var topicVecs [][]float32
	if w.embed != nil {
		topicVecs = make([][]float32, len(required))
		if res, err := w.embed.EmbedBatch(required); err == nil && len(res) == len(required) {
			for i := range res {
				topicVecs[i] = res[i].Embedding
			}
		}
	}
	var orphans []Finding
	for _, f := range ix.All() {
		best, bestScore := -1, 0.0
		if fv := ix.vec(f.ID); len(fv) > 0 && topicVecs != nil {
			for i, tv := range topicVecs {
				if len(tv) == 0 {
					continue
				}
				if s := cosine(fv, tv); s > bestScore {
					best, bestScore = i, s
				}
			}
			if bestScore < 0.25 { // too far from every topic
				best = -1
			}
		} else {
			best = bestKeywordSection(f, required)
		}
		if best < 0 {
			orphans = append(orphans, f)
			continue
		}
		secs[best].FindingIDs = append(secs[best].FindingIDs, f.ID)
	}
	// Union with bottom-up clustering: cluster the off-spine findings into their own
	// labeled sections so nothing extracted is lost and no allegation the spine missed
	// gets dumped unlabelled.
	if len(orphans) > 0 {
		sub := NewFindingIndex(w.embed, orphans)
		clusters := cluster(sub, w.opt.ClusterThreshold, w.opt.MaxClusters)
		if len(clusters) == 0 { // no embeddings: keep everything as one labelled section
			ids := make([]string, 0, len(orphans))
			for _, f := range orphans {
				ids = append(ids, f.ID)
			}
			secs = append(secs, section{Title: "Additional Findings", Brief: "findings not specific to a named category", FindingIDs: ids})
		}
		for _, c := range clusters {
			ids := make([]string, 0, len(c.Items))
			for _, f := range c.Items {
				ids = append(ids, f.ID)
			}
			secs = append(secs, section{Title: c.Label, Brief: "off-spine findings: " + c.Label, FindingIDs: ids})
		}
	}
	return secs
}

// repairCoverage re-drafts any required (non-"Other") section whose draft came out
// thin or empty — a coverage critic ensuring no guaranteed category is left blank.
// Bounded to one repair pass per section.
func (w *Writer) repairCoverage(taskDesc, workflowType string, secs []section, drafts []string, ix *FindingIndex) {
	const thin = 200 // chars; below this a section isn't meaningfully covered
	for i, s := range secs {
		if s.Title == "Other Findings" {
			continue
		}
		if len(strings.TrimSpace(drafts[i])) >= thin {
			continue
		}
		// Re-draft with an explicit mandate + a fresh figure pull for the topic.
		repaired := w.draftSection(taskDesc, workflowType, section{
			Title: s.Title, Brief: s.Brief + " — this category MUST be covered; state its specific allegations and exact figures", FindingIDs: s.FindingIDs,
		}, ix, draftExtra{})
		if len(strings.TrimSpace(repaired)) > len(strings.TrimSpace(drafts[i])) {
			drafts[i] = repaired
		}
	}
}

// cosine is the cosine similarity of two equal-length vectors (0 if degenerate).
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// bestKeywordSection assigns a finding to the required section sharing the most
// content words (the no-embedder fallback). Returns -1 if no overlap.
func bestKeywordSection(f Finding, required []string) int {
	best, bestN := -1, 0
	fl := strings.ToLower(f.Content + " " + f.Evidence)
	for i, t := range required {
		n := 0
		for _, w := range strings.Fields(strings.ToLower(t)) {
			if len(w) >= 4 && strings.Contains(fl, w) {
				n++
			}
		}
		if n > bestN {
			best, bestN = i, n
		}
	}
	return best
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
func (w *Writer) draftSection(taskDesc, workflowType string, s section, ix *FindingIndex, extra draftExtra) string {
	allow := make(map[string]bool, len(s.FindingIDs))
	for _, id := range s.FindingIDs {
		allow[id] = true
	}
	tools := []providers.ToolParam{{
		Name:        "search_findings",
		Description: "Search the findings assigned to YOUR section. Returns each finding's conclusion plus its verbatim evidence and citation source. Only your section's findings are visible.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "What aspect of this section to retrieve"},
			},
			"required": []string{"query"},
		},
	}}
	if extra.board != nil {
		tools = append(tools, providers.ToolParam{
			Name:        "expand_section",
			Description: "Uncompact an already-written section to read its FULL text. The other sections you can see are COMPACTED summaries; call this when you need a finished section's exact wording, figures, or citations — e.g. to avoid repeating it or to stay consistent with it.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{"type": "string", "description": "The exact title of the finished section to expand"},
				},
				"required": []string{"title"},
			},
		})
	}
	if w.opt.Specifics != nil {
		tools = append(tools, providers.ToolParam{
			Name:        "extract_specifics",
			Description: "Pull the EXACT figures for this section from the source exhibits — dollar amounts, percentages, dates, counts, account numbers, statutory citations. Call it whenever your section states a number or precise reference. State the figures exactly as returned, with their source.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{"type": "string", "description": "The specific figures/references this section needs"},
				},
				"required": []string{"topic"},
			},
		})
	}
	system := drafterSystem
	if extra.system != "" { // author AS the chosen DyTopo writing agent
		system = extra.system
	}
	if w.opt.Persona != "" {
		system += "\n\n" + w.opt.Persona
	}

	// Gather the section's figures at synthesis. A single title query is an
	// inadequate retrieval key — a category's specific facts are phrased in their own
	// vocabulary ("Chao profitable allocation rate", "account ending -7823", "omnibus
	// % of volume"), not the category name, so one query leaves them at rank 17+ or
	// off the list entirely. So the critic (planSectionFacts) enumerates the specific
	// facts this category must contain and we run a PRECISE query for each, unioning
	// the figure hits. Pulled from the exhibits, not the finding pile.
	var figHits []SpecificHit
	if w.opt.Specifics != nil {
		seen := map[string]bool{}
		for _, q := range w.planSectionFacts(s, ix) {
			for _, h := range w.opt.Specifics(q, 4) {
				if h.Text == "" || seen[h.Text] {
					continue
				}
				seen[h.Text] = true
				figHits = append(figHits, h)
			}
			if len(figHits) >= 16 { // bounded union across sub-queries
				break
			}
		}
	}
	figuresBlock := ""
	if len(figHits) > 0 {
		var fb strings.Builder
		fb.WriteString("\n\nEXACT FIGURES available for this section (state any you use VERBATIM, with the source in parentheses; call extract_specifics for more):\n")
		for _, h := range figHits {
			if h.Context != "" {
				fmt.Fprintf(&fb, "- %s  [%s] (%s)\n", oneLine(h.Text), h.Context, h.Source)
			} else {
				fmt.Fprintf(&fb, "- %s (%s)\n", oneLine(h.Text), h.Source)
			}
		}
		figuresBlock = fb.String()
	}

	user := fmt.Sprintf(`TASK: %s
WORKFLOW: %s

Write the section "%s" of the final deliverable. Brief: %s

Call search_findings to retrieve the findings for this section, then write it grounded ONLY in what the findings and figures say — never invent facts.
Be COMPREHENSIVE for this category: cover the specific allegations, the parties implicated, the harm, and the defense points.
CRITICAL — for ANY specific figure or precise reference (a dollar amount, a percentage or rate, a count, a date, an account number, or a statutory/section/clause citation), DO NOT write the number or citation yourself. Write a placeholder of the form {{FIG: brief description of which figure you mean}} instead — e.g. "a loss of {{FIG: total alleged loss amount}}", "a rate of {{FIG: the relevant rate or percentage}}", "in violation of {{FIG: the cited statutory provision}}". The exact grounded value is injected automatically, so you never recall a digit — this is how we keep every figure correct. Use a placeholder for EVERY specific you reference. NEVER compute, add, sum, total, or otherwise derive a number yourself — state only figures that appear verbatim in the data; if a total is not given, do not invent one.
If a finding is marked UNVERIFIED, either omit it or caveat it explicitly. Clean client-ready prose: no finding numbers or agent names. Output only the section text (no heading).%s%s`,
		oneLine(taskDesc), workflowType, s.Title, s.Brief, figuresBlock, extra.priorCompacted)

	msgs := []providers.Message{{Role: "user", Content: user}}
	final := ""
	searched := false
	for it := 0; it < w.opt.MaxToolIterations; it++ {
		resp, err := w.prov.Chat(providers.ChatParams{
			Model:       w.model,
			MaxTokens:   w.opt.DraftMaxTokens,
			System:      system,
			Tools:       tools,
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
				var payload interface{}
				switch b.Name {
				case "extract_specifics":
					topic, _ := b.Input["topic"].(string)
					payload = map[string]interface{}{"figures": specificsToJSON(w.opt.Specifics(topic, w.opt.MaxFindingsPerSec))}
				case "expand_section":
					title, _ := b.Input["title"].(string)
					full := "(no finished section by that title)"
					if extra.board != nil {
						if f := extra.board.expand(title); f != "" {
							full = f
						}
					}
					payload = map[string]interface{}{"section": full}
				default: // search_findings
					searched = true
					q, _ := b.Input["query"].(string)
					payload = map[string]interface{}{"findings": findingsToJSON(ix.SearchScoped(q, w.opt.MaxFindingsPerSec, allow))}
				}
				raw, _ := json.Marshal(payload)
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
	result := strings.TrimSpace(final)
	if result == "" {
		result = w.fallbackSection(s, ix) // never blank
	}
	// Mechanically attach the section's grounded figures the drafter didn't already
	// state — from BOTH the per-section figure queries AND this section's mapped
	// findings (which include the at-start swept specifics: $7.8M, 81.6%, account #s,
	// counts). The 7B inconsistently transcribes numbers into prose; the figures are
	// already in hand verbatim, so guarantee they land by construction. This is the
	// figure analogue of locking evidence before analysis in the extraction stage.
	figs := figHits
	for _, id := range s.FindingIDs {
		f, ok := ix.Get(id)
		if !ok {
			continue
		}
		text := f.Evidence
		if salientFigure(text) == "" {
			text = f.Content
		}
		if salientFigure(text) != "" {
			figs = append(figs, SpecificHit{Text: text, Source: f.Source})
		}
	}
	// Neurosymbolic: resolve the drafter's {{FIG: …}} placeholders by injecting the
	// matching grounded figure verbatim — the model never types (and so can't garble,
	// e.g. 68.6% for 81.6%) a number. Unmatched placeholders are dropped, never guessed.
	result = resolveFigurePlaceholders(result, figs)
	return attachKeyFigures(result, figs)
}

var rePlaceholder = regexp.MustCompile(`\{\{\s*FIG:\s*([^}]+?)\s*\}\}`)

// resolveFigurePlaceholders replaces each {{FIG: description}} with the salient
// figure of the grounded row that best matches the description (by content-word
// overlap). No confident match → the placeholder is removed (never guessed), so the
// prose can omit a figure but can never state a wrong one.
func resolveFigurePlaceholders(text string, figs []SpecificHit) string {
	out := rePlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		desc := rePlaceholder.FindStringSubmatch(m)[1]
		best, bestScore := "", 0
		for _, f := range figs {
			if sc := tokenOverlap(desc, f.Text); sc > bestScore {
				bestScore, best = sc, f.Text
			}
		}
		if bestScore >= 2 { // require real overlap before injecting
			if sal := salientFigure(best); sal != "" {
				return sal
			}
		}
		return "" // unmatched → drop, never guess a number
	})
	// Tidy artefacts a dropped placeholder leaves behind: empty/whitespace-only parens
	// (e.g. a citation "()" the drafter emitted with no source), doubled spaces, and a
	// space before punctuation.
	out = regexp.MustCompile(`\(\s*\)`).ReplaceAllString(out, "")
	out = regexp.MustCompile(`\s{2,}`).ReplaceAllString(out, " ")
	return regexp.MustCompile(`\s+([.,;)])`).ReplaceAllString(out, "$1")
}

// tokenOverlap counts shared content words (≥3 chars) between a placeholder
// description and a figure row — a cheap, dependency-free relevance signal.
func tokenOverlap(a, b string) int {
	bw := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(b)) {
		if len(w) >= 3 {
			bw[w] = true
		}
	}
	n := 0
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(a)) {
		if len(w) >= 3 && bw[w] && !seen[w] {
			seen[w] = true
			n++
		}
	}
	return n
}

// planSectionFacts is the critique/planner pass: given a category and a few of its
// findings, it enumerates the SPECIFIC facts the category must contain and returns a
// PRECISE search query for each — a dollar amount, a rate, an account number, a
// count, a date, a statutory cite, a named entity — phrased the way the exhibit
// states them, not as the category name. This replaces the one blunt title query
// (which leaves a category's own figures at rank 17+ or off the list) with several
// targeted ones. Always includes the title; falls back to it alone on failure.
func (w *Writer) planSectionFacts(s section, ix *FindingIndex) []string {
	out := []string{s.Title}
	seen := map[string]bool{strings.ToLower(s.Title): true}
	var ctx strings.Builder
	n := 0
	for _, id := range s.FindingIDs {
		if n >= 4 {
			break
		}
		if f, ok := ix.Get(id); ok && strings.TrimSpace(f.Content) != "" {
			fmt.Fprintf(&ctx, "- %s\n", oneLine(strutil.TruncateToTokens(f.Content, 30)))
			n++
		}
	}
	prompt := fmt.Sprintf(`Category: %s

Findings so far:
%s
Does this category have SPECIFIC figures or references to find in the source exhibits (dollar amounts, percentages/rates, account numbers, trade/record counts, dates, statutory citations, named entities)?
- If NO (it is narrative — e.g. an executive summary or a purely qualitative point), output exactly: NONE
- If YES, list ONLY the queries genuinely needed (usually 2-4), one per line, each targeting ONE such fact and phrased the way an exhibit would state it (NOT the category name). No numbering.`, s.Title, ctx.String())
	text, err := w.complete(plannerSystem, prompt, 300, nil)
	if err != nil || strings.Contains(strings.ToUpper(text), "NONE") {
		return out // necessity-driven: narrative sections get just the one title query
	}
	for _, q := range planLines(text, 6) {
		if key := strings.ToLower(strings.TrimSpace(q)); key != "" && !seen[key] {
			seen[key] = true
			out = append(out, q)
		}
	}
	return out
}

// planLines extracts up to max non-empty lines, stripping bullets/numbering.
func planLines(s string, max int) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-*•0123456789.) \t"))
		if ln != "" && !strings.EqualFold(ln, "none") {
			out = append(out, ln)
			if len(out) >= max {
				break
			}
		}
	}
	return out
}

var reAllFigures = regexp.MustCompile(`\$?\d[\d,]*(?:\.\d+)?%?`)

// salientFigure picks the most meaningful figure in a row for dedup: a $-amount or
// percentage first, else the longest number that ISN'T a bare 4-digit year. Using
// the row's FIRST number was a bug — most exhibit rows lead with a year (e.g.
// "…Oceanic Fund I LP (2021–2023) $7,800,000"), so a narrative mentioning "2021"
// wrongly suppressed the whole row and the $ figure never landed.
var reCiteWord = regexp.MustCompile(`(?i)(section|sections|rule|item|part|subsection|paragraph|no\.|§|§§)\s*$`)

func salientFigure(s string) string {
	best := ""
	for _, loc := range reAllFigures.FindAllStringIndex(s, -1) {
		n := strings.TrimRight(s[loc[0]:loc[1]], ",.")
		if strings.ContainsAny(n, "$%") { // $ amount or percentage is most salient
			return n
		}
		if len(n) == 4 && !strings.Contains(n, ",") { // bare 4-digit year — ignore
			continue
		}
		// Skip numbers that are part of a CITATION, not a figure: "Section 206",
		// "Rule 204-2", "206(1)" — otherwise a statutory ref masquerades as a figure
		// (the "(206)" bug) and pollutes the figure list / placeholder resolution.
		lo := loc[0] - 12
		if lo < 0 {
			lo = 0
		}
		if reCiteWord.MatchString(s[lo:loc[0]]) {
			continue
		}
		if loc[1] < len(s) && (s[loc[1]] == '(' || s[loc[1]] == '-') {
			continue // "206(1)", "204-2" — leading part of a citation
		}
		if loc[0] > 0 && (s[loc[0]-1] == '(' || s[loc[0]-1] == '-') {
			continue // "(1)", "-2" — a subsection/suffix inside a citation
		}
		if len(n) > len(best) {
			best = n
		}
	}
	return best
}

// attachKeyFigures appends a "Key figures" list of the section's retrieved figure
// rows whose SALIENT figure the narrative did NOT already state — so every grounded
// figure lands even when the drafter omitted it. Biased toward inclusion: a row is
// skipped only when its salient $/%/number is already present.
func attachKeyFigures(text string, hits []SpecificHit) string {
	if len(hits) == 0 {
		return text
	}
	var lines []string
	seen := map[string]bool{}
	for _, h := range hits {
		sal := salientFigure(h.Text)
		if sal == "" { // only rows carrying a real figure — never raw narrative dumps
			continue
		}
		if seen[sal] { // dedup BY the figure, not by exact row text
			continue
		}
		seen[sal] = true
		if strings.Contains(text, sal) {
			continue // already stated in the prose
		}
		lines = append(lines, fmt.Sprintf("- %s: %s (%s)", figureLabel(h.Text, sal), sal, h.Source))
		if len(lines) >= 12 { // bounded — a curated list, not a data dump
			break
		}
	}
	if len(lines) == 0 {
		return text
	}
	return text + "\n\n**Key figures:**\n" + strings.Join(lines, "\n")
}

// figureLabel renders a short human label for a figure row: the row text with the
// figure value removed and trimmed to a phrase — so the Key-figures list reads
// "Excess profits to Oceanic Fund: $7,800,000 (src)", not a pasted exhibit row.
func figureLabel(row, sal string) string {
	label := oneLine(row)
	if i := strings.Index(label, sal); i >= 0 {
		label = label[:i] + label[i+len(sal):]
	}
	label = strings.Trim(strings.Join(strings.Fields(label), " "), " -—:|·\t")
	if len(label) > 80 {
		label = strings.TrimSpace(label[:80])
	}
	if label == "" {
		label = "figure"
	}
	return label
}

// specificsToJSON shapes figure hits for an extract_specifics tool result.
func specificsToJSON(hits []SpecificHit) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(hits))
	for _, h := range hits {
		m := map[string]interface{}{"figure": h.Text, "source": h.Source}
		if h.Context != "" {
			m["context"] = h.Context
		}
		out = append(out, m)
	}
	return out
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
	out = strings.TrimSpace(out)
	// Collapse guard: a merge that returns a small fraction of its input has compressed
	// the document to a stub (the failure mode at high finding counts — repeated passes
	// each shrinking it). Reject it so the caller keeps the fuller assembly.
	if len(out) < len(strings.TrimSpace(draft))*2/5 {
		return ""
	}
	return out
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
