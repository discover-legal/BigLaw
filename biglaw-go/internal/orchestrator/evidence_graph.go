// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/ontology"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/discover-legal/biglaw-go/internal/writer"
)

func (o *Orchestrator) buildEvidenceGraph(task *types.Task, prov providers.Provider, model string) {
	passages := o.allegationPassages(task, 6000)
	if strings.TrimSpace(passages) == "" {
		return
	}
	g := evidencegraph.New()
	kept, rej := 0, 0
	chunks := chunkByTokens(passages, 1500)
	// Phase 1 — entity/relation/allegation extraction on the bulk model (7B) over all chunks.
	for _, chunk := range chunks {
		k, r := evidencegraph.ExtractInto(g, prov, model, o.cfg.LLMTemperature, chunk, "")
		kept += k
		rej += r
	}
	// Phase 2 — the typed conduct/spine pass, optionally on a STRONGER model (BELO_SPINE_MODEL,
	// e.g. qwen2.5:14b). Conducts are document-level abstractions the 7B mislabels; a capable
	// model populates the Conduct nodes cleanly. Run as a separate phase so the GPU swaps models
	// once (not per chunk). Falls back to the bulk model/provider when SpineModel is unset.
	spineModel, spineProv := model, prov
	if sm := o.localize(o.cfg.Models.SpineModel); sm != "" {
		// sm is a routing model ID (e.g. "local:qwen2.5:14b"); Get() routes by its prefix, but
		// the PROVIDER call needs the bare model name (the prefix is stripped) — otherwise the
		// endpoint gets "local:qwen2.5:14b" and fails. Resolve to bare for the call; only switch
		// providers if Get succeeds (else keep the bulk provider).
		if p, err := o.provReg.Get(sm); err == nil {
			spineModel, spineProv = routing.ResolveModelID(sm), p
		} else {
			slog.Warn("BELO spine model provider unavailable; using bulk model", "spine_model", sm, "err", err)
		}
	}
	// FIX 1 — the conduct/spine pass sweeps the FULL text of every charging document, NOT the
	// allegationPassages semantic subset Phase 1 uses. That subset (4 queries × top_k 8, truncated
	// to 6000 tokens) structurally misses any category whose chunk doesn't rank (e.g. Books-&-
	// Records), so the spine silently dropped it. Reading every doc's full text guarantees every
	// category is seen; AddTriple dedups inside the graph, so overlapping sweeps are safe. Mirrors
	// the harvestAndBindFigures full-doc idiom (GetFullText, title via GetByID, 40k-token cap,
	// chunkByTokens). Falls back to the Phase-1 `chunks` if no document yields usable full text.
	// FIX 2 — run the conduct pass at temperature 0 (deterministic copy-out): the prior 0.2 caused
	// run-to-run wobble on what is fundamentally a transcribe-and-classify task.
	// Allegations live in the CHARGING document, not the exhibits. Sweeping all docs' full text
	// on the (stronger, slower) spine model is both wasteful and slow enough to stall the run, so
	// PAGE through the charging doc(s) — ranked by allegation-language density — up to a token
	// budget, the same bounded-paging discipline used elsewhere. The 7B figure sweep already
	// covers the exhibits. Falls back to the Phase-1 chunks if no document yields usable text.
	const spineTokenBudget = 20000 // ~ the charging doc; bounds the expensive spine pass to a few calls
	zero := 0.0
	spineChunks := o.chargingDocChunks(task, spineTokenBudget)
	if len(spineChunks) == 0 { // no usable doc text → degrade gracefully to the allegation passages
		spineChunks = chunks
	}
	ckept, crej := 0, 0
	for _, chunk := range spineChunks {
		k, r := evidencegraph.ExtractTriplesInto(g, spineProv, spineModel, &zero, chunk, "")
		ckept += k
		crej += r
	}
	// Zero-yield guard (the July-3 Haiku trigger regression): a spine pass that produces NOTHING
	// — not even rejected rows — means every call failed or returned unparseable output (chatJSON
	// swallows provider errors; on that run BELO_SPINE_MODEL resolved through localize() to a
	// "local:" ID whose endpoint was a dead placeholder). Without typed triples the graph carries
	// no subsection-level `violates` edges and the analytic defense layer starves. Retry once on
	// the bulk provider/model, which Phase 1 just used successfully.
	if ckept+crej == 0 && len(spineChunks) > 0 && (spineProv != prov || spineModel != model) {
		slog.Warn("BELO spine pass yielded zero triples; retrying on the bulk provider", "task", task.ID, "spine_model", spineModel)
		for _, chunk := range spineChunks {
			k, r := evidencegraph.ExtractTriplesInto(g, prov, model, &zero, chunk, "")
			ckept += k
			crej += r
		}
	}
	if ckept+crej == 0 {
		slog.Warn("BELO spine pass produced no typed triples — spine falls back to enumeration; defense issues derive from the charging documents directly", "task", task.ID, "spine_model", spineModel)
	}
	if g.Len() == 0 {
		return
	}
	o.egraphsMu.Lock()
	o.egraphs[task.ID] = g
	o.egraphsMu.Unlock()
	// Deterministic figure floor: harvest every $/%/date/account#/citation from the docs and
	// BIND each to the graph nodes it co-occurs with, then seed the figure-bearing sentences
	// as grounded findings. Removes the run-to-run figure variance (LLM-query-driven sweep
	// missed $7.8M/$438K some runs) and makes figures ride their node into synthesis.
	// Figure-extraction model: the user-picked small model (settings/env), falling back to
	// the tool model. A 7B-class model at temp 0 is enough for deterministic copy-out
	// extraction — keeps the pipeline efficient (the heavy model is not needed here).
	figModel := strings.TrimSpace(o.cfg.Models.FigureModel)
	if figModel == "" {
		figModel = model
	}
	// The harvest's second return value is its raw normalized figureHit records —
	// the seam detectCrossDocDiscrepancies notes: feed them to crossDocFindings and
	// crossdoc's own duplicate full-corpus sweep can be dropped (follow-up wiring).
	// On compliance matters the raw records feed the deviation pass's mechanical
	// numeric join (deviationNumericJoin) below.
	figs, rawFigs := o.harvestAndBindFigures(task, g, prov, figModel)
	if len(figs) > 0 {
		o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, figs...) })
		slog.Info("figure harvest seeded findings", "task", task.ID, "n", len(figs), "model", figModel, "graph_facts_after", g.Len())
	}
	// Stash the raw harvest (canonical quantity labels already assigned) for the
	// round-boundary cross-document RE-JOIN (reentry.go): when a round grows the graph's
	// alias/entity knowledge, the discrepancy pass re-runs over these records without
	// repeating the full-corpus LLM sweep.
	if o.cfg.ReentrantMachinery && len(rawFigs) > 0 {
		o.reentryStateFor(task.ID).rawFigs = rawFigs
	}
	// Cross-document discrepancy pass (crossdoc.go): same metric identity (entity +
	// quantity-kind + referent) reported with different values in different documents,
	// plus event-date conflicts (metadata vs narrative). Augments the intra-harvest
	// contradiction dimension above.
	if xd := o.detectCrossDocDiscrepancies(task, g, prov, figModel); len(xd) > 0 {
		o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, xd...) })
	}
	// Stage 2 — for a COMPLIANCE (compare/review) matter, DETECT where the document DEVIATES from
	// the controlling standard per requirement. This is the finding such tasks are scored on
	// ("residuary should be 40/35/25, draft has …"), not a description of each requirement. Runs
	// on the spine model. Enforcement matters use the figure-discrepancy path instead.
	if o.shouldRunDeviationPass(g) {
		if devs := o.detectDeviations(task, g, spineProv, spineModel, rawFigs, prov, figModel); len(devs) > 0 {
			o.update(task, func(t *types.Task) { t.Findings = append(t.Findings, devs...) })
			slog.Info("deviations detected", "task", task.ID, "n", len(devs))
		}
	}
	// The graph's per-chunk allegation extraction (g.Allegations()) is a grounded RECALL
	// floor — fine-grained sub-issues, the wrong altitude for section headings on their own.
	// We deliberately do NOT make the spine from them directly (medoid-of-cluster headings
	// scored 20: fragmented, lost whole rubric categories). Instead ensureAllegations feeds
	// them as a SEED into the holistic, category-level synthesis (which scored 26), getting
	// graph recall at the right altitude. So nothing to set here beyond the graph itself.
	slog.Info("evidence graph built", "task", task.ID, "facts", g.Len(), "kept", kept, "grounding_rejected", rej,
		"allegation_candidates", len(g.Allegations()), "conducts", len(g.Conducts()),
		"spine_model", spineModel, "conduct_triples_kept", ckept, "conduct_triples_rejected", crej)
}

