// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Tests for family_law_math: a full equalization scenario (exclusions and
// marriage-date deductions, incl. a partially-consumed inheritance), the
// support set-off, s.7 proportionate shares with a partial reimbursement,
// income averaging, rounding edges, and invalid-input errors. Pure
// arithmetic — no network, no models, no config.

package tools

import (
	"math"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

func newFamilyMathRegistry(t *testing.T) *Registry {
	t.Helper()
	cfg := &config.Config{}
	return NewRegistry(cfg, providers.NewRegistry(cfg), nil, nil, nil)
}

func execFamilyMath(t *testing.T, reg *Registry, input map[string]interface{}) map[string]interface{} {
	t.Helper()
	res, err := reg.Execute("family_law_math", input, agents.ToolContext{})
	if err != nil {
		t.Fatalf("family_law_math returned a hard error: %v", err)
	}
	out, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("result is %T, want map", res)
	}
	return out
}

func wantFail(t *testing.T, out map[string]interface{}, fragment string) {
	t.Helper()
	if out["ok"] != false {
		t.Fatalf("expected ok=false, got %v", out)
	}
	msg, _ := out["error"].(string)
	if fragment != "" && !containsFold(msg, fragment) {
		t.Fatalf("error %q does not mention %q", msg, fragment)
	}
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 'a' - 'A'
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		j := 0
		for ; j < len(sub) && lower(s[i+j]) == lower(sub[j]); j++ {
		}
		if j == len(sub) {
			return i
		}
	}
	return -1
}

func wantMoney(t *testing.T, got interface{}, want float64, what string) {
	t.Helper()
	f, ok := got.(float64)
	if !ok {
		t.Fatalf("%s is %T (%v), want float64", what, got, got)
	}
	if math.Abs(f-want) > 0.005 {
		t.Fatalf("%s = %.2f, want %.2f", what, f, want)
	}
}

// ─── equalization ────────────────────────────────────────────────────────────

// Full scenario: a $150,000 inheritance where $110,000 was applied to the
// matrimonial home (loses exclusion — the caller categorises it inside the
// home's asset value) and only $40,000 remains excluded. Wife also carries a
// marriage-date deduction and debts; husband's side is simpler.
func TestFamilyMathEqualizationFullScenario(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "equalization",
		"items": []interface{}{
			// Wife
			map[string]interface{}{"label": "Matrimonial home (1/2 interest, incl. $110,000 inheritance applied)", "value": 450000.0, "owner": "Wife", "category": "asset"},
			map[string]interface{}{"label": "RRSP", "value": 80000.0, "owner": "Wife", "category": "asset"},
			map[string]interface{}{"label": "Remaining inheritance in segregated account", "value": 40000.0, "owner": "Wife", "category": "excluded"},
			map[string]interface{}{"label": "Line of credit", "value": 25000.0, "owner": "Wife", "category": "debt"},
			map[string]interface{}{"label": "Net worth at date of marriage", "value": 30000.0, "owner": "Wife", "category": "date_of_marriage_deduction"},
			// Husband
			map[string]interface{}{"label": "Matrimonial home (1/2 interest)", "value": 450000.0, "owner": "Husband", "category": "asset"},
			map[string]interface{}{"label": "Pension (actuarial value)", "value": 120000.0, "owner": "Husband", "category": "asset"},
			map[string]interface{}{"label": "Car loan", "value": 15000.0, "owner": "Husband", "category": "debt"},
			map[string]interface{}{"label": "Net worth at date of marriage", "value": 10000.0, "owner": "Husband", "category": "date_of_marriage_deduction"},
		},
	})
	if out["ok"] != true {
		t.Fatalf("equalization failed: %v", out["error"])
	}
	// Wife NFP: 530,000 − 25,000 − 30,000 = 475,000 (excluded $40,000 out).
	// Husband NFP: 570,000 − 15,000 − 10,000 = 545,000.
	// Difference 70,000 → payment 35,000, Husband pays.
	wantMoney(t, out["nfpDifference"], 70000, "nfpDifference")
	wantMoney(t, out["equalizationPayment"], 35000, "equalizationPayment")
	if out["payer"] != "Husband" || out["payee"] != "Wife" {
		t.Fatalf("payer/payee = %v/%v, want Husband/Wife", out["payer"], out["payee"])
	}
	schedules, ok := out["schedules"].([]map[string]interface{})
	if !ok || len(schedules) != 2 {
		t.Fatalf("schedules = %v", out["schedules"])
	}
	var wife map[string]interface{}
	for _, s := range schedules {
		if s["owner"] == "Wife" {
			wife = s
		}
	}
	if wife == nil {
		t.Fatal("no Wife schedule")
	}
	wantMoney(t, wife["assetsTotal"], 530000, "Wife assetsTotal")
	wantMoney(t, wife["debtsTotal"], 25000, "Wife debtsTotal")
	wantMoney(t, wife["dateOfMarriageDeductionsTotal"], 30000, "Wife deductionsTotal")
	wantMoney(t, wife["exclusionsTotal"], 40000, "Wife exclusionsTotal")
	wantMoney(t, wife["netFamilyProperty"], 475000, "Wife NFP")
	// Every line itemised: 2 assets, 1 debt, 1 deduction, 1 exclusion.
	if len(wife["assets"].([]map[string]interface{})) != 2 ||
		len(wife["debts"].([]map[string]interface{})) != 1 ||
		len(wife["dateOfMarriageDeductions"].([]map[string]interface{})) != 1 ||
		len(wife["exclusions"].([]map[string]interface{})) != 1 {
		t.Fatalf("Wife schedule not fully itemised: %v", wife)
	}
}

