// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/discover-legal/biglaw-go/internal/writer"
)

func (o *Orchestrator) synthesise(task *types.Task) (string, error) {
	safeDesc := adapters.SanitizePromptContent(task.Description)
	var filteredFindings []types.Finding
	rejectedIDs := map[string]bool{}
	for _, g := range task.PendingGates {
		if g.Status == "rejected" {
			rejectedIDs[g.FindingID] = true
		}
	}
	for _, f := range task.Findings {
		if !rejectedIDs[f.ID] {
			filteredFindings = append(filteredFindings, f)
		}
	}

	// When the findings won't fit a single synthesis call's input budget, write the
	// deliverable via the scoped multi-pass writer (cluster → tight agentic drafters
	// that pull their own findings via search_findings → stitch) instead of dumping
	// every finding into one prompt — which truncates to the window and yields an
	// empty result on small-context local models. The monolith path below still
	// handles small tasks / large-context models in one clean call.
	estTokens := 0
	for _, f := range filteredFindings {
		estTokens += strutil.EstimateTokens(f.Content) + 40
	}
	if estTokens > synthesisWriterBudgetTokens {
		if out, err := o.writeDeliverable(task, filteredFindings); err == nil && strings.TrimSpace(out) != "" {
			return o.appendDiscrepancies(task, out), nil
		} else if err != nil {
			slog.Warn("multi-pass writer failed; falling back to single-call synthesis", "task", task.ID, "err", err)
		}
	}

	var lines []string
	anyFlagged := false
	for i, f := range filteredFindings {
		content := f.Content
		if len(content) > 5000 {
			content = strutil.Truncate(content, 5000)
		}
		marker := ""
		switch f.EvidenceStatus {
		case types.EvidenceUnverified, types.EvidenceUnsupported:
			anyFlagged = true
			note := f.EvidenceNote
			if note == "" {
				note = "support could not be mechanically verified"
			}
			marker = fmt.Sprintf("⚠️ UNVERIFIED — %s. Do NOT present this as established fact; if you rely on it, caveat it as unverified in the output.\n", note)
		}
		lines = append(lines, fmt.Sprintf("[%d] (%s, Round %d) %s%s", i+1, f.AgentName, f.Round, marker, content))
	}
	findingsSummary := strings.Join(lines, "\n\n")
	if len(findingsSummary) > 200_000 {
		findingsSummary = strutil.Truncate(findingsSummary, 200_000)
	}

	toneBlock := ""
	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil && p.ToneProfile != nil {
			snippet := adapters.SanitizePromptContent(p.ToneProfile.InjectionSnippet)
			if len(snippet) > 2000 {
				snippet = strutil.Truncate(snippet, 2000)
			}
			toneBlock = "\nLAWYER TONE PROFILE — write the final output in this voice:\n" + snippet + "\n"
		}
	}

	unverifiedDirective := ""
	if anyFlagged {
		unverifiedDirective = "\nSome findings are marked \"⚠️ UNVERIFIED\": their citations could not be mechanically verified against the source documents. You MUST NOT state these as established fact — either omit them or surface them with an explicit caveat (e.g. \"unverified — requires confirmation\") so the reader is warned."
	}

	prompt := fmt.Sprintf(`TASK: %s

ALL FINDINGS FROM ALL ROUNDS:
%s
%s
Produce the final legal output for this task. Structure appropriately for the workflow type: %s.
Ground every statement in the findings above — do not introduce facts, figures, or citations they do not support. Write a clean, client-ready deliverable: do NOT print internal finding numbers or IDs, bracketed references (e.g. [3] or "Finding 12"), agent names, tool names, or unfilled placeholder tokens (e.g. [Current Date], [Email Address]) — fill them in or omit them.%s`,
		safeDesc, findingsSummary, toneBlock, task.WorkflowType, unverifiedDirective)

	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskSynthesis,
	})
	// Extended thinking is model-agnostic now: a larger output budget for
	// reasoning, plus an optional reasoning_effort hint for endpoints that
	// support it. Any reasoning-capable model can use it.
	useThinking := routing.ShouldUseThinking(routing.TaskSynthesis, &tier, routing.ComplexityHigh)

	maxTokens := 4000
	if useThinking {
		maxTokens = 16000
	}

	prov, bare, err := o.synthesisModel(model)
	if err != nil {
		return "", err
	}
	chatParams := providers.ChatParams{
		Model:       bare,
		MaxTokens:   maxTokens,
		System:      o.rootAgentDef.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: prompt}},
		CacheSystem: true,
		Temperature: o.cfg.LLMTemperature,
	}
	if useThinking {
		chatParams.ReasoningEffort = o.cfg.ReasoningEffort
	}
	resp, err := prov.Chat(chatParams)
	if err != nil {
		return "", err
	}
	o.recordCost(resp, bare, cost.ContextSynthesis, task.ID)

	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return o.appendDiscrepancies(task, b.Text), nil
		}
	}
	return "", nil
}

