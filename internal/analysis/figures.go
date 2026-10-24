// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	_ "embed"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The figure parser (lab-r4 r3, coordinator decision; moved here in r4 so that it is the only
// number reader: typed values, claimSums, G8, the committee, coverage, Canon, Join, Calc and
// Salience all read text through it), specified by the corpus in figures_test.go.
//
//	figure   = [sign | "(" | minus | negative] [currency] [sign] [ "(" ] number [scale] [percent | currency] ["-"] [")"]
//	number   = digits with grouping or decimal separators, read by readNumber (never stripped blindly):
//	           "," "." group or mark decimals by their own form; "'" "’", NBSP, thin spaces and (beside a
//	           currency) plain spaces only group; lakh groups ("1,00,000") group for rupees or no currency;
//	           a leading "." is a decimal (".5%"); other scripts' digits are read as ASCII
//	scale    = million | billion | trillion | thousand | mn | bn | mln | bln | mio | mrd | lakh | crore …,
//	           and m | mm | k | b only right after a currency ("$5mm"; a bare "5 mm" is a measurement)
//	currency = a sign (glued prefixes "CAD$", "R$", "NT$" …), an ISO 4217 code, or a currency word;
//	           a currency-like token the parser does not know makes the figure ambiguous, never USD
//	range    = figure ("-" | "–" | "—" | "to") figure, or "between" figure "and" figure: the second
//	           end's scale (and currency, percent, noun) carries back to the first, the first's
//	           currency forward to the second, and both ends are figures
//	words    = written-out numbers ("three million dollars", "five per cent") with a scale, currency or percent
//
// A figure's identity is its class, currency, sign and amount: a value typed in one currency never
// verifies from another, a loss never from a gain, a sum never from a count. A currency token
// belongs to one figure only, so spans never overlap.

// FigClass is what a figure measures.
type FigClass uint8

const (
	UnknownFig FigClass = iota // a bare number: no currency, percent or count noun
	MoneyFig                   // a currency or currency word is stated
	CountFig                   // a count noun follows or labels it ("3,000,000 shares", "Shares outstanding: 3,000,000")
	PercentFig                 // "%", "per cent", "percent", "pct", "percentage points", "‰"
)

func (c FigClass) String() string { return [...]string{"unknown", "money", "count", "percent"}[c] }

// Figure is one number a text states, read with what it stands for.
type Figure struct {
	Amount     *big.Rat // the magnitude, scale applied
	Neg        bool     // a leading or trailing sign, a minus word, or accounting parentheses
	Cur        string   // ISO code (or "kr") when a currency is stated; "" otherwise
	Class      FigClass //
	Scaled     bool     // a scale word is stated (or carried from a range's other end, or a table header)
	Grouped    bool     // its thousands are grouped ("4,217", "1.200.000", "1'200'000", "1,00,000")
	Amb        bool     // it reads two ways; Why says how
	Why        string   // why it is ambiguous
	Single     bool     // one bare digit, unscaled ("paragraph 3")
	Noun       string   // the count noun, when it is a count
	Start, End int      // its span in the text: sign, currency and scale included

	raw              *big.Rat // the number before its scale (a range carries a scale only upward)
	mul              int64    // the scale applied (1: none)
	hyphen           bool     // its sign is a dash before it, which a range may read as the separator
	numStart, numEnd int      // the digits' span
	preAt, preEnd    int      // the prefix currency's span (-1: none)
	sufAt            int      // where the suffix currency starts (-1: none)
	preSign          bool     // the prefix currency is a sign, not a code or word
}

// String is a figure's identity (class, sign, amount, currency), as the corpus test writes it.
func (f Figure) String() string {
	s := f.Class.String() + ":" + map[bool]string{true: "-"}[f.Neg] + Decimal(f.Amount)
	if f.Cur != "" {
		s += " " + f.Cur
	}
	if f.Amb {
		s += " (ambiguous)"
	}
	return s
}

// Render writes a figure in a canonical form the parser reads back to the same identity (the
// property test's parse → render → parse).
func (f Figure) Render() string {
	s := exact(f.Amount)
	switch f.Class {
	case PercentFig:
		s += "%"
	case CountFig:
		if isNoun(countNouns, f.Noun) {
			s += " " + f.Noun
		} else {
			s = "number of " + f.Noun + ": " + s
		}
	}
	if f.Cur != "" {
		if wordCodes[f.Cur] && !strings.Contains(s, ".") {
			s += ".00" // "TRY 60" is a word before a number; "TRY 60.00" an amount
		}
		s = f.Cur + " " + s
	}
	return map[bool]string{true: "-"}[f.Neg] + s
}

// exact prints a decimal rational with every digit.
func exact(r *big.Rat) string {
	for p := 0; p < 60; p++ {
		if s := r.FloatString(p); func() bool { x, _ := new(big.Rat).SetString(s); return x.Cmp(r) == 0 }() {
			return s
		}
	}
	return r.FloatString(60)
}

//go:embed iso4217.txt
var iso4217 string

// isoCodes are the active ISO 4217 codes; wordCodes those that are also English words in
// capitals ("ALL", "TOP"): a currency only before or after an amount-shaped figure.
var (
	isoCodes  = map[string]bool{}
	wordCodes = map[string]bool{}
)

func init() {
	for _, l := range strings.Split(iso4217, "\n") {
		if !strings.HasPrefix(l, "#") {
			for _, c := range strings.Fields(l) {
				isoCodes[c] = true
			}
		}
	}
	for _, c := range strings.Fields("ALL TOP CUP MOP PEN SOS TRY BAM MAD GEL BOB XXX XTS SAR CAD") {
		wordCodes[c] = c != "CAD"
	}
	isoCodes["RMB"] = true // the renminbi's common abbreviation (read as CNY)
}

var (
	// scaleRe reads a scale word after a number; scaleCurRe the letters read as one only after a currency.
	scaleRe    = regexp.MustCompile(`(?i)^[ \x{00a0}]?(million|millions|millionen|billion|billions|trillion|thousand|milliards?|milliarden|mn|bn|mln|bln|mio|mrd|lakhs?|lacs?|crores?)\b`)
	scaleCurRe = regexp.MustCompile(`(?i)^[ \x{00a0}]?(mm|bn|m|k|b)\b`)
	pctRe      = regexp.MustCompile(`(?i)^[ \x{00a0}]?(?:%|‰|per[ \x{00a0}]?cent\b|percent\b|pct\b|percentage points?\b)`)
	// curPhrase is a currency stated in words, longest first; curWordRe is the same anywhere in a text.
	curPhrase  = `u\.\s?s\.\s?dollars?|us dollars?|united states dollars?|canadian dollars?|australian dollars?|new zealand dollars?|hong kong dollars?|singapore dollars?|dollars?|euros?|pounds sterling|pounds?|sterling|yen|yuan|renminbi|swiss francs?|francs?|rupees?|dirhams?|kronor|kroner|krona|krone|rand|pesos?|reais|rubles?|roubles?`
	curAfterRe = regexp.MustCompile(`(?i)^[ \x{00a0}]?(?:` + curPhrase + `)\b`)
	curWordRe  = regexp.MustCompile(`(?i)\b(?:` + curPhrase + `)\b`)
	codeRe     = regexp.MustCompile(`\b[A-Z]{3}\b`)
	// ofRe joins a figure to the count it is part of: "1,200,000 of the Company's 3,000,000 Shares".
	ofRe = regexp.MustCompile(`^\s+(?:out\s+)?of\s+(?:(?:the|its|their|his|her|our|such|all|a|an|[\p{L}][\p{L}\-.&]*['’]s)\s+){0,4}$`)
	// headerRe is a table header that scales its column ("(in thousands)", "$ in millions", "($000)").
	headerRe = regexp.MustCompile(`(?i)\(\s*(?:([$€£]|usd|eur|gbp)\s*)?(?:amounts\s+)?in\s+(thousands|millions|billions)\b[^)]*\)|(?:([$€£]|usd|eur|gbp)\s*)in\s+(thousands|millions|billions)\b|\bin\s+(thousands|millions|billions)\s+of\s+(dollars|euros|pounds)\b|\(\s*([$€£])\s*000'?s?\s*\)`)
)

