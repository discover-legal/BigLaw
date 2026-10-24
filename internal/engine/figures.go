// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/discover-legal/BigLaw/internal/analysis"
)

// The figure parser (lab-r4 r3, coordinator decision): one small, explicit grammar that every
// figure check reads text through (typed values, claimSums, G8, the committee's statesValue and
// coverage's addressed), specified by the corpus in figures_test.go.
//
//	figure   = [sign | "("] [currency] [sign] number [scale] [percent | currency] [")"]
//	number   = digits with grouping or decimal separators, read by readNumber (never stripped blindly)
//	scale    = million | billion | trillion | thousand | mn | bn | mln | bln | mio,
//	           and m | mm | k | b only right after a currency ("$5mm"; a bare "5 mm" is a measurement)
//	currency = a sign or ISO code before the number, or a sign, code or currency phrase after it
//	range    = figure ("-" | "–" | "—" | "to") figure, or "between" figure "and" figure: the second
//	           end's scale (and currency, percent, noun) carries back to the first, the first's
//	           currency forward to the second, and both ends are figures
//
// A figure's identity is its class, currency, sign and amount: a value typed in one currency never
// verifies from another, a loss never from a gain, a sum never from a count.

// figClass is what a figure measures.
type figClass uint8

const (
	unknownFig figClass = iota // a bare number: no currency, percent or count noun
	moneyFig                   // a currency or currency word is stated
	countFig                   // a count noun follows ("3,000,000 shares", "5 mm", "30 days")
	percentFig                 // "%", "per cent", "percent", "pct", "percentage points"
)

func (c figClass) String() string { return [...]string{"unknown", "money", "count", "percent"}[c] }

// figure is one number a text states, read with what it stands for.
type figure struct {
	amount     *big.Rat // the magnitude, scale applied
	neg        bool     // a leading sign, or accounting parentheses
	cur        string   // ISO code when a currency is stated; "" otherwise
	class      figClass //
	scaled     bool     // a scale word is stated (or carried from a range's other end)
	grouped    bool     // its thousands are grouped ("4,217", "1.200.000")
	amb        bool     // the separators read two ways (grouping or a decimal comma), or two currencies
	single     bool     // one bare digit, unscaled ("paragraph 3")
	noun       string   // the count noun, when it is a count
	start, end int      // its span in the text: sign, currency and scale included

	raw    *big.Rat // the number before its scale (a range carries a scale only upward)
	mul    int64    // the scale applied (1: none)
	hyphen bool     // its sign is a hyphen right before it, which a range may read as the separator
}

// String is a figure's identity (class, sign, amount, currency), as the corpus test writes it.
func (f figure) String() string {
	s := f.class.String() + ":" + map[bool]string{true: "-"}[f.neg] + analysis.Decimal(f.amount)
	if f.cur != "" {
		s += " " + f.cur
	}
	if f.amb {
		s += " (ambiguous)"
	}
	return s
}

var (
	numRe = regexp.MustCompile(`\d+(?:[.,]\d+)*`)
	// scaleRe reads a scale word after a number; scaleCurRe the letters read as one only after a currency.
	scaleRe    = regexp.MustCompile(`(?i)^[ \x{00a0}]?(million|billion|trillion|thousand|mn|bn|mln|bln|mio)\b`)
	scaleCurRe = regexp.MustCompile(`(?i)^[ \x{00a0}]?(mm|bn|m|k|b)\b`)
	pctRe      = regexp.MustCompile(`(?i)^[ \x{00a0}]?(?:%|per[ \x{00a0}]?cent\b|percent\b|pct\b|percentage points?\b)`)
	// curPhrase is a currency stated in words, longest first; curWordRe is the same anywhere in a text.
	curPhrase  = `u\.\s?s\.\s?dollars?|us dollars?|united states dollars?|canadian dollars?|australian dollars?|new zealand dollars?|hong kong dollars?|singapore dollars?|dollars?|euros?|pounds sterling|pounds?|sterling|yen|swiss francs?|rupees?`
	curAfterRe = regexp.MustCompile(`(?i)^[ \x{00a0}]?(?:` + curPhrase + `)\b`)
	curWordRe  = regexp.MustCompile(`(?i)\b(?:` + curPhrase + `)\b`)
	codeRe     = regexp.MustCompile(`\b(USD|EUR|GBP|CAD|AUD|CHF|JPY|NZD|SGD|HKD|INR|CNY|SEK|NOK|DKK|ZAR|MXN|BRL)\b`)
	// ofRe joins a figure to the count it is part of: "1,200,000 of the Company's 3,000,000 Shares".
	ofRe = regexp.MustCompile(`^\s+(?:out\s+)?of\s+(?:(?:the|its|their|his|her|our|such|all|a|an|[\p{L}][\p{L}\-.&]*['’]s)\s+){0,4}$`)
)

