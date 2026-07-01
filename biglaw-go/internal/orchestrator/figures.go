// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
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

// Figure extraction is model-based (a small model at temperature 0 — deterministic copy-out),
// NOT regex. A regex over the documents captured every statute cite ("Section 206", "206(1)")
// as a "figure" and flooded the deliverable; the model extracts figures deliberately, with an
// attribution and a verbatim quote, and the grounding gate drops any hallucination.
//
// The sweep runs over EVERY chunk of EVERY ingested document (full retrieval floor) — not just
// semantically-retrieved allegation narrative. The granular numbers (trade counts, allocation
// %, $ totals, ownership %) live in .xlsx EXHIBIT tables that don't embed near allegation
// queries, so a semantic sweep never sees them. Reading every chunk is the only way to
// guarantee the floor, and it's the precondition for contradiction detection (you can't flag
// "referral says 4,217 but the log says 4,312" unless you've read both sides).

// figureHit is one model-extracted figure: the value, the verbatim span that grounds it, the
// party/thing it concerns, what quantity it MEASURES (for contradiction grouping), and the
// source document.
type figureHit struct {
	Value    string
	Quote    string // the verbatim span (grounding)
	Source   string // document title
	Entity   string // party/thing the figure concerns
	Measures string // normalized label for the QUANTITY (groups same-quantity figures)
	Context  string // a wider window around the figure (so an adjudicator can tell WHAT it is)
}

// figureExtractSystem: a small model (7B-class) at temp 0 reliably copies out figures WITH a
// verbatim quote, an attribution, and a normalized "measures" label so the same quantity from
// different sources can be matched and contradictions surfaced.
const figureExtractSystem = "List every figure stated in the passage — dollar amount, percentage, count, date, account number, or statute/rule citation. INCLUDE figures inside parentheses or after 'did not disclose' / 'failed to', and INCLUDE figures stated in tables / pipe-delimited rows (e.g. 'Total Omnibus Equity Trades Analyzed | 4,312'). For EACH figure output one JSON object with exactly: \"value\" (the figure as written), \"entity\" (the party or thing it concerns), \"measures\" (a SHORT normalized label for WHAT QUANTITY this is — e.g. 'omnibus trade percentage', 'total excess commissions', 'trade count', 'ownership percentage', 'obstruction date', 'deleted file count'; use the SAME label every time for the same quantity so values can be compared across passages), and \"quote\" (copy the EXACT words from the passage around the figure — REQUIRED, must appear verbatim). EVERY field value MUST be a JSON string in double quotes — even pure numbers and ids (\"4,312\", \"78%\", \"801-74892\"). Ignore paragraph/list numbers. Output ONLY a JSON array."

// extractFiguresLLM runs the small figure model at temperature 0 (deterministic) over one
// chunk and returns grounded figure hits (quote verbatim in the chunk; ungrounded rows
// dropped — the safety net against any hallucination). Returns nil on any error/parse miss.
func extractFiguresLLM(prov providers.Provider, model, chunk string) []figureHit {
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   1500,
		System:      figureExtractSystem,
		Messages:    []providers.Message{{Role: "user", Content: "PASSAGE:\n" + chunk}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return nil
	}
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "```json"), "```")
	t = strings.TrimSuffix(t, "```")
	i, j := strings.Index(t, "["), strings.LastIndex(t, "]")
	if i < 0 || j <= i {
		return nil
	}
	rows := parseFigureRows(t[i : j+1])
	if len(rows) == 0 {
		return nil
	}
	cn := figNorm(chunk)
	var hits []figureHit
	for _, r := range rows {
		v, q := strings.TrimSpace(r.Value), strings.TrimSpace(r.Quote)
		if v == "" || q == "" || !strings.Contains(cn, figNorm(q)) { // grounding gate
			continue
		}
		hits = append(hits, figureHit{
			Value:    v,
			Quote:    q,
			Entity:   strings.TrimSpace(r.Entity),
			Measures: strings.TrimSpace(r.Measures),
			Context:  contextWindow(chunk, q, 160),
		})
	}
	return hits
}

