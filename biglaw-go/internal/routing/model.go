// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package routing

import (
	"strconv"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	ModelOpus   = "claude-opus-4-8"
	ModelSonnet = "claude-sonnet-4-6"
	ModelHaiku  = "claude-haiku-4-5-20251001"
)

type TaskType string

const (
	TaskSynthesis    TaskType = "synthesis"
	TaskReasoning    TaskType = "reasoning"
	TaskDrafting     TaskType = "drafting"
	TaskDebate       TaskType = "debate"
	TaskVerification TaskType = "verification"
	TaskDescriptor   TaskType = "descriptor"
	TaskExtraction   TaskType = "extraction"
	TaskRouting      TaskType = "routing"
	TaskTranslation  TaskType = "translation"
)

type Complexity string

const (
	ComplexityHigh   Complexity = "high"
	ComplexityMedium Complexity = "medium"
	ComplexityLow    Complexity = "low"
)

func IsOllamaModel(model string) bool { return strings.HasPrefix(model, "ollama:") }
func IsLocalModel(model string) bool  { return strings.HasPrefix(model, "local:") }

func OllamaModelID(cfg *config.Config) string {
	return "ollama:" + cfg.Local.OllamaModel
}

func LocalModelID(cfg *config.Config) string {
	return "local:" + cfg.Local.LocalInferenceModel
}

func ollamaTierSet(cfg *config.Config) map[int]bool {
	out := map[int]bool{}
	for _, s := range strings.Split(cfg.Local.OllamaTiers, ",") {
		s = strings.TrimSpace(s)
		if n, err := strconv.Atoi(s); err == nil {
			out[n] = true
		}
	}
	return out
}

func localInferenceTierSet(cfg *config.Config) (map[int]bool, bool) {
	v := strings.TrimSpace(strings.ToLower(cfg.Local.LocalInferenceTiers))
	if v == "" {
		return nil, false
	}
	if v == "all" {
		return nil, true
	}
	out := map[int]bool{}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if n, err := strconv.Atoi(s); err == nil {
			out[n] = true
		}
	}
	return out, false
}

type SelectParams struct {
	Tier       *types.AgentTier
	AgentType  *types.AgentType
	TaskType   TaskType
	Complexity Complexity
}

// SelectModel picks the right model ID for a given agent + task context.
func SelectModel(cfg *config.Config, p SelectParams) string {
	// Generic local inference (LM Studio, Jan, vLLM, llama.cpp)
	if cfg.Local.LocalInferenceURL != "" {
		tierMap, all := localInferenceTierSet(cfg)
		if all {
			return LocalModelID(cfg)
		}
		if p.Tier != nil && tierMap[int(*p.Tier)] {
			return LocalModelID(cfg)
		}
	}

	// Ollama routing for lightweight tasks
	if cfg.Local.OllamaEnabled && p.TaskType != TaskDebate && p.TaskType != TaskSynthesis &&
		(p.Tier == nil || *p.Tier != types.TierRoot) {
		ollamaTiers := ollamaTierSet(cfg)
		lightweight := p.TaskType == TaskDescriptor || p.TaskType == TaskExtraction ||
			p.TaskType == TaskRouting || p.TaskType == TaskTranslation
		if p.Tier != nil && ollamaTiers[int(*p.Tier)] {
			return OllamaModelID(cfg)
		}
		if lightweight && (p.Tier == nil || ollamaTiers[int(*p.Tier)]) {
			return OllamaModelID(cfg)
		}
	}

	// Cloud model selection
	if p.TaskType == TaskDescriptor || p.TaskType == TaskExtraction ||
		p.TaskType == TaskRouting || p.TaskType == TaskTranslation {
		return ModelHaiku
	}
	if p.Tier != nil && *p.Tier == types.TierTool {
		return ModelHaiku
	}
	if p.Tier != nil && *p.Tier == types.TierRoot {
		return ModelOpus
	}
	if p.TaskType == TaskSynthesis || p.TaskType == TaskDebate || p.Complexity == ComplexityHigh {
		return ModelOpus
	}
	return ModelSonnet
}

// EstimateComplexity uses simple keyword heuristics on a prompt.
func EstimateComplexity(text string) Complexity {
	lower := strings.ToLower(text)
	highSignals := []string{"novel", "unprecedented", "balance", "proportionality",
		"multi-jurisdict", "conflict", "fundamental right", "constitutional",
		"antitrust", "merger control", "sanctions"}
	lowSignals := []string{"extract", "list", "identify", "translate", "summarise", "count"}

	high, low := 0, 0
	for _, s := range highSignals {
		if strings.Contains(lower, s) {
			high++
		}
	}
	for _, s := range lowSignals {
		if strings.Contains(lower, s) {
			low++
		}
	}
	if high >= 2 {
		return ComplexityHigh
	}
	if low >= 2 {
		return ComplexityLow
	}
	return ComplexityMedium
}

// ShouldUseThinking returns true when extended thinking should be requested.
func ShouldUseThinking(modelID string, taskType TaskType, tier *types.AgentTier, complexity Complexity) bool {
	if IsOllamaModel(modelID) || IsLocalModel(modelID) {
		return false
	}
	if strings.Contains(modelID, "haiku") {
		return false
	}
	if taskType == TaskSynthesis || taskType == TaskDebate {
		return true
	}
	if tier != nil && *tier == types.TierRoot {
		return true
	}
	if taskType == TaskReasoning && complexity == ComplexityHigh {
		return true
	}
	return false
}

// ResolveModelID strips the "ollama:" or "local:" prefix to get the bare model name.
func ResolveModelID(modelID string) string {
	if IsOllamaModel(modelID) {
		return strings.TrimPrefix(modelID, "ollama:")
	}
	if IsLocalModel(modelID) {
		return strings.TrimPrefix(modelID, "local:")
	}
	return modelID
}
