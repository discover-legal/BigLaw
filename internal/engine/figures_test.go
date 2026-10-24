// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"slices"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

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
		// lab-r4 r4 (the r3 verifier's findings, one row each)
		{mv("$1,000", "Money"), "Fees 1,000 USD 2,000 USD per seat", true},                            // H1
		{mv("$2,000", "Money"), "Fees 1,000 USD 2,000 USD per seat", true},                            // H1
		{mv("5%", "Percentage"), "Interest accrues at .5% per month.", false},                         // H2
		{mv("0.5%", "Percentage"), "Interest accrues at .5% per month.", true},                        // H2
		{mv("$500,000", "Money"), "a break fee of $.5 million", true},                                 // H2
		{mv("$5 million", "Money"), "a break fee of $.5 million", false},                              // H2
		{mv("$1,200,000", "Money"), "The fee is CAD$1,200,000.", false},                               // M1
		{mv("CAD 1,200,000", "Money"), "The fee is Cdn$1,200,000.", true},                             // M1
		{mv("$1,200,000", "Money"), "The fee is R$1,200,000.", false},                                 // M1
		{mv("$1,200,000", "Money"), "The fee is Rs. 1,200,000.", false},                               // M1
		{mv("INR 1,200,000", "Money"), "The fee is Rs. 1,200,000.", true},                             // M1
		{mv("$1,200,000", "Money"), "The fee is XYZ 1,200,000.", false},                               // M1
		{mv("$1,200,000", "Money"), "The fee is 1,200,000 dirhams.", false},                           // M1
		{mv("$1,200,000", "Money"), "The fee is 1,200,000円.", false},                                  // M1
		{mv("-$3,000", "Money"), "Net loss for the period: $(3,000)", true},                           // M2
		{mv("$3,000", "Money"), "Net loss for the period: USD (3,000)", false},                        // M2
		{mv("-$3,000", "Money"), "Net loss for the period: –3,000", true},                             // M2
		{mv("-$5,000", "Money"), "Adjustment: - $5,000", true},                                        // M2
		{mv("-12%", "Percentage"), "Change in revenue\t(12%)", true},                                  // M2
		{mv("-$5,000", "Money"), "The balance was minus $5,000.", true},                               // M2
		{mv("$5,000", "Money"), "the Purchase Price minus $5,000", true},                              // M2
		{mv("-$3,000", "Money"), "Net\t3,000-", true},                                                 // M2
		{mv("€1,200,000", "Money"), "The fee is EUR 3,5 million and the cap is EUR 1,200,000.", true}, // M3
		{mv("₹1,00,000", "Money"), "a fee of ₹1,00,000 and a deposit of INR 12,50,000", true},         // M4
		{mv("INR 1,250,000", "Money"), "a fee of ₹1,00,000 and a deposit of INR 12,50,000", true},     // M4
		{mv("$2,000", "Money"), "Rent is $2000 per month.", true},                                     // M5
		{mv("CHF 2,093.25", "Money"), "The fee is CHF 2093.25.", true},                                // M5
		{mv("3,000,000", "Count"), "Shares outstanding: 3,000,000", true},                             // M6
		{mv("73", "Count"), "The number of trades executed was 73.", true},                            // M6
		{mv("$5,000", "Money"), "Payment\t5,000", true},                                               // M6: a payment label names an amount
		{mv("CHF 1,200,000", "Money"), "CHF 1'200'000", true},                                         // L4
		{mv("$3,000,000", "Money"), "The Purchase Price is $3 000 000.", true},                        // L4
		{mv("$3,000,000", "Money"), "The Purchase Price is three million dollars.", true},             // L1 (r3)
		{mv("€5,000,000", "Money"), "5 Millionen EUR", true},                                          // L5
		{mv("$4,000", "Money"), "Revenue (in thousands): $4,000", false},                              // L7
		{mv("$4,000,000", "Money"), "Revenue (in thousands): $4,000", true},                           // L7
		{mv("$3,500", "Money"), "(USD 3,500) shall be paid as a deposit", true},                       // L3
		{mv("1,000", "Count"), "1.000 shares", false},                                                 // L6
		{mv("0.5%", "Percentage"), "a fee of 5‰", true},                                               // L8
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
		// lab-r4 r4
		{"The fee is 5%.", "Interest accrues at .5% per month.", []string{"5"}},                                                        // H2
		{"The fee is $1,200,000.", "The fee is CAD$1,200,000.", []string{"1200000"}},                                                   // M1
		{"The cap is €1,200,000.", "The fee is EUR 3,5 million and the cap is EUR 1,200,000.", nil},                                    // M3
		{"The Loan of $1,200,000 bears interest at 1,5 % per annum.", "Interest at 1,5 % per annum.\nThe Loan is USD 1,200,000.", nil}, // M3
		{"The fee is ₹1,00,000.", "a fee of ₹1,00,000", nil},                                                                           // M4
		{"The rent is $2,000.", "Rent is $2000 per month.", nil},                                                                       // M5
		{"The loss was −$3,000.", "Net income: $3,000.", []string{"3000"}},                                                             // L1: an explicit opposite sign
		{"A loss of $3,000.", "Net income (loss):\t(3,000)", nil},                                                                      // prose states a loss unsigned
		{"The price is $3 million.", "three million dollars", nil},                                                                     // written numbers
		{"Revenue was $4 million.", "Revenue (in thousands): $4,000", nil},                                                             // L7
	} {
		if got := Untraced(c.memo, c.ground, false); !slices.Equal(got, c.untraced) {
			t.Errorf("memo %q, ground %q: untraced %q, want %q", c.memo, c.ground, got, c.untraced)
		}
	}
}
