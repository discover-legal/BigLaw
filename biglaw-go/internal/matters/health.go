// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// MatterHealthMonitor — per-matter composite health score (0–100).
// Five dimensions: budget 30%, deadline 25%, activity 20%, gates 15%, OCG 10%.
// green ≥ 75 | amber ≥ 45 | red < 45. Pure arithmetic — no AI, no network.

package matters

import (
	"fmt"
	"math"
	"time"

	"github.com/discover-legal/biglaw-go/internal/types"
)

var dimensionWeights = struct {
	Budget   float64
	Deadline float64
	Activity float64
	Gates    float64
	OCG      float64
}{0.30, 0.25, 0.20, 0.15, 0.10}

// BudgetBurner can return budget burn for a matter.
type BudgetBurner interface {
	GetBurn(matterNumber string) *types.BudgetBurn
}

// TimeReader can list time entries for a matter.
type TimeReader interface {
	List(matterNumber string) []types.TimeEntry
}

// Monitor computes per-matter health scores.
type Monitor struct {
	history map[string][]types.MatterHealthScore
}

// New creates a MatterHealthMonitor.
func New() *Monitor {
	return &Monitor{history: make(map[string][]types.MatterHealthScore)}
}

// Compute returns a health score for a single matter.
func (m *Monitor) Compute(
	matterNumber string,
	allTasks []types.Task,
	time_ TimeReader,
	budget BudgetBurner,
) types.MatterHealthScore {
	matterTasks := filterTasks(allTasks, matterNumber)
	entries := time_.List(matterNumber)

	var closedEntries []types.TimeEntry
	for _, e := range entries {
		if e.EndedAt != nil {
			closedEntries = append(closedEntries, e)
		}
	}

	// Last activity
	var lastActivityMs int64
	for _, e := range closedEntries {
		ms := e.EndedAt.UnixMilli()
		if ms > lastActivityMs {
			lastActivityMs = ms
		}
	}

	// Open gates
	openGates := 0
	for _, t := range matterTasks {
		for _, g := range t.PendingGates {
			if g.Status == "pending" {
				openGates++
			}
		}
	}

	// OCG violation rate
	violating := 0
	for _, e := range closedEntries {
		for _, s := range e.OcgSuggestions {
			if s.Severity == "hard" && s.Status == "pending" {
				violating++
				break
			}
		}
	}
	violationRate := 0.0
	if len(closedEntries) > 0 {
		violationRate = float64(violating) / float64(len(closedEntries))
	}

	burn := budget.GetBurn(matterNumber)

	bScore, bRisk := budgetDimension(burn)
	dScore, dRisk := deadlineDimension(matterTasks)
	aScore, aRisk := activityDimension(lastActivityMs)
	gScore, gRisk := gateDimension(openGates)
	oScore, oRisk := ocgDimension(violationRate)

	composite := math.Round(
		float64(bScore)*dimensionWeights.Budget +
			float64(dScore)*dimensionWeights.Deadline +
			float64(aScore)*dimensionWeights.Activity +
			float64(gScore)*dimensionWeights.Gates +
			float64(oScore)*dimensionWeights.OCG,
	)

	var signal types.HealthSignal
	var signalLabel string
	switch {
	case composite >= 75:
		signal = types.HealthGreen
		signalLabel = "On track"
	case composite >= 45:
		signal = types.HealthAmber
		signalLabel = "Needs attention"
	default:
		signal = types.HealthRed
		signalLabel = "At risk"
	}

	var riskFactors []types.MatterRiskFactor
	for _, r := range []*types.MatterRiskFactor{bRisk, dRisk, aRisk, gRisk, oRisk} {
		if r != nil {
			riskFactors = append(riskFactors, *r)
		}
	}
	// Sort: high → medium → low
	severityOrder := map[string]int{"high": 0, "medium": 1, "low": 2}
	for i := 0; i < len(riskFactors)-1; i++ {
		for j := i + 1; j < len(riskFactors); j++ {
			if severityOrder[riskFactors[i].Severity] > severityOrder[riskFactors[j].Severity] {
				riskFactors[i], riskFactors[j] = riskFactors[j], riskFactors[i]
			}
		}
	}

	trend := m.detectTrend(matterNumber, composite)

	result := types.MatterHealthScore{
		MatterNumber: matterNumber,
		Score:        composite,
		Signal:       signal,
		SignalLabel:  signalLabel,
		Dimensions: types.MatterHealthDimensions{
			BudgetHealth:      float64(bScore),
			DeadlineHealth:    float64(dScore),
			ActivityFreshness: float64(aScore),
			GateBacklog:       float64(gScore),
			OcgCompliance:     float64(oScore),
		},
		RiskFactors: riskFactors,
		Trend:       trend,
		ComputedAt:  time.Now().UTC().Format(time.RFC3339),
	}

	hist := m.history[matterNumber]
	hist = append(hist, result)
	if len(hist) > 10 {
		hist = hist[1:]
	}
	m.history[matterNumber] = hist

	return result
}

