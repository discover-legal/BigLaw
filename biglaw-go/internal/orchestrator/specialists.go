// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/google/uuid"
)

func (o *Orchestrator) specificsSweep(task *types.Task, prov providers.Provider, model string) []types.Finding {
	const maxFindings = 40 // figures + citations
	// Seed from the multi-query allegation merge (not one top-k query): a single query
	// under-retrieved an entire allegation, so its figures were never hunted. The merged
	// passages span every allegation — primary and secondary — and the figures live in
	// those same allegation passages.
	passages := o.allegationPassages(task, 2500)
	if strings.TrimSpace(passages) == "" {
		return nil
	}

	// Two parallel hunts: FIGURES and legal CITATIONS — distinct classes of "specific"
	// (numbers vs references) that each need their own queries, generated concurrently
	// and merged. The instructions name fact TYPES only; the actual entities and
	// citations must come from the passages at runtime, never from this prompt (so the
	// agent generalises to any matter rather than being told a particular answer).
	figInstr := "list up to 12 SPECIFIC search queries to find this matter's exact FIGURES — dollar amounts, percentages and rates, counts, dates, and account numbers. Tie each query to the specific named party, account, entity, or metric it concerns, using the actual names and terms you see in the passages. Prioritise the figures that quantify each allegation, claim, or loss."
	citeInstr := "list up to 12 SPECIFIC search queries to find this matter's exact LEGAL CITATIONS — statutory provisions and subsections, rule numbers, regulatory-form item numbers, internal policy or manual section numbers, contract clause numbers, and code sections. Tie each query to the conduct, allegation, or obligation it concerns, using the actual provisions and references you see in the passages."
	figCh := make(chan []string, 1)
	citeCh := make(chan []string, 1)
	go func() { figCh <- o.sweepQueries(prov, model, task.ID, passages, figInstr) }()
	go func() { citeCh <- o.sweepQueries(prov, model, task.ID, passages, citeInstr) }()
	merged := append(<-figCh, <-citeCh...)
	var queries []string
	qseen := map[string]bool{}
	for _, q := range merged {
		if k := strings.ToLower(q); !qseen[k] {
			qseen[k] = true
			queries = append(queries, q)
		}
	}

	return o.runSpecificsQueries(task, queries, map[string]bool{}, maxFindings, "specifics-sweep", "Specifics Sweep", 0)
}

// runSpecificsQueries executes fact-finding queries against the exhibits (extract_specifics)
// and emits each grounded, deduped snippet as a mechanically-verified finding. seen is the
// dedup set (lowercased, whitespace-collapsed snippet); callers seed it to dedupe against an
// existing pool. Shared by the round-0 specifics sweep and the round-boundary re-sweep
// (reentry.go), which targets it at newly-discovered entities.
func (o *Orchestrator) runSpecificsQueries(task *types.Task, queries []string, seen map[string]bool, maxFindings int, agentID, agentName string, round int) []types.Finding {
	var findings []types.Finding
	for _, q := range queries {
		sr, err := o.tools.Execute("extract_specifics", map[string]interface{}{"topic": q, "top_k": 4}, agents.ToolContext{TaskID: task.ID})
		if err != nil {
			continue
		}
		sm, _ := sr.(map[string]interface{})
		srows, _ := sm["results"].([]map[string]interface{})
		for _, r := range srows {
			snippet, _ := r["snippet"].(string)
			quote := strings.TrimSpace(strings.Join(strings.Fields(snippet), " "))
			if quote == "" {
				continue
			}
			key := strings.ToLower(quote)
			if seen[key] {
				continue
			}
			seen[key] = true
			src, _ := r["title"].(string)
			if src == "" {
				src, _ = r["id"].(string)
			}
			findings = append(findings, types.Finding{
				ID:             uuid.New().String(),
				AgentID:        agentID,
				AgentName:      agentName,
				Content:        quote,
				Citations:      []types.Citation{{Source: src, Quote: quote, MechanicallyVerified: true}},
				Confidence:     0.8,
				EvidenceStatus: types.EvidenceGrounded,
				Round:          round,
				Timestamp:      time.Now(),
			})
			if len(findings) >= maxFindings {
				return findings
			}
		}
	}
	return findings
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// classifyMatter classifies the matter into NOSLEGAL facets (practice area, sector,
// work type) from its DOCUMENTS — sampled passages, not the thin task description —
// so recruitment can seat the right practice specialists. Best-effort; returns empty
// tags on any failure.
func (o *Orchestrator) classifyMatter(task *types.Task, prov providers.Provider, model string) types.NosLegalTags {
	res, err := o.tools.Execute("search_chunks", map[string]interface{}{
		"query": "subject matter, parties, the legal claims and allegations, the practice area and legal doctrines at issue",
		"top_k": 6,
	}, agents.ToolContext{TaskID: task.ID})
	passages := ""
	if err == nil {
		if m, ok := res.(map[string]interface{}); ok {
			if rows, ok := m["results"].([]map[string]interface{}); ok {
				var b strings.Builder
				for _, r := range rows {
					if sn, _ := r["snippet"].(string); strings.TrimSpace(sn) != "" {
						b.WriteString(strings.Join(strings.Fields(sn), " "))
						b.WriteString("\n")
					}
				}
				passages = strutil.TruncateToTokens(b.String(), 1500)
			}
		}
	}
	prompt := fmt.Sprintf("Classify this legal matter for routing to specialist agents. Respond with ONLY valid JSON: {\"areaOfLaw\":\"<the specific practice area, e.g. Securities Regulation, Employment, M&A, Real Estate>\",\"workType\":\"<Advisory|Transactional|Litigious|Regulatory|Other>\",\"sector\":\"<the industry sector>\"}. Base it on the CONTENT, not the instruction.\n\nTASK: %s\n\nCONTENT:\n%s",
		strings.Join(strings.Fields(task.Description), " "), passages)
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 200,
		System:   "You are a legal taxonomy classifier. Output only the requested JSON.",
		Messages: []providers.Message{{Role: "user", Content: prompt}}, CacheSystem: true,
	})
	if err != nil {
		return types.NosLegalTags{}
	}
	o.recordCost(resp, model, cost.ContextClassification, task.ID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	s, e := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if s < 0 || e <= s {
		return types.NosLegalTags{}
	}
	var raw struct{ AreaOfLaw, WorkType, Sector string }
	if json.Unmarshal([]byte(text[s:e+1]), &raw) != nil {
		return types.NosLegalTags{}
	}
	tags := types.NosLegalTags{}
	if raw.AreaOfLaw != "" {
		tags.AreaOfLaw = &raw.AreaOfLaw
	}
	if raw.WorkType != "" {
		tags.WorkType = &raw.WorkType
	}
	if raw.Sector != "" {
		tags.Sector = &raw.Sector
	}
	return tags
}

