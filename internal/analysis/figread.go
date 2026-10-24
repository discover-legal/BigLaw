// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The meaning half of figure reading (lab-r4 r4, owner decision): the figure-reading agent (the
// engine's read_figures) labels each span code found (Spans) with what it measures, and ReadLabels
// checks the labels mechanically before anything reads them: a label may name only a span it was
// given, a scale must come from a span or from text the passage literally holds, a decimal mark must
// fit the span's form, a scale is a power of ten, and anything it cannot check, or that the agent says
// is unclear, is an unclear reading (low confidence: gated to a lawyer, never verifying by itself).
// The amount is exact arithmetic on the span's digits: reading × scale × sign, so a reading never
// states digits the text does not.

// Kinds a figure can be read as; "unclear" is always allowed.
const (
	KindMoney   = "money"
	KindCount   = "count"
	KindPercent = "percent"
	KindDate    = "date"
	KindOther   = "other" // an identifier, a duration, a section number: never a quantity compared or summed
	KindUnclear = "unclear"
)

// Passage is one text the figure reader reads: a quote, a typed value or a claim, with its spans.
type Passage struct {
	ID    string // "q1", "v2", "c" …: span ids are the passage's id, ".s" and the span's number
	Text  string
	Spans []Span
}

// SpanID is the stable id of a passage's span i (from 1).
func SpanID(passage string, i int) string { return passage + ".s" + strconv.Itoa(i+1) }

// PassageOf is a passage over text with every span Literal finds in it.
func PassageOf(id, text string) Passage {
	_, ss := Literal(text)
	return Passage{ID: id, Text: text, Spans: ss}
}

// Label is the figure reader's answer for one span, as its output schema has it.
type Label struct {
	Span      string  `json:"span"`
	Kind      string  `json:"kind"`
	Currency  string  `json:"currency"` // an ISO 4217 code, "none" or "unclear"
	Scale     string  `json:"scale"`    // the multiplier, a decimal ("1", "1000", "0.01")
	ScaleFrom *string `json:"scaleFrom"`
	Decimal   string  `json:"decimal"` // ".", "," or "none": which mark is the decimal point
	Party     *string `json:"party"`
	IsRange   bool    `json:"isRange"`
	RangeWith *string `json:"rangeWith"`
	Sign      string  `json:"sign"` // positive, negative or unclear
}

