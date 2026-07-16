// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type matterMode string

const (
	modeCompliance  matterMode = "compliance"  // Requirement issues → deviation detection (compare/review/compliance)
	modeEnforcement matterMode = "enforcement" // Conduct issues → allegation spine + defense-issue analytics
)

// classifyMatter routes by predicate DOMINANCE over BELO's typed claims, not by the presence of
// any single predicate — one stray accusation triple the model extracted from a comparison matter
// must not flip the whole routing. It DEFAULTS to compliance: reviewing a document against a
// standard is the general case; enforcement is the specialization that must earn its framing by
// accusation predicates (violates/committedBy/harmed) outweighing requirement predicates
// (requires/satisfiedBy/deviatesFrom/prohibits).
func (o *Orchestrator) routeMatter(g *evidencegraph.Graph) matterMode {
	enf, comp := matterClaimCounts(g)
	mode := modeCompliance
	if enf > comp {
		mode = modeEnforcement
	}
	slog.Info("matter routing", "enforcement_claims", enf, "compliance_claims", comp, "mode", mode)
	return mode
}

// matterClaimCounts tallies the enforcement (accusation) vs compliance (requirement)
// predicates over BELO's typed claims — the raw signal routeMatter and the deviation
// gate both classify on.
func matterClaimCounts(g *evidencegraph.Graph) (enf, comp int) {
	if g == nil {
		return 0, 0
	}
	for _, c := range g.Claims() {
		switch c.P {
		case "violates", "committedBy", "harmed":
			enf++
		case "requires", "satisfiedBy", "deviatesFrom", "prohibits":
			comp++
		}
	}
	return enf, comp
}

const (
	// deviationMarginPct / deviationMarginFloor define the margin band over which the
	// deviation pass runs even when enforcement predicates lead. The band absorbs the
	// run-to-run routing wobble (57-45 one run, 52-59 the next on the SAME submission)
	// that silently added/removed the pass and swung 3-5 rubric criteria.
	deviationMarginPct   = 25
	deviationMarginFloor = 15
)

// shouldRunDeviationPass decides whether to run the draft-vs-controlling deviation
// detection. It is NON-EXCLUSIVE: the pass is not gated to a decisive compliance
// classification. It runs whenever the matter is compliance-leaning OR the routing is
// merely borderline (enforcement's lead is within the margin band) — so a borderline
// matter deterministically gets its deviations instead of flipping with claim counts.
// A DECISIVELY enforcement matter (lead beyond the band) keeps pure enforcement
// behavior and skips the pass.
func (o *Orchestrator) shouldRunDeviationPass(g *evidencegraph.Graph) bool {
	enf, comp := matterClaimCounts(g)
	return deviationGateOpen(enf, comp)
}

// deviationGateOpen is the pure gate decision (testable without a graph): open when
// compliance predicates are at least tied, or enforcement's lead falls within the
// margin band where both modes' passes overlap.
func deviationGateOpen(enf, comp int) bool {
	if enf <= comp {
		return true // compliance-leaning (or tied) — the default framing runs the pass
	}
	band := (enf + comp) * deviationMarginPct / 100
	if band < deviationMarginFloor {
		band = deviationMarginFloor
	}
	return enf-comp <= band
}

// crossCuttingSections are the party/timeline-oriented sections a legal enforcement memo carries
// ALONGSIDE the matter-specific allegation categories. The rubric rewards them (per-person
// exposure, the examination timeline, parties and ownership stakes); the clean conduct-only BELO
// spine dropped them, costing cross-cutting criteria the messier enumeration spine had captured.
var crossCuttingSections = []string{
	"Parties, Entities, and Ownership Interests",
	"Individuals at Risk and Personal Exposure",
	"Key Dates and Examination Timeline",
}

