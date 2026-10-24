// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Conflict is one cross-document discrepancy: one metric, values from at least two documents
// whose value sets do not overlap.
type Conflict struct {
	Entity string        `json:"entity"`
	Metric string        `json:"metric"`
	Kind   string        `json:"kind"` // numeric or date
	Values []ConflictVal `json:"values"`
}

type ConflictVal struct {
	Value      string `json:"value"`
	Canon      string `json:"canon"`
	DocumentID string `json:"documentId"`
	FID        string `json:"fid"`
}

// Alias names one party and the other names the record uses for it (the lead supplies these).
type Alias struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

const maxValuesPerMetric = 6 // a longer column under one label is an enumeration, not a conflict

type entry struct {
	v      Value
	fid    string
	class  Class
	canon  string
	date   Date
	doc    string
	words  map[string]bool // the distinctive words of its quotes
	quotes []string
	scaled bool // a scale word (million, thousand…) is stated for it: in its value, unit or quote
	whole  bool // its own digits carry its magnitude: grouped thousands ("$92,000,000") or seven digits or more
}

// grouped is a figure written with its thousands grouped ("92,000,000", "4,217").
var grouped = regexp.MustCompile(`\d{1,3}(?:,\d{3})+`)

// wholeFigure reports whether a value's digits carry its magnitude: no scale word could be implied.
func wholeFigure(value string) bool {
	if grouped.MatchString(value) {
		return true
	}
	n := 0
	for _, r := range strings.SplitN(value, ".", 2)[0] {
		if r >= '0' && r <= '9' {
			n++
		}
	}
	return n >= 7
}

// pairable reports whether two quantities' scales are both known, so their normalised magnitudes
// compare: both stated with a scale word, both without one, or one with it and the other written in
// full ("$92,000,000" against "$95 million": lab-r3 r2 verifier M2). A bare "92" against "$95 million"
// is not compared: its scale may be a column's ("$ millions"), which nothing states.
func pairable(a, b entry) bool {
	return a.scaled == b.scaled || a.scaled && b.whole || b.scaled && a.whole
}

// ofLabel is a value followed by "of (the)" or "toward(s)/against/on account of (the)" and then a label.
var ofLabel = regexp.MustCompile(`(?i)^\s*(?:of|out of|from|towards?|against|on account of)\s+(?:the\s+)?`)

// portionBefore is a portion word as the label a value is stated under, right before it: "a deposit
// of $50,000", "the first instalment: $200,000", "a payment in the amount of $950,000" (not a total:
// "payments totaling $92,600" states the whole).
var portionBefore = regexp.MustCompile(`(?i)\b(portion|part|instal(?:l)?ments?|payments?|deposits?|tranches?|holdbacks?|escrow|balance|advance|retention)\s*(?:of|:|=|is|was|in the amount of|equal to)?\s*(?:the sum of\s*)?$`)

// statedAs is the text between a label and a value that states the value as the label itself: "the
// Purchase Price is", "shall be a balance of", ", being total payments of", "payable by Buyer is", "of".
var statedAs = regexp.MustCompile(`(?i)^(?:\s+payable by [\w ]{1,40}?)?\s*,?\s*(?:being|is|shall be|will be|was|equals|of|:)\s+(?:(?:a|an|the)\s+)?(?:(?:balance|sum|total|amount|aggregate)\s+(?:payments?\s+)?of\s+)?$`)

// Framings of a value as a part (lab-r3 r4 verifier M3): a deduction after it in its clause ("$950,000
// …, being the Purchase Price less the Holdback", "net of", "minus"), a sum held back or in escrow in
// its sentence, or a "payable as follows:" / "in instalments:" frame before it.
var (
	deductionAfter = regexp.MustCompile(`(?i)^[^.;]*\b(?:less|net of|minus)\b`)
	heldBack       = regexp.MustCompile(`(?i)\b(?:held (?:back|in escrow)|holdback|into escrow|in escrow)\b`)
	payableFrame   = regexp.MustCompile(`(?i)\b(?:payable|paid|due)\s+(?:as follows|in (?:\w+ )?instal(?:l)?ments)\s*:`)
	wholeBefore    = regexp.MustCompile(`(?i)\b(?:total|aggregate|combined|all)\s+$`)
)

