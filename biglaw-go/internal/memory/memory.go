// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// In-process inter-round and intra-round memory stores.
// Uses brute-force cosine search over []float32 embeddings (no external DB).

package memory

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/embeddings"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Intra-round whiteboard (cleared each round) ──────────────────────────────

type IntraRoundStore struct {
	RoundID          string
	mu               sync.Mutex
	receivedMessages map[string][]types.AgentMessage
	agentFindings    map[string][]types.Finding
	sharedContext    []string
}

func NewIntraRound(roundID string) *IntraRoundStore {
	return &IntraRoundStore{
		RoundID:          roundID,
		receivedMessages: map[string][]types.AgentMessage{},
		agentFindings:    map[string][]types.Finding{},
	}
}

func (s *IntraRoundStore) RecordMessage(agentID string, msg types.AgentMessage) {
	s.mu.Lock()
	s.receivedMessages[agentID] = append(s.receivedMessages[agentID], msg)
	s.mu.Unlock()
}

func (s *IntraRoundStore) GetMessagesFor(agentID string) []types.AgentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receivedMessages[agentID]
}

func (s *IntraRoundStore) RecordFinding(agentID string, f types.Finding) {
	s.mu.Lock()
	s.agentFindings[agentID] = append(s.agentFindings[agentID], f)
	s.mu.Unlock()
}

func (s *IntraRoundStore) AddSharedContext(text string) {
	s.mu.Lock()
	s.sharedContext = append(s.sharedContext, text)
	s.mu.Unlock()
}

func (s *IntraRoundStore) SharedContext() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.sharedContext))
	copy(out, s.sharedContext)
	return out
}

// ─── Inter-round memory store (persists across rounds) ────────────────────────

type InterRoundStore struct {
	mu      sync.RWMutex
	entries []types.MemoryEntry
	embedC  *embeddings.Client
}

func NewInterRound(embedC *embeddings.Client) *InterRoundStore {
	return &InterRoundStore{embedC: embedC}
}

func (s *InterRoundStore) Init() error { return nil } // no disk persistence for memory (re-built each run)

type QueryOpts struct {
	TaskID      string
	AgentID     string
	BeforeRound int
	TopK        int
}

func (s *InterRoundStore) Query(query string, opts QueryOpts) ([]types.MemoryEntry, error) {
	qResult, err := s.embedC.Embed(query)
	if err != nil || qResult == nil {
		return s.fallback(opts), nil
	}

	s.mu.RLock()
	type scored struct {
		e     types.MemoryEntry
		score float64
	}
	var candidates []scored
	for _, e := range s.entries {
		if opts.TaskID != "" && e.TaskID != opts.TaskID {
			continue
		}
		if opts.AgentID != "" && e.AgentID != opts.AgentID {
			continue
		}
		if opts.BeforeRound > 0 && e.Round >= opts.BeforeRound {
			continue
		}
		score := 0.0
		if len(e.Embedding) > 0 {
			score = embeddings.CosineSimilarity(qResult.Embedding, e.Embedding)
		}
		candidates = append(candidates, scored{e: e, score: score})
	}
	s.mu.RUnlock()

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	k := opts.TopK
	if k <= 0 {
		k = 6
	}
	if k > len(candidates) {
		k = len(candidates)
	}
	out := make([]types.MemoryEntry, k)
	for i := range out {
		out[i] = candidates[i].e
	}
	return out, nil
}

func (s *InterRoundStore) fallback(opts QueryOpts) []types.MemoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k := opts.TopK
	if k <= 0 {
		k = 6
	}
	var out []types.MemoryEntry
	for _, e := range s.entries {
		if opts.TaskID != "" && e.TaskID != opts.TaskID {
			continue
		}
		if opts.BeforeRound > 0 && e.Round >= opts.BeforeRound {
			continue
		}
		out = append(out, e)
		if len(out) >= k {
			break
		}
	}
	return out
}

type WriteFindingOpts struct {
	TaskID  string
	Round   int
	Phase   types.TaskPhase
	AgentID string
	Finding types.Finding
}

func (s *InterRoundStore) WriteFindingMemory(opts WriteFindingOpts) error {
	content := opts.Finding.Content
	if len(content) > 500 {
		content = strutil.Truncate(content, 500)
	}
	result, _ := s.embedC.Embed(content)
	entry := types.MemoryEntry{
		ID:        uuid.New().String(),
		TaskID:    opts.TaskID,
		Round:     opts.Round,
		Phase:     opts.Phase,
		AgentID:   opts.AgentID,
		Content:   content,
		Tags:      []string{"finding"},
		CreatedAt: time.Now(),
	}
	if result != nil {
		entry.Embedding = result.Embedding
	}
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
	return nil
}

type WriteRoundSummaryOpts struct {
	TaskID       string
	Round        int
	Phase        types.TaskPhase
	Summary      string
	FindingCount int
}

func (s *InterRoundStore) WriteRoundSummary(opts WriteRoundSummaryOpts) error {
	result, _ := s.embedC.Embed(opts.Summary)
	entry := types.MemoryEntry{
		ID:        uuid.New().String(),
		TaskID:    opts.TaskID,
		Round:     opts.Round,
		Phase:     opts.Phase,
		Content:   opts.Summary,
		Tags:      []string{"round_summary"},
		CreatedAt: time.Now(),
	}
	if result != nil {
		entry.Embedding = result.Embedding
	}
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
	return nil
}

func (s *InterRoundStore) DeleteByTaskID(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := s.entries[:0]
	for _, e := range s.entries {
		if e.TaskID != taskID {
			filtered = append(filtered, e)
		}
	}
	s.entries = filtered
}

// Adapter satisfies agents.MemoryStore using an InterRoundStore.
// The flat-argument interface agents expect is bridged to QueryOpts here.
type Adapter struct {
	store *InterRoundStore
}

func NewAdapter(store *InterRoundStore) *Adapter {
	return &Adapter{store: store}
}

func (a *Adapter) Query(query, taskID, agentID string, beforeRound, topK int) ([]types.MemoryEntry, error) {
	return a.store.Query(query, QueryOpts{
		TaskID:      taskID,
		AgentID:     agentID,
		BeforeRound: beforeRound,
		TopK:        topK,
	})
}
