// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Tests for the COST_<FAMILY>_IN/_OUT pricing env overrides.

package cost

import "testing"

// samplePricing returns a fresh copy of a representative pricing table so
// tests never mutate the package-level basePricing.
func samplePricing() map[string][2]float64 {
	return map[string][2]float64{
		"claude-haiku-4-5":          {1.00, 5.00},
		"claude-3-5-haiku-20241022": {1.00, 5.00},
		"claude-sonnet-4-6":         {3.00, 15.00},
		"claude-opus-4-8":           {15.00, 75.00},
		"gpt-5.5":                   {0, 0},
		"text-embedding-3-small":    {0, 0},
	}
}

// COST_GPT_IN/_OUT prices the OpenAI chat models; the "embed" alias prices the
// text-embedding-* IDs. Both default to 0 (untracked) until overridden.
func TestApplyPricingEnvOverrides_OpenAIFamilies(t *testing.T) {
	t.Setenv("COST_GPT_IN", "1.25")
	t.Setenv("COST_GPT_OUT", "10.00")
	t.Setenv("COST_EMBED_IN", "0.02")

	pricing := samplePricing()
	applyPricingEnvOverrides(pricing)

	if got := pricing["gpt-5.5"]; got != [2]float64{1.25, 10.00} {
		t.Errorf("gpt-5.5 = %v, want {1.25 10}", got)
	}
	// "embed" alias targets text-embedding-*; only the input rate was set.
	if got := pricing["text-embedding-3-small"]; got != [2]float64{0.02, 0} {
		t.Errorf("text-embedding-3-small = %v, want {0.02 0}", got)
	}
	// The "gpt" family must not bleed into Claude models.
	if got := pricing["claude-opus-4-8"]; got != [2]float64{15.00, 75.00} {
		t.Errorf("opus changed unexpectedly: %v", got)
	}
}

func TestApplyPricingEnvOverrides_Family(t *testing.T) {
	t.Setenv("COST_HAIKU_IN", "2.50")
	t.Setenv("COST_HAIKU_OUT", "12.50")

	pricing := samplePricing()
	applyPricingEnvOverrides(pricing)

	// Every model containing "haiku" gets the override.
	for _, model := range []string{"claude-haiku-4-5", "claude-3-5-haiku-20241022"} {
		if got := pricing[model]; got != [2]float64{2.50, 12.50} {
			t.Errorf("%s = %v, want {2.5 12.5}", model, got)
		}
	}
	// Other families untouched.
	if got := pricing["claude-sonnet-4-6"]; got != [2]float64{3.00, 15.00} {
		t.Errorf("sonnet changed unexpectedly: %v", got)
	}
	if got := pricing["claude-opus-4-8"]; got != [2]float64{15.00, 75.00} {
		t.Errorf("opus changed unexpectedly: %v", got)
	}
}

func TestApplyPricingEnvOverrides_PartialOverride(t *testing.T) {
	// Only the input rate is overridden; the output rate keeps its default.
	t.Setenv("COST_SONNET_IN", "6")

	pricing := samplePricing()
	applyPricingEnvOverrides(pricing)

	if got := pricing["claude-sonnet-4-6"]; got != [2]float64{6.00, 15.00} {
		t.Errorf("claude-sonnet-4-6 = %v, want {6 15}", got)
	}
}

func TestApplyPricingEnvOverrides_InvalidValuesIgnored(t *testing.T) {
	t.Setenv("COST_OPUS_IN", "not-a-number")
	t.Setenv("COST_OPUS_OUT", "-1")

	pricing := samplePricing()
	applyPricingEnvOverrides(pricing)

	if got := pricing["claude-opus-4-8"]; got != [2]float64{15.00, 75.00} {
		t.Errorf("invalid override applied: %v, want {15 75}", got)
	}
}

