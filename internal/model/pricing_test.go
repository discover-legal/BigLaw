// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package model

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func approx(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func builtin(t *testing.T) *Prices {
	t.Helper()
	p, err := LoadPrices("")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cost(p *Prices, model string, in, out, cw, cr int) *float64 {
	r, _, _ := p.Lookup(model)
	if r == nil {
		return nil
	}
	c := r.Cost(Usage{InputTokens: in, OutputTokens: out, CacheWriteTokens: cw, CacheReadTokens: cr})
	return &c
}

// v1Base is v1's basePricing (cost/cost.go at origin/main 971785a). Every
// row must resolve to the same rate in the v2 table; v1's {0, 0}
// placeholders ("recognised, unpriced") resolve to a known nil rate.
var v1Base = map[string][2]float64{
	"claude-haiku-4-5-20251001": {1, 5}, "claude-haiku-4-5": {1, 5}, "claude-sonnet-4-6": {3, 15}, "claude-opus-4-8": {15, 75},
	"claude-opus-4-5": {15, 75}, "claude-3-5-haiku-20241022": {1, 5}, "claude-3-5-sonnet-20241022": {3, 15},
	"claude-3-haiku-20240307": {0.25, 1.25}, "claude-3-opus-20240229": {15, 75},
	"gpt-5.6-luna": {1, 6}, "gpt-5.6-terra": {}, "gpt-5.6-sol": {}, "gpt-5.5": {}, "gpt-5": {1.25, 10}, "gpt-5-mini": {0.25, 2},
	"gpt-5-nano": {0.05, 0.4}, "gpt-4.1": {2, 8}, "gpt-4.1-mini": {0.4, 1.6}, "gpt-4.1-nano": {0.1, 0.4}, "gpt-4o": {2.5, 10},
	"gpt-4o-mini": {0.15, 0.6}, "o3": {2, 8}, "o3-mini": {1.1, 4.4}, "o4-mini": {1.1, 4.4},
	"text-embedding-3-small": {0.02, 0}, "text-embedding-3-large": {0.13, 0},
	"gemini-2.5-pro": {1.25, 10}, "gemini-2.5-flash": {0.3, 2.5}, "gemini-2.5-flash-lite": {0.1, 0.4}, "gemini-2.0-flash": {0.1, 0.4},
	"gemini-2.0-flash-lite": {0.075, 0.3}, "gemini-1.5-pro": {1.25, 5}, "gemini-1.5-flash": {0.075, 0.3},
	"deepseek-chat": {0.27, 1.1}, "deepseek-reasoner": {0.55, 2.19},
	"grok-4": {3, 15}, "grok-3": {3, 15}, "grok-3-mini": {0.3, 0.5}, "grok-2": {2, 10},
	"qwen-max": {1.6, 6.4}, "qwen-plus": {0.4, 1.2}, "qwen-turbo": {0.05, 0.2}, "qwen-vl-max": {1.6, 6.4}, "qwen-vl-plus": {0.21, 0.63},
	"mistral-large-latest": {2, 6}, "mistral-large": {2, 6}, "mistral-medium": {0.4, 2}, "mistral-small-latest": {0.1, 0.3},
	"mistral-small": {0.1, 0.3}, "open-mistral-nemo": {0.15, 0.15},
	"command-a": {2.5, 10}, "command-r-plus": {2.5, 10}, "command-r": {0.15, 0.6},
	"nova-pro": {0.8, 3.2}, "nova-lite": {0.06, 0.24}, "nova-micro": {0.035, 0.14},
	"kimi-k2": {}, "moonshot-v1-128k": {}, "glm-4.6": {}, "glm-4.5": {},
}

func TestBuiltinPricesParse(t *testing.T) {
	if len(builtinPrices) == 0 {
		t.Fatal("empty built-in table")
	}
	p := builtin(t)
	for model, want := range v1Base {
		r, known, op := p.Lookup(model)
		switch {
		case !known || op:
			t.Errorf("%s: known=%v operator=%v", model, known, op)
		case want == [2]float64{} && r != nil:
			t.Errorf("%s: v1 left it unpriced, v2 prices it %v", model, *r)
		case want != [2]float64{} && (r == nil || !approx(r.In, want[0]) || !approx(r.Out, want[1])):
			t.Errorf("%s: %v, want %v", model, r, want)
		}
	}
}

// Ported from v1 cost_test.go TestLookupPricing: exact ids first, then the
// longest prefix for version drift; unknown is not found. One vector moves:
// v1 priced "gemini-2.5-flash-lite-preview" at its flash-lite *class* rate
// (0.075/0.30), which contradicted v1's own exact gemini-2.5-flash-lite row
// (0.10/0.40); v2's prefix carries the exact row's rate.
func TestLookupPricing(t *testing.T) {
	p := builtin(t)
	for _, c := range []struct {
		model string
		want  Rate
		ok    bool
	}{
		{"deepseek-chat", Rate{0.27, 1.10}, true},
		{"deepseek-v3.1", Rate{0.27, 1.10}, true},
		{"deepseek-reasoner-0528", Rate{0.55, 2.19}, true},
		{"gemini-2.5-flash-preview-09", Rate{0.30, 2.50}, true},
		{"gemini-2.5-flash-lite-preview", Rate{0.10, 0.40}, true}, // lite before flash
		{"gpt-4o-mini", Rate{0.15, 0.60}, true},
		{"claude-sonnet-4-6", Rate{3.00, 15.00}, true},
		{"qwen-turbo-latest", Rate{0.05, 0.20}, true},
		{"anthropic/claude-haiku-4.5", Rate{1, 5}, true}, // OpenRouter-style vendor prefix
		{"some-unknown-model", Rate{}, false},
	} {
		r, known, _ := p.Lookup(c.model)
		if known != c.ok || c.ok && (r == nil || *r != c.want) {
			t.Errorf("Lookup(%q) = %v, %v; want %v, %v", c.model, r, known, c.want, c.ok)
		}
	}
	// A flagship without a list price is known but unpriced, not priced as
	// a cheaper sibling that shares its prefix.
	for _, m := range []string{"gpt-5.5", "gpt-5.7", "gpt-6"} {
		if r, known, _ := p.Lookup(m); !known || r != nil {
			t.Errorf("%s: %v %v, want known and unpriced", m, r, known)
		}
	}
}

// Ported from v1 TestCalcCostUSD.
func TestCalcCostUSD(t *testing.T) {
	p := builtin(t)
	if got := cost(p, "deepseek-chat", 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, 1.37) {
		t.Errorf("deepseek-chat = %v, want 1.37", got)
	}
	if got := cost(p, "nonexistent-model-x", 1000, 1000, 0, 0); got != nil {
		t.Errorf("unknown model = %v, want nil (unknown), not $0", *got)
	}
}

// Ported from v1 pricing_unknown_test.go.
func TestCalcCostUSD_GPT56Luna(t *testing.T) {
	p := builtin(t)
	for _, c := range []struct {
		model           string
		in, out, cw, cr int
		want            float64
	}{
		{"gpt-5.6-luna", 1_000_000, 1_000_000, 0, 0, 7.00},
		{"gpt-5.6-luna", 1_000_000, 1_000_000, 1_000_000, 1_000_000, 8.35}, // 1 + 6 + 1×1.25 + 1×0.10
		{"gpt-5.6-luna-2026-01", 1_000_000, 1_000_000, 0, 0, 7.00},
	} {
		if got := cost(p, c.model, c.in, c.out, c.cw, c.cr); got == nil || !approx(*got, c.want) {
			t.Errorf("%s %+v = %v, want %v", c.model, c, got, c.want)
		}
	}
}

func TestGPT56Siblings_RecognisedButUnpriced(t *testing.T) {
	p := builtin(t)
	for _, m := range []string{"gpt-5.6-terra", "gpt-5.6-sol", "gpt-5.6-terra-2026-03"} {
		if r, known, _ := p.Lookup(m); !known || r != nil || cost(p, m, 1, 1, 0, 0) != nil {
			t.Errorf("%s: rate %v known %v; want known, unpriced, cost unknown", m, r, known)
		}
	}
}

// v1 TestSetModelRates_OverridesBuiltins, now through BIGLAW_PRICES.
func TestSetModelRates_OverridesBuiltins(t *testing.T) {
	p, err := LoadPrices(`{"gpt-5.6-luna": {"in": 2, "out": 8}, "gpt-5.6-terra*": {"in": 3, "out": 12}, "qwen2.5:14b": {"in": 0, "out": 0}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cost(p, "gpt-5.6-luna", 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, 10) {
		t.Errorf("overridden luna = %v, want 10", got)
	}
	if got := cost(p, "gpt-5.6-terra-2026-03", 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, 15) {
		t.Errorf("terra drift = %v, want 15", got)
	}
	if r, known, op := p.Lookup("qwen2.5:14b"); r == nil || *r != (Rate{}) || !known || !op {
		t.Errorf("explicit zero = %v %v %v, want priced at $0 by the operator", r, known, op)
	}
	if _, err := LoadPrices(`{"bad-model": {"in": -1, "out": 5}}`); err == nil {
		t.Error("a negative rate was accepted (v1 dropped it silently; v2 refuses to start)")
	}
}

// v1 TestRecord_UnknownModelFlaggedAndCounted: known models keep their cost;
// an unpriced or unknown model's cost is nil (unknown), never a silent $0.
// The per-task summary that counts them moves to the budget ledger (PR 4a).
func TestRecord_UnknownModelFlaggedAndCounted(t *testing.T) {
	p := builtin(t)
	log := slog.New(slog.DiscardHandler)
	for model, want := range map[string]*float64{"claude-haiku-4-5": ptr(6.0), "gpt-5.6-terra": nil, "totally-unknown-llm-9": nil} {
		u := Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
		p.charge(model, &u, 0, log)
		if (u.CostUSD == nil) != (want == nil) || want != nil && !approx(*u.CostUSD, *want) || u.WattHours != nil {
			t.Errorf("%s: cost %v wh %v, want %v", model, u.CostUSD, u.WattHours, want)
		}
	}
}

func ptr(f float64) *float64 { return &f }

type countWarn struct {
	mu sync.Mutex
	n  map[string]int
}

func (h *countWarn) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (h *countWarn) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r.Attrs(func(a slog.Attr) bool { h.n[a.Value.String()]++; return true })
	return nil
}
func (h *countWarn) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countWarn) WithGroup(string) slog.Handler      { return h }

// v1 TestWarnUnpricedOnce_PerModel: one warning per unpriced model, not per call.
func TestWarnUnpricedOnce_PerModel(t *testing.T) {
	p := builtin(t)
	h := &countWarn{n: map[string]int{}}
	for range 5 {
		p.charge("warn-once-model-a", &Usage{}, 0, slog.New(h))
	}
	p.charge("warn-once-model-b", &Usage{}, 0, slog.New(h))
	p.charge("claude-haiku-4-5", &Usage{}, 0, slog.New(h))
	if h.n["warn-once-model-a"] != 1 || h.n["warn-once-model-b"] != 1 || len(h.n) != 2 {
		t.Errorf("warnings = %v, want one per unpriced model", h.n)
	}
}

func TestBuiltinClaudeRatesUnchanged(t *testing.T) {
	p := builtin(t)
	for m, want := range map[string]float64{"claude-haiku-4-5": 6, "claude-sonnet-4-6": 18, "claude-opus-4-8": 90} {
		if got := cost(p, m, 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, want) {
			t.Errorf("%s = %v, want %v", m, got, want)
		}
	}
}

// TestCallsPricedOnEveryHost (owner ruling, PR 2 r2; reverses v1 TestLocalWattMeteredCallsAreFree):
// the endpoint's host never makes a call free and open weights are no reason for $0 either. Every
// call is priced from the table; no row or a null row is unknown (nil), never $0. Only an operator
// entry of 0/0 makes a model free. Vendor-prefixed ids resolve to the model's own row (r2 R2-1).
func TestCallsPricedOnEveryHost(t *testing.T) {
	c, _ := endpoint(t, "", func(w http.ResponseWriter, r *http.Request) {
		reply(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000}}`)
	})
	if !strings.HasPrefix(c.endpoint, "http://127.0.0.1:") {
		t.Fatalf("the test endpoint %s is not on 127.0.0.1", c.endpoint)
	}
	for model, want := range map[string]*float64{ // USD for 1M in + 1M out
		"o3-pro": ptr(100), "openai/o3-pro": ptr(100), "o1": ptr(75), "o1-pro": ptr(750), "o1-mini": ptr(5.5),
		"o3-deep-research": ptr(50), "o4-mini-deep-research": ptr(10), "chatgpt-4o-latest": ptr(20),
		"qwen3-coder-plus": ptr(8), "qwen3-vl-plus": ptr(8), "qwen3-coder-flash": ptr(8), "qwen2.5-max": ptr(8),
		"qwen2.5:14b": ptr(8), "qwen3:30b": ptr(8), "qwen/qwen3-235b-a22b": ptr(8), "qwen/qwen-turbo": ptr(0.25),
		"claude-sonnet-4-6": ptr(18), "anthropic/claude-sonnet-4-6": ptr(18), "openai/gpt-4o": ptr(12.5),
		"mistral-nemo": ptr(8), "mistral-nemo:12b": ptr(8), "mistral": ptr(8), "mistral:7b": ptr(8), "magistral:24b": ptr(8),
		"mistral-small:24b": ptr(0.4), "mistral-small3.1:24b": ptr(0.4), "codestral:22b": ptr(8), "command-r:35b": ptr(0.75),
		"mistralai/mistral-small-3.1-24b-instruct": ptr(0.4), "mistralai/ministral-8b": ptr(0.4), "mistralai/mixtral-8x7b-instruct": ptr(8),
		"deepseek-v3": ptr(1.37), "deepseek-coder-v2:16b": ptr(1.37), "deepseek-r1:14b": ptr(2.74),
		"deepseek/deepseek-r1": ptr(2.74), "deepseek/deepseek-reasoner": ptr(2.74),
		"gpt-5.5": nil, "llama3.1:8b": nil, "gpt-oss:20b": nil, "kimi-latest": nil, "my-own-finetune": nil,
	} {
		req := userReq("hi")
		req.Model = model
		resp, err := c.Chat(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Usage.CostUSD; (got == nil) != (want == nil) || want != nil && !approx(*got, *want) {
			t.Errorf("%s on 127.0.0.1: cost %v, want %v", model, got, want)
		}
	}
	// The operator says a model is free: an explicit 0/0 row.
	c.prices, _ = LoadPrices(`{"qwen2.5:14b": {"in": 0, "out": 0}}`)
	req := userReq("hi")
	req.Model = "qwen2.5:14b"
	if resp, _ := c.Chat(context.Background(), req); resp.Usage.CostUSD == nil || *resp.Usage.CostUSD != 0 {
		t.Errorf("operator zero row: cost %v, want $0", resp.Usage.CostUSD)
	}
}

// TestWattHoursRecorded: with BIGLAW_LOCAL_WATTS set every call records watts × its duration in
// watt-hours, beside its price (watt-hours never zero the cost); unset (0) records none.
func TestWattHoursRecorded(t *testing.T) {
	c, _ := endpoint(t, "", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		reply(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`)
	})
	req := userReq("hi")
	req.Model = "qwen2.5:7b"
	resp, err := c.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	u := resp.Usage
	if u.CostUSD == nil || !approx(*u.CostUSD, 1.60) || u.WattHours == nil || u.Duration < 20*time.Millisecond ||
		!approx(*u.WattHours, 100*u.Duration.Hours()) {
		t.Fatalf("watts 100: cost %v, wh %v, duration %v; want the qwen* rate 1.60 and 100 W × duration", u.CostUSD, u.WattHours, u.Duration)
	}
	c.watts = 0
	if resp, _ = c.Chat(context.Background(), req); resp.Usage.WattHours != nil || resp.Usage.CostUSD == nil {
		t.Errorf("watts unset: wh %v cost %v, want no watt-hours and the price", resp.Usage.WattHours, resp.Usage.CostUSD)
	}
}

// TestPriceFileChecks: a FIFO (or any non-regular file) is refused at once instead of blocking startup,
// and a file over 1 MiB is refused with its own message.
func TestPriceFileChecks(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "prices.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs here:", err)
	}
	done := make(chan error, 1)
	go func() { _, err := LoadPrices(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("FIFO: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoadPrices blocked on a FIFO")
	}
	big := filepath.Join(dir, "big.json") // valid JSON, 1 MiB + 1 bytes
	os.WriteFile(big, append([]byte("{}"), bytes.Repeat([]byte(" "), 1<<20-1)...), 0o600)
	if _, err := LoadPrices(big); err == nil || !strings.Contains(err.Error(), "over 1 MiB") {
		t.Errorf("1 MiB + 1 file: %v", err)
	}
	os.Truncate(big, 1<<20) // exactly 1 MiB is fine
	if _, err := LoadPrices(big); err != nil {
		t.Errorf("1 MiB file: %v", err)
	}
}

// TestPricesJSONOverride: BIGLAW_PRICES replaces v1's COST_<FAMILY>_IN/_OUT
// env family (7 vectors). An entry overrides one model and leaves the rest;
// a prefix entry retargets a whole family; invalid input refuses to start;
// a file's contents are never echoed.
func TestPricesJSONOverride(t *testing.T) {
	base := builtin(t)
	p, err := LoadPrices(` {"deepseek-chat": {"in": 0.30, "out": 1.20}, "GLM*": {"in": 0.6, "out": 2.2}} `)
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]float64{"deepseek-chat": 1.50, "deepseek-reasoner": 2.74, "glm-4.6": 2.8, "claude-opus-4-8": 90} {
		if got := cost(p, model, 1_000_000, 1_000_000, 0, 0); got == nil || !approx(*got, want) {
			t.Errorf("%s = %v, want %v", model, got, want)
		}
	}
	// A family override (v1 TestApplyPricingEnvOverrides_GlobalFamilies)
	// beats the built-in exact rows of that family.
	p, _ = LoadPrices(`{"deepseek*": {"in": 0.30, "out": 1.20}}`)
	for _, m := range []string{"deepseek-chat", "deepseek-reasoner"} {
		if r, _, op := p.Lookup(m); r == nil || *r != (Rate{0.30, 1.20}) || !op {
			t.Errorf("%s = %v (operator %v)", m, r, op)
		}
	}
	if r, _, _ := p.Lookup("gemini-2.5-flash"); r == nil || *r != (Rate{0.30, 2.50}) {
		t.Errorf("another family changed: %v", r)
	}
	// An operator null un-prices a model.
	if p, _ = LoadPrices(`{"claude-haiku-4-5": null}`); cost(p, "claude-haiku-4-5", 1, 1, 0, 0) != nil {
		t.Error("an operator null did not un-price the model")
	}
	// Empty: the built-in table, unchanged (v1 NoEnvNoChange).
	if p, _ = LoadPrices("  "); p.operator != nil || len(p.builtin) != len(base.builtin) {
		t.Error("an empty value changed the table")
	}

	for name, v := range map[string]string{
		"not json":       `{"a": `,
		"array":          `[{"a": 1}]`,
		"partial":        `{"claude-sonnet-4-6": {"in": 6}}`,
		"string rate":    `{"m": {"in": "1", "out": 2}}`,
		"negative":       `{"m": {"in": -1, "out": 2}}`,
		"too large":      `{"m": {"in": 1e5, "out": 2}}`,
		"overflow":       `{"m": {"in": 1e400, "out": 2}}`,
		"open, v2 r1":    `{"m": {"open": true}}`,
		"unknown field":  `{"m": {"in": 1, "out": 2, "opne": true}}`,
		"empty object":   `{"m": {}}`,
		"number":         `{"m": 3}`,
		"duplicate":      `{"m": null, "M": null}`,
		"bad id":         `{"a b": null}`,
		"empty id":       `{"": null}`,
		"star inside":    `{"gpt*5": null}`,
		"trailing data":  `{"m": null} {}`,
		"no closing":     `{"m": null`,
		"nested garbage": `{"m": {"in": 1, "out": 2}, }`,
	} {
		if _, err := LoadPrices(v); err == nil {
			t.Errorf("%s: %s accepted", name, v)
		}
	}

	dir := t.TempDir()
	good, bad := filepath.Join(dir, "good.json"), filepath.Join(dir, "bad.json")
	os.WriteFile(good, []byte(`{"my-model": {"in": 1, "out": 1}}`), 0o600)
	os.WriteFile(bad, []byte("{\"x\": {\"in\": 1, \"out\": 2}, \"PASSWORD=hunter2 SECRET\": [\"sk-live-SECRET\"]}"), 0o600)
	if p, err = LoadPrices(good); err != nil || cost(p, "my-model", 1_000_000, 0, 0, 0) == nil {
		t.Errorf("a file path: %v", err)
	}
	badRate := filepath.Join(dir, "rate.json") // a valid id that could be a secret, with a bad value
	os.WriteFile(badRate, []byte(`{"sk-live-secret": {"in": -1, "out": 1}}`), 0o600)
	badID := filepath.Join(dir, "id.json") // a malformed id that is a secret
	os.WriteFile(badID, []byte(`{"PASSWORD=hunter2 SECRET": null}`), 0o600)
	for _, v := range []string{bad, badRate, badID, filepath.Join(dir, "SECRETPATH-missing.json"), dir} {
		_, err := LoadPrices(v)
		if msg := strings.ToLower(fmt.Sprint(err)); err == nil || strings.Contains(msg, "secret") || strings.Contains(msg, "hunter2") || strings.Contains(msg, strings.ToLower(dir)) {
			t.Errorf("LoadPrices(%s) = %v: want an error that names neither the file's contents nor its path", filepath.Base(v), err)
		}
	}
}