func (o *Orchestrator) ensureAllegations(task *types.Task, prov providers.Provider, model string) []string {
	if len(task.Allegations) > 0 {
		return task.Allegations
	}
	// BELO spine: derive the allegations from the evidence graph's typed Conduct nodes
	// (DISCOVERED via conduct-domain predicates), consolidated into distinct categories. This
	// replaces the noisy, run-varying LLM enumeration over all-docs retrieval (which grabbed
	// Form-ADV review-triggers and dropped real allegations — the ±10 spine wobble). Falls back
	// to the enumeration if disabled or the graph is too sparse.
	if o.cfg.BELOSpine {
		if g := o.evidenceGraph(task.ID); g != nil {
			conducts := g.Conducts()
			slog.Info("BELO spine decision", "task", task.ID, "flag", o.cfg.BELOSpine, "conducts", len(conducts))
			if len(conducts) >= 2 {
				cats := o.consolidateConducts(task, prov, model, conducts)
				slog.Info("BELO spine consolidate", "task", task.ID, "conducts", len(conducts), "categories", len(cats))
				if len(cats) >= 2 {
					// Enforcement matters also need the CROSS-CUTTING sections the rubric rewards
					// (per-person exposure, timeline, parties/ownership). These are enforcement-
					// framed, so add them ONLY for enforcement matters — a compliance/compare
					// matter has Requirement issues and would read oddly with "Individuals at Risk".
					if o.routeMatter(g) == modeEnforcement {
						cats = append(cats, crossCuttingSections...)
					}
					o.update(task, func(t *types.Task) { t.Allegations = cats })
					slog.Info("BELO spine from conduct nodes", "task", task.ID, "conducts", len(conducts), "categories", len(cats))
					return cats
				}
			}
		}
	}
	// Seed the holistic synthesis with the evidence graph's grounded allegation candidates
	// (recall floor), so the category-level spine never misses a secondary allegation the
	// graph caught — without inheriting the graph's fine-grained altitude.
	var seed []string
	if g := o.evidenceGraph(task.ID); g != nil {
		seed = g.Allegations()
	}
	_, allegations := o.allegationContext(task, prov, model, seed)
	allegations = dedupAllegations(allegations)
	// Coverage-net (coverAllegationClusters) is DISABLED — it regressed the weak model twice
	// (7B 23→17 under FACTS_GLOBAL, 27→20 under paged facts). The mechanism is fact-routing
	// fragmentation: its extra granular sections out-score the broad category sections on
	// cosine and STEAL facts into thin, poorly-drafted fragments. More sections is the wrong
	// lever for a weak writer; mechanical per-section/party rendering is the way. Helper kept.
	if len(allegations) > 0 {
		o.update(task, func(t *types.Task) { t.Allegations = allegations })
	}
	return allegations
}

// consolidateConducts turns the evidence graph's typed Conduct nodes into the matter's distinct
// allegation-category headings: it merges restatements of the same charge (e.g. "Obstruction of
// Examination" / "Obstructive Conduct During Examination") and drops non-allegation conducts.
// Robust because it consolidates a small, already-grounded set — unlike enumerating from noisy
// all-docs retrieval. Falls back to the deduped raw conducts on any model/parse failure.
func (o *Orchestrator) consolidateConducts(task *types.Task, prov providers.Provider, model string, conducts []string) []string {
	prompt := "These are ISSUE nodes extracted from a legal matter's evidence graph — the distinct propositions the deliverable must assess (alleged violations, client requirements/instructions, or contract clauses, depending on the matter). Merge restatements of the SAME issue into one heading, drop anything that is not a distinct issue to assess, and output the DISTINCT issue/section headings in the matter's own terms — one heading per line, no numbering, no preamble.\n\nISSUE NODES:\n- " + strings.Join(conducts, "\n- ")
	// FIX 3 — deterministic merging: temperature 0 (not LLMTemperature 0.2). Consolidating the
	// conduct nodes into spine categories is a stable, set-merge task; any wobble here reshuffles
	// the matter's section spine run-to-run, which is the variance we're driving out.
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   600,
		System:      "You consolidate extracted conduct nodes into a legal matter's distinct allegation categories — clean section headings, nothing else.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return dedupAllegations(conducts)
	}
	o.recordCost(resp, model, cost.ContextSynthesis, task.ID)
	var text string
	for _, bl := range resp.Content {
		if bl.Type == providers.BlockText {
			text = bl.Text
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-*•0123456789.) \t"))
		ln = strings.TrimSpace(strings.Trim(ln, "*_#:"))
		if n := len(ln); n < 4 || n > 90 {
			continue
		}
		if k := strings.ToLower(ln); !seen[k] {
			seen[k] = true
			out = append(out, ln)
		}
		if len(out) >= maxSpineSections {
			break
		}
	}
	if len(out) < 2 {
		return dedupAllegations(conducts)
	}
	return out
}

// coverAllegationClusters guarantees every grounded allegation cluster is represented in the
// spine. It clusters the graph's grounded candidates and appends the representative of any
// cluster not already covered (by embedding similarity) by the LLM enumeration. When the
// enumeration is empty (very weak model), this degrades to a pure grounded-cluster spine —
// still covering every allegation the graph caught. Generalizable: no rubric, no hardcoding.
func (o *Orchestrator) coverAllegationClusters(have, candidates []string) []string {
	if len(candidates) == 0 || o.embedC == nil {
		return have
	}
	reps, _ := o.clusterAllegations(candidates)
	if len(reps) == 0 {
		return have
	}
	all := append(append([]string{}, have...), reps...)
	res, err := o.embedC.EmbedBatch(all)
	if err != nil || len(res) != len(all) {
		return have
	}
	haveVecs, repVecs := res[:len(have)], res[len(have):]
	const coveredThreshold = 0.72 // short legal headings via nomic: same-topic, not identical
	out := append([]string{}, have...)
	for i, rep := range reps {
		rv := repVecs[i].Embedding
		if len(rv) == 0 {
			continue
		}
		covered := false
		for _, hv := range haveVecs {
			if len(hv.Embedding) > 0 && embeddings.CosineSimilarity(rv, hv.Embedding) >= coveredThreshold {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, rep) // a grounded allegation the enumeration missed
		}
	}
	out = dedupAllegations(out)
	if len(out) > maxSpineSections { // keep enumeration headings first, then the recovered gaps
		out = out[:maxSpineSections]
	}
	return out
}

