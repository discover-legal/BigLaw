// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Boot-time quarantine of stale tasks. The defect this guards against: a task
// persisted as "running" was restored at backend start and posed as live work
// for days, contending with a benchmark run for a single local model.

package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func bootOrchestrator(t *testing.T, tasks []*types.Task, resumeRunning bool) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "tasks.json")
	data, err := json.Marshal(tasks)
	if err != nil {
		t.Fatalf("marshal tasks: %v", err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatalf("write tasks file: %v", err)
	}
	cfg := &config.Config{}
	cfg.Persistence.TasksFile = file
	cfg.Resilience.ResumeRunningTasks = resumeRunning
	return &Orchestrator{
		tasks:     map[string]*types.Task{},
		gateChans: map[string]chan struct{}{},
		cfg:       cfg,
	}
}

func staleTask(id string, status types.TaskStatus) *types.Task {
	return &types.Task{
		ID:           id,
		Description:  "benchmark matter",
		WorkflowType: types.WorkflowRoundtable,
		Status:       status,
		CreatedAt:    time.Now().Add(-72 * time.Hour),
		UpdatedAt:    time.Now().Add(-72 * time.Hour),
	}
}

func TestRestoreQuarantinesRunningTask(t *testing.T) {
	id := "zombie-running-task"
	o := bootOrchestrator(t, []*types.Task{staleTask(id, types.TaskStatusRunning)}, false)
	o.restoreTasks()

	got := o.tasks[id]
	if got == nil {
		t.Fatal("task not restored at all")
	}
	if got.Status != types.TaskStatusInterrupted {
		t.Fatalf("status = %q, want %q", got.Status, types.TaskStatusInterrupted)
	}
	if !strings.Contains(got.Error, "interrupted") {
		t.Errorf("Error = %q, want an interrupted explanation", got.Error)
	}

	found := false
	for _, entry := range audit.Default.ReadRecent(id, 10) {
		if entry.Event == "task.interrupted" {
			found = true
			if entry.Data["previousStatus"] != "running" {
				t.Errorf("previousStatus = %v, want running", entry.Data["previousStatus"])
			}
		}
	}
	if !found {
		t.Error("no task.interrupted audit event emitted")
	}

	// The quarantined status must be persisted back, so the next boot does not
	// re-quarantine (or re-zombify) the same task.
	data, err := os.ReadFile(o.cfg.Persistence.TasksFile)
	if err != nil {
		t.Fatalf("read persisted tasks: %v", err)
	}
	var persisted []*types.Task
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshal persisted tasks: %v", err)
	}
	if len(persisted) != 1 || persisted[0].Status != types.TaskStatusInterrupted {
		t.Errorf("persisted status = %v, want interrupted written back to disk", persisted)
	}
}

func TestRestoreQuarantinesAwaitingGateTask(t *testing.T) {
	id := "zombie-gate-task"
	o := bootOrchestrator(t, []*types.Task{staleTask(id, types.TaskStatusAwaitingGate)}, false)
	o.restoreTasks()
	if got := o.tasks[id]; got.Status != types.TaskStatusInterrupted {
		t.Fatalf("status = %q, want %q (the gate-waiter goroutine is just as dead)", got.Status, types.TaskStatusInterrupted)
	}
}

func TestRestoreResumeEnvFlagKeepsRunning(t *testing.T) {
	id := "opted-in-running-task"
	o := bootOrchestrator(t, []*types.Task{staleTask(id, types.TaskStatusRunning)}, true)
	o.restoreTasks()

	if got := o.tasks[id]; got.Status != types.TaskStatusRunning {
		t.Fatalf("status = %q, want running preserved under RESUME_RUNNING_TASKS", got.Status)
	}
	for _, entry := range audit.Default.ReadRecent(id, 10) {
		if entry.Event == "task.interrupted" {
			t.Error("task.interrupted emitted despite RESUME_RUNNING_TASKS=true")
		}
	}
}

func TestRestoreLeavesTerminalStatusesAlone(t *testing.T) {
	completeID, failedID, pendingID := "done-task", "failed-task", "pending-task"
	o := bootOrchestrator(t, []*types.Task{
		staleTask(completeID, types.TaskStatusComplete),
		staleTask(failedID, types.TaskStatusFailed),
		staleTask(pendingID, types.TaskStatusPending),
	}, false)
	o.restoreTasks()

	for id, want := range map[string]types.TaskStatus{
		completeID: types.TaskStatusComplete,
		failedID:   types.TaskStatusFailed,
		pendingID:  types.TaskStatusPending,
	} {
		if got := o.tasks[id]; got.Status != want {
			t.Errorf("task %s status = %q, want %q untouched", id, got.Status, want)
		}
	}
}

// Upgrade path: a 0644 snapshot (and a stale fixed-name temp file from the
// pre-atomicfile writer) end up 0600 / removed after restore + persist.
func TestPersistTightensSnapshotAndDropsLegacyTemp(t *testing.T) {
	o := bootOrchestrator(t, []*types.Task{staleTask("t1", types.TaskStatusComplete)}, false)
	file := o.cfg.Persistence.TasksFile
	if err := os.WriteFile(file+".tmp", []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.restoreTasks()
	if err := o.persistTasks(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot perm = %v (%v)", fi.Mode().Perm(), err)
	}
	if _, err := os.Stat(file + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("legacy temp file left behind: %v", err)
	}
	var back []*types.Task
	raw, _ := os.ReadFile(file)
	if err := json.Unmarshal(raw, &back); err != nil || len(back) != 1 || back[0].ID != "t1" {
		t.Fatalf("snapshot = %s (%v)", raw, err)
	}
}
