// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// family_law_math: deterministic family-law arithmetic. LLM agents reason
// about the law; they must not do equalization or support arithmetic in
// prose. This tool is pure math over caller-supplied figures — it holds NO
// statutory table data and does NO rate lookups. The professional supplies
// the categorised line items, guideline table amounts, and incomes; the tool
// returns an auditable schedule where every input line is itemised, so the
// output can be checked figure-by-figure rather than trusted as one number.
//
// All money is carried as int64 cents; division rounds half away from zero.

package tools

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/agents"
	"github.com/discover-legal/biglaw-go/internal/providers"
)

func (r *Registry) registerFamilyMathTools() {
	r.Register(r.familyLawMathTool())
}

// ─── cents arithmetic ────────────────────────────────────────────────────────

// toCents converts a dollar figure to int64 cents, rounding half away from
// zero. A tiny epsilon absorbs binary-float representation error (2.675
// arrives as 2.67499999…) so that half-cent inputs round up as expected.
func toCents(dollars float64) int64 {
	scaled := dollars * 100
	return int64(math.Round(scaled + math.Copysign(1e-6, scaled)))
}

// halfCents divides cents by two, rounding half away from zero.
func halfCents(c int64) int64 {
	if c >= 0 {
		return (c + 1) / 2
	}
	return -((-c + 1) / 2)
}

// shareCents apportions total cents by num/den, rounding half away from zero.
func shareCents(total, num, den int64) int64 {
	v := float64(total) * float64(num) / float64(den)
	return int64(math.Round(v + math.Copysign(1e-6, v)))
}

// dollars renders cents as a float dollar amount for JSON output (exact to
// two decimal places by construction).
func dollars(c int64) float64 {
	return float64(c) / 100
}

// numInput reads a JSON number from the input map; ok is false when the key
// is absent or not a number.
func numInput(input map[string]interface{}, key string) (float64, bool) {
	switch v := input[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

// listInput reads an array of objects from the input map.
func listInput(input map[string]interface{}, key string) ([]map[string]interface{}, error) {
	raw, ok := input[key].([]interface{})
	if !ok {
		return nil, fmt.Errorf("%s is required and must be an array", key)
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object", key, i)
		}
		out = append(out, m)
	}
	return out, nil
}

// ─── family_law_math ─────────────────────────────────────────────────────────

func (r *Registry) familyLawMathTool() *ToolImpl {
	fail := func(msg string) map[string]interface{} {
		return map[string]interface{}{"ok": false, "error": msg}
	}
	return &ToolImpl{
		Name: "family_law_math",
		Schema: providers.ToolParam{
			Name:        "family_law_math",
			Description: "Deterministic family-law arithmetic — NEVER do this math yourself in prose. Operations: equalization (categorised asset/debt/exclusion/marriage-date-deduction line items per spouse → Net Family Property schedule, NFP difference, and equalization payment with payer identified), support_setoff (two caller-computed guideline table amounts → set-off amount and payer), s7_shares (each parent's guideline income + special-expense line items with reimbursements → proportionate percentages and dollar shares per expense), income_average (per-year incomes → simple and weighted-most-recent averages plus the delta vs the latest year). This tool does NOT contain statutory tables, guideline formulas, or rates; the caller supplies the table amounts, incomes, valuations, and categorisations; the output is an auditable schedule that itemises every input line. All amounts in dollars; results are rounded half-up to the cent.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"operation": map[string]interface{}{"type": "string", "enum": []string{"equalization", "support_setoff", "s7_shares", "income_average"}, "description": "Which calculation to run"},
					"items":     map[string]interface{}{"type": "array", "description": "equalization: line items, each {label, value (dollars, >= 0), owner (spouse identifier — exactly two distinct owners across all items), category: asset|debt|excluded|date_of_marriage_deduction}", "items": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"label": map[string]interface{}{"type": "string"}, "value": map[string]interface{}{"type": "number"}, "owner": map[string]interface{}{"type": "string"}, "category": map[string]interface{}{"type": "string", "enum": []string{"asset", "debt", "excluded", "date_of_marriage_deduction"}}}, "required": []string{"label", "value", "owner", "category"}}},
					"party_a":   map[string]interface{}{"type": "string", "description": "support_setoff / s7_shares: first party's name (default \"Party A\")"},
					"party_b":   map[string]interface{}{"type": "string", "description": "support_setoff / s7_shares: second party's name (default \"Party B\")"},
					"amount_a":  map[string]interface{}{"type": "number", "description": "support_setoff: party A's guideline table amount (caller-computed, dollars, >= 0)"},
					"amount_b":  map[string]interface{}{"type": "number", "description": "support_setoff: party B's guideline table amount (caller-computed, dollars, >= 0)"},
					"income_a":  map[string]interface{}{"type": "number", "description": "s7_shares: party A's guideline income (dollars, >= 0; combined must be > 0)"},
					"income_b":  map[string]interface{}{"type": "number", "description": "s7_shares: party B's guideline income (dollars, >= 0; combined must be > 0)"},
					"expenses":  map[string]interface{}{"type": "array", "description": "s7_shares: expense line items, each {label, gross_annual (dollars, >= 0), reimbursed (dollars, >= 0, <= gross_annual, default 0)}", "items": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"label": map[string]interface{}{"type": "string"}, "gross_annual": map[string]interface{}{"type": "number"}, "reimbursed": map[string]interface{}{"type": "number"}}, "required": []string{"label", "gross_annual"}}},
					"years":     map[string]interface{}{"type": "array", "description": "income_average: per-year incomes, each {year (e.g. 2025), income (dollars; losses may be negative)}; at least two years", "items": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"year": map[string]interface{}{"type": "number"}, "income": map[string]interface{}{"type": "number"}}, "required": []string{"year", "income"}}},
				},
				"required": []string{"operation"},
			},
		},
		Exec: func(input map[string]interface{}, _ agents.ToolContext) (interface{}, error) {
			op := strings.TrimSpace(strInput(input, "operation"))
			switch op {
			case "equalization":
				return familyMathEqualization(input, fail), nil
			case "support_setoff":
				return familyMathSetoff(input, fail), nil
			case "s7_shares":
				return familyMathS7(input, fail), nil
			case "income_average":
				return familyMathIncomeAverage(input, fail), nil
			default:
				return fail(fmt.Sprintf("unknown operation: %q (expected equalization, support_setoff, s7_shares, or income_average)", op)), nil
			}
		},
	}
}

