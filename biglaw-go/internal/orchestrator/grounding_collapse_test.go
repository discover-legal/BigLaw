// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package orchestrator

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/types"
)

func collapseOrch(action string) *Orchestrator {
	return &Orchestrator{cfg: &config.Config{Grounding: config.GroundingConfig{
		CollapseThreshold:   0.5,
		CollapseMinFindings: 20,
		CollapseAction:      action,
	}}}
}

func mkFindings(grounded, unverified int) []types.Finding {
	fs := make([]types.Finding, 0, grounded+unverified)
	for i := 0; i < grounded; i++ {
		fs = append(fs, types.Finding{EvidenceStatus: types.EvidenceGrounded})
	}
	for i := 0; i < unverified; i++ {
		fs = append(fs, types.Finding{EvidenceStatus: types.EvidenceUnverified})
	}
	return fs
}

func TestGroundingCollapseFail(t *testing.T) {
	o := collapseOrch("fail")
	task := &types.Task{ID: "t1"}
	// 78/87 unverified — the local-model matter run's actual shape.
	_, err := o.applyGroundingCollapse(task, mkFindings(9, 78))
	if err == nil {
		t.Fatal("collapse with action=fail must return an error")
	}
	if !strings.Contains(err.Error(), "grounding collapse") || !strings.Contains(err.Error(), "90%") {
		t.Fatalf("error should name the condition and rate, got: %v", err)
	}
}

func TestGroundingCollapseStrictDropsUnverified(t *testing.T) {
	o := collapseOrch("strict")
	task := &types.Task{ID: "t1"}
	in := mkFindings(15, 25)
	// Machinery findings without a status must survive strict mode.
	in = append(in, types.Finding{EvidenceStatus: ""})
	out, err := o.applyGroundingCollapse(task, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 16 {
		t.Fatalf("strict should keep 15 grounded + 1 unstatused, got %d", len(out))
	}
	if task.GroundingAlert == "" || !strings.Contains(task.GroundingAlert, "strict mode engaged") {
		t.Fatalf("strict must record the alert on the task, got %q", task.GroundingAlert)
	}
}

func TestGroundingCollapseWarnKeepsAll(t *testing.T) {
	o := collapseOrch("warn")
	task := &types.Task{ID: "t1"}
	out, err := o.applyGroundingCollapse(task, mkFindings(5, 45))
	if err != nil || len(out) != 50 {
		t.Fatalf("warn keeps everything, got %d, %v", len(out), err)
	}
	if task.GroundingAlert == "" {
		t.Fatal("warn must record the alert on the task")
	}
}

func TestGroundingCollapseNotTriggered(t *testing.T) {
	o := collapseOrch("fail")
	task := &types.Task{ID: "t1"}

	// Healthy rate (27/730-style): well under threshold.
	if _, err := o.applyGroundingCollapse(task, mkFindings(96, 4)); err != nil {
		t.Fatalf("healthy run must not collapse: %v", err)
	}
	// Below the minimum sample: high rate but too few findings to judge.
	if _, err := o.applyGroundingCollapse(task, mkFindings(1, 10)); err != nil {
		t.Fatalf("small sample must not collapse: %v", err)
	}
	// Disabled via threshold 0.
	o.cfg.Grounding.CollapseThreshold = 0
	if _, err := o.applyGroundingCollapse(task, mkFindings(0, 100)); err != nil {
		t.Fatalf("threshold 0 disables the policy: %v", err)
	}
}

func TestGroundingCollapseCountsPriorRounds(t *testing.T) {
	o := collapseOrch("fail")
	task := &types.Task{ID: "t1", Rounds: []types.RoundState{{Findings: mkFindings(0, 18)}}}
	// Only 4 findings this round, but 18 prior unverified push past both the
	// minimum sample and the threshold.
	if _, err := o.applyGroundingCollapse(task, mkFindings(2, 2)); err == nil {
		t.Fatal("cumulative counting must include prior rounds")
	}
}