// curSigns are the signs written before (or after) a number, longest first.
var curSigns = [][2]string{{"U.S.$", "USD"}, {"US$", "USD"}, {"CA$", "CAD"}, {"AU$", "AUD"}, {"NZ$", "NZD"}, {"HK$", "HKD"}, {"S$", "SGD"},
	{"C$", "CAD"}, {"A$", "AUD"}, {"$", "USD"}, {"＄", "USD"}, {"£", "GBP"}, {"€", "EUR"}, {"¥", "JPY"}, {"₹", "INR"}}

// wordCur maps a currency phrase's words to its code.
func wordCur(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	switch {
	case strings.HasPrefix(s, "canadian"):
		return "CAD"
	case strings.HasPrefix(s, "australian"):
		return "AUD"
	case strings.HasPrefix(s, "new zealand"):
		return "NZD"
	case strings.HasPrefix(s, "hong kong"):
		return "HKD"
	case strings.HasPrefix(s, "singapore"):
		return "SGD"
	case strings.Contains(s, "dollar"):
		return "USD"
	case strings.HasPrefix(s, "euro"):
		return "EUR"
	case strings.HasPrefix(s, "pound"), s == "sterling":
		return "GBP"
	case s == "yen":
		return "JPY"
	case strings.Contains(s, "franc"):
		return "CHF"
	case strings.HasPrefix(s, "rupee"):
		return "INR"
	}
	return ""
}

var scaleOf = map[string]int64{"thousand": 1e3, "k": 1e3, "million": 1e6, "mn": 1e6, "mln": 1e6, "mio": 1e6, "m": 1e6, "mm": 1e6,
	"billion": 1e9, "bn": 1e9, "bln": 1e9, "b": 1e9, "trillion": 1e12}

// countNouns are the nouns of a count (singular; a plural "s"/"es" is read too): units of things,
// time and measure. A closed list: any other word after a bare number leaves it unknown.
var countNouns = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`share unit trade transaction day week month year hour minute quarter employee person persons people
		individual account customer client member director officer patient child children document page item copy copies option warrant
		security securities contract invoice payment instalment installment claim vote point bp bps lot parcel vehicle row entry entries
		record email call visit location store site property properties building home apartment bed physician doctor case incident
		violation defendant plaintiff shareholder stockholder holder owner partner worker student tenant room acre hectare mile kilometre
		kilometer km metre meter foot feet inch inches mm cm kg lb lbs ton tonne litre liter gallon barrel kwh mwh kw mw sq square m ft
		piece order user subscriber seat license licence count`) {
		countNouns[w] = true
	}
}

func isCountNoun(w string) bool {
	return countNouns[w] || countNouns[strings.TrimSuffix(w, "s")] || countNouns[strings.TrimSuffix(w, "es")]
}

// nounStop are the words that end the look for a count noun after a number ("1,200,000 to the
// Seller", "3,000,000 in cash", "500 per share").
var nounStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`and or to in on at by for from as per plus less minus was were is are will shall be been being has have had
		paid payable due each net gross which that under with over into upon if when after before within against not than but so then may
		must would should could can including inclusive exclusive representing annually monthly weekly yearly daily`) {
		nounStop[w] = true
	}
}

// figuresIn reads every figure in text, dates first set aside (their digits are no figures). The
// separator convention is read per line: a line that writes a decimal comma ("3,5", "1,5 %") or
// dot grouping ("1.200.000") makes its "1,200" ambiguous.
func figuresIn(text string) (dateKeys []string, out []figure) {
	ds, spans := dates(text)
	masked := []byte(text)
	for _, loc := range spans {
		for i := loc[0]; i < loc[1]; i++ {
			masked[i] = ' '
		}
	}
	t := string(masked)
	for off := 0; off <= len(t); {
		n := strings.IndexByte(t[off:], '\n')
		if n < 0 {
			n = len(t) - off
		}
		for _, f := range lineFigures(t[off : off+n]) {
			f.start, f.end = f.start+off, f.end+off
			out = append(out, f)
		}
		off += n + 1
	}
	return ds, out
}

