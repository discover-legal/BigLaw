// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/google/uuid"
)

// Cross-document discrepancy detection — the planted source-vs-source conflicts the
// benchmark scores: the same METRIC reported with different values in different documents
// (4,217 trades in the referral vs 4,312 in the exhibit; a $92,600 compensation total vs
// the bank-records exhibit's $103,800 grand total; 73% vs 78% omnibus), and the same EVENT
// dated differently across documents (an email's actual header date vs the narrative's
// "on or about" allegation).
//
// The detection principle — and the false-positive cure — are the same rule: only compare
// values whose METRIC IDENTITY matches (same entity + same quantity-kind + same referent),
// never bare numbers. Two prior embarrassments, and what kills each:
//
//   - "2021 vs early 2022" (the vintages of the deleted spreadsheets, described identically
//     in both documents) — killed by the per-document VALUE-SET rule: a conflict requires two
//     documents whose asserted value sets are DISJOINT. When both documents carry both values
//     it is an enumeration consistently restated, not a conflict.
//   - "10-day vs 45-day" (two different Code-of-Ethics obligations) — killed by metric-identity
//     separation: the canonical-quantity normalization gives different obligations different
//     labels so they are never compared; if labels ever collide, the adjudicator sees both
//     contexts and rejects it; and on the deterministic (no-provider) path bare durations are
//     never flagged at all (deadline-vs-deadline is the archetypal false-friend shape).
//
// Machinery is REUSED from the figure harvest, not re-invented: extractFiguresLLM (the
// grounded figureHit record), normalizeFigures (canonical quantity labels — the referent
// half of metric identity), figNorm, contextWindow, clusterLabel, adjudicateContradiction.
// Everything emitted is substring-locked: both citations carry verbatim quotes verified
// against their source documents, per the grounding invariant.

const crossDocAgentID = "crossdoc-discrepancy"

const (
	// crossDocMinSharedReferents is the deterministic referent-identity floor: without a
	// model adjudicator, two figures are only treated as the same metric if their context
	// windows share at least this many distinctive content words (value and unit words
	// excluded) — "omnibus"+"equity"+"trades" ties the referral count to the exhibit row.
	crossDocMinSharedReferents = 2
	// crossDocMaxValuesPerMetric: a long ledger column sharing one label is an enumeration,
	// not a conflict (mirrors the harvest detector's distinct-value bound).
	crossDocMaxValuesPerMetric = 6
	crossDocAdjudicationCap    = 20  // bound numeric adjudication model calls
	crossDocDateAdjCap         = 10  // bound date-conflict adjudication model calls
	crossDocAliasCap           = 12  // bound alias-unification model calls
	crossDocMaxDateEntries     = 120 // bound the pairwise event-tie scan
)

// Value kinds — the quantity-kind leg of metric identity. Values of different kinds are
// never compared (a count is not a percentage of the same topic).
const (
	crossDocMoney    = "money"
	crossDocPercent  = "percent"
	crossDocCount    = "count"
	crossDocDuration = "duration"
	crossDocDateKind = "date"
	crossDocOther    = "other" // account numbers, statute cites — identity strings, not quantities
)

// crossDocEntry is one indexed figure: the harvested record plus its quantity-kind,
// canonical comparable value, parsed date (for date kind), and a best-effort ¶/section/
// page handle pulled from the context window.
type crossDocEntry struct {
	hit     figureHit
	kind    string
	canon   string       // canonical value: "usd:92600", "pct:73", "n:4217", "dur:10day", "date:2024-04-15"
	date    crossDocDate // set when kind == crossDocDateKind
	section string
}

// ─── Detection entry points ───────────────────────────────────────────────────