// ensureSpecialists synthesises fine-grained specialist agents for the matter's
// classified practice area ON DEMAND, caching them in the agent registry (agentdb)
// for reuse. A matter is then handled by specialists tailored to its sub-specialties
// rather than whatever generic agents the registry happened to contain. First time an
// area is seen → generate + persist; thereafter → reuse. Best-effort.
func (o *Orchestrator) ensureSpecialists(area, sector, workType string, prov providers.Provider, model string, task *types.Task) {
	area = strings.TrimSpace(area)
	if area == "" {
		return
	}
	key := slugify(area)
	for _, a := range o.registry.ListAll() { // cache: already generated for this area?
		if a.Metadata != nil {
			if g, _ := a.Metadata["genArea"].(string); g == key {
				return
			}
		}
	}
	defs := o.synthesizeAgents(area, sector, workType, key, prov, model, task)
	if len(defs) == 0 {
		return
	}
	if err := o.registry.RegisterAll(defs); err == nil {
		_ = o.registry.Persist()
		slog.Info("synthesised specialist agents on demand", "area", area, "n", len(defs))
	}
}

// synthesizeAgents asks the model to design fine-grained sub-specialty analyst agents
// for a practice area (taxonomy-driven, on-demand), returning ready AgentDefinitions.
func (o *Orchestrator) synthesizeAgents(area, sector, workType, key string, prov providers.Provider, model string, task *types.Task) []types.AgentDefinition {
	ctx := ""
	if sector != "" {
		ctx += " in the " + sector + " sector"
	}
	if workType != "" {
		ctx += ", " + workType + " work"
	}
	// Ground generation in THIS matter's actual allegations, not the area's generic
	// sub-areas. Keying off the area name alone produced off-topic specialists (an
	// Insider-Trading analyst on a cherry-picking matter) that diluted the pool; the
	// EXHAUSTIVE multi-query enumeration (vs one top-k query) makes every distinct
	// allegation — primary or secondary — visible, so each gets its own specialist
	// rather than an arbitrary 5-6 collapsing onto the dominant theme.
	allegations := o.ensureAllegations(task, prov, model)
	issues := ""
	if len(allegations) > 0 {
		issues = "- " + strings.Join(allegations, "\n- ")
	}
	var prompt string
	if strings.TrimSpace(issues) != "" {
		// One specialist per distinct allegation (merging only near-duplicates), so a
		// secondary allegation is never left unstaffed. Clamp to a sane pool size.
		n := len(allegations)
		if n < 5 {
			n = 5
		}
		if n > 8 {
			n = 8
		}
		prompt = fmt.Sprintf("A legal matter in %s%s raises the SPECIFIC allegations below. Design %d specialist legal analyst agents: design ONE per distinct allegation listed (merge only near-duplicates), EACH tailored to a SPECIFIC issue, allegation, or course of conduct IN THIS MATTER — NOT generic sub-areas of the practice area, and DO NOT collapse several allegations into one analyst. Name each for the conduct it analyses (e.g. a 'Trade-Allocation Analyst' for an allocation issue, a 'Directed-Brokerage Analyst' for a brokerage-kickback issue). Respond with ONLY a JSON array; each element: {\"name\":\"<issue-specific> Analyst\",\"description\":\"<the specific issue in THIS matter it analyses>\",\"framework\":\"<a numbered analytical framework of 4-6 steps>\",\"skills\":[\"<kebab-skill>\"]}\n\nMATTER ALLEGATIONS:\n%s",
			area, ctx, n, issues)
	} else {
		prompt = fmt.Sprintf("Design 5 to 6 FINE-GRAINED specialist legal analyst agents for the practice area \"%s\"%s. Each must be a DISTINCT sub-specialty of that area. Respond with ONLY a JSON array; each element: {\"name\":\"<sub-specialty> Analyst\",\"description\":\"<one sentence>\",\"framework\":\"<a numbered analytical framework of 4-6 steps>\",\"skills\":[\"<kebab-skill>\"]}",
			area, ctx)
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model: model, MaxTokens: 2800,
		System:   "You design rigorous, specialised legal AI analyst agents. Output ONLY a JSON array, no prose before or after.",
		Messages: []providers.Message{{Role: "user", Content: prompt}}, CacheSystem: true, Temperature: o.cfg.LLMTemperature,
	})
	if err != nil {
		slog.Warn("synthesizeAgents: chat error", "area", area, "err", err)
		return nil
	}
	o.recordCost(resp, model, cost.ContextTask, task.ID)
	var text string
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			text = b.Text
		}
	}
	arr := parseAgentSpecs(text)
	if len(arr) == 0 {
		slog.Warn("synthesizeAgents: 0 specs parsed", "area", area, "respLen", len(text), "head", strutil.Truncate(strings.Join(strings.Fields(text), " "), 240))
	}
	dom := domainForWorkType(workType)
	var defs []types.AgentDefinition
	for i, a := range arr {
		name := strings.TrimSpace(a.Name)
		framework := strings.TrimSpace(string(a.Framework))
		if name == "" || framework == "" {
			continue
		}
		defs = append(defs, types.AgentDefinition{
			ID:           fmt.Sprintf("gen-%s-%d", key, i),
			Name:         name,
			Tier:         2,
			Type:         types.AgentTypeSpecialist,
			Domain:       dom,
			Description:  strings.TrimSpace(a.Description) + " Specialist in " + area + ".",
			SystemPrompt: "You are the " + name + ", a specialist in " + area + ".\n" + framework + "\nGround every finding in the matter's documents: quote verbatim evidence and cite its source.",
			AllowedTools: []string{"search_chunks", "extract_specifics", "search_knowledge", "read_document", "find_in_document", "list_documents"},
			Skills:       a.Skills,
			Metadata:     map[string]interface{}{"genArea": key, "practiceArea": area},
		})
	}
	return defs
}

