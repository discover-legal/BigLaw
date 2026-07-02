// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package lpm

import (
	"archive/zip"
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

func seedCorpus(t *testing.T) *Corpus {
	t.Helper()
	c := NewCorpus(filepath.Join(t.TempDir(), "corpus.jsonl"))
	reports := []*types.MatterStatusReport{
		{ReportID: "r1", MatterNumber: "M-001", Date: "2026-06-07", GeneratedAt: "2026-06-07T06:00:00Z",
			HealthScore: 80, HealthSignal: "green", BLUF: "Acme on track."},
		{ReportID: "r2", MatterNumber: "M-002", Date: "2026-06-07", GeneratedAt: "2026-06-07T06:00:00Z",
			HealthScore: 35, HealthSignal: "red", BLUF: "Beta in trouble.",
			Risks: []types.LPMRisk{{Severity: "high", Description: "deadline missed"}, {Severity: "low", Description: "minor"}}},
	}
	for _, r := range reports {
		if err := c.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestPortfolioGenerateSortsWorstFirstAndCounts(t *testing.T) {
	corpus := seedCorpus(t)
	prov := &fakeProvider{replies: []string{"Beta needs attention; Acme steady."}}
	b := NewPortfolioBriefer(prov, "m")

	matters := []MatterRef{{MatterNumber: "M-001"}, {MatterNumber: "M-002"}, {MatterNumber: "M-003"}}
	br, err := b.Generate(matters, corpus, "2026-06-07")
	if err != nil {
		t.Fatal(err)
	}
	if br.MatterCount != 3 {
		t.Errorf("matter count: %d", br.MatterCount)
	}
	if br.Red != 1 || br.Green != 1 {
		t.Errorf("signal counts wrong: red=%d green=%d", br.Red, br.Green)
	}
	// Worst reported matter first (M-002, red 35), then M-001 (green 80); the
	// matter with no report yet (M-003) sorts last, not first.
	if br.Matters[0].MatterNumber != "M-002" {
		t.Errorf("worst reported matter should sort first, got %s", br.Matters[0].MatterNumber)
	}
	if br.Matters[1].MatterNumber != "M-001" {
		t.Errorf("green reported matter should precede the unreported one, got %s", br.Matters[1].MatterNumber)
	}
	if br.Matters[2].MatterNumber != "M-003" {
		t.Errorf("matter with no report should sort last, got %s", br.Matters[2].MatterNumber)
	}
	// Top risk surfaced for M-002.
	for _, m := range br.Matters {
		if m.MatterNumber == "M-002" && m.TopRisk != "deadline missed" {
			t.Errorf("top risk: %q", m.TopRisk)
		}
	}
	if br.BLUF == "" {
		t.Error("BLUF should be populated from the model")
	}
}

func TestPortfolioFallbackBLUFWhenNoModel(t *testing.T) {
	corpus := seedCorpus(t)
	b := NewPortfolioBriefer(nil, "m") // no provider
	br, err := b.Generate([]MatterRef{{MatterNumber: "M-002"}}, corpus, "2026-06-07")
	if err != nil {
		t.Fatal(err)
	}
	if br.BLUF == "" || !strings.Contains(br.BLUF, "red") {
		t.Errorf("fallback BLUF expected, got %q", br.BLUF)
	}
}

func TestPortfolioRenderersValid(t *testing.T) {
	corpus := seedCorpus(t)
	b := NewPortfolioBriefer(nil, "m")
	br, _ := b.Generate([]MatterRef{{MatterNumber: "M-001"}, {MatterNumber: "M-002"}}, corpus, "2026-06-07")

	md := RenderPortfolioMarkdown(br)
	if !strings.Contains(md, "Portfolio Briefing") || !strings.Contains(md, "M-002") {
		t.Error("markdown missing expected content")
	}
	docx, err := RenderPortfolioDOCX(br)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx))); err != nil {
		t.Errorf("portfolio docx invalid: %v", err)
	}
}

func TestTopRiskSeverityOrdering(t *testing.T) {
	got := topRisk([]types.LPMRisk{
		{Severity: "low", Description: "lo"},
		{Severity: "high", Description: "hi"},
		{Severity: "medium", Description: "med"},
	})
	if got != "hi" {
		t.Errorf("topRisk should pick highest severity, got %q", got)
	}
	if topRisk(nil) != "" {
		t.Error("topRisk(nil) should be empty")
	}
}
