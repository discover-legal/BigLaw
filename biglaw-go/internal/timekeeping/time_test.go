// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package timekeeping

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func newStore(t *testing.T) *TimeStore {
	t.Helper()
	s := NewTimeStore()
	if err := s.Init(filepath.Join(t.TempDir(), "time.json")); err != nil {
		t.Fatal(err)
	}
	return s
}

// AI time bills at the AI timekeeper's own rate, and only active time:
// a pause (human gate) does not count.
func TestAIEntryRateAndPause(t *testing.T) {
	s := newStore(t)
	rate := 120.0
	e := s.Open(types.TimeEntry{AgentID: "biglaw-ai", AgentName: "BigLaw AI", RequestedByProfileID: "lawyer-1",
		Event: types.TimeEventTaskRun, StartedAt: time.Now().Add(-40 * time.Minute), BillingRate: &rate})
	// Simulate a 30-minute gate wait inside the 40 minutes.
	s.Pause(e.ID)
	s.mu.Lock()
	past := time.Now().Add(-30 * time.Minute)
	s.entries[0].PausedAt = &past
	s.mu.Unlock()
	s.Resume(e.ID)
	got := s.Close(e.ID)
	if got.PausedMs < 29*60*1000 || got.DurationMs > 11*60*1000 || got.DurationMs < 9*60*1000 {
		t.Fatalf("paused=%dms duration=%dms, want ~30m paused / ~10m active", got.PausedMs, got.DurationMs)
	}
	if got.BillingUnits != 2 || got.BillingAmountUsd == nil || *got.BillingAmountUsd != 24 || got.NonBillable {
		t.Fatalf("AI entry billed %+v, want 2 units at $120/h = $24", got)
	}
	if got.ProfileID != "" {
		t.Fatal("AI time must not be attributed to the lawyer")
	}
	// The requesting lawyer can see it; another lawyer cannot.
	if n := len(s.List(TimeFilter{VisibleToProfileID: "lawyer-1"})); n != 1 {
		t.Fatalf("requester sees %d entries, want 1", n)
	}
	if n := len(s.List(TimeFilter{VisibleToProfileID: "lawyer-2"})); n != 0 {
		t.Fatalf("other lawyer sees %d entries, want 0", n)
	}
	if n := len(s.List(TimeFilter{ProfileID: "lawyer-1"})); n != 0 {
		t.Fatal("profileId filter is the timekeeper; AI time is not the lawyer's")
	}
}

func TestAIEntryWithoutRateIsNonBillable(t *testing.T) {
	s := newStore(t)
	e := s.Open(types.TimeEntry{AgentID: "biglaw-ai", Event: types.TimeEventTaskRun, StartedAt: time.Now().Add(-10 * time.Minute)})
	got := s.Close(e.ID)
	if !got.NonBillable || got.NonBillableReason != ReasonAIRateNotConfigured || got.BillingUnits != 0 {
		t.Fatalf("got %+v, want non-billable %q", got, ReasonAIRateNotConfigured)
	}
}

func TestPausedAtCloseExcludesOpenPause(t *testing.T) {
	s := newStore(t)
	rate := 100.0
	e := s.Open(types.TimeEntry{AgentID: "a", StartedAt: time.Now().Add(-20 * time.Minute), BillingRate: &rate})
	s.Pause(e.ID)
	s.mu.Lock()
	past := time.Now().Add(-15 * time.Minute)
	s.entries[0].PausedAt = &past
	s.mu.Unlock()
	got := s.Close(e.ID) // closed while still paused
	if got.PausedAt != nil || got.DurationMs > 6*60*1000 || got.BillingUnits != 1 {
		t.Fatalf("got %+v, want ~5m active, 1 unit", got)
	}
}

func TestNewAITimekeeper(t *testing.T) {
	t.Setenv("AI_TIMEKEEPER_ID", "")
	t.Setenv("AI_TIMEKEEPER_NAME", "")
	if tk := NewAITimekeeper(true, 0); tk.RateUSD != nil || tk.ID != "biglaw-ai" || tk.Name != "BigLaw AI" ||
		tk.NonBillableReason != ReasonAIRateNotConfigured {
		t.Fatalf("unset rate: %+v", tk)
	}
	if tk := NewAITimekeeper(true, -5); tk.RateUSD != nil {
		t.Fatalf("negative rate must be treated as not configured: %+v", tk)
	}
	if tk := NewAITimekeeper(false, 200); tk.RateUSD != nil || tk.NonBillableReason != ReasonAIBillingDisabled {
		t.Fatalf("disabled: %+v", tk)
	}
	t.Setenv("AI_TIMEKEEPER_ID", "firm-ai")
	if tk := NewAITimekeeper(true, 95.5); tk.RateUSD == nil || *tk.RateUSD != 95.5 || tk.ID != "firm-ai" || tk.NonBillableReason != "" {
		t.Fatalf("configured: %+v", tk)
	}
}

func TestWriteOffIdempotent(t *testing.T) {
	s := newStore(t)
	rate := 100.0
	a := s.Open(types.TimeEntry{AgentID: "x", TaskID: "t1", StartedAt: time.Now().Add(-7 * time.Minute), BillingRate: &rate})
	s.Close(a.ID)
	open := s.Open(types.TimeEntry{AgentID: "x", TaskID: "t1", BillingRate: &rate})
	calls := 0
	sel := func(e types.TimeEntry) string { calls++; return "task failed" }
	if got := s.WriteOff(sel); len(got) != 1 || got[0].ID != a.ID || got[0].BillingUnits != 0 {
		t.Fatalf("first write-off = %+v", got)
	}
	if got := s.WriteOff(sel); len(got) != 0 {
		t.Fatalf("second write-off touched %d entries", len(got))
	}
	if calls != 1 {
		t.Fatalf("selector offered already written-off/open entries: %d calls", calls)
	}
	if s.GetByID(open.ID).NonBillable {
		t.Fatal("open entry must not be written off")
	}
}

// AI-timekeeper entries are agent time (task_run + AgentID), not human time;
// the CSV carries the write-off and requester columns.
func TestAgentOnlyAndCSVColumns(t *testing.T) {
	ts := NewTimeStore()
	if err := ts.Init(filepath.Join(t.TempDir(), "time.json")); err != nil {
		t.Fatal(err)
	}
	r := 100.0
	ai := ts.Open(types.TimeEntry{AgentID: "biglaw-ai", BillingRate: &r, Event: types.TimeEventTaskRun, RequestedByProfileID: "p1"})
	ts.CloseNonBillable(ai.ID, "task failed")
	hu := ts.Open(types.TimeEntry{ProfileID: "p1", BillingRate: &r, Event: types.TimeEventGateReview})
	ts.Close(hu.ID)
	yes, no := true, false
	if a, h := ts.List(TimeFilter{AgentOnly: &yes}), ts.List(TimeFilter{AgentOnly: &no}); len(a) != 1 || a[0].ID != ai.ID || len(h) != 1 || h[0].ID != hu.ID {
		t.Fatalf("agentOnly=%v humanOnly=%v", a, h)
	}
	csv := strings.Split(ts.ExportCSV(TimeFilter{AgentOnly: &yes}), "\n")
	if !strings.HasSuffix(csv[0], ",nonBillable,nonBillableReason,requestedByProfileId,pausedMs") ||
		!strings.HasSuffix(csv[1], `,true,"task failed","p1",0`) {
		t.Fatalf("csv = %q", csv[:2])
	}
}
