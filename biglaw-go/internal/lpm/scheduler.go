// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// A minimal once-a-day scheduler. It checks the clock each minute and fires the
// run callback the first time it sees the configured local-time hour on a given
// day — idempotent across restarts within the same day via a last-run-date guard.
// This is the 0600 trigger for the daily status-report sweep; it deliberately
// avoids any external cron so the low-power box stays self-contained.
package lpm

import (
	"log/slog"
	"sync"
	"time"
)

// Scheduler fires run() once per day at the configured hour.
type Scheduler struct {
	hour       int
	run        func(time.Time)
	tick       time.Duration
	now        func() time.Time // injectable clock for tests
	mu         sync.Mutex
	lastRunDay string
	stop       chan struct{}
	stopped    bool
}

// NewScheduler builds a daily scheduler for the given local-time hour (0–23).
func NewScheduler(hour int, run func(time.Time)) *Scheduler {
	if hour < 0 || hour > 23 {
		hour = 6
	}
	return &Scheduler{
		hour: hour,
		run:  run,
		tick: time.Minute,
		now:  time.Now,
		stop: make(chan struct{}),
	}
}

// Start launches the scheduler loop in a goroutine.
func (s *Scheduler) Start() {
	go func() {
		t := time.NewTicker(s.tick)
		defer t.Stop()
		// Evaluate immediately so a process started exactly at the hour still fires.
		s.maybeRun(s.now())
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				s.maybeRun(s.now())
			}
		}
	}()
	slog.Info("LPM scheduler started", "dailyHour", s.hour)
}

// Stop halts the scheduler loop.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.stopped {
		close(s.stop)
		s.stopped = true
	}
}

// maybeRun fires run() once if now is at/after the target hour and today has not
// already run. Exposed (unexported) for direct testing.
func (s *Scheduler) maybeRun(now time.Time) bool {
	day := now.Format("2006-01-02")
	s.mu.Lock()
	if now.Hour() < s.hour || s.lastRunDay == day {
		s.mu.Unlock()
		return false
	}
	s.lastRunDay = day
	s.mu.Unlock()

	slog.Info("LPM daily run firing", "day", day, "hour", now.Hour())
	s.run(now)
	return true
}