// clusterAllegations merges near-duplicate candidate allegation headings into nodes by
// embedding cosine: each cluster keeps ALL its surface forms (aliases — alternate phrasings
// the docs use, which are routing/retrieval keys) and a canonical heading (the medoid). The
// number of semantically-distinct clusters is the natural section count; a safety cap still
// applies. Deterministic given the embeddings (no LLM variance). Returns (canonicals,
// canonical→aliases). Degrades to a hard cap when no embedder is available.
func (o *Orchestrator) clusterAllegations(raw []string) ([]string, map[string][]string) {
	if len(raw) == 0 {
		return nil, nil
	}
	hardCap := func() ([]string, map[string][]string) {
		c := raw
		if len(c) > maxSpineSections {
			c = c[:maxSpineSections]
		}
		al := make(map[string][]string, len(c))
		for _, s := range c {
			al[s] = []string{s}
		}
		return c, al
	}
	if o.embedC == nil || len(raw) <= 2 {
		return hardCap()
	}
	res, err := o.embedC.EmbedBatch(raw)
	if err != nil || len(res) != len(raw) {
		return hardCap()
	}
	vecs := make([][]float32, len(raw))
	for i := range res {
		vecs[i] = res[i].Embedding
	}
	const mergeThreshold = 0.80 // short headings: high enough to keep distinct allegations apart
	type cluster struct {
		idxs     []int
		centroid []float32
	}
	var clusters []*cluster
	for i, v := range vecs {
		if len(v) == 0 {
			clusters = append(clusters, &cluster{idxs: []int{i}})
			continue
		}
		best, bestSim := -1, mergeThreshold
		for ci, c := range clusters {
			if len(c.centroid) == 0 {
				continue
			}
			if s := embeddings.CosineSimilarity(v, c.centroid); s >= bestSim {
				best, bestSim = ci, s
			}
		}
		if best < 0 {
			clusters = append(clusters, &cluster{idxs: []int{i}, centroid: append([]float32(nil), v...)})
			continue
		}
		c := clusters[best]
		c.idxs = append(c.idxs, i)
		n := float32(len(c.idxs))
		for k := range c.centroid {
			if k < len(v) {
				c.centroid[k] += (v[k] - c.centroid[k]) / n
			}
		}
	}
	canon := make([]string, 0, len(clusters))
	aliases := make(map[string][]string, len(clusters))
	for _, c := range clusters {
		members := make([]string, 0, len(c.idxs))
		for _, idx := range c.idxs {
			members = append(members, raw[idx])
		}
		rep := medoid(c.idxs, vecs, raw)
		canon = append(canon, rep)
		aliases[rep] = members
	}
	if len(canon) > maxSpineSections { // largest-first so the cap keeps the best-attested
		sort.SliceStable(canon, func(i, j int) bool { return len(aliases[canon[i]]) > len(aliases[canon[j]]) })
		for _, c := range canon[maxSpineSections:] {
			delete(aliases, c)
		}
		canon = canon[:maxSpineSections]
	}
	return canon, aliases
}

