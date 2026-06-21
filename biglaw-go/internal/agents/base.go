// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package agents

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/adapters"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// AgentContext carries round context into each agent's processing loop.
type AgentContext struct {
	RoundGoal        types.RoundGoal
	IncomingMessages []types.AgentMessage
	MemoryEntries    []types.MemoryEntry
	TaskDescription  string
	TaskID           string
	// DocumentIndex is a short, sanitized list of the task's documents (title + ID)
	// — the MAP of what is on the matter, not the territory. Agents pull verbatim
	// passages on demand via the search_knowledge tool, which keeps a small model's
	// context window lean and keeps quoting on the tool-calling path where the
	// citation gate verifies it. Empty when the task has no documents.
	DocumentIndex         string
	ToolRegistry          ToolRegistry
	KnowledgeStore        KnowledgeStore
	MemoryStore           MemoryStore
	OwnerID               string
	AssignedLawyerTone    *types.ToneProfile
	ResponsibleLawyerID   string
	ResponsibleLawyerName string
	MatterNumber          string
	ClientNumber          string
}

// ToolRegistry is the interface agents use to discover and execute tools.
type ToolRegistry interface {
	SchemasFor(toolNames []string) []providers.ToolParam
	Execute(name string, input map[string]interface{}, ctx ToolContext) (interface{}, error)
}

// KnowledgeStore is the interface agents use to search documents.
type KnowledgeStore interface {
	Search(query string, ownerID string, topK int) ([]types.SearchResult, error)
	GetFullText(docID string) (string, error)
	GetByID(docID string) *types.Document
}

// MemoryStore is the interface agents use to query inter-round memory.
type MemoryStore interface {
	Query(query string, taskID string, agentID string, beforeRound int, topK int) ([]types.MemoryEntry, error)
}

// ToolContext is forwarded from the agent into each tool call.
type ToolContext struct {
	KnowledgeStore      KnowledgeStore
	MemoryStore         MemoryStore
	TaskID              string
	OwnerID             string
	ResponsibleLawyerID string
}

// Agent wraps an AgentDefinition and runs the agentic loop.
type Agent struct {
	Def       types.AgentDefinition
	cfg       *config.Config
	providers *providers.Registry
	costs     *cost.Store
}

func NewAgent(def types.AgentDefinition, cfg *config.Config, prov *providers.Registry, costs *cost.Store) *Agent {
	return &Agent{Def: def, cfg: cfg, providers: prov, costs: costs}
}

// GenerateNeedOffer produces the per-round Need/Offer descriptors (Haiku).
func (a *Agent) GenerateNeedOffer(ctx AgentContext) (types.NeedDescriptor, types.OfferDescriptor, error) {
	tier := a.Def.Tier
	model := routing.SelectModel(a.cfg, routing.SelectParams{
		Tier:     &tier,
		TaskType: routing.TaskDescriptor,
	})
	prompt := buildNeedOfferPrompt(a.Def, ctx)
	resp, err := a.callModel(prompt, 200, model, ctx.TaskID, cost.ContextDescriptor)
	if err != nil {
		return types.NeedDescriptor{}, types.OfferDescriptor{}, err
	}
	need, offer := parseNeedOffer(resp, a.Def.ID)
	return need, offer, nil
}