// curSigns are the signs written before (or after) a number, longest first: glued prefixes name
// which dollar; a letter glued before "$" that is none of these is a currency the parser does not know.
var curSigns = [][2]string{{"U.S.$", "USD"}, {"US$", "USD"}, {"USD$", "USD"}, {"CAD$", "CAD"}, {"Cdn$", "CAD"}, {"CDN$", "CAD"}, {"CA$", "CAD"},
	{"AUD$", "AUD"}, {"AU$", "AUD"}, {"NZD$", "NZD"}, {"NZ$", "NZD"}, {"HKD$", "HKD"}, {"HK$", "HKD"}, {"SGD$", "SGD"}, {"SG$", "SGD"},
	{"NT$", "TWD"}, {"MX$", "MXN"}, {"Mex$", "MXN"}, {"R$", "BRL"}, {"S$", "SGD"}, {"C$", "CAD"}, {"A$", "AUD"},
	{"$", "USD"}, {"＄", "USD"}, {"£", "GBP"}, {"€", "EUR"}, {"¥", "JPY"}, {"￥", "JPY"}, {"₹", "INR"}, {"₩", "KRW"}, {"₱", "PHP"},
	{"₪", "ILS"}, {"₺", "TRY"}, {"₫", "VND"}, {"₦", "NGN"}, {"₽", "RUB"}, {"₴", "UAH"}, {"฿", "THB"}, {"円", "JPY"}, {"元", "CNY"}}

// curWords are currencies written as short words or abbreviations beside a number ("Rs. 1,000",
// "kr 500", "100 zł", "Fr. 20"): matched on word boundaries, either side.
var curWords = [][2]string{{"Rs.", "INR"}, {"Rs", "INR"}, {"kr.", "kr"}, {"kr", "kr"}, {"Kr.", "kr"}, {"Kr", "kr"}, {"zł", "PLN"}, {"Fr.", "CHF"}, {"Fr", "CHF"}, {"SFr.", "CHF"}}

// afterOnly are the signs written only after a number: one before a number belongs to it only after.
var afterOnly = map[string]bool{"円": true, "元": true}

// wordCur maps a currency phrase's words to its code.
func wordCur(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	for _, p := range [][2]string{{"canadian", "CAD"}, {"australian", "AUD"}, {"new zealand", "NZD"}, {"hong kong", "HKD"}, {"singapore", "SGD"}} {
		if strings.HasPrefix(s, p[0]) {
			return p[1]
		}
	}
	switch {
	case strings.Contains(s, "dollar"):
		return "USD"
	case strings.HasPrefix(s, "euro"):
		return "EUR"
	case strings.HasPrefix(s, "pound"), s == "sterling":
		return "GBP"
	case s == "yen":
		return "JPY"
	case s == "yuan", s == "renminbi":
		return "CNY"
	case strings.Contains(s, "franc"):
		return "CHF"
	case strings.HasPrefix(s, "rupee"):
		return "INR"
	case strings.HasPrefix(s, "dirham"):
		return "AED"
	case s == "kronor", s == "krona":
		return "SEK"
	case s == "kroner", s == "krone":
		return "kr"
	case s == "rand":
		return "ZAR"
	case strings.HasPrefix(s, "peso"):
		return "MXN"
	case s == "reais":
		return "BRL"
	case strings.HasPrefix(s, "ruble"), strings.HasPrefix(s, "rouble"):
		return "RUB"
	}
	return ""
}

var scaleOf = map[string]int64{"thousand": 1e3, "k": 1e3, "million": 1e6, "millions": 1e6, "millionen": 1e6, "mn": 1e6, "mln": 1e6, "mio": 1e6,
	"m": 1e6, "mm": 1e6, "billion": 1e9, "billions": 1e9, "milliard": 1e9, "milliards": 1e9, "milliarden": 1e9, "bn": 1e9, "bln": 1e9, "mrd": 1e9,
	"b": 1e9, "trillion": 1e12, "lakh": 1e5, "lakhs": 1e5, "lac": 1e5, "lacs": 1e5, "crore": 1e7, "crores": 1e7, "thousands": 1e3}

// ScaleWord reads a scale word ("million", "thousand", "bn", "lakh") at the start of s.
func ScaleWord(s string) (int64, bool) {
	m := scaleRe.FindString(" " + strings.TrimLeft(s, " \t "))
	if m == "" {
		return 0, false
	}
	return scaleOf[strings.ToLower(strings.Trim(m, "  "))], true
}

// countNouns are the nouns of a count (singular; a plural "s"/"es" is read too): units of things,
// time and measure. A closed list: any other word after a bare number leaves it unknown.
// labelNouns are those that also count when they label a figure before it ("Shares outstanding:
// 3,000,000"); a payment, invoice or page before a number names an amount or an identifier.
var countNouns, labelNouns = map[string]bool{}, map[string]bool{}

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
	for _, w := range strings.Fields(`share unit trade transaction employee person persons people individual customer client member director
		officer patient child children vote security securities option warrant shareholder stockholder holder worker student tenant user
		subscriber seat vehicle location store site defendant plaintiff violation incident day week month hour`) {
		labelNouns[w] = true
	}
}

func isNoun(set map[string]bool, w string) bool {
	return set[w] || set[strings.TrimSuffix(w, "s")] || set[strings.TrimSuffix(w, "es")]
}

// nounStop are the words that end the look for a count noun after a number ("1,200,000 to the
// Seller", "3,000,000 in cash", "500 per share").
var nounStop = map[string]bool{}

// labelLink are the words that may stand between a count noun and the figure it labels ("Shares
// outstanding: 3,000,000", "the number of trades executed was 73").
var labelLink = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`and or to in on at by for from as per plus less minus was were is are will shall be been being has have had
		paid payable due each net gross which that under with over into upon if when after before within against not than but so then may
		must would should could can including inclusive exclusive representing annually monthly weekly yearly daily`) {
		nounStop[w] = true
	}
	for _, w := range strings.Fields(`outstanding issued executed analysed analyzed reviewed held made traded recorded counted reported employed
		total totals totalled totaled was were is are : =`) {
		labelLink[w] = true
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune { r, _ := utf8.DecodeLastRuneInString(s); return r }

func firstRune(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }

// asciiDigits rewrites the decimal digits of other scripts ("١٠٠", "１００") as ASCII, with the
// byte offset in text of each byte of the result and one past its end (nil: text is unchanged).
func asciiDigits(text string) (string, []int) {
	if !strings.ContainsFunc(text, func(r rune) bool { return r > unicode.MaxASCII && unicode.IsDigit(r) }) {
		return text, nil
	}
	var b strings.Builder
	pos := make([]int, 0, len(text)+1)
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if d, ok := digitValue(r); ok && r > unicode.MaxASCII {
			b.WriteByte('0' + d)
			pos = append(pos, i)
		} else {
			b.WriteString(text[i : i+n])
			for k := range n {
				pos = append(pos, i+k)
			}
		}
		i += n
	}
	return b.String(), append(pos, len(text))
}

