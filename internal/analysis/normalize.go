// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"fmt"
	"math/big"
	"regexp"
	"slices"
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
	dateLayouts = []string{"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05", "Mon, 2 Jan 2006",
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

// Canon is a value's comparable form within its class ("usd:9000000", "eur:3500000", "pct:73",
// "n:4217", "date:2024-04-15"), read by the figure parser (ValueFigure): a money value's prefix is
// its currency, the unit's when the text names none, US dollars when neither does. ok is false for a
// value that is not a comparable quantity (a duration, an identifier, a name, an ambiguous figure).
func Canon(v Value) (class Class, canon string, d Date, ok bool) {
	class = ClassOf(v)
	switch class {
	case "Money", "Percentage", "Count":
		if f, why := ValueFigure(v); why == "" && !duration(f) {
			r := new(big.Rat).Set(f.Amount)
			if f.Neg {
				r.Neg(r)
			}
			prefix := map[Class]string{"Percentage": "pct:", "Count": "n:"}[class]
			if class == "Money" {
				prefix = strings.ToLower(cmp.Or(f.Cur, "USD")) + ":"
			}
			return class, prefix + Decimal(r), d, true
		}
	case "Date":
		if d, ok := ParseDate(v.Value); ok {
			return class, "date:" + d.Key(), d, true
		}
	}
	return class, "", d, false
}

// duration reports whether a count is a span of time ("10 days"): never compared (a deadline in one
// document is not the same obligation's deadline in another).
func duration(f Figure) bool {
	return f.Class == CountFig && slices.Contains([]string{"day", "week", "month", "year", "hour", "minute", "quarter"}, strings.TrimSuffix(f.Noun, "s"))
}

// CanonAmount decodes Canon's own output (never text a person wrote): its kind ("usd", "pct",
// "n", "date"…) and, for a quantity, its signed amount.
func CanonAmount(canon string) (kind string, r *big.Rat, ok bool) {
	kind, num, _ := strings.Cut(canon, ":")
	r, ok = new(big.Rat).SetString(num)
	return kind, r, ok && kind != "date"
}
