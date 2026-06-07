// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"errors"
	"testing"
	"time"

	"github.com/discover-legal/biglaw-go/internal/providers"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// fakeProvider returns canned responses in sequence (one per Chat call).
type fakeProvider struct {
	replies []string
	err     error
	calls   int
}

func (f *fakeProvider) Chat(_ providers.ChatParams) (*providers.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	text := "{}"
	if f.calls < len(f.replies) {
		text = f.replies[f.calls]
	}
	f.calls++
	return &providers.ChatResponse{
		StopReason: providers.StopEndTurn,
		Content:    []providers.ContentBlock{{Type: providers.BlockText, Text: text}},
		Usage:      providers.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func ptrF(f float64) *float64 { return &f }

func sampleInput() ReportInput {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)
	return ReportInput{
		MatterNumber: "M-001",
		ClientNumber: "C-100",
		Health: types.MatterHealthScore{
			Score: 72, Signal: types.HealthAmber, Trend: "stable",
			RiskFactors: []types.MatterRiskFactor{{Severity: "medium", Message: "budget tight"}},
		},
		Tasks: []types.Task{
			{ID: "t1", Description: "old open task", Status: types.TaskStatusRunning, CreatedAt: old,
				Findings: []types.Finding{{ID: "f1", Timestamp: recent}}},
			{ID: "t2", Description: "new task", Status: types.TaskStatusPending, CreatedAt: recent},
			{ID: "t3", Description: "done task", Status: types.TaskStatusComplete, CreatedAt: old, CompletedAt: &recent},
		},
		TimeEntries: []types.TimeEntry{
			{ID: "e1", EndedAt: &recent, BillingUnits: 10, BillingAmountUsd: ptrF(250)}, // 1.0h
			{ID: "e2", EndedAt: &old, BillingUnits: 20, BillingAmountUsd: ptrF(500)},    // before cutoff, excluded
		},
	}
}

func TestGenerateHappyPath(t *testing.T) {
	prov := &fakeProvider{replies: []string{
		`Here you go: {"bluf":"On track, amber.","summary":"Solid week.","workstreams":[{"name":"DD","status":"on track"}],"risks":[{"severity":"medium","description":"budget"}],"openQuestions":["q1"],"sources":["facts"],"confidence":0.8}`,
		`{"grounded":true,"confidence":0.9}`,
	}}
	g := NewGenerator(prov, "claude-haiku-4-5-20251001")

	rep, err := g.Generate(sampleInput(), GenOpts{Verify: true})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Deterministic deltas (cutoff = now-24h since no prev report).
	if rep.Deltas.NewTasks != 1 {
		t.Errorf("NewTasks: want 1, got %d", rep.Deltas.NewTasks)
	}
	if rep.Deltas.ClosedTasks != 1 {
		t.Errorf("ClosedTasks: want 1, got %d", rep.Deltas.ClosedTasks)
	}
	if rep.Deltas.NewFindings != 1 {
		t.Errorf("NewFindings: want 1, got %d", rep.Deltas.NewFindings)
	}
	if rep.Deltas.HoursLogged != 1.0 {
		t.Errorf("HoursLogged: want 1.0, got %.2f", rep.Deltas.HoursLogged)
	}
	if rep.Deltas.BilledUsd != 250 {
		t.Errorf("BilledUsd: want 250, got %.2f", rep.Deltas.BilledUsd)
	}

	// Narrative parsed out of surrounding prose.
	if rep.BLUF != "On track, amber." {
		t.Errorf("BLUF: %q", rep.BLUF)
	}
	if len(rep.Workstreams) != 1 || rep.Workstreams[0].Name != "DD" {
		t.Errorf("workstreams: %+v", rep.Workstreams)
	}
	// Verify pass overrides confidence.
	if rep.Confidence != 0.9 {
		t.Errorf("confidence after verify: want 0.9, got %.2f", rep.Confidence)
	}
	if rep.HealthScore != 72 || rep.HealthSignal != "amber" {
		t.Errorf("health not threaded through: %.0f %s", rep.HealthScore, rep.HealthSignal)
	}
	if rep.ReportID == "" || rep.GeneratedBy != "claude-haiku-4-5-20251001" {
		t.Errorf("metadata missing: id=%q model=%q", rep.ReportID, rep.GeneratedBy)
	}
}

func TestGenerateFallsBackWhenModelFails(t *testing.T) {
	g := NewGenerator(&fakeProvider{err: errors.New("model down")}, "m")
	rep, err := g.Generate(sampleInput(), GenOpts{Verify: true})
	if err != nil {
		t.Fatalf("Generate should degrade, not error: %v", err)
	}
	if rep.BLUF == "" {
		t.Error("fallback BLUF should be populated when the model is unavailable")
	}
	// Deltas are still computed deterministically without the model.
	if rep.Deltas.NewTasks != 1 {
		t.Errorf("deltas should survive model failure, got NewTasks=%d", rep.Deltas.NewTasks)
	}
}

func TestGenerateUsesPrevReportCutoff(t *testing.T) {
	in := sampleInput()
	// A prev report generated 30 minutes ago — only events after that count.
	prevAt := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	in.Prev = &types.MatterStatusReport{ReportID: "prev-1", GeneratedAt: prevAt}

	prov := &fakeProvider{replies: []string{`{"bluf":"b","summary":"s","confidence":0.7}`, `{"grounded":true,"confidence":0.7}`}}
	rep, err := NewGenerator(prov, "m").Generate(in, GenOpts{Verify: false})
	if err != nil {
		t.Fatal(err)
	}
	if rep.PrevReportID != "prev-1" {
		t.Errorf("PrevReportID not linked: %q", rep.PrevReportID)
	}
	// The recent (1h ago) task/finding/time predate the 30-min cutoff → excluded.
	if rep.Deltas.NewTasks != 0 || rep.Deltas.NewFindings != 0 || rep.Deltas.HoursLogged != 0 {
		t.Errorf("cutoff not applied from prev report: %+v", rep.Deltas)
	}
}

func TestGenerateThreadsBudgetBurn(t *testing.T) {
	in := sampleInput()
	in.Budget = &types.BudgetBurn{BudgetUsd: 10000, BurnUsd: 6500, BurnPct: 0.65, Remaining: 3500}
	prov := &fakeProvider{replies: []string{`{"bluf":"b","summary":"s","confidence":0.7}`}}
	rep, err := NewGenerator(prov, "m").Generate(in, GenOpts{Verify: false})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Deltas.BudgetBurnPct != 0.65 {
		t.Errorf("budget burn pct should be threaded into deltas, got %.2f", rep.Deltas.BudgetBurnPct)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`prefix {"a":1} suffix`: `{"a":1}`,
		`{"a":1}`:               `{"a":1}`,
		`no json here`:          ``,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}