// digitValue is a decimal digit's value (Unicode's Nd ranges run in tens from a zero).
func digitValue(r rune) (byte, bool) {
	for _, g := range unicode.Nd.R16 {
		if rune(g.Lo) <= r && r <= rune(g.Hi) && g.Stride == 1 {
			return byte((r - rune(g.Lo)) % 10), true
		}
	}
	for _, g := range unicode.Nd.R32 {
		if rune(g.Lo) <= r && r <= rune(g.Hi) && g.Stride == 1 {
			return byte((r - rune(g.Lo)) % 10), true
		}
	}
	return 0, false
}

// Figures reads every figure in text, dates first set aside (their digits are no figures). A
// figure's separators are read from its own form; only a single group of three ("1,200") takes the
// line's convention: a line that writes a decimal comma ("3,5", "1,5 %") or dot grouping
// ("1.200.000") makes it ambiguous.
func Figures(text string) (dateKeys []string, out []Figure) { return scan(text, true) }

// valueFigures reads a typed value's own text: no dates (a typed sum "$2000" or "1950" is no year).
func valueFigures(text string) []Figure { _, fs := scan(text, false); return fs }

func scan(text string, withDates bool) ([]string, []Figure) {
	t, pos := asciiDigits(text)
	var ds []string
	if withDates {
		var spans [][]int
		ds, spans = dateSpans(t)
		b := []byte(t)
		for _, loc := range spans {
			for i := loc[0]; i < loc[1]; i++ {
				b[i] = ' '
			}
		}
		t = string(b)
	}
	var out []Figure
	var hdr *header
	for off := 0; off <= len(t); {
		n := strings.IndexByte(t[off:], '\n')
		if n < 0 {
			n = len(t) - off
		}
		line := t[off : off+n]
		if strings.TrimSpace(line) == "" {
			hdr = nil // a blank line ends a table
		}
		var fs []Figure
		fs, hdr = lineFigures(line, hdr)
		for _, f := range fs {
			f.Start, f.End = f.Start+off, f.End+off
			if pos != nil {
				f.Start, f.End = pos[f.Start], pos[f.End]
			}
			out = append(out, f)
		}
		off += n + 1
	}
	return ds, out
}

// header is a table header's scale, applied to the figures after it until a blank line.
type header struct {
	mul    int64
	cur    string
	except bool // "(in thousands, except per share data)": which figures it scales is the column's
}

func findHeader(line string) (*header, []int) {
	m := headerRe.FindStringSubmatchIndex(line)
	if m == nil {
		return nil, nil
	}
	g := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return strings.ToLower(line[m[2*i]:m[2*i+1]])
	}
	h := &header{mul: map[string]int64{"thousands": 1e3, "millions": 1e6, "billions": 1e9}[g(2)+g(4)+g(5)]}
	if g(7) != "" {
		h.mul = 1e3
	}
	if c := g(1) + g(3) + g(7); c != "" {
		h.cur = UnitCur(c)
	} else if w := g(6); w != "" {
		h.cur = wordCur(w)
	}
	h.except = strings.Contains(strings.ToLower(line[m[0]:m[1]]), "except")
	return h, m[:2]
}

// euroLine reports whether a line writes the continental convention somewhere: a decimal comma or
// dot grouping.
func euroLine(line string, toks [][2]int) bool {
	for _, m := range toks {
		s := line[m[0]:m[1]]
		dots, commas := strings.Count(s, "."), strings.Count(s, ",")
		switch {
		case commas == 1 && dots == 0:
			if l := len(s) - strings.IndexByte(s, ',') - 1; l != 3 {
				return true
			}
		case commas == 0 && dots > 1 && groupedBy(s, '.'):
			return true
		case commas > 0 && dots > 0 && strings.LastIndexByte(s, ',') > strings.LastIndexByte(s, '.'):
			return true
		}
	}
	return false
}

// groupSep is a separator that only ever groups ("'", "’", NBSP, a narrow or thin space) and its length.
func groupSep(s string) int {
	for _, p := range []string{"'", "’", " ", " ", " "} {
		if strings.HasPrefix(s, p) {
			return len(p)
		}
	}
	return 0
}

// threeDigits reports whether s starts with exactly three digits.
func threeDigits(s string) bool {
	return len(s) >= 3 && isDigit(s[0]) && isDigit(s[1]) && isDigit(s[2]) && (len(s) == 3 || !isDigit(s[3]))
}

// numTokens finds the numbers of a line: digits with their separators, or a leading-dot decimal (".5")
// that follows no letter, digit or separator.
func numTokens(line string) [][2]int {
	var out [][2]int
	for i := 0; i < len(line); {
		lead := line[i] == '.' && i+1 < len(line) && isDigit(line[i+1]) && (i == 0 || !strings.ContainsRune(".,'’", lastRune(line[:i])) && !isWordRune(lastRune(line[:i])))
		if !isDigit(line[i]) && !lead {
			i++
			continue
		}
		s := i
		if lead {
			i++
		}
		for i < len(line) && isDigit(line[i]) {
			i++
		}
		for {
			if i+1 < len(line) && (line[i] == '.' || line[i] == ',') && isDigit(line[i+1]) {
				for i++; i < len(line) && isDigit(line[i]); i++ {
				}
				continue
			}
			if n := groupSep(line[i:]); n > 0 && threeDigits(line[i+n:]) {
				i += n + 3
				continue
			}
			break
		}
		out = append(out, [2]int{s, i})
	}
	return mergeSpaced(line, out)
}

// mergeSpaced joins plain-space groups into one number when a currency is beside it ("$3 000 000",
// "1 200 000 €"): without one, "120 450 300" is three columns.
func mergeSpaced(line string, toks [][2]int) [][2]int {
	pure := func(t [2]int) bool { return !strings.ContainsAny(line[t[0]:t[1]], ".,'’   ") }
	var out [][2]int
	for i := 0; i < len(toks); i++ {
		j := i
		for j+1 < len(toks) && toks[j+1][0] == toks[j][1]+1 && line[toks[j][1]] == ' ' && pure(toks[j]) &&
			threeDigits(line[toks[j+1][0]:]) && (toks[j+1][1]-toks[j+1][0] == 3 || !strings.ContainsAny(line[toks[j+1][0]+3:toks[j+1][1]], "'’")) {
			j++
		}
		if j > i && toks[i][1]-toks[i][0] <= 3 {
			c, _, _, _ := currencyBefore(line[:toks[i][0]])
			if a, _, _ := currencyAfter(line[toks[j][1]:]); c != "" || a != "" {
				out = append(out, [2]int{toks[i][0], toks[j][1]})
				i = j
				continue
			}
		}
		out = append(out, toks[i])
	}
	return out
}

