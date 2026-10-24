// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// figure is one number a text states, read with what it stands for: its currency, its scale
// ("$3 million" is 3,000,000; the "3" of "Section 3" is 3), a percent sign, and whether a noun
// that is no currency follows it ("3,000,000 shares" is a count, never a sum of money). Every
// figure check reads text through it (typed values, claimSums, G8, coverage), so none is scale-blind
// (lab-r4 verifier H1).
type figure struct {
	amount  *big.Rat
	cur     string // "USD", "GBP", … when a sign, code or currency word is stated; "" otherwise
	scaled  bool   // a scale word is stated
	grouped bool   // its thousands are grouped ("4,217")
	pct     bool   // "%" or "percent"
	unit    bool   // a noun that is no currency follows it: a count of something
	single  bool   // one bare digit, unscaled ("paragraph 3")
}

// figRe is a figure as text writes it: an optional currency, the digits, an optional scale (a
// single letter only after a currency: "$3m", never "Rule 12b"), an optional percent.
var figRe = regexp.MustCompile(`(?i)(US\$|USD\s?|C\$|A\$|CHF\s?|GBP\s?|EUR\s?|[$£€¥]\s?)?(\d[\d,]*(?:\.\d+)?)(\s*(?:million|billion|thousand)\b|\s?(?:mm|bn)\b|[mkb]\b)?(\s*(?:%|percent\b))?`)

var curOf = map[string]string{"$": "USD", "US$": "USD", "USD": "USD", "C$": "CAD", "A$": "AUD", "CHF": "CHF", "GBP": "GBP", "EUR": "EUR", "£": "GBP", "€": "EUR", "¥": "JPY"}

var scaleOf = map[string]int64{"thousand": 1e3, "k": 1e3, "million": 1e6, "m": 1e6, "mm": 1e6, "billion": 1e9, "b": 1e9, "bn": 1e9}

// currencyWord follows a bare figure as its currency ("500 dollars").
var currencyWord = map[string]string{"dollar": "USD", "dollars": "USD", "usd": "USD", "pounds": "GBP", "euros": "EUR", "euro": "EUR"}

// notUnit are the words that may follow a sum without naming what it counts ("$3,000,000 was paid",
// "3,000,000 of the Purchase Price", "1,250 per month").
var notUnit = map[string]bool{"and": true, "or": true, "to": true, "of": true, "in": true, "on": true, "at": true, "by": true, "for": true, "from": true,
	"as": true, "per": true, "plus": true, "less": true, "was": true, "were": true, "is": true, "are": true, "will": true, "shall": true, "be": true,
	"been": true, "being": true, "has": true, "have": true, "had": true, "paid": true, "payable": true, "due": true, "each": true,
	"net": true, "gross": true, "the": true, "a": true, "an": true, "which": true, "that": true, "under": true, "with": true, "over": true, "into": true,
	"total": true, "aggregate": true, "upon": true, "if": true, "when": true, "after": true, "before": true, "within": true, "against": true, "not": true}

// figuresIn reads every figure in text, dates first set aside (their digits are no figures).
func figuresIn(text string) (dateKeys []string, out []figure) {
	ds, spans := dates(text)
	masked := []byte(text)
	for _, loc := range spans {
		for i := loc[0]; i < loc[1]; i++ {
			masked[i] = ' '
		}
	}
	t := string(masked)
	for _, m := range figRe.FindAllStringSubmatchIndex(t, -1) {
		part := func(k int) string {
			if m[2*k] < 0 {
				return ""
			}
			return t[m[2*k]:m[2*k+1]]
		}
		digits := strings.TrimRight(part(2), ",.")
		r, ok := new(big.Rat).SetString(strings.ReplaceAll(digits, ",", ""))
		if !ok {
			continue
		}
		f := figure{amount: r, cur: curOf[strings.ToUpper(strings.TrimSpace(part(1)))], pct: part(4) != "", grouped: groupedFigure.MatchString(digits)}
		if sc := strings.ToLower(strings.TrimSpace(part(3))); sc != "" && (len(sc) > 1 || f.cur != "") {
			f.scaled = true
			r.Mul(r, new(big.Rat).SetInt64(scaleOf[sc]))
		}
		f.single = len(digits) == 1 && !f.scaled
		if !f.pct && f.cur == "" {
			end := m[5] // after the digits, and the scale when it was read as one
			if f.scaled {
				end = m[7]
			}
			w := nextWord(t[end:])
			if c, ok := currencyWord[w]; ok {
				f.cur = c
			} else {
				f.unit = w != "" && !notUnit[w]
			}
		}
		out = append(out, f)
	}
	return ds, out
}

// nextWord is the word that follows at the start of s, after spaces only, lower-cased ("" when a
// mark, a line break or the end comes first).
func nextWord(s string) string {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	if i < 0 {
		i = len(s)
	}
	return strings.ToLower(s[:i])
}

// money reports whether a figure states a sum of money: a currency, or a scale or grouped digits
// with no noun of another unit after it ("$3 million", "3,000,000"; never "3", "3,000,000 shares").
func (f figure) money() bool { return !f.pct && (f.cur != "" || (f.scaled || f.grouped) && !f.unit) }

// valueInQuote reports whether a typed value is stated in the quotes. A quantity (money, a count, a
// percentage) needs a figure of its class and amount, its scale read ("$3 million" never from the "3"
// of "Section 3"; a sum never from "3,000,000 shares"); a date a date the quotes state; anything else
// its text, or its figures (lab-r4 r2: the typed-value check was as scale-blind as H1's).
func valueInQuote(v analysis.Value, quotes string) bool {
	if strings.TrimSpace(v.Value) == "" {
		return false
	}
	class, canon, _, ok := analysis.Canon(v)
	if !ok || class == "Date" {
		return strings.Contains(strings.ToLower(norm(quotes)), strings.ToLower(norm(v.Value))) || covered(v.Value, quotes)
	}
	_, want, _ := strings.Cut(canon, ":")
	_, fs := figuresIn(quotes)
	for _, f := range fs {
		if analysis.Decimal(f.amount) != want {
			continue
		}
		switch class {
		case "Money":
			if f.money() {
				return true
			}
		case "Percentage":
			if f.pct {
				return true
			}
		default: // a count
			if f.cur == "" && !f.pct {
				return true
			}
		}
	}
	return false
}
