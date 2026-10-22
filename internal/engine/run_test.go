// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"strings"
	"testing"
)

// TestMemoRunEndToEnd: a scripted memo run plans, extracts a verified finding, drafts, runs the
// committee, finishes, and waits at the deliverable gate with a rendered memo.
func TestMemoRunEndToEnd(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(f.happy(), Config{})
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	if v.Status != "awaiting_gate" || v.Run.StopReason == nil || *v.Run.StopReason != "finished" {
		t.Fatalf("status %s %v error %v; steps %s", v.Status, v.Run.StopReason, v.Error, f.steps(task.ID))
	}
	if v.Output == nil || !strings.Contains(*v.Output, "$644,910") || strings.Contains(*v.Output, "{Zephyr}") || !strings.Contains(*v.Output, "## Evidence Note") {
		t.Fatalf("output:\n%v", deref(v.Output))
	}
	if len(v.PendingGates) != 1 || v.PendingGates[0].Kind != "deliverable" || len(v.Findings) != 1 || v.Findings[0].Status != "verified" {
		t.Fatalf("gates %+v findings %+v", v.PendingGates, v.Findings)
	}
}
