// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// LPM wiring — adapts the live orchestrator, time store and client roster to the
// lpm.DataProvider interface the status-report spine consumes, including real
// budget burn (matter budget vs. billed time) feeding both the EmailsRouted-style
// deltas and the matter-health budget dimension.
package main

import (
	"github.com/discover-legal/biglaw-go/internal/budget"
	"github.com/discover-legal/biglaw-go/internal/clients"
	"github.com/discover-legal/biglaw-go/internal/lpm"
	"github.com/discover-legal/biglaw-go/internal/matters"
	"github.com/discover-legal/biglaw-go/internal/orchestrator"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// tsAdapter adapts the time store to both the matter-health TimeReader
// (List(matter)) and the budget TimeStore (List(matter) + ListAll()) interfaces.
type tsAdapter struct{ ts *timekeeping.TimeStore }

func (a tsAdapter) List(matter string) []types.TimeEntry {
	return a.ts.List(timekeeping.TimeFilter{MatterNumber: matter})
}

func (a tsAdapter) ListAll() []types.TimeEntry {
	return a.ts.List(timekeeping.TimeFilter{})
}

// budgetClientStore adapts the client roster to the budget ClientStore interface.
type budgetClientStore struct{ cs *clients.ClientStore }

func (b budgetClientStore) List() []*types.Client {
	src := b.cs.List()
	out := make([]*types.Client, len(src))
	for i := range src {
		c := src[i]
		out[i] = &c
	}
	return out
}

func (b budgetClientStore) SetMatterBudgetAlerts(matterNumber string, triggered []float64) error {
	return b.cs.SetMatterBudgetAlerts(matterNumber, triggered)
}

// lpmDataProvider implements lpm.DataProvider over the orchestrator, time store
// and client roster.
type lpmDataProvider struct {
	orch   *orchestrator.Orchestrator
	ts     *timekeeping.TimeStore
	health *matters.Monitor
	budget *budget.Monitor
}

func newLPMDataProvider(orch *orchestrator.Orchestrator, ts *timekeeping.TimeStore, cs *clients.ClientStore) *lpmDataProvider {
	return &lpmDataProvider{
		orch:   orch,
		ts:     ts,
		health: matters.New(),
		budget: budget.NewMonitor(tsAdapter{ts}, budgetClientStore{cs}, nil),
	}
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

func (p *lpmDataProvider) BudgetForMatter(matter string) *types.BudgetBurn {
	return p.budget.GetBurn(matter)
}

func (p *lpmDataProvider) HealthForMatter(matter string) types.MatterHealthScore {
	return p.health.Compute(matter, p.allTasks(), tsAdapter{p.ts}, p.budget)
}

// MatterOptions returns the active matters as routing candidates for the email
// router, using the most recent task description as the matter's label.
func (p *lpmDataProvider) MatterOptions() []lpm.MatterOption {
	seen := map[string]bool{}
	var out []lpm.MatterOption
	for _, t := range p.orch.ListTasks() {
		if t == nil || t.MatterNumber == "" || seen[t.MatterNumber] {
			continue
		}
		if t.Status == types.TaskStatusComplete || t.Status == types.TaskStatusFailed {
			continue
		}
		seen[t.MatterNumber] = true
		out = append(out, lpm.MatterOption{
			MatterNumber: t.MatterNumber,
			ClientNumber: t.ClientNumber,
			Description:  t.Description,
		})
	}
	return out
}
