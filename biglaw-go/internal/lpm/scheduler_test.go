// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"testing"
	"time"
)

func TestSchedulerFiresOncePerDayAtHour(t *testing.T) {
	var fired int
	s := NewScheduler(6, func(time.Time) { fired++ })

	day1 := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)

	// Before the hour: no fire.
	if s.maybeRun(day1.Add(5 * time.Hour)) {
		t.Fatal("should not fire before the target hour")
	}
	if fired != 0 {
		t.Fatalf("fired=%d before hour", fired)
	}

	// At the hour: fires once.
	if !s.maybeRun(day1.Add(6 * time.Hour)) {
		t.Fatal("should fire at the target hour")
	}
	// Later same day: does not fire again (idempotent).
	if s.maybeRun(day1.Add(8 * time.Hour)) {
		t.Fatal("should not fire twice in one day")
	}
	if fired != 1 {
		t.Fatalf("want 1 fire on day1, got %d", fired)
	}

	// Next day at the hour: fires again.
	day2 := day1.Add(24 * time.Hour)
	if !s.maybeRun(day2.Add(6 * time.Hour)) {
		t.Fatal("should fire on the next day")
	}
	if fired != 2 {
		t.Fatalf("want 2 fires across two days, got %d", fired)
	}
}

func TestSchedulerInvalidHourDefaults(t *testing.T) {
	s := NewScheduler(99, func(time.Time) {})
	if s.hour != 6 {
		t.Errorf("invalid hour should default to 6, got %d", s.hour)
	}
}