// ─── equalization ────────────────────────────────────────────────────────────

type nfpSide struct {
	assets, debts, deductions, exclusions     []map[string]interface{}
	assetsC, debtsC, deductionsC, exclusionsC int64
}

func familyMathEqualization(input map[string]interface{}, fail func(string) map[string]interface{}) map[string]interface{} {
	items, err := listInput(input, "items")
	if err != nil {
		return fail(err.Error())
	}
	if len(items) == 0 {
		return fail("items must contain at least one line item")
	}
	sides := map[string]*nfpSide{}
	owners := []string{} // insertion order
	for i, item := range items {
		label, _ := item["label"].(string)
		owner, _ := item["owner"].(string)
		category, _ := item["category"].(string)
		value, okV := numInput(item, "value")
		if strings.TrimSpace(label) == "" {
			return fail(fmt.Sprintf("items[%d]: label is required", i))
		}
		if strings.TrimSpace(owner) == "" {
			return fail(fmt.Sprintf("items[%d] (%s): owner is required", i, label))
		}
		if !okV {
			return fail(fmt.Sprintf("items[%d] (%s): value is required and must be a number", i, label))
		}
		if value < 0 {
			return fail(fmt.Sprintf("items[%d] (%s): value must not be negative — supply debts as positive amounts under category \"debt\"", i, label))
		}
		side, ok := sides[owner]
		if !ok {
			side = &nfpSide{}
			sides[owner] = side
			owners = append(owners, owner)
		}
		c := toCents(value)
		line := map[string]interface{}{"label": label, "value": dollars(c)}
		switch category {
		case "asset":
			side.assets = append(side.assets, line)
			side.assetsC += c
		case "debt":
			side.debts = append(side.debts, line)
			side.debtsC += c
		case "date_of_marriage_deduction":
			side.deductions = append(side.deductions, line)
			side.deductionsC += c
		case "excluded":
			side.exclusions = append(side.exclusions, line)
			side.exclusionsC += c
		default:
			return fail(fmt.Sprintf("items[%d] (%s): unknown category %q (expected asset, debt, excluded, or date_of_marriage_deduction)", i, label, category))
		}
	}
	if len(owners) != 2 {
		return fail(fmt.Sprintf("items must name exactly two distinct owners (got %d)", len(owners)))
	}

	nfpC := map[string]int64{}
	schedules := make([]map[string]interface{}, 0, 2)
	for _, owner := range owners {
		s := sides[owner]
		rawC := s.assetsC - s.debtsC - s.deductionsC
		nfp := rawC
		note := ""
		if rawC < 0 {
			nfp = 0
			note = "raw NFP is negative; treated as zero for the equalization difference (itemised raw figure shown)"
		}
		nfpC[owner] = nfp
		schedules = append(schedules, map[string]interface{}{
			"owner":                         owner,
			"assets":                        s.assets,
			"assetsTotal":                   dollars(s.assetsC),
			"debts":                         s.debts,
			"debtsTotal":                    dollars(s.debtsC),
			"dateOfMarriageDeductions":      s.deductions,
			"dateOfMarriageDeductionsTotal": dollars(s.deductionsC),
			"exclusions":                    s.exclusions,
			"exclusionsTotal":               dollars(s.exclusionsC),
			"netFamilyPropertyRaw":          dollars(rawC),
			"netFamilyProperty":             dollars(nfp),
			"note":                          note,
		})
	}

	a, b := owners[0], owners[1]
	payer, payee := a, b
	if nfpC[b] > nfpC[a] {
		payer, payee = b, a
	}
	diffC := nfpC[payer] - nfpC[payee]
	paymentC := halfCents(diffC)
	return map[string]interface{}{
		"ok":                  true,
		"operation":           "equalization",
		"schedules":           schedules,
		"nfpDifference":       dollars(diffC),
		"equalizationPayment": dollars(paymentC),
		"payer":               payer,
		"payee":               payee,
		"summary":             fmt.Sprintf("%s pays %s an equalization payment of $%.2f (half the NFP difference of $%.2f).", payer, payee, dollars(paymentC), dollars(diffC)),
	}
}

