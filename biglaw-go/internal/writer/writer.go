// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package writer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

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
	// Facts is the matter's evidence-graph ledger: grounded entity/relation facts
	// (Whitmore holds 12% of Oceanic; Ostrowski is 40% owner of Lakeshore; Crescent Bay is
	// victim of the directed-brokerage scheme). Each section author is given only the facts
	// RELEVANT to its section (matched by overlap with the section's title + its findings),
	// not the whole ledger — lighter context (no crowding on a small window) and sharper
	// attribution. Fixes the mis-attribution and dropped-relation failures of flat findings.
	Facts []Fact
	// FactsGlobal feature-gates the routing: when true, every section author gets the WHOLE
	// ledger (the earlier behaviour) instead of its routed subset. Default false = per-section.
	FactsGlobal bool
	// SectionAliases maps a RequiredSections heading to the alternate surface forms the
	// documents use for that allegation (from the evidence-graph merge). Folded into the
	// section's match text so per-section fact routing catches facts phrased like any variant.
	SectionAliases map[string][]string
	// DyTopoDrafting turns on collaborative section writing: each section is produced by a
	// bounded writing "huddle" (a lead drafter + contributor agents that critique and feed
	// grounded specifics, over a few rounds) instead of a single drafter — DyTopo's
	// Need/Offer collaboration applied to writing. Phase 1 (huddles) runs CONCURRENTLY across
	// sections; Phase 2 is the sequential paged compose/assemble. Off → single-drafter paging.
	DyTopoDrafting bool
	// DraftingAgents are the writing agents' expertise/voice prompts for a huddle: index 0 is
	// the LEAD drafter, the rest are CONTRIBUTORS that critique and offer grounded additions.
	DraftingAgents []string
	// DraftingRounds bounds the huddle: 1 = lead drafts only; 2-3 = draft → critique → revise.
	DraftingRounds int
	// Respondents is the matter's named individual respondents (from the evidence
	// graph's committedBy → Person claims). When set, the writer ENFORCES one exposure
	// entry per respondent: each gets a consolidated grounded record in the exposure
	// section, and a respondent with nothing extracted gets an explicit gap note —
	// a structural hole is surfaced, never silent (the omitted-CEO defect).
	Respondents []string
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
	// factVecs parallels opt.Facts: the embedding of each fact's Key, precomputed once so
	// per-section routing matches facts to sections by cosine (semantic) instead of keyword
	// overlap — catching facts phrased unlike the section heading. nil → keyword fallback.
	factVecs [][]float32
	// groundedCents accumulates every grounded dollar amount (in cents) seen during the
	// write — fact-ledger amounts, finding amounts, and each section's handled figure
	// values. The final total-audit (stripUngroundedTotals) checks any prose "total"
	// against this set, so a model-computed sum ($45,000 + $2,800 presented as a
	// "$47,800 total") can never ship as an asserted aggregate.
	groundedMu    sync.Mutex
	groundedCents map[int64]bool
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
	return &Writer{embed: embed, prov: prov, model: model, opt: opt, groundedCents: map[int64]bool{}}
}

// recordGroundedMoney adds a section's handled figure values to the grounded-money set
// (huddles draft concurrently, hence the lock).
func (w *Writer) recordGroundedMoney(handled []handledFig) {
	w.groundedMu.Lock()
	defer w.groundedMu.Unlock()
	for _, h := range handled {
		for _, m := range reMoney.FindAllString(h.Value+" "+h.Context, -1) {
			if c, ok := parseMoneyCents(m); ok {
				w.groundedCents[c] = true
			}
		}
	}
}

// groundedMoneySet seeds the grounded-money set from the finding pool and fact ledger.
func (w *Writer) seedGroundedMoney(findings []Finding) {
	w.groundedMu.Lock()
	defer w.groundedMu.Unlock()
	add := func(s string) {
		for _, m := range reMoney.FindAllString(s, -1) {
			if c, ok := parseMoneyCents(m); ok {
				w.groundedCents[c] = true
			}
		}
	}
	for _, f := range findings {
		add(f.Content)
		add(f.Evidence)
	}
	for _, f := range w.opt.Facts {
		add(f.Line)
		add(f.Key)
	}
}

// section is one tight, drafter-sized unit: a partition of findings with a title.
type section struct {
	Title      string
	Brief      string
	FindingIDs []string
}

// Fact is one grounded evidence-graph fact for synthesis: Line is the display form shown
// to the author; Key is its lowercased matchable text (subject+relation+object+value+quote)
// used to route the fact to the section(s) it concerns.
type Fact struct {
	Line   string
	Key    string
	Entity string // subject/party — groups the compacted "rest" in paged-facts mode
}

