// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// literal is what Literal reads in s, as the corpus writes it: each date as "date:KEY", each span as
// its text, "=", and its readings ("1,200=1200|1.2"; "-3,000=-3000").
func literal(s string) []string {
	ds, ss := Literal(s)
	out := []string{}
	for _, d := range ds {
		out = append(out, "date:"+d)
	}
	for _, x := range ss {
		var rs []string
		for _, r := range x.Readings() {
			rs = append(rs, Decimal(r))
		}
		out = append(out, x.Text+"="+strings.Join(rs, "|"))
	}
	return out
}

// literalCorpus specifies Literal: each input and what it states as written (literal), dates as
// keys and each span with every reading its form allows. What a span means is never decided here
// (the figure reader's labels, ReadLabels): "$3 million" is the span "3", "(3,000)" the span
// "3,000", "1,200" reads 1200 or 1.2. The inputs are the earlier parser corpus's.
var literalCorpus = []struct {
	in   string
	want []string
}{
	{"Section 3 provides that the principal, in the amount of one million, is due.", []string{"3=3"}},
	{"The fee is $3 and the deposit is million.", []string{"3=3"}},
	{"The Purchaser shall pay EUR 3,5 million on Closing.", []string{"3,5=3.5"}},
	{"Interest accrues at 1,5 % per annum.", []string{"1,5=1.5"}},
	{"Damages are estimated at $3–5 million.", []string{"3=3", "5=5"}},
	{"Damages are estimated at between $3 and $5 million.", []string{"3=3", "5=5"}},
	{"Damages are estimated at $3 million to $5 million.", []string{"3=3", "5=5"}},
	{"Net income (loss) for the period: (3,000).", []string{"3,000=3000|3"}},
	{"Net income for the period: $3,000.", []string{"3,000=3000|3"}},
	{"-$3,000", []string{"3,000=3000|3"}},
	{"a fee of 3% of $1 million", []string{"3=3", "1=1"}},
	{"The Purchase Price is €3,000,000.", []string{"3,000,000=3000000"}},
	{"The Purchase Price is 3,000,000 €.", []string{"3,000,000=3000000"}},
	{"The Purchase Price is 3,000,000 GBP.", []string{"3,000,000=3000000"}},
	{"The Purchase Price is $3,000,000.", []string{"3,000,000=3000000"}},
	{"The Purchase Price is USD 3 mn.", []string{"3=3"}},
	{"The Purchase Price is USD 3mn.", []string{"3=3"}},
	{"The Purchase Price is three million dollars.", []string{}},
	{"The Purchase Price is $3,000,000¹.", []string{"3,000,000=3000000"}},
	{"The Purchase Price is $3\u00a0000\u00a0000.", []string{"3\u00a0000\u00a0000=3000000"}},
	{"The gap between panels shall not exceed 5 mm in any direction.", []string{"5=5"}},
	{"steel plate of 12 mm and", []string{"12=12"}},
	{"Rent is $500 per month.", []string{"500=500"}},
	{"Rent is 500 dollars per month.", []string{"500=500"}},
	{"Rent is USD 500 per month.", []string{"500=500"}},
	{"The fee is EUR 1.2 million.", []string{"1.2=1.2"}},
	{"pay 1,200,000 to the Seller", []string{"1,200,000=1200000"}},
	{"3,000,000 in cash", []string{"3,000,000=3000000"}},
	{"pay 1,200,000 Canadian dollars", []string{"1,200,000=1200000"}},
	{"The Buyer shall pay 1,200,000 US dollars at Closing.", []string{"1,200,000=1200000"}},
	{"The Executive's salary is 100,000 annually.", []string{"100,000=100000|100"}},
	{"a fee of 50,000 CAD", []string{"50,000=50000|50"}},
	{"ten percent of the price", []string{}},
	{"Interest accrues at 5 per cent per annum.", []string{"5=5"}},
	{"5 percent of the price", []string{"5=5"}},
	{"1,200,000 USD", []string{"1,200,000=1200000"}},
	{"The price is €1.200.000.", []string{"1.200.000=1200000"}},
	{"EUR 1.234,56", []string{"1.234,56=1234.56"}},
	{"€1.200", []string{"1.200=1200|1.2"}},
	{"an aggregate of $3 million", []string{"3=3"}},
	{"2,500 Euro", []string{"2,500=2500|2.5"}},
	{"1,000 pounds", []string{"1,000=1000|1"}},
	{"1,000 pound", []string{"1,000=1000|1"}},
	{"The Company issued 3,000,000 shares.", []string{"3,000,000=3000000"}},
	{"2m shares", []string{"2=2"}},
	{"$2m", []string{"2=2"}},
	{"$2.5mm", []string{"2.5=2.5"}},
	{"$2,500K", []string{"2,500=2500|2.5"}},
	{"١٠٠ dollars", []string{"١٠٠=100"}},
	{"$3,000,000.\u200b", []string{"3,000,000=3000000"}},
	{"＄3,000,000", []string{"3,000,000=3000000"}},
	{"The Seller shall transfer 1,200,000 of the Company's 3,000,000 Shares to the Buyer.", []string{"1,200,000=1200000", "3,000,000=3000000"}},
	{"37 of the 120 trades", []string{"37=37", "120=120"}},
	{"Rule 12b-1 fees of $4,000.", []string{"12=12", "1=1", "4,000=4000|4"}},
	{"Rule 10b-5 applies.", []string{"10=10", "5=5"}},
	{"Principal: $3m.", []string{"3=3"}},
	{"Principal: USD 3,000,000.", []string{"3,000,000=3000000"}},
	{"Within 3 days the Company shall pay 3 instalments.", []string{"3=3", "3=3"}},
	{"The Company shall pay 3 million instalments.", []string{"3=3"}},
	{"Greystone claims damages of $644,910 for unpaid invoices", []string{"644,910=644910|644.91"}},
	{"payments totaling $103,800.00", []string{"103,800.00=103800"}},
	{"WCA's account took 73% of volume.", []string{"73=73"}},
	{"WCA made 73 trades.", []string{"73=73"}},
	{"Forty Percent (40%) … Thirty-Five Percent (35%) … Serial #612847 … $92,600", []string{"40=40", "35=35", "612847=612847", "92,600=92600|92.6"}},
	{"the sum of Three Million Dollars ($3,000,000)", []string{"3,000,000=3000000"}},
	{"thirty (30) days", []string{"30=30"}},
	{"Revenue\t(1,250)\t4,000", []string{"1,250=1250|1.25", "4,000=4000|4"}},
	{"A fee of €1,200 and $5 per page, or 40 CHF, plus £30.", []string{"1,200=1200|1.2", "5=5", "40=40", "30=30"}},
	{"$3,000 CAD", []string{"3,000=3000|3"}},
	{"Sections 3 and 14 million in fees", []string{"3=3", "14=14"}},
	{"clauses 3(a) and 3(b) or 3(c); 1,200 rows", []string{"3=3", "3=3", "3=3", "1,200=1200|1.2"}},
	{"Section 7.3.1 of the Manual", []string{}},
	{"The fee is EUR 3,5 million and the cap is 1,200.", []string{"3,5=3.5", "1,200=1200|1.2"}},
	{"pay $500 to 3 creditors", []string{"500=500", "3=3"}},
	{"3-5 days", []string{"3=3", "5=5"}},
	{"from 3 to 5 per cent", []string{"3=3", "5=5"}},
	{"The fee is 3,5 Mio. EUR.", []string{"3,5=3.5"}},
	{"US$ 2,000 and C$1,500 and A$10", []string{"2,000=2000|2", "1,500=1500|1.5", "10=10"}},
	{"rates of 1.5% to 2.5%", []string{"1.5=1.5", "2.5=2.5"}},
	{"a 25 basis points rise", []string{"25=25"}},
	{"500 full-time employees", []string{"500=500"}},
	{"1,000,000 Class A Shares", []string{"1,000,000=1000000"}},
	{"The cap is −€5,000.", []string{"5,000=5000|5"}},
	{"($3,000,000)", []string{"3,000,000=3000000"}},
	{"1,200", []string{"1,200=1200|1.2"}},
	{"103,800.00", []string{"103,800.00=103800"}},
	{"Fees 1,000 USD 2,000 USD", []string{"1,000=1000|1", "2,000=2000|2"}},
	{"Rent 5 $ 10", []string{"5=5", "10=10"}},
	{"Q1 1.000 EUR 2.000 EUR", []string{"1.000=1000|1", "2.000=2000|2"}},
	{"100 € 200 €", []string{"100=100", "200=200"}},
	{"fee 50 GBP 75 GBP total", []string{"50=50", "75=75"}},
	{"USD 1,000 USD 2,000", []string{"1,000=1000|1", "2,000=2000|2"}},
	{"Price 1,000 USD - 2,000 USD", []string{"1,000=1000|1", "2,000=2000|2"}},
	{"1,000 USD to USD 2,000", []string{"1,000=1000|1", "2,000=2000|2"}},
	{"Interest accrues at .5% per month on overdue sums.", []string{".5=0.5"}},
	{"The Buyer shall pay a break fee of $.5 million.", []string{".5=0.5"}},
	{"No.5 applies", []string{"5=5"}},
	{"The fee is CAD$1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is Cdn$1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is AUD$1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is R$1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is NT$1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is AED 1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is Rs. 1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is RMB 1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is ₩1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is ₱1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is 1,200,000円.", []string{"1,200,000=1200000"}},
	{"The fee is kr 1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is 1,200,000 dirhams.", []string{"1,200,000=1200000"}},
	{"The fee is ZZ$1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is XYZ 1,200,000.", []string{"1,200,000=1200000"}},
	{"The fee is ₿1,200,000.", []string{"1,200,000=1200000"}},
	{"ISO 9001 applies", []string{"9001=9001"}},
	{"WCA 7,912 trades", []string{"7,912=7912|7.912"}},
	{"CHF 500 and Fr. 20 and 100 zł and ₪50 and ₺60 and ₫70 and ₦80 and 5 yuan and 7 kronor and 9 rand and 4 pesos and 3 reais and 2 francs and 6 rupees and 8 yen", []string{"500=500", "20=20", "100=100", "50=50", "60=60", "70=70", "80=80", "5=5", "7=7", "9=9", "4=4", "3=3", "2=2", "6=6", "8=8"}},
	{"Net loss for the period: $(3,000)", []string{"3,000=3000|3"}},
	{"Net loss for the period: USD (3,000)", []string{"3,000=3000|3"}},
	{"Net loss for the period: –3,000", []string{"–3,000=-3000|-3"}},
	{"Net loss for the period: —3,000", []string{"—3,000=-3000|-3"}},
	{"Adjustment: - $5,000", []string{"5,000=5000|5"}},
	{"Change in revenue\t(12%)", []string{"12=12"}},
	{"The balance was minus $5,000", []string{"5,000=5000|5"}},
	{"the Purchase Price minus $5,000", []string{"5,000=5000|5"}},
	{"negative 3,000", []string{"3,000=3000|3"}},
	{"Net\t3,000-", []string{"3,000=3000|3"}},
	{"(USD 3,500) shall be paid as a deposit", []string{"3,500=3500|3.5"}},
	{"The fee is EUR 3,5 million and the cap is EUR 1,200,000.", []string{"3,5=3.5", "1,200,000=1200000"}},
	{"The fee is EUR 3,5 million and the cap is 1,200.", []string{"3,5=3.5", "1,200=1200|1.2"}},
	{"The Licensee shall pay a fee of ₹1,00,000 and a deposit of INR 12,50,000.", []string{"1,00,000=100000", "12,50,000=1250000"}},
	{"INR 12,34,567", []string{"12,34,567=1234567"}},
	{"$1,00,000", []string{"1,00,000=100000"}},
	{"5 lakh rupees and 2 crore", []string{"5=5", "2=2"}},
	{"Rent is $2000 per month.", []string{"2000=2000"}},
	{"The deposit is €1950.", []string{"1950=1950"}},
	{"The fee is CHF 2093.25.", []string{"2093.25=2093.25"}},
	{"In 2024 the Company had 2000 employees.", []string{"2024=2024", "2000=2000"}},
	{"in 2024 shares were issued", []string{"2024=2024"}},
	{"the 1999 shares", []string{"1999=1999"}},
	{"Shares outstanding: 3,000,000", []string{"3,000,000=3000000"}},
	{"The number of trades executed was 73.", []string{"73=73"}},
	{"Number of employees: 500", []string{"500=500"}},
	{"Payment\t5,000", []string{"5,000=5000|5"}},
	{"3 instalments of 5,000", []string{"3=3", "5,000=5000|5"}},
	{"CHF 1'200'000", []string{"1'200'000=1200000"}},
	{"CHF 1’200’000.50", []string{"1’200’000.50=1200000.5"}},
	{"$3 000 000", []string{"3=3", "000=0", "000=0"}},
	{"1 200 000 $", []string{"1=1", "200=200", "000=0"}},
	{"120 450 300", []string{"120=120", "450=450", "300=300"}},
	{"EUR 1 234 567,89", []string{"1=1", "234=234", "567,89=567.89"}},
	{"three million dollars", []string{}},
	{"five per cent", []string{}},
	{"one hundred and twenty thousand euros", []string{}},
	{"between three and five million dollars", []string{}},
	{"Revenue (in thousands): $4,000", []string{"4,000=4000|4"}},
	{"($ in millions)\nRevenue\t4,000\tCosts\t(1,250)\n\nOther 4,000", []string{"4,000=4000|4", "1,250=1250|1.25", "4,000=4000|4"}},
	{"(in thousands, except per share data)\nEPS\t1.25", []string{"1.25=1.25"}},
	{"5 millions", []string{"5=5"}},
	{"5 Millionen EUR", []string{"5=5"}},
	{"5 Mrd. EUR", []string{"5=5"}},
	{"EUR 3,5 Mio.", []string{"3,5=3.5"}},
	{"1.5.2024 EUR 3,5", []string{"date:2024-05-01", "3,5=3.5"}},
	{"15.03.2024", []string{"date:2024-03-15"}},
	{"1.000 shares", []string{"1.000=1000|1"}},
	{"3.5x EBITDA", []string{"3.5=3.5"}},
	{"5‰", []string{"5=5"}},
	{"$3 – $5", []string{"3=3", "5=5"}},
	{"Fees 1,000 USD 2,000 USD per seat; Rent 5 $ 10", []string{"1,000=1000|1", "2,000=2000|2", "5=5", "10=10"}},
	// lab-r4 r4 (owner decision: code reads only what is written): signs as written, identifiers'
	// digits, outline numbers, plain-space columns, dates, other scripts' digits
	{"Net:\t-1,250 and – 3 and 5-3 and 10b5 and F12", []string{"-1,250=-1250|-1.25", "3=3", "5=5", "3=3", "10=10"}},
	{"Section 7.3.1, 1.2.3 and 4.", []string{"4=4"}},
	{"On 10/21/2026 and April 15, 2024 and 2024-04-22, in 2024", []string{"date:2026-10-21", "date:2024-04-15", "date:2024-04-22", "2024=2024"}},
	{"１２,５００ shares and ١٠٠", []string{"１２,５００=12500|12.5", "١٠٠=100"}},
	{"1,234.567.8 and 1..2 and 3,,4", []string{"1=1", "2=2", "3=3", "4=4"}},
}

