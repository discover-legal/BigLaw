// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package budget

import (
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

type fakeTime struct{ entries []types.TimeEntry }

func (f fakeTime) List(matter string) []types.TimeEntry {
	var out []types.TimeEntry
	for _, e := range f.entries {
		if e.MatterNumber == matter {
			out = append(out, e)
		}
	}
	return out
}
func (f fakeTime) ListAll() []types.TimeEntry { return f.entries }

type fakeClients struct{ clients []*types.Client }

func (f *fakeClients) List() []*types.Client { return f.clients }

func (f *fakeClients) SetMatterBudgetAlerts(matterNumber string, triggered []float64) error {
	for _, c := range f.clients {
		for i := range c.Matters {
			if c.Matters[i].MatterNumber == matterNumber {
				c.Matters[i].BudgetAlertsTriggered = triggered
			}
		}
	}
	return nil
}

func usd(v float64) *float64 { return &v }

func TestBudgetCheckMatterFiresThenDedups(t *testing.T) {
	budgetUsd := 1000.0
	matter := types.ClientMatter{MatterNumber: "M-001", BudgetUsd: &budgetUsd}
	client := &types.Client{ClientNumber: "C-1", Matters: []types.ClientMatter{matter}}

	endedAt := time.Now()
	ft := fakeTime{entries: []types.TimeEntry{
		{MatterNumber: "M-001", EndedAt: &endedAt, BillingAmountUsd: usd(850)}, // 85% burn
	}}
	fc := &fakeClients{clients: []*types.Client{client}}

	var alerts []types.BudgetAlert
	m := NewMonitor(ft, fc, func(a types.BudgetAlert) { alerts = append(alerts, a) })

	// First check: 0.5 and 0.8 thresholds crossed (not 1.0).
	m.CheckMatter("M-001")
	if len(alerts) != 2 {
		t.Fatalf("first check: want 2 alerts (0.5, 0.8), got %d", len(alerts))
	}

	// Second check: dedup — no new alerts because state persisted via SetMatterBudgetAlerts.
	m.CheckMatter("M-001")
	if len(alerts) != 2 {
		t.Errorf("dedup failed: want 2 total alerts, got %d", len(alerts))
	}

	// The triggered state must have been written back to the client matter.
	if len(fc.clients[0].Matters[0].BudgetAlertsTriggered) != 2 {
		t.Errorf("triggered thresholds not persisted: %+v", fc.clients[0].Matters[0].BudgetAlertsTriggered)
	}
}

// TestMonitor_StartStop_Lifecycle covers the ticker lifecycle (budget.go:56-89)
// which has zero coverage today: Start must run an immediate check plus
// periodic ones, a second Start call must be a no-op (not replace the
// ticker / leak a goroutine), and Stop must be safe to call without a prior
// Start (nil-ticker guard).
func TestMonitor_StartStop_Lifecycle(t *testing.T) {
	budgetUsd := 1000.0
	client := &types.Client{Matters: []types.ClientMatter{{MatterNumber: "M-001", BudgetUsd: &budgetUsd}}}
	endedAt := time.Now()
	ft := fakeTime{entries: []types.TimeEntry{{MatterNumber: "M-001", EndedAt: &endedAt, BillingAmountUsd: usd(900)}}}
	fc := &fakeClients{clients: []*types.Client{client}}

	checks := make(chan struct{}, 10)
	m := NewMonitor(ft, fc, func(a types.BudgetAlert) { checks <- struct{}{} })

	// Stop before Start must not panic (nil ticker/stop channel guard).
	m.Stop()

	m.Start(time.Hour, func() []string { return []string{"M-001"} })
	defer m.Stop()

	select {
	case <-checks:
		// Start must run an immediate check synchronously/soon, not wait a
		// full interval — confirmed by at least one alert firing (0.5 and 0.8
		// thresholds crossed at 90% burn).
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not perform an immediate check")
	}

	// A second Start call must be a no-op — assert the ticker field is
	// unchanged (requires access to the unexported field from within the
	// package, which this test file already has).
	m.mu.Lock()
	first := m.ticker
	m.mu.Unlock()
	m.Start(time.Hour, func() []string { return []string{"M-001"} })
	m.mu.Lock()
	second := m.ticker
	m.mu.Unlock()
	if first != second {
		t.Error("Start() called twice replaced the ticker, want the first call to win")
	}

	// TODO: assert Stop() actually halts the ticker goroutine — e.g. by
	// checking no further sends arrive on `checks` after Stop() plus a short
	// grace period. Note: unlike a sync.Once, Stop()'s safety on a second call
	// relies entirely on the `m.ticker != nil` guard resetting ticker to nil
	// after the first Stop — confirm that guard can never race with a
	// concurrent Start() (both take mu, so this should hold, but it's worth
	// pinning with -race given Start/Stop can plausibly be called from
	// different goroutines in the REST layer).
	m.Stop()
	m.Stop() // second call must not panic (ticker is nil after the first Stop)
}

func TestBudgetGetBurn(t *testing.T) {
	budgetUsd := 1000.0
	client := &types.Client{Matters: []types.ClientMatter{{MatterNumber: "M-001", BudgetUsd: &budgetUsd}}}
	endedAt := time.Now()
	ft := fakeTime{entries: []types.TimeEntry{
		{MatterNumber: "M-001", EndedAt: &endedAt, BillingAmountUsd: usd(250)},
	}}
	m := NewMonitor(ft, &fakeClients{clients: []*types.Client{client}}, nil)

	burn := m.GetBurn("M-001")
	if burn == nil || burn.BurnUsd != 250 || burn.BurnPct != 0.25 || burn.Remaining != 750 {
		t.Fatalf("unexpected burn: %+v", burn)
	}
	if m.GetBurn("UNKNOWN") != nil {
		t.Error("GetBurn for an unbudgeted matter should be nil")
	}
}