// detectCrossDocDiscrepancies sweeps every ingested document for figures (the same
// full-retrieval-floor idiom as harvestAndBindFigures — a discrepancy can only be seen
// after reading BOTH sides), normalizes them to canonical quantity labels, and returns
// grounded discrepancy findings for cross-document value conflicts and date conflicts.
//
// Seam note: this re-runs the cheap figure sweep because the harvest does not currently
// expose its raw records. When it does, feed them to crossDocFindings directly and drop
// the sweep here — the core consumes plain []figureHit.
func (o *Orchestrator) detectCrossDocDiscrepancies(task *types.Task, g *evidencegraph.Graph, prov providers.Provider, figModel string) []types.Finding {
	if prov == nil || figModel == "" || len(task.DocumentIDs) < 2 {
		return nil
	}
	const perDocTokenCap = 40000 // mirror the harvest's bound on a pathological raw log
	docText := map[string]string{}
	var raw []figureHit
	for _, docID := range task.DocumentIDs {
		txt, err := o.knowledge.GetFullText(docID)
		if err != nil || strings.TrimSpace(txt) == "" {
			continue
		}
		title := docID
		if d := o.knowledge.GetByID(docID); d != nil && strings.TrimSpace(d.Title) != "" {
			title = d.Title
		}
		swept := txt
		if len(swept) > perDocTokenCap*4 { // ~4 chars/token
			swept = swept[:perDocTokenCap*4]
			slog.Info("crossdoc sweep truncated oversized doc", "task", task.ID, "doc", title)
		}
		docText[title] = swept
		for _, chunk := range chunkByTokens(swept, 1500) {
			for _, h := range extractFiguresLLM(prov, figModel, chunk) {
				h.Source = title
				raw = append(raw, h)
			}
		}
	}
	if len(raw) < 2 || len(docText) < 2 {
		return nil
	}
	// REUSE the harvest's canonical-quantity normalization: one model pass assigns the
	// SAME label to the same quantity across documents ("alleged 4,217 trades" and
	// "4,312 trades analyzed" both become "omnibus trade count") and DIFFERENT labels to
	// different quantities (the 10-day vs 45-day obligations stay apart).
	o.normalizeFigures(prov, figModel, raw)
	return o.crossDocFindings(task.ID, raw, docText, g, prov, figModel)
}

// crossDocFindings is the pure-ish core: index the harvested figures, unify entity
// aliases, and apply the two conflict rules. docText (title → full text) re-verifies the
// substring lock; a source missing from the map falls back to the chunk-level grounding
// gate the extractor already applied.
func (o *Orchestrator) crossDocFindings(taskID string, raw []figureHit, docText map[string]string, g *evidencegraph.Graph, prov providers.Provider, model string) []types.Finding {
	var entries []crossDocEntry
	seen := map[string]bool{}
	for _, h := range raw {
		if strings.TrimSpace(h.Value) == "" || strings.TrimSpace(h.Quote) == "" || strings.TrimSpace(h.Source) == "" {
			continue
		}
		// Substring lock: the quote we would cite must be a verbatim span of its source.
		if txt, ok := docText[h.Source]; ok && !strings.Contains(figNorm(txt), figNorm(h.Quote)) {
			continue
		}
		kind, canon, dt := classifyFigureValue(h.Value)
		if kind == crossDocOther {
			continue
		}
		k := figNorm(h.Source) + "|" + canon + "|" + figNorm(h.Measures) + "|" + figNorm(h.Quote)
		if seen[k] {
			continue
		}
		seen[k] = true
		ctx := h.Context
		if ctx == "" {
			ctx = h.Quote
		}
		entries = append(entries, crossDocEntry{hit: h, kind: kind, canon: canon, date: dt, section: crossDocSection(ctx)})
	}
	if len(entries) < 2 {
		return nil
	}
	es := buildCrossDocEntitySet(entries, g)
	var out []types.Finding
	out = append(out, o.crossDocNumericConflicts(taskID, entries, es, prov, model)...)
	out = append(out, o.crossDocDateConflicts(taskID, entries, prov, model)...)
	if len(out) > 0 {
		slog.Info("cross-document discrepancies detected", "task", taskID, "n", len(out))
	}
	return out
}

// ─── Numeric conflicts: same metric identity, different value, different documents ─────

func (o *Orchestrator) crossDocNumericConflicts(taskID string, entries []crossDocEntry, es *crossDocEntitySet, prov providers.Provider, model string) []types.Finding {
	// Group by (canonical quantity label, kind) — the label carries the referent, the
	// kind keeps counts and percentages of the same topic apart.
	byKey := map[string][]int{}
	var order []string
	for i, e := range entries {
		if e.kind == crossDocDateKind {
			continue // dates take the event-identity path below
		}
		lbl := figNorm(e.hit.Measures)
		if lbl == "" {
			continue // no referent — a bare number is never compared
		}
		k := lbl + "|" + e.kind
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], i)
	}

	aliasBudget := crossDocAliasCap
	adjBudget := crossDocAdjudicationCap
	var out []types.Finding
	for _, gk := range order {
		// The ENTITY leg of metric identity: within one quantity label, split by
		// alias-unified entity — the same label for different (non-unifiable) parties is
		// NOT one metric ("Ostrowski's ownership %" vs "Chen's ownership %").
		for _, sub := range o.splitByEntity(taskID, entries, byKey[gk], es, prov, model, &aliasBudget) {
			if f := o.evalNumericGroup(entries, sub, prov, model, &adjBudget); f != nil {
				out = append(out, *f)
			}
		}
	}
	return out
}

