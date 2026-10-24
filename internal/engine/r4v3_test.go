// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r3 verifier repros (critique/biglaw-lab-r4-r3-verifier.md): each failed at 4b5f776 and passes since lab-r4 r4.

func mv3(v, c string) analysis.Value { return analysis.Value{Value: v, Class: c} }

// H1: two adjacent figures whose currency sits between them ("1,000 USD 2,000 USD") overlap, and
// ranges() slices line[a.end:b.start] with a.end > b.start: a panic.
func TestR4V3AdjacentCurrencyPanics(t *testing.T) {
	for _, s := range []string{"Fees 1,000 USD 2,000 USD", "Rent 5 $ 10", "Q1 1.000 EUR 2.000 EUR", "100 € 200 €", "fee 50 GBP 75 GBP total"} {
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("Literal(%q) panicked: %v", s, p)
				}
			}()
			analysis.Literal(s)
		}()
	}
}

// H1 end to end: one verified quote from a fee table fails the whole task.
func TestR4V3AdjacentCurrencyFailsTheTask(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Fees\nAnnual fees 1,000 USD 2,000 USD per seat.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("The annual fee is 1,000 USD.", d1, "Annual fees 1,000 USD 2,000 USD per seat", val("Customer", "annual fee", "1,000 USD", "Money")))},
	}), Config{})
	if v.Status == "failed" || v.Error != nil {
		t.Errorf("the task %s: %v", stop(v), deref(v.Error))
	}
}

// H3: the parser admits a decimal-comma value ("$3,5 million", "1,5%") as 3.5 million / 1.5%, but
// the analysis package reads the same verified value with its own parser (analysis.Number: commas
// stripped) as 35 million / 15%: Calc's worksheets (which G8 treats as traced) and the join's canon
// are tenfold.
func TestR4V3DecimalCommaValueReadTenfoldDownstream(t *testing.T) {
	cases := []struct {
		v analysis.Value
		q string
	}{
		{mv3("$3,5 million", "Money"), "The Purchaser shall pay USD 3,5 million on Closing."},
		{analysis.Value{Value: "3,5 million", Class: "Money", Unit: "EUR"}, "The Purchaser shall pay EUR 3,5 million on Closing."},
		{mv3("1,5%", "Percentage"), "Interest accrues at 1,5 % per annum."},
	}
	for _, c := range cases {
		if !valueInQuote(c.v, c.q) {
			continue // not admitted: nothing downstream reads it
		}
		_, canon, _, _ := analysis.Canon(c.v)
		if strings.HasSuffix(canon, ":35000000") || canon == "pct:15" {
			t.Errorf("%q (admitted from %q as 3.5 million / 1.5%%) has canon %s", c.v.Value, c.q, canon)
		}
	}
	fs := []analysis.Finding{
		{FID: "F1", Verified: true, Values: []analysis.Value{mv3("1,5%", "Percentage")}},
		{FID: "F2", Verified: true, Values: []analysis.Value{mv3("$1,000,000", "Money")}},
	}
	r1, r2 := "F1.v1", "F2.v1"
	w, err := analysis.Calc(fs, "product", []analysis.Operand{{Ref: &r2}, {Ref: &r1}}, nil)
	if err == nil && w.Result != "15000" {
		t.Errorf("1,5%% of $1,000,000 computes to %s %s (worksheet %s), not 15000", w.Result, w.Unit, w.WID)
	}
}

// H3 end to end: the admitted value reaches the ledger and the lead's calculator reads it tenfold
// (product of the verified rate and a literal principal, as the lint's own hint suggests).
func TestR4V3DecimalCommaReachesCalc(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "1. Interest\nInterest accrues at 1,5 % per annum on the Loan.\n")
	d1 := f.docs[0].ID
	v := f.run(f.with(map[string][]model.Reply{
		"extract_facts": {facts(fd("Interest accrues at 1,5% per annum on the Loan.", d1, "Interest accrues at 1,5 % per annum on the Loan",
			val("Borrower", "interest rate", "1,5%", "Percentage")))},
	}), Config{})
	an, err := f.e.Findings(context.Background(), v.ID)
	if err != nil || len(an) != 1 || len(an[0].Values) != 1 {
		t.Fatalf("%v %+v", err, an)
	}
	r1, lit, usd := "F1.v1", "1000000", "USD"
	w, err := analysis.Calc(an, "product", []analysis.Operand{{Literal: &lit, Unit: &usd}, {Ref: &r1}}, nil)
	if err != nil || w.Result != "15000" {
		t.Errorf("annual interest on $1,000,000 at the verified 1,5 %%: %s %s (%v), want 15000", w.Result, w.Unit, err)
	}
}

