// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package matters

import (
	"fmt"
	"strings"
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

func TestInterruptedIsMediumWithOwnWording(t *testing.T) {
	i := task("t1", types.TaskStatusInterrupted)
	i.Description = "Draft the NDA"
	got := New().Compute("M-1", []types.Task{i, task("t0", types.TaskStatusComplete)},
		fakeTime{recentEntry()}, fakeBudget{})
	if len(got.RiskFactors) != 1 {
		t.Fatalf("risks = %+v", got.RiskFactors)
	}
	r := got.RiskFactors[0]
	if r.Type != "task_interrupted" || r.Severity != "medium" || strings.Contains(r.Message, "failed") {
		t.Errorf("interrupted risk = %+v", r)
	}
	if got.Dimensions.DeadlineHealth >= 85 || got.Dimensions.DeadlineHealth <= 0 {
		t.Errorf("interrupted delivery = %v, want a partial penalty", got.Dimensions.DeadlineHealth)
	}
}

func TestFailurePenaltyIsProportional(t *testing.T) {
	tasks := []types.Task{}
	for i := range 104 {
		st := types.TaskStatusComplete
		if i < 4 {
			st = types.TaskStatusFailed
		}
		tk := task(fmt.Sprint(i), st)
		// completions happened before the failures, so nothing is superseded
		done := time.Now().Add(-48 * time.Hour)
		if st == types.TaskStatusComplete {
			tk.CompletedAt = &done
		}
		tasks = append(tasks, tk)
	}
	got := New().Compute("M-1", tasks, fakeTime{recentEntry()}, fakeBudget{})
	if d := got.Dimensions.DeadlineHealth; d != 85 {
		t.Errorf("4/104 failed: delivery = %v, want 85 (proportional, min 15-point dent)", d)
	}
	if got.RiskFactors[0].Severity != "medium" {
		t.Errorf("a small failure share should be medium: %+v", got.RiskFactors)
	}
}

func TestSupersededFailureDoesNotCount(t *testing.T) {
	failedAt := time.Now().Add(-2 * time.Hour)
	f := task("f", types.TaskStatusFailed)
	f.CompletedAt = &failedAt
	ok := task("ok", types.TaskStatusComplete)
	done := time.Now().Add(-time.Hour)
	ok.CompletedAt = &done
	got := New().Compute("M-1", []types.Task{f, ok}, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth != 100 || len(got.RiskFactors) != 0 {
		t.Fatalf("superseded failure still counted: %+v", got)
	}
	// …but a failure after the last success still counts.
	later := time.Now()
	f.CompletedAt = &later
	got = New().Compute("M-1", []types.Task{f, ok}, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth == 100 {
		t.Fatal("failure after the last success ignored")
	}
}

func TestOldFailuresAgeOut(t *testing.T) {
	old := time.Now().Add(-31 * 24 * time.Hour)
	f := task("f", types.TaskStatusFailed)
	f.CreatedAt, f.UpdatedAt, f.CompletedAt = old, old, &old
	got := New().Compute("M-1", []types.Task{f}, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth != 100 {
		t.Fatalf("31-day-old failure still counted: %v", got.Dimensions.DeadlineHealth)
	}
}

func TestHealthyMatterStaysGreen(t *testing.T) {
	got := New().Compute("M-1", []types.Task{task("a", types.TaskStatusComplete)},
		fakeTime{recentEntry()}, fakeBudget{&types.BudgetBurn{BurnPct: 0.2}})
	if got.Signal != types.HealthGreen || len(got.RiskFactors) != 0 {
		t.Fatalf("healthy matter = %v %v %+v", got.Score, got.Signal, got.RiskFactors)
	}
}

// Every task interrupted: nothing delivered, so not "On track" — but worded as
// an interruption, not a failure.
func TestAllInterruptedNeedsAttention(t *testing.T) {
	tasks := []types.Task{task("a", types.TaskStatusInterrupted), task("b", types.TaskStatusInterrupted), task("c", types.TaskStatusInterrupted)}
	got := New().Compute("M-1", tasks, fakeTime{recentEntry()}, fakeBudget{})
	if got.Signal == types.HealthGreen {
		t.Fatalf("all-interrupted matter = %v %q, want below green", got.Score, got.SignalLabel)
	}
	r := got.RiskFactors[0]
	if r.Type != "task_interrupted" || r.Severity != "high" || strings.Contains(r.Message, "failed") {
		t.Errorf("risk = %+v", r)
	}
}

// An interruption weighs half a failure in the failure branch too.
func TestInterruptedHalfWeightAlongsideFailures(t *testing.T) {
	tasks := []types.Task{task("f", types.TaskStatusFailed)}
	for i := range 5 {
		tasks = append(tasks, task(fmt.Sprint("i", i), types.TaskStatusInterrupted))
	}
	got := New().Compute("M-1", tasks, fakeTime{recentEntry()}, fakeBudget{})
	// (1 + 5×0.5) / 6 → 42
	if d := got.Dimensions.DeadlineHealth; d != 42 {
		t.Errorf("delivery = %v, want 42", d)
	}
}

// Only a successful re-run of the same work supersedes a failure.
func TestUnrelatedCompletionDoesNotSupersede(t *testing.T) {
	failedAt := time.Now().Add(-2 * time.Hour)
	f := task("f", types.TaskStatusFailed)
	f.Description, f.CompletedAt = "Draft the NDA", &failedAt
	other := task("o", types.TaskStatusComplete)
	done := time.Now().Add(-time.Hour)
	other.Description, other.CompletedAt = "Research HSR thresholds", &done
	got := New().Compute("M-1", []types.Task{f, other}, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth == 100 {
		t.Fatal("unrelated completion superseded the failure")
	}
	rerun := other
	rerun.Description = "  draft the  NDA "
	got = New().Compute("M-1", []types.Task{f, rerun}, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth != 100 {
		t.Fatalf("re-run of the same work did not supersede: %v", got.Dimensions.DeadlineHealth)
	}
}

func TestUntimestampedFailureDoesNotAgeOut(t *testing.T) {
	f := types.Task{ID: "f", MatterNumber: "M-1", Status: types.TaskStatusFailed}
	got := New().Compute("M-1", []types.Task{f}, fakeTime{recentEntry()}, fakeBudget{})
	if got.Dimensions.DeadlineHealth != 0 {
		t.Fatalf("zero-timestamp failure aged out: %v", got.Dimensions.DeadlineHealth)
	}
}