// Process runs the agent's full agentic loop and returns findings.
func (a *Agent) Process(ctx AgentContext) ([]types.Finding, error) {
	startedAt := time.Now()

	if ctx.TaskID != "" {
		audit.Default.Write(audit.WriteRequest{
			Event:   "agent.processing",
			ActorID: orSystem(ctx.ResponsibleLawyerID),
			TaskID:  ctx.TaskID,
			AgentID: a.Def.ID,
			Data: map[string]interface{}{
				"agentName": a.Def.Name,
				"tier":      a.Def.Tier,
				"domain":    a.Def.Domain,
				"round":     ctx.RoundGoal.Round,
			},
		})
	}

	tier := a.Def.Tier
	taskType := inferTaskType(a.Def)
	complexity := routing.EstimateComplexity(ctx.RoundGoal.Description)
	model := routing.SelectModel(a.cfg, routing.SelectParams{
		Tier:       &tier,
		AgentType:  &a.Def.Type,
		TaskType:   taskType,
		Complexity: complexity,
	})

	prompt := buildProcessingPrompt(a.Def, ctx)
	maxTokens := 2500
	if a.Def.Tier == types.TierTool {
		maxTokens = 600
	} else if a.Def.Tier == types.TierRoot {
		maxTokens = 4000
	}

	// Grant the document-retrieval tools to every finding-producing agent when
	// the matter has documents, so grounding never depends on the agent
	// definition's own tool list (many ship none, and a no-tool agent can only
	// paraphrase from the document titles).
	allowed := a.Def.AllowedTools
	if a.cfg.Agents.GrantRetrievalTools && strings.TrimSpace(ctx.DocumentIndex) != "" {
		allowed = mergeTools(allowed, retrievalTools)
	}

	hasTools := ctx.ToolRegistry != nil && ctx.KnowledgeStore != nil &&
		ctx.MemoryStore != nil && ctx.TaskID != "" && len(allowed) > 0

	var findings []types.Finding
	if hasTools {
		passages, loopText, lerr := a.runAgenticLoop(prompt, maxTokens, model, ctx, allowed)
		if lerr != nil {
			return nil, lerr
		}
		// Staged finding generation: when the agent has retrieved source, transcribe
		// evidence and analyse it as SEPARATE calls (extract → analyse). The
		// monolithic loop entangles verbatim transcription with analysis in one
		// context, and a small model paraphrases under that load; staging keeps the
		// evidence verbatim by construction. With no retrieval (e.g. no documents),
		// fall back to parsing the loop's own output.
		if len(passages) > 0 {
			findings = a.stagedFindings(ctx, passages, model)
		} else {
			findings = parseFindings(loopText, a.Def)
		}
	} else {
		text, cerr := a.callModel(prompt, maxTokens, model, ctx.TaskID, cost.ContextTask)
		if cerr != nil {
			return nil, cerr
		}
		findings = parseFindings(text, a.Def)
	}
	for i := range findings {
		findings[i].Round = ctx.RoundGoal.Round
	}

	if ctx.TaskID != "" {
		for _, f := range findings {
			audit.Default.Write(audit.WriteRequest{
				Event:   "finding.produced",
				ActorID: orSystem(ctx.ResponsibleLawyerID),
				TaskID:  ctx.TaskID,
				AgentID: a.Def.ID,
				Data: map[string]interface{}{
					"findingId":      f.ID,
					"agentName":      a.Def.Name,
					"confidence":     f.Confidence,
					"round":          ctx.RoundGoal.Round,
					"contentPreview": strutil.Truncate(f.Content, 150),
				},
			})
		}
		audit.Default.Write(audit.WriteRequest{
			Event:      "agent.complete",
			ActorID:    orSystem(ctx.ResponsibleLawyerID),
			TaskID:     ctx.TaskID,
			AgentID:    a.Def.ID,
			DurationMs: ptr(time.Since(startedAt).Milliseconds()),
			Data: map[string]interface{}{
				"agentName":    a.Def.Name,
				"findingCount": len(findings),
				"round":        ctx.RoundGoal.Round,
			},
		})
	}
	return findings, nil
}

// ─── Agentic loop ─────────────────────────────────────────────────────────────

// runAgenticLoop drives the agent to RETRIEVE the matter's documents via
// search_knowledge and returns the verbatim passages it pulled (deduped). It does
// NOT produce findings — those come from the staged extract→analyse path. finalText
// is the model's own output, kept only as a fallback for the no-retrieval case.
func (a *Agent) runAgenticLoop(initialPrompt string, maxTokens int, model string, ctx AgentContext, allowed []string) ([]retrievedPassage, string, error) {
	toolSchemas := ctx.ToolRegistry.SchemasFor(allowed)
	toolCtx := ToolContext{
		KnowledgeStore:      ctx.KnowledgeStore,
		MemoryStore:         ctx.MemoryStore,
		TaskID:              ctx.TaskID,
		OwnerID:             ctx.OwnerID,
		ResponsibleLawyerID: ctx.ResponsibleLawyerID,
	}

	prov, err := a.providers.Get(model)
	if err != nil {
		return nil, "", err
	}
	bareModel := routing.ResolveModelID(model)

	msgs := []providers.Message{{Role: "user", Content: initialPrompt}}
	var finalText string
	retrieved := false
	var passages []retrievedPassage
	seen := map[string]bool{}
	hasDocs := strings.TrimSpace(ctx.DocumentIndex) != ""

	for iteration := 0; iteration < a.cfg.Agents.MaxToolIterations; iteration++ {
		resp, err := prov.Chat(providers.ChatParams{
			Model:       bareModel,
			MaxTokens:   maxTokens,
			System:      a.Def.SystemPrompt,
			Tools:       toolSchemas,
			Messages:    msgs,
			CacheSystem: true,
			Temperature: a.cfg.LLMTemperature,
		})
		if err != nil {
			return passages, finalText, err
		}

		a.recordCost(resp, model, cost.ContextTask, ctx.TaskID)

		for _, block := range resp.Content {
			if block.Type == providers.BlockText {
				finalText = block.Text
			}
		}

		if resp.StopReason == providers.StopEndTurn {
			// Nudge a weaker model back to the tools if it tries to finish without
			// retrieving — staging needs passages to extract evidence from.
			if a.cfg.Agents.RequireRetrieval && hasDocs && !retrieved &&
				iteration < a.cfg.Agents.MaxToolIterations-1 {
				msgs = append(msgs, providers.Message{Role: "assistant", Content: resp.Content})
				msgs = append(msgs, providers.Message{Role: "user", Content: "Call search_chunks now to retrieve relevant passages from the matter's documents before finishing."})
				continue
			}
			break
		}

		if resp.StopReason == providers.StopToolUse {
			msgs = append(msgs, providers.Message{Role: "assistant", Content: resp.Content})

			var toolResults []providers.ContentBlock
			for _, block := range resp.Content {
				if block.Type != providers.BlockToolUse {
					continue
				}
				var result interface{}
				if !contains(allowed, block.Name) {
					result = map[string]string{"error": fmt.Sprintf("tool '%s' not permitted", block.Name)}
				} else {
					result, err = ctx.ToolRegistry.Execute(block.Name, block.Input, toolCtx)
					if err != nil {
						result = map[string]string{"error": err.Error()}
					}
					if isRetrievalTool(block.Name) {
						retrieved = true
						for _, p := range extractPassages(result) {
							if !seen[p.text] {
								seen[p.text] = true
								passages = append(passages, p)
							}
						}
					}
				}
				raw, _ := json.Marshal(result)
				content := string(raw)
				if maxTok := a.cfg.Agents.MaxToolResultTokens; maxTok > 0 {
					if trimmed := strutil.TruncateToTokens(content, maxTok); len(trimmed) < len(content) {
						content = trimmed + "…[truncated]"
					}
				}
				toolResults = append(toolResults, providers.ContentBlock{
					Type:      providers.BlockToolResult,
					ToolUseID: block.ID,
					Content:   content,
				})
			}
			msgs = append(msgs, providers.Message{Role: "user", Content: toolResults})
			continue
		}
		break
	}
	return passages, finalText, nil
}

