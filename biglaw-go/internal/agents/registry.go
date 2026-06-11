// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// In-process agent registry with brute-force cosine search.
// Replaces ruvector HNSW. For < 200 agents on ARM64 hardware, brute-force
// cosine is fast enough (~1 ms) and eliminates the native-module dependency.

package agents

import (
	"encoding/json"
	"os"
	"sort"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/types"
)

type SearchOpts struct {
	Tier *types.AgentTier
	TopK int
}

type Registry struct {
	mu      sync.RWMutex
	agents  []types.AgentDefinition
	embedC  *embeddings.Client
	dataDir string
}

func NewRegistry(embedC *embeddings.Client, dataDir string) *Registry {
	return &Registry{embedC: embedC, dataDir: dataDir}
}

func (r *Registry) Init() error {
	path := r.dataDir + "/agents.json"
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // first run — registry will be seeded
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return json.Unmarshal(data, &r.agents)
}

func (r *Registry) Persist() error {
	r.mu.RLock()
	data, err := json.Marshal(r.agents)
	r.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(r.dataDir+"/agents.json", data, 0644)
}

// RegisterAll upserts all definitions and (re-)embeds them.
func (r *Registry) RegisterAll(defs []types.AgentDefinition) error {
	texts := make([]string, len(defs))
	for i, d := range defs {
		texts[i] = d.Description
	}
	results, err := r.embedC.EmbedBatch(texts)
	if err != nil {
		// Proceed without embeddings — search falls back to name matching
		results = make([]embeddings.EmbeddingResult, len(defs))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	byID := make(map[string]int, len(r.agents))
	for i, a := range r.agents {
		byID[a.ID] = i
	}
	for i, def := range defs {
		if i < len(results) {
			def.Embedding = results[i].Embedding
		}
		if idx, ok := byID[def.ID]; ok {
			r.agents[idx] = def
		} else {
			r.agents = append(r.agents, def)
		}
	}
	return nil
}

// Search returns the topK agents semantically closest to the query.
func (r *Registry) Search(query string, opts SearchOpts) ([]types.AgentDefinition, error) {
	qResult, err := r.embedC.Embed(query)
	if err != nil || qResult == nil {
		return r.fallbackByName(query, opts), nil
	}
	return r.searchByEmbedding(qResult.Embedding, opts), nil
}

// Recommend biases search toward agents with positive history, away from negative.
func (r *Registry) Recommend(query string, positive, negative []string, opts SearchOpts) ([]types.AgentDefinition, error) {
	results, err := r.Search(query, opts)
	if err != nil {
		return nil, err
	}
	negSet := make(map[string]bool, len(negative))
	for _, id := range negative {
		negSet[id] = true
	}
	posSet := make(map[string]bool, len(positive))
	for _, id := range positive {
		posSet[id] = true
	}
	// Re-score: +0.1 for positive history, -0.2 for negative
	type scored struct {
		def   types.AgentDefinition
		score float64
	}
	scored_list := make([]scored, len(results))
	for i, d := range results {
		s := d.SuccessScore
		if posSet[d.ID] {
			s += 0.1
		}
		if negSet[d.ID] {
			s -= 0.2
		}
		scored_list[i] = scored{def: d, score: s}
	}
	sort.Slice(scored_list, func(i, j int) bool { return scored_list[i].score > scored_list[j].score })
	out := make([]types.AgentDefinition, len(scored_list))
	for i, s := range scored_list {
		out[i] = s.def
	}
	return out, nil
}

func (r *Registry) ListAll() []types.AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.AgentDefinition, len(r.agents))
	copy(out, r.agents)
	return out
}

func (r *Registry) GetByID(id string) *types.AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i, a := range r.agents {
		if a.ID == id {
			cp := r.agents[i]
			return &cp
		}
	}
	return nil
}

// RecordOutcome updates the success score for a list of agents.
func (r *Registry) RecordOutcome(agentIDs []string, score float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.agents {
		for _, id := range agentIDs {
			if r.agents[i].ID == id {
				// Exponential moving average (α=0.2)
				r.agents[i].SuccessScore = 0.8*r.agents[i].SuccessScore + 0.2*score
			}
		}
	}
}

// ─── Private helpers ──────────────────────────────────────────────────────────

type agentScore struct {
	def   types.AgentDefinition
	score float64
}

func (r *Registry) searchByEmbedding(queryEmb []float32, opts SearchOpts) []types.AgentDefinition {
	r.mu.RLock()
	candidates := make([]agentScore, 0, len(r.agents))
	for _, a := range r.agents {
		if opts.Tier != nil && a.Tier != *opts.Tier {
			continue
		}
		if len(a.Embedding) == 0 {
			continue
		}
		sim := embeddings.CosineSimilarity(queryEmb, a.Embedding)
		candidates = append(candidates, agentScore{def: a, score: sim})
	}
	r.mu.RUnlock()

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	k := opts.TopK
	if k <= 0 {
		k = 10
	}
	if k > len(candidates) {
		k = len(candidates)
	}
	out := make([]types.AgentDefinition, k)
	for i := range out {
		out[i] = candidates[i].def
	}
	return out
}

func (r *Registry) fallbackByName(query string, opts SearchOpts) []types.AgentDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	k := opts.TopK
	if k <= 0 {
		k = 10
	}
	var out []types.AgentDefinition
	for _, a := range r.agents {
		if opts.Tier != nil && a.Tier != *opts.Tier {
			continue
		}
		out = append(out, a)
		if len(out) >= k {
			break
		}
	}
	return out
}
