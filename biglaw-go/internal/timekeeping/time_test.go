// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package timekeeping

import (
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func TestSplitClioUnsynced(t *testing.T) {
	entries := []types.TimeEntry{
		{ID: "open", DurationMs: 0},                                          // still open — excluded entirely
		{ID: "synced", DurationMs: 360_000, ClioSyncedAt: "2026-01-01T00:00:00Z"}, // already synced — skipped
		{ID: "fresh-1", DurationMs: 360_000},                                 // needs sync
		{ID: "fresh-2", DurationMs: 90_000},                                  // needs sync
		{ID: "negative", DurationMs: -1},                                     // defensive — excluded
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