// Reading is a checked label: what one span states. It is what consumers read (admission, claimSums,
// G8, the joins); Amount is exact, signed, with the scale applied ("" when it cannot be computed).
type Reading struct {
	Span      string `json:"span"`
	Text      string `json:"text"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Kind      string `json:"kind"`
	Currency  string `json:"currency,omitempty"`
	Amount    string `json:"amount,omitempty"`
	Party     string `json:"party,omitempty"`
	ScaleFrom string `json:"scaleFrom,omitempty"` // the words or span its scale comes from
	RangeWith string `json:"rangeWith,omitempty"`
	Unclear   bool   `json:"unclear,omitempty"`
	Why       string `json:"why,omitempty"` // what made it unclear
}

// Clear reports whether the reading is a quantity read with no doubt (money, a count, a percentage).
func (r Reading) Clear() bool {
	return !r.Unclear && r.Amount != "" && (r.Kind == KindMoney || r.Kind == KindCount || r.Kind == KindPercent)
}

// Rat is the reading's amount (nil when it has none).
func (r Reading) Rat() *big.Rat {
	x, ok := new(big.Rat).SetString(r.Amount)
	if !ok {
		return nil
	}
	return x
}

var codeRe = regexp.MustCompile(`^[A-Z]{3}$`)

// ReadLabels checks the reader's labels against the passages it was given and returns one reading per
// span (by span id) and notes on what was refused: a label for a span not given (rejected), a second
// label for one span (both unclear). A span with no label is unclear (not read).
func ReadLabels(ps []Passage, labels []Label) (map[string]Reading, []string) {
	type at struct {
		p Passage
		s Span
	}
	spans := map[string]at{}
	out := map[string]Reading{}
	for _, p := range ps {
		for i, s := range p.Spans {
			id := SpanID(p.ID, i)
			spans[id] = at{p, s}
			out[id] = Reading{Span: id, Text: s.Text, Start: s.Start, End: s.End, Kind: KindUnclear, Unclear: true, Why: "not read"}
		}
	}
	var notes []string
	seen := map[string]bool{}
	for _, l := range labels {
		a, ok := spans[l.Span]
		switch {
		case !ok:
			notes = append(notes, fmt.Sprintf("a label for %q was rejected: no such span was given", l.Span))
			continue
		case seen[l.Span]:
			r := out[l.Span]
			r.Unclear, r.Why = true, "labelled twice"
			out[l.Span] = r
			notes = append(notes, fmt.Sprintf("%s was labelled twice: read as unclear", l.Span))
			continue
		}
		seen[l.Span] = true
		out[l.Span] = check(a.p, a.s, l, func(id string) bool { _, ok := spans[id]; return ok })
	}
	return out, notes
}

// check turns one label into a reading, unclear wherever it cannot be checked.
func check(p Passage, s Span, l Label, known func(string) bool) Reading {
	r := Reading{Span: l.Span, Text: s.Text, Start: s.Start, End: s.End, Kind: l.Kind}
	unclear := func(why string) {
		if !r.Unclear {
			r.Unclear, r.Why = true, why
		}
	}
	if !slices.Contains([]string{KindMoney, KindCount, KindPercent, KindDate, KindOther, KindUnclear}, l.Kind) {
		r.Kind = KindUnclear
	}
	if r.Kind == KindUnclear {
		unclear("its kind is unclear")
	}
	switch {
	case l.Currency == "unclear":
		unclear("its currency is unclear")
	case r.Kind == KindMoney && codeRe.MatchString(l.Currency):
		r.Currency = l.Currency
	case r.Kind == KindMoney:
		unclear("money with no currency code")
	case l.Currency != "none" && l.Currency != "":
		unclear("a currency on a figure that is not money")
	}
	if l.Party != nil && strings.TrimSpace(*l.Party) != "" {
		r.Party = strings.Join(strings.Fields(*l.Party), " ")
	}
	if l.RangeWith != nil && *l.RangeWith != l.Span && known(*l.RangeWith) {
		r.RangeWith = *l.RangeWith
	}
	scale, ok := new(big.Rat).SetString(strings.TrimSpace(l.Scale))
	if !ok || !powerOfTen(scale) {
		unclear("its scale is not a power of ten") // a thousand, a million, a lakh: never a factor that makes up digits
		scale = big.NewRat(1, 1)
	} else if scale.Cmp(big.NewRat(1, 1)) != 0 {
		from := ""
		if l.ScaleFrom != nil {
			from = strings.TrimSpace(*l.ScaleFrom)
		}
		if from == "" || !known(from) && !strings.Contains(strings.ToLower(p.Text), strings.ToLower(from)) {
			unclear("its scale comes from nothing the passage states")
		}
		r.ScaleFrom = from
	}
	dec := map[string]byte{".": '.', ",": ','}[l.Decimal]
	if l.Decimal != "none" && l.Decimal != "" && dec == 0 {
		unclear("its decimal mark is unclear")
	}
	v, ok := s.Value(dec)
	if !ok { // the mark the reader named does not fit what is written: unclear, its amount the form's when it has one
		unclear("its decimal mark does not fit the digits as written")
		if rs := s.Readings(); len(rs) == 1 {
			v = rs[0]
		} else {
			return r
		}
	}
	v = new(big.Rat).Mul(v, scale)
	switch {
	case l.Sign == "negative" && v.Sign() > 0:
		v.Neg(v)
	case l.Sign == "positive" && s.Neg():
		unclear("read as positive, but a minus sign is written")
	case l.Sign != "negative" && l.Sign != "positive":
		unclear("its sign is unclear")
	}
	r.Amount = exactDecimal(v)
	return r
}

// powerOfTen reports whether r is 10^k for -3 ≤ k ≤ 15: the only scales a figure is read with
// (thousands, millions, lakhs, per mille), so a reading's amount is always its written digits shifted,
// never digits the text does not state.
func powerOfTen(r *big.Rat) bool {
	x := big.NewRat(1, 1000)
	for range 19 {
		if r.Cmp(x) == 0 {
			return true
		}
		x.Mul(x, big.NewRat(10, 1))
	}
	return false
}

// exactDecimal prints a terminating decimal with every digit (a figure's amount is never rounded).
func exactDecimal(r *big.Rat) string {
	for p := 0; p <= 40; p++ {
		if s := r.FloatString(p); func() bool { x, _ := new(big.Rat).SetString(s); return x.Cmp(r) == 0 }() {
			return s
		}
	}
	return r.RatString()
}

// SameFigure reports whether two readings state one figure: the same kind, currency and amount.
// unclear is true when they agree only on what can be read and one of them is unclear (a match a
// lawyer must confirm).
func SameFigure(a, b Reading) (same, unclear bool) {
	x, y := a.Rat(), b.Rat()
	if x == nil || y == nil {
		return false, false
	}
	if a.Clear() && b.Clear() {
		return a.Kind == b.Kind && a.Currency == b.Currency && x.Cmp(y) == 0, false
	}
	kinds := a.Kind == b.Kind || a.Kind == KindUnclear || b.Kind == KindUnclear
	curs := a.Currency == b.Currency || a.Currency == "" && a.Kind != b.Kind || b.Currency == "" && b.Kind != a.Kind || a.Kind == KindUnclear || b.Kind == KindUnclear
	amounts := new(big.Rat).Abs(x).Cmp(new(big.Rat).Abs(y)) == 0
	return false, kinds && curs && amounts
}

// KindOf is the reading kind a typed value's class asks for ("" for a date or any other class).
func KindOf(v Value) string {
	return map[Class]string{"Money": KindMoney, "Percentage": KindPercent, "Count": KindCount}[ParseClass(v.Class)]
}
