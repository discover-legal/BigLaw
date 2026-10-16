// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/discover-legal/biglaw-go/internal/atomicfile"
	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/billing"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// failedRunWriteOffID names the one-off migration in the marker file.
const failedRunWriteOffID = "writeoff-failed-runs-v1"

// migrationMarker is <TIME_FILE>.migrations.json: which one-off time-entry
// migrations have run, when, and what they did.
type migrationMarker map[string]migrationRecord

type migrationRecord struct {
	RanAt            string   `json:"ranAt"`
	WrittenOff       int      `json:"writtenOff"`
	SkippedFinalised []string `json:"skippedFinalised,omitempty"`
	DraftPreBills    []string `json:"draftPreBills,omitempty"`
}

// writeOffHistoricalFailedRuns is a one-off, idempotent migration: before
// failed/interrupted AI runs (and runs whose synthesis failed, which still
// "completed" with an error string as the deliverable) were closed
// non-billable, they were billed. It writes those historical entries off — unless the entry is already on a
// finalised pre-bill (approved / invoiced) or synced to Clio, which would
// rewrite money already sent; those are skipped and reported for a human.
// Draft/reviewed pre-bills that list a written-off entry are reported so
// they can be regenerated. Runs only in the process that owns the stores,
// and records itself in a marker file so later starts skip it.
//
// A task's outcome comes from the task store; for a task since deleted, the
// audit log's last task.failed / task.interrupted / task.complete event is
// the second source (a deleted task's synthesis failure left no trace and
// cannot be recovered).
func writeOffHistoricalFailedRuns(timeFile, preBillsFile, auditLogFile string, tasks []*types.Task, ts *timekeeping.TimeStore) {
	markerPath := timeFile + ".migrations.json"
	marker := migrationMarker{}
	if raw, err := os.ReadFile(markerPath); err == nil {
		_ = json.Unmarshal(raw, &marker)
	}
	if _, done := marker[failedRunWriteOffID]; done {
		return
	}

	// Why each task's AI run is not billable ("" = billable / unknown).
	failure := taskOutcomesFromAudit(auditLogFile)
	for _, t := range tasks {
		if t == nil {
			continue
		}
		switch {
		case t.Status == types.TaskStatusFailed || t.Status == types.TaskStatusInterrupted:
			failure[t.ID] = "task " + string(t.Status)
		case t.Status == types.TaskStatusComplete && strings.HasPrefix(t.Output, "Synthesis error:"):
			failure[t.ID] = "synthesis failed"
		default:
			failure[t.ID] = ""
		}
	}

	// Pre-bill membership: finalised bills protect their entries; drafts are
	// reported for regeneration.
	finalised := map[string]string{} // entryID → bill ID
	drafts := map[string][]string{}  // entryID → bill IDs
	pb := billing.NewPreBillStore(preBillsFile)
	if err := pb.Init(); err != nil {
		// Unknown pre-bill state: cannot prove an entry is not invoiced, so
		// do nothing and retry next start.
		slog.Error("failed-run write-off: cannot read pre-bills; migration deferred", "path", preBillsFile, "err", err)
		return
	}
	for _, b := range pb.List("") {
		for _, e := range b.Entries {
			if b.Status == billing.PreBillApproved || b.Status == billing.PreBillInvoiced {
				finalised[e.EntryID] = b.ID
			} else {
				drafts[e.EntryID] = append(drafts[e.EntryID], b.ID)
			}
		}
	}

	var skipped []string
	draftSet := map[string]bool{}
	written := ts.WriteOff(func(e types.TimeEntry) string {
		why := failure[e.TaskID]
		if why == "" {
			return ""
		}
		if e.Event != types.TimeEventTaskRun {
			return "" // only the AI run itself; never a lawyer's own entries
		}
		if billID, ok := finalised[e.ID]; ok {
			skipped = append(skipped, e.ID+" (pre-bill "+billID+")")
			return ""
		}
		if e.ClioSyncedAt != "" {
			skipped = append(skipped, e.ID+" (synced to Clio)")
			return ""
		}
		for _, id := range drafts[e.ID] {
			draftSet[id] = true
		}
		return why + " (historical write-off)"
	})

	rec := migrationRecord{RanAt: time.Now().UTC().Format(time.RFC3339), WrittenOff: len(written), SkippedFinalised: skipped}
	for id := range draftSet {
		rec.DraftPreBills = append(rec.DraftPreBills, id)
	}
	ids := make([]string, 0, len(written))
	for _, e := range written {
		ids = append(ids, e.ID)
	}
	slog.Info("failed-run write-off migration", "writtenOff", len(written), "skippedFinalised", len(skipped), "draftPreBillsToRegenerate", rec.DraftPreBills)
	audit.Default.Write(audit.WriteRequest{
		Event:   "time.migration.failed_runs_written_off",
		ActorID: audit.ActorSystem,
		Data: map[string]interface{}{
			"migration": failedRunWriteOffID, "writtenOff": ids,
			"skippedFinalised": skipped, "draftPreBillsToRegenerate": rec.DraftPreBills,
		},
	})

	marker[failedRunWriteOffID] = rec
	raw, _ := json.MarshalIndent(marker, "", "  ")
	if err := atomicfile.WriteFile(markerPath, raw, 0o600); err != nil {
		slog.Warn("failed-run write-off: marker not saved (migration will re-run idempotently)", "err", err)
	}
}

// taskOutcomesFromAudit maps task ID → why its run is not billable, from the
// last terminal task event in the audit log ("" when the last one was
// task.complete). A missing or unreadable log yields an empty map.
func taskOutcomesFromAudit(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e struct {
				Event  string `json:"event"`
				TaskID string `json:"taskId"`
			}
			if json.Unmarshal(line, &e) == nil && e.TaskID != "" {
				switch e.Event {
				case "task.failed":
					out[e.TaskID] = "task failed"
				case "task.interrupted":
					out[e.TaskID] = "task interrupted"
				case "task.complete":
					out[e.TaskID] = ""
				}
			}
		}
		if err != nil {
			return out
		}
	}
}