// partOf reports whether x's quote frames x itself as a component or portion of y's quantity. A value
// the quote states as y's label ("the Purchase Price is $1,200,000, less the deposit", "shall be a
// balance of", "being total payments of": statedAs) is never a part, whatever follows it. Otherwise it
// is when the words right after it name y's label ("$950,000 of the Purchase Price", "toward the
// Purchase Price"), a portion word that is not y's label's own is the word it is stated under ("a
// deposit of $950,000", not "total payments of"), a deduction follows it in its clause ("…, being the
// Purchase Price less the Holdback", "net of", "minus"), its sentence holds a sum back or in escrow, or
// a "payable as follows:" frame comes before it (lab-r3 r3 M3, r4 M3 and L5).
func partOf(x, y entry) bool {
	label := norm(y.v.Metric)
	if label == "" || x.v.Value == "" {
		return false
	}
	foreign := func(w string) bool { // a portion word that is not one of y's label's own words
		w = strings.ToLower(w)
		return !tokens(label)[strings.TrimSuffix(w, "s")] && !tokens(label)[w]
	}
	for _, q := range x.quotes {
		i := strings.Index(q, x.v.Value)
		if i < 0 {
			continue
		}
		before := strings.ToLower(strings.Join(strings.Fields(q[:i]), " ")) + map[bool]string{true: " ", false: ""}[i > 0 && q[i-1] == ' ']
		if k := strings.LastIndex(before, label); k >= 0 && statedAs.MatchString(before[k+len(label):]) {
			continue
		}
		after := q[i+len(x.v.Value) : min(len(q), i+len(x.v.Value)+80)]
		if m := ofLabel.FindStringIndex(after); m != nil && strings.HasPrefix(norm(after[m[1]:]), label) {
			return true
		}
		head := strings.TrimRight(q[max(0, i-40):i], "$£€¥ \t")
		if m := portionBefore.FindStringSubmatchIndex(head); m != nil && foreign(head[m[2]:m[3]]) && !wholeBefore.MatchString(head[:m[2]]) {
			return true
		}
		if deductionAfter.MatchString(after) || heldBack.MatchString(sentenceOf(q, i)) || payableFrame.MatchString(q[max(0, i-80):i]) {
			return true
		}
	}
	return false
}

// sentenceOf is the sentence of q holding byte i (between the end marks around it).
func sentenceOf(q string, i int) string {
	s := strings.LastIndexAny(q[:i], ".;!?") + 1
	e := strings.IndexAny(q[i:], ";!?")
	if d := strings.Index(q[i:], ". "); d >= 0 && (e < 0 || d < e) {
		e = d
	}
	if e < 0 {
		return q[s:]
	}
	return q[s : i+e]
}

// scaleWord is a scale stated after a figure: "$92 million", "310 million dollars".
var scaleWord = regexp.MustCompile(`(?i)^\s*(million|billion|thousand|mm|bn)\b`)

// rescale applies the scale a finding states for a quantity outside its value (bench4, SEC: "92"
// with unit "million" against "$92 million", "310" with unit "million dollars" against "310
// million": two conflicts that were one figure): its unit's first word, else the word after the
// value in a quote. scaled reports whether any scale is stated, the value's own included; a value
// whose scale nothing states is never compared with one whose scale is stated.
func rescale(class Class, canon string, v Value, quotes []string) (string, bool) {
	if class != "Money" && class != "Count" {
		return canon, false
	}
	if m := reMoneyVal.FindStringSubmatch(strings.TrimSpace(v.Value)); m != nil && m[2] != "" {
		return canon, true // the value states it ("$92 million"): Canon applied it
	}
	word := ""
	if m := scaleWord.FindStringSubmatch(v.Unit); m != nil {
		word = m[1]
	}
	for _, q := range quotes { // every copy of the value that is a whole figure ("92" in "$92 million", not in "1992")
		for i := 0; word == "" && v.Value != "" && i < len(q); i++ {
			j := strings.Index(q[i:], v.Value)
			if j < 0 {
				break
			}
			i += j
			if m := scaleWord.FindStringSubmatch(q[i+len(v.Value):]); m != nil && (i == 0 || !strings.ContainsRune("0123456789.,", rune(q[i-1]))) {
				word = m[1]
			}
		}
	}
	prefix, num, _ := strings.Cut(canon, ":")
	r, ok := new(big.Rat).SetString(num)
	if word == "" || !ok {
		return canon, false
	}
	return prefix + ":" + Decimal(r.Mul(r, new(big.Rat).SetInt64(scale[strings.ToLower(word)]))), true
}