// readNumber reads digits and separators by the number's own form. A separator that only groups
// ("'", a space) groups; then a comma before exactly three digits groups when there are two or more
// groups ("1,200,000") or the line writes no decimal comma ("1,200"; ambiguous on a line that does),
// and before one, two or four or more digits it is a decimal comma ("3,5"); lakh groups
// ("1,00,000") group when lakh is allowed; several dots in groups of three group ("1.200.000"); one
// dot is a decimal point ("1.200" with a currency and no scale, or on a continental line, is
// ambiguous); with both, the last separator is the decimal one. ok is false for an outline number
// ("7.3.1").
func readNumber(s string, euro, cur, scaled, lakh bool) (r *big.Rat, grouped, amb bool, why string, ok bool) {
	twoWays := "grouping or a decimal comma"
	if strings.ContainsAny(s, " '’   ") {
		for _, p := range []string{" ", "'", "’", " ", " ", " "} {
			s = strings.ReplaceAll(s, p, "\x00")
		}
		parts := strings.Split(s, "\x00")
		if len(parts[0]) == 0 || len(parts[0]) > 3 || strings.ContainsAny(parts[0], ".,") {
			return nil, false, false, "", false
		}
		head, tail := strings.Join(parts[:len(parts)-1], ""), parts[len(parts)-1]
		if strings.Count(tail, ".")+strings.Count(tail, ",") > 1 || len(tail) < 3 {
			return nil, false, false, "", false
		}
		r, ok = new(big.Rat).SetString(head + tail[:3] + strings.NewReplacer(",", ".").Replace(tail[3:]))
		return r, true, false, "", ok
	}
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
		if strings.IndexByte(head, dec) >= 0 || !groupedBy(head, grp) && !(lakh && grp == ',' && lakhGrouped(head)) {
			amb, why = true, twoWays
		}
		plain, grouped = strings.ReplaceAll(head, string(grp), "")+"."+s[last+1:], true
	case commas > 0:
		parts := strings.Split(s, ",")
		if commas == 1 && len(parts[1]) != 3 {
			plain = parts[0] + "." + parts[1] // a decimal comma
			break
		}
		switch {
		case groupedBy(s, ','):
			if euro && commas == 1 {
				amb, why = true, twoWays+": the line writes decimal commas" // "1,200" on a line that writes decimal commas
			}
		case lakh && lakhGrouped(s):
		case lakhGrouped(s):
			amb, why = true, "lakh grouping beside a currency other than the rupee"
		default:
			amb, why = true, twoWays
		}
		plain, grouped = strings.ReplaceAll(s, ",", ""), true
	case dots == 1:
		if l := len(s) - strings.IndexByte(s, '.') - 1; l == 3 && (euro || cur) && !scaled && strings.IndexByte(s, '.') > 0 {
			amb, why = true, "a decimal point or dot grouping" // "€1.200": one euro twenty, or twelve hundred
		}
	default: // several dots
		if !groupedBy(s, '.') {
			return nil, false, false, "", false
		}
		plain, grouped = strings.ReplaceAll(s, ".", ""), true
	}
	r, ok = new(big.Rat).SetString(plain)
	return r, grouped, amb, why, ok
}

// groupedBy reports whether s is digits grouped by sep in threes ("1,200,000").
func groupedBy(s string, sep byte) bool {
	parts := strings.Split(s, string(sep))
	if len(parts) < 2 || len(parts[0]) == 0 || len(parts[0]) > 3 {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) != 3 {
			return false
		}
	}
	return true
}

// lakhGrouped reports whether s is Indian grouping: the last three digits, then pairs ("1,00,000",
// "12,34,567").
func lakhGrouped(s string) bool {
	parts := strings.Split(s, ",")
	if len(parts) < 3 || len(parts[0]) == 0 || len(parts[0]) > 2 || len(parts[len(parts)-1]) != 3 {
		return false
	}
	for _, p := range parts[1 : len(parts)-1] {
		if len(p) != 2 {
			return false
		}
	}
	return true
}

// parseOpt keeps a currency token from a figure: the one beside it took it (one token, one figure).
type parseOpt struct{ noPre, noSuf, noLeft bool }

// lineFigures parses one line under the header in force (a table's "(in thousands)"), and returns
// the header in force after it.
func lineFigures(line string, hdr *header) ([]Figure, *header) {
	from := 0
	if h, span := findHeader(line); h != nil {
		b := []byte(line)
		for i := span[0]; i < span[1]; i++ {
			b[i] = ' '
		}
		line, hdr, from = string(b), h, span[1]
	}
	toks := numTokens(line)
	euro := euroLine(line, toks)
	var out []Figure
	for _, m := range toks {
		if f, ok := parseAt(line, m[0], m[1], euro, parseOpt{}); ok {
			out = append(out, f)
		}
	}
	out = separate(line, euro, out)
	out = append(out, wordFigures(line, out)...)
	slices.SortStableFunc(out, func(a, b Figure) int { return a.Start - b.Start })
	ranges(line, out)
	for i := len(out) - 1; i >= 0; i-- { // "N of the Company's M shares": N counts what M counts
		if f := &out[i]; f.Class == UnknownFig && i+1 < len(out) && out[i+1].Class == CountFig && ofRe.MatchString(line[f.End:out[i+1].Start]) {
			f.Class, f.Noun = CountFig, out[i+1].Noun
		}
	}
	if hdr != nil {
		for i := range out {
			f := &out[i]
			if f.Start < from || f.Scaled || f.Single || f.Class == PercentFig || f.Class == CountFig || f.Cur != "" && hdr.cur != "" && f.Cur != hdr.cur {
				continue
			}
			if hdr.except {
				f.Amb, f.Why = true, "a table header scales some of its columns ('except …')"
				continue
			}
			f.Amount, f.mul, f.Scaled = new(big.Rat).Mul(f.Amount, new(big.Rat).SetInt64(hdr.mul)), hdr.mul, true
			if f.Cur == "" && hdr.cur != "" {
				f.Cur, f.Class = hdr.cur, MoneyFig
			}
		}
	}
	return out, hdr
}

// separate gives a currency token between two figures to one of them ("1,000 USD 2,000 USD"): to
// the second when the first has a prefix currency of its own; to the first when the second has a
// suffix of its own; else to the nearer; at equal distance a sign goes to the figure after it and a
// code or word to the figure before it. Spans never overlap: a figure that still overlaps its
// neighbour is read without its left side, and else dropped.
func separate(line string, euro bool, fs []Figure) []Figure {
	for i := 0; i+1 < len(fs); {
		a, b := fs[i], fs[i+1]
		if a.End <= b.Start {
			i++
			continue
		}
		toA := true
		if b.preAt >= 0 && a.sufAt >= 0 {
			gapA := b.preAt - len(strings.TrimRight(line[:b.preAt], "  "))
			gapB := len(line[b.preEnd:]) - len(strings.TrimLeft(line[b.preEnd:], "  "))
			switch {
			case a.preAt >= 0:
				toA = false
			case b.sufAt >= 0:
			case gapA != gapB:
				toA = gapA < gapB
			default:
				toA = !b.preSign
			}
		}
		if toA {
			nb, ok := parseAt(line, b.numStart, b.numEnd, euro, parseOpt{noPre: true})
			if ok && nb.Start >= a.End {
				fs[i+1] = nb
			} else if nb, ok = parseAt(line, b.numStart, b.numEnd, euro, parseOpt{noPre: true, noLeft: true}); ok && nb.Start >= a.End {
				fs[i+1] = nb
			} else {
				fs = slices.Delete(fs, i+1, i+2)
			}
		} else {
			na, ok := parseAt(line, a.numStart, a.numEnd, euro, parseOpt{noSuf: true})
			if !ok || na.End > b.Start {
				fs = slices.Delete(fs, i+1, i+2)
				continue
			}
			fs[i] = na
		}
	}
	return fs
}