// euroLine reports whether a line writes the continental convention somewhere: a decimal comma or
// dot grouping.
func euroLine(line string, toks [][]int) bool {
	for _, m := range toks {
		s := line[m[0]:m[1]]
		dots, commas := strings.Count(s, "."), strings.Count(s, ",")
		switch {
		case commas == 1 && dots == 0:
			if l := len(s) - strings.IndexByte(s, ',') - 1; l != 3 {
				return true
			}
		case commas == 0 && dots > 1 && dotGrouped(s):
			return true
		case commas > 0 && dots > 0 && strings.LastIndexByte(s, ',') > strings.LastIndexByte(s, '.'):
			return true
		}
	}
	return false
}

func dotGrouped(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts[0]) > 3 {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) != 3 {
			return false
		}
	}
	return true
}

// readNumber reads digits and separators: a comma before exactly three digits groups ("1,200";
// ambiguous on a line that writes decimal commas), before one, two or four or more is a decimal
// comma ("3,5"); several dots in groups of three group ("1.200.000"); one dot is a decimal point
// ("1.200" with a currency and no scale, or on a continental line, is ambiguous); with both, the
// last separator is the decimal one. ok is false for an outline number ("7.3.1").
func readNumber(s string, euro, cur, scaled bool) (r *big.Rat, grouped, amb, ok bool) {
	dots, commas := strings.Count(s, "."), strings.Count(s, ",")
	plain := s
	switch {
	case dots == 0 && commas == 0:
	case dots > 0 && commas > 0:
		last := max(strings.LastIndexByte(s, '.'), strings.LastIndexByte(s, ','))
		dec, grp := s[last], byte(',')
		if dec == ',' {
			grp = '.'
		}
		head := s[:last]
		if strings.IndexByte(head, dec) >= 0 || !groupedBy(head, grp) {
			amb = true
		}
		plain, grouped = strings.ReplaceAll(head, string(grp), "")+"."+s[last+1:], true
	case commas > 0:
		parts := strings.Split(s, ",")
		if commas == 1 && len(parts[1]) != 3 {
			plain = parts[0] + "." + parts[1] // a decimal comma
			break
		}
		if !groupedBy(s, ',') {
			amb = true
		} else if euro {
			amb = true // "1,200" on a line that writes decimal commas
		}
		plain, grouped = strings.ReplaceAll(s, ",", ""), true
	case dots == 1:
		if l := len(s) - strings.IndexByte(s, '.') - 1; l == 3 && (euro || cur) && !scaled {
			amb = true // "€1.200": one euro twenty, or twelve hundred
		}
	default: // several dots
		if !dotGrouped(s) {
			return nil, false, false, false
		}
		plain, grouped = strings.ReplaceAll(s, ".", ""), true
	}
	r, ok = new(big.Rat).SetString(plain)
	return r, grouped, amb, ok
}

// groupedBy reports whether s is digits grouped by sep in threes ("1,200,000").
func groupedBy(s string, sep byte) bool {
	parts := strings.Split(s, string(sep))
	if len(parts[0]) == 0 || len(parts[0]) > 3 {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) != 3 {
			return false
		}
	}
	return true
}

func isWordByte(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }

// lineFigures parses one line.
func lineFigures(line string) []figure {
	toks := numRe.FindAllStringIndex(line, -1)
	euro := euroLine(line, toks)
	var out []figure
	for _, m := range toks {
		if f, ok := parseAt(line, m[0], m[1], euro); ok {
			out = append(out, f)
		}
	}
	ranges(line, out)
	for i := len(out) - 1; i >= 0; i-- { // "N of the Company's M shares": N counts what M counts
		if f := &out[i]; f.class == unknownFig && i+1 < len(out) && out[i+1].class == countFig && ofRe.MatchString(line[f.end:out[i+1].start]) {
			f.class, f.noun = countFig, out[i+1].noun
		}
	}
	return out
}

