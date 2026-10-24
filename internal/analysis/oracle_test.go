// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"math/big"
	"regexp"
	"strings"
)

// What a number means is the figure reader's (the engine's read_figures); in these tests a typed
// value's reading is given (fig), or read from its own text by valueOracle, a stand-in that reads the
// tests' plain values as a careful reader would: a sign or code beside it is its currency, "%" a
// percentage, a scale word its scale, its class what the analyst typed, its party its entity. It is
// test data, not a rule of the package.

var (
	oracleSign  = map[string]string{"US$": "USD", "$": "USD", "€": "EUR", "£": "GBP", "¥": "JPY", "₹": "INR", "C$": "CAD", "A$": "AUD"}
	oracleCode  = regexp.MustCompile(`\b(USD|EUR|GBP|CHF|CAD|JPY|INR|AUD|SEK)\b`)
	durationRe  = regexp.MustCompile(`(?i)\b(days?|weeks?|months?|years?)\b`)
	oracleScale = map[string]string{"million": "1000000", "billion": "1000000000", "thousand": "1000", "mn": "1000000", "bn": "1000000000"}
)

// valueOracle is a typed value's reading from its own text: its one span, its class, a currency sign
// or code in it (else its unit, else US dollars for money), a scale word after it; party its entity,
// what it measures its metric, no period.
func valueOracle(v Value) *Reading {
	if c := ClassifyLiteral(v.Value); ParseClass(v.Class) == "" && c != "" && c != "Date" { // an authority, a fund: no figure
		return nil
	} else if ParseClass(v.Class) == "Date" || ParseClass(v.Class) == "" && c == "Date" {
		if _, ok := ParseDate(v.Value); ok || ParseClass(v.Class) == "Date" {
			return &Reading{Kind: KindDate, Text: v.Value, Party: strings.TrimSpace(v.Entity), Measures: v.Metric, Period: "none"}
		}
	}
	_, ss := Literal(v.Value)
	if len(ss) == 0 {
		if ParseClass(v.Class) == "Date" && v.Entity != "" {
			return &Reading{Kind: KindDate, Text: v.Value, Party: v.Entity, Measures: v.Metric, Period: "none"}
		}
		return nil
	}
	s := ss[0]
	r := &Reading{Span: "v1.s1", Text: s.Text, Start: s.Start, End: s.End, Party: strings.TrimSpace(v.Entity), Measures: v.Metric, Period: "none"}
	switch ParseClass(v.Class) {
	case "Money":
		r.Kind = KindMoney
	case "Percentage":
		r.Kind = KindPercent
	case "Count":
		r.Kind = KindCount
	case "Date":
		r.Kind = KindDate
		return r
	default:
		switch {
		case strings.Contains(v.Value, "%"):
			r.Kind = KindPercent
		case oracleCode.MatchString(v.Value) || strings.ContainsAny(v.Value, "$€£¥₹"):
			r.Kind = KindMoney
		default:
			r.Kind = KindCount
		}
	}
	if r.Kind == KindCount && durationRe.MatchString(v.Value+" "+v.Unit) {
		r.Kind = KindOther // a span of time, never compared
	}
	if r.Kind == KindMoney && !strings.ContainsAny(v.Value, "$€£¥₹") && !oracleCode.MatchString(v.Value) && strings.TrimSpace(v.Unit) == "" {
		r.Unclear, r.Why = true, "no currency stated" // a bare "92" typed as money: whose currency and scale, the text does not say
	}
	if r.Kind == KindMoney {
		r.Currency = "USD"
		if u := strings.ToUpper(strings.TrimSpace(v.Unit)); len(u) == 3 {
			r.Currency = u
		}
		before, best := strings.TrimSpace(v.Value[:s.Start]), ""
		for sign := range oracleSign {
			if strings.HasSuffix(before, sign) && len(sign) > len(best) {
				best = sign
			}
		}
		if best != "" {
			r.Currency = oracleSign[best]
		}
		if m := oracleCode.FindString(v.Value); m != "" {
			r.Currency = m
		}
	}
	dec := byte(0)
	if strings.Count(s.Text, ".") == 1 {
		dec = '.'
	} else if strings.Count(s.Text, ",") == 1 && len(s.Text)-strings.LastIndex(s.Text, ",")-1 != 3 {
		dec = ','
	}
	x, ok := s.Value(dec)
	if !ok {
		r.Unclear, r.Why = true, "its decimal mark"
		return r
	}
	after := strings.ToLower(strings.TrimSpace(v.Value[s.End:] + " " + v.Unit))
	for w, m := range oracleScale {
		if strings.HasPrefix(after, w) {
			mul, _ := new(big.Rat).SetString(m)
			x.Mul(x, mul)
			r.ScaleFrom = w
		}
	}
	if strings.HasPrefix(strings.TrimSpace(v.Value), "-") || strings.HasPrefix(strings.TrimSpace(v.Value), "(") {
		x.Abs(x)
		x.Neg(x)
	}
	r.Amount = exactDecimal(x)
	return r
}

// read gives every value of fs the stand-in's reading (one already given is kept).
func read(fs []Finding) []Finding {
	out := make([]Finding, len(fs))
	for i, f := range fs {
		f.Values = append([]Value{}, f.Values...)
		for j, v := range f.Values {
			if v.Fig == nil {
				f.Values[j].Fig = valueOracle(v)
			}
		}
		out[i] = f
	}
	return out
}

// joinRead is Join over fs with the stand-in's readings.
func joinRead(fs []Finding, aliases []Alias) []Conflict { return Join(read(fs), aliases) }

// fig is a given clear reading: kind, currency ("" when not money), exact amount, party (what it
// measures: the value's metric, as Value.Fig is given in fnd).
func fig(kind, cur, amount, party string) *Reading {
	return &Reading{Kind: kind, Currency: cur, Amount: amount, Party: party, Period: "none"}
}

// figOf is fig with what it measures and the period it is for.
func figOf(kind, cur, amount, party, measures, period string) *Reading {
	r := fig(kind, cur, amount, party)
	r.Measures, r.Period = measures, period
	return r
}

// relabel is a test's right label for a value's reading where the stand-in's would be wrong: whose
// figure it is, what it measures, for when, and what its typed entity is to it ("" keeps the
// stand-in's).
type relabel struct{ party, measures, period, entity string }

// readAs is read with the labels keyed by "FID/value" or "value" in ls.
func readAs(fs []Finding, ls map[string]relabel) []Finding {
	fs = read(fs)
	for i := range fs {
		for j, v := range fs[i].Values {
			l, ok := ls[fs[i].FID+"/"+v.Value]
			if !ok {
				l, ok = ls[v.Value]
			}
			if ok && v.Fig != nil {
				x := *v.Fig
				x.Party, x.Measures, x.Period, x.TypedEntity = cmp.Or(l.party, x.Party), cmp.Or(l.measures, x.Measures), cmp.Or(l.period, x.Period), cmp.Or(l.entity, x.TypedEntity)
				fs[i].Values[j].Fig = &x
			}
		}
	}
	return fs
}

// withFig is v with the stand-in's reading.
func withFig(v Value) Value { v.Fig = valueOracle(v); return v }
