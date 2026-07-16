// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"errors"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestTaskSchedulerBoundedFIFO(t *testing.T) {
	q := newTaskScheduler(1, 2)
	a := queuedTask{id: "a", workflow: types.WorkflowRoundtable}
	b := queuedTask{id: "b", workflow: types.WorkflowReview}
	if err := q.enqueue(a); err != nil {
		t.Fatal(err)
	}
	if err := q.enqueue(b); err != nil {
		t.Fatal(err)
	}
	if err := q.enqueue(queuedTask{id: "c", workflow: types.WorkflowFullBench}); !errors.Is(err, ErrTaskQueueFull) {
		t.Fatalf("third enqueue error = %v, want ErrTaskQueueFull", err)
	}

	info := q.info("b", types.TaskStatusQueued, types.WorkflowReview, time.Now())
	if info == nil || info.Position != 2 {
		t.Fatalf("queue info = %+v, want position 2", info)
	}
	first, ok := q.claim()
	if !ok || first.id != "a" {
		t.Fatalf("first claim = %+v, %v; want a", first, ok)
	}
	second, ok := q.claim()
	if !ok || second.id != "b" {
		t.Fatalf("second claim = %+v, %v; want b", second, ok)
	}
}

func TestTaskSchedulerEstimateLearnsFromHistory(t *testing.T) {
	q := newTaskScheduler(2, 10)
	for _, elapsed := range []time.Duration{4 * time.Minute, 5 * time.Minute, 6 * time.Minute} {
		q.observe(types.WorkflowReview, elapsed)
	}
	if err := q.enqueue(queuedTask{id: "review", workflow: types.WorkflowReview}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	info := q.info("review", types.TaskStatusQueued, types.WorkflowReview, now)
	if info == nil {
		t.Fatal("expected queue estimate")
	}
	if info.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium", info.Confidence)
	}
	if got := info.Completion.Likely.Sub(info.EstimatedStartAt); got < 4*time.Minute || got > 6*time.Minute {
		t.Fatalf("learned duration = %v, want 4m..6m", got)
	}
	if !info.Completion.Earliest.Before(info.Completion.Likely) || !info.Completion.Latest.After(info.Completion.Likely) {
		t.Fatalf("invalid estimate window: %+v", info.Completion)
	}
}

func TestTaskSchedulerRemoveQueuedTask(t *testing.T) {
	q := newTaskScheduler(1, 2)
	_ = q.enqueue(queuedTask{id: "cancel", workflow: types.WorkflowRoundtable})
	if !q.remove("cancel") {
		t.Fatal("remove returned false")
	}
	if info := q.info("cancel", types.TaskStatusQueued, types.WorkflowRoundtable, time.Now()); info != nil {
		t.Fatalf("removed task still has queue info: %+v", info)
	}
}