// Portfolio computes health scores for all given matters.
func (m *Monitor) Portfolio(
	allMatters []string,
	allTasks []types.Task,
	time_ TimeReader,
	budget BudgetBurner,
) types.PortfolioHealthSummary {
	scores := make([]types.MatterHealthScore, 0, len(allMatters))
	for _, mn := range allMatters {
		scores = append(scores, m.Compute(mn, allTasks, time_, budget))
	}
	// Sort worst first
	for i := 0; i < len(scores)-1; i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[i].Score > scores[j].Score {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}
	green, amber, red := 0, 0, 0
	for _, s := range scores {
		switch s.Signal {
		case types.HealthGreen:
			green++
		case types.HealthAmber:
			amber++
		case types.HealthRed:
			red++
		}
	}
	return types.PortfolioHealthSummary{
		TotalMatters: len(scores),
		Green:        green,
		Amber:        amber,
		Red:          red,
		Matters:      scores,
		ComputedAt:   time.Now().UTC().Format(time.RFC3339),
	}
}

// ─── Dimension calculators ────────────────────────────────────────────────────

func budgetDimension(burn *types.BudgetBurn) (int, *types.MatterRiskFactor) {
	if burn == nil {
		return 85, nil
	}
	pct := burn.BurnPct * 100
	var score int
	var risk *types.MatterRiskFactor
	switch {
	case pct >= 100:
		score = 0
		risk = &types.MatterRiskFactor{
			Type:            "budget_overrun",
			Severity:        "high",
			Message:         formatPct("Matter is %d%% through budget (over budget)", pct),
			SuggestedAction: "File a budget adjustment request and notify the client immediately.",
		}
	case pct >= 80:
		score = int(math.Max(0, 100-(pct-50)*2))
		risk = &types.MatterRiskFactor{
			Type:            "budget_overrun",
			Severity:        "high",
			Message:         formatPct("Matter is %d%% through budget", pct),
			SuggestedAction: "Notify the partner and prepare a revised budget estimate.",
		}
	case pct >= 50:
		score = int(math.Max(0, 100-(pct-50)*2))
		risk = &types.MatterRiskFactor{
			Type:            "budget_overrun",
			Severity:        "medium",
			Message:         formatPct("Matter is %d%% through budget", pct),
			SuggestedAction: "Monitor spend closely over the next billing cycle.",
		}
	default:
		score = 100
	}
	return score, risk
}

