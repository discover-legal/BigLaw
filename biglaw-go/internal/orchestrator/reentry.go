// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Re-entrant machinery doctrine v1 — the round-boundary lever.
//
// All the SELECTIVE intelligence (specifics-sweep targeting, cross-document alias
// unification, defense-lens derivation) used to run once at round 0, before any
// understanding existed, and the pipe was one-way: rounds discovered entities, aliases,
// and theories that could never re-trigger the machinery (rounds saturated by 3; a run
// with a dead analysis round scored the same). This file is the lever the rounds'
// discoveries pull.
//
// The exhaustive round-0 passes are untouched — blind exhaustiveness is the retrieval
// floor and it works. What is added is a ROUND-BOUNDARY HOOK: after each DyTopo round's
// findings merge, the round's findings are absorbed into the evidence graph, the DELTA
// since the previous boundary (new entities, new claims, new allegation candidates) is
// computed by cheap snapshot comparison, and the targeted machinery re-fires on it:
//
//   1. targeted specifics RE-SWEEP for each new entity (capped), reusing the round-0
//      sweep machinery (runSpecificsQueries), deduped against the whole finding pool;
//   2. cross-document RE-JOIN: the discrepancy pass re-runs over the round-0 figure
//      harvest with the GROWN graph (new alias/affiliation ties unify entities that
//      round 0 could not), deduped against every discrepancy already emitted;
//   3. defense-lens RE-DERIVATION: deriveDefenseIssues re-runs over the grown graph;
//      only issues not already derivable are emitted, as findings the next round sees.
//
// The hook lives in the orchestrator's phase loop (policy), never inside the DyTopo
// engine (mechanism). It only fires on a non-empty delta, is bounded per round, writes a
// "machinery.reentry" audit event, and is disabled by REENTRANT_MACHINERY=false.

package orchestrator

import (
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	// reentryMaxEntities bounds the targeted re-sweep: at most this many NEW entities
	// are swept per round boundary (first-discovered first).
	reentryMaxEntities = 7
	// reentryMaxSweepFindings bounds what one boundary's re-sweep may add to the pool.
	reentryMaxSweepFindings = 12
	// reentryMaxAbsorbChunks bounds the absorption extraction calls per boundary
	// (~1500 tokens of round-finding text per chunk).
	reentryMaxAbsorbChunks = 4
)

const (
	reentryResweepAgentID = "specifics-resweep"
	defenseLensAgentID    = "defense-lens"
)

// machineryAgentIDs are the pipeline's own mechanical emitters. Their findings never
// feed absorption — only what the round's AGENTS discovered constitutes a delta.
var machineryAgentIDs = map[string]bool{
	"specifics-sweep":        true,
	reentryResweepAgentID:    true,
	"figure-harvest":         true,
	"section-handle-harvest": true,
	"contradiction-detector": true,
	crossDocAgentID:          true,
	"deviation-detector":     true,
	defenseLensAgentID:       true,
}

// reentryState is the per-task state the round-boundary hook keeps between rounds: the
// graph snapshot the delta is computed against, the dedup keys for everything the
// machinery has already emitted, and the round-0 figure harvest the re-join reuses.
// Mutated only from the task's own runTask goroutine.
type reentryState struct {
	prevEntities map[string]bool // lowercased entity names seen at the last boundary
	prevClaims   int             // claim count at the last boundary
	prevAllegs   int             // allegation-candidate count at the last boundary

	emittedIssues        map[string]bool // normalized defense-issue texts already derivable/emitted
	emittedDiscrepancies map[string]bool // discrepancy keys (sorted citation quotes) already emitted

	rawFigs []figureHit // the round-0 harvest (canonical labels), for the re-join
}

// reentryDelta is one round's contribution to the evidence graph.
type reentryDelta struct {
	newEntities []string
	newClaims   int
	newAllegs   int
}

func (d reentryDelta) empty() bool {
	return len(d.newEntities) == 0 && d.newClaims == 0 && d.newAllegs == 0
}

// reentryStateFor returns (lazily creating) the task's re-entry state.
func (o *Orchestrator) reentryStateFor(taskID string) *reentryState {
	o.reentryMu.Lock()
	defer o.reentryMu.Unlock()
	if o.reentry == nil {
		o.reentry = map[string]*reentryState{}
	}
	st := o.reentry[taskID]
	if st == nil {
		st = &reentryState{
			prevEntities:         map[string]bool{},
			emittedIssues:        map[string]bool{},
			emittedDiscrepancies: map[string]bool{},
		}
		o.reentry[taskID] = st
	}
	return st
}

