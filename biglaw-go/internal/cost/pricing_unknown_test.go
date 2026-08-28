// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for gpt-5.6 family pricing, COST_MODEL_RATES per-model overrides,
// and unknown-model (unpriced) flagging.

package cost

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"testing"
)

func approx(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// gpt-5.6-luna carries a verified built-in rate ($1.00 in / $6.00 out per 1M
// tokens) and the cache-aware multipliers (×1.25 write, ×0.10 read) apply.
func TestCalcCostUSD_GPT56Luna(t *testing.T) {
	got := CalcCostUSD("gpt-5.6-luna", 1_000_000, 1_000_000, 0, 0)
	if got == nil || !approx(*got, 7.00) {
		t.Fatalf("luna 1M/1M = %v, want 7.00", got)
	}
	// 1M each of input, output, cache write, cache read:
	// 1.00 + 6.00 + 1.00*1.25 + 1.00*0.10 = 8.35
	got = CalcCostUSD("gpt-5.6-luna", 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if got == nil || !approx(*got, 8.35) {
		t.Fatalf("luna cache-aware = %v, want 8.35", got)
	}
	// Version-drift IDs starting with the luna prefix hit the same rate.
	got = CalcCostUSD("gpt-5.6-luna-2026-01", 1_000_000, 1_000_000, 0, 0)
	if got == nil || !approx(*got, 7.00) {
		t.Fatalf("luna drift = %v, want 7.00", got)
	}
}

// gpt-5.6-terra / -sol are recognised names without invented prices: they
// resolve (rate {0,0}) but are NOT priced, so their entries get flagged.
func TestGPT56Siblings_RecognisedButUnpriced(t *testing.T) {
	for _, model := range []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-terra-2026-03"} {
		rate, priced, recognised := resolvePricing(model)
		if !recognised {
			t.Errorf("%s not recognised", model)
		}
		if priced {
			t.Errorf("%s priced=%v with rate %v, want unpriced", model, priced, rate)
		}
	}
}

// A COST_MODEL_RATES entry overrides the built-in table, and an explicit
// zero rate counts as priced (deliberately free), not unknown.
func TestSetModelRates_OverridesBuiltins(t *testing.T) {
	SetModelRates(map[string]ModelRate{
		"gpt-5.6-luna":  {In: 2.00, Out: 8.00},
		"gpt-5.6-terra": {In: 3.00, Out: 12.00},
		"qwen2.5:14b":   {In: 0, Out: 0},
	})
	t.Cleanup(func() { SetModelRates(nil) })

	// Override wins over the built-in luna rate.
	if got := CalcCostUSD("gpt-5.6-luna", 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, 10.00) {
		t.Errorf("overridden luna = %v, want 10.00", got)
	}
	// Terra is now priced.
	if _, priced, _ := resolvePricing("gpt-5.6-terra"); !priced {
		t.Error("terra should be priced after COST_MODEL_RATES override")
	}
	// Prefix match: a dated terra ID hits the terra override.
	if got := CalcCostUSD("gpt-5.6-terra-2026-03", 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, 15.00) {
		t.Errorf("terra drift = %v, want 15.00", got)
	}
	// Explicit zero: priced (free), not flagged unknown.
	rate, priced, recognised := resolvePricing("qwen2.5:14b")
	if !recognised || !priced || rate != [2]float64{} {
		t.Errorf("qwen2.5:14b = %v priced=%v recognised=%v, want {0 0} true true", rate, priced, recognised)
	}
	// Negative rates are dropped at SetModelRates.
	SetModelRates(map[string]ModelRate{"bad-model": {In: -1, Out: 5}})
	if _, _, recognised := resolvePricing("bad-model"); recognised {
		t.Error("negative-rate entry should have been dropped")
	}
}

// An unknown model records $0 but the entry is flagged PriceUnknown, and the
// summary counts it — never a silent zero. Known Claude models stay unflagged
// at their existing rates.
func TestRecord_UnknownModelFlaggedAndCounted(t *testing.T) {
	s := &Store{}
	usd := 6.0
	s.Record(RecordRequest{Model: "claude-haiku-4-5", InputTokens: 1_000_000, OutputTokens: 1_000_000, CostUSD: &usd, Context: ContextTask})
	s.Record(RecordRequest{Model: "gpt-5.6-terra", InputTokens: 500, OutputTokens: 100, Context: ContextTask})
	s.Record(RecordRequest{Model: "totally-unknown-llm-9", InputTokens: 500, OutputTokens: 100, Context: ContextTask})

	if e := s.entries[0]; e.PriceUnknown || e.CostUSD == nil || !approx(*e.CostUSD, 6.0) {
		t.Errorf("haiku entry = flag %v cost %v, want unflagged $6", e.PriceUnknown, e.CostUSD)
	}
	for _, i := range []int{1, 2} {
		e := s.entries[i]
		if !e.PriceUnknown {
			t.Errorf("entry %d (%s) not flagged PriceUnknown", i, e.Model)
		}
		if e.CostUSD == nil || *e.CostUSD != 0 {
			t.Errorf("entry %d cost = %v, want recorded $0", i, e.CostUSD)
		}
	}
	sum := s.Summarise(nil)
	if sum.UnpricedCalls != 2 {
		t.Errorf("UnpricedCalls = %d, want 2", sum.UnpricedCalls)
	}
	if !approx(sum.TotalUSD, 6.0) {
		t.Errorf("TotalUSD = %v, want 6.0", sum.TotalUSD)
	}
}

// countWarnHandler counts slog Warn records.
type countWarnHandler struct {
	mu    sync.Mutex
	count int
}

func (h *countWarnHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (h *countWarnHandler) Handle(_ context.Context, _ slog.Record) error {
	h.mu.Lock()
	h.count++
	h.mu.Unlock()
	return nil
}
func (h *countWarnHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countWarnHandler) WithGroup(_ string) slog.Handler      { return h }

// The unknown-model warning fires once per model per process, not per call.
func TestWarnUnpricedOnce_PerModel(t *testing.T) {
	h := &countWarnHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s := &Store{}
	for i := 0; i < 5; i++ {
		s.Record(RecordRequest{Model: "warn-once-model-a", Context: ContextTask})
	}
	s.Record(RecordRequest{Model: "warn-once-model-b", Context: ContextTask})

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count != 2 {
		t.Errorf("warn count = %d, want 2 (one per unknown model)", h.count)
	}
}

// Existing Anthropic behavior is unchanged: Haiku/Sonnet/Opus price at their
// built-in rates and are never flagged.
func TestBuiltinClaudeRatesUnchanged(t *testing.T) {
	cases := []struct {
		model string
		want  float64 // 1M in + 1M out
	}{
		{"claude-haiku-4-5", 6.00},
		{"claude-sonnet-4-6", 18.00},
		{"claude-opus-4-8", 90.00},
	}
	for _, c := range cases {
		got := CalcCostUSD(c.model, 1_000_000, 1_000_000, 0, 0)
		if got == nil || !approx(*got, c.want) {
			t.Errorf("%s = %v, want %v", c.model, got, c.want)
		}
		if _, priced, recognised := resolvePricing(c.model); !priced || !recognised {
			t.Errorf("%s should be priced and recognised", c.model)
		}
	}
}

// Local inference (watt-metered) is free by definition — even when the model
// name prefix-matches a hosted family's rate class (qwen2.5:7b → DashScope
// qwen rates billed a real local run $0.60).
func TestLocalWattMeteredCallsAreFree(t *testing.T) {
	s := &Store{}
	usd := 0.598
	wh := 12.5
	s.Record(RecordRequest{Model: "qwen2.5:7b", InputTokens: 1000, OutputTokens: 500,
		CostUSD: &usd, EstimatedWh: &wh, Context: ContextTask})
	sum := s.Summarise(nil)
	if sum.TotalUSD != 0 {
		t.Fatalf("local watt-metered call must record $0, got %v", sum.TotalUSD)
	}
	if sum.UnpricedCalls != 0 {
		t.Fatalf("local call is deliberately free, not unpriced; got %d", sum.UnpricedCalls)
	}
	// A hosted call on the same model name still prices normally (the caller
	// computes via CalcCostUSD and Record keeps it — no watt meter, no zeroing).
	hostedUSD := CalcCostUSD("qwen2.5:7b", 1_000_000, 0, 0, 0)
	if hostedUSD == nil || *hostedUSD == 0 {
		t.Fatal("hosted qwen should price by class")
	}
	s.Record(RecordRequest{Model: "qwen2.5:7b", InputTokens: 1_000_000, CostUSD: hostedUSD, Context: ContextTask})
	if s.Summarise(nil).TotalUSD == 0 {
		t.Fatal("hosted (non-watt-metered) qwen call must keep its computed cost")
	}
}