// Raw negative NFP is floored at zero for the difference, with the raw
// figure still itemised.
func TestFamilyMathEqualizationNegativeNFPFloor(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "equalization",
		"items": []interface{}{
			map[string]interface{}{"label": "Savings", "value": 10000.0, "owner": "A", "category": "asset"},
			map[string]interface{}{"label": "Debts", "value": 50000.0, "owner": "A", "category": "debt"},
			map[string]interface{}{"label": "Savings", "value": 100000.0, "owner": "B", "category": "asset"},
		},
	})
	if out["ok"] != true {
		t.Fatalf("equalization failed: %v", out["error"])
	}
	// A raw NFP −40,000 → 0; difference 100,000; payment 50,000 from B.
	wantMoney(t, out["nfpDifference"], 100000, "nfpDifference")
	wantMoney(t, out["equalizationPayment"], 50000, "equalizationPayment")
	if out["payer"] != "B" {
		t.Fatalf("payer = %v, want B", out["payer"])
	}
	for _, s := range out["schedules"].([]map[string]interface{}) {
		if s["owner"] == "A" {
			wantMoney(t, s["netFamilyPropertyRaw"], -40000, "A raw NFP")
			wantMoney(t, s["netFamilyProperty"], 0, "A floored NFP")
			if s["note"] == "" {
				t.Fatal("expected a note explaining the zero floor")
			}
		}
	}
}

// An odd-cent NFP difference halves with half-up rounding.
func TestFamilyMathEqualizationRoundingEdge(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "equalization",
		"items": []interface{}{
			map[string]interface{}{"label": "Account", "value": 100.03, "owner": "A", "category": "asset"},
			map[string]interface{}{"label": "Account", "value": 100.00, "owner": "B", "category": "asset"},
		},
	})
	// Difference $0.03 → half is 1.5 cents → rounds up to $0.02.
	wantMoney(t, out["nfpDifference"], 0.03, "nfpDifference")
	wantMoney(t, out["equalizationPayment"], 0.02, "equalizationPayment")
}

func TestFamilyMathEqualizationInvalidInput(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	item := func(owner, category string, value float64) map[string]interface{} {
		return map[string]interface{}{"label": "x", "value": value, "owner": owner, "category": category}
	}
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{"operation": "equalization"}), "items")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "equalization",
		"items":     []interface{}{item("A", "asset", -5.0), item("B", "asset", 1.0)},
	}), "negative")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "equalization",
		"items":     []interface{}{item("A", "asset", 5.0)},
	}), "two distinct owners")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "equalization",
		"items":     []interface{}{item("A", "chattel", 5.0), item("B", "asset", 1.0)},
	}), "unknown category")
}

// ─── support_setoff ──────────────────────────────────────────────────────────

func TestFamilyMathSupportSetoff(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "support_setoff",
		"party_a":   "Mother", "party_b": "Father",
		"amount_a": 1250.0, "amount_b": 780.0,
	})
	if out["ok"] != true {
		t.Fatalf("support_setoff failed: %v", out["error"])
	}
	wantMoney(t, out["setoff"], 470, "setoff")
	if out["payer"] != "Mother" || out["payee"] != "Father" {
		t.Fatalf("payer/payee = %v/%v, want Mother/Father", out["payer"], out["payee"])
	}

	// Equal amounts: zero set-off, no payer.
	equal := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "support_setoff", "amount_a": 500.0, "amount_b": 500.0,
	})
	wantMoney(t, equal["setoff"], 0, "setoff")
	if equal["payer"] != "" {
		t.Fatalf("payer = %v, want empty", equal["payer"])
	}

	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "support_setoff", "amount_a": 500.0,
	}), "amount_b")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "support_setoff", "amount_a": -1.0, "amount_b": 500.0,
	}), "negative")
}

// ─── s7_shares ───────────────────────────────────────────────────────────────

