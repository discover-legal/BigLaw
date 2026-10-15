// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package timekeeping

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestSplitClioUnsynced(t *testing.T) {
	entries := []types.TimeEntry{
		{ID: "open", DurationMs: 0}, // still open — excluded entirely
		{ID: "synced", DurationMs: 360_000, ClioSyncedAt: "2026-01-01T00:00:00Z"}, // already synced — skipped
		{ID: "fresh-1", DurationMs: 360_000},                                      // needs sync
		{ID: "fresh-2", DurationMs: 90_000},                                       // needs sync
		{ID: "negative", DurationMs: -1},                                          // defensive — excluded
		{ID: "failed-run", DurationMs: 360_000, NonBillable: true},                // written off — excluded
	}

	toSync, skipped := SplitClioUnsynced(entries)

	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	if len(toSync) != 2 {
		t.Fatalf("len(toSync) = %d, want 2", len(toSync))
	}
	if toSync[0].ID != "fresh-1" || toSync[1].ID != "fresh-2" {
		t.Errorf("toSync IDs = %s, %s; want fresh-1, fresh-2", toSync[0].ID, toSync[1].ID)
	}
}

func TestSplitClioUnsyncedEmpty(t *testing.T) {
	toSync, skipped := SplitClioUnsynced(nil)
	if toSync != nil || skipped != 0 {
		t.Errorf("SplitClioUnsynced(nil) = %v, %d; want nil, 0", toSync, skipped)
	}
}

func TestClioDurationHours(t *testing.T) {
	tests := []struct {
		name  string
		entry types.TimeEntry
		want  float64
	}{
		// 1 billing unit (0.1 h) beats 90 s elapsed (0.025 h).
		{"billing units dominate", types.TimeEntry{BillingUnits: 1, DurationMs: 90_000}, 0.1},
		// 2 h elapsed beats 1 unit — defensive, units normally ceil duration.
		{"elapsed dominates", types.TimeEntry{BillingUnits: 1, DurationMs: 7_200_000}, 2},
		// Rounding to 2 dp: 1 234 567 ms = 0.342935 h < 4 units = 0.4 h.
		{"six-minute increments", types.TimeEntry{BillingUnits: 4, DurationMs: 1_234_567}, 0.4},
		// No units recorded — raw elapsed rounded: 555 000 ms = 0.154 h → 0.15.
		{"raw elapsed rounded", types.TimeEntry{BillingUnits: 0, DurationMs: 555_000}, 0.15},
		{"zero", types.TimeEntry{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClioDurationHours(tt.entry); got != tt.want {
				t.Errorf("ClioDurationHours() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A failed/interrupted AI run keeps its elapsed time but is written off, and
// the write-off is on disk when CloseNonBillable returns (no async persist).
func TestCloseNonBillable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "time.json")
	s := NewTimeStore()
	if err := s.Init(path); err != nil {
		t.Fatal(err)
	}
	rate := 600.0
	e := s.Open(types.TimeEntry{ProfileID: "p", TaskID: "t", Event: types.TimeEventTaskRun,
		StartedAt: time.Now().Add(-10 * time.Minute), BillingRate: &rate})
	got := s.CloseNonBillable(e.ID, "task failed: boom")
	if got == nil || !got.NonBillable || got.BillingUnits != 0 || got.BillingAmountUsd != nil ||
		got.DurationMs < 9*60*1000 || got.EndedAt == nil || got.NonBillableReason != "task failed: boom" {
		t.Fatalf("CloseNonBillable = %+v", got)
	}
	// A later Close must not resurrect billing on a written-off entry.
	if again := s.Close(e.ID); again.BillingUnits != 0 || !again.NonBillable {
		t.Fatalf("re-Close re-billed a written-off entry: %+v", again)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []types.TimeEntry
	if err := json.Unmarshal(data, &onDisk); err != nil || len(onDisk) != 1 || !onDisk[0].NonBillable {
		t.Fatalf("persisted state = %s (%v)", data, err)
	}

	// The billable path still bills: 10 min → 2 units.
	e2 := s.Open(types.TimeEntry{ProfileID: "p", StartedAt: time.Now().Add(-10 * time.Minute), BillingRate: &rate})
	if c := s.Close(e2.ID); c.BillingUnits != 2 || c.BillingAmountUsd == nil || *c.BillingAmountUsd != 120 || c.NonBillable {
		t.Fatalf("Close = %+v", c)
	}
}