// dedupAllegations collapses headings that name the same allegation under different
// category numbers/prefixes (e.g. "Allegation Category 1 — Cherry-Picking" and
// "Category 5 — Cherry-Picking"): it strips leading category/number/roman prefixes and
// keys on the remaining content words, keeping the first (richest-ordered) occurrence.
func dedupAllegations(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, h := range in {
		if themeKey(h) == "" || seen[themeKey(h)] {
			continue
		}
		seen[themeKey(h)] = true
		out = append(out, h)
	}
	return out
}

var reAllegPrefix = regexp.MustCompile(`(?i)^\s*(allegation\s+)?(category|count|claim|issue|item|no\.?|number|section|part)\s*[ivxlcdm0-9]+\s*[-–—:.)]*\s*`)

// themeKey reduces a heading to its content-word signature for dedup: drop category/
// number prefixes, lowercase, keep alphanumerics, sort the words (so order/phrasing
// differences collapse).
func themeKey(h string) string {
	h = reAllegPrefix.ReplaceAllString(strings.TrimSpace(h), "")
	var words []string
	for _, w := range strings.Fields(strings.ToLower(h)) {
		var b strings.Builder
		for _, r := range w {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		if s := b.String(); len(s) >= 4 { // skip short connectives (and, the, of)
			words = append(words, s)
		}
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// draftingAgentVoices selects the writing-agent bench for synthesis and returns each as an
// EXPERTISE/voice line (name + description) — NOT the agent's whole-document template, which
// crammed IRAC / "exec-summary→findings→recommendations" into every section. The lead is
// index 0; contributors follow. Selected by semantic fit to the deliverable so the right
// agents (report/summary/exec, not a fixed memo agent) are seated; the writer appends these
// over its clean section-part base.
func (o *Orchestrator) draftingAgentVoices(task *types.Task) []string {
	n := o.cfg.Drafting.AgentsPerSection
	if n < 1 {
		n = 2
	}
	voice := func(a *types.AgentDefinition) string {
		return "Bring the perspective of the " + a.Name + ": " + strings.Join(strings.Fields(a.Description), " ")
	}
	var voices []string
	seen := map[string]bool{}
	query := strings.Join(strings.Fields(task.Description), " ")
	if cands, err := o.registry.Search(query, agents.SearchOpts{TopK: 50}); err == nil {
		for i := range cands {
			a := cands[i]
			if a.Domain != types.DomainDrafting || seen[a.ID] || strings.TrimSpace(a.Description) == "" {
				continue
			}
			seen[a.ID] = true
			voices = append(voices, voice(&a))
			if len(voices) >= n {
				break
			}
		}
	}
	// Ensure a lead + at least one contributor from known general drafters.
	for _, id := range []string{"due-diligence-report-drafter", "executive-summary-drafter", "legal-research-memo-drafter"} {
		if len(voices) >= n || len(voices) >= 2 && len(voices) >= n {
			break
		}
		if seen[id] {
			continue
		}
		if a := o.registry.GetByID(id); a != nil && strings.TrimSpace(a.Description) != "" {
			seen[id] = true
			voices = append(voices, voice(a))
		}
	}
	return voices
}

// writingAgentSystem is the single-drafter (non-DyTopo) path's voice: the lead drafting
// agent's expertise line, appended over the writer's clean section base.
func (o *Orchestrator) writingAgentSystem(task *types.Task) string {
	if v := o.draftingAgentVoices(task); len(v) > 0 {
		return v[0]
	}
	return ""
}

// extractCoverageSpine returns the matter's enumerated allegations as the writer's
// required sections — the SAME shared set recruitment staffed — so every allegation a
// specialist analysed has a guaranteed section. Returns nil when nothing enumerable is
// found (the writer then falls back to clustering).
func (o *Orchestrator) extractCoverageSpine(task *types.Task, prov providers.Provider, model string) []string {
	allegations := o.ensureAllegations(task, prov, model)
	if len(allegations) < 2 {
		return nil // not a usable spine; writer falls back to clustering
	}
	slog.Info("coverage spine extracted", "task", task.ID, "sections", len(allegations))
	return allegations
}
