// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Boot-time quarantine for stale tasks restored from persistence.
//
// A task persisted in a mid-run status ("running" / "awaiting_gate") describes
// a runner goroutine that died with the previous process. Nothing resumes it
// after a restart, so left alone it poses as live work forever: it holds a
// slot against the max-concurrent-tasks cap, keeps a billable time entry open,
// and misleads every consumer that polls it — a benchmark driver saw exactly
// this when a days-old zombie contended with a fresh run for a single local
// model. The quarantine marks such tasks "interrupted" with an audit trail;
// they must be explicitly resubmitted. RESUME_RUNNING_TASKS=true restores the
// old leave-it-as-is behaviour for anyone who relies on it.

package orchestrator

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// quarantineStaleTask marks a task restored mid-run as interrupted, closes its
// open time entry, and writes a task.interrupted audit event. Returns true if
// the task was quarantined. Called from restoreTasks under o.mu; t is not yet
// visible to any other goroutine, so mutating it directly is safe.
func (o *Orchestrator) quarantineStaleTask(t *types.Task) bool {
	if t.Status != types.TaskStatusRunning && t.Status != types.TaskStatusAwaitingGate {
		return false
	}
	if o.cfg.Resilience.ResumeRunningTasks {
		slog.Warn("RESUME_RUNNING_TASKS=true: restoring mid-run task without a live runner",
			"taskId", t.ID, "status", t.Status)
		return false
	}

	prev := t.Status
	t.Status = types.TaskStatusInterrupted
	t.Error = fmt.Sprintf("interrupted: backend restarted while task was %s; resubmit to rerun", prev)
	t.UpdatedAt = time.Now()
	if t.ActiveTimeEntryID != "" && o.time != nil {
		o.time.Close(t.ActiveTimeEntryID)
		t.ActiveTimeEntryID = ""
	}

	slog.Warn("stale task quarantined at boot: was persisted mid-run, no runner survives a restart",
		"taskId", t.ID, "previousStatus", prev, "createdAt", t.CreatedAt)
	audit.Default.Write(audit.WriteRequest{
		Event:   "task.interrupted",
		ActorID: audit.ActorSystem,
		TaskID:  t.ID,
		Data: map[string]interface{}{
			"previousStatus": string(prev),
			"reason":         "restored from persistence mid-run; the runner goroutine did not survive the restart",
			"resume":         "resubmit the task, or set RESUME_RUNNING_TASKS=true to restore the old behaviour",
		},
	})
	return true
}
