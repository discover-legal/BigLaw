// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package providers

import (
	"fmt"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/routing"
)

type Registry struct {
	anthropic *AnthropicProvider
	ollama    *OllamaProvider
}

func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{
		anthropic: NewAnthropicProvider(cfg),
	}
	if cfg.Local.OllamaEnabled || cfg.Local.LocalInferenceURL != "" {
		r.ollama = NewOllamaProvider(cfg)
	}
	return r
}

func (r *Registry) Get(modelID string) (Provider, error) {
	if routing.IsOllamaModel(modelID) || routing.IsLocalModel(modelID) {
		if r.ollama == nil {
			return nil, fmt.Errorf("local inference not configured for model %s", modelID)
		}
		return r.ollama, nil
	}
	return r.anthropic, nil
}

// MustGet panics if the provider is not available (use only in startup paths).
func (r *Registry) MustGet(modelID string) Provider {
	p, err := r.Get(modelID)
	if err != nil {
		panic(err)
	}
	return p
}
