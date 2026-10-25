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
	"unicode"
	"unicode/utf8"
)

// The meaning half of figure reading (lab-r4 r4, owner decision): the figure-reading agent (the
// engine's read_figures) labels each span code found (Spans) with what it measures, and ReadLabels
// checks the labels mechanically before anything reads them: a label may name only a span it was
// given; a scale is a power of ten and comes from words its own passage states, whole words outside
// the span itself (never another passage, never a span id: lab-r4 r5 H1), or it is not applied (the
// figure is read at scale 1, unclear: r5 M2); a decimal mark must fit the span's form; and anything it
// cannot check, or that the agent says is unclear, is an unclear reading (low confidence: gated to a
// lawyer, never verifying by itself). The amount is exact arithmetic on the span's digits: reading ×
// scale × sign, so a reading never states digits the text does not. A scaled reading states the same
// figure as another clearly only when the other carries the same scale (SameFigure): a "$500" read
// "in thousands" never verifies a typed "$500,000" or traces a memo's by itself.

// Kinds a figure can be read as; "unclear" is always allowed.
const (
	KindMoney   = "money"
	KindCount   = "count"
	KindPercent = "percent"
	KindDate    = "date"
	// KindDuration is a period of time with its unit (Reading.Unit: DurationUnits): "21 days", "one
	// (1) year". It is a worksheet's day count or a date's offset only when read so (lab-r4 r8 H1).
	KindDuration = "duration"
	// KindNumber is a bare number or a multiple ("1.5x", "two times", a ratio): a plain operand of a
	// product, never money, a count of things or an identifier.
	KindNumber  = "number"
	KindOther   = "other" // an identifier, a section, clause or rule number: never a quantity compared, summed or counted
	KindUnclear = "unclear"
)

// DurationUnits are the units a duration is read in.
var DurationUnits = []string{"days", "business days", "weeks", "months", "years"}

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
	Sign      string  `json:"sign"`     // positive, negative or unclear
	Measures  *string `json:"measures"` // what the figure is an amount of ("initial deposit", "annual revenue")
	Period    *string `json:"period"`   // the period or date it is for ("fiscal 2023"), null when it states none
	// TypedEntity is, for a typed value's span, what the analyst's typed entity is to the figure:
	// "party" (it names the figure's party), "other" (another party: a payer, not the payee), "none"
	// (not a party at all: "Email", a table's title); null elsewhere or when it cannot tell.
	TypedEntity *string `json:"typedEntity"`
	// Counterparty is the figure's other party, when the passage names one: the payer of a payee's
	// compensation, the landlord of a tenant's rent, the recipient of a payment (null when none).
	Counterparty *string `json:"counterparty"`
	// Unit is a duration's unit (one of DurationUnits); null for every other kind.
	Unit *string `json:"unit"`
	// UnitFrom is the exact words beside the span that state a duration's unit ("days", "business
	// days", "year") or make a number a multiple ("x", "times"); null for every other kind.
	UnitFrom *string `json:"unitFrom"`
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
	Scale     string `json:"scale,omitempty"`     // the scale applied ("1000"; "" is 1)
	ScaleFrom string `json:"scaleFrom,omitempty"` // the words of its passage its scale comes from
	Measures  string `json:"measures,omitempty"`  // what it is an amount of, as the reader names it
	Period    string `json:"period,omitempty"`    // the period it is for, as the reader names it
	// TypedEntity is what the reader says the typed value's entity is to the figure (party, other,
	// none; "" when it did not say).
	TypedEntity string `json:"typedEntity,omitempty"`
	// Counterparty is the figure's other party as the reader names it ("" when none or not said).
	Counterparty string `json:"counterparty,omitempty"`
	Unit         string `json:"unit,omitempty"`     // a duration's unit (DurationUnits)
	UnitFrom     string `json:"unitFrom,omitempty"` // the words beside it its unit or multiple comes from
	RangeWith    string `json:"rangeWith,omitempty"`
	Unclear      bool   `json:"unclear,omitempty"`
	Why          string `json:"why,omitempty"` // what made it unclear
	// Stated is, for a typed value's reading, the quote span admission matched it to ("q2.s1"; ""
	// when none): where its quotes state it.
	Stated string `json:"stated,omitempty"`
}

