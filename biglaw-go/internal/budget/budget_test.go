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
