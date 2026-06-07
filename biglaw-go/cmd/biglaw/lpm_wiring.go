// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// LPM wiring — adapts the live orchestrator + time store to the lpm.DataProvider
// interface the status-report spine consumes. Budget burn is left nil for now
// (the matter-health monitor degrades gracefully without it); the budget
// dimension wires in with a later phase.
package main

import (
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/matters"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// lpmTimeReader adapts the time store to the matter-health TimeReader interface.
type lpmTimeReader struct{ ts *timekeeping.TimeStore }

func (r lpmTimeReader) List(matter string) []types.TimeEntry {
	return r.ts.List(timekeeping.TimeFilter{MatterNumber: matter})
}

// lpmNilBudget satisfies the matter-health BudgetBurner interface with no burn.
type lpmNilBudget struct{}

func (lpmNilBudget) GetBurn(matter string) *types.BudgetBurn { return nil }

// lpmDataProvider implements lpm.DataProvider over the orchestrator + time store.
type lpmDataProvider struct {
	orch   *orchestrator.Orchestrator
	ts     *timekeeping.TimeStore
	health *matters.Monitor
}

func newLPMDataProvider(orch *orchestrator.Orchestrator, ts *timekeeping.TimeStore) *lpmDataProvider {
	return &lpmDataProvider{orch: orch, ts: ts, health: matters.New()}
}

func (p *lpmDataProvider) allTasks() []types.Task {
	ptrs := p.orch.ListTasks()
	out := make([]types.Task, 0, len(ptrs))
	for _, t := range ptrs {
		if t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// ActiveMatters returns the distinct matters with at least one non-terminal task.
func (p *lpmDataProvider) ActiveMatters() []lpm.MatterRef {
	seen := map[string]bool{}
	var out []lpm.MatterRef
	for _, t := range p.orch.ListTasks() {
		if t == nil || t.MatterNumber == "" {
			continue
		}
		if t.Status == types.TaskStatusComplete || t.Status == types.TaskStatusFailed {
			continue
		}
		if seen[t.MatterNumber] {
			continue
		}
		seen[t.MatterNumber] = true
		out = append(out, lpm.MatterRef{MatterNumber: t.MatterNumber, ClientNumber: t.ClientNumber})
	}
	return out
}

func (p *lpmDataProvider) TasksForMatter(matter string) []types.Task {
	var out []types.Task
	for _, t := range p.orch.ListTasks() {
		if t != nil && t.MatterNumber == matter {
			out = append(out, *t)
		}
	}
	return out
}

func (p *lpmDataProvider) TimeEntriesForMatter(matter string) []types.TimeEntry {
	return p.ts.List(timekeeping.TimeFilter{MatterNumber: matter})
}

func (p *lpmDataProvider) HealthForMatter(matter string) types.MatterHealthScore {
	return p.health.Compute(matter, p.allTasks(), lpmTimeReader{p.ts}, lpmNilBudget{})
}