// contextWindow returns the text around a figure's quote (± pad chars, trimmed to word
// boundaries) so a downstream adjudicator can see WHAT the number is — a bare "4,312" vs
// "4,217" is uninterpretable; "Total Omnibus Equity Trades Analyzed | 4,312" vs "referral
// alleges 4,217 cherry-picked trades" is decidable. Falls back to the quote if not located.
func contextWindow(chunk, quote string, pad int) string {
	i := strings.Index(chunk, quote)
	if i < 0 { // whitespace/case drift — try a loose locate on the first token of the quote
		if fields := strings.Fields(quote); len(fields) > 0 {
			i = strings.Index(chunk, fields[0])
		}
	}
	if i < 0 {
		return quote
	}
	lo, hi := i-pad, i+len(quote)+pad
	if lo < 0 {
		lo = 0
	}
	if hi > len(chunk) {
		hi = len(chunk)
	}
	w := chunk[lo:hi]
	if lo > 0 { // trim partial leading word
		if k := strings.IndexAny(w, " \n\t|"); k >= 0 {
			w = w[k+1:]
		}
	}
	if hi < len(chunk) { // trim partial trailing word
		if k := strings.LastIndexAny(w, " \n\t|"); k >= 0 {
			w = w[:k]
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(w), " "))
}

// harvestAndBindFigures sweeps EVERY chunk of EVERY ingested document for figures, BINDS each
// to the evidence-graph nodes it co-occurs with (so a figure rides along whenever its node is
// rendered), surfaces cross-source CONTRADICTIONS as high-priority findings, and returns the
// figure-bearing sentences as grounded findings for the floor. Deterministic and run-stable.
func (o *Orchestrator) harvestAndBindFigures(task *types.Task, g *evidencegraph.Graph, prov providers.Provider, figModel string) []types.Finding {
	if prov == nil || figModel == "" {
		return nil
	}
	const (
		perDocTokenCap  = 40000 // bound a pathological raw log; generous for real exhibits
		perSourceFigCap = 30    // keep each doc represented (exhibits not crowded out by narrative)
	)
	var entities []string
	if g != nil {
		entities = g.Entities()
	}
	lc := func(s string) string { return strings.ToLower(s) }

	// FULL SWEEP: every document, every chunk — tagging each hit with its source doc so a
	// discrepancy can name "[referral] vs [trading log]". No semantic retrieval gate.
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
		if len(swept) > perDocTokenCap*4 { // ~4 chars/token; bound the worst case
			swept = swept[:perDocTokenCap*4]
			slog.Info("figure sweep truncated oversized doc", "task", task.ID, "doc", title)
		}
		for _, chunk := range chunkByTokens(swept, 1500) {
			for _, h := range extractFiguresLLM(prov, figModel, chunk) {
				h.Source = title
				raw = append(raw, h)
			}
		}
	}
	if len(raw) == 0 {
		return nil
	}

	// Bind every grounded figure to graph nodes (uncapped — the graph dedups). Binding lets a
	// figure ride its node into synthesis; the graph also feeds contradiction detection.
	for _, h := range raw {
		ql := lc(h.Quote)
		for _, e := range entities {
			if e == "" {
				continue
			}
			if strings.Contains(ql, lc(e)) || (h.Entity != "" && strings.Contains(lc(h.Entity), lc(e))) {
				if g != nil {
					rel := "has associated figure"
					if h.Measures != "" {
						rel = "measures " + h.Measures
					}
					g.Add(evidencegraph.Fact{Subject: e, Relation: rel, Value: h.Value, Quote: h.Quote, Source: h.Source}, h.Quote)
				}
			}
		}
	}

	// FIX 2 — contradiction detection FIRST, so discrepancies lead the floor (uncapped, never
	// crowded out by ordinary figures). Normalize each figure to a CANONICAL quantity label so
	// same-quantity figures group across docs (the per-chunk measures labels are inconsistent;
	// embeddings cluster by topic not quantity), then adjudicate each clean group.
	o.normalizeFigures(prov, figModel, raw)
	out := o.detectContradictions(task, raw, prov, figModel)

	// FIX 1 — ordinary figure findings, balanced per source so exhibit figures get a fair share
	// of the budget rather than being buried under the narrative's figures.
	type cand struct {
		h     figureHit
		bound bool
		cite  bool
	}
	var cands []cand
	seen := map[string]bool{}
	for _, h := range raw {
		k := lc(h.Quote)
		if seen[k] {
			continue
		}
		seen[k] = true
		bound := false
		for _, e := range entities {
			if e != "" && (strings.Contains(lc(h.Quote), lc(e)) || (h.Entity != "" && strings.Contains(lc(h.Entity), lc(e)))) {
				bound = true
				break
			}
		}
		cands = append(cands, cand{h: h, bound: bound, cite: reCiteLike.MatchString(h.Value)})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return score(cands[i].bound, cands[i].cite) > score(cands[j].bound, cands[j].cite)
	})
	perSource := map[string]int{}
	for _, c := range cands {
		if perSource[c.h.Source] >= perSourceFigCap {
			continue
		}
		perSource[c.h.Source]++
		out = append(out, types.Finding{
			ID:             uuid.New().String(),
			AgentID:        "figure-harvest",
			AgentName:      "Figure Harvest",
			Content:        c.h.Quote,
			Citations:      []types.Citation{{Source: c.h.Source, Quote: c.h.Quote, MechanicallyVerified: true}},
			Confidence:     0.9,
			EvidenceStatus: types.EvidenceGrounded,
			Round:          0,
			Timestamp:      time.Now(),
		})
	}
	return out
}

