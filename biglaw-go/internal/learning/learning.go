// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Q-learning layer for agent recruitment.
// Uses epsilon-greedy exploration to rank candidates by historical performance
// per (phase, jurisdiction, workflowType) combination.

package learning

import (
	"encoding/json"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/types"
)

const (
	epsilon      = 0.15 // exploration rate
	learningRate = 0.1
	discount     = 0.9
)

type stateKey struct {
	phase        types.TaskPhase
	jurisdiction string
	workflow     types.WorkflowType
}

type Engine struct {
	mu     sync.Mutex
	qtable map[string]map[string]float64 // stateKey.String() → agentID → Q-value
	file   string
}

var Default = &Engine{qtable: map[string]map[string]float64{}}

func (e *Engine) Init(file string) error {
	e.file = file
	data, err := os.ReadFile(file)
	if err != nil {
		return nil // first run
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return json.Unmarshal(data, &e.qtable)
}

// persist marshals the Q-table. Callers must hold e.mu.
func (e *Engine) persist() {
	if e.file == "" {
		return
	}
	data, err := json.Marshal(e.qtable)
	if err == nil {
		if werr := os.WriteFile(e.file, data, 0600); werr != nil {
			slog.Warn("learning: persist failed", "path", e.file, "err", werr)
		}
	}
}

func stateStr(phase types.TaskPhase, jurisdiction string, workflow types.WorkflowType) string {
	return strings.Join([]string{string(phase), strings.ToUpper(jurisdiction), string(workflow)}, "|")
}

// RankCandidates re-orders a candidate list using epsilon-greedy Q-values.
func (e *Engine) RankCandidates(phase types.TaskPhase, jurisdiction string, workflow types.WorkflowType, candidates []string) []string {
	if len(candidates) == 0 {
		return candidates
	}
	// Epsilon-greedy: with probability ε return random shuffle.
	if rand.Float64() < epsilon {
		out := make([]string, len(candidates))
		copy(out, candidates)
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}

	key := stateStr(phase, jurisdiction, workflow)
	e.mu.Lock()
	qmap := e.qtable[key]
	e.mu.Unlock()

	type scored struct {
		id    string
		score float64
	}
	list := make([]scored, len(candidates))
	for i, id := range candidates {
		q := 0.0
		if qmap != nil {
			q = qmap[id]
		}
		list[i] = scored{id: id, score: q}
	}
	// Sort descending by Q-value.
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].score > list[j-1].score; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.id
	}
	return out
}

type EpisodeOpts struct {
	Phase        types.TaskPhase
	NextPhase    types.TaskPhase
	Jurisdiction string
	WorkflowType types.WorkflowType
	AgentID      string
	Reward       float64
	Done         bool
}

// RecordEpisode updates Q-values with a Bellman equation update.
func (e *Engine) RecordEpisode(opts EpisodeOpts) error {
	key := stateStr(opts.Phase, opts.Jurisdiction, opts.WorkflowType)
	nextKey := stateStr(opts.NextPhase, opts.Jurisdiction, opts.WorkflowType)

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.qtable[key] == nil {
		e.qtable[key] = map[string]float64{}
	}
	current := e.qtable[key][opts.AgentID]

	// Max Q-value in the next state.
	maxNext := 0.0
	if !opts.Done && e.qtable[nextKey] != nil {
		for _, v := range e.qtable[nextKey] {
			maxNext = math.Max(maxNext, v)
		}
	}

	// Bellman update: Q(s,a) ← Q(s,a) + α * [r + γ * maxQ(s') - Q(s,a)]
	updated := current + learningRate*(opts.Reward+discount*maxNext-current)
	e.qtable[key][opts.AgentID] = updated

	// Synchronous, under e.mu: a goroutine here would read qtable unlocked.
	e.persist()
	return nil
}