// appendDiscrepancies guarantees the detected cross-source contradictions land in the
// deliverable. Detection is model-agnostic, but a weak drafter drops most of them when left
// to weave them into prose (7B surfaced 2 of 14; Haiku 24). So we render them mechanically as
// a dedicated section rather than trusting the writer — surfacing the conflicts is the whole
// point (defense issues), and they must not depend on synthesis quality.
func (o *Orchestrator) appendDiscrepancies(task *types.Task, body string) string {
	// BELO analytic layer: the defense issues derived from the charges (scienter element,
	// criminal exposure, statute of limitations) — the analytic reasoning the rubric asks for.
	derived := o.deriveDefenseIssues(task)

	// Figure discrepancies: cross-source value conflicts surfaced by the contradiction detector.
	var discrepancies []string
	seen := map[string]bool{}
	for _, f := range task.Findings {
		if f.AgentID != "contradiction-detector" && f.AgentID != crossDocAgentID {
			continue
		}
		c := strings.TrimSpace(f.Content)
		c = strings.TrimPrefix(c, "DISCREPANCY (defense issue) — ")
		if i := strings.Index(c, ". These figures conflict"); i > 0 {
			c = strings.TrimSpace(c[:i])
		}
		if c == "" || seen[strings.ToLower(c)] {
			continue
		}
		seen[strings.ToLower(c)] = true
		discrepancies = append(discrepancies, "- "+c+".")
	}

	// Deviations: draft-vs-instruction conflicts from the deviation detector (compliance/compare
	// matters). These are the finding such tasks are scored on.
	var deviations []string
	seenDev := map[string]bool{}
	for _, f := range task.Findings {
		if f.AgentID != "deviation-detector" {
			continue
		}
		c := strings.TrimSpace(f.Content)
		if c == "" || seenDev[strings.ToLower(c)] {
			continue
		}
		seenDev[strings.ToLower(c)] = true
		deviations = append(deviations, "- "+c)
	}

	if len(derived) == 0 && len(discrepancies) == 0 && len(deviations) == 0 {
		return body
	}
	if len(deviations) > 0 {
		body = strings.TrimRight(body, "\n") +
			"\n\n## Deviations Identified\n\nWhere the draft documents deviate from the client's instructions — each should be corrected:\n\n" +
			strings.Join(deviations, "\n") + "\n"
	}
	if len(derived) == 0 && len(discrepancies) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n## Discrepancies and Defense Issues\n\n")
	if len(derived) > 0 {
		b.WriteString("Defense issues raised by the charges and the record — elements that must be proven, exposure beyond the civil counts, and timing defenses:\n\n")
		for _, d := range derived {
			b.WriteString("- ")
			b.WriteString(d)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(discrepancies) > 0 {
		b.WriteString("The following figures conflict across the record. Each is a potential defense point — the inconsistency should be raised and its significance assessed, not silently reconciled:\n\n")
		b.WriteString(strings.Join(discrepancies, "\n"))
		b.WriteString("\n")
	}
	return b.String()
}

// synthesisWriterBudgetTokens is the per-call input budget for synthesis: when the
// findings exceed it, the multi-pass writer is used instead of one monolithic call.
// Sized to sit comfortably inside a small local window (e.g. 8K) alongside the
// system prompt and output budget.
const synthesisWriterBudgetTokens = 5000

// writeDeliverable produces the final output via the scoped multi-pass writer: it
// maps findings into the writer's view, builds a Writer over the synthesis model,
// and lets it cluster → draft (tight agentic sub-agents, search_findings scoped per
// section) → stitch. Used when findings overflow a single synthesis call.
// localize prepends the local-inference prefix to a bare model id when the registry serves
// local models, so an admin can pick "qwen2.5:14b" in the panel and it routes to the LOCAL
// provider (not the cloud stack, which Get() would otherwise select). A value already prefixed
// (local:/ollama:) is left as-is, so env knobs may pass either form.
func (o *Orchestrator) localize(m string) string {
	m = strings.TrimSpace(m)
	if m == "" || routing.IsOllamaModel(m) || routing.IsLocalModel(m) {
		return m
	}
	if o.cfg.Local.LocalInferenceURL != "" {
		return "local:" + m
	}
	if o.cfg.Local.OllamaEnabled {
		return "ollama:" + m
	}
	return m
}

// synthesisModel resolves the provider + bare model for synthesis/drafting, honouring the
// SYNTHESIS_MODEL knob (route ONLY the judged-memo step to a stronger local model, e.g. 14B,
// while the high-volume bulk stays on the fast 7B) and falling back to the routed default.
func (o *Orchestrator) synthesisModel(routed string) (providers.Provider, string, error) {
	use := routed
	if sm := o.localize(o.cfg.Models.SynthesisModel); sm != "" {
		if _, err := o.provReg.Get(sm); err == nil {
			use = sm
		} else {
			slog.Warn("SYNTHESIS_MODEL provider unavailable; using routed default", "synthesis_model", sm, "err", err)
		}
	}
	prov, err := o.provReg.Get(use)
	return prov, routing.ResolveModelID(use), err
}

func (o *Orchestrator) writeDeliverable(task *types.Task, findings []types.Finding) (string, error) {
	tier := types.TierRoot
	model := routing.SelectModel(o.cfg, routing.SelectParams{Tier: &tier, TaskType: routing.TaskSynthesis})
	prov, bare, err := o.synthesisModel(model)
	if err != nil {
		return "", err
	}

	wf := make([]writer.Finding, 0, len(findings))
	for _, f := range findings {
		item := writer.Finding{
			ID:       f.ID,
			Content:  f.Content,
			Agent:    f.AgentName,
			Round:    f.Round,
			Grounded: f.EvidenceStatus == types.EvidenceGrounded,
			Note:     f.EvidenceNote,
		}
		if len(f.Citations) > 0 {
			item.Evidence = f.Citations[0].Quote
			item.Source = f.Citations[0].Source
		}
		wf = append(wf, item)
	}

	// Lawyer tone → writer persona (same source as the monolith path).
	persona := ""
	primaryProfileID := task.CreatedByProfileID
	if primaryProfileID == "" && len(task.AssignedLawyerIDs) > 0 {
		primaryProfileID = task.AssignedLawyerIDs[0]
	}
	if primaryProfileID != "" {
		if p := o.profiles.Get(primaryProfileID); p != nil && p.ToneProfile != nil {
			snippet := adapters.SanitizePromptContent(p.ToneProfile.InjectionSnippet)
			if len(snippet) > 2000 {
				snippet = strutil.Truncate(snippet, 2000)
			}
			persona = "Write in this lawyer's voice:\n" + snippet
		}
	}

	// Temperature 0 was tried and backfired: greedy decoding favours generic
	// high-probability legal prose and STRIPS specific figures (lower-probability
	// tokens). Keep the configured sampling temperature for figure-rich narrative;
	// figure landing is guaranteed mechanically in the writer instead (Key figures).
	w := writer.New(o.embedC, prov, bare, writer.Options{
		Temperature:       o.cfg.LLMTemperature,
		InputBudgetTokens: synthesisWriterBudgetTokens,
		Persona:           persona,
		// Coverage spine: the matter's own enumerated topics become guaranteed
		// sections, so no required allegation category vanishes through clustering.
		RequiredSections: o.extractCoverageSpine(task, prov, bare),
		// Alias map per spine section (the merged allegation's alternate surface forms), so
		// fact routing matches a fact phrased like ANY variant, not just the canonical heading.
		SectionAliases: o.allegationAliases(task.ID),
		// Paged synthesis: sections composed with compact-when-done / uncompact-on-demand,
		// assembled losslessly. With DyTopoDrafting on, each section is written by a bounded
		// writing huddle (lead + contributors, draft→critique→revise) run concurrently, then
		// composed by this paged pass.
		WriterSystem:   o.writingAgentSystem(task),
		Paged:          true,
		DyTopoDrafting: o.cfg.Drafting.DyTopo,
		DraftingAgents: o.draftingAgentVoices(task),
		DraftingRounds: o.cfg.Drafting.Rounds,
		// Evidence-graph facts: routed per-section (by entity/allegation overlap) so each
		// author states its relations with correct attribution — no whole-ledger crowding.
		// Gate BIGLAW_FACTS_GLOBAL=1 reverts to whole-ledger injection for A/B.
		Facts:       o.groundedFacts(task.ID),
		FactsGlobal: os.Getenv("BIGLAW_FACTS_GLOBAL") == "1" || os.Getenv("BIGLAW_FACTS_GLOBAL") == "true",
		// Named individual respondents (committedBy → Person claims): the writer enforces
		// one exposure entry per respondent — consolidated record or explicit gap note.
		Respondents: o.respondentRoster(task.ID),
		RecordCost:  func(resp *providers.ChatResponse) { o.recordCost(resp, bare, cost.ContextSynthesis, task.ID) },
		// Synthesis-time figure handling: drafters pull exact figures for their
		// section from the source exhibits on demand (document-backed
		// extract_specifics), rather than every agent pre-stuffing figures into
		// findings (which floods the writer). Backed by the tool registry's RAG.
		Specifics: func(topic string, topK int) []writer.SpecificHit {
			res, err := o.tools.Execute("extract_specifics", map[string]interface{}{"topic": topic, "top_k": topK}, agents.ToolContext{TaskID: task.ID})
			if err != nil {
				return nil
			}
			m, ok := res.(map[string]interface{})
			if !ok {
				return nil
			}
			rows, _ := m["results"].([]map[string]interface{})
			hits := make([]writer.SpecificHit, 0, len(rows))
			for _, r := range rows {
				sn, _ := r["snippet"].(string)
				if strings.TrimSpace(sn) == "" {
					continue
				}
				src, _ := r["title"].(string)
				if src == "" {
					src, _ = r["id"].(string)
				}
				ctx, _ := r["context"].(string)
				hits = append(hits, writer.SpecificHit{Text: sn, Source: src, Context: ctx})
			}
			return hits
		},
	})
	return w.Write(adapters.SanitizePromptContent(task.Description), string(task.WorkflowType), wf)
}

// specificsSweep runs at task START (intent steering): it retrieves the matter's
// figure-dense passages, has the model enumerate TARGETED fact-finding queries —
// entity-aware, since the passages name the people/accounts/funds/metrics — runs
// each against the exhibits, and emits the exact figures as grounded findings. This
// gets the specifics that the rounds' conceptual queries would miss (rates, account
// numbers, counts, percentages) into the finding pool from round 1, so the whole
// pipeline (and synthesis) is aware of them. Bounded + deduped, so no finding flood.