// detectContradictions groups grounded figures by (entity, measures) and, where ≥2 distinct
// values exist, emits a high-priority DISCREPANCY finding the writer must foreground. This is
// the Bucket-B defense-analysis machinery: contradictions are inherent in legal work — the
// system SURFACES the conflict, it does not reconcile or paper over it.
func (o *Orchestrator) detectContradictions(task *types.Task, raw []figureHit, prov providers.Provider, model string) []types.Finding {
	// Candidates = clusters of figures describing the SAME quantity, found by EMBEDDING each
	// figure's context (what the number actually is). The figure model's free-text "measures"
	// labels are inconsistent across docs, so string-key grouping both over-selected (unrelated
	// figures sharing a generic "amount" label) and under-selected (the real cross-doc pair
	// "alleged 4,217 trades" vs "4,312 trades analyzed" never shared a key, so was never judged).
	// Semantic grouping surfaces the right candidates; the LLM then adjudicates conflict + sig.
	clusters := o.clusterFiguresByContext(raw)

	var out []types.Finding
	n, judged := 0, 0
	for _, idxs := range clusters {
		// Distinct values within the cluster (one exemplar per value).
		byVal := map[string]figureHit{}
		var vorder []string
		for _, i := range idxs {
			h := raw[i]
			if strings.TrimSpace(h.Value) == "" {
				continue
			}
			vk := figNorm(h.Value)
			if _, ok := byVal[vk]; !ok {
				byVal[vk] = h
				vorder = append(vorder, vk)
			}
		}
		// Cheap recall-safe bound only (NOT the determination): 2–8 distinct values. A long
		// ledger column clusters together and is excluded here; the LLM judges the rest.
		if len(vorder) < 2 || len(vorder) > 8 {
			continue
		}
		if judged >= 30 { // bound adjudication calls
			break
		}
		judged++
		vals := make([]figureHit, 0, len(vorder))
		for _, vk := range vorder {
			vals = append(vals, byVal[vk])
		}
		ent, meas := clusterLabel(vals)
		real, significance := o.adjudicateContradiction(prov, model, ent, meas, vals)
		if !real {
			continue
		}
		var parts []string
		var cites []types.Citation
		for _, h := range vals {
			src := h.Source
			if src == "" {
				src = "source"
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", h.Value, src))
			cites = append(cites, types.Citation{Source: h.Source, Quote: h.Quote, MechanicallyVerified: true})
		}
		ent = strings.TrimSpace(ent)
		if ent == "" {
			ent = "the matter"
		}
		sig := strings.TrimSpace(significance)
		if sig == "" {
			sig = "Surface the inconsistency and assess its significance; do not silently reconcile it."
		}
		content := fmt.Sprintf("DISCREPANCY (defense issue) — %s, %s: %s. %s",
			ent, strings.TrimSpace(meas), strings.Join(parts, "; "), sig)
		out = append(out, types.Finding{
			ID:             uuid.New().String(),
			AgentID:        "contradiction-detector",
			AgentName:      "Contradiction Detector",
			Content:        content,
			Citations:      cites,
			Confidence:     0.95,
			EvidenceStatus: types.EvidenceGrounded,
			Round:          0,
			Timestamp:      time.Now(),
		})
		n++
	}
	if judged > 0 {
		slog.Info("contradictions adjudicated", "task", task.ID, "candidates", judged, "confirmed", n)
	}
	return out
}

const contradictionJudgeSystem = "You decide whether a set of figures recorded for the SAME quantity represent a GENUINE INCONSISTENCY — the same fact reported differently across the record, a real defense issue (e.g. the referral alleges 4,217 trades but the analysis shows 4,312; a compensation total stated two different ways) — OR are LEGITIMATELY DIFFERENT values that only share a label: separate transactions or rows in a ledger, different time periods, tiered/marginal rates, distinct accounts, or sub-totals vs totals. Use the context shown for each figure. Output ONLY a JSON object: {\"contradiction\": true|false, \"significance\": \"one sentence on why the inconsistency matters for the defense (statute of limitations, scienter, exposure, credibility), or empty string if not a contradiction\"}."

// adjudicateContradiction asks the model whether a candidate group is a real inconsistency,
// using each figure's context window so it can tell what the numbers actually are. Neurosymbolic
// (grounded candidates) + LLM (judgment) — not a brittle heuristic. On parse failure it keeps
// the candidate (recall) without a significance line.
func (o *Orchestrator) adjudicateContradiction(prov providers.Provider, model, entity, measures string, vals []figureHit) (bool, string) {
	if prov == nil || model == "" {
		return true, "" // no judge available — keep, let synthesis weigh it
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Quantity: %s (concerning %s).\nFigures recorded for it:\n", strings.TrimSpace(measures), strings.TrimSpace(entity))
	for _, h := range vals {
		ctx := h.Context
		if ctx == "" {
			ctx = h.Quote
		}
		fmt.Fprintf(&b, "- %s  — context: \"%s\"  [%s]\n", h.Value, strutil.Truncate(ctx, 240), h.Source)
	}
	b.WriteString("\nIs this a genuine inconsistency or legitimately-different values?")
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   220,
		System:      contradictionJudgeSystem,
		Messages:    []providers.Message{{Role: "user", Content: b.String()}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return true, ""
	}
	o.recordCost(resp, routing.ResolveModelID(model), cost.ContextSynthesis, "")
	var text string
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			text = blk.Text
		}
	}
	lo, hi := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if lo >= 0 && hi > lo {
		var v struct {
			Contradiction bool   `json:"contradiction"`
			Significance  string `json:"significance"`
		}
		if json.Unmarshal([]byte(text[lo:hi+1]), &v) == nil {
			return v.Contradiction, v.Significance
		}
	}
	// Parse miss — fall back to a keyword read so a flaky JSON doesn't drop a real one.
	return strings.Contains(strings.ToLower(text), "true"), ""
}