// medoid returns the cluster member whose embedding is most central (highest summed cosine
// to the others) — the most representative heading. Falls back to the first member.
func medoid(idxs []int, vecs [][]float32, labels []string) string {
	if len(idxs) == 1 {
		return labels[idxs[0]]
	}
	best, bestScore := idxs[0], -1.0
	for _, a := range idxs {
		if len(vecs[a]) == 0 {
			continue
		}
		sum := 0.0
		for _, b := range idxs {
			if a != b && len(vecs[b]) > 0 {
				sum += embeddings.CosineSimilarity(vecs[a], vecs[b])
			}
		}
		if sum > bestScore {
			best, bestScore = a, sum
		}
	}
	return labels[best]
}

// maxSpineSections caps the coverage spine: more than this and synthesis (one paged
// drafter per section on a local model) runs too long; the matter's real distinct
// allegations comfortably fit.
const maxSpineSections = 12

// evidenceGraph returns the task's evidence graph, or nil if none was built.
func (o *Orchestrator) evidenceGraph(taskID string) *evidencegraph.Graph {
	o.egraphsMu.Lock()
	defer o.egraphsMu.Unlock()
	return o.egraphs[taskID]
}

// allegationAliases returns the canonical→surface-form alias map for the task's spine
// sections, so the writer can route facts using every phrasing the docs use, not just the
// canonical heading.
func (o *Orchestrator) allegationAliases(taskID string) map[string][]string {
	o.egraphsMu.Lock()
	defer o.egraphsMu.Unlock()
	return o.egraphAliases[taskID]
}