// factsFor selects the evidence-graph facts relevant to a section: a fact is included when
// its Key shares enough salient tokens (len ≥ 4) with the section's match text (title +
// brief + its findings' content). Routes entity facts to the sections that discuss those
// entities, and keeps each author's fact load small (no whole-ledger crowding). Returns
// the rendered block (empty if none match).
func (w *Writer) factsFor(s section, ix *FindingIndex, plan []string) string {
	if len(w.opt.Facts) == 0 {
		return ""
	}
	if w.opt.FactsGlobal { // gate: whole ledger to every author (A/B override)
		lines := make([]string, 0, len(w.opt.Facts))
		for _, f := range w.opt.Facts {
			lines = append(lines, f.Line)
		}
		return factsHeader + strings.Join(lines, "\n")
	}
	// Build the section's match text from title + brief + a sample of its findings.
	var sb strings.Builder
	sb.WriteString(s.Title)
	sb.WriteString(" ")
	sb.WriteString(s.Brief)
	for i, id := range s.FindingIDs {
		if i >= 12 { // bound the match text
			break
		}
		if f, ok := ix.Get(id); ok {
			sb.WriteString(" ")
			sb.WriteString(f.Content)
		}
	}
	secText := sb.String()
	secLower := strings.ToLower(secText)

	const maxPerSection = 20 // paged own-set: deliberate (party + planned + cosine), still << ledger
	var lines []string
	selected := map[int]bool{}
	add := func(i int) {
		if selected[i] || len(lines) >= maxPerSection {
			return
		}
		selected[i] = true
		lines = append(lines, w.opt.Facts[i].Line)
	}

	// (#1 + #4) Entity-aware / proactive party: seat every fact whose party is central to this
	// section (its name appears in the section text) UNCOMPACTED — a weak writer won't pull them
	// via lookup_fact, and routing party facts to a party's own section was starving the
	// allegation sections (Cherry-Picking read "no specific instances cited" while Chao's
	// numbers sat in the floor). Skip ubiquitous entities (the firm) that would drag the whole
	// ledger in. This runs FIRST so the section's parties' facts always make the own-set.
	entFreq := map[string]int{}
	for _, f := range w.opt.Facts {
		entFreq[strings.ToLower(strings.TrimSpace(f.Entity))]++
	}
	ubiqMax := len(w.opt.Facts) * 40 / 100 // an entity in >40% of facts isn't discriminating
	for i, f := range w.opt.Facts {
		e := strings.ToLower(strings.TrimSpace(f.Entity))
		if len(e) >= 4 && entFreq[e] <= ubiqMax && strings.Contains(secLower, e) {
			add(i)
		}
	}

	// (#2) Plan-driven: the per-section critic enumerated the specific facts this section needs
	// (planSectionFacts, computed once by the caller). Pull facts matching those queries into
	// the own-set by keyword overlap — guaranteeing the planned facts aren't left at rank 17+.
	for _, q := range plan {
		qt := salientTokens(q)
		if len(qt) == 0 {
			continue
		}
		for i := range w.opt.Facts {
			if selected[i] {
				continue
			}
			ov := 0
			for tok := range salientTokens(w.opt.Facts[i].Key) {
				if qt[tok] {
					ov++
				}
			}
			if ov >= 2 {
				add(i)
			}
		}
	}

	// Cosine top-K fills any remaining slots with topically-relevant facts (the alias-catcher).
	if len(lines) < maxPerSection && len(w.factVecs) == len(w.opt.Facts) && w.embed != nil {
		if res, err := w.embed.EmbedBatch([]string{secText}); err == nil && len(res) == 1 && len(res[0].Embedding) > 0 {
			sv := res[0].Embedding
			type sc struct {
				i int
				s float64
			}
			var scored []sc
			for i, fv := range w.factVecs {
				if selected[i] || len(fv) == 0 {
					continue
				}
				if s := cosine(fv, sv); s >= 0.25 {
					scored = append(scored, sc{i, s})
				}
			}
			sort.SliceStable(scored, func(a, b int) bool { return scored[a].s > scored[b].s })
			for _, x := range scored {
				add(x.i)
				if len(lines) >= maxPerSection {
					break
				}
			}
		}
	}

	// Keyword fallback (no embedder) if nothing matched.
	if len(lines) == 0 {
		secTokens := salientTokens(secText)
		for i := range w.opt.Facts {
			ov := 0
			for tok := range salientTokens(w.opt.Facts[i].Key) {
				if secTokens[tok] {
					ov++
				}
			}
			if ov >= 2 {
				add(i)
			}
			if len(lines) >= maxPerSection {
				break
			}
		}
	}

	// Paged facts: this section's own facts UNCOMPACTED, every other fact COMPACTED to a
	// per-entity handle and expandable on demand. Mirrors the section paging.
	var out strings.Builder
	if len(lines) > 0 {
		out.WriteString(factsHeader)
		out.WriteString(strings.Join(lines, "\n"))
	}
	if rest := w.compactRestFacts(selected); rest != "" {
		out.WriteString(rest)
	}
	return out.String()
}

// compactRestFacts compresses every fact NOT routed to this section into per-entity handles
// (party + count) instead of dumping them all — so the drafter isn't drowned, yet still knows
// what else is on file and can pull any of it in full via extract_specifics.
func (w *Writer) compactRestFacts(selected map[int]bool) string {
	counts := map[string]int{}
	var order []string
	for i, f := range w.opt.Facts {
		if selected[i] {
			continue
		}
		e := strings.TrimSpace(f.Entity)
		if e == "" {
			e = "(unattributed)"
		}
		if counts[e] == 0 {
			order = append(order, e)
		}
		counts[e]++
	}
	if len(order) == 0 {
		return ""
	}
	sort.SliceStable(order, func(a, b int) bool { return counts[order[a]] > counts[order[b]] })
	var hs []string
	for i, e := range order {
		if i >= 20 { // cap the handle list itself so it can't become its own flood
			hs = append(hs, fmt.Sprintf("- …and %d more parties/topics", len(order)-20))
			break
		}
		hs = append(hs, fmt.Sprintf("- %s: %d more figure(s) on file", e, counts[e]))
	}
	return "\n\nOTHER FIGURES ON FILE (compacted — these belong to OTHER sections; pull one in full only if this section genuinely needs it, via extract_specifics with the party/topic):\n" + strings.Join(hs, "\n")
}

const factsHeader = "\n\nGROUNDED FACTS relevant to this section (each is verbatim-sourced). State the ones that belong HERE exactly, with correct attribution — do NOT attach a fact to the wrong allegation:\n"

