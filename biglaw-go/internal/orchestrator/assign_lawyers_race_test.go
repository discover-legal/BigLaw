// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Coverage gap this file targets: AssignLawyers (orchestrator.go ~426-440)
// writes task.AssignedLawyerIDs correctly under o.mu, but then reads
// `*task` again AFTER o.mu.Unlock() to build the returned copy:
//
//	o.mu.Lock()
//	task := o.tasks[taskID]
//	task.AssignedLawyerIDs = lawyerIDs
//	task.UpdatedAt = time.Now()
//	o.mu.Unlock()
//	...
//	cp := *task          // <-- unsynchronized read
//	return &cp
//
// Every other reader of a live task (snapshot(), ListTasks()) takes its
// copy while still holding the lock. runPhase mutates the same *types.Task
// via o.update() (itself correctly locked) throughout a round, so a
// concurrent AssignLawyers call races that mutation. No existing test
// exercises AssignLawyers concurrently with another writer — this one
// does, and is meant to be run with `go test -race`.

package orchestrator

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestAssignLawyersRaceWithConcurrentUpdate(t *testing.T) {
	const taskID = "race-task"
	task := &types.Task{
		ID:          taskID,
		Description: "race target",
		Status:      types.TaskStatusRunning,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	cfg := &config.Config{}
	cfg.Persistence.TasksFile = t.TempDir() + "/tasks.json"

	o := &Orchestrator{
		tasks:     map[string]*types.Task{taskID: task},
		gateChans: map[string]chan struct{}{},
		cfg:       cfg,
	}

	stop := make(chan struct{})
	var writes int64

	// Writer goroutine: mimics runPhase's continuous, correctly-locked task
	// mutation while a round is in flight.
	var wg sync.WaitGroup
	wg.Add(1)
	start := make(chan struct{})
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			select {
			case <-stop:
				return
			default:
			}
			o.update(task, func(tk *types.Task) {
				tk.Findings = append(tk.Findings, types.Finding{ID: "f"})
				tk.UpdatedAt = time.Now()
			})
			atomic.AddInt64(&writes, 1)
		}
	}()

	// AssignLawyers concurrently. Its post-unlock `cp := *task` read races
	// the writer above under `go test -race`.
	close(start)
	for i := 0; i < 20; i++ {
		got := o.AssignLawyers(taskID, []string{"lawyer-1"}, "partner-1")
		if got == nil {
			t.Fatalf("AssignLawyers(%d) returned nil for a task known to exist", i)
		}
	}

	close(stop)
	wg.Wait()

	if atomic.LoadInt64(&writes) == 0 {
		t.Fatal("writer goroutine never ran — test is not actually racing anything")
	}

	// TODO once AssignLawyers is fixed to take its `cp := *task` copy while
	// still holding o.mu (matching snapshot()'s pattern at orchestrator.go
	// ~395-400): assert the final state is exactly what's expected —
	//   final := o.snapshot(task)
	//   if len(final.AssignedLawyerIDs) != 1 || final.AssignedLawyerIDs[0] != "lawyer-1" {
	//       t.Errorf("AssignedLawyerIDs = %v, want [lawyer-1]", final.AssignedLawyerIDs)
	//   }
	// Today the value assertion is secondary — the point of this test is
	// that `go test -race ./internal/orchestrator/...` must fail on the
	// unfixed code and pass once the copy moves inside the lock.
}
