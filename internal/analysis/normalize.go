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

// DaysBetween is the number of days between two full dates (never negative), as date_diff counts.
func DaysBetween(a, b Date) int64 {
	return abs((b.time().Unix() - a.time().Unix()) / 86400)
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

// ClassOf is a value's quantity class: the one stated, else the kind its figure was read as, else
// from its text (a date, an authority).
func ClassOf(v Value) Class {
	if c := ParseClass(v.Class); c != "" {
		return c
	}
	if v.Fig != nil && !v.Fig.Unclear {
		if c := (map[string]Class{KindMoney: "Money", KindPercent: "Percentage", KindCount: "Count", KindDate: "Date"})[v.Fig.Kind]; c != "" {
			return c
		}
	}
	return ClassifyLiteral(v.Value)
}

// Canon is a value's comparable form within its class ("usd:9000000", "eur:3500000", "pct:73",
// "n:4217", "date:2024-04-15"): a quantity's from the figure reader's clear reading of it (its kind,
// currency and exact amount: Value.Fig), a date's from its text. ok is false for a value that is not
// a comparable quantity (no reading, an unclear one, a kind other than its class, a duration or an
// identifier: kind other).
func Canon(v Value) (class Class, canon string, d Date, ok bool) {
	class = ClassOf(v)
	switch class {
	case "Money", "Percentage", "Count":
		if f := v.Fig; f != nil && f.Clear() && f.Kind == KindOf(Value{Class: string(class)}) {
			prefix := map[Class]string{"Percentage": "pct:", "Count": "n:"}[class]
			if class == "Money" {
				prefix = strings.ToLower(f.Currency) + ":"
			}
			return class, prefix + f.Amount, d, true
		}
	case "Date":
		if d, ok := ParseDate(v.Value); ok {
			return class, "date:" + d.Key(), d, true
		}
	}
	return class, "", d, false
}

// CanonAmount decodes Canon's own output (never text a person wrote): its kind ("usd", "pct",
// "n", "date"…) and, for a quantity, its signed amount.
func CanonAmount(canon string) (kind string, r *big.Rat, ok bool) {
	kind, num, _ := strings.Cut(canon, ":")
	r, ok = new(big.Rat).SetString(num)
	return kind, r, ok && kind != "date"
}