// lookupFacts answers the lookup_fact tool: ranks the WHOLE fact ledger by salient-token
// overlap with the query and returns the top-k display lines. The recall escape hatch for
// per-section routing — any author can pull any grounded fact on demand.
func (w *Writer) lookupFacts(query string, k int) []string {
	q := salientTokens(query)
	if len(q) == 0 {
		return nil
	}
	type sc struct {
		line string
		n    int
	}
	var scored []sc
	for _, f := range w.opt.Facts {
		n := 0
		for tok := range salientTokens(f.Key) {
			if q[tok] {
				n++
			}
		}
		if n > 0 {
			scored = append(scored, sc{f.Line, n})
		}
	}
	sort.SliceStable(scored, func(a, b int) bool { return scored[a].n > scored[b].n })
	out := make([]string, 0, k)
	for i := 0; i < len(scored) && i < k; i++ {
		out = append(out, scored[i].line)
	}
	return out
}

// salientTokens returns the set of lowercased word tokens of length ≥ 4 (skips short
// connectives), for cheap overlap matching.
func salientTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if t := b.String(); len(t) >= 4 {
			out[t] = true
		}
	}
	return out
}

// Write produces the final deliverable. It never returns empty when findings exist:
// every model call has a deterministic fallback (the findings' own conclusions), so
// a flaky local model degrades to a plain grounded summary rather than a blank.
func (w *Writer) Write(taskDesc, workflowType string, findings []Finding) (string, error) {
	if len(findings) == 0 {
		return "", nil
	}
	// Structurally exclude process-language conclusions BEFORE anything can render them:
	// a finding whose Content is a placeholder or extraction to-do ("Evidence on point for
	// this matter; see the quoted source.", "These must be extracted from…") is replaced by
	// its verbatim evidence — substance, not stage direction — so no path (drafter tool
	// result, fallback, figure list) can emit the tell.
	for i := range findings {
		if isProcessConclusion(findings[i].Content) {
			if e := strings.TrimSpace(findings[i].Evidence); e != "" && !isProcessConclusion(e) {
				findings[i].Content = e
			}
		}
	}
	// Collapse near-duplicate findings first. The rounds + sweep + reconciliation can
	// surface the same passage many times; left in, the duplicates both bloat the
	// writer (the merge then compresses the whole document to a stub — a real
	// regression at high finding counts) and litter the deliverable with repetition.
	findings = dedupeFindings(findings)
	w.seedGroundedMoney(findings)
	ix := NewFindingIndex(w.embed, findings)

	// Precompute fact embeddings once for semantic per-section routing (the "fancier math":
	// cosine dot-product, not keyword overlap). Only when routing per-section.
	if !w.opt.FactsGlobal && w.embed != nil && len(w.opt.Facts) > 0 {
		keys := make([]string, len(w.opt.Facts))
		for i, f := range w.opt.Facts {
			keys[i] = f.Key
		}
		if res, err := w.embed.EmbedBatch(keys); err == nil && len(res) == len(keys) {
			w.factVecs = make([][]float32, len(res))
			for i := range res {
				w.factVecs[i] = res[i].Embedding
			}
		}
	}

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
		return w.auditTotals(w.writePaged(taskDesc, workflowType, secs, ix)), nil
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

	// 4. Stitch sections into one coherent document, then the document-level QA:
	//    duplicate-block suppression and the per-respondent exposure guarantee.
	out := dedupeDocBlocks(w.stitch(taskDesc, workflowType, secs, drafts))
	if rb := w.rosterBlock(); rb != "" && !strings.Contains(out, rosterHeader) {
		out = strings.TrimRight(out, "\n") + "\n\n## Individual Exposure\n\n" + rb
	}
	if strings.TrimSpace(out) == "" {
		out = w.emergencyDoc(findings) // never empty when findings exist
	}
	return w.auditTotals(out), nil
}

// auditTotals runs the document-level total audit against the accumulated grounded-money
// set (facts + findings + every section's handled figures).
func (w *Writer) auditTotals(doc string) string {
	w.groundedMu.Lock()
	grounded := make(map[int64]bool, len(w.groundedCents))
	for k := range w.groundedCents {
		grounded[k] = true
	}
	w.groundedMu.Unlock()
	return stripUngroundedTotals(doc, grounded)
}