// splitByEntity partitions a same-label group by alias-unified entity. Deterministic ties
// (exact / token-containment / evidence-graph link) were already applied to the entity
// set; the remaining ambiguous cross-document pairs ("Ostrowski" vs "Bayshore Palms LLC"
// on the same compensation stream) get one extraction-tier model call each, cost-recorded,
// with a deterministic fallback of NOT unifying (never merge parties on a guess).
// Unattributed figures (empty entity — common for exhibit table rows) join the group when
// exactly one entity remains, otherwise stand alone.
func (o *Orchestrator) splitByEntity(taskID string, entries []crossDocEntry, idxs []int, es *crossDocEntitySet, prov providers.Provider, model string, aliasBudget *int) [][]int {
	collect := func() (map[int][]int, []int, []int) {
		roots := map[int][]int{}
		var rorder []int
		var unattributed []int
		for _, i := range idxs {
			r := es.root(entries[i].hit.Entity)
			if r < 0 {
				unattributed = append(unattributed, i)
				continue
			}
			if _, ok := roots[r]; !ok {
				rorder = append(rorder, r)
			}
			roots[r] = append(roots[r], i)
		}
		return roots, rorder, unattributed
	}
	roots, rorder, unattributed := collect()
	if len(rorder) > 1 && prov != nil && model != "" {
		for x := 0; x < len(rorder); x++ {
			for y := x + 1; y < len(rorder); y++ {
				if *aliasBudget <= 0 {
					break
				}
				if es.d.find(rorder[x]) == es.d.find(rorder[y]) {
					continue // already unified by an earlier judgment
				}
				a, b := entries[roots[rorder[x]][0]], entries[roots[rorder[y]][0]]
				if a.hit.Source == b.hit.Source {
					continue // same-document distinct parties are genuinely different
				}
				*aliasBudget--
				if o.crossDocAliasJudge(taskID, prov, model, a, b) {
					es.d.union(rorder[x], rorder[y])
				}
			}
		}
		roots, rorder, unattributed = collect()
	}
	var out [][]int
	for _, r := range rorder {
		out = append(out, roots[r])
	}
	switch {
	case len(out) == 1:
		out[0] = append(out[0], unattributed...)
	case len(out) == 0 && len(unattributed) > 0:
		out = append(out, unattributed)
	case len(unattributed) > 0:
		out = append(out, unattributed)
	}
	return out
}

// evalNumericGroup applies the conflict rule to one metric-identity group: ≥2 documents,
// 2..N distinct canonical values, and at least one pair of documents whose asserted value
// SETS are disjoint (the enumeration guard — "2021 and early 2022" in both docs never
// fires). Confirmed candidates are adjudicated by the model (context-aware; rejects
// tiered rates, sub-totals vs totals, different obligations sharing a label); without a
// provider, deterministic guards apply and the finding carries a template note.
func (o *Orchestrator) evalNumericGroup(entries []crossDocEntry, idxs []int, prov providers.Provider, model string, adjBudget *int) *types.Finding {
	if len(idxs) < 2 {
		return nil
	}
	byCanon := map[string]crossDocEntry{}
	var corder []string
	perDoc := map[string]map[string]bool{}
	var docs []string
	for _, i := range idxs {
		e := entries[i]
		if _, ok := byCanon[e.canon]; !ok {
			byCanon[e.canon] = e
			corder = append(corder, e.canon)
		}
		if perDoc[e.hit.Source] == nil {
			perDoc[e.hit.Source] = map[string]bool{}
			docs = append(docs, e.hit.Source)
		}
		perDoc[e.hit.Source][e.canon] = true
	}
	if len(corder) < 2 || len(corder) > crossDocMaxValuesPerMetric || len(docs) < 2 {
		return nil
	}
	sort.Strings(docs) // deterministic pair order
	confA, confB, ok := crossDocDisjointPair(entries, idxs, perDoc, docs)
	if !ok {
		return nil
	}
	noJudge := prov == nil || model == ""
	if noJudge {
		// Deterministic guards: bare durations are the deadline-vs-deadline false-friend
		// shape (never flag without a judge), and the referent floor must hold between
		// the actual conflicting pair.
		if confA.kind == crossDocDuration {
			return nil
		}
		if crossDocSharedReferents(confA.hit, confB.hit) < crossDocMinSharedReferents {
			return nil
		}
	}
	vals := make([]figureHit, 0, len(corder))
	for _, c := range corder {
		vals = append(vals, byCanon[c].hit)
	}
	ent, meas := clusterLabel(vals)
	sig := ""
	if !noJudge {
		if *adjBudget <= 0 {
			return nil
		}
		*adjBudget--
		real, s := o.adjudicateContradiction(prov, model, ent, meas, vals)
		if !real {
			return nil
		}
		sig = s
	}
	exemplars := make([]crossDocEntry, 0, len(corder))
	for _, c := range corder {
		exemplars = append(exemplars, byCanon[c])
	}
	f := crossDocBuildFinding(exemplars, ent, meas, sig, crossDocTemplateNote)
	return &f
}