// respondentRoster returns the matter's named individual respondents — the Person-class
// parties the typed evidence graph records as having committed conduct (committedBy →
// Person). Name variants sharing a surname collapse to the fullest form, so "Whitmore"
// and "Gerald R. Whitmore" yield one roster entry. The writer enforces one exposure
// entry per name returned here.
func (o *Orchestrator) respondentRoster(taskID string) []string {
	g := o.evidenceGraph(taskID)
	if g == nil {
		return nil
	}
	bySurname := map[string]string{}
	var order []string
	for _, c := range g.Claims() {
		if c.P != "committedBy" || c.OClass != ontology.Person {
			continue
		}
		n := strings.TrimSpace(c.O)
		if n == "" || len(n) > 60 {
			continue
		}
		fields := strings.Fields(n)
		surname := strings.ToLower(strings.Trim(fields[len(fields)-1], ".,"))
		if surname == "" {
			continue
		}
		if have, ok := bySurname[surname]; !ok {
			bySurname[surname] = n
			order = append(order, surname)
		} else if len(n) > len(have) {
			bySurname[surname] = n // keep the fullest name variant
		}
	}
	const maxRespondents = 8
	out := make([]string, 0, len(order))
	for _, s := range order {
		out = append(out, bySurname[s])
		if len(out) >= maxRespondents {
			break
		}
	}
	return out
}

// groundedFacts converts the task's evidence-graph facts into the writer's per-section
// routable form: each fact carries its display Line and a lowercased Key (subject +
// relation + object + value + quote) the writer overlap-matches against each section.
func (o *Orchestrator) groundedFacts(taskID string) []writer.Fact {
	g := o.evidenceGraph(taskID)
	if g == nil || g.Len() == 0 {
		return nil
	}
	all := g.All()
	out := make([]writer.Fact, 0, len(all))
	for _, f := range all {
		line := strings.TrimSpace(evidencegraph.Render([]evidencegraph.Fact{f}))
		key := strings.ToLower(strings.Join([]string{f.Subject, f.Relation, f.Object, f.Value, f.Quote}, " "))
		out = append(out, writer.Fact{Line: line, Key: key, Entity: f.Subject})
	}
	return out
}

// chunkByTokens splits line-oriented text into windows of at most maxTok estimated tokens,
// never splitting a line (snippets stay intact for grounded extraction).
func chunkByTokens(text string, maxTok int) []string {
	var chunks, cur []string
	tok := 0
	for _, ln := range strings.Split(text, "\n") {
		lt := strutil.EstimateTokens(ln)
		if tok+lt > maxTok && len(cur) > 0 {
			chunks = append(chunks, strings.Join(cur, "\n"))
			cur, tok = nil, 0
		}
		cur = append(cur, ln)
		tok += lt
	}
	if len(cur) > 0 {
		chunks = append(chunks, strings.Join(cur, "\n"))
	}
	return chunks
}

