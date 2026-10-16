// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func newTimeOrch(t *testing.T, task *types.Task) (*Orchestrator, *timekeeping.TimeStore) {
	t.Helper()
	cfg := &config.Config{}
	cfg.AgentBilling.Enabled = true
	cfg.AgentBilling.DefaultRate = 150
	cfg.Persistence.TasksFile = filepath.Join(t.TempDir(), "tasks.json")
	ts := timekeeping.NewTimeStore()
	if err := ts.Init(filepath.Join(t.TempDir(), "time.json")); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{
		tasks:     map[string]*types.Task{task.ID: task},
		gateChans: map[string]chan struct{}{},
		cfg:       cfg,
		time:      ts,
	}
	return o, ts
}

// An AI run is the AI timekeeper's time at the AI rate, not the lawyer's.
func TestOpenTaskTimeEntryAttributesToAI(t *testing.T) {
	task := &types.Task{ID: "t1", CreatedByProfileID: "lawyer-1", MatterNumber: "M-1", ClientNumber: "C-1",
		Description: "research", Status: types.TaskStatusRunning, CreatedAt: time.Now()}
	o, ts := newTimeOrch(t, task)
	o.openTaskTimeEntry(task)
	e := ts.GetByID(task.ActiveTimeEntryID)
	if e == nil || e.ProfileID != "" || e.AgentID != "biglaw-ai" || e.RequestedByProfileID != "lawyer-1" ||
		e.BillingRate == nil || *e.BillingRate != 150 || e.MatterNumber != "M-1" {
		t.Fatalf("entry = %+v", e)
	}
}

// No AI rate → the run is recorded but closes non-billable with the reason.
func TestAIEntryNoRateReason(t *testing.T) {
	task := &types.Task{ID: "t3", Status: types.TaskStatusRunning, CreatedAt: time.Now()}
	o, ts := newTimeOrch(t, task)
	o.cfg.AgentBilling.DefaultRate = 0
	o.openTaskTimeEntry(task)
	e := ts.Close(task.ActiveTimeEntryID)
	if !e.NonBillable || e.NonBillableReason != timekeeping.ReasonAIRateNotConfigured || e.BillingRate != nil {
		t.Fatalf("entry = %+v", e)
	}
	o.cfg.AgentBilling.Enabled = false
	o.openTaskTimeEntry(task)
	if e := ts.Close(task.ActiveTimeEntryID); e.NonBillableReason != timekeeping.ReasonAIBillingDisabled {
		t.Fatalf("disabled entry = %+v", e)
	}
}

// Deleting a task that still holds an open entry closes it non-billable.
func TestDeleteTaskClosesOpenEntry(t *testing.T) {
	task := &types.Task{ID: "t2", Status: types.TaskStatusFailed, CreatedAt: time.Now()}
	o, ts := newTimeOrch(t, task)
	o.openTaskTimeEntry(task)
	id := task.ActiveTimeEntryID
	if err := o.DeleteTask("t2"); err != nil {
		t.Fatal(err)
	}
	e := ts.GetByID(id)
	if e.EndedAt == nil || !e.NonBillable || e.NonBillableReason != timekeeping.ReasonTaskDeleted || e.BillingUnits != 0 {
		t.Fatalf("entry after delete = %+v", e)
	}
}