// crossDocDisjointPair finds two documents whose value sets for this metric do not
// overlap and returns one conflicting entry from each. No disjoint pair → every document
// pair shares a value → consistent restatement/enumeration, not a conflict.
func crossDocDisjointPair(entries []crossDocEntry, idxs []int, perDoc map[string]map[string]bool, docs []string) (crossDocEntry, crossDocEntry, bool) {
	entryFrom := func(doc string, exclude map[string]bool) (crossDocEntry, bool) {
		for _, i := range idxs {
			if entries[i].hit.Source == doc && !exclude[entries[i].canon] {
				return entries[i], true
			}
		}
		return crossDocEntry{}, false
	}
	for x := 0; x < len(docs); x++ {
		for y := x + 1; y < len(docs); y++ {
			disjoint := true
			for c := range perDoc[docs[x]] {
				if perDoc[docs[y]][c] {
					disjoint = false
					break
				}
			}
			if !disjoint {
				continue
			}
			a, aok := entryFrom(docs[x], nil)
			b, bok := entryFrom(docs[y], map[string]bool{a.canon: true})
			if aok && bok {
				return a, b, true
			}
		}
	}
	return crossDocEntry{}, crossDocEntry{}, false
}

// ─── Date conflicts: same event, different dates across documents ─────────────────────

// crossDocDateConflicts ties date-bearing figures to their EVENT (the context window
// carries it: "clean up the shared drive") and flags cross-document date disagreement.
// The email-header-vs-narrative case is the target: metadata dates and allegation dates
// rarely share a quantity label across document genres, so event identity comes from
// shared distinctive context words OR a shared label. Dates are compared at their
// coarsest common precision ("April 2024" does not conflict with "April 22, 2024"), and
// the per-document value-set rule suppresses enumerations (the spreadsheet vintages).
func (o *Orchestrator) crossDocDateConflicts(taskID string, entries []crossDocEntry, prov providers.Provider, model string) []types.Finding {
	var di []int
	for i, e := range entries {
		if e.kind == crossDocDateKind {
			di = append(di, i)
		}
	}
	if len(di) < 2 {
		return nil
	}
	if len(di) > crossDocMaxDateEntries {
		di = di[:crossDocMaxDateEntries]
	}
	d := newCrossDocDSU(len(di))
	for x := 0; x < len(di); x++ {
		for y := x + 1; y < len(di); y++ {
			a, b := entries[di[x]], entries[di[y]]
			la, lb := figNorm(a.hit.Measures), figNorm(b.hit.Measures)
			if (la != "" && la == lb) || crossDocSharedReferents(a.hit, b.hit) >= crossDocMinSharedReferents {
				d.union(x, y)
			}
		}
	}
	clusters := map[int][]int{}
	var corder []int
	for x := range di {
		r := d.find(x)
		if _, ok := clusters[r]; !ok {
			corder = append(corder, r)
		}
		clusters[r] = append(clusters[r], x)
	}
	adjBudget := crossDocDateAdjCap
	var out []types.Finding
	for _, r := range corder {
		if f := o.evalDateCluster(taskID, entries, di, clusters[r], prov, model, &adjBudget); f != nil {
			out = append(out, *f)
		}
	}
	return out
}

