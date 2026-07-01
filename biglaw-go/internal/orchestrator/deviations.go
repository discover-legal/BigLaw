// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/evidencegraph"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/google/uuid"
)

// Stage 2 — DEVIATION DETECTION, the compliance analogue of contradiction detection. On an
// enforcement matter the epistemic issues are Conducts and the finding is "the Division alleges
// X"; on a compare/review matter the issues are Requirements and the finding the rubric scores is
// "the DRAFT DEVIATES from the client's INSTRUCTION on X". Describing each requirement (what the
// pipeline did) scores nothing; FINDING where the draft is wrong is the whole task. Per
// Requirement issue, retrieve the passages addressing it across the instruction memo AND the
// drafts, then adjudicate: conform, or deviate (with severity + the specific correction)?

const deviationSystem = "You compare a client's INSTRUCTIONS against a DRAFT legal document. You are given passages — each tagged with its SOURCE document — that all address ONE requirement. Decide whether the DRAFT DEVIATES from what the client INSTRUCTED (a wrong value, a wrong name, an omitted provision, a conflicting term). Output ONLY JSON: {\"deviation\": true|false, \"summary\": \"<one sentence naming the requirement, the draft's value, and the instructed value>\", \"severity\": \"critical|high|medium|low\", \"recommendation\": \"<the specific correction>\"}. State EXACT values — percentages, ages, dates, dollar amounts, names. Set deviation=false if the draft conforms or the passages don't show a conflict. Never invent a conflict."

// detectDeviations adjudicates each requirement issue for a draft-vs-instruction deviation and
// returns the confirmed ones as findings (routed to their section at synthesis + summarised).
func (o *Orchestrator) detectDeviations(task *types.Task, g *evidencegraph.Graph, prov providers.Provider, model string) []types.Finding {
	if prov == nil || model == "" || g == nil {
		return nil
	}
	reqs := g.Issues()
	if len(reqs) == 0 {
		return nil
	}
	const maxReqs = 18 // bound the (slow) spine-model adjudications; dedup near-identical labels
	var out []types.Finding
	seenReq := map[string]bool{}
	seenDev := map[string]bool{}
	adjudicated := 0
	for _, req := range reqs {
		if adjudicated >= maxReqs {
			break
		}
		key := strings.ToLower(strings.TrimSpace(req))
		if key == "" || seenReq[key] {
			continue
		}
		seenReq[key] = true
		ctx := o.retrieveForDeviation(task, req)
		if strings.TrimSpace(ctx) == "" {
			continue
		}
		adjudicated++
		dev := o.adjudicateDeviation(prov, model, req, ctx, task.ID)
		if dev == "" {
			continue
		}
		if seenDev[strings.ToLower(dev)] {
			continue
		}
		seenDev[strings.ToLower(dev)] = true
		out = append(out, types.Finding{
			ID:         uuid.NewString(),
			AgentID:    "deviation-detector",
			AgentName:  "Deviation Detector",
			Content:    dev,
			Confidence: 0.8,
			Timestamp:  time.Now(),
		})
	}
	return out
}

// retrieveForDeviation pulls passages addressing one requirement across ALL documents (so the
// instruction memo and the draft both appear), each tagged with its source so the adjudicator
// can tell instruction from draft.
func (o *Orchestrator) retrieveForDeviation(task *types.Task, req string) string {
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{"query": req, "top_k": 8}, agents.ToolContext{TaskID: task.ID})
	if err != nil {
		return ""
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		return ""
	}
	rows, _ := m["results"].([]map[string]interface{})
	var b strings.Builder
	for _, r := range rows {
		sn, _ := r["snippet"].(string)
		if strings.TrimSpace(sn) == "" {
			continue
		}
		src, _ := r["source"].(string)
		if src == "" {
			if v, ok := r["document"].(string); ok {
				src = v
			}
		}
		if src == "" {
			src = "document"
		}
		fmt.Fprintf(&b, "[%s] %s\n", src, strings.Join(strings.Fields(sn), " "))
	}
	return strutil.TruncateToTokens(b.String(), 2200)
}

func (o *Orchestrator) adjudicateDeviation(prov providers.Provider, model, req, ctx, taskID string) string {
	zero := 0.0
	resp, err := prov.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   400,
		System:      deviationSystem,
		Messages:    []providers.Message{{Role: "user", Content: "REQUIREMENT: " + req + "\n\nPASSAGES:\n" + ctx}},
		CacheSystem: true,
		Temperature: &zero,
	})
	if err != nil {
		return ""
	}
	o.recordCost(resp, model, cost.ContextSynthesis, taskID)
	var text string
	for _, blk := range resp.Content {
		if blk.Type == providers.BlockText {
			text = blk.Text
		}
	}
	t := strings.TrimSpace(text)
	i, j := strings.Index(t, "{"), strings.LastIndex(t, "}")
	if i < 0 || j <= i {
		return ""
	}
	var d struct {
		Deviation      bool   `json:"deviation"`
		Summary        string `json:"summary"`
		Severity       string `json:"severity"`
		Recommendation string `json:"recommendation"`
	}
	if json.Unmarshal([]byte(t[i:j+1]), &d) != nil || !d.Deviation || strings.TrimSpace(d.Summary) == "" {
		return ""
	}
	sev := strings.ToLower(strings.TrimSpace(d.Severity))
	if sev == "" {
		sev = "medium"
	}
	out := fmt.Sprintf("DEVIATION (%s severity) — %s", sev, strings.TrimSpace(d.Summary))
	if r := strings.TrimSpace(d.Recommendation); r != "" {
		out += " Recommended correction: " + r
	}
	return out
}
