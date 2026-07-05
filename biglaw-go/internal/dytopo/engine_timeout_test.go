// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Round-timeout retry + starvation surfacing. The defect these guard against:
// under model contention every agent of every round exceeded the round
// timeout, the engine recorded zero findings each time, and the task
// "completed" with all its epistemic rounds empty — silently.

package dytopo

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func fakeFindings(n int) []types.Finding {
	out := make([]types.Finding, n)
	for i := range out {
		out[i] = types.Finding{ID: "f", AgentID: "a", Content: "c"}
	}
	return out
}

func TestProcessWithRetry_FastSuccess(t *testing.T) {
	var calls atomic.Int32
	got := processWithRetry("agent-1", 1, types.PhaseAnalysis, 200*time.Millisecond, 2.0,
		func() ([]types.Finding, error) {
			calls.Add(1)
			return fakeFindings(2), nil
		})
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2", len(got))
	}
	if calls.Load() != 1 {
		t.Errorf("process called %d times, want 1 (no retry on success)", calls.Load())
	}
}

// First attempt exceeds the round timeout; the retry succeeds within the
// extended budget → findings are recorded instead of silently dropped.
func TestProcessWithRetry_RetrySucceeds(t *testing.T) {
	var calls atomic.Int32
	got := processWithRetry("agent-1", 2, types.PhaseAnalysis, 50*time.Millisecond, 2.0,
		func() ([]types.Finding, error) {
			if calls.Add(1) == 1 {
				time.Sleep(2 * time.Second) // blows the 50ms budget
				return nil, errors.New("abandoned")
			}
			return fakeFindings(3), nil
		})
	if len(got) != 3 {
		t.Fatalf("findings = %d, want 3 from the retry attempt", len(got))
	}
	if calls.Load() != 2 {
		t.Errorf("process called %d times, want 2 (one retry)", calls.Load())
	}
}

// The first attempt is merely slow, not hung: it lands during the retry
// window and wins — its findings must not be discarded.
func TestProcessWithRetry_FirstAttemptLandsInRetryWindow(t *testing.T) {
	var calls atomic.Int32
	got := processWithRetry("agent-1", 2, types.PhaseAnalysis, 40*time.Millisecond, 3.0,
		func() ([]types.Finding, error) {
			if calls.Add(1) == 1 {
				time.Sleep(70 * time.Millisecond) // > base budget, < extended
				return fakeFindings(1), nil
			}
			time.Sleep(2 * time.Second) // retry is the slow one
			return nil, errors.New("abandoned")
		})
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1 from the original in-flight attempt", len(got))
	}
}

func TestProcessWithRetry_BothExceed(t *testing.T) {
	start := time.Now()
	got := processWithRetry("agent-1", 3, types.PhaseReview, 30*time.Millisecond, 2.0,
		func() ([]types.Finding, error) {
			time.Sleep(2 * time.Second)
			return fakeFindings(1), nil
		})
	if got != nil {
		t.Fatalf("findings = %v, want nil when both attempts exceed", got)
	}
	// Bounded: base + extended budget, with slack for a slow CI box.
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("took %v, want ~90ms (30ms base + 60ms retry)", elapsed)
	}
}

// A round in which every agent came back empty (both attempts exceeded) must
// be surfaced loudly: round.starved audit event + Starved flag.
func TestSurfaceStarvation_EmitsAuditEvent(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agents.RoundTimeoutMs = 30
	cfg.Resilience.RoundTimeoutRetryFactor = 2.0
	e := &Engine{cfg: cfg}

	// Simulate the round: the only agent blows both budgets → zero findings.
	findings := processWithRetry("agent-1", 4, types.PhaseAnalysis, 30*time.Millisecond, 2.0,
		func() ([]types.Finding, error) {
			time.Sleep(2 * time.Second)
			return fakeFindings(1), nil
		})

	taskID := "task-starved-test"
	goal := types.RoundGoal{Round: 4, Phase: types.PhaseAnalysis}
	starved := e.surfaceStarvation(taskID, "round-id", goal, 1, len(findings))
	if !starved {
		t.Fatal("surfaceStarvation = false, want true for a zero-finding round")
	}

	found := false
	for _, entry := range audit.Default.ReadRecent(taskID, 10) {
		if entry.Event == "round.starved" {
			found = true
			if entry.Data["round"] != 4 {
				t.Errorf("round.starved round = %v, want 4", entry.Data["round"])
			}
		}
	}
	if !found {
		t.Error("no round.starved audit event emitted for the starved round")
	}
}

func TestSurfaceStarvation_QuietWhenFindingsExist(t *testing.T) {
	cfg := &config.Config{}
	e := &Engine{cfg: cfg}
	taskID := "task-not-starved-test"
	if e.surfaceStarvation(taskID, "r", types.RoundGoal{Round: 1}, 3, 5) {
		t.Error("surfaceStarvation = true with findings present")
	}
	if e.surfaceStarvation(taskID, "r", types.RoundGoal{Round: 1}, 0, 0) {
		t.Error("surfaceStarvation = true with zero agents (nothing to starve)")
	}
	for _, entry := range audit.Default.ReadRecent(taskID, 10) {
		if entry.Event == "round.starved" {
			t.Error("round.starved emitted for a healthy round")
		}
	}
}