func (o *Orchestrator) evalDateCluster(taskID string, entries []crossDocEntry, di, cluster []int, prov providers.Provider, model string, adjBudget *int) *types.Finding {
	if len(cluster) < 2 {
		return nil
	}
	perDoc := map[string]map[string]bool{}
	var docs []string
	distinct := map[string]bool{}
	for _, x := range cluster {
		e := entries[di[x]]
		if perDoc[e.hit.Source] == nil {
			perDoc[e.hit.Source] = map[string]bool{}
			docs = append(docs, e.hit.Source)
		}
		perDoc[e.hit.Source][e.canon] = true
		distinct[e.canon] = true
	}
	if len(docs) < 2 || len(distinct) < 2 || len(distinct) > crossDocMaxValuesPerMetric {
		return nil
	}
	sort.Strings(docs)
	// The conflict needs a document pair with DISJOINT date sets (enumeration guard) AND
	// an actually incompatible cross-document pair (precision-aware).
	var confA, confB crossDocEntry
	found := false
	for x := 0; x < len(docs) && !found; x++ {
		for y := x + 1; y < len(docs) && !found; y++ {
			disjoint := true
			for c := range perDoc[docs[x]] {
				if perDoc[docs[y]][c] {
					disjoint = false
					break
				}
			}
			if !disjoint {
				continue
			}
			for _, ix := range cluster {
				for _, iy := range cluster {
					a, b := entries[di[ix]], entries[di[iy]]
					if a.hit.Source != docs[x] || b.hit.Source != docs[y] {
						continue
					}
					if !a.date.compatible(b.date) {
						confA, confB, found = a, b, true
						break
					}
				}
				if found {
					break
				}
			}
		}
	}
	if !found {
		return nil
	}
	vals := []figureHit{confA.hit, confB.hit}
	ent, meas := clusterLabel(vals)
	if strings.TrimSpace(meas) == "" || meas == "the same quantity" {
		meas = "event date"
	}
	sig := ""
	if prov != nil && model != "" {
		if *adjBudget <= 0 {
			return nil
		}
		*adjBudget--
		real, s := o.adjudicateContradiction(prov, model, ent, meas, vals)
		if !real {
			return nil
		}
		sig = s
	}
	f := crossDocBuildFinding([]crossDocEntry{confA, confB}, ent, meas, sig, crossDocDateTemplateNote)
	return &f
}

// ─── Finding construction ─────────────────────────────────────────────────────────────

const crossDocTemplateNote = "The record states this figure differently in different documents; establish which source is authoritative and use the inconsistency to test the reliability of the factual narrative — surface it, do not silently reconcile it."

const crossDocDateTemplateNote = "The document's own metadata conflicts with the narrative's alleged timing; sequence-of-events, notice, and limitations arguments may turn on which date is correct — surface the conflict, do not silently reconcile it."