// emergencyDoc is the last-resort floor: every substantive path failed, so render the
// findings' verbatim evidence (or substantive conclusions) as grounded paragraphs.
// It exists so the never-empty guarantee survives the process-language filters.
func (w *Writer) emergencyDoc(findings []Finding) string {
	var sents []string
	seen := map[string]bool{}
	for _, f := range findings {
		c := oneLine(f.Content)
		if c == "" || isProcessConclusion(c) {
			c = oneLine(f.Evidence)
		}
		if c == "" || isProcessConclusion(c) {
			continue
		}
		if k := dedupKey(c); k == "" || seen[k] {
			continue
		} else {
			seen[k] = true
		}
		if !endsSentence(c) {
			c += "."
		}
		sents = append(sents, c)
	}
	return strings.TrimSpace(strings.Join(sents, " "))
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
		brief := t
		// Fold the allegation's alternate surface forms into the brief so fact routing
		// (factsFor) and the drafter both see every phrasing the docs use, while the
		// heading (Title) stays the clean canonical.
		if al := w.opt.SectionAliases[t]; len(al) > 0 {
			var extra []string
			for _, a := range al {
				if !strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(t)) {
					extra = append(extra, a)
				}
			}
			if len(extra) > 0 {
				brief = t + " — also referred to as: " + strings.Join(extra, "; ")
			}
		}
		secs[i] = section{Title: t, Brief: brief}
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
	// Inter-section pull: in per-section routing mode a section is given only its routed
	// facts; lookup_fact lets the author REQUEST any grounded fact from the WHOLE matter on
	// demand, so a fact relevant here but routed elsewhere is never starved (the recall fix
	// for per-section routing). Not offered in global mode (the author already has all facts).
	if len(w.opt.Facts) > 0 && !w.opt.FactsGlobal {
		tools = append(tools, providers.ToolParam{
			Name:        "lookup_fact",
			Description: "Search ALL of the matter's grounded facts (every party, relationship, ownership %, amount, role across every allegation), not just this section's. Use it when you need a fact about a party or entity that wasn't handed to you — e.g. to state a person's exposure or a cross-allegation link.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{"type": "string", "description": "The party, entity, or fact you need (e.g. 'Ostrowski ownership', 'Whitmore exposure')"},
				},
				"required": []string{"query"},
			},
		})
	}
	// The clean section-part drafter is ALWAYS the base. The chosen writing agent contributes
	// its EXPERTISE/voice (extra.system), appended — NOT its whole-document template. (Using
	// an agent's verbatim system prompt per section crammed its document structure — IRAC,
	// "exec summary → findings → recommendations" — into every section, producing 16 stacked
	// templates. The genre belongs at the document level, not the section level.)
	system := drafterSystem
	if extra.system != "" {
		system += "\n\n" + extra.system
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
	plan := w.planSectionFacts(s, ix) // computed once: drives both the figures pull and fact routing
	var figHits []SpecificHit
	if w.opt.Specifics != nil {
		seen := map[string]bool{}
		for _, q := range plan {
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
	// What3Words for figures: unify the planned section figures with the section's own
	// findings' salient figures, assign each a neutral handle, and show the drafter the figure
	// IN CONTEXT with its digit MASKED by the handle — so it learns what each name means without
	// ever reading the number (can't garble it, no attention skew). The exact value is
	// substituted by handle after drafting.
	pool := figHits
	for _, id := range s.FindingIDs {
		if f, ok := ix.Get(id); ok {
			t := f.Evidence
			if salientFigure(t) == "" {
				t = f.Content
			}
			if salientFigure(t) != "" {
				pool = append(pool, SpecificHit{Text: t, Source: f.Source})
			}
		}
	}
	handled := assignHandles(pool)
	// Diagnostic: which figures actually reached THIS section's handle list. Lets us tell a
	// routing-into-handles gap (a key figure never enters the list) from a drafter-skip (it's
	// listed but the prose ignores it).
	if len(handled) > 0 {
		vals := make([]string, 0, len(handled))
		for _, h := range handled {
			vals = append(vals, h.Value)
		}
		slog.Info("section handled figures", "section", s.Title, "n", len(handled), "values", strings.Join(vals, " | "))
	}
	figuresBlock := ""
	if len(handled) > 0 {
		var fb strings.Builder
		fb.WriteString("\n\nFIGURES for this section — each has a NAME. To state a figure, write its NAME (capitalised, exactly as shown) where the figure belongs; the precise value is substituted automatically. NEVER write the number/percentage/date/citation yourself:\n")
		for _, h := range handled {
			masked := maskValue(oneLine(h.Context), h.Value, h.Handle)
			fmt.Fprintf(&fb, "  %s — \"%s\" (%s)\n", h.Handle, masked, h.Source)
		}
		figuresBlock = fb.String()
	}

	// Grounded relation facts from the evidence graph — only those relevant to THIS
	// section, routed by entity/allegation overlap (no whole-ledger crowding).
	factsBlock := w.factsFor(s, ix, plan)

	user := fmt.Sprintf(`TASK: %s
WORKFLOW: %s

Write the section "%s" of the final deliverable. Brief: %s

Call search_findings to retrieve the findings for this section, then write it grounded ONLY in what the findings and figures say — never invent facts.
Be COMPREHENSIVE for this category: cover the specific allegations, the parties implicated, the harm, and the defense points.
CRITICAL — for ANY specific figure or precise reference (a dollar amount, a percentage or rate, a count, a date, an account number, or a statutory/section/clause citation), DO NOT write the number or citation yourself. Instead write the NAME of the matching figure from the FIGURES list above (e.g. "the scheme generated Zephyr in excess profits across Quasar trades") — write the name exactly, capitalised. The exact grounded value is substituted for the name automatically, so you never recall a digit — this is how we keep every figure correct. Use a name for EVERY specific you reference, and use ONLY names from the list (if you need a figure that isn't listed, call extract_specifics). NEVER compute, add, sum, total, or otherwise derive a number yourself.
Where the findings support it, develop the section's prose in this order: the governing statute, rule, or standard; the alleged conduct; the quantities and amounts involved; the parties implicated; and the resulting exposure. Carry statutory and internal-section identifiers exactly as given — via their FIGURES names where listed — never shortened or paraphrased (write "Section 9.1", never "Section 9").
If a finding is marked UNVERIFIED, either omit it or caveat it explicitly.
Write FLOWING, professional client-ready prose — connected paragraphs, not an outline. Every sentence must be complete; never end mid-phrase. Do NOT emit internal labels or scaffolding such as "Issue:", "Brief Answer:", "Stronger View", "Counter-Argument", "Open Questions", "Recommendations:", "Analysis:". Do NOT write any commentary about your own process or about the findings/inputs — never write things like "Since there are no findings…", "I will write…", "Based on the provided grounded facts…", "As an AI". No finding numbers or agent names. Output only the section's prose (no heading).%s%s%s`,
		oneLine(taskDesc), workflowType, s.Title, s.Brief, figuresBlock, factsBlock, extra.priorCompacted)

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
				case "lookup_fact":
					q, _ := b.Input["query"].(string)
					payload = map[string]interface{}{"facts": w.lookupFacts(q, 8)}
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
	// A refusal / role-clarification response is not a draft at all — a weak model
	// arguing with its inputs ("I need to clarify my role here…") must never ship as
	// section content. Discard it wholesale and render the grounded fallback instead.
	if result == "" || isRefusalDraft(result) {
		result = w.fallbackSection(s, ix) // never blank
	}
	// Mechanically attach the section's grounded figures the drafter didn't already
	// state — from BOTH the per-section figure queries AND this section's mapped
	// findings (which include the at-start swept specifics: $7.8M, 81.6%, account #s,
	// counts). The 7B inconsistently transcribes numbers into prose; the figures are
	// already in hand verbatim, so guarantee they land by construction. This is the
	// figure analogue of locking evidence before analysis in the extraction stage.
	// Substitute each figure handle with its exact grounded value — deterministic (exact key,
	// no fuzzy desc-matching). The model never typed a digit, so it can't garble one; a handle
	// it mangled or skipped simply doesn't substitute, so a figure can be omitted but never
	// stated wrong.
	result = resolveFigureHandles(result, handled)
	// Assertion: handle names can never appear in output. Every HANDLED name was just
	// substituted globally; scrub any UNMAPPED pool name the drafter hallucinated
	// (outside quoted spans — quotes are never edited) and log what remains.
	result = scrubUnresolvedHandles(result, handled)
	w.recordGroundedMoney(handled)
	result = sanitizeDraft(result)
	// Authorship QA: no orphan fragments, no pasted ledger runs (tables instead), no
	// process-tell sentences, no duplicate leading heading, never ends mid-sentence.
	result = polishSection(s.Title, result)
	if result == "" {
		result = w.fallbackSection(s, ix)
	}
	// Guarantee the SALIENT figures land. Ranking gets most figures inline, but a weak drafter
	// still drops some of the headline ones (it used the top figure and reverted to qualitative
	// prose). attachKeyFigures appends ONLY salient figures (big money / large counts / major
	// rates) that the prose did NOT already state — a tight factual tail, not a data dump, and
	// model-independent so figure coverage no longer rides on drafter mood.
	var salient []SpecificHit
	for _, h := range handled {
		if figureSalience(h.Value) >= salienceGuaranteeFloor {
			salient = append(salient, SpecificHit{Text: h.Context, Source: h.Source})
		}
	}
	return attachKeyFigures(result, salient)
}

var (
	// reMetaLine flags whole lines of agent process-chatter: planning monologue ("Let me
	// extract the specific figures…", "Now I have the necessary information to write…"),
	// role/meta commentary, and self-referential framing. First-person planning verbs are
	// matched generically — the partner-review leaks were exactly the verbs ("extract",
	// "compose") an enumerated list had missed.
	reMetaLine  = regexp.MustCompile(`(?i)(since there (are|were) no\b|based on the (provided|extracted|grounded)\b|as an ai\b|as requested\b|^\s*i'?ll?\s+\w+\b|^\s*i\s+will\s+(now\s+)?\w+\b|^\s*here is (the|my|a)\b|^\s*below is (the|my|a)\b|^\s*\[?note:\s|^\s*let me\b|\blet me (now\s+)?(write|draft|search|extract|compose|pull|gather|retrieve)\b|^\s*now (that\s+)?i have\b|now i have (comprehensive|sufficient)\b|i now have (comprehensive|sufficient)\b|^\s*i appreciate\b|^\s*i need to clarify\b|\bclarify my role\b|^\s*i can(not| not)? (draft|write)\b|^\s*i can instead\b|^\s*please confirm which\b|^\s*you'?ve asked me\b|^\s*document prepared as\b)`)
	reLeadLabel = regexp.MustCompile(`(?i)^\s*#*\s*(stronger view|credible counter-?argument|counter-?argument|open questions?|brief answer|issue(\(s\))?(\s+\d+)?)\s*[:.\-]*\s*`)
	reBlankRun  = regexp.MustCompile(`\n{3,}`)
	reHrLine    = regexp.MustCompile(`^\s*[-—_*]{3,}\s*$`)
)

// reRefusalTell marks a drafter response that is a REFUSAL or role-clarification rather
// than a draft — the model arguing with its instructions. One hard marker condemns the
// whole response: refusals are structured multi-paragraph dialogue (numbered options,
// "please confirm…"), so line-level stripping cannot salvage them.
var reRefusalTell = regexp.MustCompile(`(?i)(clarify my role|i cannot (draft|write) (a|the|this) section|i can instead draft|you'?ve asked me to|i must decline|i'?m unable to (help|draft|write)|i appreciate the [^.\n]{0,50}(correction|clarification|feedback)|please confirm which approach)`)

// isRefusalDraft reports whether a drafter/reviser response is meta-dialogue about the
// task (a refusal, a role clarification, a request for confirmation) instead of content.
func isRefusalDraft(s string) bool {
	return reRefusalTell.MatchString(s)
}

// sanitizeDraft strips the machine tells a human immediately flags: leaked process
// commentary ("Since there are no findings…", "I will write…") and internal deliberation
// labels (Stronger View / Counter-Argument / Open Questions / Brief Answer / Issue) that
// make the output read like a stitched template instead of a client deliverable. Conservative
// — it drops meta lines and label prefixes, never substantive prose.
func sanitizeDraft(s string) string {
	var keep []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) == "" {
			keep = append(keep, ln)
			continue
		}
		if reMetaLine.MatchString(ln) { // a whole meta-commentary line
			continue
		}
		if reHrLine.MatchString(ln) { // a bare "---" separator is scaffolding, not prose
			continue
		}
		stripped := reLeadLabel.ReplaceAllString(ln, "") // remove a leading deliberation label
		if strings.TrimSpace(stripped) == "" {           // line was only a label
			continue
		}
		keep = append(keep, stripped)
	}
	return strings.TrimSpace(reBlankRun.ReplaceAllString(strings.Join(keep, "\n"), "\n\n"))
}

