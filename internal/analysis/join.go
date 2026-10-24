// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	party  string // the alias-unified party the figure reader read its figure as belonging to
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
		for _, m := range Spans(sent) { // another figure of the label in its sentence: this value is the rest
			if (at+m.End <= i || at+m.Start >= i+len(x.v.Value)) && sibling.MatchString(spaces.ReplaceAllString(sent[m.End:min(len(sent), m.End+60)], " ")) {
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

// Join finds the discrepancies among verified findings (v1's cross-document join, model-free). It
// compares two figures only when the figure reader read both as one kind of quantity of one party
// (Value.Fig: its kind, currency and exact amount, scale applied, in Canon; its party unified by the
// lead's aliases): a figure with no reading, an unclear one or no party is never part of a conflict
// (lab-r4 r4, owner decision: no word test decides whose figure it is). Numeric values share a
// metric identity when their metric label, quantity class and party match; dates of one party are one
// event when their labels match (dates under two labels pair only by near's context test: gate A, a
// service date and a termination notice of one party shared two words). A conflict needs two
// documents whose value sets are disjoint, so a list restated in
// both is not one. Durations and identifiers (kind other) are never compared, and a quarantined
// finding never joins. Then near (labels aside, close values of one party whose quotes share words).
func Join(findings []Finding, aliases []Alias) []Conflict {
	key := func(s string) string { return strings.TrimPrefix(norm(s), "the ") }
	parties, events := sets{}, sets{}
	for _, a := range aliases {
		for _, o := range a.Aliases {
			if key(a.Name) != "" && key(o) != "" {
				parties.union(key(a.Name), key(o))
			}
		}
	}
	var es []entry
	for _, f := range findings {
		for _, v := range f.Values {
			class, canon, d, ok := Canon(v)
			if !ok || !f.Verified || v.Fig == nil || key(v.Fig.Party) == "" { // no reading, or no party: no conflict is asserted on it
				continue
			}
			var qs []string
			for _, q := range f.Quotes {
				qs = append(qs, q.Quote)
			}
			es = append(es, entry{v, f.FID, class, canon, d, docOf(f, v.Value), referents(f, v.Value), qs, parties.find(key(v.Fig.Party))})
		}
	}
	metrics := map[string][]int{}
	var order []string
	for i, e := range es {
		if e.class == "Date" {
			for j := range i {
				if es[j].class == "Date" && es[j].party == e.party && norm(e.v.Metric) != "" && norm(e.v.Metric) == norm(es[j].v.Metric) {
					events.union(strconv.Itoa(i), strconv.Itoa(j))
				}
			}
		} else if k := norm(e.v.Metric) + "|" + string(e.class) + "|" + e.party; norm(e.v.Metric) != "" { // a bare number is never compared
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
		try(metrics[k], "numeric", func(a, b entry) bool { return a.canon != b.canon && !partOf(a, b) && !partOf(b, a) })
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
	return append(out, near(es, out)...)
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
// labelled differently in its document's finding): two values of one class and one party (the figure
// reader's) from two documents that are near each other (within an eighth for a quantity; a month for
// a full date), possibly of one metric (sameMetric), whose quotes share at least three distinctive
// words (or, under two extractor labels, whose context agrees: onePair), when neither document states the other's value anywhere
// (so a total restated beside its parts is not one). Quantities must be salient (nearSalience), and two
// values each only in table rows are never paired (rows of two tables are enumerations, not one fact
// restated). It skips pairs a found conflict already holds.
func near(es []entry, found []Conflict) []Conflict {
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
				a.class != "Date" && (Salience(a.v) < nearSalience || Salience(b.v) < nearSalience) ||
				a.party != b.party || perDoc[a.doc][b.canon] || perDoc[b.doc][a.canon] || partOf(a, b) || partOf(b, a) || !close(a, b) {
				continue
			}
			labelled := shared(a.words, b.words) >= 3 && !(a.row() && b.row()) && sameMetric(a, b)
			if !labelled && (norm(a.v.Entity) == norm(b.v.Entity) || !onePair(a, b)) { // two extractor labels: the context decides
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

// onePair is the near join's context pairing (bench6, SEC: the extractor labelled 4,217 trades "OQR"
// and 4,312 "Whitmore Capital Advisors LLC", $92,600 "Bellini" and $103,800 "Kevin Ostrowski", April
// 15 "Email transmission" and April 22 "deletion instruction", so the labels kept all three bench5
// pairs apart): two close values of one party (the figure reader's, checked by near) whose extractor
// labels disagree are one figure when their metric and quote context agree:
//   - the context: at least two distinctive words (by stem) of the values' quotes are shared (three of
//     the dates' own clauses), and the labels can be one metric (sameMetric, and a content word of the
//     labels shared, or one label with none and three words shared), or both are totals;
//   - two table rows pair only when both state a total (rows are otherwise an enumeration).
func onePair(a, b entry) bool {
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
	return n >= 2 && metric && (sameMetric(a, b) || totals)
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
