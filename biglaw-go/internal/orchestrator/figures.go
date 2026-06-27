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

	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
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
		})
	}
	return hits
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
	// crowded out by ordinary figures).
	out := o.detectContradictions(task, raw)

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
func (o *Orchestrator) detectContradictions(task *types.Task, raw []figureHit) []types.Finding {
	type group struct {
		entity, measures string
		byVal            map[string]figureHit // distinct value → exemplar (first seen)
		order            []string
	}
	groups := map[string]*group{}
	for _, h := range raw {
		if h.Measures == "" || h.Value == "" {
			continue
		}
		key := figNorm(h.Entity) + "|" + figNorm(h.Measures)
		gp := groups[key]
		if gp == nil {
			gp = &group{entity: h.Entity, measures: h.Measures, byVal: map[string]figureHit{}}
			groups[key] = gp
		}
		vk := figNorm(h.Value)
		if _, ok := gp.byVal[vk]; !ok {
			gp.byVal[vk] = h
			gp.order = append(gp.order, vk)
		}
	}
	// Stable iteration order (maps are random) — sort group keys.
	var keys []string
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []types.Finding
	n := 0
	for _, k := range keys {
		gp := groups[k]
		if len(gp.order) < 2 { // no conflict
			continue
		}
		// Build "v1 (src1); v2 (src2)" and collect distinct sources + citations.
		var parts []string
		var cites []types.Citation
		srcSeen := map[string]bool{}
		for _, vk := range gp.order {
			h := gp.byVal[vk]
			src := h.Source
			if src == "" {
				src = "source"
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", h.Value, src))
			cites = append(cites, types.Citation{Source: h.Source, Quote: h.Quote, MechanicallyVerified: true})
			srcSeen[figNorm(h.Source)] = true
		}
		ent := strings.TrimSpace(gp.entity)
		if ent == "" {
			ent = "the matter"
		}
		content := fmt.Sprintf(
			"DISCREPANCY (defense issue) — %s, %s: %s. These figures conflict across the record; SURFACE the discrepancy and its defense significance — do not reconcile or silently pick one.",
			ent, strings.TrimSpace(gp.measures), strings.Join(parts, "; "))
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
		if n >= 25 { // bound noise
			break
		}
	}
	if n > 0 {
		slog.Info("contradictions detected", "task", task.ID, "n", n)
	}
	return out
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
