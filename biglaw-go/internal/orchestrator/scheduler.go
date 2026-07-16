// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/strutil"
	"github.com/discover-legal/biglaw-go/internal/types"
)

var ErrTaskQueueFull = errors.New("task queue is full")

type queuedTask struct {
	id       string
	workflow types.WorkflowType
}

type runningTask struct {
	workflow types.WorkflowType
	started  time.Time
}

type durationStat struct {
	count int
	mean  time.Duration
}

// taskScheduler owns admission order and running-task accounting. Task data
// remains in Orchestrator.tasks and is persisted there; the scheduler stores
// only small identifiers and timing statistics.
type taskScheduler struct {
	mu         sync.Mutex
	maxPending int
	workers    int
	pending    []queuedTask
	running    map[string]runningTask
	stats      map[types.WorkflowType]durationStat
	wake       chan struct{}
	stop       chan struct{}
}

func newTaskScheduler(workers, maxPending int) *taskScheduler {
	if workers < 1 {
		workers = 1
	}
	if maxPending < workers {
		maxPending = workers
	}
	return &taskScheduler{
		maxPending: maxPending,
		workers:    workers,
		running:    make(map[string]runningTask),
		stats:      make(map[types.WorkflowType]durationStat),
		wake:       make(chan struct{}, 1),
		stop:       make(chan struct{}),
	}
}

func (q *taskScheduler) enqueue(task queuedTask) error {
	q.mu.Lock()
	if len(q.pending) >= q.maxPending {
		q.mu.Unlock()
		return fmt.Errorf("%w: maximum pending tasks is %d", ErrTaskQueueFull, q.maxPending)
	}
	for _, item := range q.pending {
		if item.id == task.id {
			q.mu.Unlock()
			return nil
		}
	}
	if _, exists := q.running[task.id]; exists {
		q.mu.Unlock()
		return nil
	}
	q.pending = append(q.pending, task)
	q.mu.Unlock()
	q.notify()
	return nil
}

func (q *taskScheduler) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *taskScheduler) claim() (queuedTask, bool) {
	for {
		q.mu.Lock()
		if len(q.pending) > 0 {
			item := q.pending[0]
			q.pending[0] = queuedTask{}
			q.pending = q.pending[1:]
			q.running[item.id] = runningTask{workflow: item.workflow, started: time.Now()}
			more := len(q.pending) > 0
			q.mu.Unlock()
			if more {
				q.notify()
			}
			return item, true
		}
		q.mu.Unlock()

		select {
		case <-q.wake:
		case <-q.stop:
			return queuedTask{}, false
		}
	}
}

func (q *taskScheduler) finish(id string, elapsed time.Duration) {
	q.mu.Lock()
	running, exists := q.running[id]
	delete(q.running, id)
	if exists && elapsed > 0 {
		stat := q.stats[running.workflow]
		stat.count++
		if stat.count == 1 {
			stat.mean = elapsed
		} else {
			// EWMA follows recent backend/model performance without allowing one
			// outlier to completely replace the history.
			stat.mean = time.Duration(float64(stat.mean)*0.75 + float64(elapsed)*0.25)
		}
		q.stats[running.workflow] = stat
	}
	q.mu.Unlock()
	q.notify()
}

func (q *taskScheduler) remove(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, item := range q.pending {
		if item.id != id {
			continue
		}
		copy(q.pending[i:], q.pending[i+1:])
		q.pending[len(q.pending)-1] = queuedTask{}
		q.pending = q.pending[:len(q.pending)-1]
		return true
	}
	return false
}

func (q *taskScheduler) observe(workflow types.WorkflowType, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	q.mu.Lock()
	stat := q.stats[workflow]
	stat.count++
	if stat.count == 1 {
		stat.mean = elapsed
	} else {
		stat.mean = time.Duration((int64(stat.mean)*int64(stat.count-1) + int64(elapsed)) / int64(stat.count))
	}
	q.stats[workflow] = stat
	q.mu.Unlock()
}

func (q *taskScheduler) info(id string, status types.TaskStatus, workflow types.WorkflowType, now time.Time) *types.TaskQueueInfo {
	q.mu.Lock()
	defer q.mu.Unlock()

	stat := q.stats[workflow]
	own := estimatedWorkflowDuration(workflow, stat)
	confidence := estimateConfidence(stat.count)

	if running, ok := q.running[id]; ok {
		elapsed := now.Sub(running.started)
		remaining := own - elapsed
		if remaining < time.Second {
			remaining = time.Second
		}
		return queueInfo(0, running.started, now, 0, remaining, confidence)
	}
	if status != types.TaskStatusQueued && status != types.TaskStatusPending {
		return nil
	}

	position := 0
	workAhead := time.Duration(0)
	for _, running := range q.running {
		remaining := estimatedWorkflowDuration(running.workflow, q.stats[running.workflow]) - now.Sub(running.started)
		if remaining > 0 {
			workAhead += remaining
		}
	}
	for i, item := range q.pending {
		if item.id == id {
			position = i + 1
			break
		}
		workAhead += estimatedWorkflowDuration(item.workflow, q.stats[item.workflow])
	}
	if position == 0 {
		return nil
	}
	startDelay := workAhead / time.Duration(q.workers)
	start := now.Add(startDelay)
	return queueInfo(position, start, now, startDelay, own, confidence)
}

