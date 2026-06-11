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
	RoundGoal             types.RoundGoal
	IncomingMessages      []types.AgentMessage
	MemoryEntries         []types.MemoryEntry
	TaskDescription       string
	TaskID                string
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

	hasTools := ctx.ToolRegistry != nil && ctx.KnowledgeStore != nil &&
		ctx.MemoryStore != nil && ctx.TaskID != "" && len(a.Def.AllowedTools) > 0

	var text string
	var err error
	if hasTools {
		text, err = a.runAgenticLoop(prompt, maxTokens, model, ctx)
	} else {
		text, err = a.callModel(prompt, maxTokens, model, ctx.TaskID, cost.ContextTask)
	}
	if err != nil {
		return nil, err
	}

	findings := parseFindings(text, a.Def)
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

func (a *Agent) runAgenticLoop(initialPrompt string, maxTokens int, model string, ctx AgentContext) (string, error) {
	toolSchemas := ctx.ToolRegistry.SchemasFor(a.Def.AllowedTools)
	toolCtx := ToolContext{
		KnowledgeStore:      ctx.KnowledgeStore,
		MemoryStore:         ctx.MemoryStore,
		TaskID:              ctx.TaskID,
		OwnerID:             ctx.OwnerID,
		ResponsibleLawyerID: ctx.ResponsibleLawyerID,
	}

	prov, err := a.providers.Get(model)
	if err != nil {
		return "", err
	}
	bareModel := routing.ResolveModelID(model)

	msgs := []providers.Message{{Role: "user", Content: initialPrompt}}
	var finalText string

	for iteration := 0; iteration < a.cfg.Agents.MaxToolIterations; iteration++ {
		resp, err := prov.Chat(providers.ChatParams{
			Model:       bareModel,
			MaxTokens:   maxTokens,
			System:      a.Def.SystemPrompt,
			Tools:       toolSchemas,
			Messages:    msgs,
			CacheSystem: true,
		})
		if err != nil {
			return finalText, err
		}

		a.recordCost(resp, model, cost.ContextTask, ctx.TaskID)

		for _, block := range resp.Content {
			if block.Type == providers.BlockText {
				finalText = block.Text
			}
		}

		if resp.StopReason == providers.StopEndTurn {
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
				if !contains(a.Def.AllowedTools, block.Name) {
					result = map[string]string{"error": fmt.Sprintf("tool '%s' not permitted", block.Name)}
				} else {
					result, err = ctx.ToolRegistry.Execute(block.Name, block.Input, toolCtx)
					if err != nil {
						result = map[string]string{"error": err.Error()}
					}
				}
				raw, _ := json.Marshal(result)
				content := string(raw)
				if len(content) > 100_000 {
					content = strutil.Truncate(content, 100_000) + "…[truncated]"
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
	return finalText, nil
}

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

	return fmt.Sprintf(`TASK: %s

ROUND GOAL (Round %d — Phase: %s):
%s

EXPECTED OUTPUTS THIS ROUND:
%s
INTER-ROUND MEMORY (what has been established in prior rounds):
%s

MESSAGES ROUTED TO YOU THIS ROUND (from other agents whose offers matched your needs):
%s
%s
────────────────────────────────────────────────────────────────
Produce your findings. For each distinct finding:

FINDING:
Content: <finding — state your conclusion or analysis clearly>
Citation: SOURCE=<document ID or URL or case ECLI> | QUOTE=<verbatim text> | PAGE=<page/para if known>
Confidence: <0.0–1.0>
END_FINDING

Rules:
- Each finding must have at least one Citation.
- Quote must be verbatim — not paraphrased.
- Multiple Citations allowed per finding (repeat Citation: lines).
- If you have no findings this round: NO_FINDINGS`,
		taskDesc, ctx.RoundGoal.Round, ctx.RoundGoal.Phase,
		sanitize(ctx.RoundGoal.Description), expectedOutputs, memory, incoming, toneBlock)
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

var reFindingBlock = regexp.MustCompile(`(?si)FINDING:(.*?)END_FINDING`)
var reContent = regexp.MustCompile(`(?si)Content:\s*(.*?)(?:Citation:|Confidence:|END_FINDING|$)`)
var reCitation = regexp.MustCompile(`(?si)Citation:\s*SOURCE=(.+?)\s*\|\s*QUOTE=(.+?)(?:\s*\|\s*PAGE=(.+?))?(?:\nCitation:|\nConfidence:|END_FINDING|$)`)
var reConfidence = regexp.MustCompile(`(?i)Confidence:\s*([\d.]+)`)

func parseFindings(text string, def types.AgentDefinition) []types.Finding {
	if regexp.MustCompile(`(?i)NO_FINDINGS`).MatchString(text) {
		return nil
	}
	blocks := reFindingBlock.FindAllStringSubmatch(text, 3)
	var findings []types.Finding
	for _, block := range blocks {
		body := block[1]
		contentMatch := reContent.FindStringSubmatch(body)
		if len(contentMatch) < 2 || strings.TrimSpace(contentMatch[1]) == "" {
			continue
		}
		content := strings.TrimSpace(contentMatch[1])

		// Non-nil even when the agent cites nothing — citations are part of
		// the serialized JSON contract, and nil marshals to null.
		citations := []types.Citation{}
		for _, cm := range reCitation.FindAllStringSubmatch(body, 50) {
			cit := types.Citation{
				Source:               truncate(strings.TrimSpace(cm[1]), 200),
				Quote:                truncate(strings.TrimSpace(cm[2]), 500),
				MechanicallyVerified: false,
			}
			if len(cm) > 3 && strings.TrimSpace(cm[3]) != "" {
				if n, err := strconv.Atoi(strings.TrimSpace(cm[3])); err == nil {
					cit.Page = &n
				}
			}
			citations = append(citations, cit)
		}

		confidence := 0.7
		if m := reConfidence.FindStringSubmatch(body); len(m) > 1 {
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
			Citations:  citations,
			Confidence: confidence,
			Timestamp:  time.Now(),
		})
	}
	return findings
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

func orSystem(id string) string {
	if id == "" {
		return audit.ActorSystem
	}
	return id
}

func ptr[T any](v T) *T { return &v }