// parseAt reads the figure whose digits are line[s:e].
func parseAt(line string, s, e int, euro bool) (figure, bool) {
	if s > 0 && unicode.IsLetter(lastRune(line[:s])) && codeBefore(line[:s]) == "" {
		return figure{}, false // an identifier's digits ("F12", "Rule 10b5")
	}
	f := figure{start: s, mul: 1}
	paren := -1
	signAt := -1
	j := s
	for k := 0; k < 3; k++ { // peel a sign, a parenthesis and a currency off the left, in any order
		j0 := j
		switch {
		case signAt < 0 && (strings.HasSuffix(line[:j], "-") || strings.HasSuffix(line[:j], "−")):
			l := 1
			if strings.HasSuffix(line[:j], "−") {
				l = len("−")
			}
			if j-l > 0 && isWordByte(lastRune(line[:j-l])) {
				break // a hyphen inside a word or a range ("10b-5", "3-5")
			}
			signAt, j = j-l, j-l
		case paren < 0 && strings.HasSuffix(line[:j], "("):
			paren, j = j-1, j-1
		case f.cur == "":
			if c, at := currencyBefore(line[:j]); c != "" {
				f.cur, j = c, at
			}
		}
		if j == j0 {
			break
		}
	}
	f.start = j
	f.neg = signAt >= 0
	f.hyphen = signAt >= 0 && line[signAt] == '-' && signAt == j
	end := e
	prefixCur := f.cur != ""
	if m := scaleRe.FindString(line[end:]); m != "" {
		f.mul, end = scaleOf[strings.ToLower(strings.Trim(m, " \u00a0"))], end+len(m)
		if c, _ := currencyAfter(line[min(end+1, len(line)):]); c != "" && len(m) <= 4 && strings.HasPrefix(line[end:], ".") {
			end++ // "3,5 Mio. EUR": the abbreviation's full stop
		}
	} else if m := scaleCurRe.FindString(line[end:]); m != "" && prefixCur {
		f.mul, end = scaleOf[strings.ToLower(strings.Trim(m, " \u00a0"))], end+len(m)
	}
	f.scaled = f.mul > 1
	pct := false
	if m := pctRe.FindString(line[end:]); m != "" {
		pct, end = true, end+len(m)
	} else if c, n := currencyAfter(line[end:]); c != "" {
		switch {
		case f.cur == "" || f.cur == "USD" && strings.HasSuffix(line[f.start:s], "$") && !strings.HasSuffix(line[f.start:s], "US$"):
			f.cur = c // "$3,000 CAD": the code names which dollar
		case f.cur != c:
			f.amb = true
		}
		end += n
	}
	r, grouped, amb, ok := readNumber(line[s:e], euro, f.cur != "", f.scaled)
	if !ok {
		return figure{}, false
	}
	f.raw, f.grouped, f.amb = r, grouped, f.amb || amb
	f.amount = new(big.Rat).Mul(r, new(big.Rat).SetInt64(f.mul))
	if paren >= 0 { // accounting parentheses: "(3,000)" in a statement's column, never "Dollars ($3,000,000)"
		if end < len(line) && line[end] == ')' {
			end++
			if accounting(line[:paren]) && (f.grouped || f.cur != "" || strings.ContainsAny(line[s:e], ".,")) {
				f.neg = true
			}
		}
		if f.start > paren && signAt < paren {
			f.start = paren
		}
	}
	f.end = end
	switch {
	case pct:
		f.class = percentFig
	case f.cur != "":
		f.class = moneyFig
	default:
		if w := nounAfter(line[end:]); w != "" { // "3,000,000 shares", "5 mm", "2m shares"
			f.class, f.noun = countFig, w
		}
	}
	f.single = e-s == 1 && !f.scaled
	return f, true
}

// accounting reports whether a "(" opens a statement's figure: at a line or cell start, after a
// colon, bar or equals sign, or after column spacing.
func accounting(before string) bool {
	t := strings.TrimRight(before, " ")
	return t == "" || strings.HasSuffix(before, "\t") || strings.HasSuffix(before, "  ") || strings.ContainsAny(t[len(t)-1:], ":|=")
}

// currencyBefore is a sign or code right before a number (one space allowed), and where it starts.
func currencyBefore(s string) (string, int) {
	t := strings.TrimSuffix(strings.TrimSuffix(s, " "), "\u00a0")
	for _, c := range curSigns {
		if strings.HasSuffix(t, c[0]) {
			at := len(t) - len(c[0])
			if len(c[0]) > 1 && at > 0 && unicode.IsLetter(lastRune(t[:at])) {
				continue // "XUS$"
			}
			return c[1], at
		}
	}
	if c := codeBefore(t); c != "" {
		return c, len(t) - 3
	}
	return "", 0
}

// codeBefore is an ISO code that ends s, on a word boundary.
func codeBefore(s string) string {
	if len(s) < 3 {
		return ""
	}
	if c := s[len(s)-3:]; codeRe.MatchString(c) && (len(s) == 3 || !isWordByte(lastRune(s[:len(s)-3]))) {
		return c
	}
	return ""
}