func TestApplyPricingEnvOverrides_NoEnvNoChange(t *testing.T) {
	// Ensure unrelated env state doesn't bleed in.
	t.Setenv("COST_HAIKU_IN", "")
	t.Setenv("COST_HAIKU_OUT", "")

	pricing := samplePricing()
	applyPricingEnvOverrides(pricing)

	want := samplePricing()
	for model, p := range want {
		if pricing[model] != p {
			t.Errorf("%s = %v, want %v", model, pricing[model], p)
		}
	}
}

// New global families (deepseek, gemini, grok, qwen, …) are overridable by the
// same COST_<FAMILY>_IN/_OUT mechanism, on every tier of the family at once.
func TestApplyPricingEnvOverrides_GlobalFamilies(t *testing.T) {
	t.Setenv("COST_DEEPSEEK_IN", "0.30")
	t.Setenv("COST_DEEPSEEK_OUT", "1.20")

	pricing := map[string][2]float64{
		"deepseek-chat":     {0.27, 1.10},
		"deepseek-reasoner": {0.55, 2.19},
		"gemini-2.5-flash":  {0.30, 2.50},
	}
	applyPricingEnvOverrides(pricing)

	for _, m := range []string{"deepseek-chat", "deepseek-reasoner"} {
		if got := pricing[m]; got != [2]float64{0.30, 1.20} {
			t.Errorf("%s = %v, want {0.3 1.2}", m, got)
		}
	}
	// A different family is left alone.
	if got := pricing["gemini-2.5-flash"]; got != [2]float64{0.30, 2.50} {
		t.Errorf("gemini changed unexpectedly: %v", got)
	}
}

// lookupPricing resolves exact IDs first, then a substring fallback for
// version-drift IDs, and reports unrecognised models as not-found.
func TestLookupPricing(t *testing.T) {
	cases := []struct {
		model string
		want  [2]float64
		ok    bool
	}{
		{"deepseek-chat", [2]float64{0.27, 1.10}, true},          // exact
		{"deepseek-v3.1", [2]float64{0.27, 1.10}, true},          // drift → base deepseek
		{"deepseek-reasoner-0528", [2]float64{0.55, 2.19}, true}, // drift → reasoner tier
		{"gemini-2.5-flash-preview-09", [2]float64{0.30, 2.50}, true},
		{"gemini-2.5-flash-lite-preview", [2]float64{0.075, 0.30}, true}, // lite before flash
		{"gpt-4o-mini", [2]float64{0.15, 0.60}, true},                    // exact
		{"claude-sonnet-4-6", [2]float64{3.00, 15.00}, true},
		{"qwen-turbo-latest", [2]float64{0.05, 0.20}, true},
		{"some-unknown-model", [2]float64{}, false},
	}
	for _, c := range cases {
		got, ok := lookupPricing(c.model)
		if ok != c.ok || got != c.want {
			t.Errorf("lookupPricing(%q) = %v, %v; want %v, %v", c.model, got, ok, c.want, c.ok)
		}
	}
}

func TestCalcCostUSD(t *testing.T) {
	// 1M input + 1M output at deepseek-chat rates → 0.27 + 1.10 = 1.37.
	got := CalcCostUSD("deepseek-chat", 1_000_000, 1_000_000, 0, 0)
	if got == nil || *got < 1.3699 || *got > 1.3701 {
		t.Errorf("CalcCostUSD(deepseek-chat) = %v, want ~1.37", got)
	}
	// Unrecognised model records no cost (nil), not a misleading $0.
	if got := CalcCostUSD("nonexistent-model-x", 1000, 1000, 0, 0); got != nil {
		t.Errorf("CalcCostUSD(unknown) = %v, want nil", got)
	}
}

func TestParsePriceEnv(t *testing.T) {
	t.Setenv("COST_TEST_RATE", " 4.25 ")
	if v, ok := parsePriceEnv("COST_TEST_RATE"); !ok || v != 4.25 {
		t.Errorf("parsePriceEnv = %v, %v; want 4.25, true", v, ok)
	}
	if _, ok := parsePriceEnv("COST_TEST_UNSET_RATE"); ok {
		t.Error("unset env var parsed as valid")
	}
}
