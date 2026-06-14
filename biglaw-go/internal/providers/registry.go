// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package providers

import (
	"fmt"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/routing"
)

// Registry resolves a model ID to its provider. Every backend is an
// OpenAI-compatible HTTP endpoint — there is no Anthropic/Claude provider in
// this build by design.
type Registry struct {
	// local serves "ollama:"/"local:"-prefixed IDs — Ollama, LM Studio, vLLM,
	// llama.cpp, and the OPENAI_MODEL shortcut.
	local *OllamaProvider
	// primary serves the active stack's bare model IDs (qwen-*, glm-*, kimi-* …)
	// over its OpenAI-compatible endpoint.
	primary *OllamaProvider
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{}
	if cfg.Local.OllamaEnabled || cfg.Local.LocalInferenceURL != "" {
		r.local = NewOllamaProvider(cfg)
	}
	if cfg.Model.PrimaryURL != "" {
		r.primary = NewOpenAICompatProvider(cfg.Model.PrimaryURL, cfg.Model.PrimaryKey)
	}
	return r
}

// Get resolves a model ID to its provider:
//
//	"ollama:"/"local:" prefix → local inference provider
//	anything else             → the primary stack provider (Qwen/GLM/Kimi …)
func (r *Registry) Get(modelID string) (Provider, error) {
	if routing.IsOllamaModel(modelID) || routing.IsLocalModel(modelID) {
		if r.local == nil {
			return nil, fmt.Errorf("local inference not configured for model %s", modelID)
		}
		return r.local, nil
	}
	if r.primary != nil {
		return r.primary, nil
	}
	return nil, fmt.Errorf("no provider configured for model %s (set the stack API key / PRIMARY_MODEL_URL, or LOCAL_INFERENCE_URL)", modelID)
}

// MustGet panics if the provider is not available (use only in startup paths).
func (r *Registry) MustGet(modelID string) Provider {
	p, err := r.Get(modelID)
	if err != nil {
		panic(err)
	}
	return p
}
