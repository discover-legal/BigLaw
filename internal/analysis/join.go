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
	"sync"
	"unicode"
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

// wholeFigure reports whether a value's digits carry its magnitude: no scale word could be implied
// (grouped thousands, or seven digits or more before any decimals), as the figure parser reads it.
func wholeFigure(v Value) bool {
	f, why := ValueFigure(v)
	if why != "" {
		return false
	}
	intPart, _, _ := strings.Cut(Decimal(f.raw), ".")
	return f.Grouped || len(strings.TrimPrefix(intPart, "-")) >= 7
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
// Purchase Price is", "shall be a balance of", "shall equal", ", being total payments of", "payable by
// Buyer is", "of", and an appositive ", " ("the Purchase Price, $1,200,000,": lab-r3 r5 verifier M1).
var statedAs = regexp.MustCompile(`(?i)^(?:\s+payable by [\w ]{1,40}?)?\s*,?\s*(?:(?:being|is|shall be|will be|was|equals?|shall equal|will equal|amounts? to|totals?|of|:)\s+(?:(?:a|an|the)\s+)?(?:(?:balance|sum|total|amount|aggregate)\s+(?:payments?\s+)?of\s+)?)?$`)

// Framings of a value as a part (lab-r3 r4 M3, narrowed in r5 M1: only words that frame the value
// itself): a portion of the label before it ("the balance of the Purchase Price of", "the cash
// portion of the Purchase Price is"); the value itself held back or in escrow ("$950,000 to be held in
// escrow"); the label less a deduction restated right after it ("$950,000 to Seller, being the Purchase
// Price less the Holdback", "(the Purchase Price net of …)") or right before it ("the Purchase Price
// less the Holdback, being $950,000"); another sum stated as "of the <label>" in its sentence ("$950,000
// at Closing, with $50,000 of the Purchase Price held in escrow"); or a "payable as follows:" frame
// before it.
var (
	portionOf    = regexp.MustCompile(`(?i)\b(?:portion|balance|part|remainder|rest|instal(?:l)?ment|tranche)s?\s+of\s+(?:the\s+)?$`)
	heldOwn      = regexp.MustCompile(`(?i)^\s*(?:(?:shall|will|is|are|to)\s+)?(?:be\s+)?(?:held (?:back|in escrow)|(?:deposited|paid|placed) (?:into|in) escrow|into escrow|in escrow)\b`)
	payableFrame = regexp.MustCompile(`(?i)\b(?:payable|paid|due)\s+(?:as follows|in (?:\w+ )?instal(?:l)?ments)\s*:`)
	wholeBefore  = regexp.MustCompile(`(?i)\b(?:total|aggregate|combined|all)\s+$`)
	spaces       = regexp.MustCompile(`\s+`)
)

// partOf reports whether x's quote frames x itself as a component or portion of y's quantity. A value
// the quote states as y's label (statedAs) is never a part, whatever follows it, unless a portion word
// governs the label ("the cash portion of the Purchase Price is"). Otherwise the framings above, the
// words right after it naming y's label ("$950,000 of the Purchase Price", "toward the Purchase
// Price"), or a portion word that is not y's label's own as the word it is stated under ("a deposit of
// $950,000", not "total payments of") make it a part (lab-r3 r3 M3, r4 M3 and L5, r5 M1).
func partOf(x, y entry) bool {
	label := norm(y.v.Metric)
	if label == "" || x.v.Value == "" {
		return false
	}
	foreign := func(w string) bool { // a portion word that is not one of y's label's own words
		w = strings.ToLower(w)
		return !tokens(label)[strings.TrimSuffix(w, "s")] && !tokens(label)[w]
	}
	deductionAfter, deductionBefore, sibling := labelFramings(label)
	for _, q := range x.quotes {
		i := strings.Index(q, x.v.Value)
		if i < 0 {
			continue
		}
		before := strings.ToLower(strings.Join(strings.Fields(q[:i]), " ")) + map[bool]string{true: " ", false: ""}[i > 0 && q[i-1] == ' ']
		if k := strings.LastIndex(before, label); k >= 0 {
			if portionOf.MatchString(before[:k]) {
				return true
			} else if statedAs.MatchString(before[k+len(label):]) {
				continue
			}
		}
		after := q[i+len(x.v.Value) : min(len(q), i+len(x.v.Value)+80)]
		flat := spaces.ReplaceAllString(after, " ")
		if m := ofLabel.FindStringIndex(after); m != nil && strings.HasPrefix(norm(after[m[1]:]), label) {
			return true
		}
		head := strings.TrimRight(q[max(0, i-40):i], "$£€¥ \t")
		if m := portionBefore.FindStringSubmatchIndex(head); m != nil && foreign(head[m[2]:m[3]]) && !wholeBefore.MatchString(head[:m[2]]) {
			return true
		}
		if heldOwn.MatchString(flat) || deductionAfter.MatchString(flat) || deductionBefore.MatchString(before) || payableFrame.MatchString(q[max(0, i-80):i]) {
			return true
		}
		sent, at := sentenceOf(q, i)
		_, sums := Figures(sent)
		for _, m := range sums { // another sum of the label: this value is the rest
			if m.Class == MoneyFig && at+m.Start != i && at+m.numStart != i && sibling.MatchString(spaces.ReplaceAllString(sent[m.End:min(len(sent), m.End+60)], " ")) {
				return true
			}
		}
	}
	return false
}

// framings caches labelFramings by label (Join asks partOf for every candidate pair), bounded: it is
// emptied past 4,096 labels.
var framings = struct {
	sync.Mutex
	m map[string][3]*regexp.Regexp
}{m: map[string][3]*regexp.Regexp{}}

// labelFramings are the framings that name the label: a deduction from it right after the value or
// right before it, and another sum "of the <label>".
func labelFramings(label string) (after, before, sibling *regexp.Regexp) {
	framings.Lock()
	defer framings.Unlock()
	if r, ok := framings.m[label]; ok {
		return r[0], r[1], r[2]
	}
	lbl := `(?:the\s+)?` + regexp.QuoteMeta(label)
	r := [3]*regexp.Regexp{
		regexp.MustCompile(`(?i)^(?:\s+(?:to|at|on|by|from|into|for)\s+[\w ]{1,30}?)?\s*(?:[,(]\s*)?(?:(?:being|that is|i\.e\.,?|representing|namely)\s+)?` + lbl + `\s+(?:less|net of|minus)\b`),
		regexp.MustCompile(`(?i)` + regexp.QuoteMeta(label) + `\s+(?:less|net of|minus)\b[^.;]{0,80}?,\s*(?:being|i\.e\.,?|that is|namely)\s*$`),
		regexp.MustCompile(`(?i)^\s+of\s+` + lbl + `\b`),
	}
	if len(framings.m) >= 4096 {
		clear(framings.m)
	}
	framings.m[label] = r
	return r[0], r[1], r[2]
}

// sentenceOf is the sentence of q holding byte i (between the end marks around it), and its offset.
func sentenceOf(q string, i int) (string, int) {
	s := strings.LastIndexAny(q[:i], ";!?") + 1
	if d := strings.LastIndex(q[:i], ". "); d+2 > s {
		s = d + 2
	}
	e := strings.IndexAny(q[i:], ";!?")
	if d := strings.Index(q[i:], ". "); d >= 0 && (e < 0 || d < e) {
		e = d
	}
	if e < 0 {
		return q[s:], s
	}
	return q[s : i+e], s
}

// rescale applies the scale a finding states for a quantity outside its value (bench4, SEC: "92"
// with unit "million" against "$92 million", "310" with unit "million dollars" against "310
// million": two conflicts that were one figure): its unit's first word, else the scale the figure
// parser reads on a copy of the value in a quote. scaled reports whether any scale is stated, the
// value's own included; a value whose scale nothing states is never compared with one whose scale is
// stated.
func rescale(class Class, canon string, v Value, quotes []string) (string, bool) {
	if class != "Money" && class != "Count" {
		return canon, false
	}
	if f, why := ValueFigure(v); why == "" && f.Scaled {
		return canon, true // the value states it ("$92 million"): Canon applied it
	}
	mul, _ := ScaleWord(v.Unit)
	val := strings.TrimSpace(v.Value)
	for _, q := range quotes { // a copy of the value that is a whole figure ("92" in "$92 million", not in "1992")
		if mul > 0 || val == "" {
			break
		}
		_, fs := Figures(q)
		for _, f := range fs {
			if f.Scaled && f.mul > 1 && q[f.numStart:f.numEnd] == val {
				mul = f.mul
				break
			}
		}
	}
	kind, r, ok := CanonAmount(canon)
	if mul == 0 || !ok {
		return canon, false
	}
	return kind + ":" + Decimal(r.Mul(r, new(big.Rat).SetInt64(mul))), true
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
				es = append(es, entry{v, f.FID, class, canon, d, docOf(f, v.Value), referents(f, v.Value), qs, scaled, (class == "Money" || class == "Count") && wholeFigure(v)})
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
	for _, a := range names { // token containment, pairwise: "Ostrowski" is "Richard Ostrowski"
		if b, ok := fullName(a, names); ok {
			ents.union(a, b)
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
	return append(out, near(es, out, sameEntity, partiesOf(findings))...)
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

// mentions reports whether x's quotes name y's entity: every distinctive word of it (a stem match:
// "trades", "traded"). Extracting models label one figure's entity inconsistently, the actor in one
// finding and the thing counted in another (bench5 SEC: "Office of Quantitative Research" analysed
// 4,217 trades "through the omnibus account", where the other document's 4,312 is entity "Omnibus
// Equity Trades"; "Bellini" against a payment row that names "Marcus T. Bellini"), so a near quantity pair whose
// entities differ is still one entity's figure when one side's quotes name the other's entity. Not for dates:
// a party's name is in most of a matter's dated passages (a notice to Talbot and Talbot's service).
func mentions(x, y entry) bool {
	ws := tokens(y.v.Entity)
	if len(ws) == 0 {
		return false
	}
	for w := range ws {
		if !stemIn(x.words, w) {
			return false
		}
	}
	return true
}

// oneWay reports whether x's quotes name y's entity and y's quotes do not name x's. When each side's
// quotes name both entities they state a flow between two parties (a payment from one to the other)
// and the entity each finding typed is its role in it, so two labels are two roles: Acme's $50,000 to
// Baker is not Baker's $47,000 to Acme (lab-r4 verifier M4). Only the labels decide such a pair: a
// payer→payee figure pairs only with one its finding gave the same entity.
func oneWay(x, y entry) bool { return mentions(x, y) && !mentions(y, x) }

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
func near(es []entry, found []Conflict, sameEntity func(a, b entry) bool, pc partyContext) []Conflict {
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
		_, x, okx := CanonAmount(a.canon)
		_, y, oky := CanonAmount(b.canon)
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
				a.class != "Date" && (Salience(a.v.Value) < nearSalience || Salience(b.v.Value) < nearSalience) ||
				perDoc[a.doc][b.canon] || perDoc[b.doc][a.canon] || !pairable(a, b) || partOf(a, b) || partOf(b, a) || !close(a, b) {
				continue
			}
			labelled := shared(a.words, b.words) >= 3 && !(a.row() && b.row()) && sameMetric(a, b) &&
				(sameEntity(a, b) || a.class != "Date" && (oneWay(a, b) || oneWay(b, a)))
			if !labelled && (sameEntity(a, b) || !pc.onePair(a, b)) {
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

// partyContext is what the near join's context pairing reads of the whole ledger: the parties its
// findings name (typed entities that are persons, firms or funds, not authorities or instruments)
// and each document's verified quotes.
type partyContext struct {
	parties []string            // normalised party names
	docText map[string][]string // a document's verified quotes
}

// nonParty are entity classes that name no party to a figure.
var nonParty = map[string]bool{"authority": true, "instrument": true, "class": true, "account": true, "document": true, "rule": true,
	"statute": true, "regulation": true, "section": true, "court": true, "date": true, "money": true, "count": true, "percentage": true}

func partiesOf(findings []Finding) partyContext {
	pc := partyContext{docText: map[string][]string{}}
	for _, f := range findings {
		if !f.Verified {
			continue
		}
		for _, q := range f.Quotes {
			pc.docText[q.DocumentID] = append(pc.docText[q.DocumentID], q.Quote)
		}
		names := []string{}
		for _, e := range f.Entities {
			if !nonParty[strings.ToLower(string(e.Class))] {
				names = append(names, e.Name)
			}
		}
		for _, v := range f.Values { // a label written as a name ("Buyer", "Kevin Ostrowski"; not "deletion instruction")
			if ws := strings.Fields(v.Entity); len(ws) > 0 && !slices.ContainsFunc(ws, func(w string) bool { return len(w) >= 3 && !unicode.IsUpper([]rune(w)[0]) }) {
				names = append(names, v.Entity)
			}
		}
		for _, name := range names {
			if n := norm(name); n != "" && len(tokens(n)) > 0 && !slices.Contains(pc.parties, n) {
				pc.parties = append(pc.parties, n)
			}
		}
	}
	return pc
}

// party reports whether a value's entity label is a party the ledger names (not "Email
// transmission", "deletion instruction", "omnibus equity trades").
func (pc partyContext) party(label string) bool {
	l := norm(label)
	return l != "" && slices.ContainsFunc(pc.parties, func(p string) bool { return sameParty(p, l) })
}

// content is a metric label's words that name a quantity of their own (not "total", "date").
func content(metric string) []string {
	var out []string
	for w := range tokens(metric) {
		if !generic[w] && w != "time" {
			out = append(out, w)
		}
	}
	return out
}

// sameParty: one name is part of the other ("Kevin Ostrowski" of "Kevin D. Ostrowski"; one word only
// as the other's last, a surname: "Ostrowski", never "Whitmore" of "Whitmore Capital Advisors LLC"),
// or one is the other's initials ("WCA", "Whitmore Capital Advisors LLC").
func sameParty(a, b string) bool {
	a, b = norm(a), norm(b)
	part := func(short, long string) bool {
		if f := strings.Fields(short); len(f) == 1 {
			lf := strings.Fields(long)
			return len(lf) > 1 && strings.Trim(lf[len(lf)-1], ".,") == f[0]
		}
		return contains(short, long)
	}
	if a == b || part(a, b) || part(b, a) {
		return true
	}
	initials := func(s string) string {
		var out []rune
		for _, w := range strings.Fields(s) {
			if w = strings.Trim(w, ".,()&"); w != "" && !slices.Contains([]string{"llc", "inc", "ltd", "lp", "llp", "corp", "co", "n.a.", "plc", "of", "the", "and"}, w) {
				out = append(out, []rune(w)[0])
			}
		}
		return string(out)
	}
	return len(a) >= 2 && len(strings.Fields(a)) == 1 && a == initials(b) || len(b) >= 2 && len(strings.Fields(b)) == 1 && b == initials(a)
}

// named lists the parties text names (every distinctive word of the name, or its initials as a word).
func (pc partyContext) named(text string) []string {
	ws := tokens(text)
	var out []string
	for _, p := range pc.parties {
		pw := tokens(p)
		all := len(pw) > 0
		for w := range pw {
			all = all && ws[w]
		}
		if all && !slices.ContainsFunc(out, func(o string) bool { return sameParty(o, p) }) {
			out = append(out, p)
		}
	}
	return out
}

// clause is the part of x's quotes that states its value: the value's sentence, or its table row;
// all of its quotes when no quote holds the value's text (a date written another way).
func clause(x entry) string {
	for _, q := range x.quotes {
		if i := strings.Index(q, x.v.Value); i >= 0 && x.v.Value != "" {
			if strings.ContainsAny(q, "\t|") {
				s := strings.LastIndexByte(q[:i], '\n') + 1
				e := strings.IndexByte(q[i:], '\n')
				if e < 0 {
					return q[s:]
				}
				return q[s : i+e]
			}
			sent, _ := sentenceOf(q, i)
			return sent
		}
	}
	return strings.Join(x.quotes, "\n")
}

// source is the party a payment or transfer comes from in s, when it says ("from Ostrowski",
// "by Acme", "Acme Corp paid"): "" when it does not.
var sourceRe = regexp.MustCompile(`\bfrom\s+(?:the\s+)?(\p{Lu}[\p{L}.&'’-]*(?:\s+\p{Lu}[\p{L}.&'’-]*){0,3})|(\p{Lu}[\p{L}.&'’-]*(?:\s+\p{Lu}[\p{L}.&'’-]*){0,3})\s+(?:paid|pays|wired|transferred|sent|remitted|advanced)\b`)

func source(s string) string {
	if m := sourceRe.FindStringSubmatch(s); m != nil {
		return m[1] + m[2]
	}
	return ""
}

// onePair is the near join's context pairing (bench6, SEC: the extractor labelled 4,217 trades "OQR"
// and 4,312 "Whitmore Capital Advisors LLC", $92,600 "Bellini" and $103,800 "Kevin Ostrowski", April
// 15 "Email transmission" and April 22 "deletion instruction", so the labels kept all three bench5
// pairs apart): two close values whose labels disagree are one figure when their metric and quote
// context agree and neither side's quote names a different party for it. Concretely:
//   - the context: at least two distinctive words (by stem) of the values' quotes are shared (three of
//     the dates' own clauses), and the labels can be one metric (sameMetric, and a content word of the
//     labels shared, or one label with none and three words shared), or both are totals;
//   - two table rows pair only when both state a total (rows are otherwise an enumeration);
//   - parties: the parties each clause names (partiesOf) share one, or one side names none, or one
//     side's party label is named in the other's document (a matter's principal party, named
//     throughout); two party labels each foreign to the other's document are two parties
//     ("Buyer"'s price and "Guarantor"'s);
//   - a flow keeps its direction: when both clauses say whom it is from, it is from one party
//     (Acme's payment to Baker is not Baker's to Acme).
func (pc partyContext) onePair(a, b entry) bool {
	total := func(x entry) bool {
		t := strings.ToLower(x.v.Metric + " " + clause(x))
		return strings.Contains(t, "total") || strings.Contains(t, "aggregate")
	}
	totals := total(a) && total(b)
	if a.row() && b.row() && !totals {
		return false
	}
	wa, wb := a.words, b.words
	if a.class == "Date" { // a matter's dated passages share its parties and its vocabulary: only the dates' own clauses count
		wa, wb = tokens(clause(a)), tokens(clause(b))
		for _, m := range strings.Fields("january february march april may june july august september october november december jan feb mar apr jun jul aug sep sept oct nov dec mon tue wed thu fri sat sun") {
			delete(wa, m) // a date's own words are not its context
		}
	}
	n := 0
	for w := range wa {
		if stemIn(wb, w) {
			n++
		}
	}
	if a.class == "Date" && n < 3 {
		return false
	}
	ma, mb := content(a.v.Metric), content(b.v.Metric)
	metric := totals || len(ma) == 0 && n >= 3 || len(mb) == 0 && n >= 3 ||
		slices.ContainsFunc(ma, func(w string) bool { return stemIn(tokens(b.v.Metric), w) })
	if n < 2 || !metric || !sameMetric(a, b) && !totals {
		return false
	}
	ca, cb := clause(a), clause(b)
	if sa, sb := source(ca), source(cb); sa != "" && sb != "" && !sameParty(sa, sb) {
		return false
	}
	known := func(x, y entry) bool { // x's party label is named in y's document
		return pc.party(x.v.Entity) && len(pc.named(strings.Join(pc.docText[y.doc], "\n"))) > 0 &&
			slices.ContainsFunc(pc.named(strings.Join(pc.docText[y.doc], "\n")), func(p string) bool { return sameParty(p, x.v.Entity) })
	}
	if pc.party(a.v.Entity) && pc.party(b.v.Entity) && !known(a, b) && !known(b, a) {
		return false
	}
	pa, pb := pc.named(ca), pc.named(cb)
	return len(pa) == 0 || len(pb) == 0 || known(a, b) || known(b, a) ||
		slices.ContainsFunc(pa, func(p string) bool { return slices.ContainsFunc(pb, func(q string) bool { return sameParty(p, q) }) })
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

// fullName is the one longer name that short is part of ("Ostrowski" of "Richard Ostrowski"). Names
// are compared pairwise, never chained: two different full names are two parties whatever they share,
// and a fragment that is part of two of them ("Davenport-Chen" of "Margaret Davenport-Chen" and
// "Sophia Davenport-Chen", "Bayshore" of two Bayshore companies) names neither (bench6, trust:
// Margaret's age 71 was compared with Sophia's 44 through a shared surname).
func fullName(short string, names []string) (string, bool) {
	var longer []string
	for _, n := range names {
		if n != short && contains(short, n) {
			longer = append(longer, n)
		}
	}
	top := slices.DeleteFunc(slices.Clone(longer), func(n string) bool { // the longest: not part of another of them
		return slices.ContainsFunc(longer, func(o string) bool { return o != n && contains(n, o) })
	})
	if len(top) != 1 {
		return "", false
	}
	return top[0], true
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
