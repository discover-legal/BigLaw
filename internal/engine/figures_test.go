// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"slices"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// figureCorpus specifies the figure parser (figures.go, lab-r4 r3): each input and every figure it
// states, as "class:[-]amount [CUR] [(ambiguous)]" (figure.String). It holds the lab-r4 r2
// verifier's probes and the earlier rounds' cases (lab-r4 H1/M3, the r2 typed-value and G8 tests,
// the 4a currency test, the bench5 SEC sums, the deviation and locator-list grounds).
var figureCorpus = []struct {
	in   string
	want []string
}{
	{"Section 3 provides that the principal, in the amount of one million, is due.", []string{"unknown:3"}},
	{"The fee is $3 and the deposit is million.", []string{"money:3 USD"}},
	{"The Purchaser shall pay EUR 3,5 million on Closing.", []string{"money:3500000 EUR"}},
	{"Interest accrues at 1,5 % per annum.", []string{"percent:1.5"}},
	{"Damages are estimated at $3–5 million.", []string{"money:3000000 USD", "money:5000000 USD"}},
	{"Damages are estimated at between $3 and $5 million.", []string{"money:3000000 USD", "money:5000000 USD"}},
	{"Damages are estimated at $3 million to $5 million.", []string{"money:3000000 USD", "money:5000000 USD"}},
	{"Net income (loss) for the period: (3,000).", []string{"unknown:-3000"}},
	{"Net income for the period: $3,000.", []string{"money:3000 USD"}},
	{"-$3,000", []string{"money:-3000 USD"}},
	{"a fee of 3% of $1 million", []string{"percent:3", "money:1000000 USD"}},
	{"The Purchase Price is €3,000,000.", []string{"money:3000000 EUR"}},
	{"The Purchase Price is 3,000,000 €.", []string{"money:3000000 EUR"}},
	{"The Purchase Price is 3,000,000 GBP.", []string{"money:3000000 GBP"}},
	{"The Purchase Price is $3,000,000.", []string{"money:3000000 USD"}},
	{"The Purchase Price is USD 3 mn.", []string{"money:3000000 USD"}},
	{"The Purchase Price is USD 3mn.", []string{"money:3000000 USD"}},
	{"The Purchase Price is three million dollars.", []string{}},
	{"The Purchase Price is $3,000,000¹.", []string{"money:3000000 USD"}},
	{"The Purchase Price is $3\u00a0000\u00a0000.", []string{"money:3 USD", "unknown:0", "unknown:0"}},
	{"The gap between panels shall not exceed 5 mm in any direction.", []string{"count:5"}},
	{"steel plate of 12 mm and", []string{"count:12"}},
	{"Rent is $500 per month.", []string{"money:500 USD"}},
	{"Rent is 500 dollars per month.", []string{"money:500 USD"}},
	{"Rent is USD 500 per month.", []string{"money:500 USD"}},
	{"The fee is EUR 1.2 million.", []string{"money:1200000 EUR"}},
	{"pay 1,200,000 to the Seller", []string{"unknown:1200000"}},
	{"3,000,000 in cash", []string{"unknown:3000000"}},
	{"pay 1,200,000 Canadian dollars", []string{"money:1200000 CAD"}},
	{"The Buyer shall pay 1,200,000 US dollars at Closing.", []string{"money:1200000 USD"}},
	{"The Executive's salary is 100,000 annually.", []string{"unknown:100000"}},
	{"a fee of 50,000 CAD", []string{"money:50000 CAD"}},
	{"ten percent of the price", []string{}},
	{"Interest accrues at 5 per cent per annum.", []string{"percent:5"}},
	{"5 percent of the price", []string{"percent:5"}},
	{"1,200,000 USD", []string{"money:1200000 USD"}},
	{"The price is €1.200.000.", []string{"money:1200000 EUR"}},
	{"EUR 1.234,56", []string{"money:1234.56 EUR"}},
	{"€1.200", []string{"money:1.2 EUR (ambiguous)"}},
	{"an aggregate of $3 million", []string{"money:3000000 USD"}},
	{"2,500 Euro", []string{"money:2500 EUR"}},
	{"1,000 pounds", []string{"money:1000 GBP"}},
	{"1,000 pound", []string{"money:1000 GBP"}},
	{"The Company issued 3,000,000 shares.", []string{"count:3000000"}},
	{"2m shares", []string{"count:2"}},
	{"$2m", []string{"money:2000000 USD"}},
	{"$2.5mm", []string{"money:2500000 USD"}},
	{"$2,500K", []string{"money:2500000 USD"}},
	{"١٠٠ dollars", []string{}},
	{"$3,000,000.\u200b", []string{"money:3000000 USD"}},
	{"＄3,000,000", []string{"money:3000000 USD"}},
	{"The Seller shall transfer 1,200,000 of the Company's 3,000,000 Shares to the Buyer.", []string{"count:1200000", "count:3000000"}},
	{"37 of the 120 trades", []string{"count:37", "count:120"}},
	{"Rule 12b-1 fees of $4,000.", []string{"unknown:12", "unknown:1", "money:4000 USD"}},
	{"Rule 10b-5 applies.", []string{"unknown:10", "unknown:5"}},
	{"Principal: $3m.", []string{"money:3000000 USD"}},
	{"Principal: USD 3,000,000.", []string{"money:3000000 USD"}},
	{"Within 3 days the Company shall pay 3 instalments.", []string{"count:3", "count:3"}},
	{"The Company shall pay 3 million instalments.", []string{"count:3000000"}},
	{"Greystone claims damages of $644,910 for unpaid invoices", []string{"money:644910 USD"}},
	{"payments totaling $103,800.00", []string{"money:103800 USD"}},
	{"WCA's account took 73% of volume.", []string{"percent:73"}},
	{"WCA made 73 trades.", []string{"count:73"}},
	{"Forty Percent (40%) … Thirty-Five Percent (35%) … Serial #612847 … $92,600", []string{"percent:40", "percent:35", "unknown:612847", "money:92600 USD"}},
	{"the sum of Three Million Dollars ($3,000,000)", []string{"money:3000000 USD"}},
	{"thirty (30) days", []string{"count:30"}},
	{"Revenue\t(1,250)\t4,000", []string{"unknown:-1250", "unknown:4000"}},
	{"A fee of €1,200 and $5 per page, or 40 CHF, plus £30.", []string{"money:1200 EUR", "money:5 USD", "money:40 CHF", "money:30 GBP"}},
	{"$3,000 CAD", []string{"money:3000 CAD"}},
	{"Sections 3 and 14 million in fees", []string{"unknown:3", "unknown:14000000"}},
	{"clauses 3(a) and 3(b) or 3(c); 1,200 rows", []string{"unknown:3", "unknown:3", "unknown:3", "count:1200"}},
	{"Section 7.3.1 of the Manual", []string{}},
	{"The fee is EUR 3,5 million and the cap is 1,200.", []string{"money:3500000 EUR", "unknown:1200 (ambiguous)"}},
	{"pay $500 to 3 creditors", []string{"money:500 USD", "unknown:3"}},
	{"3-5 days", []string{"count:3", "count:5"}},
	{"from 3 to 5 per cent", []string{"percent:3", "percent:5"}},
	{"The fee is 3,5 Mio. EUR.", []string{"money:3500000 EUR"}},
	{"US$ 2,000 and C$1,500 and A$10", []string{"money:2000 USD", "money:1500 CAD", "money:10 AUD"}},
	{"rates of 1.5% to 2.5%", []string{"percent:1.5", "percent:2.5"}},
	{"a 25 basis points rise", []string{"count:25"}},
	{"500 full-time employees", []string{"count:500"}},
	{"1,000,000 Class A Shares", []string{"count:1000000"}},
	{"The cap is −€5,000.", []string{"money:-5000 EUR"}},
	{"($3,000,000)", []string{"money:-3000000 USD"}},
	{"1,200", []string{"unknown:1200"}},
	{"103,800.00", []string{"unknown:103800"}},
}