// figureHandles is the pool of neutral, inert codenames — "What3Words for figures". Each
// section figure is assigned one; the drafter drops the NAME into prose where the figure goes
// and the exact grounded value is substituted by exact key. Names are LLM-native (unlike the
// {{FIG:…}} meta-placeholder the model resisted, producing vague figureless prose) and
// semantically inert (a descriptive placeholder skews attention; an arbitrary name does not).
// Chosen distinctive and absent from legal prose so a word-boundary substitution can't collide.
var figureHandles = []string{
	"Zephyr", "Quasar", "Nimbus", "Cobalt", "Halcyon", "Obsidian", "Peregrine", "Calliope",
	"Verdigris", "Marlowe", "Thessaly", "Caspian", "Larkspur", "Onyx", "Sable", "Indigo",
	"Cinnabar", "Perdita", "Aurelian", "Bramble", "Citrine", "Dovetail", "Ravenna", "Tindal",
}

type handledFig struct {
	Handle, Value, Context, Source string
}

// ─── Quote-span protection: verbatim quotes are inviolable ──────────────────────

// quotedSpans returns the [start,end) byte ranges of quoted text in s (straight and
// curly quotes). An unclosed quote extends to the end of the string — protective:
// when in doubt, treat text as quoted so machinery never edits inside a quote.
func quotedSpans(s string) [][2]int {
	var spans [][2]int
	open := -1
	for i, r := range s {
		switch r {
		case '"':
			if open < 0 {
				open = i
			} else {
				spans = append(spans, [2]int{open, i + 1})
				open = -1
			}
		case '“': // “
			if open < 0 {
				open = i
			}
		case '”': // ”
			if open >= 0 {
				spans = append(spans, [2]int{open, i + len("”")})
				open = -1
			}
		}
	}
	if open >= 0 {
		spans = append(spans, [2]int{open, len(s)})
	}
	return spans
}