// signBefore is a minus right before s's end: "-", "−", "–" or "—", one space allowed after it,
// with no letter or digit right before it ("10b-5" and "3–5" are none). dash is a hyphen-like sign
// a range may read as its separator.
func signBefore(s string) (at int, ok bool) {
	t := strings.TrimSuffix(s, " ")
	for _, d := range []string{"-", "−", "–", "—"} {
		if strings.HasSuffix(t, d) {
			at = len(t) - len(d)
			if at > 0 && isWordRune(lastRune(t[:at])) {
				return 0, false
			}
			return at, true
		}
	}
	return 0, false
}

// wordSign is "minus" or "negative" right before s's end; "minus" only where it cannot be a
// subtraction ("the Price minus $5,000" subtracts a positive sum).
func wordSign(s string) (int, bool) {
	t := strings.TrimRight(s, "  ")
	if len(t) == len(s) {
		return 0, false
	}
	l := strings.ToLower(t[max(0, len(t)-8):]) // the tail only: a long line is read once
	for _, w := range []string{"minus", "negative"} {
		if !strings.HasSuffix(l, w) {
			continue
		}
		at := len(t) - len(w)
		before := strings.TrimRight(t[:at], "  ")
		if at > 0 && isWordRune(lastRune(t[:at])) {
			continue
		}
		if w == "minus" && before != "" && isWordRune(lastRune(before)) {
			ws := strings.Fields(strings.ToLower(before[max(0, len(before)-32):]))
			if len(ws) == 0 || !nounStop[ws[len(ws)-1]] {
				continue
			}
		}
		return at, true
	}
	return 0, false
}

// parseAt reads the figure whose digits are line[s:e].
func parseAt(line string, s, e int, euro bool, o parseOpt) (Figure, bool) {
	if s > 0 && unicode.IsLetter(lastRune(line[:s])) {
		if c, _, unk, _ := currencyBefore(line[:s]); c == "" && !unk {
			return Figure{}, false // an identifier's digits ("F12", "Rule 10b5")
		}
	}
	f := Figure{Start: s, mul: 1, numStart: s, numEnd: e, preAt: -1, sufAt: -1}
	paren, signAt, dash, unknown, unknownTok := -1, -1, false, false, ""
	j := s
	for k := 0; k < 4 && !o.noLeft; k++ { // peel a sign, a parenthesis and a currency off the left, in any order
		j0 := j
		if at, ok := signBefore(line[:j]); ok && signAt < 0 {
			signAt, j, dash = at, at, true
		} else if paren < 0 && strings.HasSuffix(line[:j], "(") {
			paren, j = j-1, j-1
		} else if f.Cur == "" && !unknown && !o.noPre {
			tokEnd := len(strings.TrimRight(line[:j], " \u00a0"))
			if c, at, unk, sign := currencyBefore(line[:j]); c != "" && !afterOnly[line[at:tokEnd]] {
				f.Cur, f.preAt, f.preEnd, f.preSign, j = c, at, tokEnd, sign, at
			} else if unk {
				unknown, unknownTok = true, line[at:tokEnd]
				if sign || len(unknownTok) != 3 { // an all-caps word stays outside the span until it reads as a currency
					f.preAt, f.preEnd, f.preSign, j = at, tokEnd, sign, at
				}
			}
		}
		if j == j0 {
			break
		}
	}
	if signAt < 0 && !o.noLeft {
		if at, ok := wordSign(line[:j]); ok {
			signAt, j = at, at
		}
	}
	f.Start = j
	f.Neg = signAt >= 0
	f.hyphen = dash && signAt == j
	end := e
	if m := scaleRe.FindString(line[end:]); m != "" {
		f.mul, end = scaleOf[strings.ToLower(strings.Trim(m, "  "))], end+len(m)
		if c, _, _ := currencyAfter(line[min(end+1, len(line)):]); c != "" && len(m) <= 4 && strings.HasPrefix(line[end:], ".") {
			end++ // "3,5 Mio. EUR": the abbreviation's full stop
		}
	} else if m := scaleCurRe.FindString(line[end:]); m != "" && f.Cur != "" {
		f.mul, end = scaleOf[strings.ToLower(strings.Trim(m, "  "))], end+len(m)
	}
	f.Scaled = f.mul > 1
	pct, permille := false, false
	if m := pctRe.FindString(line[end:]); m != "" {
		pct, permille, end = true, strings.Contains(m, "‰"), end+len(m)
	} else if !o.noSuf {
		if c, n, unk := currencyAfter(line[end:]); c != "" {
			f.sufAt = end
			switch {
			case f.Cur == "" && !unknown || f.Cur == "USD" && strings.HasSuffix(line[f.Start:s], "$") && len(strings.TrimRight(line[f.Start:s], "( ")) == 1:
				f.Cur, unknown = c, false // "$3,000 CAD": the code names which dollar
			case f.Cur != c:
				f.Amb, f.Why = true, "two currencies"
			}
			end += n
		} else if unk && f.Cur == "" {
			unknown, unknownTok, f.sufAt, end = true, strings.TrimSpace(line[end:end+n]), end, end+n
		}
	}
	num := line[s:e]
	lakh := f.Cur == "" || f.Cur == "INR" || f.Cur == "PKR" || f.Cur == "NPR" || f.Cur == "LKR" || f.Cur == "BDT"
	r, grouped, amb, why, ok := readNumber(num, euro, f.Cur != "", f.Scaled, lakh)
	if !ok {
		return Figure{}, false
	}
	f.raw, f.Grouped = r, grouped
	if amb && !f.Amb {
		f.Amb, f.Why = true, why
	}
	f.Amount = new(big.Rat).Mul(r, new(big.Rat).SetInt64(f.mul))
	if permille {
		f.Amount.Quo(f.Amount, big.NewRat(10, 1)) // per mille, as a percentage
	}
	shaped := f.Grouped || f.Scaled || strings.ContainsAny(num, ".,")
	if end < len(line) && line[end] == '-' && !o.noSuf && columnAfter(line[end+1:]) && accounting(line[:f.Start]) && (shaped || f.Cur != "") && signAt < 0 {
		f.Neg, end = true, end+1 // a statement column's trailing minus: "3,000-"
	}
	if paren >= 0 { // accounting parentheses: "(3,000)" in a statement's column, never "Dollars ($3,000,000)"
		if end < len(line) && line[end] == ')' {
			end++
			if accounting(line[:f.Start]) && columnAfter(line[end:]) && (shaped || f.Cur != "" || pct) {
				f.Neg = true
			}
		} else if f.Start == paren {
			f.Start = paren + 1
		}
	}
	f.End = end
	switch {
	case pct:
		f.Class = PercentFig
	case f.Cur != "":
		f.Class = MoneyFig
	default:
		if w := nounAfter(line[end:]); w != "" { // "3,000,000 shares", "5 mm", "2m shares"
			f.Class, f.Noun = CountFig, w
		} else if w := nounBefore(line[:f.Start]); w != "" { // "Shares outstanding: 3,000,000"
			f.Class, f.Noun = CountFig, w
		}
	}
	switch {
	case unknown && (len(unknownTok) != 3 || !isUpperWord(unknownTok)): // "ZZ$1,000", "₿5": a currency the parser does not know
		f.Amb, f.Why, f.Class = true, "a currency the parser does not know ("+unknownTok+")", MoneyFig
	case unknown && shaped && f.Class == UnknownFig && wordCodes[unknownTok]: // "TRY 60.00", "ALL 1,000": a code that is also a word, beside an amount
		f.Cur, f.Class = unknownTok, MoneyFig
		if f.preAt < 0 && f.sufAt < 0 {
			f.Start = min(f.Start, strings.LastIndex(line[:f.Start], unknownTok))
		}
	case unknown && shaped && f.Class == UnknownFig: // "XYZ 1,200,000": an all-caps code that is no ISO code
		f.Amb, f.Why, f.Class = true, "a currency the parser does not know ("+unknownTok+")", MoneyFig
		if f.preAt < 0 && f.sufAt < 0 {
			f.Start = min(f.Start, strings.LastIndex(line[:f.Start], unknownTok))
		}
	}
	if f.Class == CountFig && strings.Count(num, ".") == 1 && !strings.Contains(num, ",") && len(num)-strings.IndexByte(num, '.')-1 == 3 && !f.Amb {
		f.Amb, f.Why = true, "a decimal point or dot grouping" // "1.000 shares": one share or a thousand
	}
	f.Single = e-s == 1 && !f.Scaled && f.Class != MoneyFig && f.Class != PercentFig // "5%" and "$5" are no locators
	return f, true
}