// retrievedPassage is one verbatim snippet the agent pulled, tagged with the
// document it came from (for the SOURCE= field + citation-gate resolution).
type retrievedPassage struct {
	source  string // document title/id
	text    string // verbatim snippet (what may be quoted — gate-safe)
	context string // optional: a table row's sheet + column headers, for understanding only
}

// extractPassages pulls the verbatim snippets out of a retrieval tool's result.
// Returns nil for tools that don't carry document snippets.
func extractPassages(result interface{}) []retrievedPassage {
	m, ok := result.(map[string]interface{})
	if !ok {
		return nil
	}
	rows, ok := m["results"].([]map[string]interface{})
	if !ok {
		return nil
	}
	var out []retrievedPassage
	for _, r := range rows {
		sn, _ := r["snippet"].(string)
		sn = strings.TrimSpace(sn)
		if sn == "" {
			continue
		}
		src, _ := r["title"].(string)
		if src == "" {
			if id, _ := r["id"].(string); id != "" {
				src = id
			} else {
				src = "source"
			}
		}
		ctx, _ := r["context"].(string)
		out = append(out, retrievedPassage{source: src, text: sn, context: strings.TrimSpace(ctx)})
	}
	return out
}

// ─── Staged finding generation: extract → analyse ──────────────────────────────
//
// A finding is a verbatim FACT (evidence) plus an INTERPRETATION (conclusion) —
// opposite cognitive modes. Generating both in one call lets a small model's
// "summariser" prior corrupt the transcription, so evidence comes out paraphrased
// (0% verbatim in the pipeline). Staging fixes it by construction:
//
//	EXTRACT (finer: one lean, persona-free, transcription-only call PER passage):
//	  copy the verbatim sentences relevant to the task. Each quote is verified to
//	  be a substring of its passage and LOCKED.
//	ANALYSE (fan-in, one call): write a conclusion per LOCKED quote, keyed by
//	  index. The model never re-emits the quote, so the evidence stays verbatim.
//
// Extraction is sequential for now (a single local GPU serialises requests
// anyway); the per-passage shape parallelises trivially on better hardware.
const extractSystemPrompt = "You are a verbatim evidence extractor. You copy exact sentences out of a source passage, character-for-character. You never paraphrase, summarise, interpret, shorten, or add words. You only transcribe."

const maxEvidencePerAgent = 8

// maxEvidencePassages caps how many retrieved passages go into the single batched
// extraction call, keeping the transcription prompt within the context window on
// small local models.
const maxEvidencePassages = 8

type extractedEvidence struct{ quote, source string }

var (
	// reQuoteLine matches "[n] QUOTE: ..." (n optional). Group 1 = passage index
	// (may be empty), group 2 = the quoted text.
	reQuoteLine = regexp.MustCompile(`(?im)^\s*(?:\[(\d+)\]\s*)?(?:[-*]\s*)?QUOTE:\s*(.+?)\s*$`)
	reConclLine = regexp.MustCompile(`(?im)^\s*\[?(\d+)\]?[.):\s-]*(?:Conclusion\s*:\s*)?(.+\S)\s*$`)
)

func (a *Agent) stagedFindings(ctx AgentContext, passages []retrievedPassage, model string) []types.Finding {
	focus := oneLine(ctx.TaskDescription)
	if rg := strings.TrimSpace(ctx.RoundGoal.Description); rg != "" {
		focus += " — " + oneLine(rg)
	}

	// Stage 1 — EXTRACT: one batched transcription call over all passages (a single
	// call so agents finish within the round timeout on local models).
	evidence := a.extractEvidenceBatch(focus, passages, model, ctx.TaskID)
	if len(evidence) == 0 {
		return nil
	}

	// Stage 2 — ANALYSE (fan-in): conclusion per LOCKED quote, by index.
	quotes := make([]string, len(evidence))
	sources := make([]string, len(evidence))
	for i, e := range evidence {
		quotes[i], sources[i] = e.quote, e.source
	}
	conclusions := a.analyseEvidence(ctx, quotes, sources, model)

	findings := make([]types.Finding, 0, len(evidence))
	for i, e := range evidence {
		concl := strings.TrimSpace(conclusions[i])
		if concl == "" {
			concl = "Evidence on point for this matter; see the quoted source."
		}
		findings = append(findings, types.Finding{
			ID:             uuid.New().String(),
			AgentID:        a.Def.ID,
			AgentName:      a.Def.Name,
			Content:        concl,
			Citations:      []types.Citation{{Source: e.source, Quote: e.quote, MechanicallyVerified: true}},
			Confidence:     0.8,
			EvidenceStatus: types.EvidenceGrounded,
			Timestamp:      time.Now(),
		})
	}
	return findings
}