// ─── support_setoff ──────────────────────────────────────────────────────────

func familyMathSetoff(input map[string]interface{}, fail func(string) map[string]interface{}) map[string]interface{} {
	partyA := strInput(input, "party_a")
	partyB := strInput(input, "party_b")
	if partyA == "" {
		partyA = "Party A"
	}
	if partyB == "" {
		partyB = "Party B"
	}
	amountA, okA := numInput(input, "amount_a")
	amountB, okB := numInput(input, "amount_b")
	if !okA || !okB {
		return fail("amount_a and amount_b are required and must be numbers (each spouse's caller-computed guideline table amount)")
	}
	if amountA < 0 || amountB < 0 {
		return fail("table amounts must not be negative")
	}
	aC, bC := toCents(amountA), toCents(amountB)
	payer, payee, setoffC := partyA, partyB, aC-bC
	if bC > aC {
		payer, payee, setoffC = partyB, partyA, bC-aC
	}
	out := map[string]interface{}{
		"ok":        true,
		"operation": "support_setoff",
		"tableAmounts": []map[string]interface{}{
			{"party": partyA, "amount": dollars(aC)},
			{"party": partyB, "amount": dollars(bC)},
		},
		"setoff": dollars(setoffC),
	}
	if setoffC == 0 {
		out["payer"] = ""
		out["payee"] = ""
		out["summary"] = "Table amounts are equal — the set-off is $0.00 and no payment flows either way."
		return out
	}
	out["payer"] = payer
	out["payee"] = payee
	out["summary"] = fmt.Sprintf("%s pays %s the set-off amount of $%.2f (difference of the two table amounts).", payer, payee, dollars(setoffC))
	return out
}

// ─── s7_shares ───────────────────────────────────────────────────────────────