func TestLiteralCorpus(t *testing.T) {
	for _, c := range literalCorpus {
		if got := literal(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
	if len(literalCorpus) < 160 {
		t.Errorf("the corpus shrank to %d cases", len(literalCorpus))
	}
}

// literalProperties are Literal's and Spans' invariants over any text: no panic, spans in bounds,
// in order and never overlapping, each span's text exactly the text's bytes there, and every reading
// round-trips: written plainly, it reads back to itself.
// literalProperties checks Spans' and Literal's spans of s and returns Spans', which it checked.
func literalProperties(t *testing.T, s string) []Span {
	t.Helper()
	spans := Spans(s)
	for _, ss := range [][]Span{spans, func() []Span { _, x := Literal(s); return x }()} {
		for i, x := range ss {
			if x.Start < 0 || x.End > len(s) || x.Start >= x.End || s[x.Start:x.End] != x.Text {
				t.Fatalf("bad span %+v in %q", x, s)
			}
			if i > 0 && ss[i-1].End > x.Start {
				t.Fatalf("overlapping spans %q and %q in %q", ss[i-1].Text, x.Text, s)
			}
			for _, r := range x.Readings() {
				back := Spans(exactDecimal(r))
				if len(back) != 1 || !slices.ContainsFunc(back[0].Readings(), func(y *big.Rat) bool { return y.Cmp(r) == 0 }) {
					t.Fatalf("%q in %q reads %s, which reads back as %v", x.Text, s, exactDecimal(r), back)
				}
			}
		}
	}
	return spans
}

// TestLiteralPropertyGenerated: 100,000 numbers written plain, US-grouped, continental, with
// apostrophes or NBSP, signed or not, inside sentences, beside currency signs and words, percent
// signs and nouns: each is found as exactly one span with its text and its value among its readings,
// and the properties hold.
func TestLiteralPropertyGenerated(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	n := 100000
	if testing.Short() {
		n = 10000
	}
	group := func(digits, sep string) string {
		for k := len(digits) - 3; k > 0; k -= 3 {
			digits = digits[:k] + sep + digits[k:]
		}
		return digits
	}
	bad := 0
	for range n {
		ip := int64(r.IntN(10))*[]int64{1, 10, 100, 1000, 10000, 1000000, 1}[r.IntN(7)] + int64(r.IntN(999))
		digits := fmt.Sprint(ip)
		frac := []string{"", "", "5", "25", "50"}[r.IntN(5)]
		num, dec := digits, "."
		switch r.IntN(5) {
		case 1:
			num = group(digits, ",")
		case 2:
			num, dec = group(digits, "."), ","
		case 3:
			num = group(digits, "'")
		case 4:
			num = group(digits, "\u00a0")
		}
		if frac != "" {
			num += dec + frac
		}
		want, _ := new(big.Rat).SetString(digits + map[bool]string{true: "." + frac}[frac != ""])
		if r.IntN(4) == 0 {
			num, want = "-"+num, want.Neg(want)
		}
		ctx := []string{"The Purchaser shall pay $%s on Closing.", "Total:\t%s", "%s", "Net: %s.", "fee of EUR %s", "%s shares", "%s%%", "(%s)", "CHF %s per seat"}[r.IntN(9)]
		line := fmt.Sprintf(ctx, num)
		ss := literalProperties(t, line) // Spans(line), checked
		if len(ss) != 1 || ss[0].Text != num || !slices.ContainsFunc(ss[0].Readings(), func(x *big.Rat) bool { return x.Cmp(want) == 0 }) {
			if bad++; bad <= 20 {
				t.Errorf("%q reads %+v, want %q = %s", line, ss, num, want.RatString())
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d generated numbers misread", bad, n)
	}
}

// FuzzFigureProperties: the properties over arbitrary text (go test -fuzz=FuzzFigureProperties).
func FuzzFigureProperties(f *testing.F) {
	for _, c := range literalCorpus {
		f.Add(c.in)
	}
	f.Fuzz(func(t *testing.T, s string) { literalProperties(t, s) })
}