// row reports whether a value is stated only in table rows (tab-separated): one row of an
// enumeration (a trade, a payment), never by itself the same fact as another table's row.
func (e entry) row() bool {
	return !slices.ContainsFunc(e.quotes, func(q string) bool { return strings.Contains(q, e.v.Value) && !strings.Contains(q, "\t") })
}

// sets is a union-find over names.
type sets map[string]string

func (s sets) find(x string) string {
	for s[x] != "" && s[x] != x {
		x = s[x]
	}
	return x
}

func (s sets) union(a, b string) { s[s.find(a)] = s.find(b) }

// Join finds the discrepancies among verified findings (v1's cross-document join, model-free).
// Numeric values share a metric identity when their metric label, quantity class and alias-unified
// entity match (a value with no entity joins its metric's one entity, if there is one); dates are
// one event when their labels match or their quotes share two distinctive words (two table rows only
// by label: r3). A conflict needs two documents whose value sets are disjoint, so a list restated in
// both is not one. Durations and identifiers are never compared, and a quarantined finding never
// joins. Then near (labels aside, close values whose quotes share words).
func Join(findings []Finding, aliases []Alias) []Conflict {
	key := func(s string) string { return strings.TrimPrefix(norm(s), "the ") }
	var es []entry
	var names []string
	for _, f := range findings {
		for _, v := range f.Values {
			if class, canon, d, ok := Canon(v); ok && f.Verified {
				var qs []string
				for _, q := range f.Quotes {
					qs = append(qs, q.Quote)
				}
				canon, scaled := rescale(class, canon, v, qs)
				es = append(es, entry{v, f.FID, class, canon, d, docOf(f, v.Value), referents(f, v.Value), qs, scaled, (class == "Money" || class == "Count") && wholeFigure(v.Value)})
				if n := key(v.Entity); n != "" && !slices.Contains(names, n) {
					names = append(names, n)
				}
			}
		}
	}
	ents, events := sets{}, sets{}
	for _, a := range aliases {
		for _, o := range a.Aliases {
			if key(a.Name) != "" && key(o) != "" {
				ents.union(key(a.Name), key(o))
			}
		}
	}
	for i, a := range names { // token containment: "Ostrowski" is "Richard Ostrowski"
		for _, b := range names[i+1:] {
			if contains(a, b) || contains(b, a) {
				ents.union(a, b)
			}
		}
	}
	metrics := map[string][]int{}
	var order []string
	for i, e := range es {
		if e.class == "Date" {
			for j := range i {
				if es[j].class == "Date" && (norm(e.v.Metric) != "" && norm(e.v.Metric) == norm(es[j].v.Metric) || !(e.row() && es[j].row()) && shared(e.words, es[j].words) >= 2 && sameMetric(e, es[j])) {
					events.union(strconv.Itoa(i), strconv.Itoa(j))
				}
			}
		} else if k := norm(e.v.Metric) + "|" + string(e.class); norm(e.v.Metric) != "" { // a bare number is never compared
			if metrics[k] == nil {
				order = append(order, k)
			}
			metrics[k] = append(metrics[k], i)
		}
	}
	var out []Conflict
	try := func(idx []int, kind string, differ func(a, b entry) bool) {
		if c, ok := conflict(es, idx, kind, differ); ok {
			out = append(out, c)
		}
	}
	for _, k := range order {
		byEnt, roots := map[string][]int{}, []string{}
		for _, i := range metrics[k] {
			r := ents.find(key(es[i].v.Entity)) // "" when it names no entity
			if byEnt[r] == nil {
				roots = append(roots, r)
			}
			byEnt[r] = append(byEnt[r], i)
		}
		if loose := byEnt[""]; loose != nil && len(roots) == 2 { // the unattributed join the one entity
			r := roots[0] + roots[1]
			byEnt[r], roots = append(byEnt[r], loose...), []string{r}
		}
		for _, r := range roots {
			try(byEnt[r], "numeric", func(a, b entry) bool { return a.canon != b.canon && pairable(a, b) && !partOf(a, b) && !partOf(b, a) })
		}
	}
	byEvent, roots := map[string][]int{}, []string{}
	for i, e := range es {
		if r := events.find(strconv.Itoa(i)); e.class == "Date" {
			if byEvent[r] == nil {
				roots = append(roots, r)
			}
			byEvent[r] = append(byEvent[r], i)
		}
	}
	for _, r := range roots {
		try(byEvent[r], "date", func(a, b entry) bool { return !a.date.Compatible(b.date) })
	}
	sameEntity := func(a, b entry) bool { return ents.find(key(a.v.Entity)) == ents.find(key(b.v.Entity)) }
	return append(out, near(es, out, sameEntity)...)
}

