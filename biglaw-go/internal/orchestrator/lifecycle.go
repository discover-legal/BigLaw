// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// ─── Gate waiting ─────────────────────────────────────────────────────────────

func (o *Orchestrator) waitForGates(task *types.Task) {
	o.mu.RLock()
	ch := o.gateChans[task.ID]
	o.mu.RUnlock()
	if ch == nil {
		return
	}
	for {
		// Gate handlers rewrite PendingGates under the lock.
		allResolved := true
		o.mu.RLock()
		for _, g := range task.PendingGates {
			if g.Status == "pending" {
				allResolved = false
				break
			}
		}
		o.mu.RUnlock()
		if allResolved {
			return
		}
		select {
		case <-ch:
		case <-time.After(30 * time.Second):
		}
	}
}

// ─── Persistence ──────────────────────────────────────────────────────────────

func (o *Orchestrator) persistTasks() error {
	o.persistMu.Lock()
	defer o.persistMu.Unlock()

	path := o.cfg.Persistence.TasksFile
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	// Hold the read lock while the encoder streams. This trades a bounded
	// period of update backpressure for a race-free snapshot without creating
	// a second full in-memory copy of every task and its findings.
	o.mu.RLock()
	tasks := make([]*types.Task, 0, len(o.tasks))
	for _, task := range o.tasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.Before(tasks[j].CreatedAt) })
	err = json.NewEncoder(file).Encode(tasks)
	o.mu.RUnlock()
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace task snapshot: %w", err)
	}
	return nil
}

func (o *Orchestrator) restoreTasks() {
	data, err := os.ReadFile(o.cfg.Persistence.TasksFile)
	if err != nil {
		return
	}
	var items []*types.Task
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	o.mu.Lock()
	quarantined := 0
	for _, t := range items {
		if _, ok := phaseSequences[t.WorkflowType]; !ok {
			continue
		}
		normalizeTask(t)
		if o.quarantineStaleTask(t) { // see restore.go — no runner survives a restart
			quarantined++
		}
		o.tasks[t.ID] = t
		o.gateChans[t.ID] = make(chan struct{}, 8)
	}
	o.mu.Unlock()
	if quarantined > 0 {
		o.persistTasks()
	}
}

// normalizeTask repairs nil slices on tasks restored from disk. Earlier
// builds persisted rounds whose Edges/Findings (and findings' Citations)
// could be nil; nil marshals to JSON null, which breaks the UI's contract
// that these fields are always arrays.
func normalizeTask(t *types.Task) {
	if t.DocumentIDs == nil {
		t.DocumentIDs = []string{}
	}
	if t.ActiveAgentIDs == nil {
		t.ActiveAgentIDs = []string{}
	}
	if t.Rounds == nil {
		t.Rounds = []types.RoundState{}
	}
	if t.Findings == nil {
		t.Findings = []types.Finding{}
	}
	if t.PendingGates == nil {
		t.PendingGates = []types.GateRequest{}
	}
	for i := range t.Findings {
		if t.Findings[i].Citations == nil {
			t.Findings[i].Citations = []types.Citation{}
		}
	}
	for i := range t.Rounds {
		r := &t.Rounds[i]
		if r.Goal.ExpectedOutputs == nil {
			r.Goal.ExpectedOutputs = []string{}
		}
		if r.ActiveAgentIDs == nil {
			r.ActiveAgentIDs = []string{}
		}
		if r.Edges == nil {
			r.Edges = []types.CommunicationEdge{}
		}
		if r.Messages == nil {
			r.Messages = []types.AgentMessage{}
		}
		if r.Findings == nil {
			r.Findings = []types.Finding{}
		}
		for j := range r.Findings {
			if r.Findings[j].Citations == nil {
				r.Findings[j].Citations = []types.Citation{}
			}
		}
	}
}

// ─── Cost recording ───────────────────────────────────────────────────────────

func (o *Orchestrator) recordCost(resp *providers.ChatResponse, modelID string, ctx cost.CostContext, taskID string) {
	isLocal := routing.IsOllamaModel(modelID) || routing.IsLocalModel(modelID)
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
		costUSD = cost.CalcCostUSD(modelID, resp.Usage.InputTokens, resp.Usage.OutputTokens, cw, cr)
	} else {
		w := cost.CalcWattHours(o.cfg.Local.InferenceWatts, resp.DurationMs)
		wh = &w
		watts = &o.cfg.Local.InferenceWatts
	}
	provider := "anthropic"
	if routing.IsOllamaModel(modelID) {
		provider = "ollama"
	} else if routing.IsLocalModel(modelID) {
		provider = "local"
	}
	o.costs.Record(cost.RecordRequest{
		Model:          modelID,
		Provider:       provider,
		InputTokens:    resp.Usage.InputTokens,
		OutputTokens:   resp.Usage.OutputTokens,
		CostUSD:        costUSD,
		EstimatedWh:    wh,
		EstimatedWatts: watts,
		DurationMs:     resp.DurationMs,
		Context:        ctx,
		TaskID:         taskID,
	})
}

// ─── Utilities ────────────────────────────────────────────────────────────────

func orSystem(id string) string {
	if id == "" {
		return audit.ActorSystem
	}
	return id
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func regexpFindSubmatch(pattern, text string) []string {
	re := regexp.MustCompile(pattern)
	return re.FindStringSubmatch(text)
}

func regexpFindAllSubmatch(pattern, text string, n int) [][]string {
	re := regexp.MustCompile(pattern)
	return re.FindAllStringSubmatch(text, n)
}