// extractEvidenceBatch runs ONE lean, persona-free transcription call over ALL
// retrieved passages at once and returns the verbatim sentences it copied, each
// verified to be a substring of a source passage (anything paraphrased is
// dropped — grounding by construction). Batching keeps each agent to a single
// extraction call so it finishes within the round timeout on a local model.
func (a *Agent) extractEvidenceBatch(focus string, passages []retrievedPassage, model, taskID string) []extractedEvidence {
	prov, err := a.providers.Get(model)
	if err != nil || len(passages) == 0 {
		return nil
	}
	if len(passages) > maxEvidencePassages {
		passages = passages[:maxEvidencePassages]
	}
	var b strings.Builder
	for i, p := range passages {
		if p.context != "" {
			// Table row: show the column context so the model understands a cryptic
			// row, but it copies only the row text (the substring check below verifies
			// the quote against p.text, not the context).
			fmt.Fprintf(&b, "PASSAGE %d (table — %s):\n%s\n\n", i+1, p.context, p.text)
		} else {
			fmt.Fprintf(&b, "PASSAGE %d:\n%s\n\n", i+1, p.text)
		}
	}
	user := fmt.Sprintf("Task focus: %s\n\nBelow are %d source PASSAGES. From EACH passage, copy out up to 2 complete sentences OR table/data rows, WORD-FOR-WORD, that are most relevant to the task focus — INCLUDE specific figures, amounts, dates, percentages, account numbers, and citations exactly as written. Copy character-for-character — do not paraphrase, summarise, shorten, or fix anything. For a table row, copy the row text itself (not the parenthetical column context). Put each on its own line, prefixed with its passage number like:\n[1] QUOTE: <exact text>\nSkip any passage with nothing relevant. Output only QUOTE lines.\n\n%s", focus, len(passages), b.String())
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   1200,
		System:      extractSystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		Temperature: a.cfg.LLMTemperature,
	})
	if err != nil {
		return nil
	}
	a.recordCost(resp, model, cost.ContextTask, taskID)
	var text string
	for _, bl := range resp.Content {
		if bl.Type == providers.BlockText {
			text = bl.Text
		}
	}
	npass := make([]string, len(passages))
	for i, p := range passages {
		npass[i] = normalizeWS(p.text)
	}
	var out []extractedEvidence
	seenQ := map[string]bool{}
	for _, m := range reQuoteLine.FindAllStringSubmatch(text, -1) {
		q := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[2]), `"`))
		if q == "" {
			continue
		}
		nq := normalizeWS(q)
		if seenQ[nq] {
			continue
		}
		// Verify against the tagged passage first, then any passage; drop if it is
		// not a verbatim substring anywhere (paraphrase guard).
		src := ""
		if idx, e := strconv.Atoi(m[1]); e == nil && idx >= 1 && idx <= len(passages) && strings.Contains(npass[idx-1], nq) {
			src = passages[idx-1].source
		} else {
			for j := range npass {
				if strings.Contains(npass[j], nq) {
					src = passages[j].source
					break
				}
			}
		}
		if src == "" {
			continue
		}
		seenQ[nq] = true
		out = append(out, extractedEvidence{quote: q, source: src})
		if len(out) >= maxEvidencePerAgent {
			break
		}
	}
	return out
}

// analyseEvidence runs one fan-in call that writes a conclusion per locked quote,
// keyed by index. Returns conclusions aligned to the input order.
func (a *Agent) analyseEvidence(ctx AgentContext, quotes, sources []string, model string) []string {
	out := make([]string, len(quotes))
	prov, err := a.providers.Get(model)
	if err != nil {
		return out
	}
	var b strings.Builder
	for i := range quotes {
		fmt.Fprintf(&b, "[%d] (%s) %s\n", i+1, sources[i], oneLine(quotes[i]))
	}
	user := fmt.Sprintf("TASK: %s\nROUND GOAL (Round %d — %s): %s\n\nBelow are verbatim EVIDENCE quotes already extracted from the matter's documents. For EACH numbered item, write ONE concise CONCLUSION — your legal analysis of what that evidence shows for the task. Do NOT alter, re-quote, or merge the evidence; analyse each item on its own. Output exactly one line per item:\n[1] Conclusion: <your analysis>\n[2] Conclusion: <your analysis>\n\nEVIDENCE:\n%s",
		oneLine(ctx.TaskDescription), ctx.RoundGoal.Round, ctx.RoundGoal.Phase, oneLine(ctx.RoundGoal.Description), b.String())
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   1500,
		System:      a.Def.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: user}},
		CacheSystem: true,
		Temperature: a.cfg.LLMTemperature,
	})
	if err != nil {
		return out
	}
	a.recordCost(resp, model, cost.ContextTask, ctx.TaskID)
	var text string
	for _, bl := range resp.Content {
		if bl.Type == providers.BlockText {
			text = bl.Text
		}
	}
	for _, m := range reConclLine.FindAllStringSubmatch(text, -1) {
		idx, e := strconv.Atoi(m[1])
		if e == nil && idx >= 1 && idx <= len(out) && strings.TrimSpace(out[idx-1]) == "" {
			out[idx-1] = strings.TrimSpace(m[2])
		}
	}
	return out
}