// crossDocBuildFinding renders a discrepancy finding: both (all) verbatim quotes as
// mechanically-verified citations, each value tagged with its source document and
// section handle, plus a one-line defense-significance note (the adjudicator's, or the
// template under graceful degradation). The content shape matches the harvest detector's
// so appendDiscrepancies renders both under the same section framing.
func crossDocBuildFinding(vals []crossDocEntry, ent, meas, sig, fallbackNote string) types.Finding {
	var parts []string
	var cites []types.Citation
	for _, e := range vals {
		loc := e.hit.Source
		if e.section != "" {
			loc += ", " + e.section
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", e.hit.Value, loc))
		cites = append(cites, types.Citation{Source: e.hit.Source, Quote: e.hit.Quote, MechanicallyVerified: true})
	}
	ent = strings.TrimSpace(ent)
	if ent == "" {
		ent = "the matter"
	}
	meas = strings.TrimSpace(meas)
	if meas == "" {
		meas = "the same quantity"
	}
	sig = strings.TrimSpace(sig)
	if sig == "" {
		sig = fallbackNote
	}
	return types.Finding{
		ID:             uuid.New().String(),
		AgentID:        crossDocAgentID,
		AgentName:      "Cross-Document Discrepancy Detector",
		Content:        fmt.Sprintf("DISCREPANCY (defense issue) — %s, %s: %s. %s", ent, meas, strings.Join(parts, " vs "), sig),
		Citations:      cites,
		Confidence:     0.95,
		EvidenceStatus: types.EvidenceGrounded,
		Round:          0,
		Timestamp:      time.Now(),
	}
}

// ─── Value classification & canonicalization ──────────────────────────────────────────

var (
	reCrossDocPercent  = regexp.MustCompile(`^~?(?:approximately\s+)?(-?\d+(?:\.\d+)?)\s*%$`)
	reCrossDocMoney    = regexp.MustCompile(`(?i)^~?\$\s*([\d,]+(?:\.\d+)?)\s*(million|billion|thousand|mm|bn|[kmb])?$`)
	reCrossDocDuration = regexp.MustCompile(`(?i)^(\d+)[ -](?:calendar[ -]|business[ -])?(day|week|month|year)s?$`)
	reCrossDocNumber   = regexp.MustCompile(`^-?[\d,]+(?:\.\d+)?$`)
)

// classifyFigureValue types a figure's value and produces the canonical comparable form,
// so "4,217" ≡ "4217" and "$0.1 million" ≡ "$100,000" — formatting differences are never
// mistaken for conflicts, and only commensurable kinds are ever compared.
func classifyFigureValue(v string) (string, string, crossDocDate) {
	v = strings.TrimSpace(v)
	if m := reCrossDocPercent.FindStringSubmatch(v); m != nil {
		return crossDocPercent, "pct:" + canonNumber(m[1]), crossDocDate{}
	}
	if m := reCrossDocMoney.FindStringSubmatch(v); m != nil {
		f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if err != nil {
			return crossDocOther, "", crossDocDate{}
		}
		switch strings.ToLower(m[2]) {
		case "k", "thousand":
			f *= 1e3
		case "m", "mm", "million":
			f *= 1e6
		case "b", "bn", "billion":
			f *= 1e9
		}
		return crossDocMoney, "usd:" + strconv.FormatFloat(f, 'f', -1, 64), crossDocDate{}
	}
	if m := reCrossDocDuration.FindStringSubmatch(v); m != nil {
		return crossDocDuration, "dur:" + m[1] + strings.ToLower(m[2]), crossDocDate{}
	}
	if dt, ok := parseCrossDocDate(v); ok {
		return crossDocDateKind, "date:" + dt.key(), dt
	}
	if reCrossDocNumber.MatchString(v) {
		return crossDocCount, "n:" + canonNumber(v), crossDocDate{}
	}
	return crossDocOther, "", crossDocDate{}
}

func canonNumber(s string) string {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), 64)
	if err != nil {
		return figNorm(s)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ─── Dates ────────────────────────────────────────────────────────────────────────────

// crossDocDate is a calendar date at whatever precision the source stated it: month and
// day may be zero ("2021", "May 2022"). Precision travels with the value so comparisons
// never invent conflicts between a coarse statement and a precise one.
type crossDocDate struct{ y, m, d int }

func (a crossDocDate) key() string {
	switch {
	case a.m == 0:
		return fmt.Sprintf("%04d", a.y)
	case a.d == 0:
		return fmt.Sprintf("%04d-%02d", a.y, a.m)
	default:
		return fmt.Sprintf("%04d-%02d-%02d", a.y, a.m, a.d)
	}
}

// compatible reports whether two dates AGREE at their coarsest common precision:
// "April 2024" is compatible with "April 22, 2024"; "April 15, 2024" is not compatible
// with "April 22, 2024".
func (a crossDocDate) compatible(b crossDocDate) bool {
	if a.y != b.y {
		return false
	}
	if a.m == 0 || b.m == 0 {
		return true
	}
	if a.m != b.m {
		return false
	}
	if a.d == 0 || b.d == 0 {
		return true
	}
	return a.d == b.d
}

var crossDocDateLayouts = []string{
	"Mon, 2 Jan 2006 15:04:05 -0700", // RFC-2822 email header
	"Mon, 2 Jan 2006 15:04:05 MST",
	"2 Jan 2006 15:04:05 -0700",
	"Mon, 2 Jan 2006",
	"2 Jan 2006",
	"2 January 2006",
	"January 2, 2006",
	"Jan 2, 2006",
	"January 2 2006",
	"2006-01-02",
	"01/02/2006",
	"1/2/2006",
}

var crossDocMonthYearLayouts = []string{"January 2006", "Jan 2006", "01/2006", "1/2006"}

var reCrossDocYear = regexp.MustCompile(`(?i)^(?:early|late|mid)?[ -]?((?:19|20)\d{2})$`)

// parseCrossDocDate parses a date value at its stated precision, tolerating narrative
// hedges ("on or about April 22, 2024") and email headers.
func parseCrossDocDate(s string) (crossDocDate, bool) {
	s = strings.Join(strings.Fields(s), " ")
	for _, p := range []string{"on or about", "on or around", "dated", "date:"} {
		if len(s) > len(p) && strings.EqualFold(s[:len(p)], p) {
			s = strings.TrimSpace(s[len(p):])
		}
	}
	s = strings.Trim(s, ".,")
	for _, l := range crossDocDateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return crossDocDate{t.Year(), int(t.Month()), t.Day()}, true
		}
	}
	for _, l := range crossDocMonthYearLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return crossDocDate{t.Year(), int(t.Month()), 0}, true
		}
	}
	if m := reCrossDocYear.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		return crossDocDate{y, 0, 0}, true
	}
	return crossDocDate{}, false
}