// lettered reports whether a sign is spelt with Latin letters ("US$", "CA$"): those need a word
// boundary, and are never read after a number.
func lettered(sign string) bool {
	return strings.ContainsFunc(sign, func(r rune) bool { return r <= unicode.MaxASCII && unicode.IsLetter(r) })
}

func isUpperWord(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < 'A' || r > 'Z' })
}

// accounting reports whether a "(" or a sign opens a statement's figure: at a line or cell start,
// after a colon, bar or equals sign, or after column spacing.
func accounting(before string) bool {
	t := strings.TrimRight(before, " ")
	return t == "" || strings.HasSuffix(before, "\t") || strings.HasSuffix(before, "  ") || strings.ContainsAny(t[len(t)-1:], ":|=")
}

// columnAfter reports whether a statement's figure ends its cell: the line ends (a full stop
// aside), or a tab, column spacing, a bar or another figure follows ("(USD 3,500) shall be paid"
// is prose, not a column).
func columnAfter(rest string) bool {
	t := strings.TrimLeft(rest, " ")
	if strings.Trim(t, ".,;") == "" || strings.HasPrefix(rest, "\t") || strings.HasPrefix(rest, "  ") {
		return true
	}
	r := firstRune(t)
	return r == '|' || r == '\t' || unicode.IsDigit(r) || r == '(' || r == '-' || unicode.Is(unicode.Sc, r)
}

// currencyBefore is a sign, code or currency word right before a number (one space allowed), where
// it starts, and whether it is a sign. unknown is a currency-like token the parser does not know:
// letters glued before a sign ("ZZ$"), a currency symbol it does not list, or an all-caps three-letter
// token that is no ISO code (the caller decides whether that one is a currency).
func currencyBefore(s string) (code string, at int, unknown, sign bool) {
	t := strings.TrimSuffix(strings.TrimSuffix(s, " "), " ")
	for _, c := range curSigns {
		if strings.HasSuffix(t, c[0]) {
			at := len(t) - len(c[0])
			if at > 0 && unicode.IsLetter(lastRune(t[:at])) && unicode.IsLetter(firstRune(c[0])) {
				continue // "XUS$": no word boundary before a lettered sign
			}
			if at > 0 && unicode.IsLetter(lastRune(t[:at])) {
				k := strings.LastIndexFunc(t[:at], func(r rune) bool { return !unicode.IsLetter(r) && r != '.' }) + 1
				return "", k, true, true // letters glued before a sign that are none of the listed prefixes ("ZZ$")
			}
			return c[1], at, false, true
		}
	}
	for _, c := range curWords {
		if strings.HasSuffix(t, c[0]) {
			if at := len(t) - len(c[0]); at == 0 || !isWordRune(lastRune(t[:at])) {
				return c[1], at, false, false
			}
		}
	}
	if len(t) >= 3 && isUpperWord(t[len(t)-3:]) && (len(t) == 3 || !isWordRune(lastRune(t[:len(t)-3]))) {
		if c := t[len(t)-3:]; isoCodes[c] && !wordCodes[c] {
			return map[string]string{"RMB": "CNY"}[c] + map[bool]string{true: c}[c != "RMB"], len(t) - 3, false, false
		}
		return "", len(t) - 3, true, false
	}
	if r := lastRune(t); r != utf8.RuneError && unicode.Is(unicode.Sc, r) {
		return "", len(t) - utf8.RuneLen(r), true, true
	}
	return "", 0, false, false
}

// currencyAfter is a sign, code or currency word right after a number, and its length; unknown as
// for currencyBefore (n is then the token's length).
func currencyAfter(s string) (code string, n int, unknown bool) {
	if m := curAfterRe.FindString(s); m != "" {
		return wordCur(m), len(m), false
	}
	t := strings.TrimPrefix(strings.TrimPrefix(s, " "), " ")
	skip := len(s) - len(t)
	for _, c := range curSigns {
		if strings.HasPrefix(t, c[0]) && !lettered(c[0]) {
			if !numberNext(t[len(c[0]):]) { // "3 $5" is two figures
				return c[1], skip + len(c[0]), false
			}
		}
	}
	for _, c := range curWords {
		if strings.HasPrefix(t, c[0]) && (len(t) == len(c[0]) || !isWordRune(firstRune(t[len(c[0]):]))) && !strings.HasSuffix(c[0], ".") {
			return c[1], skip + len(c[0]), false
		}
	}
	if len(t) >= 3 && isUpperWord(t[:3]) && (len(t) == 3 || !isWordRune(firstRune(t[3:]))) {
		if c := t[:3]; isoCodes[c] && !wordCodes[c] {
			return map[string]string{"RMB": "CNY"}[c] + map[bool]string{true: c}[c != "RMB"], skip + 3, false
		}
		return "", skip + 3, true
	}
	if r := firstRune(t); r != utf8.RuneError && unicode.Is(unicode.Sc, r) && !numberNext(t[utf8.RuneLen(r):]) {
		return "", skip + utf8.RuneLen(r), true
	}
	return "", 0, false
}

// numberNext reports whether s starts with a number (a sign there belongs to it: "3 $5", "$.5").
func numberNext(s string) bool {
	return s != "" && (isDigit(s[0]) || len(s) > 1 && s[0] == '.' && isDigit(s[1]))
}

// nounAfter is the count noun within the next four words after a number, before a stop word, a
// mark or a line end ("" when none): "3,000,000 ordinary shares", "(30) days", "37 of the trades".
func nounAfter(s string) string {
	s = strings.TrimPrefix(s, ")")
	for range 4 {
		t := strings.TrimLeft(s, " \t ")
		i := strings.IndexFunc(t, func(r rune) bool { return !unicode.IsLetter(r) && r != '-' && r != '\'' && r != '’' })
		if i < 0 {
			i = len(t)
		}
		if i == 0 {
			return ""
		}
		w := strings.ToLower(t[:i])
		w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
		if isNoun(countNouns, w) {
			return w
		}
		if nounStop[w] {
			return ""
		}
		s = t[i:]
	}
	return ""
}

