// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for the COST_MODEL_RATES env JSON (per-model pricing overrides).

package config

import "testing"

func TestEnvModelRates_ValidJSON(t *testing.T) {
	t.Setenv("COST_MODEL_RATES", `{"gpt-5.6-terra":{"in":3.0,"out":12.0},"qwen2.5:14b":{"in":0,"out":0}}`)
	got := envModelRates("COST_MODEL_RATES")
	if len(got) != 2 {
		t.Fatalf("parsed %d entries, want 2: %v", len(got), got)
	}
	if r := got["gpt-5.6-terra"]; r.In != 3.0 || r.Out != 12.0 {
		t.Errorf("terra = %+v, want {3 12}", r)
	}
	if r, ok := got["qwen2.5:14b"]; !ok || r.In != 0 || r.Out != 0 {
		t.Errorf("qwen2.5:14b = %+v ok=%v, want explicit {0 0}", r, ok)
	}
}

func TestEnvModelRates_MalformedJSONIgnored(t *testing.T) {
	t.Setenv("COST_MODEL_RATES", `{"gpt-5.6-terra":{"in":3.0`)
	if got := envModelRates("COST_MODEL_RATES"); got != nil {
		t.Errorf("malformed JSON parsed to %v, want nil (ignored with warning)", got)
	}
}

func TestEnvModelRates_UnsetAndEmpty(t *testing.T) {
	t.Setenv("COST_MODEL_RATES", "")
	if got := envModelRates("COST_MODEL_RATES"); got != nil {
		t.Errorf("empty env parsed to %v, want nil", got)
	}
}

func TestEnvModelRates_NegativeRatesDropped(t *testing.T) {
	t.Setenv("COST_MODEL_RATES", `{"good":{"in":1,"out":2},"bad":{"in":-1,"out":2}}`)
	got := envModelRates("COST_MODEL_RATES")
	if _, ok := got["bad"]; ok {
		t.Error("negative-rate entry survived parsing")
	}
	if r := got["good"]; r.In != 1 || r.Out != 2 {
		t.Errorf("good = %+v, want {1 2}", r)
	}
}

func TestLoad_CostModelRatesWired(t *testing.T) {
	t.Setenv("COST_MODEL_RATES", `{"gpt-5.6-luna":{"in":1.0,"out":6.0}}`)
	cfg := Load()
	if r := cfg.Cost.ModelRates["gpt-5.6-luna"]; r.In != 1.0 || r.Out != 6.0 {
		t.Errorf("cfg.Cost.ModelRates[luna] = %+v, want {1 6}", r)
	}
}
