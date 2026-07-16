// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func (o *Orchestrator) sweepQueries(prov providers.Provider, model, taskID, passages, instruction string) []string {
	prompt := fmt.Sprintf("From the passages below, %s One query per line, no numbering.\n\nPASSAGES:\n%s", instruction, passages)
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 500,
		System:      "You generate precise, entity-named search queries to locate a legal matter's specific facts and citations. Output only the queries.",
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true, Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		return nil
	}
	o.recordCost(resp, model, cost.ContextTask, taskID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	var out []string
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "-*•0123456789.) \t"))
		if len(ln) >= 4 {
			out = append(out, ln)
		}
	}
	return out
}

// allegationPassages gathers the matter's allegation-bearing passages using MULTIPLE
// complementary retrieval queries, merged and deduped. A single top-k query was the
// root cause of an entire allegation (a directed-brokerage scheme) being dropped from a
// securities matter: its passages never ranked in one query's top-k, so no specialist
// was recruited for it, no figures were swept, and no section was written. Several
// generic angles on "what is alleged" — phrasings only, never matter-specific terms —
// surface primary AND secondary allegations across every party. Returns the merged
// passages truncated to tokenBudget (empty when nothing is found).
func (o *Orchestrator) allegationPassages(task *types.Task, tokenBudget int) string {
	queries := []string{
		"allegation categories of potential violations enumerated; the referral identifies the following categories; counts of alleged violations; issues presented",
		"each distinct scheme, course of conduct, claim, or violation alleged against every named party, individual, entity, fund, or account",
		"secondary and additional allegations, separate counts, further charges, other misconduct beyond the principal claim",
		"every named individual, entity, account, fund, or third party and the specific wrongdoing, exposure, or liability attributed to it",
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, q := range queries {
		res, err := o.tools.Execute("search_chunks", map[string]interface{}{"query": q, "top_k": 8}, agents.ToolContext{TaskID: task.ID})
		if err != nil {
			continue
		}
		m, ok := res.(map[string]interface{})
		if !ok {
			continue
		}
		rows, _ := m["results"].([]map[string]interface{})
		for _, r := range rows {
			sn, _ := r["snippet"].(string)
			sn = strings.Join(strings.Fields(sn), " ")
			if sn == "" {
				continue
			}
			key := chunkKey(sn)
			if seen[key] {
				continue
			}
			seen[key] = true
			b.WriteString("- ")
			b.WriteString(sn)
			b.WriteString("\n")
		}
	}
	return strutil.TruncateToTokens(b.String(), tokenBudget)
}

// buildEvidenceGraph extracts grounded entity/relation facts from the matter's relational
// passages into a per-task Lite evidence graph, so synthesis can state relations with
// correct attribution (a "victim-of → directed-brokerage" edge can't render under cherry-
// picking) and render each party's full exposure. Two-pass, entity-anchored extraction
// (the probe showed single-pass drops parenthetical/omission facts like an ownership %);
// every fact is grounded (quote must be verbatim in its chunk) or dropped. Bounded to the
// retrieved allegation passages for now; true ingestion/per-chunk extraction is the
// follow-on once the lift is confirmed.
// reAllegationTerm scores how CONTROLLING-document-like a text is — the doc that STATES what
// must be assessed. Enforcement: accusation/charge language. Compliance/compare: instruction/
// requirement language. The controlling doc (referral, or client instruction memo) is where the
// issues are enumerated, so both vocabularies count.
var reAllegationTerm = regexp.MustCompile(`(?i)\balleg|\bviolat|\bthe division\b|\bsection\s+\d|\brule\s+\d|\bcount\s|\bfraud|\bbreach|\bscheme|\bfailed to|\brequire|\binstruct|\bshall\b|\bmust\b|\bshould\b|\bwants?\b|\bdirect`)

// chargingDocChunks pages through the matter's CHARGING document(s) — those densest in
// allegation language — up to a token budget, chunked. The conducts live in the charging doc;
// exhibits/policy docs are left to the cheaper figure sweep. This keeps the expensive spine
// pass bounded (paging the charging doc, not dumping every document). Returns nil if no doc
// yields usable text, so the caller can fall back.
func (o *Orchestrator) chargingDocChunks(task *types.Task, tokenBudget int) []string {
	type scored struct {
		text  string
		score int
	}
	var docs []scored
	for _, docID := range task.DocumentIDs {
		txt, err := o.knowledge.GetFullText(docID)
		if err != nil || strings.TrimSpace(txt) == "" {
			continue
		}
		docs = append(docs, scored{txt, len(reAllegationTerm.FindAllStringIndex(txt, -1))})
	}
	if len(docs) == 0 {
		return nil
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].score > docs[j].score })
	var out []string
	used := 0
	for _, d := range docs {
		if d.score == 0 || used >= tokenBudget {
			break // only allegation-bearing docs, only up to the budget
		}
		swept := d.text
		if maxChars := (tokenBudget - used) * 4; len(swept) > maxChars { // ~4 chars/token
			swept = swept[:maxChars]
		}
		out = append(out, chunkByTokens(swept, 1500)...)
		used += len(swept) / 4
	}
	return out
}