func queueInfo(position int, start, now time.Time, startDelay, duration time.Duration, confidence string) *types.TaskQueueInfo {
	likely := start.Add(duration)
	earliest := now.Add(scaleDuration(startDelay, 0.7)).Add(scaleDuration(duration, 0.7))
	latest := now.Add(scaleDuration(startDelay, 1.5)).Add(scaleDuration(duration, 1.5))
	return &types.TaskQueueInfo{
		Position:         position,
		EstimatedStartAt: start,
		Completion: types.EstimateWindow{
			Earliest: earliest,
			Likely:   likely,
			Latest:   latest,
		},
		Confidence:   confidence,
		CalculatedAt: now,
	}
}

func scaleDuration(d time.Duration, factor float64) time.Duration {
	return time.Duration(float64(d) * factor)
}

func estimatedWorkflowDuration(workflow types.WorkflowType, stat durationStat) time.Duration {
	if stat.count > 0 && stat.mean > 0 {
		return stat.mean
	}
	switch workflow {
	case types.WorkflowFullBench:
		return 20 * time.Minute
	case types.WorkflowAdversarial, types.WorkflowReview:
		return 12 * time.Minute
	case types.WorkflowTabulate, types.WorkflowLegalDesign:
		return 8 * time.Minute
	case types.WorkflowPreEngagement:
		return 5 * time.Minute
	default:
		return 10 * time.Minute
	}
}

func estimateConfidence(samples int) string {
	switch {
	case samples >= 10:
		return "high"
	case samples >= 3:
		return "medium"
	default:
		return "low"
	}
}

func (o *Orchestrator) startTaskWorkers() {
	o.workersOnce.Do(func() {
		var restored []*types.Task
		o.mu.RLock()
		for _, task := range o.tasks {
			if task.Status == types.TaskStatusQueued || task.Status == types.TaskStatusPending {
				restored = append(restored, task)
			}
			if task.Status == types.TaskStatusComplete && task.StartedAt != nil && task.CompletedAt != nil {
				o.scheduler.observe(task.WorkflowType, task.CompletedAt.Sub(*task.StartedAt))
			}
		}
		o.mu.RUnlock()
		sort.Slice(restored, func(i, j int) bool { return restored[i].CreatedAt.Before(restored[j].CreatedAt) })
		for _, task := range restored {
			o.update(task, func(t *types.Task) { t.Status = types.TaskStatusQueued })
			if err := o.scheduler.enqueue(queuedTask{id: task.ID, workflow: task.WorkflowType}); err != nil {
				o.update(task, func(t *types.Task) {
					t.Status = types.TaskStatusFailed
					t.Error = "restored task could not be admitted: " + err.Error()
				})
			}
		}
		if len(restored) > 0 {
			_ = o.persistTasks()
		}
		for i := 0; i < o.scheduler.workers; i++ {
			go o.taskWorker(i)
		}
	})
}

func (o *Orchestrator) taskWorker(workerID int) {
	for {
		item, ok := o.scheduler.claim()
		if !ok {
			return
		}
		o.mu.RLock()
		task := o.tasks[item.id]
		o.mu.RUnlock()
		if task == nil {
			o.scheduler.finish(item.id, 0)
			continue
		}
		started := time.Now()
		o.update(task, func(t *types.Task) {
			t.Status = types.TaskStatusRunning
			t.StartedAt = &started
			t.UpdatedAt = started
		})
		o.openTaskTimeEntry(task)
		o.persistTasks()
		emitProgress(task.ID, "started", map[string]interface{}{"worker": workerID})
		o.executeQueuedTask(task)
		o.scheduler.finish(task.ID, time.Since(started))
	}
}

func (o *Orchestrator) executeQueuedTask(task *types.Task) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("task worker recovered panic", "taskId", task.ID, "panic", recovered)
			o.update(task, func(t *types.Task) {
				t.Status = types.TaskStatusFailed
				t.Error = fmt.Sprintf("task worker panic: %v", recovered)
				t.UpdatedAt = time.Now()
			})
			if task.ActiveTimeEntryID != "" {
				o.time.Close(task.ActiveTimeEntryID)
				o.update(task, func(t *types.Task) { t.ActiveTimeEntryID = "" })
			}
			emitProgress(task.ID, "failed", map[string]interface{}{"error": "task worker panic"})
			audit.Default.Write(audit.WriteRequest{Event: "task.failed", ActorID: audit.ActorSystem, TaskID: task.ID, Data: map[string]interface{}{"error": "task worker panic"}})
			o.persistTasks()
		}
	}()
	o.runTask(task)
}

func (o *Orchestrator) openTaskTimeEntry(task *types.Task) {
	if task.CreatedByProfileID == "" || o.profiles == nil || o.time == nil {
		return
	}
	profile := o.profiles.Get(task.CreatedByProfileID)
	if profile == nil {
		return
	}
	entry := o.time.Open(types.TimeEntry{
		ProfileID:    profile.ID,
		ProfileName:  profile.Name,
		TaskID:       task.ID,
		MatterNumber: task.MatterNumber,
		ClientNumber: task.ClientNumber,
		Description:  "Task: " + strutil.Truncate(task.Description, 200),
		Event:        types.TimeEventTaskRun,
		StartedAt:    time.Now(),
	})
	o.update(task, func(t *types.Task) { t.ActiveTimeEntryID = entry.ID })
}