func inSpans(spans [][2]int, start, end int) bool {
	for _, sp := range spans {
		if start < sp[1] && end > sp[0] {
			return true
		}
	}
	return false
}

// maskValue replaces value with handle in a context row shown to the drafter — the
// value→handle direction of the What3Words scheme. Two hard rules fix the partner-review
// corruption ("Q1 2021" → "QCalliope 202Calliope"):
//  1. word-boundary only: a value that is a substring of a larger token ("1" inside
//     "Q1"/"2021") is never touched;
//  2. quoted spans are inviolable: text inside quotation marks is source verbatim and is
//     never masked, so a drafter copying the quote copies the original characters.
func maskValue(text, value, handle string) string {
	if value == "" {
		return text
	}
	spans := quotedSpans(text)
	var b strings.Builder
	i := 0
	for i < len(text) {
		j := strings.Index(text[i:], value)
		if j < 0 {
			b.WriteString(text[i:])
			break
		}
		j += i
		end := j + len(value)
		prevOK := j == 0 || !isWordByte(text[j-1])
		nextOK := end >= len(text) || !isWordByte(text[end])
		if prevOK && nextOK && !inSpans(spans, j, end) {
			b.WriteString(text[i:j])
			b.WriteString(handle)
		} else {
			b.WriteString(text[i:end])
		}
		i = end
	}
	return b.String()
}

// scrubUnresolvedHandles enforces the output-side assertion: no handle name may ship.
// resolveFigureHandles already substituted every MAPPED handle globally; what can remain
// is a pool name the drafter hallucinated without a mapping. Those are removed at word
// boundaries — outside quoted spans only (quotes are never edited) — and anything left
// is logged so a leak is visible in ops, never silent.
func scrubUnresolvedHandles(text string, handled []handledFig) string {
	mapped := make(map[string]bool, len(handled))
	for _, h := range handled {
		mapped[strings.ToLower(h.Handle)] = true
	}
	changed := false
	for _, name := range figureHandles {
		if mapped[strings.ToLower(name)] {
			continue // mapped names were substituted by resolveFigureHandles
		}
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(name) + `\b`)
		locs := re.FindAllStringIndex(text, -1)
		if len(locs) == 0 {
			continue
		}
		spans := quotedSpans(text)
		var b strings.Builder
		prev := 0
		for _, loc := range locs {
			b.WriteString(text[prev:loc[0]])
			if inSpans(spans, loc[0], loc[1]) {
				b.WriteString(text[loc[0]:loc[1]]) // quotes are inviolable — leave, but log
				slog.Warn("unmapped figure handle inside a quoted span left untouched", "handle", name)
			} else {
				changed = true
				slog.Warn("scrubbed unmapped figure handle from section output", "handle", name)
			}
			prev = loc[1]
		}
		b.WriteString(text[prev:])
		text = b.String()
	}
	if changed {
		text = reEmptyParens.ReplaceAllString(text, "")
		text = reSpaceRun.ReplaceAllString(text, " ")
		text = reSpaceBefPunct.ReplaceAllString(text, "$1")
	}
	return text
}

// assignHandles maps a neutral handle to each DISTINCT salient figure (deduped by value,
// capped to the pool size). The mapping is shown to the drafter (digit masked) and substituted
// by the resolver.
// maxSectionFigures caps the handle list. Exhibit tables flood a section with small numbers
// (sample sizes, per-account rates); a noise-heavy list buries the headline figures so the
// drafter uses none. Ranking by salience + a cap keeps the harm figures at the top.
const maxSectionFigures = 14