func familyMathS7(input map[string]interface{}, fail func(string) map[string]interface{}) map[string]interface{} {
	partyA := strInput(input, "party_a")
	partyB := strInput(input, "party_b")
	if partyA == "" {
		partyA = "Party A"
	}
	if partyB == "" {
		partyB = "Party B"
	}
	incomeA, okA := numInput(input, "income_a")
	incomeB, okB := numInput(input, "income_b")
	if !okA || !okB {
		return fail("income_a and income_b are required and must be numbers (each parent's guideline income)")
	}
	if incomeA < 0 || incomeB < 0 {
		return fail("guideline incomes must not be negative")
	}
	incAC, incBC := toCents(incomeA), toCents(incomeB)
	combinedC := incAC + incBC
	if combinedC <= 0 {
		return fail("combined guideline income must be greater than zero")
	}
	expenses, err := listInput(input, "expenses")
	if err != nil {
		return fail(err.Error())
	}
	if len(expenses) == 0 {
		return fail("expenses must contain at least one line item")
	}

	pctA := float64(incAC) / float64(combinedC) * 100
	pctB := float64(incBC) / float64(combinedC) * 100
	var grossTotalC, reimbTotalC, netTotalC, shareATotalC, shareBTotalC int64
	lines := make([]map[string]interface{}, 0, len(expenses))
	for i, e := range expenses {
		label, _ := e["label"].(string)
		gross, okG := numInput(e, "gross_annual")
		if strings.TrimSpace(label) == "" {
			return fail(fmt.Sprintf("expenses[%d]: label is required", i))
		}
		if !okG {
			return fail(fmt.Sprintf("expenses[%d] (%s): gross_annual is required and must be a number", i, label))
		}
		if gross < 0 {
			return fail(fmt.Sprintf("expenses[%d] (%s): gross_annual must not be negative", i, label))
		}
		reimb, _ := numInput(e, "reimbursed")
		if reimb < 0 {
			return fail(fmt.Sprintf("expenses[%d] (%s): reimbursed must not be negative", i, label))
		}
		grossC, reimbC := toCents(gross), toCents(reimb)
		if reimbC > grossC {
			return fail(fmt.Sprintf("expenses[%d] (%s): reimbursed exceeds gross_annual", i, label))
		}
		netC := grossC - reimbC
		shareAC := shareCents(netC, incAC, combinedC)
		shareBC := netC - shareAC // remainder to B so each line reconciles exactly
		grossTotalC += grossC
		reimbTotalC += reimbC
		netTotalC += netC
		shareATotalC += shareAC
		shareBTotalC += shareBC
		lines = append(lines, map[string]interface{}{
			"label":       label,
			"grossAnnual": dollars(grossC),
			"reimbursed":  dollars(reimbC),
			"net":         dollars(netC),
			"shareA":      dollars(shareAC),
			"shareB":      dollars(shareBC),
		})
	}
	return map[string]interface{}{
		"ok":        true,
		"operation": "s7_shares",
		"parties": []map[string]interface{}{
			{"party": partyA, "guidelineIncome": dollars(incAC), "proportionPercent": math.Round(pctA*100) / 100},
			{"party": partyB, "guidelineIncome": dollars(incBC), "proportionPercent": math.Round(pctB*100) / 100},
		},
		"combinedIncome": dollars(combinedC),
		"expenses":       lines,
		"totals": map[string]interface{}{
			"grossAnnual": dollars(grossTotalC),
			"reimbursed":  dollars(reimbTotalC),
			"net":         dollars(netTotalC),
			"shareA":      dollars(shareATotalC),
			"shareB":      dollars(shareBTotalC),
		},
		"summary": fmt.Sprintf("Net special expenses of $%.2f shared %s %.2f%% ($%.2f) / %s %.2f%% ($%.2f).", dollars(netTotalC), partyA, pctA, dollars(shareATotalC), partyB, pctB, dollars(shareBTotalC)),
	}
}

// ─── income_average ──────────────────────────────────────────────────────────

func familyMathIncomeAverage(input map[string]interface{}, fail func(string) map[string]interface{}) map[string]interface{} {
	years, err := listInput(input, "years")
	if err != nil {
		return fail(err.Error())
	}
	if len(years) < 2 {
		return fail("years must contain at least two entries to average")
	}
	type yearIncome struct {
		year    int
		incomeC int64
	}
	rows := make([]yearIncome, 0, len(years))
	seen := map[int]bool{}
	for i, y := range years {
		yr, okY := numInput(y, "year")
		income, okI := numInput(y, "income")
		if !okY || !okI {
			return fail(fmt.Sprintf("years[%d]: year and income are required and must be numbers", i))
		}
		yi := int(yr)
		if seen[yi] {
			return fail(fmt.Sprintf("years[%d]: duplicate year %d", i, yi))
		}
		seen[yi] = true
		rows = append(rows, yearIncome{year: yi, incomeC: toCents(income)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].year < rows[j].year })

	var sumC, weightedSumC, weightSum int64
	lines := make([]map[string]interface{}, 0, len(rows))
	for i, row := range rows {
		weight := int64(i + 1) // oldest 1 … most recent n
		sumC += row.incomeC
		weightedSumC += row.incomeC * weight
		weightSum += weight
		lines = append(lines, map[string]interface{}{"year": row.year, "income": dollars(row.incomeC), "weight": weight})
	}
	n := int64(len(rows))
	simpleC := shareCents(sumC, 1, n)
	weightedC := shareCents(weightedSumC, 1, weightSum)
	latest := rows[len(rows)-1]
	return map[string]interface{}{
		"ok":                    true,
		"operation":             "income_average",
		"years":                 lines,
		"simpleAverage":         dollars(simpleC),
		"weightedMostRecent":    dollars(weightedC),
		"weightingNote":         "weighted average uses ascending integer weights (oldest ×1 … most recent ×n)",
		"latestYear":            latest.year,
		"latestIncome":          dollars(latest.incomeC),
		"deltaLatestVsSimple":   dollars(latest.incomeC - simpleC),
		"deltaLatestVsWeighted": dollars(latest.incomeC - weightedC),
		"summary":               fmt.Sprintf("Simple average $%.2f; weighted-most-recent $%.2f; latest year (%d) $%.2f — an averaging proposal conceals $%.2f vs the latest year.", dollars(simpleC), dollars(weightedC), latest.year, dollars(latest.incomeC), dollars(latest.incomeC-simpleC)),
	}
}
