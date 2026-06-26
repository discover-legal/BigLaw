// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
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

// figureHit is one model-extracted figure: the value, the verbatim span that grounds it, the
// party/thing it concerns, and the source document.
type figureHit struct {
	Value  string
	Quote  string // the verbatim span (grounding)
	Source string
	Entity string // party/thing the figure concerns
}

// figureExtractSystem is the temp-0 figure-extraction prompt validated on the corpus: a small
// model (7B-class) reliably copies out figures WITH a verbatim quote and an attribution, and
// — told explicitly — catches figures inside parentheticals / "did not disclose" clauses.
const figureExtractSystem = "List every figure stated in the passage — dollar amount, percentage, count, date, account number, or statute/rule citation. INCLUDE figures inside parentheses or after 'did not disclose' / 'failed to'. For EACH figure output one JSON object with exactly: \"value\" (the figure as written), \"entity\" (the party or thing it concerns), and \"quote\" (copy the EXACT words from the passage around the figure — REQUIRED, and it must appear verbatim in the passage). Ignore paragraph/list numbers. Output ONLY a JSON array."

// extractFiguresLLM runs the small figure model at temperature 0 (deterministic) over one
// chunk and returns grounded figure hits (quote verbatim in the chunk; ungrounded rows
// dropped — the safety net against any hallucination). Returns nil on any error/parse miss.
func extractFiguresLLM(prov providers.Provider, model, chunk string) []figureHit {
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   1200,
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
	var rows []struct{ Value, Entity, Quote string }
	if json.Unmarshal([]byte(t[i:j+1]), &rows) != nil {
		return nil
	}
	nrm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	cn := nrm(chunk)
	var hits []figureHit
	for _, r := range rows {
		v, q := strings.TrimSpace(r.Value), strings.TrimSpace(r.Quote)
		if v == "" || q == "" || !strings.Contains(cn, nrm(q)) { // grounding gate
			continue
		}
		hits = append(hits, figureHit{Value: v, Quote: q, Entity: strings.TrimSpace(r.Entity)})
	}
	return hits
}

// harvestAndBindFigures scans the task's documents for figures, BINDS each to the evidence-
// graph nodes (entities) it co-occurs with — so a figure rides along whenever its node is
// rendered, and can't drift or be summarized away — and returns the figure-bearing sentences
// as grounded findings for the floor (where the writer's per-section figure-landing picks
// them up). Entity-bound sentences are prioritized over bare ones, which also filters the
// high-volume trading-log rows that name no party. Deterministic and run-stable.
func (o *Orchestrator) harvestAndBindFigures(task *types.Task, g *evidencegraph.Graph, prov providers.Provider, figModel string) []types.Finding {
	const maxFindings = 50
	var entities []string
	if g != nil {
		entities = g.Entities()
	}
	lc := func(s string) string { return strings.ToLower(s) }

	// Figure extraction is MODEL-ONLY: the small temp-0 model over the matter's allegation
	// passages (semantic, attributed, deterministic, grounded). No regex — the regex backstop
	// captured every statute cite ("Section 206", "206(1)") as a "figure" and flooded the
	// deliverable (206( ×119). The model extracts figures deliberately and attributes them.
	var raw []figureHit
	if prov != nil && figModel != "" {
		for _, chunk := range chunkByTokens(o.allegationPassages(task, 8000), 1500) {
			raw = append(raw, extractFiguresLLM(prov, figModel, chunk)...)
		}
	}

	type cand struct {
		h     figureHit
		bound bool // co-occurs with / attributed to at least one graph entity
		cite  bool // a statutory/rule/section citation (always worth keeping)
	}
	var cands []cand
	seen := map[string]bool{}
	for _, h := range raw {
		ql := lc(h.Quote)
		bound := false
		// Bind to the entity the model named (if it matches a graph node) and to any graph
		// entity co-occurring in the quote — rendering the node then renders the figure.
		for _, e := range entities {
			if e == "" {
				continue
			}
			if strings.Contains(ql, lc(e)) || (h.Entity != "" && strings.Contains(lc(h.Entity), lc(e))) {
				if g != nil {
					g.Add(evidencegraph.Fact{Subject: e, Relation: "has associated figure", Value: h.Value, Quote: h.Quote, Source: h.Source}, h.Quote)
				}
				bound = true
			}
		}
		if k := lc(h.Quote); !seen[k] { // one candidate per figure-bearing sentence
			seen[k] = true
			cands = append(cands, cand{h: h, bound: bound, cite: reCiteLike.MatchString(h.Value)})
		}
	}
	// Prioritise entity-bound and citation figures; bare figures (e.g. unattributed log rows)
	// fill any remaining budget.
	sort.SliceStable(cands, func(i, j int) bool {
		si, sj := score(cands[i].bound, cands[i].cite), score(cands[j].bound, cands[j].cite)
		return si > sj
	})
	var out []types.Finding
	for _, c := range cands {
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
		if len(out) >= maxFindings {
			break
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

var reCiteLike = regexp.MustCompile(`(?i:section|rule|item|u\.s\.c)|\d+\([a-z0-9]+\)|§`)
