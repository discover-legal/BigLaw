// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// M8 — Full harness (smoke). All 8 arms run with MockTransport; H1–H6 emitted.
package topoflow

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestAllEightArmsAndMetrics(t *testing.T) {
	out := filepath.Join(t.TempDir(), "report.json")
	rep, err := RunSuite(DefaultConfig(), SuiteOptions{Epochs: 2, OutPath: out})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Arms) != 8 {
		t.Fatalf("expected 8 arms, got %d", len(rep.Arms))
	}
	for _, name := range []string{
		"1_fixed_linear", "2_pure_dytopo", "3_pure_agensflow", "4_topoflow_linear",
		"5_topoflow_dytopo", "6_topoflow_free_cold", "7_topoflow_free_warm", "8_no_skip_ablation",
	} {
		if _, ok := rep.Arms[name]; !ok {
			t.Errorf("missing arm %s", name)
		}
	}
	for _, key := range []string{"H1_selection", "H2_frontier", "H3_learned_vs_swept", "H4_cold_start", "H5_reward_fragility", "H6_skip_ablation"} {
		if _, ok := rep.Metrics[key]; !ok {
			t.Errorf("missing metric %s", key)
		}
	}
	// report written + round-trips
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var disk SuiteReport
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Arms) != 8 {
		t.Error("disk report should have 8 arms")
	}
}

func TestArm4RecoversArm3AndArm5RecoversArm2(t *testing.T) {
	rep, err := RunSuite(DefaultConfig(), SuiteOptions{Epochs: 2})
	if err != nil {
		t.Fatal(err)
	}
	a := rep.Arms
	if math.Abs(a["4_topoflow_linear"].MeanQuality-a["3_pure_agensflow"].MeanQuality) > 1e-6 {
		t.Error("arm4 should recover arm3 quality")
	}
	if math.Abs(a["5_topoflow_dytopo"].MeanQuality-a["2_pure_dytopo"].MeanQuality) > 1e-6 {
		t.Error("arm5 should recover arm2 quality")
	}
}

func TestH5AuditReportedSeparately(t *testing.T) {
	rep, err := RunSuite(DefaultConfig(), SuiteOptions{Epochs: 1})
	if err != nil {
		t.Fatal(err)
	}
	h5, _ := rep.Metrics["H5_reward_fragility"].(map[string]any)
	if len(h5) == 0 {
		t.Fatal("H5 should report per-arm")
	}
	for _, v := range h5 {
		rec := v.(map[string]any)
		for _, k := range []string{"live", "audit", "delta", "sign_flip"} {
			if _, ok := rec[k]; !ok {
				t.Errorf("H5 record missing %s", k)
			}
		}
	}
}
