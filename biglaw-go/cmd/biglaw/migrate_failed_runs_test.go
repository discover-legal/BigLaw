// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/billing"
	"github.com/discover-legal/biglaw-go/internal/timekeeping"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestWriteOffHistoricalFailedRuns(t *testing.T) {
	dir := t.TempDir()
	timeFile := filepath.Join(dir, "time.json")
	preBillsFile := filepath.Join(dir, "prebills.json")

	ts := timekeeping.NewTimeStore()
	if err := ts.Init(timeFile); err != nil {
		t.Fatal(err)
	}
	rate := 500.0
	mk := func(task string, ev types.TimeEventType) types.TimeEntry {
		e := ts.Open(types.TimeEntry{ProfileID: "lawyer", TaskID: task, Event: ev,
			StartedAt: time.Now().Add(-10 * time.Minute), BillingRate: &rate})
		return *ts.Close(e.ID)
	}
	failed := mk("t-failed", types.TimeEventTaskRun)
	interrupted := mk("t-int", types.TimeEventTaskRun)
	onInvoice := mk("t-failed", types.TimeEventTaskRun)
	synced := mk("t-failed", types.TimeEventTaskRun)
	onDraft := mk("t-failed", types.TimeEventTaskRun)
	gate := mk("t-failed", types.TimeEventGateReview) // a lawyer's own time: untouched
	ok := mk("t-ok", types.TimeEventTaskRun)
	unknown := mk("t-gone", types.TimeEventTaskRun)             // task deleted, no audit trace: unknown
	deletedFailed := mk("t-del-failed", types.TimeEventTaskRun) // deleted; audit says failed
	retried := mk("t-del-retried", types.TimeEventTaskRun)      // deleted; failed, then completed
	synthErr := mk("t-synth", types.TimeEventTaskRun)           // "completed" with a synthesis error
	ts.MarkClioSynced(synced.ID)

	pb := billing.NewPreBillStore(preBillsFile)
	_ = pb.Init()
	inv := pb.Create("M", "C", "p", []types.TimeEntry{onInvoice})
	pb.Transition(inv.ID, billing.PreBillReviewed)
	pb.Transition(inv.ID, billing.PreBillApproved)
	pb.Transition(inv.ID, billing.PreBillInvoiced)
	draft := pb.Create("M", "C", "p", []types.TimeEntry{onDraft})

	tasks := []*types.Task{
		{ID: "t-failed", Status: types.TaskStatusFailed},
		{ID: "t-int", Status: types.TaskStatusInterrupted},
		{ID: "t-ok", Status: types.TaskStatusComplete, Output: "Memo"},
		{ID: "t-synth", Status: types.TaskStatusComplete, Output: "Synthesis error: model unavailable"},
	}
	auditFile := filepath.Join(dir, "audit.jsonl")
	auditLines := `{"event":"task.started","taskId":"t-del-failed"}
{"event":"task.failed","taskId":"t-del-failed","data":{"error":"x"}}
{"event":"task.failed","taskId":"t-del-retried"}
not json
{"event":"task.complete","taskId":"t-del-retried"}
{"event":"task.failed","taskId":"t-ok"}
{"event":"task.deleted","taskId":"t-del-failed"}`
	if err := os.WriteFile(auditFile, []byte(auditLines), 0o600); err != nil {
		t.Fatal(err)
	}
	writeOffHistoricalFailedRuns(timeFile, preBillsFile, auditFile, tasks, ts)

	want := map[string]bool{failed.ID: true, interrupted.ID: true, onDraft.ID: true,
		onInvoice.ID: false, synced.ID: false, gate.ID: false, ok.ID: false, unknown.ID: false,
		deletedFailed.ID: true, retried.ID: false, synthErr.ID: true}
	for id, nb := range want {
		e := ts.GetByID(id)
		if e.NonBillable != nb {
			t.Errorf("entry %s nonBillable=%v want %v (%+v)", id, e.NonBillable, nb, e)
		}
		if nb && (e.BillingUnits != 0 || e.BillingAmountUsd != nil || e.DurationMs == 0) {
			t.Errorf("written-off entry must keep time, zero billing: %+v", e)
		}
	}

	raw, err := os.ReadFile(timeFile + ".migrations.json")
	if err != nil {
		t.Fatal(err)
	}
	var marker migrationMarker
	_ = json.Unmarshal(raw, &marker)
	rec := marker[failedRunWriteOffID]
	if rec.WrittenOff != 5 || len(rec.SkippedFinalised) != 2 || len(rec.DraftPreBills) != 1 || rec.DraftPreBills[0] != draft.ID {
		t.Fatalf("marker = %s", raw)
	}

	// Recorded as run: a later start does nothing even if a new failed
	// entry appears (new failures are closed non-billable at run time).
	later := mk("t-failed", types.TimeEventTaskRun)
	writeOffHistoricalFailedRuns(timeFile, preBillsFile, auditFile, tasks, ts)
	if ts.GetByID(later.ID).NonBillable {
		t.Fatal("migration re-ran despite its marker")
	}
}