// nounBefore is a count noun that labels the figure from before it, within its clause, with only
// labelLink words between ("Shares outstanding: 3,000,000", "the number of trades executed was 73",
// "Number of employees: 500"); "number of <anything>" labels a count too.
func nounBefore(s string) string {
	if i := strings.LastIndexAny(s, ".;!?,()\n0123456789"); i >= 0 {
		s = s[i+1:]
	}
	var ws []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == ' ' || r == '\t' || r == ' ' }) {
		for {
			if i := strings.IndexAny(w, ":="); i >= 0 {
				if i > 0 {
					ws = append(ws, w[:i])
				}
				ws, w = append(ws, w[i:i+1]), w[i+1:]
				continue
			}
			break
		}
		if w != "" {
			ws = append(ws, w)
		}
	}
	k := len(ws) - 1
	for k >= 0 && labelLink[ws[k]] {
		k--
	}
	if k < 0 || k < len(ws)-5 {
		return ""
	}
	w := strings.Trim(ws[k], "'’\"")
	if k >= 2 && ws[k-1] == "of" && ws[k-2] == "number" && len(w) >= 3 && !strings.ContainsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) || isNoun(labelNouns, w) {
		return w
	}
	return ""
}

// numberWords are the written-out numbers wordFigures reads.
var numberWords = map[string]int64{"zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
	"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50, "sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90}

var wordRe = regexp.MustCompile(`[\p{L}]+`)

// wordFigures reads written-out numbers that a scale, currency or percent makes figures ("three
// million dollars", "five per cent", "one hundred and twenty thousand euros"); a bare "three
// parties" is prose, not a figure. A figure's span never overlaps a digit figure's.
func wordFigures(line string, have []Figure) []Figure {
	var out []Figure
	ws := wordRe.FindAllStringIndex(line, -1)
	for i := 0; i < len(ws); i++ {
		total, cur, last, scaled := int64(0), int64(0), "", false
		start, end, j := ws[i][0], -1, i
		for ; j < len(ws); j++ {
			w := strings.ToLower(line[ws[j][0]:ws[j][1]])
			gap := ""
			if j > i {
				gap = line[ws[j-1][1]:ws[j][0]]
			}
			if j > i && gap != " " && gap != "-" && gap != " " {
				break
			}
			v, unit := numberWords[w]
			_, sc := scaleOf[w]
			switch {
			case unit && (last == "" || last == "scale" || last == "hundred" || last == "and" || last == "tens" && v < 10 && v > 0):
				cur += v
				last = map[bool]string{true: "tens", false: "unit"}[v >= 20 && v%10 == 0]
			case w == "hundred" && (last == "unit" || last == "tens"):
				cur, last = cur*100, "hundred"
			case sc && len(w) > 2 && w != "thousands" && (last == "unit" || last == "tens" || last == "hundred"):
				total, cur, last, scaled = total+cur*scaleOf[w], 0, "scale", true
			case w == "and" && (last == "hundred" || last == "scale") && j+1 < len(ws):
				last = "and"
				continue
			default:
				j-- // this word ends the run
				goto done
			}
			end = ws[j][1]
		}
		j--
	done:
		if end < 0 || last == "and" {
			continue
		}
		f := Figure{Start: start, mul: 1, numStart: start, numEnd: end, preAt: -1, sufAt: -1, Scaled: scaled}
		f.Amount, f.raw = big.NewRat(total+cur, 1), big.NewRat(total+cur, 1)
		e := end
		if m := pctRe.FindString(line[e:]); m != "" {
			f.Class, e = PercentFig, e+len(m)
		} else if c, n, _ := currencyAfter(line[e:]); c != "" {
			f.Class, f.Cur, e = MoneyFig, c, e+n
		} else if !scaled {
			i = j
			continue
		}
		f.End, f.Single = e, total+cur < 10 && !scaled
		if !slices.ContainsFunc(have, func(g Figure) bool { return g.Start < f.End && f.Start < g.End }) {
			out = append(out, f)
		}
		i = j
	}
	return out
}

// ranges reads "$3–5 million", "$3 to $5 million" and "between $3 and $5 million": the second end's
// scale, suffix currency, percent and noun carry back to the first; the first's currency forward.
func ranges(line string, fs []Figure) {
	for i := 0; i+1 < len(fs); i++ {
		a, b := &fs[i], &fs[i+1]
		if a.End > b.Start || a.raw == nil {
			continue
		}
		btw := strings.ToLower(strings.TrimSpace(line[a.End:b.Start]))
		if btw == "" && b.hyphen {
			btw, b.Neg = "-", false // "3 -5", "$3 – $5": the dash is the range's
		}
		head := strings.TrimRight(line[:a.Start], " ")
		between := strings.HasSuffix(strings.ToLower(head[max(0, len(head)-7):]), "between")
		if !(btw == "-" || btw == "–" || btw == "—" || btw == "to" || btw == "and" && between) || a.Amb || b.Amb || a.Neg != b.Neg ||
			a.Class != UnknownFig && b.Class != UnknownFig && a.Class != b.Class {
			continue
		}
		amount := a.Amount
		if a.mul == 1 && b.mul > 1 && a.Class != PercentFig {
			amount = new(big.Rat).Mul(a.raw, new(big.Rat).SetInt64(b.mul))
		}
		if amount.Cmp(b.Amount) > 0 || a.Cur != "" && b.Cur != "" && a.Cur != b.Cur {
			continue // a range ascends in one currency ("pay $500 to 3 creditors" is none)
		}
		if a.mul == 1 && b.mul > 1 && a.Class != PercentFig {
			a.mul, a.Scaled, a.Single, a.Amount = b.mul, true, false, amount
		}
		switch {
		case a.Cur == "" && b.Cur != "":
			a.Cur, a.Class = b.Cur, MoneyFig
		case b.Cur == "" && a.Cur != "":
			b.Cur, b.Class = a.Cur, MoneyFig
		}
		if a.Class == UnknownFig && (b.Class == PercentFig || b.Class == CountFig) {
			a.Class, a.Noun = b.Class, b.Noun
		}
	}
}

// dateRe finds dates: month names, ISO, m/d/y, continental d.m.y, and bare years (a statute section
// such as "§ 2462" is not a year).
var dateRe = regexp.MustCompile(`(?i)\b(?:\d{1,2}\s+)?(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?(?:\s+\d{1,2},?)?\s+(?:19|20)\d\d\b|\b(?:19|20)\d\d-\d\d(?:-\d\d)?\b|\b\d{1,2}/\d{1,2}/(?:19|20)\d\d\b|\b\d{1,2}\.\d{1,2}\.(?:19|20)\d\d\b|(?:§\s*)?\b(?:19|20)\d\d\b`)