// Clear reports whether the reading is a quantity read with no doubt (money, a count, a percentage, a
// bare number or multiple).
func (r Reading) Clear() bool {
	return !r.Unclear && r.Amount != "" && (r.Kind == KindMoney || r.Kind == KindCount || r.Kind == KindPercent || r.Kind == KindNumber)
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

// IsCurrency reports whether code is written as an ISO 4217 code is (three capital letters), as a
// reading's currency and a worksheet's money unit are.
func IsCurrency(code string) bool { return codeRe.MatchString(code) }

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
	if !slices.Contains([]string{KindMoney, KindCount, KindPercent, KindDate, KindDuration, KindNumber, KindOther, KindUnclear}, l.Kind) {
		r.Kind = KindUnclear
	}
	if r.Kind == KindUnclear {
		unclear("its kind is unclear")
	}
	u, from := "", ""
	if l.Unit != nil {
		u = *l.Unit
	}
	if l.UnitFrom != nil {
		from = strings.TrimSpace(*l.UnitFrom)
	}
	switch { // a duration's unit and a multiple's sign are a number's meaning: cited words beside it, as a scale's (lab-r4 r9 H1)
	case r.Kind == KindDuration && !slices.Contains(DurationUnits, u):
		unclear("a duration with no unit")
	case r.Kind == KindNumber && u != "":
		unclear("a unit on a figure that is no duration")
	case r.Kind == KindDuration || r.Kind == KindNumber:
		if !statesUnit(p.Text, s, from, r.Kind == KindNumber) {
			unclear("its " + map[bool]string{true: "unit", false: "multiple"}[r.Kind == KindDuration] + " is not stated by words beside it")
			break
		}
		r.Unit, r.UnitFrom = map[bool]string{true: u}[r.Kind == KindDuration], from
	case u != "":
		unclear("a unit on a figure that is no duration")
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
	if l.Counterparty != nil && strings.TrimSpace(*l.Counterparty) != "" {
		r.Counterparty = strings.Join(strings.Fields(*l.Counterparty), " ")
	}
	if l.RangeWith != nil && *l.RangeWith != l.Span && strings.HasPrefix(*l.RangeWith, p.ID+".s") && known(*l.RangeWith) { // the other end of its own passage's range
		r.RangeWith = *l.RangeWith
	}
	for _, x := range []struct {
		to *string
		l  *string
	}{{&r.Measures, l.Measures}, {&r.Period, l.Period}} {
		if x.l != nil {
			*x.to = strings.Join(strings.Fields(*x.l), " ")
		}
	}
	if l.TypedEntity != nil && slices.Contains([]string{"party", "other", "none"}, *l.TypedEntity) {
		r.TypedEntity = *l.TypedEntity
	}
	scale, ok := new(big.Rat).SetString(strings.TrimSpace(l.Scale))
	one := big.NewRat(1, 1)
	switch {
	case !ok || !powerOfTen(scale):
		unclear("its scale is not a power of ten") // a thousand, a million, a lakh: never a factor that makes up digits
		scale = one
	case scale.Cmp(one) != 0:
		from := ""
		if l.ScaleFrom != nil {
			from = strings.TrimSpace(*l.ScaleFrom)
		}
		end := s.End // a range's scale follows its far end: "$5-10 million"
		for i, x := range p.Spans {
			if r.RangeWith != "" && SpanID(p.ID, i) == r.RangeWith {
				end = max(end, x.End)
			}
		}
		if !statesScale(p.Text, s, end, from) { // never applied from nothing (lab-r4 r5 M2)
			unclear("its scale is not stated by words of its own passage")
			scale = one
			break
		}
		r.Scale, r.ScaleFrom = exactDecimal(scale), from
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

// statesScale reports whether from is words text states, outside the span s and at word boundaries,
// adjacent to the span: right after its digits, or after the currency code written there ("4.2
// million", "2810,EUR million"); earlier in its own line with no sentence end between (a lead-in:
// "costs (in thousands): 1,200"); or in the passage's first line when that is a header (headerLine)
// and the span is in a table row below it (tableRow): where a figure's scale may come from (lab-r4 r5 H1, r6
// H1, r7 M2). Not a span id, not a part of a word ("e" of "fee"), not the span's own digits, never
// another passage's words, nor words of another sentence ("Costs (in thousands): 1,200. The fee is
// $500", "$500 per application. Costs (in thousands): 1,200"); at least one letter, or
// a "000" ("($000)": a bare "$" is no scale, r7 L1). Which words are a scale is the reader's;
// SameFigure compares them (scaleWords).
func statesScale(text string, s Span, end int, from string) bool {
	if from == "" || !strings.ContainsFunc(from, unicode.IsLetter) && !strings.Contains(from, "000") {
		return false
	}
	low, f := asciiLower(text), asciiLower(from) // byte for byte, so offsets in low are offsets in text
	header := strings.IndexByte(text, '\n')      // the first line's end; -1: one line
	for i := 0; i+len(f) <= len(low); {
		k := strings.Index(low[i:], f)
		if k < 0 {
			return false
		}
		a, b := i+k, i+k+len(f)
		i = a + 1
		if a < s.End && s.Start < b { // the span itself
			continue
		}
		edge := func(r rune, word bool) bool { return !word || !isWordRune(r) }
		before := a == 0 || a == s.End || edge(lastRune(low[:a]), isWordRune(firstRune(f))) // glued to its own span: "$5M", "4.2bn"
		after := b == len(low) || b == s.Start || edge(firstRune(low[b:]), isWordRune(lastRune(f)))
		gap := strings.Trim(text[min(end, a):a], " \t(,") // after its digits (or its range's far end), or a currency code written after them: "4.2 million", "$5M", "4,000 (in thousands)", "2810,EUR million"
		adjacent := a >= end && (gap == "" || IsCurrency(gap))
		leadIn := b <= s.Start && !strings.ContainsRune(text[b:s.Start], '\n') && !sentenceEnd(text[b:s.Start])
		heading := header >= 0 && b <= header && s.Start > header && headerLine(text, header, a) && tableRow(text, s.Start)
		if before && after && (adjacent || leadIn || heading) {
			return true
		}
	}
	return false
}

// statesUnit reports whether from is words of text right beside the span s that can state a
// duration's unit or a multiple's sign (lab-r4 r9 H1, r10 L2), checked by position and shape, never by
// a word list:
//   - words right after the digits: only spaces, a closing parenthesis or a hyphen between ("21 days",
//     "thirty (30) days", "21-day"), never an opening parenthesis ("12(b)" states no unit), the
//     source starting at the first word there, of two or more letters;
//   - or, for a multiple, one sign right against the digits or a space away, either side ("1.5x",
//     "x 1.5", "× 1.5").
//
// A source with no letter (and no "×") is none, and so is a lone letter that is no multiple's sign.
// Which words state which unit is the reader's: a word that is no unit ("of" in "Section 30 of") is
// caught where the words are compared (G8's periods, durationStated) or shown (the computed figures).
func statesUnit(text string, s Span, from string, multiple bool) bool {
	f := asciiLower(strings.TrimSpace(from))
	sign := utf8.RuneCountInString(f) == 1
	if f == "" || !strings.ContainsFunc(f, unicode.IsLetter) && f != "×" || sign && !multiple {
		return false
	}
	low := asciiLower(text)
	if sign { // "1.5x", "1.5 x", "x 1.5", "×1.5"
		after, before := strings.TrimPrefix(low[s.End:], " "), strings.TrimSuffix(low[:s.Start], " ")
		edge := func(x string) bool { r, _ := utf8.DecodeRuneInString(x); return x == "" || !isWordRune(r) }
		return strings.HasPrefix(after, f) && edge(after[len(f):]) || strings.HasSuffix(before, f) && edge(lastString(before[:len(before)-len(f)]))
	}
	rest := strings.TrimLeft(low[s.End:], " \t)-")
	if !strings.HasPrefix(rest, f) || strings.ContainsRune(low[s.End:len(low)-len(rest)], '(') {
		return false
	}
	first := strings.FieldsFunc(f, func(r rune) bool { return !unicode.IsLetter(r) })
	tail := rest[len(f):]
	r, _ := utf8.DecodeRuneInString(tail)
	return len(first) > 0 && utf8.RuneCountInString(first[0]) >= 2 && strings.IndexFunc(f, unicode.IsLetter) == 0 && (tail == "" || !isWordRune(r))
}

func lastString(x string) string {
	if x == "" {
		return ""
	}
	_, n := utf8.DecodeLastRuneInString(x)
	return x[len(x)-n:]
}

// headerLine reports whether a passage's first line (ending at end) is a header whose words at a give
// the rows below it their scale (lab-r4 r8 M2): no sentence in it (no sentence end, not ending in a
// full stop), and the words not a figure's own scale on that line ("grew to $4.2 million").
func headerLine(text string, end, a int) bool {
	line := text[:end]
	if sentenceEnd(line+" ") || strings.HasSuffix(strings.TrimSpace(line), ".") {
		return false
	}
	_, ss := Literal(line)
	return !slices.ContainsFunc(ss, func(n Span) bool {
		gap := strings.Trim(line[min(n.End, a):a], " \t(,")
		return n.End <= a && (gap == "" || IsCurrency(gap))
	})
}

// tableRow reports whether the line holding offset at is a table's row (lab-r4 r8 M2, r9 L3): it and
// a row next to it (not the passage's first line, the header) split into cells the same way, by as
// many tabs, as many "|", or as many commas (two or more), neither of them a sentence.
func tableRow(text string, at int) bool {
	lines := strings.Split(text, "\n")
	n := strings.Count(text[:at], "\n")
	prose := func(l string) bool { return sentenceEnd(l+" ") || strings.HasSuffix(strings.TrimSpace(l), ".") }
	cells := func(a, b string) bool {
		for _, sep := range []string{"\t", "|"} {
			if k := strings.Count(a, sep); k > 0 && k == strings.Count(b, sep) {
				return true
			}
		}
		k := strings.Count(a, ",")
		return k >= 2 && k == strings.Count(b, ",") && !prose(a) && !prose(b)
	}
	for _, m := range []int{n - 1, n + 1} {
		if m >= 1 && m < len(lines) && n >= 1 && cells(lines[n], lines[m]) {
			return true
		}
	}
	return false
}

// sentenceEnd reports whether text holds the end of a sentence: a full stop, question or exclamation
// mark followed by a space (not a decimal point, "1.5").
func sentenceEnd(text string) bool {
	for i := 0; i+1 < len(text); i++ {
		if strings.ContainsRune(".?!", rune(text[i])) && unicode.IsSpace(rune(text[i+1])) {
			return true
		}
	}
	return false
}

func firstRune(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }

// asciiLower is s with its ASCII capitals lowered, every other byte as it is (its length unchanged).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
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

// exactDecimal prints a terminating decimal with every digit (a figure's amount is never rounded):
// its places are the larger power of 2 or 5 in its denominator; a non-terminating one is a fraction.
func exactDecimal(r *big.Rat) string {
	d := new(big.Int).Set(r.Denom())
	places := 0
	for _, f := range []int64{2, 5} {
		n, m := 0, new(big.Int)
		for q, rem := new(big.Int).QuoRem(d, big.NewInt(f), m); rem.Sign() == 0; q, rem = new(big.Int).QuoRem(d, big.NewInt(f), m) {
			d, n = q, n+1
		}
		places = max(places, n)
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return r.RatString()
	}
	return r.FloatString(places)
}

// SameFigure reports whether two readings state one figure: the same kind, currency and amount, at
// the same scale from equivalent words (a "$500" read in thousands is $500,000 only beside another
// figure read in thousands from words that say so, never beside a "$500,000", and never when one
// side's scale came from "fee": lab-r4 r5 H1, r6 H1). unclear is true when they agree only on what can be read and one of them is unclear,
// or on their amounts at two scales (a match a lawyer must confirm).
func SameFigure(a, b Reading) (same, unclear bool) {
	x, y := a.Rat(), b.Rat()
	if x == nil || y == nil {
		return false, false
	}
	if a.Clear() && b.Clear() {
		same = a.Kind == b.Kind && a.Currency == b.Currency && x.Cmp(y) == 0
		if same && (a.Scale != b.Scale || a.Scale != "" && !sameScaleWords(a.ScaleFrom, b.ScaleFrom)) {
			return false, true // one amount at two scales, or one scale from two different words: the same figure only if a lawyer says so
		}
		return same, false
	}
	kinds := a.Kind == b.Kind || a.Kind == KindUnclear || b.Kind == KindUnclear
	curs := a.Currency == b.Currency || a.Currency == "" && a.Kind != b.Kind || b.Currency == "" && b.Kind != a.Kind || a.Kind == KindUnclear || b.Kind == KindUnclear
	amounts := new(big.Rat).Abs(x).Cmp(new(big.Rat).Abs(y)) == 0
	return false, kinds && curs && amounts
}

// scaleWords are a scale source's words as two texts may write one scale: case-folded, letters only
// (punctuation, parentheses, "$" and digits dropped), a simple plural folded ("(In Millions)" → in,
// million). No word is known to be a scale: two sources are compared only with each other.
func scaleWords(from string) []string {
	ws := strings.FieldsFunc(strings.ToLower(from), func(r rune) bool { return !unicode.IsLetter(r) })
	for i, w := range ws {
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") {
			ws[i] = w[:len(w)-1]
		}
	}
	return ws
}

// sameScaleWords reports whether two scale sources write one scale: the same folded words, or the
// words of one all among the other's ("(In Millions)" and "million"); none on both sides ("($000)"
// and "$000") is the same, none on one side never is.
func sameScaleWords(a, b string) bool {
	x, y := scaleWords(a), scaleWords(b)
	if len(x) > len(y) {
		x, y = y, x
	}
	if len(x) == 0 {
		bare := func(s string) string {
			return strings.Map(func(r rune) rune {
				return map[bool]rune{true: -1, false: r}[unicode.IsSpace(r) || strings.ContainsRune("()[]", r)]
			}, s)
		}
		return len(y) == 0 && bare(a) == bare(b)
	}
	for _, w := range x {
		if !slices.Contains(y, w) {
			return false
		}
	}
	return true
}

// KindOf is the reading kind a typed value's class asks for ("" for a date or any other class).
func KindOf(v Value) string {
	return map[Class]string{"Money": KindMoney, "Percentage": KindPercent, "Count": KindCount}[ParseClass(v.Class)]
}
