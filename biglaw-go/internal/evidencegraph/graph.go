// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Package evidencegraph is the Lite (in-process) evidence-graph seam: a per-matter store
// of GROUNDED facts — typed entity attributes and relations, each carrying the verbatim
// source span that proves it. Flat findings (free text) can't hold relations, so the
// synthesis writer mis-attributes and drops them; the graph gives attribution structure
// (e.g. a "victim-of → directed-brokerage" edge can't render under cherry-picking) and
// lets each party's exposure be rendered as "every fact touching that node".
//
// "Grounded" is enforced: a fact whose quote is not a verbatim substring of the source is
// DROPPED, never kept — a wrong edge in the substrate is worse than a missing one (it
// would bake in the very mis-attribution we are trying to fix). The Full (TypeDB) seam in
// internal/graph is separate (firm-wide conflicts, typed inference); this is per-matter.
package evidencegraph

import (
	"strings"
	"sync"

	"github.com/discover-legal/biglaw-go/internal/ontology"
)

// Fact is one grounded triple. Entity attributes (e.g. "Ostrowski — 40% owner of —
// Lakeshore Trading") and relations (e.g. "Crescent Bay — victim of — directed-brokerage
// scheme") use the same shape: Object may be empty for a pure attribute. Value carries an
// attached figure/percent/date when present. Quote is the verbatim span that grounds it.
type Fact struct {
	Subject  string
	Relation string
	Object   string
	Value    string
	Quote    string
	Source   string
}

// Graph is an in-process, append-only set of grounded facts, deduped. It also collects
// the matter's distinct ALLEGATIONS (grounded headings) — the coverage spine derives from
// these, so the section set is anchored to the same grounded extraction as the facts
// (instead of a separate, run-to-run-varying enumeration).
type Graph struct {
	mu        sync.Mutex
	facts     []Fact
	claims    []ontology.Claim // each kept Fact, mapped onto BLEO (canonical predicate, classes, status)
	seen      map[string]bool
	allegs    []string
	allegSeen map[string]bool
}

func New() *Graph { return &Graph{seen: map[string]bool{}, allegSeen: map[string]bool{}} }

// AddAllegation records a distinct allegation heading, grounded by a verbatim quote
// (the heading itself is synthesized, so we ground on the quote that evidences it).
// Deduped by exact lowercased label; finer theme-dedup is the caller's job.
func (g *Graph) AddAllegation(label, quote, sourceText string) bool {
	label = strings.TrimSpace(label)
	if label == "" || len(label) < 4 || len(label) > 90 {
		return false
	}
	if !grounded(quote, sourceText) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	k := norm(label)
	if g.allegSeen[k] {
		return false
	}
	g.allegSeen[k] = true
	g.allegs = append(g.allegs, label)
	return true
}

// Allegations returns the grounded allegation headings, in insertion order.
func (g *Graph) Allegations() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.allegs...)
}

// Add stores a fact iff it is grounded (Quote is a verbatim substring of source, modulo
// whitespace/case) and not a duplicate. Returns whether it was kept — callers can count
// the grounded-vs-rejected ratio. An empty Subject or Quote is always rejected.
func (g *Graph) Add(f Fact, sourceText string) bool {
	if strings.TrimSpace(f.Subject) == "" || strings.TrimSpace(f.Quote) == "" {
		return false
	}
	if !grounded(f.Quote, sourceText) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	k := dedupKey(f)
	if g.seen[k] {
		return false
	}
	g.seen[k] = true
	g.facts = append(g.facts, f)
	// Map the grounded fact onto BLEO: classify the nodes, canonicalize the predicate, and
	// re-orient if stated in reverse (domain/range). Unrecognized relations are kept raw — a
	// grounded fact is still evidence even if it isn't a controlled domain predicate.
	sc := ontology.ClassifyLiteral(f.Subject)
	oc := ontology.ClassifyLiteral(f.Object)
	s, scl, p, o, ocl, _ := ontology.Normalize(f.Subject, sc, f.Relation, f.Object, oc)
	g.claims = append(g.claims, ontology.Claim{
		S: s, SClass: scl, P: p, O: o, OClass: ocl, Value: f.Value,
		Quote: f.Quote, Source: f.Source, Status: ontology.Grounded,
	})
	return true
}

// Claims returns the BLEO-mapped claims (a copy).
func (g *Graph) Claims() []ontology.Claim {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ontology.Claim(nil), g.claims...)
}

// Conducts returns the distinct Conduct nodes — the subjects of conduct-domain predicates
// (committedBy / violates / harmed / occurredDuring). These ARE the allegations, discovered
// from the typed graph rather than enumerated; each is the spine anchor for its evidence.
func (g *Graph) Conducts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, c := range g.claims {
		switch c.P {
		case "committedBy", "violates", "harmed", "occurredDuring":
			s := strings.TrimSpace(c.S)
			if s == "" {
				continue
			}
			if k := strings.ToLower(s); !seen[k] {
				seen[k] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// All returns a copy of the stored facts.
func (g *Graph) All() []Fact {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Fact(nil), g.facts...)
}

func (g *Graph) Len() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.facts) }

// Entities returns the distinct named entities in the graph (every fact subject/object),
// in first-seen order — the nodes a harvested figure can be bound to.
func (g *Graph) Entities() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, f := range g.facts {
		for _, n := range []string{f.Subject, f.Object} {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if k := strings.ToLower(n); !seen[k] {
				seen[k] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// FactsAbout returns every fact whose subject OR object mentions any of the given names
// (case-insensitive substring) — a party/entity's full grounded profile, for rendering
// its exposure or for attributing it to the right section.
func (g *Graph) FactsAbout(names ...string) []Fact {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Fact
	for _, f := range g.facts {
		hay := strings.ToLower(f.Subject + " " + f.Object)
		for _, n := range names {
			n = strings.ToLower(strings.TrimSpace(n))
			if n != "" && strings.Contains(hay, n) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// Render formats a set of facts as a compact grounded-fact block for a synthesis prompt.
func Render(facts []Fact) string {
	if len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range facts {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(f.Subject))
		if r := strings.TrimSpace(f.Relation); r != "" {
			b.WriteString(" ")
			b.WriteString(r)
		}
		if o := strings.TrimSpace(f.Object); o != "" {
			b.WriteString(" ")
			b.WriteString(o)
		}
		if v := strings.TrimSpace(f.Value); v != "" {
			b.WriteString(" (")
			b.WriteString(v)
			b.WriteString(")")
		}
		if s := strings.TrimSpace(f.Source); s != "" {
			b.WriteString(" [")
			b.WriteString(s)
			b.WriteString("]")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// grounded reports whether quote appears verbatim in sourceText, comparing on
// whitespace-collapsed, lowercased forms (the source text is pre-normalized upstream, but
// a model may re-space its quote). Empty sourceText skips the check (caller chose not to
// supply it) — but Add always passes the chunk text, so the gate is live in practice.
func grounded(quote, sourceText string) bool {
	if strings.TrimSpace(sourceText) == "" {
		return true
	}
	return strings.Contains(norm(sourceText), norm(quote))
}

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func dedupKey(f Fact) string {
	return norm(f.Subject + "|" + f.Relation + "|" + f.Object + "|" + f.Value)
}