// delta compares the graph against the last boundary snapshot. Pure read — sync()
// advances the snapshot once the boundary's machinery (including its own mechanical
// figure re-binding) has finished, so rebinds never masquerade as the next round's
// discoveries.
func (st *reentryState) delta(g *evidencegraph.Graph) reentryDelta {
	var d reentryDelta
	if g == nil {
		return d
	}
	for _, e := range g.Entities() {
		if !st.prevEntities[strings.ToLower(e)] {
			d.newEntities = append(d.newEntities, e)
		}
	}
	if n := len(g.Claims()) - st.prevClaims; n > 0 {
		d.newClaims = n
	}
	if n := len(g.Allegations()) - st.prevAllegs; n > 0 {
		d.newAllegs = n
	}
	return d
}

// sync re-snapshots the graph as the new baseline.
func (st *reentryState) sync(g *evidencegraph.Graph) {
	if g == nil {
		return
	}
	for _, e := range g.Entities() {
		st.prevEntities[strings.ToLower(e)] = true
	}
	st.prevClaims = len(g.Claims())
	st.prevAllegs = len(g.Allegations())
}

// initReentryState snapshots the round-0 baseline after the exhaustive task-start passes:
// the graph's entity/claim/allegation state, the defense issues ALREADY derivable (so a
// boundary re-derivation emits only genuinely new ones), and the discrepancy keys the
// round-0 contradiction/crossdoc passes already emitted. Called once from runTask.
func (o *Orchestrator) initReentryState(task *types.Task) {
	if o.cfg == nil || !o.cfg.ReentrantMachinery {
		return
	}
	st := o.reentryStateFor(task.ID)
	st.sync(o.evidenceGraph(task.ID))
	for _, issue := range o.deriveDefenseIssues(task) {
		st.emittedIssues[issueKey(issue)] = true
	}
	for _, f := range o.snapshot(task).Findings {
		if f.AgentID == "contradiction-detector" || f.AgentID == crossDocAgentID {
			st.emittedDiscrepancies[discrepancyKey(f)] = true
		}
	}
}

// machineryReentry is the round-boundary hook, called from runTask's phase loop after a
// round's findings merge and before the next round starts. prevFindingCount is the pool
// size before the round ran; everything past it is the round's contribution. A round
// that contributed nothing is a strict no-op (no model calls). Kill switch:
// REENTRANT_MACHINERY=false.
func (o *Orchestrator) machineryReentry(task *types.Task, prevFindingCount int) {
	if o.cfg == nil || !o.cfg.ReentrantMachinery {
		return
	}
	snap := o.snapshot(task)
	if len(snap.Findings) <= prevFindingCount {
		return // starved/empty round: no delta, nothing fires
	}
	newFindings := make([]types.Finding, 0, len(snap.Findings)-prevFindingCount)
	for _, f := range snap.Findings[prevFindingCount:] {
		if machineryAgentIDs[f.AgentID] || strings.TrimSpace(f.Content) == "" {
			continue
		}
		newFindings = append(newFindings, f)
	}
	if len(newFindings) == 0 {
		return
	}
	// Same model routing as the round-0 sweep: extraction work on the tool tier.
	stier := types.TierTool
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &stier, TaskType: routing.TaskExtraction})
	prov, err := o.provReg.Get(model)
	if err != nil {
		return
	}
	bare := routing.ResolveModelID(model)
	figModel := strings.TrimSpace(o.cfg.Models.FigureModel)
	if figModel == "" {
		figModel = bare
	}
	o.runMachineryReentry(task, snap, newFindings, prov, bare, figModel)
}

