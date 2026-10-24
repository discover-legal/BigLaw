// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"slices"
	"strings"
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
	party  string // the alias-unified party the figure reader read its figure as belonging to
	entity string // the alias-unified entity the extractor typed ("" when none)
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

// fold is a reader's label as the join compares it: lower case, words only, no leading article.
func fold(s string) string {
	s = strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '%' }), " ")
	for _, a := range []string{"the ", "a ", "an "} {
		s = strings.TrimPrefix(s, a)
	}
	return s
}

// Join finds the discrepancies among verified findings (v1's cross-document join, model-free). What a
// figure is, whose, of what and for when are the figure reader's labels (Value.Fig: its kind,
// currency and exact amount, scale applied, in Canon; its party unified by the lead's aliases; what it
// measures and the period it is for, lab-r4 r5 L2): two figures are one quantity only when the
// reader read both as one kind of one party's same measure for the same period, and the extractor's
// typed entities do not name two others (r5 M4: a payer read as the party of two employees'
// salaries), unless the reader says a typed entity is the party itself, or no party at all ("Email",
// a table's title); a figure whose typed entity the reader says is another party joins nothing. A figure with no reading, an unclear one, or no party or measure is never part of a
// conflict: no word test in Go decides whose figure it is or what it measures. A conflict needs two
// documents whose value sets are disjoint, so a list restated in both is not one. Durations and
// identifiers (kind other) are never compared, and a quarantined finding never joins.
func Join(findings []Finding, aliases []Alias) []Conflict {
	key := func(s string) string { return strings.TrimPrefix(norm(s), "the ") }
	parties := sets{}
	for _, a := range aliases {
		for _, o := range a.Aliases {
			if key(a.Name) != "" && key(o) != "" {
				parties.union(key(a.Name), key(o))
			}
		}
	}
	var es []entry
	groups := map[string][]int{}
	var order []string
	for _, f := range findings {
		for _, v := range f.Values {
			class, canon, d, ok := Canon(v)
			if !ok || !f.Verified || v.Fig == nil || key(v.Fig.Party) == "" || fold(v.Fig.Measures) == "" { // no reading, party or measure: no conflict is asserted on it
				continue
			}
			if v.Fig.TypedEntity == "other" { // the reader's party is not the typed entity's: two parties, no conflict asserted (r5 M4)
				continue
			}
			e := entry{v, f.FID, class, canon, d, docOf(f, v.Value), parties.find(key(v.Fig.Party)), ""}
			switch {
			case v.Fig.TypedEntity == "party": // the reader says the typed entity names its party
				e.entity = e.party
			case key(v.Entity) != "" && v.Fig.TypedEntity == "": // the typed entity stands beside the reader's party
				e.entity = parties.find(key(v.Entity))
			}
			k := string(class) + "|" + e.party + "|" + fold(v.Fig.Measures) + "|" + fold(v.Fig.Period)
			if groups[k] == nil {
				order = append(order, k)
			}
			groups[k] = append(groups[k], len(es))
			es = append(es, e)
		}
	}
	var out []Conflict
	for _, k := range order {
		idx := groups[k]
		kind, differ := "numeric", func(a, b entry) bool { return a.canon != b.canon }
		if es[idx[0]].class == "Date" {
			kind, differ = "date", func(a, b entry) bool { return !a.date.Compatible(b.date) }
		}
		if c, ok := conflict(es, idx, kind, func(a, b entry) bool {
			return (a.entity == b.entity || a.entity == "" || b.entity == "") && differ(a, b) // two typed entities: two figures
		}); ok {
			out = append(out, c)
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
						if j < i { // in the findings' order, whatever the documents' ids
							a, b = b, a
						}
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