func assignHandles(hits []SpecificHit) []handledFig {
	type cand struct {
		sal   string
		hit   SpecificHit
		score int
	}
	var cands []cand
	seen := map[string]bool{}
	for _, h := range hits {
		sal := salientFigure(h.Text)
		score := 0
		if sal != "" {
			score = figureSalience(sal)
		}
		// A bare 1-2 digit number is a fragment, not a figure — a day-of-month ("18" out
		// of "October 18, 2024"), a quarter digit, a footnote number. Handling one made
		// the drafter treat the handle as the WHOLE date and drop the month ("On 18,
		// 2024" / "commenced on 11, 2024"). Never a handle value.
		if isBareShortNumber(sal) {
			sal, score = "", 0
		}
		if sal == "" {
			if c := salientCite(h.Text); c != "" {
				// Section-number carriage: the harvest surfaces "Section 9.1"/"Item 6"/
				// "Rule 204A-1"-style identifiers as first-class values. Give them handles
				// too, so the drafter carries them into prose INTACT (it is forbidden from
				// typing citations itself — without a handle it would paraphrase to
				// "Section 9" or drop the reference).
				sal, score = c, citeSalience
			} else if d := salientDate(h.Text); d != "" {
				// Date carriage: the FULL date ("October 18, 2024") is the value, so the
				// substituted prose always carries the month — never a day fragment.
				sal, score = d, dateSalience
			}
		}
		if sal == "" || seen[strings.ToLower(sal)] {
			continue
		}
		seen[strings.ToLower(sal)] = true
		cands = append(cands, cand{sal, h, score})
	}
	// Rank by salience so headline figures ($8.2M, 4,217, 81.6%) lead the list and the noise
	// tail (small bare numbers from exhibit rows) falls off the cap, not the salient ones.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []handledFig
	for _, c := range cands {
		if len(out) >= maxSectionFigures || len(out) >= len(figureHandles) {
			break
		}
		out = append(out, handledFig{Handle: figureHandles[len(out)], Value: c.sal, Context: c.hit.Text, Source: c.hit.Source})
	}
	return out
}

// isBareShortNumber reports whether a candidate value is a bare 1-2 digit number with no
// unit marker — the fragment class ("18", "1") that must never become a handle value.
func isBareShortNumber(s string) bool {
	if s == "" || len(s) > 2 || strings.ContainsAny(s, "$%,./") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// dateSalience ranks a full-date handle value: above minor percentages but below the
// salience-guarantee floor, so dates are carried via handles yet never appended to the
// mechanical "Key figures" tail.
const dateSalience = 50

// reSalientDate extracts a FULL date — month-name day-year, month-name year, or a
// numeric m/d/y form — so the handle value is the complete date, never a day fragment.
var reSalientDate = regexp.MustCompile(`(?i)\b(?:january|february|march|april|may|june|july|august|september|october|november|december)\s+(?:\d{1,2},?\s+)?(?:19|20)\d\d\b|\b\d{1,2}/\d{1,2}/\d{2,4}\b`)

// salientDate returns the first full date in a row, or "" when none.
func salientDate(s string) string {
	return reSalientDate.FindString(s)
}

// figureSalience scores how "headline" a figure is. Big money and large counts rank highest
// (the quantified harm a legal memo turns on); rates next; the small bare numbers exhibit tables
// are full of rank lowest, so they fall off the cap before a salient figure does.
func figureSalience(v string) int {
	if !strings.ContainsAny(v, "$%") && reSalientDate.MatchString(v) {
		return dateSalience // a date value is carried, but is never a "Key figure"
	}
	lv := strings.ToLower(v)
	clean := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || r == '.' {
			return r
		}
		return -1
	}, strings.ReplaceAll(v, ",", ""))
	x, _ := strconv.ParseFloat(clean, 64)
	dollar := strings.Contains(v, "$")
	pct := strings.Contains(v, "%")
	bigWord := strings.Contains(lv, "million") || strings.Contains(lv, "billion")
	switch {
	case dollar && (bigWord || x >= 100000):
		return 100 // big money — the headline loss/profit/penalty
	case strings.Contains(v, ",") && !dollar && x >= 1000:
		return 95 // large count, e.g. 4,217 trades
	case dollar && x >= 10000:
		return 90
	case dollar && x >= 1000:
		return 70
	case pct && x >= 40:
		return 60 // a major rate (81.6%, 62%) — the kind a memo turns on
	case pct:
		return 45 // a minor percentage (1.5%, 5%) — usually exhibit detail
	case x >= 1000:
		return 40
	case dollar:
		return 30 // small dollar amount
	default:
		return 10 // bare small number (likely exhibit noise)
	}
}

// salienceGuaranteeFloor is the cutoff above which a figure is guaranteed to land — big money,
// large counts, and major rates — so the mechanical key-figures tail catches the salient
// stragglers a weak drafter dropped without dragging in minor percentages or small numbers.
const salienceGuaranteeFloor = 55

// citeSalience ranks a citation identifier in the handle list: above minor percentages
// (an operative "Rule 204A-1" outranks a 1.5% exhibit detail) but below the headline
// money/counts/rates. Note assignHandles scores cites with this constant, while the
// Key-figures guarantee tail keys on figureSalience(Value) — which stays low for a cite,
// so citations never leak into the "Key figures" block (TestAttachKeyFiguresSelective).
const citeSalience = 58

// reSalientCite extracts a FULL citation identifier — the keyword and its complete
// number, subsections included: "Section 9.1", "Item 6", "Rule 204A-1", "Rule 206(4)-7",
// "§ 275.204A-1". Used to hand citations to the drafter as handles carried verbatim.
var reSalientCite = regexp.MustCompile(`(?i)\b(?:Sections?|Rule|Item|Part|Article|Clause|Paragraph|Exhibit|§§?)\s*[0-9](?:[0-9A-Za-z.\-]|\([0-9a-zA-Z]+\))*`)

// salientCite returns the first full citation identifier in a row (trailing sentence
// punctuation trimmed), or "" when the row carries none.
func salientCite(s string) string {
	m := reSalientCite.FindString(s)
	return strings.TrimRight(m, ".-")
}

// Substitution-tidy patterns, hoisted (and scoped): cleanup must never eat paragraph
// breaks, so runs collapse within a line only ([ \t], not \s — \s{2,} matched "\n\n"
// and flattened the whole section into one wall of text).
var (
	reEmptyParens   = regexp.MustCompile(`\(\s*\)`)
	reSpaceRun      = regexp.MustCompile(`[ \t]{2,}`)
	reSpaceBefPunct = regexp.MustCompile(`[ \t]+([.,;)])`)
)