// runMachineryReentry is the provider-injected core (testable with fakes): absorb →
// delta → targeted re-fire → publish → audit.
func (o *Orchestrator) runMachineryReentry(task *types.Task, snap *types.Task, newFindings []types.Finding, prov providers.Provider, model, figModel string) {
	st := o.reentryStateFor(task.ID)
	g := o.evidenceGraph(task.ID)
	if g == nil {
		// Round 0 found nothing to build a graph from; the rounds still get one.
		g = evidencegraph.New()
		o.egraphsMu.Lock()
		o.egraphs[task.ID] = g
		o.egraphsMu.Unlock()
	}
	round := snap.CurrentRound

	absorbed := absorbRoundFindings(g, newFindings, prov, model, "round findings")
	delta := st.delta(g)

	fired := map[string]interface{}{}
	var added []types.Finding
	if len(delta.newEntities) > 0 {
		ents := delta.newEntities
		if len(ents) > reentryMaxEntities {
			ents = ents[:reentryMaxEntities]
		}
		// Bind the round-0 figure harvest to the new nodes (deterministic, no model
		// calls) so their figures ride the nodes into synthesis, exactly as the round-0
		// harvest bound figures to the entities it knew about then.
		if n := rebindFigures(g, st.rawFigs, ents); n > 0 {
			fired["reboundFigures"] = n
		}
		seen := poolContentKeys(snap.Findings)
		if sw := o.runSpecificsQueries(task, entitySweepQueries(ents), seen, reentryMaxSweepFindings, reentryResweepAgentID, "Specifics Re-Sweep", round); len(sw) > 0 {
			added = append(added, sw...)
			fired["resweepFindings"] = len(sw)
		}
	}
	if !delta.empty() {
		if xd := o.rejoinCrossDoc(task.ID, st, o.corpusText(task), g, prov, figModel); len(xd) > 0 {
			for i := range xd {
				xd[i].Round = round
			}
			added = append(added, xd...)
			fired["crossdocFindings"] = len(xd)
		}
		if lens := o.reentryLenses(task, st, round); len(lens) > 0 {
			added = append(added, lens...)
			fired["defenseIssues"] = len(lens)
		}
	}
	st.sync(g) // baseline includes this boundary's own additions

	if len(added) > 0 {
		o.update(task, func(t *types.Task) {
			t.Findings = append(t.Findings, added...)
			t.UpdatedAt = time.Now()
		})
	}
	if delta.empty() {
		return // absorption ran but the round taught the graph nothing new — quiet
	}
	slog.Info("machinery re-entry", "task", task.ID, "round", round,
		"absorbed_facts", absorbed, "new_entities", len(delta.newEntities),
		"new_claims", delta.newClaims, "new_allegations", delta.newAllegs,
		"findings_added", len(added))
	emitProgress(task.ID, "reentry", map[string]interface{}{
		"round": round, "newEntities": len(delta.newEntities), "findings": len(added),
	})
	audit.Default.Write(audit.WriteRequest{
		Event: "machinery.reentry", ActorID: audit.ActorSystem, TaskID: task.ID,
		Data: map[string]interface{}{
			"round":          round,
			"absorbedFacts":  absorbed,
			"newEntities":    len(delta.newEntities),
			"newClaims":      delta.newClaims,
			"newAllegations": delta.newAllegs,
			"fired":          fired,
		},
	})
}

// absorbRoundFindings runs the graph extractors over the round's new findings, so the
// graph accumulates what the rounds discovered (the pipe was one-way before this). The
// extraction text is the findings' own content plus their verbatim citation quotes;
// grounding is checked against that text, so nothing enters the graph that the round did
// not actually state. Bounded to reentryMaxAbsorbChunks chunks; temperature 0 (copy-out).
func absorbRoundFindings(g *evidencegraph.Graph, findings []types.Finding, prov providers.Provider, model, source string) int {
	if g == nil || prov == nil || model == "" || len(findings) == 0 {
		return 0
	}
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(strings.Join(strings.Fields(f.Content), " "))
		b.WriteString("\n")
		for _, c := range f.Citations {
			if q := strings.Join(strings.Fields(c.Quote), " "); q != "" {
				b.WriteString(q)
				b.WriteString("\n")
			}
		}
	}
	chunks := chunkByTokens(b.String(), 1500)
	if len(chunks) > reentryMaxAbsorbChunks {
		chunks = chunks[:reentryMaxAbsorbChunks]
	}
	zero := 0.0
	kept := 0
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		k1, _ := evidencegraph.ExtractInto(g, prov, model, &zero, chunk, source)
		k2, _ := evidencegraph.ExtractTriplesInto(g, prov, model, &zero, chunk, source)
		kept += k1 + k2
	}
	return kept
}

// entitySweepQueries builds the targeted fact-hunt queries for newly-discovered
// entities. Round 0 needed a model to discover WHICH entities to hunt from the passages;
// here the entity names ARE the targeting, so the queries are mechanical (deterministic,
// no model call) and run through the same runSpecificsQueries machinery.
func entitySweepQueries(entities []string) []string {
	qs := make([]string, 0, 2*len(entities))
	for _, e := range entities {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		qs = append(qs,
			e+" — exact figures: dollar amounts, percentages, rates, counts, dates, account numbers",
			e+" — statutory provisions, rule numbers, section and clause citations, obligations")
	}
	return qs
}