// currencyAfter is a sign, code or currency phrase right after a number, and its length.
func currencyAfter(s string) (string, int) {
	if m := curAfterRe.FindString(s); m != "" {
		return wordCur(m), len(m)
	}
	t := strings.TrimPrefix(strings.TrimPrefix(s, " "), "\u00a0")
	skip := len(s) - len(t)
	for _, c := range curSigns {
		if strings.HasPrefix(t, c[0]) && len(c[0]) <= 3 && !strings.ContainsAny(c[0][:1], "UCANHS") {
			if r, _ := utf8.DecodeRuneInString(t[len(c[0]):]); !unicode.IsDigit(r) { // "3 $5" is two figures
				return c[1], skip + len(c[0])
			}
		}
	}
	if len(t) >= 3 && codeRe.MatchString(t[:3]) && (len(t) == 3 || !isWordByte(rune(t[3]))) {
		return t[:3], skip + 3
	}
	return "", 0
}

// nounAfter is the count noun within the next four words after a number, before a stop word, a
// mark or a line end ("" when none): "3,000,000 ordinary shares", "(30) days", "37 of the trades".
func nounAfter(s string) string {
	s = strings.TrimPrefix(s, ")")
	for k := 0; k < 4; k++ {
		t := strings.TrimLeft(s, " \t\u00a0")
		i := strings.IndexFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && r != '-' && r != '\'' && r != '’' })
		if i < 0 {
			i = len(t)
		}
		if i == 0 {
			return ""
		}
		w := strings.ToLower(t[:i])
		w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
		if isCountNoun(w) {
			return w
		}
		if nounStop[w] {
			return ""
		}
		s = t[i:]
	}
	return ""
}

// ranges reads "$3–5 million", "$3 to $5 million" and "between $3 and $5 million": the second end's
// scale, suffix currency, percent and noun carry back to the first; the first's currency forward.
func ranges(line string, fs []figure) {
	for i := 0; i+1 < len(fs); i++ {
		a, b := &fs[i], &fs[i+1]
		btw := strings.ToLower(strings.TrimSpace(line[a.end:b.start]))
		if btw == "" && b.hyphen {
			btw, b.neg = "-", false // "3 -5": the hyphen is the range's
		}
		between := strings.HasSuffix(strings.ToLower(strings.TrimRight(line[:a.start], " ")), "between")
		if !(btw == "-" || btw == "–" || btw == "—" || btw == "to" || btw == "and" && between) || a.amb || b.amb || a.neg != b.neg ||
			a.class != unknownFig && b.class != unknownFig && a.class != b.class {
			continue
		}
		amount := a.amount
		if a.mul == 1 && b.mul > 1 && a.class != percentFig {
			amount = new(big.Rat).Mul(a.raw, new(big.Rat).SetInt64(b.mul))
		}
		if amount.Cmp(b.amount) > 0 || a.cur != "" && b.cur != "" && a.cur != b.cur {
			continue // a range ascends in one currency ("pay $500 to 3 creditors" is none)
		}
		if a.mul == 1 && b.mul > 1 && a.class != percentFig {
			a.mul, a.scaled, a.single, a.amount = b.mul, true, false, amount
		}
		switch {
		case a.cur == "" && b.cur != "":
			a.cur, a.class = b.cur, moneyFig
		case b.cur == "" && a.cur != "":
			b.cur, b.class = a.cur, moneyFig
		}
		if a.class == unknownFig && (b.class == percentFig || b.class == countFig) {
			a.class, a.noun = b.class, b.noun
		}
	}
}

// currenciesNamed are the currencies a text names anywhere: signs, codes and currency words.
func currenciesNamed(text string) map[string]bool {
	out := map[string]bool{}
	for _, c := range curSigns {
		if strings.Contains(text, c[0]) {
			out[c[1]] = true
		}
	}
	for _, m := range codeRe.FindAllString(text, -1) {
		out[m] = true
	}
	for _, m := range curWordRe.FindAllString(text, -1) {
		out[wordCur(m)] = true
	}
	return out
}