// resolveFigureHandles substitutes each handle (case-insensitive, word-boundary) with its exact
// grounded value, then strips any stray {{FIG:…}} the drafter emitted out of habit and tidies
// the artefacts a substitution can leave (empty parens, doubled spaces, space-before-punct).
//
// The substitution is LITERAL (ReplaceAllLiteralString): a grounded value like "$7,800,000"
// contains "$7", which ReplaceAllString would expand as a (nonexistent) capture-group
// reference — eating the "$7" and corrupting the figure to ",800,000" (the
// "approximately,800,000" bug). Values are data, never replacement templates.
func resolveFigureHandles(text string, handled []handledFig) string {
	for _, h := range handled {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(h.Handle) + `\b`)
		text = re.ReplaceAllLiteralString(text, h.Value)
	}
	text = rePlaceholder.ReplaceAllString(text, "")
	text = reEmptyParens.ReplaceAllString(text, "")
	text = reSpaceRun.ReplaceAllString(text, " ")
	return reSpaceBefPunct.ReplaceAllString(text, "$1")
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
	// space before punctuation. Within-line only — paragraph breaks are sacred.
	out = reEmptyParens.ReplaceAllString(out, "")
	out = reSpaceRun.ReplaceAllString(out, " ")
	return reSpaceBefPunct.ReplaceAllString(out, "$1")
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
		// Skip a bare paragraph/list/sentence number — "22. The Division…", "14) " — where
		// the number is immediately followed by a "." or ")" then whitespace/end. These are
		// not figures; surfacing them as "Key figures" is the noise a human flags. (Amounts
		// like "$7,800,000" return earlier via the $ check; decimals like "22.2" have a digit
		// after the dot, so they are not caught here.)
		if loc[1] < len(s) && (s[loc[1]] == '.' || s[loc[1]] == ')') {
			if a := loc[1] + 1; a >= len(s) || s[a] == ' ' || s[a] == '\n' || s[a] == '\t' {
				continue
			}
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
var reLeadEnum = regexp.MustCompile(`^\s*[\(\[]?\d+[.)\]]\s*`) // "24. " / "25) " / "(3) " paragraph numbers

func figureLabel(row, sal string) string {
	label := oneLine(row)
	if i := strings.Index(label, sal); i >= 0 {
		label = label[:i] + label[i+len(sal):]
	}
	label = reLeadEnum.ReplaceAllString(label, "") // drop a leading paragraph/list number
	label = strings.Trim(strings.Join(strings.Fields(label), " "), " -—:|·,\t")
	if len(label) > 64 { // cut on a word boundary, not mid-phrase
		cut := label[:64]
		if k := strings.LastIndex(cut, " "); k > 20 {
			cut = cut[:k]
		}
		label = strings.TrimRight(cut, " -—:|·,")
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

// fallbackSection renders a section from its findings when the drafter model returns
// nothing — as AUTHORED content, not a case-file dump: substantive conclusions become
// flowing sentences grouped into paragraphs, ledger/exhibit rows become a table, and
// process-language conclusions ("must be extracted from…", "Evidence on point…") are
// structurally excluded — they can never reach the client. Returns "" when nothing
// substantive remains (the section is then omitted, never placeholder-filled).
func (w *Writer) fallbackSection(s section, ix *FindingIndex) string {
	var sents []string
	var ledger []string
	seen := map[string]bool{}
	for _, id := range s.FindingIDs {
		f, ok := ix.Get(id)
		if !ok {
			continue
		}
		c := oneLine(f.Content)
		if c == "" || isProcessConclusion(c) {
			// The conclusion is a placeholder/process line — fall back to the finding's
			// verbatim evidence, which is always substantive, or skip entirely.
			if e := oneLine(f.Evidence); e != "" && !isProcessConclusion(e) {
				c = e
			} else {
				continue
			}
		}
		if k := dedupKey(c); k == "" || seen[k] {
			continue
		} else {
			seen[k] = true
		}
		if isLedgerLine(c) {
			ledger = append(ledger, c)
			continue
		}
		if !endsSentence(c) {
			c += "."
		}
		if !f.Grounded {
			c = strings.TrimSuffix(c, ".") + " (unverified — requires confirmation)."
		}
		sents = append(sents, c)
	}
	var blocks []string
	// Compose the sentences into paragraphs of a few sentences each — prose, not bullets.
	const perPara = 4
	for i := 0; i < len(sents); i += perPara {
		end := i + perPara
		if end > len(sents) {
			end = len(sents)
		}
		blocks = append(blocks, strings.Join(sents[i:end], " "))
	}
	if len(ledger) >= 3 {
		blocks = append(blocks, strings.Join(collapseLedgerRuns(ledger), "\n"))
	} else if len(ledger) > 0 {
		// Too few rows for a table — summarize in prose instead of pasting fragments.
		blocks = append(blocks, "The grounded record also shows: "+strings.Join(ledger, "; ")+".")
	}
	return strings.TrimSpace(strings.Join(blocks, "\n\n"))
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
	drafterSystem = "You draft ONE section of a client deliverable in flowing, professional prose. Ground every statement in the findings retrieved via search_findings; never invent facts, figures, or citations. Write only this section's substance as connected paragraphs — do NOT add document-level structure (no executive summary, no overall conclusion) and do NOT emit internal labels or scaffolding such as 'Issue:', 'Rule:', 'Brief Answer:', 'Applicable Law:', 'Analysis:', 'Stronger View', 'Counter-Argument', 'Open Questions', 'Conclusion:'. No meta-commentary about your process or the inputs."
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