type figRow struct{ Value, Entity, Measures, Quote string }

// reBareFieldVal quotes an UNQUOTED value for value/entity/measures/quote. The figure model
// routinely emits values as bare numbers (4312) or invalid tokens (801-74892, account ids),
// which makes a strict array unmarshal fail on the WHOLE chunk and silently drop every figure
// — worst on the dense numeric tables (the quantitative summary) we most need. First char
// excludes already-quoted / array / object / whitespace so we only touch bare tokens.
var reBareFieldVal = regexp.MustCompile(`("(?:value|entity|measures|quote)"\s*:\s*)([^"\s\[\]{}][^,}\n]*?)(\s*[,}])`)
var reFigObj = regexp.MustCompile(`\{[^{}]*\}`)

// parseFigureRows parses the figure JSON array, tolerating the model's bare/numeric/invalid
// value tokens: sanitize unquoted values to strings, parse the array; if that still fails,
// salvage object-by-object so one malformed row can't drop the rest.
func parseFigureRows(arr string) []figRow {
	arr = reBareFieldVal.ReplaceAllString(arr, `${1}"${2}"${3}`)
	var rows []figRow
	if json.Unmarshal([]byte(arr), &rows) == nil {
		return rows
	}
	var out []figRow
	for _, m := range reFigObj.FindAllString(arr, -1) {
		var one figRow
		if json.Unmarshal([]byte(m), &one) == nil && one.Quote != "" {
			out = append(out, one)
		}
	}
	return out
}

