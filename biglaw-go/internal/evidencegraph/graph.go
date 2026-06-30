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
	"regexp"
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
	claims    []ontology.Claim // each kept Fact, mapped onto BELO (canonical predicate, classes, status)
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
	// Map the grounded fact onto BELO: classify the nodes, canonicalize the predicate, and
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

// AddTriple stores a typed, ontology-recognized triple (the controlled-vocabulary extraction
// path). Unlike Add (which keeps any grounded fact, mapping it best-effort), AddTriple keeps
// ONLY triples whose predicate is a controlled BELO predicate that validates against
// domain/range (re-orienting reversed ones) — so the Conduct graph the spine derives from
// stays clean. Classes come from the extractor; an unrecognized class falls back to literal
// classification. Grounded (quote must be verbatim in sourceText) and deduped.
func (g *Graph) AddTriple(s, sClass, rel, o, oClass, value, quote, source, sourceText string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	sc := classOf(sClass, s)
	oc := classOf(oClass, o)
	ss, scl, pp, oo, ocl, ok := ontology.Normalize(s, sc, rel, o, oc)
	if !ok {
		return false // not a recognized controlled domain relation
	}
	// Grounding with span-snapping: prefer the model's verbatim quote; if it paraphrased, snap
	// to the nearest real span. The stored quote is always verbatim (invariant preserved); if
	// nothing clears the coverage threshold, drop the triple rather than mis-attach.
	gq := strings.TrimSpace(quote)
	if gq == "" || !grounded(gq, sourceText) {
		if gq = snapToSpan(quote, sourceText); gq == "" {
			return false
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	k := norm(ss + "|" + pp + "|" + oo + "|" + value)
	if g.seen[k] {
		return false
	}
	g.seen[k] = true
	g.claims = append(g.claims, ontology.Claim{
		S: ss, SClass: scl, P: pp, O: oo, OClass: ocl, Value: value,
		Quote: gq, Source: source, Status: ontology.Grounded,
	})
	g.facts = append(g.facts, Fact{Subject: ss, Relation: pp, Object: oo, Value: value, Quote: gq, Source: source})
	return true
}

func classOf(declared, surface string) ontology.Class {
	if c := ontology.ParseClass(declared); c != ontology.Unknown {
		return c
	}
	return ontology.ClassifyLiteral(surface)
}

// Claims returns the BELO-mapped claims (a copy).
func (g *Graph) Claims() []ontology.Claim {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ontology.Claim(nil), g.claims...)
}

// Issues returns the distinct ISSUE nodes — the subjects of issue-domain predicates. These are
// the matter's organizing propositions, discovered from the typed graph rather than enumerated;
// each is a spine anchor. The "E" in BELO is epistemic: an Issue is whatever the deliverable
// must assess — an alleged Conduct (enforcement: committedBy/violates/harmed) OR a Requirement
// (compliance/compare: requires/satisfiedBy/deviatesFrom) OR a Clause — so the spine fires
// across practice areas, not just enforcement.
func (g *Graph) Issues() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, c := range g.claims {
		switch c.P {
		case "committedBy", "violates", "harmed", "occurredDuring", // enforcement (Conduct)
			"requires", "satisfiedBy", "deviatesFrom", "prohibits": // compliance/compare (Requirement/Clause)
			// The subject must BE an Issue — drop noise where a Party/Authority slipped in as the
			// subject (an un-swapped "Section 7.3 violates …"). Unknown is allowed (untyped).
			if !c.SClass.IsA(ontology.Issue) {
				continue
			}
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

// Conducts is the enforcement-named alias for Issues (Issue-type = Allegation). Kept so existing
// callers and tests read naturally; the canonical, practice-area-general accessor is Issues.
func (g *Graph) Conducts() []string { return g.Issues() }

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

// Span-snapping: a weak model identifies WHAT a triple is but paraphrases the verbatim quote,
// so the grounding gate drops ~80% of real triples. snapToSpan recovers grounding by finding
// the actual span in sourceText whose content tokens best COVER the paraphrase, returning that
// real (verbatim) span. If no span clears the coverage threshold it returns "" — a paraphrase
// matching nothing is dropped, never mis-attached. This keeps the verbatim grounding INVARIANT
// (the stored quote is always a real span) while tolerating the model's loose transcription.
const snapMinCoverage = 0.6 // fraction of the quote's content tokens that must appear in the span
const snapMinTokens = 3     // need enough signal to snap safely (avoid matching on a word or two)

var sentenceSplit = regexp.MustCompile(`[.;\n\r|]+`)

func snapToSpan(quote, sourceText string) string {
	qt := contentTokens(quote)
	if len(qt) < snapMinTokens {
		return ""
	}
	best, bestCov := "", snapMinCoverage
	for _, raw := range sentenceSplit.Split(sourceText, -1) {
		span := strings.TrimSpace(raw)
		if len(span) < 12 {
			continue
		}
		st := contentTokens(span)
		if len(st) == 0 {
			continue
		}
		covered := 0
		for t := range qt {
			if st[t] {
				covered++
			}
		}
		if cov := float64(covered) / float64(len(qt)); cov >= bestCov {
			best, bestCov = span, cov
		}
	}
	return best
}

var snapStop = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "with": true, "this": true, "was": true,
	"are": true, "its": true, "had": true, "has": true, "not": true, "did": true, "any": true,
	"all": true, "from": true, "into": true, "such": true, "which": true, "were": true, "their": true,
}

func contentTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(s)) {
		t = strings.Trim(t, ".,;:()[]{}\"'`")
		if len(t) >= 3 && !snapStop[t] {
			out[t] = true
		}
	}
	return out
}

func dedupKey(f Fact) string {
	return norm(f.Subject + "|" + f.Relation + "|" + f.Object + "|" + f.Value)
}