// oneLine collapses any run of whitespace (incl. newlines) to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// normalizeWS lowercases and collapses whitespace, for verbatim substring checks.
func normalizeWS(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// ─── callModel (single-shot) ──────────────────────────────────────────────────

func (a *Agent) callModel(userMsg string, maxTokens int, model string, taskID string, ctx cost.CostContext) (string, error) {
	prov, err := a.providers.Get(model)
	if err != nil {
		return "", err
	}
	resp, err := prov.Chat(providers.ChatParams{
		Model:       routing.ResolveModelID(model),
		MaxTokens:   maxTokens,
		System:      a.Def.SystemPrompt,
		Messages:    []providers.Message{{Role: "user", Content: userMsg}},
		CacheSystem: true,
		Temperature: a.cfg.LLMTemperature,
	})
	if err != nil {
		return "", err
	}
	a.recordCost(resp, model, ctx, taskID)
	for _, b := range resp.Content {
		if b.Type == providers.BlockText {
			return b.Text, nil
		}
	}
	return "", fmt.Errorf("no text in response from %s", model)
}

// ─── Cost recording ───────────────────────────────────────────────────────────

func (a *Agent) recordCost(resp *providers.ChatResponse, modelID string, ctx cost.CostContext, taskID string) {
	isLocal := routing.IsOllamaModel(modelID) || routing.IsLocalModel(modelID)
	bare := routing.ResolveModelID(modelID)

	var costUSD *float64
	var wh *float64
	var watts *int

	if !isLocal {
		cw, cr := 0, 0
		if resp.Usage.CacheWriteTokens != nil {
			cw = *resp.Usage.CacheWriteTokens
		}
		if resp.Usage.CacheReadTokens != nil {
			cr = *resp.Usage.CacheReadTokens
		}
		costUSD = cost.CalcCostUSD(bare, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	} else {
		w := cost.CalcWattHours(a.cfg.Local.InferenceWatts, resp.DurationMs)
		wh = &w
		watts = &a.cfg.Local.InferenceWatts
	}

	provider := "anthropic"
	if routing.IsOllamaModel(modelID) {
		provider = "ollama"
	} else if routing.IsLocalModel(modelID) {
		provider = "local"
	}

	a.costs.Record(cost.RecordRequest{
		Model:            bare,
		Provider:         provider,
		InputTokens:      resp.Usage.InputTokens,
		OutputTokens:     resp.Usage.OutputTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens,
		CacheReadTokens:  resp.Usage.CacheReadTokens,
		CostUSD:          costUSD,
		EstimatedWh:      wh,
		EstimatedWatts:   watts,
		DurationMs:       resp.DurationMs,
		Context:          ctx,
		TaskID:           taskID,
		AgentID:          a.Def.ID,
	})
}

// ─── Prompt builders ──────────────────────────────────────────────────────────

func buildNeedOfferPrompt(def types.AgentDefinition, ctx AgentContext) string {
	taskDesc := sanitize(ctx.TaskDescription)
	mem := "None yet."
	if len(ctx.MemoryEntries) > 0 {
		lines := make([]string, len(ctx.MemoryEntries))
		for i, e := range ctx.MemoryEntries {
			lines[i] = fmt.Sprintf("[Round %d] %s", e.Round, sanitize(e.Content))
		}
		mem = strings.Join(lines, "\n")
	}
	return fmt.Sprintf(`TASK: %s

CURRENT ROUND GOAL (Round %d, Phase: %s):
%s

YOUR ROLE: %s — %s

RELEVANT MEMORY FROM PRIOR ROUNDS:
%s

Output exactly:
NEED: <one sentence — what information or expertise you currently need from other agents>
OFFER: <one sentence — what you can contribute this round given your role>`,
		taskDesc, ctx.RoundGoal.Round, ctx.RoundGoal.Phase,
		sanitize(ctx.RoundGoal.Description), def.Name, def.Description, mem)
}

func buildProcessingPrompt(def types.AgentDefinition, ctx AgentContext) string {
	taskDesc := sanitize(ctx.TaskDescription)

	incoming := "No messages routed to you this round."
	if len(ctx.IncomingMessages) > 0 {
		parts := make([]string, len(ctx.IncomingMessages))
		for i, m := range ctx.IncomingMessages {
			parts[i] = fmt.Sprintf("[FROM: %s]\n%s", m.From, sanitize(m.Content))
		}
		incoming = strings.Join(parts, "\n\n---\n\n")
	}

	memory := "No prior memory."
	if len(ctx.MemoryEntries) > 0 {
		lines := make([]string, len(ctx.MemoryEntries))
		for i, e := range ctx.MemoryEntries {
			lines[i] = fmt.Sprintf("[Round %d — %s] %s", e.Round, e.Phase, sanitize(e.Content))
		}
		memory = strings.Join(lines, "\n")
	}

	expectedOutputs := ""
	for i, o := range ctx.RoundGoal.ExpectedOutputs {
		expectedOutputs += fmt.Sprintf("%d. %s\n", i+1, sanitize(o))
	}

	toneBlock := ""
	if def.Domain == types.DomainDrafting && ctx.AssignedLawyerTone != nil {
		toneBlock = "\n────────────────────────────────────────────────────────────────\n" +
			"ASSIGNED LAWYER TONE PROFILE — mirror this voice in all drafted output:\n" +
			sanitize(ctx.AssignedLawyerTone.InjectionSnippet) + "\n"
	}

	// The matter's documents are listed by title + ID only — the map, not the
	// territory. Agents pull verbatim passages from them via search_knowledge,
	// which keeps the context lean for a small local model and keeps quoting on
	// the tool-calling path the citation gate already verifies against.
	docIndexBlock := ""
	if strings.TrimSpace(ctx.DocumentIndex) != "" {
		docIndexBlock = "\n────────────────────────────────────────────────────────────────\n" +
			"DOCUMENTS ON THIS MATTER — call search_chunks to retrieve the relevant verbatim passages from these (or get_outline + read_section to navigate), then copy your Evidence QUOTEs from what it returns and set SOURCE= to the document id shown:\n" +
			sanitize(ctx.DocumentIndex) + "\n"
	}

	return fmt.Sprintf(`TASK: %s

ROUND GOAL (Round %d — Phase: %s):
%s

EXPECTED OUTPUTS THIS ROUND:
%s
INTER-ROUND MEMORY (what has been established in prior rounds):
%s

MESSAGES ROUTED TO YOU THIS ROUND (from other agents whose offers matched your needs):
%s
%s%s
────────────────────────────────────────────────────────────────
Produce your findings. Call the search_chunks tool to retrieve relevant verbatim
passages from the matter's documents (listed under DOCUMENTS ON THIS MATTER above
when present). For each finding, FIRST copy the exact supporting sentence from a
retrieved passage into the Evidence line, THEN state your Conclusion about it.
Copying the quote BEFORE you reason is required — it keeps the quote verbatim. Use
this EXACT format, copying the labels verbatim:

FINDING:
Evidence: SOURCE=<document ID or URL or case ECLI> | QUOTE=<a sentence copied character-for-character from that source> | PAGE=<page/para if known>
Conclusion: <what that evidence shows — your analysis, in your own words>
Confidence: <0.0–1.0>
END_FINDING

The Evidence and the Conclusion are different and are judged differently:
- Evidence QUOTE must appear verbatim in the source — copy it character-for-character. Do NOT summarise, reword, shorten, paraphrase, or fix typos; it is mechanically verified against the source and a reworded quote will not verify. Write the Evidence first so you copy real text before reasoning about it.
- Conclusion is YOUR analysis — write it in your own words; it need not match the source wording. NEVER put your analysis in a QUOTE.

Worked example:
FINDING:
Evidence: SOURCE=employment-agreement-2024 | QUOTE=Employee shall not engage in any competing business for two years | PAGE=7
Conclusion: The non-compete clause is unenforceable in California under Bus. & Prof. Code §16600, which voids contracts restraining lawful trade.
Confidence: 0.9
END_FINDING

Rules:
- Always close every finding with END_FINDING on its own line.
- Provide at least one Evidence line; add more Evidence lines for additional support.
- Reply with exactly NO_FINDINGS only if you genuinely have no findings this round.`,
		taskDesc, ctx.RoundGoal.Round, ctx.RoundGoal.Phase,
		sanitize(ctx.RoundGoal.Description), expectedOutputs, memory, incoming, toneBlock, docIndexBlock)
}

// ─── Response parsers ─────────────────────────────────────────────────────────

func parseNeedOffer(text, agentID string) (types.NeedDescriptor, types.OfferDescriptor) {
	needText := extractLine(text, "NEED:")
	offerText := extractLine(text, "OFFER:")
	if needText == "" {
		needText = "No specific need this round."
	}
	if offerText == "" {
		offerText = "General domain expertise available."
	}
	return types.NeedDescriptor{AgentID: agentID, Text: truncate(needText, 500)},
		types.OfferDescriptor{AgentID: agentID, Text: truncate(offerText, 500)}
}

// maxFindingsPerAgentRound caps how many findings one agent contributes per
// round, matching the original strict-parser bound.
const maxFindingsPerAgentRound = 3

// The finding grammar is forgiving by design: BigLaw must run on cheap/local
// models (qwen2.5:14b and the like) whose instruction-following is looser than
// a frontier model's. Such models routinely drop the END_FINDING terminator,
// omit the "Content:" label and write the finding as prose, decorate markers
// with markdown (**FINDING:**, ### FINDING, "FINDING 1:"), and emit citations
// in natural language instead of the SOURCE=/QUOTE= micro-format. None of that
// is a reasoning failure — it is formatting drift — so the parser recovers the
// work instead of discarding it on the floor.
var (
	// reFindingStart matches a finding header, decoration and ordinal allowed.
	reFindingStart = regexp.MustCompile(`(?im)^[\s>*#_-]*FINDING\b[ \t]*#?\d*[ \t]*:?`)
	reEndFinding   = regexp.MustCompile(`(?i)END_FINDING`)
	reNoFindings   = regexp.MustCompile(`(?i)\bNO_FINDINGS\b`)
	// reContent captures the conclusion. "Conclusion:" is the current label;
	// "Content:" is accepted for back-compat with older output.
	reContent = regexp.MustCompile(`(?si)\b(?:Conclusion|Content)\s*:\s*(.*?)(?:\n\s*(?:Evidence|Citation)\s*:|\n\s*Confidence\s*:|END_FINDING|$)`)
	// reCitationStrict is the verifiable gold form: SOURCE=/QUOTE= give the gate
	// a verbatim quote to check. "Evidence:" is the current label; "Citation:"
	// is accepted for back-compat.
	reCitationStrict = regexp.MustCompile(`(?si)(?:Evidence|Citation)\s*:\s*SOURCE\s*=\s*(.+?)\s*\|\s*QUOTE\s*=\s*(.+?)(?:\s*\|\s*PAGE\s*=\s*([^\n|]+))?(?:\n\s*(?:Evidence|Citation)\s*:|\n\s*Confidence\s*:|END_FINDING|$)`)
	// reCitationLoose is the fallback for natural evidence lines ("Evidence: the
	// LCA, p.3"). It yields a source but no verbatim quote, so the gate marks it
	// unverified rather than trusting it.
	reCitationLoose = regexp.MustCompile(`(?im)^[\s>*#_-]*(?:Evidence|Citation|Source|Cite)\s*:\s*(.+)$`)
	reConfidence    = regexp.MustCompile(`(?i)Confidence\s*:\s*([01]?(?:\.\d+)?)`)
	rePageLoose     = regexp.MustCompile(`(?i)\b(?:pp?\.?|page|para\.?|¶|§)\s*(\d{1,5})`)
	reQuoted        = regexp.MustCompile(`["“]([^"”]{3,})["”]`)
)

func parseFindings(text string, def types.AgentDefinition) []types.Finding {
	if reNoFindings.MatchString(text) {
		return nil
	}
	var findings []types.Finding
	for _, body := range splitFindingBlocks(text) {
		content := extractFindingContent(body)
		if content == "" {
			continue
		}
		confidence := 0.7
		if m := reConfidence.FindStringSubmatch(body); len(m) > 1 && m[1] != "" {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil {
				// Clamp to [0,1] — an agent (or injected text echoed by one)
				// must not be able to claim out-of-range confidence.
				confidence = math.Min(1, math.Max(0, f))
			}
		}
		findings = append(findings, types.Finding{
			ID:         uuid.New().String(),
			AgentID:    def.ID,
			AgentName:  def.Name,
			Content:    content,
			Citations:  extractCitations(body),
			Confidence: confidence,
			Timestamp:  time.Now(),
		})
		if len(findings) >= maxFindingsPerAgentRound {
			break
		}
	}
	return findings
}

// splitFindingBlocks segments the response into per-finding bodies. It anchors
// on each FINDING header and runs to the next header or end of text; when an
// explicit END_FINDING terminator is present within a segment it trims to that
// (the frontier-model path), otherwise it keeps the whole segment (the cheap-
// model path that forgot to close the block).
func splitFindingBlocks(text string) []string {
	locs := reFindingStart.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return nil
	}
	blocks := make([]string, 0, len(locs))
	for i, loc := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := text[loc[1]:end]
		if m := reEndFinding.FindStringIndex(body); m != nil {
			body = body[:m[0]]
		}
		blocks = append(blocks, body)
	}
	return blocks
}