func TestFigureCorpus(t *testing.T) {
	for _, c := range figureCorpus {
		_, fs := figuresIn(c.in)
		got := []string{}
		for _, f := range fs {
			got = append(got, f.String())
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	if len(figureCorpus) < 80 {
		t.Errorf("the corpus shrank to %d cases", len(figureCorpus))
	}
}

// TestFigureAdmission is the admission rule over the corpus's quotes: a typed value verifies only
// from a figure of its class, currency, sign and amount; a typed sum also from a bare grouped or
// scaled figure with no count noun in a quote naming no other currency; never from an ambiguous one.
// claimSums reads the same rule.
func TestFigureAdmission(t *testing.T) {
	mv := func(v, c string) analysis.Value { return analysis.Value{Value: v, Class: c} }
	for _, c := range []struct {
		v    analysis.Value
		q    string
		want bool
	}{
		{mv("$3 million", "Money"), "Section 3 provides that the principal, in the amount of one million, is due.", false},
		{mv("$3 million", "Money"), "The fee is $3 and the deposit is million.", false},
		{mv("$35 million", "Money"), "The Purchaser shall pay EUR 3,5 million on Closing.", false},
		{mv("€35 million", "Money"), "The Purchaser shall pay EUR 3,5 million on Closing.", false},
		{mv("€3.5 million", "Money"), "The Purchaser shall pay EUR 3,5 million on Closing.", true},
		{mv("15%", "Percentage"), "Interest accrues at 1,5 % per annum.", false},
		{mv("1.5%", "Percentage"), "Interest accrues at 1,5 % per annum.", true},
		{mv("$3 million", "Money"), "Damages are estimated at $3–5 million.", true},
		{mv("$5 million", "Money"), "Damages are estimated at $3–5 million.", true},
		{mv("$3,000", "Money"), "Net income (loss): (3,000).", false},
		{mv("-$3,000", "Money"), "Net income (loss): (3,000).", true},
		{mv("-$3,000", "Money"), "Net income (loss): $3,000.", false},
		{mv("$30,000", "Money"), "a fee of 3% of $1 million", false},
		{mv("$3,000,000", "Money"), "The Purchase Price is €3,000,000.", false},
		{mv("$3,000,000", "Money"), "The Purchase Price is 3,000,000 €.", false},
		{mv("$3,000,000", "Money"), "The Purchase Price is 3,000,000 GBP.", false},
		{mv("£3,000,000", "Money"), "The Purchase Price is $3,000,000.", false},
		{mv("$3 million", "Money"), "The Purchase Price is USD 3 mn.", true},
		{mv("$3", "Money"), "The Purchase Price is USD 3 mn.", false},
		{mv("$3,000,000", "Money"), "The Purchase Price is $3,000,000¹.", true},
		{mv("$5 million", "Money"), "The gap shall not exceed 5 mm.", false},
		{mv("$12 million", "Money"), "steel plate of 12 mm and", false},
		{mv("$500", "Money"), "Rent is $500 per month.", true},
		{mv("$500", "Money"), "Rent is 500 dollars per month.", true},
		{mv("$500", "Money"), "Rent is USD 500 per month.", true},
		{mv("$500", "Money"), "Rule 500 applies.", false},
		{mv("€1.2 million", "Money"), "The fee is EUR 1.2 million.", true},
		{mv("$1,200,000", "Money"), "pay 1,200,000 to the Seller", true},
		{mv("$3,000,000", "Money"), "3,000,000 in cash", true},
		{mv("$1,200,000", "Money"), "pay 1,200,000 Canadian dollars", false},
		{mv("$1,200,000", "Money"), "pay 1,200,000 US dollars", true},
		{mv("$100,000", "Money"), "a salary of 100,000 annually", true},
		{mv("$100,000", "Money"), "a salary of 100,000 annually, payable in euros", false},
		{mv("$50,000", "Money"), "a fee of 50,000 CAD", false},
		{mv("5%", "Percentage"), "5 per cent of the price", true},
		{mv("$1,200,000", "Money"), "1,200,000 USD", true},
		{mv("$1.2 million", "Money"), "€1.200.000", false},
		{mv("€1.2 million", "Money"), "€1.200.000", true},
		{mv("€1,234.56", "Money"), "EUR 1.234,56", true},
		{mv("$1.234", "Money"), "EUR 1.234,56", false},
		{mv("€1.2", "Money"), "€1.200", false},
		{mv("€1,200", "Money"), "€1.200", false},
		{mv("$1,200", "Money"), "The fee is EUR 3,5 million and the cap is 1,200.", false},
		{mv("$2,500", "Money"), "2,500 Euro", false},
		{mv("$1,000", "Money"), "1,000 pound", false},
		{mv("3,000,000", "Count"), "3,000,000 shares", true},
		{mv("$3,000,000", "Money"), "3,000,000 shares", false},
		{mv("$2 million", "Money"), "2m shares", false},
		{mv("$2 million", "Money"), "$2m", true},
		{mv("$1,200,000", "Money"), "The Seller shall transfer 1,200,000 of the Company's 3,000,000 Shares to the Buyer.", false},
		{mv("1,200,000", "Count"), "The Seller shall transfer 1,200,000 of the Company's 3,000,000 Shares to the Buyer.", true},
		{mv("$3,000,000", "Money"), "the sum of Three Million Dollars ($3,000,000)", true},
		{mv("30", "Count"), "thirty (30) days", true},
		{mv("$12 billion", "Money"), "Rule 12b-1 applies.", false},
	} {
		if got := valueInQuote(c.v, c.q); got != c.want {
			t.Errorf("%s %q from %q: %v, want %v", c.v.Class, c.v.Value, c.q, got, c.want)
		}
		if c.v.Class == "Money" && c.v.Value[0] == '$' {
			if got := len(claimSums("x owes "+c.v.Value+".", c.q, nil)) == 1; got != c.want {
				t.Errorf("claimSums %q from %q: %v, want %v", c.v.Value, c.q, got, c.want)
			}
		}
	}
}

// TestFigureG8 is G8 over the corpus: a memo figure traces to a ground figure of its amount, class
// and currency, ranges expanded; the sign is not compared (prose states "(3,000)" as a loss of $3,000).
func TestFigureG8(t *testing.T) {
	for _, c := range []struct {
		memo, ground string
		untraced     []string
	}{
		{"The purchase price was $3 million.", "Section 3 of the Note.", []string{"3000000"}},
		{"The purchase price was $35 million.", "EUR 3,5 million", []string{"35000000"}},
		{"The purchase price was €3.5 million.", "EUR 3,5 million", nil},
		{"Damages are $3 million to $5 million.", "Damages are estimated at $3–5 million.", nil},
		{"Damages are between $3 million and $5 million.", "between $3 and $5 million", nil},
		{"The loss was $3,000.", "Net income (loss): (3,000).", nil},
		{"The fee is $5 million.", "a tolerance of 5 mm in any direction", []string{"5000000"}},
		{"The fee is $3 million.", "USD 3 mn", nil},
		{"The fee is $3 million.", "USD 3mn", nil},
		{"The fee is 1.5%.", "1,5 %", nil},
		{"The fee is 15%.", "1,5 %", []string{"15"}},
		{"The price is €1.2 million.", "€1.200.000", nil},
		{"The price is $1.2 million.", "1.2 million", nil},
		{"The price is £3 million.", "$3,000,000", []string{"3000000"}},
		{"The cap is 3,000,000 shares.", "$3 million", []string{"3000000"}},
		{"The cap is $3 million.", "3,000,000 shares", []string{"3000000"}},
		{"He paid 2.5 million", "$2,500,000", nil},
		{"He paid $2.5mm", "$2,500,000", nil},
		{"He paid $2,500K", "$2,500,000", nil},
		{"The cap is $1,200.", "The fee is EUR 3,5 million and the cap is 1,200.", []string{"1200"}},
		{"The cap is $1,200.", "The fee is EUR 3,5 million.\nThe cap is 1,200.", nil}, // the convention is read per line
	} {
		if got := Untraced(c.memo, c.ground, false); !slices.Equal(got, c.untraced) {
			t.Errorf("memo %q, ground %q: untraced %q, want %q", c.memo, c.ground, got, c.untraced)
		}
	}
}
