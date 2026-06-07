// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// BudgetMonitor — emits alerts when matter spend crosses configured thresholds.
// BudgetPredictor — predicts final cost from historical closed-matter data.
// Pure arithmetic — no AI, no network.

package budget

import (
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// TimeStore is the subset of timekeeping.TimeStore used by budget.
type TimeStore interface {
	List(matterNumber string) []types.TimeEntry
	ListAll() []types.TimeEntry
}

// ClientStore is the subset of clients.ClientStore used by budget.
type ClientStore interface {
	List() []*types.Client
	Persist() error
}

// AlertHandler is called when a budget threshold is crossed.
type AlertHandler func(alert types.BudgetAlert)

// Monitor checks matter spend against OCG budget thresholds.
type Monitor struct {
	time     TimeStore
	clients  ClientStore
	onAlert  AlertHandler
}

// NewMonitor creates a BudgetMonitor.
func NewMonitor(time TimeStore, clients ClientStore, onAlert AlertHandler) *Monitor {
	return &Monitor{time: time, clients: clients, onAlert: onAlert}
}

// CheckMatter evaluates budget thresholds for a matter and fires alerts.
func (m *Monitor) CheckMatter(matterNumber string) {
	var client *types.Client
	var matter *types.ClientMatter
	for _, c := range m.clients.List() {
		for i := range c.Matters {
			if c.Matters[i].MatterNumber == matterNumber {
				client = c
				matter = &c.Matters[i]
				break
			}
		}
		if matter != nil {
			break
		}
	}
	if matter == nil || matter.BudgetUsd == nil {
		return
	}

	entries := m.time.List(matterNumber)
	burnUsd := 0.0
	for _, e := range entries {
		if e.EndedAt != nil && e.BillingAmountUsd != nil {
			burnUsd += *e.BillingAmountUsd
		}
	}
	burnPct := burnUsd / *matter.BudgetUsd

	thresholds := matter.BudgetAlertThresholds
	if len(thresholds) == 0 {
		thresholds = []float64{0.5, 0.8, 1.0}
	}
	triggered := map[float64]bool{}
	for _, t := range matter.BudgetAlertsTriggered {
		triggered[t] = true
	}

	changed := false
	for _, threshold := range thresholds {
		if burnPct >= threshold && !triggered[threshold] {
			triggered[threshold] = true
			changed = true
			alert := types.BudgetAlert{
				MatterNumber: matterNumber,
				ClientNumber: client.ClientNumber,
				BudgetUsd:    *matter.BudgetUsd,
				BurnUsd:      roundCents(burnUsd),
				BurnPct:      math.Round(burnPct*10000) / 10000,
				Threshold:    threshold,
				TriggeredAt:  time.Now().UTC().Format(time.RFC3339),
			}
			if m.onAlert != nil {
				m.onAlert(alert)
			}
			slog.Info("Budget threshold crossed", "matterNumber", matterNumber, "threshold", threshold, "burnPct", burnPct)
		}
	}

	if changed {
		matter.BudgetAlertsTriggered = make([]float64, 0, len(triggered))
		for t := range triggered {
			matter.BudgetAlertsTriggered = append(matter.BudgetAlertsTriggered, t)
		}
		if err := m.clients.Persist(); err != nil {
			slog.Warn("Failed to persist budget alert state", "error", err)
		}
	}
}

// GetBurn returns the current budget burn for a matter.
func (m *Monitor) GetBurn(matterNumber string) *types.BudgetBurn {
	var matter *types.ClientMatter
	for _, c := range m.clients.List() {
		for i := range c.Matters {
			if c.Matters[i].MatterNumber == matterNumber {
				matter = &c.Matters[i]
				break
			}
		}
		if matter != nil {
			break
		}
	}
	if matter == nil || matter.BudgetUsd == nil {
		return nil
	}

	entries := m.time.List(matterNumber)
	burnUsd := 0.0
	for _, e := range entries {
		if e.EndedAt != nil && e.BillingAmountUsd != nil {
			burnUsd += *e.BillingAmountUsd
		}
	}
	burnUsd = roundCents(burnUsd)
	burnPct := math.Round(burnUsd / *matter.BudgetUsd * 10000) / 10000
	return &types.BudgetBurn{
		BudgetUsd: *matter.BudgetUsd,
		BurnUsd:   burnUsd,
		BurnPct:   burnPct,
		Remaining: roundCents(*matter.BudgetUsd - burnUsd),
	}
}

// ─── BudgetPredictor ──────────────────────────────────────────────────────────

// TaskStore is used by the predictor to look up task metadata.
type TaskStore interface {
	ListAll() []types.Task
}

// MatterCostSample is a closed-matter cost data point.
type MatterCostSample struct {
	MatterNumber      string
	PracticeArea      string
	Jurisdiction      string
	TotalAmountUsd    float64
	TotalBillingUnits int
	EntryCount        int
}

// Predictor predicts final matter cost from historical comparable matters.
type Predictor struct{}

// Predict predicts the final cost of an in-progress matter.
func (p *Predictor) Predict(matterNumber string, time TimeStore, tasks TaskStore) *types.BudgetPrediction {
	entries := time.List(matterNumber)
	var closed []types.TimeEntry
	for _, e := range entries {
		if e.EndedAt != nil {
			closed = append(closed, e)
		}
	}
	if len(closed) == 0 {
		return nil
	}

	spentUsd := 0.0
	spentUnits := 0
	for _, e := range closed {
		if e.BillingAmountUsd != nil {
			spentUsd += *e.BillingAmountUsd
		}
		spentUnits += e.BillingUnits
	}

	// Find associated task for metadata
	var practiceArea, jurisdiction string
	for _, t := range tasks.ListAll() {
		if t.MatterNumber == matterNumber {
			if t.NosLegal != nil && t.NosLegal.AreaOfLaw != nil {
				practiceArea = *t.NosLegal.AreaOfLaw
			}
			jurisdiction = t.Jurisdiction
			break
		}
	}

	samples := p.buildSamples(time, tasks)

	// Select comparables in order of specificity
	var comparables []MatterCostSample
	basedOn := "all_matters"

	if practiceArea != "" && jurisdiction != "" {
		var filtered []MatterCostSample
		for _, s := range samples {
			if s.MatterNumber != matterNumber && s.PracticeArea == practiceArea && s.Jurisdiction == jurisdiction {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) >= 3 {
			comparables = filtered
			basedOn = "practice_area+jurisdiction"
		}
	}
	if comparables == nil && practiceArea != "" {
		var filtered []MatterCostSample
		for _, s := range samples {
			if s.MatterNumber != matterNumber && s.PracticeArea == practiceArea {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) >= 3 {
			comparables = filtered
			basedOn = "practice_area"
		}
	}
	if comparables == nil {
		for _, s := range samples {
			if s.MatterNumber != matterNumber {
				comparables = append(comparables, s)
			}
		}
	}

	costs := make([]float64, 0, len(comparables))
	for _, s := range comparables {
		costs = append(costs, s.TotalAmountUsd)
	}
	sort.Float64s(costs)

	count := len(costs)
	confidence := "insufficient_data"
	switch {
	case count >= 10:
		confidence = "high"
	case count >= 5:
		confidence = "medium"
	case count >= 3:
		confidence = "low"
	}

	median := percentile(costs, 0.5)
	p25 := percentile(costs, 0.25)
	p75 := percentile(costs, 0.75)
	estimatedTotal := median

	completionPct := 0.0
	estimatedRemaining := 0.0
	if estimatedTotal > 0 {
		completionPct = math.Min(99, spentUsd/estimatedTotal*100)
		estimatedRemaining = math.Max(0, estimatedTotal-spentUsd)
	}

	return &types.BudgetPrediction{
		MatterNumber:          matterNumber,
		PracticeArea:          practiceArea,
		SpentUsd:              roundCents(spentUsd),
		SpentBillingUnits:     spentUnits,
		EstimatedTotalUsd:     roundCents(estimatedTotal),
		EstimatedRemainingUsd: roundCents(estimatedRemaining),
		CompletionPct:         math.Round(completionPct*100) / 100,
		Confidence:            confidence,
		ComparableMatterCount: count,
		MedianFinalCost:       roundCents(median),
		P25FinalCost:          roundCents(p25),
		P75FinalCost:          roundCents(p75),
		BasedOn:               basedOn,
	}
}

func (p *Predictor) buildSamples(timeStore TimeStore, tasks TaskStore) []MatterCostSample {
	allEntries := timeStore.ListAll()
	grouped := map[string][]types.TimeEntry{}
	for _, e := range allEntries {
		if e.MatterNumber != "" {
			grouped[e.MatterNumber] = append(grouped[e.MatterNumber], e)
		}
	}

	// Build task lookup
	taskByMatter := map[string]types.Task{}
	for _, t := range tasks.ListAll() {
		if t.MatterNumber != "" {
			taskByMatter[t.MatterNumber] = t
		}
	}

	var samples []MatterCostSample
	for mn, entries := range grouped {
		if len(entries) < 2 {
			continue
		}
		allClosed := true
		for _, e := range entries {
			if e.EndedAt == nil {
				allClosed = false
				break
			}
		}
		if !allClosed {
			continue
		}
		totalAmt := 0.0
		totalUnits := 0
		for _, e := range entries {
			if e.BillingAmountUsd != nil {
				totalAmt += *e.BillingAmountUsd
			}
			totalUnits += e.BillingUnits
		}
		if totalAmt <= 0 {
			continue
		}
		pa := ""
		jur := ""
		if t, ok := taskByMatter[mn]; ok {
			if t.NosLegal != nil && t.NosLegal.AreaOfLaw != nil {
				pa = *t.NosLegal.AreaOfLaw
			}
			jur = t.Jurisdiction
		}
		samples = append(samples, MatterCostSample{
			MatterNumber:      mn,
			PracticeArea:      pa,
			Jurisdiction:      jur,
			TotalAmountUsd:    roundCents(totalAmt),
			TotalBillingUnits: totalUnits,
			EntryCount:        len(entries),
		})
	}
	return samples
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := p * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

func roundCents(f float64) float64 {
	return math.Round(f*100) / 100
}