var (
	euroDate = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.((?:19|20)\d\d)$`)
	bareYear = regexp.MustCompile(`^(?:19|20)\d\d$`)
	yearWord = regexp.MustCompile(`(?i)\b(?:in|since|during|by|until|till|before|after|from|of|year|years|fy|fiscal|calendar|circa|c\.|early|late|mid|spring|summer|autumn|fall|winter|q[1-4])\s*$`)
)

// dateSpans finds the dates in text with their spans. A bare four-digit 19xx/20xx is a year only
// when it is bare: never after a currency, with decimals or grouping, or before a percent, currency,
// scale or count noun ("$2000", "CHF 2093.25", "2000 employees"), unless a date word precedes it
// ("in 2024 shares were issued"). A continental "1.5.2024" is a date, not an amount.
func dateSpans(text string) (keys []string, spans [][]int) {
	for _, loc := range dateRe.FindAllStringIndex(text, -1) {
		raw := text[loc[0]:loc[1]]
		if strings.HasPrefix(raw, "§") {
			continue
		}
		if m := euroDate.FindStringSubmatch(raw); m != nil {
			d, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			y, _ := strconv.Atoi(m[3])
			if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 && (loc[0] == 0 || !strings.ContainsRune(".,", lastRune(text[:loc[0]]))) {
				keys, spans = append(keys, Date{y, mo, d}.Key()), append(spans, loc)
			}
			continue
		}
		if bareYear.MatchString(raw) && amountShaped(text, loc[0], loc[1]) {
			continue
		}
		if d, ok := ParseDate(raw); ok {
			keys, spans = append(keys, d.Key()), append(spans, loc)
		}
	}
	return keys, spans
}

// amountShaped reports whether the bare four digits text[s:e] are an amount by their own marks.
func amountShaped(text string, s, e int) bool {
	before, after := text[:s], text[e:]
	if yearWord.MatchString(before) {
		return false
	}
	if c, _, unk, _ := currencyBefore(before); c != "" || unk && strings.HasSuffix(strings.TrimRight(before, " "), "$") {
		return true
	}
	if t := strings.TrimRight(before, " "); strings.HasSuffix(t, "(") || strings.HasSuffix(t, "-") || strings.HasSuffix(t, "−") {
		if c, _, _, _ := currencyBefore(strings.TrimRight(t[:len(t)-1], " ")); c != "" {
			return true
		}
	}
	if len(before) > 1 && strings.ContainsRune(".,", lastRune(before)) && isDigit(before[len(before)-2]) {
		return true // part of another number ("1.2024")
	}
	if len(after) > 1 && (after[0] == '.' || after[0] == ',') && isDigit(after[1]) {
		return true // "2093.25"
	}
	if pctRe.MatchString(after) || scaleRe.MatchString(after) || nounAfter(after) != "" {
		return true
	}
	c, _, _ := currencyAfter(after)
	return c != ""
}

// CurrenciesNamed are the currencies a text names anywhere: its figures' currencies, signs (a "$"
// only when no letter is glued before it: "CAD$" names no US dollar), codes and currency words.
func CurrenciesNamed(text string) map[string]bool {
	out := map[string]bool{}
	for _, f := range valueFigures(text) {
		if f.Cur != "" {
			out[f.Cur] = true
		}
	}
	for _, c := range curSigns {
		if lettered(c[0]) {
			continue
		}
		for i := 0; i < len(text); {
			k := strings.Index(text[i:], c[0])
			if k < 0 {
				break
			}
			if i+k == 0 || !unicode.IsLetter(lastRune(text[:i+k])) {
				out[c[1]] = true
				break
			}
			i += k + len(c[0])
		}
	}
	for _, m := range codeRe.FindAllString(text, -1) {
		if isoCodes[m] && !wordCodes[m] {
			out[UnitCur(m)] = true
		}
	}
	for _, m := range curWordRe.FindAllString(text, -1) {
		out[wordCur(m)] = true
	}
	return out
}

// Admits reports whether quote figure q states the typed figure want of class cl (the admission
// rule, fail-safe both ways): the same class, currency, sign and amount; a typed sum may also come
// from a bare figure that is grouped or scaled, has no count noun, in a quote that names no other
// currency ("1,200,000 annually"). An ambiguous figure states nothing. why says what differed.
func Admits(want Figure, cl FigClass, q Figure, named map[string]bool) (cur string, ok bool, why string) {
	if q.Amount.Cmp(want.Amount) != 0 {
		return "", false, ""
	}
	if q.Amb {
		return "", false, "the quote's " + strconv.Quote(Decimal(q.Amount)) + " reads two ways (" + q.Why + ")"
	}
	if q.Neg != want.Neg {
		return "", false, "the quote states it with the other sign"
	}
	switch {
	case cl == MoneyFig && q.Class == MoneyFig:
		if want.Cur != "" && q.Cur != want.Cur {
			return "", false, "the quote states it in " + q.Cur
		}
		return q.Cur, true, ""
	case cl == MoneyFig && q.Class == UnknownFig:
		if !q.Grouped && !q.Scaled {
			return "", false, "the quote states only a bare number"
		}
		c := want.Cur
		for k := range named {
			if c != "" && k != c || c == "" && len(named) > 1 {
				return "", false, "the quote names another currency (" + k + ")"
			}
			c = k
		}
		return c, true, ""
	case cl == CountFig && q.Class == UnknownFig && !q.Single && q.Cur == "" && (q.Grouped || q.Scaled):
		return "", false, "the quote names no count noun for it"
	case cl == q.Class:
		return "", true, ""
	}
	return "", false, "the quote states it as " + map[bool]string{true: "an ", false: "a "}[q.Class == UnknownFig] + q.Class.String() + " figure" +
		map[bool]string{true: " (" + q.Noun + ")"}[q.Noun != ""]
}

// FigureClassOf is a typed value's figure class.
func FigureClassOf(v Value) FigClass {
	return map[Class]FigClass{"Money": MoneyFig, "Percentage": PercentFig, "Count": CountFig}[ClassOf(v)]
}

// UnitCur is a money value's stated unit as a currency code ("" when it names none).
func UnitCur(unit string) string {
	u := strings.TrimSpace(unit)
	if U := strings.ToUpper(u); len(u) == 3 && isoCodes[U] {
		return map[string]string{"RMB": "CNY"}[U] + map[bool]string{true: U}[U != "RMB"]
	}
	for _, c := range curSigns {
		if u == c[0] {
			return c[1]
		}
	}
	for _, c := range curWords {
		if u == c[0] {
			return c[1]
		}
	}
	return wordCur(u)
}

// ValueFigure reads a typed value (money, a count, a percentage) as its one figure, the unit's
// currency applied; why says why it is none.
func ValueFigure(v Value) (Figure, string) {
	cl := FigureClassOf(v)
	vf := valueFigures(v.Value)
	if len(vf) != 1 || vf[0].Amb || vf[0].Class != UnknownFig && vf[0].Class != cl {
		return Figure{}, "it is not one figure of its class"
	}
	want := vf[0]
	if want.Cur == "" && cl == MoneyFig {
		want.Cur = UnitCur(v.Unit)
	}
	if cl != MoneyFig && want.Cur != "" {
		return Figure{}, "a " + cl.String() + " with a currency"
	}
	return want, ""
}

// number is the one figure s states, signed (Calc's operands, Salience); dates are not figures.
func number(s string) (Figure, *big.Rat, bool) {
	fs := valueFigures(s)
	if len(fs) != 1 || fs[0].Amb {
		return Figure{}, nil, false
	}
	r := new(big.Rat).Set(fs[0].Amount)
	if fs[0].Neg {
		r.Neg(r)
	}
	return fs[0], r, true
}

// Traces reports whether ground figure g traces memo figure m (G8, coverage, the committee): the
// same amount, compatible classes (unknown is compatible with any) and currencies (when both state
// one), neither ambiguous, and not an explicit minus against a positive ground figure. A memo's
// positive figure may trace a negative one: prose states "(3,000)" as "a loss of $3,000".
func Traces(m, g Figure) bool {
	return !m.Amb && !g.Amb && m.Amount.Cmp(g.Amount) == 0 && (m.Class == g.Class || m.Class == UnknownFig || g.Class == UnknownFig) &&
		(m.Cur == "" || g.Cur == "" || m.Cur == g.Cur) && !(m.Neg && !g.Neg)
}