const figureNormSystem = "For each figure, output a CANONICAL quantity label — the underlying thing being measured, normalized so the SAME quantity gets the SAME label regardless of wording, and DIFFERENT quantities (a count vs a percentage of the same topic; the same metric for different parties) get DIFFERENT labels. Prefer a short '<thing> <quantity-type>' form (e.g. 'omnibus trade count', 'undisclosed compensation total', 'profitable allocation rate', 'review period start date'). Output ONLY a JSON array of strings — exactly one label per figure, in the SAME order."

// normalizeFigures replaces each figure's inconsistent per-chunk Measures label with a CANONICAL
// quantity label assigned by one model pass over all figures together — so the same quantity
// reported differently across documents ("alleged 4,217 trades" / "4,312 trades analyzed") gets
// the SAME label and groups, while a count and a percentage of the same topic stay distinct.
// This is the grouping fix: string-key on raw labels under-grouped, embeddings cluster by topic.
func (o *Orchestrator) normalizeFigures(prov providers.Provider, model string, raw []figureHit) {
	if prov == nil || model == "" || len(raw) == 0 {
		return
	}
	const batch = 25
	for start := 0; start < len(raw); start += batch {
		end := start + batch
		if end > len(raw) {
			end = len(raw)
		}
		var b strings.Builder
		b.WriteString("Figures:\n")
		for i := start; i < end; i++ {
			ctx := raw[i].Context
			if ctx == "" {
				ctx = raw[i].Quote
			}
			fmt.Fprintf(&b, "%d. \"%s\" — %s\n", i-start+1, raw[i].Value, strutil.Truncate(ctx, 180))
		}
		zero := 0.0
		resp, err := prov.Chat(providers.ChatParams{
			Model: model, MaxTokens: 700, System: figureNormSystem,
			Messages: []providers.Message{{Role: "user", Content: b.String()}}, CacheSystem: true, Temperature: &zero,
		})
		if err != nil {
			continue
		}
		o.recordCost(resp, routing.ResolveModelID(model), cost.ContextSynthesis, "")
		var text string
		for _, blk := range resp.Content {
			if blk.Type == providers.BlockText {
				text = blk.Text
			}
		}
		labels := parseStringArray(text)
		for k := 0; k < end-start && k < len(labels); k++ {
			if l := strings.TrimSpace(labels[k]); l != "" {
				raw[start+k].Measures = l
			}
		}
	}
}

func parseStringArray(t string) []string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "```json"), "```")
	t = strings.TrimSuffix(t, "```")
	i, j := strings.Index(t, "["), strings.LastIndex(t, "]")
	if i < 0 || j <= i {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(t[i:j+1]), &out) == nil {
		return out
	}
	return nil
}

// clusterFiguresByContext groups figures by their (now canonical) quantity label — after
// normalizeFigures, the same quantity shares a label across documents, so a string group is
// clean and precise (no embedding threshold to mis-tune).
func (o *Orchestrator) clusterFiguresByContext(raw []figureHit) [][]int {
	byKey := map[string][]int{}
	var order []string
	for i, h := range raw {
		k := figNorm(h.Measures)
		if k == "" {
			continue
		}
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], i)
	}
	out := make([][]int, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

// clusterLabel picks a representative entity + measures (most common) for a cluster's heading.
func clusterLabel(vals []figureHit) (string, string) {
	mc := map[string]int{}
	for _, h := range vals {
		if m := strings.TrimSpace(h.Measures); m != "" {
			mc[m]++
		}
	}
	best, bestN := "", 0
	for m, c := range mc {
		if c > bestN {
			best, bestN = m, c
		}
	}
	if best == "" {
		best = "the same quantity"
	}
	ent := ""
	for _, h := range vals {
		if e := strings.TrimSpace(h.Entity); e != "" {
			ent = e
			break
		}
	}
	return ent, best
}

func score(bound, cite bool) int {
	s := 0
	if bound {
		s += 2
	}
	if cite {
		s++
	}
	return s
}

func figNorm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

var reCiteLike = regexp.MustCompile(`(?i:section|rule|item|u\.s\.c)|\d+\([a-z0-9]+\)|§`)
