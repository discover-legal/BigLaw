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

// TestDeletedDocumentFailsRun (PR 4b r1 M2): a task whose document was deleted after submission
// fails with a fixed message naming the cause, instead of staying "running" and being claimed
// again at every lease expiry, forever.
func TestDeletedDocumentFailsRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(f.happy(), Config{})
	task := f.submit(0)
	if err := f.d.Delete(context.Background(), f.docs[1].ID); err != nil {
		t.Fatal(err)
	}
	f.step(context.Background())
	v := f.get(task.ID)
	if stop(v) != "failed/failed" || v.Error == nil || !strings.Contains(*v.Error, "no longer exists") {
		t.Fatalf("%s error %v", stop(v), v.Error)
	}
}

// TestRunAccountsEveryStep (PR 4b r1 M3): the run's step, budget and cost ledger count every step
// of a long run (maxSteps up to 10,000), not the first page of the step log.
func TestRunAccountsEveryStep(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine(nil, Config{})
	task := f.submit(2000)
	if _, err := f.st.DB().Exec(`INSERT INTO task_steps (task_id, step, kind, tool, status, usage)
		SELECT $1, g, 'tool_call', 'retrieve', 'committed', '{"calls": 1, "usd": 0.01}' FROM generate_series(1, 1200) g`, task.ID); err != nil {
		t.Fatal(err)
	}
	v := f.get(task.ID)
	c, err := f.e.Cost(context.Background(), task.ID)
	if err != nil || v.Run.Step != 1200 || v.Run.Budget.Calls != 1200 || c.Summary.Calls != 1200 || len(c.Entries) != 1200 {
		t.Fatalf("run %+v, cost %+v (%d entries), %v", v.Run, c.Summary, len(c.Entries), err)
	}
}