// extractFindingContent returns the finding's prose. It prefers an explicit
// "Content:" label, and falls back to the block body with the trailing
// Citation/Confidence/marker lines stripped — which is how small models that
// skip the label still yield usable content.
func extractFindingContent(body string) string {
	if m := reContent.FindStringSubmatch(body); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		return strings.TrimSpace(m[1])
	}
	var keep []string
	for _, ln := range strings.Split(body, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			if len(keep) > 0 && keep[len(keep)-1] != "" {
				keep = append(keep, "")
			}
			continue
		}
		switch low := strings.ToLower(stripMarkerDecoration(t)); {
		case strings.HasPrefix(low, "evidence:"),
			strings.HasPrefix(low, "citation:"),
			strings.HasPrefix(low, "source:"),
			strings.HasPrefix(low, "cite:"),
			strings.HasPrefix(low, "confidence:"),
			strings.HasPrefix(low, "conclusion:"),
			strings.HasPrefix(low, "content:"):
			continue
		default:
			keep = append(keep, t)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// extractCitations pulls the strict, verifiable SOURCE=/QUOTE= form first; only
// if none are present does it fall back to natural-language citation lines. The
// returned slice is non-nil even when empty — citations are part of the
// serialized JSON contract and nil marshals to null.
func extractCitations(body string) []types.Citation {
	citations := []types.Citation{}
	seen := map[string]bool{}
	for _, cm := range reCitationStrict.FindAllStringSubmatch(body, 50) {
		source := truncate(strings.TrimSpace(cm[1]), 200)
		var page *int
		if len(cm) > 3 {
			if p := digitsOf(cm[3]); p != "" {
				if n, err := strconv.Atoi(p); err == nil {
					page = &n
				}
			}
		}
		// Small models commonly wrap the quote in "…" and sometimes repeat
		// "QUOTE=" on one line instead of starting a new Citation. Split those
		// out and strip wrapping quote marks so the verbatim text matches the
		// source on the gate's substring check (otherwise a faithful quote is
		// falsely flagged as unverified).
		for _, qpart := range strings.Split(cm[2], "QUOTE=") {
			q := trimWrappingQuotes(strings.TrimRight(strings.TrimSpace(qpart), " |"))
			if q == "" {
				continue
			}
			c := types.Citation{Source: source, Quote: truncate(q, 500), Page: page}
			if key := c.Source + "\x00" + c.Quote; !seen[key] {
				seen[key] = true
				citations = append(citations, c)
			}
		}
	}
	if len(citations) > 0 {
		return citations
	}
	for _, cm := range reCitationLoose.FindAllStringSubmatch(body, 50) {
		raw := strings.TrimSpace(cm[1])
		// Skip a SOURCE=… line the strict pass already considered.
		if raw == "" || strings.HasPrefix(strings.ToUpper(raw), "SOURCE=") {
			continue
		}
		c := types.Citation{Source: truncate(raw, 200)}
		if q := reQuoted.FindStringSubmatch(raw); len(q) > 1 {
			c.Quote = truncate(strings.TrimSpace(q[1]), 500)
		}
		if pm := rePageLoose.FindStringSubmatch(raw); len(pm) > 1 {
			if n, err := strconv.Atoi(pm[1]); err == nil {
				c.Page = &n
			}
		}
		if key := c.Source; !seen[key] {
			seen[key] = true
			citations = append(citations, c)
		}
	}
	return citations
}

func stripMarkerDecoration(s string) string {
	return strings.TrimLeft(s, " \t>*#_-")
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// trimWrappingQuotes removes matching leading/trailing quote marks (straight or
// typographic) that small models like to wrap verbatim quotes in. Repeats to
// peel nested pairs.
func trimWrappingQuotes(s string) string {
	s = strings.TrimSpace(s)
	pairs := map[rune]rune{'"': '"', '\'': '\'', '“': '”', '‘': '’', '«': '»'}
	for {
		r := []rune(s)
		if len(r) < 2 {
			return s
		}
		if closer, ok := pairs[r[0]]; ok && r[len(r)-1] == closer {
			s = strings.TrimSpace(string(r[1 : len(r)-1]))
			continue
		}
		return s
	}
}

// ─── Task type inference ──────────────────────────────────────────────────────

func inferTaskType(def types.AgentDefinition) routing.TaskType {
	if def.Tier == types.TierTool {
		return routing.TaskExtraction
	}
	if strings.Contains(def.ID, "drafter") || strings.Contains(def.ID, "writer") {
		return routing.TaskDrafting
	}
	if strings.Contains(def.ID, "analyst") || strings.Contains(def.ID, "agent") {
		return routing.TaskReasoning
	}
	if def.Type == types.AgentTypeRoot {
		return routing.TaskSynthesis
	}
	if def.Type == types.AgentTypeManager {
		return routing.TaskRouting
	}
	return routing.TaskReasoning
}

// ─── Utility ──────────────────────────────────────────────────────────────────

// sanitize neutralises protocol markers and control characters in untrusted
// content before prompt interpolation. Delegates to the shared sanitizer so
// the marker set stays in one place (mirrors TS base.ts importing
// sanitizePromptContent from adapters/lavern.ts).
func sanitize(s string) string {
	return adapters.SanitizePromptContent(s)
}

func extractLine(text, prefix string) string {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(prefix) + `\s*(.+)`)
	if m := re.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func truncate(s string, max int) string {
	return strutil.Truncate(s, max)
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// retrievalTools are the document-retrieval tools every finding-producing agent
// is granted when the matter has documents (config AGENT_GRANT_RETRIEVAL_TOOLS),
// so grounding never depends on a heterogeneous agent definition's own tool list.
var retrievalTools = []string{"search_chunks", "get_outline", "read_section", "search_knowledge", "read_document", "find_in_document", "list_documents"}

func isRetrievalTool(name string) bool { return contains(retrievalTools, name) }

// mergeTools unions two tool lists, preserving order and dropping duplicates.
func mergeTools(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, t := range base {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range extra {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func orSystem(id string) string {
	if id == "" {
		return audit.ActorSystem
	}
	return id
}

func ptr[T any](v T) *T { return &v }