type agentSpec struct {
	Name        string
	Description string
	Framework   flexText
	Skills      []string
}

// flexText accepts a JSON string OR an array of strings (a model designing a "numbered
// framework" naturally emits the steps as an array) — joining an array into a numbered
// block. Without this the whole agent object failed to unmarshal and was dropped, which
// is exactly why on-demand synthesis silently produced 0 agents.
type flexText string

func (f *flexText) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "" || t == "null" {
		return nil
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexText(s)
	case '[':
		var arr []string
		if json.Unmarshal(b, &arr) == nil {
			var sb strings.Builder
			for i, s := range arr {
				fmt.Fprintf(&sb, "%d. %s\n", i+1, strings.TrimSpace(s))
			}
			*f = flexText(strings.TrimSpace(sb.String()))
		} else {
			*f = flexText(strings.Trim(t, "[]"))
		}
	default:
		*f = flexText(t)
	}
	return nil
}

// parseAgentSpecs extracts agent specs from possibly-truncated model JSON: it tries the
// whole array first, then falls back to scanning complete top-level {...} objects — so a
// truncated final element (the 7B running out of tokens mid-array) still yields every
// complete earlier agent instead of dropping the whole batch.
func parseAgentSpecs(text string) []agentSpec {
	s := strings.Index(text, "[")
	if s < 0 {
		return nil
	}
	if e := strings.LastIndex(text, "]"); e > s {
		var arr []agentSpec
		if json.Unmarshal([]byte(text[s:e+1]), &arr) == nil && len(arr) > 0 {
			return arr
		}
	}
	var out []agentSpec
	depth, start := 0, -1
	for i := s; i < len(text); i++ {
		switch text[i] {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth--; depth == 0 && start >= 0 {
				var sp agentSpec
				if json.Unmarshal([]byte(text[start:i+1]), &sp) == nil && strings.TrimSpace(sp.Name) != "" {
					out = append(out, sp)
				}
				start = -1
			}
		}
	}
	return out
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func domainForWorkType(wt string) types.AgentDomain {
	switch strings.ToLower(strings.TrimSpace(wt)) {
	case "litigious":
		return types.DomainInvestigation
	case "regulatory":
		return types.DomainCompliance
	case "transactional":
		return types.DomainDrafting
	default:
		return types.DomainResearch
	}
}

// sweepQueries runs one query-generation call over the matter's passages with the
// given instruction (figures or citations) and returns the parsed query lines. Used
// by specificsSweep to run the figure and citation hunts concurrently.
