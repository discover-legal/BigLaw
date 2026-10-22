// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Date is a calendar date at the precision its source stated: Month and Day may be 0.
type Date struct{ Year, Month, Day int }

// Key is the date as ISO text at its precision: 2024, 2024-04 or 2024-04-22.
func (d Date) Key() string {
	switch {
	case d.Month == 0:
		return fmt.Sprintf("%04d", d.Year)
	case d.Day == 0:
		return fmt.Sprintf("%04d-%02d", d.Year, d.Month)
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// Compatible reports whether two dates agree at their coarsest common precision
// ("April 2024" agrees with "April 22, 2024"; "April 15, 2024" does not).
func (d Date) Compatible(e Date) bool {
	return d.Year == e.Year && (d.Month == 0 || e.Month == 0 || d.Month == e.Month && (d.Day == 0 || e.Day == 0 || d.Day == e.Day))
}

func (d Date) time() time.Time {
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, time.UTC)
}

var (
	dateLayouts = []string{"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006",
		"2 Jan 2006", "2 January 2006", "January 2, 2006", "Jan 2, 2006", "Jan. 2, 2006", "January 2 2006", "2006-01-02", "01/02/2006", "1/2/2006"}
	monthLayouts = []string{"January 2006", "Jan 2006", "Jan. 2006", "01/2006", "1/2006", "2006-01"}
	reYear       = regexp.MustCompile(`(?i)^(?:early|late|mid)?[ -]?((?:19|20)\d{2})$`)
)

// ParseDate reads a date at its stated precision, tolerating hedges ("on or about April 22,
// 2024") and e-mail headers.
func ParseDate(s string) (Date, bool) {
	s = strings.Join(strings.Fields(s), " ")
	for _, p := range []string{"on or about", "on or around", "dated", "date:"} {
		if len(s) > len(p) && strings.EqualFold(s[:len(p)], p) {
			s = strings.TrimSpace(s[len(p):])
		}
	}
	s = strings.Trim(s, ".,")
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return Date{t.Year(), int(t.Month()), t.Day()}, true
		}
	}
	for _, l := range monthLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return Date{t.Year(), int(t.Month()), 0}, true
		}
	}
	if m := reYear.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		return Date{y, 0, 0}, true
	}
	return Date{}, false
}

// DateToken is a date found in running text.
type DateToken struct {
	Raw  string
	Date Date
}

var reDateIn = regexp.MustCompile(`(?i)\bQ([1-4])\s+((?:19|20)\d\d)\b|\b(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?(?:\s+\d{1,2},)?\s+(?:19|20)\d\d\b|(?:§\s*|\bU\.S\.C\.\s*)?\b(?:19|20)\d\d\b`)

// DatesIn lists the dates in text: quarters (at their last month), months, full dates and bare
// years; a statute number ("28 U.S.C. § 2462") is not a year.
func DatesIn(text string) []DateToken {
	var out []DateToken
	for _, m := range reDateIn.FindAllStringSubmatch(text, -1) {
		raw := m[0]
		switch {
		case m[1] != "":
			q, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			out = append(out, DateToken{raw, Date{y, q * 3, 0}})
		case strings.ContainsAny(raw, "§") || strings.Contains(strings.ToUpper(raw), "U.S.C"):
		default:
			if d, ok := ParseDate(raw); ok {
				out = append(out, DateToken{raw, d})
			}
		}
	}
	return out
}

var (
	reMoneyVal = regexp.MustCompile(`(?i)^~?(?:approximately\s+)?(?:usd\s*)?\$?\s*([\d,]*\.?\d+)\s*(million|billion|thousand|mm|bn|[kmb])?$`)
	rePctVal   = regexp.MustCompile(`^~?(?:approximately\s+)?(-?[\d,]*\.?\d+)\s*(%|percent)$`)
	reNumVal   = regexp.MustCompile(`^~?(?:approximately\s+)?-?[\d,]*\.?\d+$`)
	scale      = map[string]int64{"k": 1e3, "thousand": 1e3, "m": 1e6, "mm": 1e6, "million": 1e6, "b": 1e9, "bn": 1e9, "billion": 1e9}
)

// Number parses a plain or scaled decimal exactly ("4,217", "$9.0 million" → 9000000).
func Number(s string) (*big.Rat, bool) {
	t := strings.TrimSpace(s)
	if m := reMoneyVal.FindStringSubmatch(t); m != nil && (strings.Contains(t, "$") || m[2] != "" || strings.HasPrefix(strings.ToLower(t), "usd")) {
		r, ok := new(big.Rat).SetString(strings.ReplaceAll(m[1], ",", ""))
		if ok && m[2] != "" {
			r.Mul(r, new(big.Rat).SetInt64(scale[strings.ToLower(m[2])]))
		}
		return r, ok
	}
	if m := rePctVal.FindStringSubmatch(t); m != nil {
		return new(big.Rat).SetString(strings.ReplaceAll(m[1], ",", ""))
	}
	if reNumVal.MatchString(t) {
		return new(big.Rat).SetString(strings.ReplaceAll(strings.TrimLeft(strings.TrimPrefix(t, "approximately "), "~"), ",", ""))
	}
	return nil, false
}

// MoneyCents is a money value in cents ("$22.2 million" → 2,220,000,000).
func MoneyCents(s string) (int64, bool) {
	r, ok := Number(s)
	if !ok {
		return 0, false
	}
	r.Mul(r, big.NewRat(100, 1))
	return new(big.Int).Quo(r.Num(), r.Denom()).Int64(), r.IsInt()
}

// Decimal prints r exactly when it has at most 6 decimal places, else rounded to 6.
func Decimal(r *big.Rat) string {
	s := r.FloatString(6)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s
}

// ClassOf is a value's quantity class: the one stated, else from its unit, else from its text.
func ClassOf(v Value) Class {
	if c := ParseClass(v.Class); c != "" {
		return c
	}
	switch u := strings.ToLower(strings.TrimSpace(v.Unit)); {
	case u == "usd" || u == "$" || u == "dollars":
		return "Money"
	case u == "%" || u == "percent":
		return "Percentage"
	case u != "" && u != "days" && u != "years" && u != "months":
		return "Count"
	}
	return ClassifyLiteral(v.Value)
}

// Canon is a value's comparable form within its class ("usd:9000000", "pct:73", "n:4217",
// "date:2024-04-15"); ok is false for a value that is not a comparable quantity (a duration, an
// identifier, a name).
func Canon(v Value) (class Class, canon string, d Date, ok bool) {
	class = ClassOf(v)
	switch class {
	case "Money", "Percentage", "Count":
		if r, ok := Number(v.Value); ok && !(class == "Count" && strings.ContainsAny(v.Value, "$%")) {
			return class, map[Class]string{"Money": "usd:", "Percentage": "pct:", "Count": "n:"}[class] + Decimal(r), d, true
		}
	case "Date":
		if d, ok := ParseDate(v.Value); ok {
			return class, "date:" + d.Key(), d, true
		}
	}
	return class, "", d, false
}
