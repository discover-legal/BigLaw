// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package analysis holds BigLaw's deterministic memo tools, as pure functions over verified
// findings: numbers (calc, figure handles, the key-figures table), join_discrepancies, the
// coverage checks finish{op:"check"} runs, the value normalisers, and BELO-lite, the typed
// vocabulary of findings and issue buckets. It imports no internal package but agent.
package analysis

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// Class is a BELO-lite class (belo.json); "" is unknown, which matches anything.
type Class string

//go:embed belo.json
var beloJSON []byte

// Predicate is a controlled relation with its domain and range (empty: unconstrained).
type Predicate struct {
	Name          string
	Domain, Range []Class
	Aliases       []string
}

var belo = func() (v struct {
	Classes      map[Class]Class
	IssueClasses []Class
	Predicates   []Predicate
}) {
	if err := json.Unmarshal(beloJSON, &v); err != nil {
		panic("analysis: belo.json: " + err.Error())
	}
	return v
}()

// ParseClass reads a class name case-insensitively; an unknown name is "".
func ParseClass(s string) Class {
	for c := range belo.Classes {
		if strings.EqualFold(string(c), strings.TrimSpace(s)) {
			return c
		}
	}
	return ""
}

// IsA reports whether c is super or a subclass of it; unknown on either side matches.
func (c Class) IsA(super Class) bool {
	for x, seen := c, 0; seen < 8; x, seen = belo.Classes[x], seen+1 {
		if super == "" || c == "" || x == super {
			return true
		}
		if x == "" {
			return false
		}
	}
	return false
}

// IssueClasses are the classes a plan's issues (the deliverable's spine) are typed by.
func IssueClasses() []Class { return slices.Clone(belo.IssueClasses) }

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Lookup maps a free-text relation onto a controlled predicate: its name, an alias, or the
// longest alias whose words all occur in it.
func Lookup(rel string) (*Predicate, bool) {
	k := norm(rel)
	var best *Predicate
	bestN := 0
	for i := range belo.Predicates {
		p := &belo.Predicates[i]
		if strings.ToLower(p.Name) == k || slices.Contains(p.Aliases, k) {
			return p, true
		}
		for _, a := range p.Aliases {
			if at := strings.Fields(a); len(at) > bestN && allIn(at, strings.Fields(k)) {
				best, bestN = p, len(at)
			}
		}
	}
	return best, best != nil
}

func allIn(words, in []string) bool {
	for _, w := range words {
		if !slices.Contains(in, w) {
			return false
		}
	}
	return true
}

func matchAny(c Class, set []Class) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if c.IsA(s) {
			return true
		}
	}
	return false
}

// Normalize maps (subject, relation, object) onto the vocabulary and checks domain and range. A
// triple stated in reverse ("Section 206 violates the scheme") is re-oriented; a predicate the
// vocabulary does not know, or classes that fit neither way, is dropped (ok false).
func Normalize(s string, sc Class, rel, o string, oc Class) (subj string, sClass Class, pred, obj string, oClass Class, ok bool) {
	p, found := Lookup(rel)
	switch {
	case !found:
		return s, sc, rel, o, oc, false
	case matchAny(sc, p.Domain) && matchAny(oc, p.Range):
		return s, sc, p.Name, o, oc, true
	case matchAny(oc, p.Domain) && matchAny(sc, p.Range):
		return o, oc, p.Name, s, sc, true
	}
	return s, sc, rel, o, oc, false
}

var (
	rePct  = regexp.MustCompile(`(?i)\bbasis points?\b|\bbps\b`)
	reAuth = regexp.MustCompile(`(?i)\bsection\b|\brule\b|\bitem\b|u\.s\.c|c\.f\.r|§|\bact\b|\d+\([a-z0-9]+\)`)
	reDate = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.? \d|\b(19|20)\d\d\b|\d{1,2}/\d{1,2}/\d{2,4}`)
	reFund = regexp.MustCompile(`(?i)\b(fund|lp|l\.p\.)\b`)
)

// ClassifyLiteral guesses the class of an untyped value or name from its text, conservatively:
// a party name it cannot place is unknown, never a guess.
func ClassifyLiteral(s string) Class {
	t := strings.TrimSpace(s)
	switch {
	case reAuth.MatchString(t):
		return "Authority"
	case rePct.MatchString(t):
		return "Percentage"
	}
	if _, fs := Figures(t); len(fs) == 1 { // the figure parser reads the quantity: "$2000", "1,5 %", "4,217"
		switch f := fs[0]; {
		case f.Class == MoneyFig || f.Class == UnknownFig && f.Scaled:
			return "Money"
		case f.Class == PercentFig:
			return "Percentage"
		case f.Class == CountFig || f.Class == UnknownFig && f.Start == 0 && f.End == len(t):
			if !reDate.MatchString(t) {
				return "Count"
			}
		}
	}
	switch {
	case reDate.MatchString(t):
		return "Date"
	case reFund.MatchString(t):
		return "Fund"
	}
	return ""
}

// Relations types each relation's ends from the finding's entities (else ClassifyLiteral),
// re-orients reversed triples and drops ill-typed ones, as Go drops an ungrounded quote.
func Relations(entities []Entity, rels []Relation) (kept, dropped []Relation) {
	classOf := func(name string) Class {
		for _, e := range entities {
			if norm(e.Name) == norm(name) {
				return ParseClass(string(e.Class))
			}
		}
		return ClassifyLiteral(name)
	}
	for _, r := range rels {
		s, _, p, o, _, ok := Normalize(r.Subject, classOf(r.Subject), r.Predicate, r.Object, classOf(r.Object))
		if !ok {
			dropped = append(dropped, r)
			continue
		}
		kept = append(kept, Relation{s, p, o})
	}
	return kept, dropped
}