// generic are metric words that name no quantity of their own ("grand total of payments" is payments).
var generic = map[string]bool{"total": true, "grand": true, "aggregate": true, "overall": true, "amount": true, "sum": true, "value": true,
	"number": true, "count": true, "figure": true, "percentage": true, "percent": true}

// sameMetric reports whether two values, labelled differently, can be one quantity: not both labels
// name a thing of their own, a word of the label that its own quote states and that neither the
// other's label nor its quotes mention (lab-r3 verifier: a management fee and an incentive
// allocation, a termination notice and a cure demand, are two facts). A label its quote does not
// state ("email sent" over an e-mail's headers) names nothing of its own.
func sameMetric(a, b entry) bool {
	own := func(x, y entry) bool {
		for w := range tokens(x.v.Metric) {
			if !generic[w] && stemIn(x.words, w) && !stemIn(tokens(y.v.Metric), w) && !stemIn(y.words, w) {
				return true
			}
		}
		return false
	}
	return !own(a, b) || !own(b, a)
}

// stemIn reports whether set holds w or a word of its stem (one is the other plus at most three letters,
// sharing five: "instructed", "instruction").
func stemIn(set map[string]bool, w string) bool {
	if set[w] {
		return true
	}
	for x := range set {
		n := 0
		for n < min(len(x), len(w)) && x[n] == w[n] {
			n++
		}
		if n >= 5 && n >= max(len(x), len(w))-3 {
			return true
		}
	}
	return false
}

// maxNear bounds the near conflicts one join reports: they are leads to check, not a column dump.
// nearSalience is the least salient quantity compared (any percentage, money from $1,000, a count
// of thousands): a share price or a day count is too common for nearness to mean one fact.
// nearShare: two quantities are near when they differ by at most an eighth of the larger (a price
// and its 20% deposit, a 2% fee and a 2.5% one, are not one figure misstated).
const (
	maxNear      = 20
	nearSalience = 45
	nearShare    = 8
)

// near finds the conflicts the label join cannot see (bench r3, SEC: 4,217 vs 4,312 trades
// analysed, $92,600 vs $103,800 paid to the club, 73% vs 78% of volume, April 22 vs April 15, each
// labelled differently in its document's finding): two values of one class from two documents that
// are near each other (within an eighth for a quantity; a month for a full date), of one entity
// (alias-unified, or both of none) and possibly one metric (sameMetric), and whose quotes share at
// least three distinctive words, when neither document states the other's value anywhere
// (so a total restated beside its parts is not one). Quantities must be salient (nearSalience), and two
// values each only in table rows are never paired (rows of two tables are enumerations, not one fact
// restated). It skips pairs a found conflict already holds.
func near(es []entry, found []Conflict, sameEntity func(a, b entry) bool) []Conflict {
	perDoc := map[string]map[string]bool{}
	for _, e := range es {
		if perDoc[e.doc] == nil {
			perDoc[e.doc] = map[string]bool{}
		}
		perDoc[e.doc][e.canon] = true
	}
	held := map[string]bool{}
	for _, c := range found {
		for _, v := range c.Values {
			held[v.FID+"|"+v.Canon] = true
		}
	}
	close := func(a, b entry) bool {
		if a.class == "Date" {
			if a.date.Day == 0 || b.date.Day == 0 {
				return false
			}
			d := a.date.time().Sub(b.date.time()).Hours() / 24
			return d != 0 && d <= 31 && d >= -31
		}
		x, okx := Number(strings.SplitN(a.canon, ":", 2)[1])
		y, oky := Number(strings.SplitN(b.canon, ":", 2)[1])
		if !okx || !oky {
			return false
		}
		fx, _ := x.Float64()
		fy, _ := y.Float64()
		hi := max(fx, fy, -fx, -fy)
		return hi > 0 && fx != fy && (fx-fy <= hi/nearShare && fy-fx <= hi/nearShare)
	}
	var out []Conflict
	seen := map[string]bool{}
	for i, a := range es {
		for _, b := range es[i+1:] {
			if a.class != b.class || a.doc == b.doc || a.doc == "" || b.doc == "" || held[a.fid+"|"+a.canon] && held[b.fid+"|"+b.canon] ||
				a.class != "Date" && (Salience(a.v.Value) < nearSalience || Salience(b.v.Value) < nearSalience) || a.row() && b.row() ||
				perDoc[a.doc][b.canon] || perDoc[b.doc][a.canon] || !pairable(a, b) || partOf(a, b) || partOf(b, a) || !close(a, b) || shared(a.words, b.words) < 3 || !sameEntity(a, b) || !sameMetric(a, b) {
				continue
			}
			k := min(a.canon, b.canon) + "|" + max(a.canon, b.canon)
			if seen[k] || len(out) == maxNear {
				continue
			}
			seen[k] = true
			kind := map[bool]string{true: "date", false: "numeric"}[a.class == "Date"]
			out = append(out, Conflict{cmp.Or(a.v.Entity, b.v.Entity), cmp.Or(a.v.Metric, b.v.Metric), kind,
				[]ConflictVal{{a.v.Value, a.canon, a.doc, a.fid}, {b.v.Value, b.canon, b.doc, b.fid}}})
		}
	}
	return out
}