func deadlineDimension(tasks []types.Task) (int, *types.MatterRiskFactor) {
	now := time.Now()
	sevenDays := 7 * 24 * time.Hour
	overdue := 0
	imminent := 0
	for _, t := range tasks {
		if t.Status == "failed" {
			overdue++
		}
		if t.Status == "pending" || t.Status == "running" {
			if now.Sub(t.CreatedAt) > sevenDays*2 {
				imminent++
			}
		}
	}
	score := 100
	var risk *types.MatterRiskFactor
	if overdue > 0 {
		score = int(math.Max(0, float64(100-overdue*30)))
		risk = &types.MatterRiskFactor{
			Type:            "task_failure",
			Severity:        "high",
			Message:         formatN("%d task(s) have failed on this matter", overdue),
			SuggestedAction: "Review failed tasks and re-run or reassign.",
		}
	} else if imminent > 0 {
		score = int(math.Max(30, float64(100-imminent*15)))
		risk = &types.MatterRiskFactor{
			Type:            "deadline_approaching",
			Severity:        "medium",
			Message:         formatN("%d task(s) have been running for over 2 weeks", imminent),
			SuggestedAction: "Check task progress and ensure no blockers.",
		}
	}
	return score, risk
}

func activityDimension(lastActivityMs int64) (int, *types.MatterRiskFactor) {
	if lastActivityMs == 0 {
		return 40, &types.MatterRiskFactor{
			Type:            "stale_activity",
			Severity:        "medium",
			Message:         "No billing activity recorded on this matter",
			SuggestedAction: "Confirm the matter is still active.",
		}
	}
	daysSince := time.Since(time.UnixMilli(lastActivityMs)).Hours() / 24
	score := 100
	var risk *types.MatterRiskFactor
	switch {
	case daysSince <= 7:
		score = 100
	case daysSince <= 14:
		score = 80
	case daysSince <= 30:
		score = 60
	case daysSince <= 60:
		score = 40
		risk = &types.MatterRiskFactor{
			Type:            "stale_activity",
			Severity:        "low",
			Message:         formatN("No activity in %d days", int(daysSince)),
			SuggestedAction: "Follow up with the assigned lawyer to confirm matter status.",
		}
	default:
		score = 15
		risk = &types.MatterRiskFactor{
			Type:            "stale_activity",
			Severity:        "medium",
			Message:         formatN("No activity in %d days", int(daysSince)),
			SuggestedAction: "Review whether this matter should be formally closed or reassigned.",
		}
	}
	return score, risk
}

func gateDimension(openGates int) (int, *types.MatterRiskFactor) {
	if openGates == 0 {
		return 100, nil
	}
	score := int(math.Max(0, float64(100-openGates*25)))
	sev := "medium"
	if openGates >= 3 {
		sev = "high"
	}
	return score, &types.MatterRiskFactor{
		Type:            "gate_backlog",
		Severity:        sev,
		Message:         formatN("%d human gate(s) awaiting review", openGates),
		SuggestedAction: "Review and approve or reject pending findings to unblock the matter.",
	}
}

func ocgDimension(violationRate float64) (int, *types.MatterRiskFactor) {
	score := int(math.Max(0, (1-violationRate)*100))
	var risk *types.MatterRiskFactor
	if violationRate >= 0.2 {
		sev := "medium"
		if violationRate >= 0.5 {
			sev = "high"
		}
		risk = &types.MatterRiskFactor{
			Type:            "ocg_violations",
			Severity:        sev,
			Message:         formatPct("%d%% of billing entries have OCG violations", violationRate*100),
			SuggestedAction: "Review flagged entries in the pre-bill queue before sending the invoice.",
		}
	}
	return score, risk
}

func (m *Monitor) detectTrend(matterNumber string, current float64) types.HealthTrend {
	hist := m.history[matterNumber]
	if len(hist) < 2 {
		return "stable"
	}
	prev := hist[len(hist)-1].Score
	diff := current - prev
	switch {
	case diff >= 5:
		return "improving"
	case diff <= -5:
		return "deteriorating"
	default:
		return "stable"
	}
}

func filterTasks(tasks []types.Task, matterNumber string) []types.Task {
	var out []types.Task
	for _, t := range tasks {
		if t.MatterNumber == matterNumber {
			out = append(out, t)
		}
	}
	return out
}

func formatPct(format string, pct float64) string {
	return fmt.Sprintf(format, int(math.Round(pct)))
}

func formatN(format string, n int) string {
	return fmt.Sprintf(format, n)
}