// rebindFigures binds the round-0 figure harvest to newly-discovered graph entities —
// the same co-occurrence binding harvestAndBindFigures applied to the entities it knew
// at round 0. Deterministic; the graph dedups. Returns the number of facts added.
func rebindFigures(g *evidencegraph.Graph, figs []figureHit, entities []string) int {
	if g == nil || len(figs) == 0 || len(entities) == 0 {
		return 0
	}
	n := 0
	for _, h := range figs {
		ql := strings.ToLower(h.Quote)
		for _, e := range entities {
			if e == "" {
				continue
			}
			el := strings.ToLower(e)
			if strings.Contains(ql, el) || (h.Entity != "" && strings.Contains(strings.ToLower(h.Entity), el)) {
				rel := "has associated figure"
				if h.Measures != "" {
					rel = "measures " + h.Measures
				}
				if g.Add(evidencegraph.Fact{Subject: e, Relation: rel, Value: h.Value, Quote: h.Quote, Source: h.Source}, h.Quote) {
					n++
				}
			}
		}
	}
	return n
}

// rejoinCrossDoc re-runs the cross-document discrepancy pass over the round-0 figure
// harvest with the GROWN graph — new alias/affiliation facts absorbed from the rounds
// unify entities the round-0 pass could not, letting previously-invisible conflicts
// fire. No full-corpus re-sweep: the harvested records are reused. Every result is
// deduped against the discrepancies already emitted (round 0's and earlier boundaries'),
// keyed on the citation-quote set so adjudicator wording differences never duplicate.
func (o *Orchestrator) rejoinCrossDoc(taskID string, st *reentryState, docText map[string]string, g *evidencegraph.Graph, prov providers.Provider, model string) []types.Finding {
	if len(st.rawFigs) < 2 || len(docText) < 2 {
		return nil
	}
	var out []types.Finding
	for _, f := range o.crossDocFindings(taskID, st.rawFigs, docText, g, prov, model) {
		k := discrepancyKey(f)
		if st.emittedDiscrepancies[k] {
			continue
		}
		st.emittedDiscrepancies[k] = true
		out = append(out, f)
	}
	return out
}

// reentryLenses re-derives the analytic defense issues over the grown graph and emits
// only the ones that were NOT already derivable at the previous boundary — a conduct
// claim absorbed in round N surfaces its defense lens at the round-N boundary, where the
// next round's agents can act on it, instead of only at synthesis.
func (o *Orchestrator) reentryLenses(task *types.Task, st *reentryState, round int) []types.Finding {
	var out []types.Finding
	for _, issue := range o.deriveDefenseIssues(task) {
		k := issueKey(issue)
		if k == "" || st.emittedIssues[k] {
			continue
		}
		st.emittedIssues[k] = true
		out = append(out, types.Finding{
			ID:         uuid.New().String(),
			AgentID:    defenseLensAgentID,
			AgentName:  "Defense Lens",
			Content:    issue,
			Confidence: 0.85,
			Round:      round,
			Timestamp:  time.Now(),
		})
	}
	return out
}

// corpusText builds the title→full-text map the re-join's substring lock verifies
// against (the same shape detectCrossDocDiscrepancies builds). No model calls.
func (o *Orchestrator) corpusText(task *types.Task) map[string]string {
	if o.knowledge == nil {
		return nil
	}
	const perDocTokenCap = 40000 // mirror the harvest's bound on a pathological raw log
	out := map[string]string{}
	for _, docID := range task.DocumentIDs {
		txt, err := o.knowledge.GetFullText(docID)
		if err != nil || strings.TrimSpace(txt) == "" {
			continue
		}
		title := docID
		if d := o.knowledge.GetByID(docID); d != nil && strings.TrimSpace(d.Title) != "" {
			title = d.Title
		}
		if len(txt) > perDocTokenCap*4 { // ~4 chars/token
			txt = txt[:perDocTokenCap*4]
		}
		out[title] = txt
	}
	return out
}

// poolContentKeys is the finding-key dedup seed: the normalized content of every finding
// already in the pool, so a re-sweep never re-enters a snippet round 0 (or an agent)
// already contributed.
func poolContentKeys(findings []types.Finding) map[string]bool {
	seen := make(map[string]bool, len(findings))
	for _, f := range findings {
		if k := figNorm(f.Content); k != "" {
			seen[k] = true
		}
	}
	return seen
}

// discrepancyKey identifies a discrepancy finding by its cited quote SET (sorted,
// normalized) — stable across adjudicator wording, so the same conflict emitted by the
// round-0 pass and a later re-join dedupes exactly once.
func discrepancyKey(f types.Finding) string {
	if len(f.Citations) > 0 {
		qs := make([]string, 0, len(f.Citations))
		for _, c := range f.Citations {
			qs = append(qs, figNorm(c.Quote))
		}
		sort.Strings(qs)
		return strings.Join(qs, "|")
	}
	return figNorm(f.Content)
}

// issueKey normalizes a derived defense issue for dedup — the same normalization
// renderDerivedIssues dedupes on.
func issueKey(s string) string { return figNorm(s) }