// ─── Referent tokens (deterministic identity floor) ───────────────────────────────────

var crossDocStop = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "with": true, "this": true,
	"was": true, "are": true, "its": true, "had": true, "has": true, "not": true,
	"did": true, "any": true, "all": true, "from": true, "into": true, "such": true,
	"which": true, "were": true, "their": true, "about": true, "after": true,
	"before": true, "during": true, "through": true, "between": true, "under": true,
	"over": true, "each": true, "per": true, "been": true, "being": true, "have": true,
	"than": true, "there": true, "these": true, "those": true, "will": true, "would": true,
}

// crossDocUnitTok excludes measure/framing words from referent identity — "days" belongs
// to the quantity, not the referent, so "within 10 days" and "within 45 days" of two
// different obligations don't tie on the word "days".
var crossDocUnitTok = map[string]bool{
	"day": true, "week": true, "month": true, "year": true, "percent": true,
	"percentage": true, "dollar": true, "total": true, "grand": true, "sum": true,
	"amount": true, "approximately": true, "number": true, "count": true, "rate": true,
}

func crossDocSingular(t string) string {
	if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
		return t[:len(t)-1]
	}
	return t
}

// crossDocReferentTokens extracts the distinctive content words of a figure's context —
// what the number is ABOUT — excluding the value itself, digits, units, and stop words.
func crossDocReferentTokens(h figureHit) map[string]bool {
	ctx := h.Context
	if ctx == "" {
		ctx = h.Quote
	}
	skip := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(h.Value)) {
		skip[crossDocSingular(strings.Trim(t, ".,;:()[]{}\"'`|"))] = true
	}
	out := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(ctx)) {
		t = strings.Trim(t, ".,;:()[]{}\"'`|—–")
		t = strings.TrimSuffix(t, "'s")
		if len(t) < 3 || strings.ContainsAny(t, "0123456789") {
			continue
		}
		t = crossDocSingular(t)
		if crossDocStop[t] || crossDocUnitTok[t] || skip[t] {
			continue
		}
		out[t] = true
	}
	return out
}

func crossDocSharedReferents(a, b figureHit) int {
	ta, tb := crossDocReferentTokens(a), crossDocReferentTokens(b)
	n := 0
	for t := range ta {
		if tb[t] {
			n++
		}
	}
	return n
}

// ─── Section handles ──────────────────────────────────────────────────────────────────

var reCrossDocSection = regexp.MustCompile(`(?i)(¶+\s*\d+[a-z]?|\bpara(?:graph)?\.?\s*\d+|\bsection\s+\d[\w.()\-]*|\bpage\s+\d+|\brow\s+\d+)`)

// crossDocSection pulls a best-effort ¶/paragraph/section/page handle from the context
// window, so a discrepancy names "SEC Referral, ¶56" not just the document.
func crossDocSection(ctx string) string {
	return strings.Join(strings.Fields(reCrossDocSection.FindString(ctx)), " ")
}

// ─── Entity alias unification ─────────────────────────────────────────────────────────

// crossDocEntitySet is the alias-unified entity space: distinct normalized entity names
// with a union-find over them. Deterministic ties are applied at build time; the model
// alias judge adds unions lazily per ambiguous group.
type crossDocEntitySet struct {
	names []string
	idx   map[string]int
	d     *crossDocDSU
}

func crossDocEntName(s string) string {
	return strings.TrimPrefix(figNorm(s), "the ")
}

