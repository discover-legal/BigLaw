// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
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
				es = append(es, entry{v, f.FID, class, canon, d, docOf(f, v.Value), referents(f, v.Value), qs})
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
				if es[j].class == "Date" && (norm(e.v.Metric) != "" && norm(e.v.Metric) == norm(es[j].v.Metric) || !(e.row() && es[j].row()) && shared(e.words, es[j].words) >= 2) {
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
			try(byEnt[r], "numeric", func(a, b entry) bool { return a.canon != b.canon })
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
	return append(out, near(es, out)...)
}

// maxNear bounds the near conflicts one join reports: they are leads to check, not a column dump.
// nearSalience is the least salient quantity compared (any percentage, money from $1,000, a count
// of thousands): a share price or a day count is too common for nearness to mean one fact.
const (
	maxNear      = 20
	nearSalience = 45
)

// near finds the conflicts the label join cannot see (bench r3, SEC: 4,217 vs 4,312 trades
// analysed, $92,600 vs $103,800 paid to the club, 73% vs 78% of volume, April 22 vs April 15, each
// labelled differently in its document's finding): two values of one class from two documents that
// are near each other (within a quarter for a quantity; a month for a full date) and whose quotes
// share at least three distinctive words, when neither document states the other's value anywhere
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
		x, okx := Number(strings.SplitN(a.canon, ":", 2)[1])
		y, oky := Number(strings.SplitN(b.canon, ":", 2)[1])
		if !okx || !oky {
			return false
		}
		fx, _ := x.Float64()
		fy, _ := y.Float64()
		hi := max(fx, fy, -fx, -fy)
		return hi > 0 && fx != fy && (fx-fy <= hi/4 && fy-fx <= hi/4)
	}
	var out []Conflict
	seen := map[string]bool{}
	for i, a := range es {
		for _, b := range es[i+1:] {
			if a.class != b.class || a.doc == b.doc || a.doc == "" || b.doc == "" || held[a.fid+"|"+a.canon] && held[b.fid+"|"+b.canon] ||
				a.class != "Date" && (Salience(a.v.Value) < nearSalience || Salience(b.v.Value) < nearSalience) || a.row() && b.row() ||
				perDoc[a.doc][b.canon] || perDoc[b.doc][a.canon] || !close(a, b) || shared(a.words, b.words) < 3 {
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
		for _, t := range strings.Fields(strings.ToLower(strings.ReplaceAll(q.Quote, value, " "))) {
			t = strings.TrimSuffix(strings.Trim(t, ".,;:()[]{}\"'`|—–"), "'s")
			if len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
				t = t[:len(t)-1]
			}
			if len(t) >= 3 && !strings.ContainsAny(t, "0123456789") && !stop[t] {
				out[t] = true
			}
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
