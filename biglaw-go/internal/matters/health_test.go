// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package matters

import (
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

type fakeTime []types.TimeEntry

func (f fakeTime) List(string) []types.TimeEntry { return f }

type fakeBudget struct{ burn *types.BudgetBurn }

func (f fakeBudget) GetBurn(string) *types.BudgetBurn { return f.burn }

// recentEntry is the time entry every task run (failed or not) writes.
func recentEntry() types.TimeEntry {
	end := time.Now().Add(-time.Minute)
	return types.TimeEntry{MatterNumber: "M-1", EndedAt: &end}
}

func task(id string, st types.TaskStatus) types.Task {
	return types.Task{ID: id, MatterNumber: "M-1", Status: st, CreatedAt: time.Now()}
}

// QA regression: a matter whose only task had failed scored 88 "On track".
func TestFailedOnlyMatterIsNotHealthy(t *testing.T) {
	m := New()
	got := m.Compute("M-1", []types.Task{task("t1", types.TaskStatusFailed)},
		fakeTime{recentEntry()}, fakeBudget{})
	if got.Signal == types.HealthGreen || got.Score >= greenThreshold {
		t.Fatalf("failed-only matter = %v %q (%v), want below green", got.Score, got.SignalLabel, got.Signal)
	}
	if got.Dimensions.DeadlineHealth != 0 {
		t.Errorf("delivery dimension = %v, want 0 when every task failed", got.Dimensions.DeadlineHealth)
	}
	if len(got.RiskFactors) == 0 || got.RiskFactors[0].Type != "task_failure" || got.RiskFactors[0].Severity != "high" {
		t.Errorf("want task_failure as the top risk, got %+v", got.RiskFactors)
	}
}

func TestInterruptedCountsAsFailed(t *testing.T) {
	got := New().Compute("M-1", []types.Task{task("t1", types.TaskStatusInterrupted)},
		fakeTime{recentEntry()}, fakeBudget{})
	if got.Signal == types.HealthGreen {
		t.Fatalf("interrupted-only matter signalled green: %+v", got)
	}
}

func TestOneFailureAmongManyStillPenalised(t *testing.T) {
	tasks := []types.Task{task("f", types.TaskStatusFailed)}
	for _, id := range []string{"a", "b", "c", "d", "e", "f2", "g", "h", "i"} {
		tasks = append(tasks, task(id, types.TaskStatusComplete))
	}
	got := New().Compute("M-1", tasks, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth != 70 {
		t.Errorf("1 of 10 failed: delivery = %v, want the flat-penalty 70", got.Dimensions.DeadlineHealth)
	}
	if got.Signal == types.HealthGreen {
		t.Errorf("a high-severity failure must cap the signal below green: %+v", got)
	}
}

func TestHealthyMatterStaysGreen(t *testing.T) {
	got := New().Compute("M-1", []types.Task{task("a", types.TaskStatusComplete)},
		fakeTime{recentEntry()}, fakeBudget{&types.BudgetBurn{BurnPct: 0.2}})
	if got.Signal != types.HealthGreen || len(got.RiskFactors) != 0 {
		t.Fatalf("healthy matter = %v %v %+v", got.Score, got.Signal, got.RiskFactors)
	}
}