// M7: dropped values are not visible where the commit says: Cover.Unadmitted has no reader (not the
// cover tool's output, not finish, not the views), the sweep discards admit's notes, and the
// committee re-adds one LOW "admission" line per note every cycle, ahead of the reproduce and
// audit lines, so enough notes push a HIGH audit gap off the committee's page.
func TestR4V3AdmissionNotesFloodCommittee(t *testing.T) {
	var found []Critic
	for i := range 80 {
		found = append(found, Critic{ID: fmt.Sprintf("C%d", i+1), Critic: "admission", Severity: "LOW", FID: fmt.Sprintf("F%d", i+1),
			Text: fmt.Sprintf("value %q (purchase price) not admitted: the quote's \"1200000\" reads two ways (grouping or a decimal comma, or two currencies)", "$1,200,000")})
	}
	found = append(found, Critic{ID: "C81", Critic: "audit_gaps", Severity: "HIGH", Text: "Limitation period not addressed"})
	page := criticPage(1, found)
	list := page["findings"].([]map[string]any)
	if !slices.ContainsFunc(list, func(x map[string]any) bool { return x["severity"] == "HIGH" }) {
		t.Errorf("the HIGH audit gap is off the committee's page (%d of %d shown, truncated %v) behind LOW admission lines", len(list), len(found), page["truncated"])
	}
}

// FuzzFigures (the r3 verifier's, kept; it found H1 within seconds): no figure check panics on any
// text, the figure reader's stand-in included, and a text traces itself (G8 never flags a verbatim copy).
func FuzzFigures(f *testing.F) {
	for _, s := range []string{"Fees 1,000 USD 2,000 USD", "Rent 5 $ 10", "Q1 1.000 EUR 2.000 EUR", "100 € 200 €", "fee 50 GBP 75 GBP total",
		"Interest accrues at .5% per month", "a break fee of $.5 million", "The fee is CAD$1,200,000.", "Net loss: $(3,000)", "Change\t(12%)",
		"The fee is EUR 3,5 million and the cap is EUR 1,200,000.", "₹1,00,000 and INR 12,50,000", "Rent is $2000 per month.", "Shares outstanding: 3,000,000",
		"CHF 1'200'000", "$3 000 000", "three million dollars", "Revenue (in thousands): $4,000", "5 Mrd. EUR", "1.5.2024", "-$3,000", "between $3 and $5 million"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = Untraced(s, s, true)
		v := analysis.Value{Value: s, Class: "Money"}
		_, _, _, _ = valueStated(v, 0, oracleReadings(s, []string{s}, []analysis.Value{v}), s)
		_ = claimSumsIn(s, s, nil)
		_ = passageQuery(s)
		if u := Untraced(s, s, false); len(u) > 0 {
			t.Fatalf("%q does not trace itself: %v", s, u)
		}
	})
}

// L10: clause-reference digits take no slot of a requirement's passage query, and a capitalised word
// after a sentence's full stop is no proper noun because the word before the stop is short.
func TestR4V3PassageQueryReferencesAndAbbreviations(t *testing.T) {
	q := passageQuery("Subject to Sections 7.3(a), 7.3(b), 8.1 and 12.4 of this Agreement, the Purchaser shall indemnify, defend and hold harmless the Seller, its Affiliates and their respective Representatives from and against any and all Losses arising out of or relating to any breach of the representations and warranties in Article 4, provided that the aggregate liability shall not exceed the Cap as defined in Schedule 2.")
	for _, w := range strings.Fields(q) {
		if strings.ContainsAny(w, "0123456789") {
			t.Errorf("a clause number %q took a slot: %q", w, q)
		}
	}
	for _, w := range []string{"breach", "defend", "relating"} {
		if !slices.Contains(strings.Fields(q), w) {
			t.Errorf("%q dropped: %q", w, q)
		}
	}
	if !abbrev("Dr") || !abbrev("T") || abbrev("Lee") || abbrev("Wu") {
		t.Errorf("abbrev: Dr %v T %v Lee %v Wu %v", abbrev("Dr"), abbrev("T"), abbrev("Lee"), abbrev("Wu"))
	}
	if q := passageQuery("Repairs are due to Lee. Payment shall follow within thirty days of any water leakage or mould found by the tenant."); !strings.Contains(q, "lee") {
		t.Errorf("a name before a full stop lost: %q", q)
	}
}