// chunkKey normalizes a passage to its leading ~120 alphanumerics for dedup across the
// overlapping retrieval queries (different queries surface the same chunk).
func chunkKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			if b.Len() >= 120 {
				break
			}
		}
	}
	return b.String()
}

// allegationContext returns the merged allegation passages (for downstream figure
// hunting) and the matter's distinct allegations as CATEGORY-LEVEL section headings —
// a holistic synthesis over the passages, recall-checked against the evidence graph's
// grounded candidates (seed) so no secondary allegation is missed. Category altitude is
// explicit: the per-chunk graph extraction produces fine-grained sub-issues/per-person
// questions, which made bad section headings ("Whether Chao was responsible…") and lost
// whole rubric categories; the holistic synthesis at category altitude is what scored 26.
// Document-grounded, never rubric-derived. allegations is nil when nothing is found.
func (o *Orchestrator) allegationContext(task *types.Task, prov providers.Provider, model string, seed []string) (string, []string) {
	passages := o.allegationPassages(task, 5000)
	if strings.TrimSpace(passages) == "" {
		return "", nil
	}
	seedBlock := ""
	if len(seed) > 0 {
		// Recall floor: the graph already extracted these grounded candidates; ensure each
		// distinct one is represented so the synthesis doesn't drop a secondary allegation the
		// graph caught — without collapsing distinct allegations together.
		seedBlock = "\n\nThese grounded allegation candidates were extracted from the same documents — make sure each genuinely distinct one is represented among the headings (do not omit a secondary allegation):\n- " + strings.Join(seed, "\n- ")
	}
	prompt := fmt.Sprintf("TASK: %s\n\nFrom the passages below, list EVERY DISTINCT allegation, claim, charge, scheme, or required topic this matter raises, as short section headings — be EXHAUSTIVE: include secondary and party-specific allegations, not only the most prominent. Prefer the document's own enumeration where it numbers or names them (e.g. a numbered allegation category, a count, a claim). Name the allegation or topic itself, not a procedural sub-question (write \"Cherry-Picking Trade Allocations\", not \"Whether X was responsible\"). One heading per line, no numbering, no preamble. Use the matter's own terms; only topics actually present.%s\n\nPASSAGES:\n%s",
		strings.Join(strings.Fields(task.Description), " "), seedBlock, passages)
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   800,
		System:      "You extract a legal matter's full set of distinct allegations as a clean, exhaustive list of section headings, nothing else.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		return passages, nil
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
		ln = strings.Trim(ln, "*_#:")
		ln = strings.TrimSpace(ln)
		if n := len(ln); n < 4 || n > 90 {
			continue
		}
		key := strings.ToLower(ln)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ln)
		if len(out) >= 16 { // safety cap (matters can enumerate many distinct allegations)
			break
		}
	}
	return passages, out
}

// ensureAllegations enumerates the matter's distinct allegations ONCE and caches them on
// the task, so recruitment (synthesizeAgents) and the writer's coverage spine
// (extractCoverageSpine) staff and write the SAME set. Rolling two independent
// enumerations at temperature>0 let them diverge — recruitment recruited a Bellini
// specialist while the spine missed Bellini and duplicated cherry-picking 4× — so the
// allegation was found in the rounds but had no section to land in. The result is
// theme-deduped to collapse near-duplicate headings.
// matterMode is the analytic mode BELO dispatches on, derived from the dominant epistemic issue
// type in the evidence graph. It decouples "what analysis to run" from any one practice area: a
// matter is classified by the KIND of issues its documents actually raise, and each analytic pass
// fires for the mode it serves. Extensible — transactional (clause review) and diligence
// (red-flag triage) slot in as further modes as their issue classes and passes are added.