func buildCrossDocEntitySet(entries []crossDocEntry, g *evidencegraph.Graph) *crossDocEntitySet {
	es := &crossDocEntitySet{idx: map[string]int{}}
	for _, e := range entries {
		n := crossDocEntName(e.hit.Entity)
		if n == "" {
			continue
		}
		if _, ok := es.idx[n]; !ok {
			es.idx[n] = len(es.names)
			es.names = append(es.names, n)
		}
	}
	es.d = newCrossDocDSU(len(es.names))
	// Deterministic tie 1 — token containment: "Ostrowski" ⊆ "Richard Ostrowski".
	for i := 0; i < len(es.names); i++ {
		for j := i + 1; j < len(es.names); j++ {
			if crossDocTokenSubset(es.names[i], es.names[j]) {
				es.d.union(i, j)
			}
		}
	}
	// Deterministic tie 2 — a grounded evidence-graph fact directly linking the two
	// entities ("Ostrowski — controls — Bayshore Palms LLC") is an alias/affiliation tie
	// the record itself asserts.
	if g != nil {
		for _, f := range g.All() {
			s, ob := crossDocEntName(f.Subject), crossDocEntName(f.Object)
			if s == "" || ob == "" {
				continue
			}
			si, sok := es.match(s)
			oi, ook := es.match(ob)
			if sok && ook && si != oi {
				es.d.union(si, oi)
			}
		}
	}
	return es
}

func (es *crossDocEntitySet) match(name string) (int, bool) {
	if i, ok := es.idx[name]; ok {
		return i, true
	}
	if len(name) < 4 {
		return 0, false // too short for a safe substring match
	}
	for i, n := range es.names {
		if strings.Contains(n, name) || strings.Contains(name, n) {
			return i, true
		}
	}
	return 0, false
}

// root returns the alias-group id for an entity name, or -1 for empty/unknown.
func (es *crossDocEntitySet) root(entity string) int {
	n := crossDocEntName(entity)
	if n == "" {
		return -1
	}
	if i, ok := es.idx[n]; ok {
		return es.d.find(i)
	}
	return -1
}

// crossDocTokenSubset reports whether the shorter name's tokens all appear in the longer
// name (requiring at least one substantial token, so a stray "llc" never unifies).
func crossDocTokenSubset(a, b string) bool {
	at, bt := strings.Fields(a), strings.Fields(b)
	if len(at) == 0 || len(bt) == 0 {
		return false
	}
	if len(at) > len(bt) {
		at, bt = bt, at
	}
	set := map[string]bool{}
	for _, t := range bt {
		set[t] = true
	}
	substantial := false
	for _, t := range at {
		if !set[t] {
			return false
		}
		if len(t) >= 4 {
			substantial = true
		}
	}
	return substantial
}

const crossDocAliasSystem = "You decide whether two named entities — each shown with the context of a figure attributed to it — refer to the SAME underlying entity or payment stream (a person and the company through which he was paid, an alias, an affiliate the record uses interchangeably) or to DIFFERENT things. Output ONLY a JSON object: {\"same\": true|false}."

// crossDocAliasJudge asks the extraction-tier model whether two entities are the same
// underlying entity/stream given each figure's context. Cost-recorded. The deterministic
// fallback (no provider, call error, parse miss) is NOT to unify — merging two parties on
// a guess would manufacture exactly the false positives this pass exists to kill.
func (o *Orchestrator) crossDocAliasJudge(taskID string, prov providers.Provider, model string, a, b crossDocEntry) bool {
	if prov == nil || model == "" {
		return false
	}
	ctxOf := func(e crossDocEntry) string {
		if e.hit.Context != "" {
			return e.hit.Context
		}
		return e.hit.Quote
	}
	user := fmt.Sprintf("Entity A: %s — figure context: %q [%s]\nEntity B: %s — figure context: %q [%s]\n\nDo A and B refer to the same underlying entity or payment stream?",
		a.hit.Entity, strutil.Truncate(ctxOf(a), 240), a.hit.Source,
		b.hit.Entity, strutil.Truncate(ctxOf(b), 240), b.hit.Source)
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   60,
		System:      crossDocAliasSystem,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return false
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextSynthesis, taskID)
	var text string
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			text = blk.Text
		}
	}
	lo, hi := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if lo >= 0 && hi > lo {
		var v struct {
			Same bool `json:"same"`
		}
		if json.Unmarshal([]byte(text[lo:hi+1]), &v) == nil {
			return v.Same
		}
	}
	return false
}

// ─── Union-find ───────────────────────────────────────────────────────────────────────

type crossDocDSU struct{ p []int }

func newCrossDocDSU(n int) *crossDocDSU {
	d := &crossDocDSU{p: make([]int, n)}
	for i := range d.p {
		d.p[i] = i
	}
	return d
}

func (d *crossDocDSU) find(x int) int {
	for d.p[x] != x {
		d.p[x] = d.p[d.p[x]]
		x = d.p[x]
	}
	return x
}

func (d *crossDocDSU) union(a, b int) { d.p[d.find(a)] = d.find(b) }