// admits reports whether quote figure q states the typed figure want of class cl (the admission
// rule, fail-safe both ways): the same class, currency, sign and amount; a typed sum may also come
// from a bare figure that is grouped or scaled, has no count noun, in a quote that names no other
// currency ("1,200,000 annually"). An ambiguous figure states nothing. why says what differed.
func admits(want figure, cl figClass, q figure, named map[string]bool) (cur string, ok bool, why string) {
	if q.amount.Cmp(want.amount) != 0 {
		return "", false, ""
	}
	if q.amb {
		return "", false, fmt.Sprintf("the quote's %q reads two ways (grouping or a decimal comma, or two currencies)", analysis.Decimal(q.amount))
	}
	if q.neg != want.neg {
		return "", false, "the quote states it with the other sign"
	}
	switch {
	case cl == moneyFig && q.class == moneyFig:
		if want.cur != "" && q.cur != want.cur {
			return "", false, "the quote states it in " + q.cur
		}
		return q.cur, true, ""
	case cl == moneyFig && q.class == unknownFig:
		if !q.grouped && !q.scaled {
			return "", false, "the quote states only a bare number"
		}
		c := want.cur
		for k := range named {
			if c != "" && k != c || c == "" && len(named) > 1 {
				return "", false, "the quote names another currency (" + k + ")"
			}
			c = k
		}
		return c, true, ""
	case cl == q.class:
		return "", true, ""
	}
	return "", false, "the quote states it as a " + q.class.String() + cmpStr(q.noun != "", " ("+q.noun+")", "")
}

// classOf is a typed value's figure class.
func classOf(v analysis.Value) figClass {
	return map[analysis.Class]figClass{"Money": moneyFig, "Percentage": percentFig, "Count": countFig}[analysis.ClassOf(v)]
}

// unitCur is a money value's stated unit as a currency code ("" when it names none).
func unitCur(unit string) string {
	u := strings.TrimSpace(unit)
	if codeRe.MatchString(strings.ToUpper(u)) && len(u) == 3 {
		return strings.ToUpper(u)
	}
	for _, c := range curSigns {
		if u == c[0] {
			return c[1]
		}
	}
	return wordCur(u)
}

// valueInQuote reports whether a typed value is stated in the quotes (valueStated).
func valueInQuote(v analysis.Value, quotes string) bool {
	ok, _, _ := valueStated(v, quotes)
	return ok
}

// valueStated checks a typed value against its verified quotes. A quantity (money, a count, a
// percentage) is read by the figure parser and needs a quote figure that admits it; cur is the
// matched figure's currency. A date or any other value needs its text, or its figures (covered).
// why says why a value is not admitted.
func valueStated(v analysis.Value, quotes string) (ok bool, cur, why string) {
	if strings.TrimSpace(v.Value) == "" {
		return false, "", "it is empty"
	}
	text := strings.Contains(strings.ToLower(norm(quotes)), strings.ToLower(norm(v.Value)))
	cl := classOf(v)
	if cl == unknownFig { // a date, a name, an identifier
		if text || covered(v.Value, quotes) {
			return true, "", ""
		}
		return false, "", "its quotes do not state it"
	}
	_, vf := figuresIn(v.Value)
	if len(vf) != 1 || vf[0].amb || vf[0].class != unknownFig && vf[0].class != cl {
		if len(vf) == 0 && text { // "three million dollars" stated as written
			return true, "", ""
		}
		return false, "", "it is not one figure of its class"
	}
	want := vf[0]
	if want.cur == "" && cl == moneyFig {
		want.cur = unitCur(v.Unit)
	}
	if cl != moneyFig && want.cur != "" {
		return false, "", "a " + cl.String() + " with a currency"
	}
	_, qs := figuresIn(quotes)
	named := currenciesNamed(quotes)
	why = "no figure of " + cmpStr(want.neg, "-", "") + analysis.Decimal(want.amount) + " in its quotes"
	for _, q := range qs {
		c, ok, w := admits(want, cl, q, named)
		if ok {
			return true, c, ""
		}
		if w != "" {
			why = w
		}
	}
	return false, "", why
}

// traces reports whether ground figure g traces memo figure m (G8, coverage, the committee): the
// same amount, compatible classes (unknown is compatible with any) and currencies (when both state
// one), neither ambiguous. The sign is not compared: prose states "(3,000)" as "a loss of $3,000".
func traces(m, g figure) bool {
	return !m.amb && !g.amb && m.amount.Cmp(g.amount) == 0 && (m.class == g.class || m.class == unknownFig || g.class == unknownFig) &&
		(m.cur == "" || g.cur == "" || m.cur == g.cur)
}

// figureStated reports whether any of fs traces m.
func figureStated(m figure, fs []figure) bool {
	return slices.ContainsFunc(fs, func(g figure) bool { return traces(m, g) })
}