// conflict applies the rule to one group: 2–6 distinct values from ≥ 2 documents, and a pair of
// documents with disjoint value sets holding a pair of values that differ.
func conflict(es []entry, idx []int, kind string, differ func(a, b entry) bool) (Conflict, bool) {
	perDoc := map[string]map[string]bool{}
	var docs, canons []string
	for _, i := range idx {
		e := es[i]
		if perDoc[e.doc] == nil {
			perDoc[e.doc] = map[string]bool{}
			docs = append(docs, e.doc)
		}
		perDoc[e.doc][e.canon] = true
		if !slices.Contains(canons, e.canon) {
			canons = append(canons, e.canon)
		}
	}
	if len(docs) < 2 || len(canons) < 2 || len(canons) > maxValuesPerMetric {
		return Conflict{}, false
	}
	slices.Sort(docs)
	for x := range docs {
		for y := x + 1; y < len(docs); y++ {
			disjoint := true
			for c := range perDoc[docs[x]] {
				disjoint = disjoint && !perDoc[docs[y]][c]
			}
			for _, i := range idx {
				for _, j := range idx {
					if a, b := es[i], es[j]; disjoint && a.doc == docs[x] && b.doc == docs[y] && differ(a, b) {
						return Conflict{cmp.Or(a.v.Entity, b.v.Entity), cmp.Or(a.v.Metric, b.v.Metric), kind,
							[]ConflictVal{{a.v.Value, a.canon, a.doc, a.fid}, {b.v.Value, b.canon, b.doc, b.fid}}}, true
					}
				}
			}
		}
	}
	return Conflict{}, false
}

// contains: every word of short is in long, one of them at least four letters long.
func contains(short, long string) bool {
	st, lt := strings.Fields(short), strings.Fields(long)
	return len(st) <= len(lt) && allIn(st, lt) && slices.ContainsFunc(st, func(w string) bool { return len(w) >= 4 })
}

// docOf is the document of the quote that holds the value (else the finding's first quote).
func docOf(f Finding, value string) string {
	for _, q := range f.Quotes {
		if strings.Contains(q.Quote, value) {
			return q.DocumentID
		}
	}
	if len(f.Quotes) > 0 {
		return f.Quotes[0].DocumentID
	}
	return ""
}

var stop = map[string]bool{"the": true, "and": true, "for": true, "that": true, "with": true, "this": true, "was": true, "are": true,
	"its": true, "had": true, "has": true, "not": true, "from": true, "into": true, "were": true, "their": true, "about": true,
	"after": true, "before": true, "during": true, "each": true, "per": true, "been": true, "have": true, "will": true, "date": true,
	"day": true, "month": true, "year": true, "total": true, "amount": true, "approximately": true, "need": true, "you": true}

// referents are the distinctive words of a value's quotes (not the value, digits, units or stop words).
func referents(f Finding, value string) map[string]bool {
	out := map[string]bool{}
	for _, q := range f.Quotes {
		for t := range tokens(strings.ReplaceAll(q.Quote, value, " ")) {
			out[t] = true
		}
	}
	return out
}

// tokens are the distinctive words of s: folded, a plural or possessive trimmed, no digits or stop words.
func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(s)) {
		t = strings.TrimSuffix(strings.Trim(t, ".,;:()[]{}\"'`|—–"), "'s")
		if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			t = t[:len(t)-1]
		}
		if len(t) >= 3 && !strings.ContainsAny(t, "0123456789") && !stop[t] {
			out[t] = true
		}
	}
	return out
}

func shared(a, b map[string]bool) (n int) {
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}