func TestFamilyMathS7SharesPartialReimbursement(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares",
		"party_a":   "Mother", "party_b": "Father",
		"income_a": 90000.0, "income_b": 60000.0,
		"expenses": []interface{}{
			map[string]interface{}{"label": "Daycare", "gross_annual": 12000.0, "reimbursed": 4000.0}, // childcare benefit
			map[string]interface{}{"label": "Orthodontics", "gross_annual": 5000.0},
		},
	})
	if out["ok"] != true {
		t.Fatalf("s7_shares failed: %v", out["error"])
	}
	wantMoney(t, out["combinedIncome"], 150000, "combinedIncome")
	parties := out["parties"].([]map[string]interface{})
	if got := parties[0]["proportionPercent"].(float64); math.Abs(got-60.0) > 0.005 {
		t.Fatalf("Mother proportion = %v, want 60.00", got)
	}
	lines := out["expenses"].([]map[string]interface{})
	// Daycare: net 8,000 → Mother 4,800 / Father 3,200.
	wantMoney(t, lines[0]["net"], 8000, "daycare net")
	wantMoney(t, lines[0]["shareA"], 4800, "daycare shareA")
	wantMoney(t, lines[0]["shareB"], 3200, "daycare shareB")
	// Orthodontics: net 5,000 → 3,000 / 2,000.
	wantMoney(t, lines[1]["shareA"], 3000, "ortho shareA")
	wantMoney(t, lines[1]["shareB"], 2000, "ortho shareB")
	totals := out["totals"].(map[string]interface{})
	wantMoney(t, totals["net"], 13000, "net total")
	wantMoney(t, totals["shareA"], 7800, "shareA total")
	wantMoney(t, totals["shareB"], 5200, "shareB total")
}

// Per-line shares reconcile exactly: shareB is the remainder after shareA
// rounds half-up, so shareA + shareB == net on every line.
func TestFamilyMathS7SharesRoundingReconciles(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares",
		"income_a":  1.0, "income_b": 2.0, // 1/3 vs 2/3
		"expenses": []interface{}{
			map[string]interface{}{"label": "Fee", "gross_annual": 100.00},
		},
	})
	line := out["expenses"].([]map[string]interface{})[0]
	// 100.00 × 1/3 = 33.333… → 33.33; remainder 66.67.
	wantMoney(t, line["shareA"], 33.33, "shareA")
	wantMoney(t, line["shareB"], 66.67, "shareB")
}

func TestFamilyMathS7SharesInvalidInput(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	expense := func(gross, reimb float64) []interface{} {
		return []interface{}{map[string]interface{}{"label": "x", "gross_annual": gross, "reimbursed": reimb}}
	}
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares", "income_a": 50000.0, "expenses": expense(100, 0),
	}), "income_b")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares", "income_a": -1.0, "income_b": 50000.0, "expenses": expense(100, 0),
	}), "negative")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares", "income_a": 0.0, "income_b": 0.0, "expenses": expense(100, 0),
	}), "combined")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares", "income_a": 50000.0, "income_b": 50000.0, "expenses": expense(100, 200),
	}), "exceeds")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "s7_shares", "income_a": 50000.0, "income_b": 50000.0, "expenses": []interface{}{},
	}), "at least one")
}

// ─── income_average ──────────────────────────────────────────────────────────

func TestFamilyMathIncomeAverage(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "income_average",
		"years": []interface{}{ // deliberately unsorted
			map[string]interface{}{"year": 2025.0, "income": 160000.0},
			map[string]interface{}{"year": 2023.0, "income": 100000.0},
			map[string]interface{}{"year": 2024.0, "income": 130000.0},
		},
	})
	if out["ok"] != true {
		t.Fatalf("income_average failed: %v", out["error"])
	}
	// Simple: 390,000/3 = 130,000. Weighted 1:2:3 = 840,000/6 = 140,000.
	wantMoney(t, out["simpleAverage"], 130000, "simpleAverage")
	wantMoney(t, out["weightedMostRecent"], 140000, "weightedMostRecent")
	if out["latestYear"] != 2025 {
		t.Fatalf("latestYear = %v, want 2025", out["latestYear"])
	}
	wantMoney(t, out["latestIncome"], 160000, "latestIncome")
	// The delta an averaging proposal conceals.
	wantMoney(t, out["deltaLatestVsSimple"], 30000, "deltaLatestVsSimple")
	wantMoney(t, out["deltaLatestVsWeighted"], 20000, "deltaLatestVsWeighted")
}

// Half-cent averages round half-up: (0.01 + 0.02)/2 = 1.5 cents → $0.02.
func TestFamilyMathIncomeAverageRoundingEdge(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	out := execFamilyMath(t, reg, map[string]interface{}{
		"operation": "income_average",
		"years": []interface{}{
			map[string]interface{}{"year": 2024.0, "income": 0.01},
			map[string]interface{}{"year": 2025.0, "income": 0.02},
		},
	})
	wantMoney(t, out["simpleAverage"], 0.02, "simpleAverage")
}

func TestFamilyMathIncomeAverageInvalidInput(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "income_average",
		"years":     []interface{}{map[string]interface{}{"year": 2025.0, "income": 100.0}},
	}), "at least two")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{
		"operation": "income_average",
		"years": []interface{}{
			map[string]interface{}{"year": 2025.0, "income": 100.0},
			map[string]interface{}{"year": 2025.0, "income": 200.0},
		},
	}), "duplicate")
}

// ─── operation dispatch ──────────────────────────────────────────────────────

func TestFamilyMathUnknownOperation(t *testing.T) {
	reg := newFamilyMathRegistry(t)
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{"operation": "child_support_table"}), "unknown operation")
	wantFail(t, execFamilyMath(t, reg, map[string]interface{}{}), "unknown operation")
}
