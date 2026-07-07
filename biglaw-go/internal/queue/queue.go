// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Job queue — in-memory with JSON persistence. Survives server restarts.
// Supports summarize_time_entry and ocg_bulk_check job types.

package queue

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// Queue stats summary.
type QueueStats struct {
	Pending    int `json:"pending"`
	Running    int `json:"running"`
	Done       int `json:"done"`
	Failed     int `json:"failed"`
	DeadLetter int `json:"dead_letter"`
}

// Queue is an in-memory job queue backed by JSON persistence.
type Queue struct {
	mu        sync.Mutex
	persistMu sync.Mutex // serialises concurrent fire-and-forget persists
	jobs      []*types.Job
	path      string
}

// New creates a Queue backed by path.
func New(path string) *Queue {
	return &Queue{path: path}
}

// Init loads persisted jobs, resetting any that were "running" at shutdown.
func (q *Queue) Init() error {
	data, err := os.ReadFile(q.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var jobs []*types.Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return err
	}

	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range jobs {
		if j.Status == types.JobStatusRunning {
			j.Status = types.JobStatusPending
		}
		// Prune done jobs older than 7 days
		if j.Status == types.JobStatusDone {
			t, err := time.Parse(time.RFC3339, j.CreatedAt)
			if err == nil && t.Before(cutoff) {
				continue
			}
		}
		q.jobs = append(q.jobs, j)
	}
	slog.Info("Job queue loaded", "pending", q.countStatus(types.JobStatusPending), "total", len(q.jobs))
	return nil
}

// Enqueue adds a new pending job.
func (q *Queue) Enqueue(jobType types.JobType, payload map[string]interface{}, maxRetries int) *types.Job {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	job := &types.Job{
		ID:         uuid.New().String(),
		Type:       jobType,
		Payload:    payload,
		Status:     types.JobStatusPending,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		MaxRetries: maxRetries,
	}
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.mu.Unlock()
	go q.persist()
	slog.Debug("Job enqueued", "jobId", job.ID, "type", jobType)
	return job
}

// Dequeue atomically claims one pending job, setting its status to "running".
// Pass nil to accept any job type.
func (q *Queue) Dequeue(types_ []types.JobType) *types.Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.Status != types.JobStatusPending {
			continue
		}
		if len(types_) > 0 && !containsType(types_, j.Type) {
			continue
		}
		j.Status = types.JobStatusRunning
		j.StartedAt = time.Now().UTC().Format(time.RFC3339)
		go q.persist()
		return j
	}
	return nil
}

// Ack marks a job as done.
func (q *Queue) Ack(jobID string) {
	q.mu.Lock()
	j := q.findJob(jobID)
	if j != nil {
		j.Status = types.JobStatusDone
		j.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	}
	q.mu.Unlock()
	go q.persist()
}

// Fail increments retries and moves to dead_letter when maxRetries exceeded.
func (q *Queue) Fail(jobID, errMsg string) {
	q.mu.Lock()
	j := q.findJob(jobID)
	if j != nil {
		j.Retries++
		j.Error = errMsg
		if j.Retries >= j.MaxRetries {
			j.Status = types.JobStatusDeadLetter
			slog.Warn("Job moved to dead_letter", "jobId", jobID, "type", j.Type)
		} else {
			j.Status = types.JobStatusPending
		}
	}
	q.mu.Unlock()
	go q.persist()
}

// Retry re-queues a failed or dead_letter job.
func (q *Queue) Retry(jobID string) *types.Job {
	q.mu.Lock()
	j := q.findJob(jobID)
	if j != nil {
		j.Status = types.JobStatusPending
		j.Error = ""
	}
	q.mu.Unlock()
	if j != nil {
		go q.persist()
	}
	return j
}

// List returns jobs filtered by status (empty = all), sorted by createdAt desc.
func (q *Queue) List(status types.JobStatus, limit, offset int) []*types.Job {
	q.mu.Lock()
	var out []*types.Job
	for _, j := range q.jobs {
		if status == "" || j.Status == status {
			cp := *j
			out = append(out, &cp)
		}
	}
	q.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	if offset > len(out) {
		return nil
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

// Stats returns counts by status.
func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	var s QueueStats
	for _, j := range q.jobs {
		switch j.Status {
		case types.JobStatusPending:
			s.Pending++
		case types.JobStatusRunning:
			s.Running++
		case types.JobStatusDone:
			s.Done++
		case types.JobStatusFailed:
			s.Failed++
		case types.JobStatusDeadLetter:
			s.DeadLetter++
		}
	}
	return s
}

func (q *Queue) findJob(id string) *types.Job {
	for _, j := range q.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (q *Queue) countStatus(s types.JobStatus) int {
	n := 0
	for _, j := range q.jobs {
		if j.Status == s {
			n++
		}
	}
	return n
}

func (q *Queue) persist() {
	q.persistMu.Lock()
	defer q.persistMu.Unlock()
	q.mu.Lock()
	data, err := json.MarshalIndent(q.jobs, "", "  ")
	q.mu.Unlock()
	if err != nil {
		slog.Warn("Failed to marshal job queue", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, q.path)
}

func containsType(ts []types.JobType, t types.JobType) bool {
	for _, v := range ts {
		if v == t {
			return true
		}
	}
	return false
}
