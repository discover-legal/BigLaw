// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

// Package ontology is the Go encoding of the BigLaw Epistemic Legal Ontology
// (BELO — see belo.ttl). It provides the controlled class + predicate vocabulary,
// domain/range validation (which rejects and re-orients the extractor's noise), and
// the reified Claim type that the evidence graph stores and every agent reads/writes.
package ontology

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed belo.ttl
var Spec string // the canonical Turtle spec, embedded for provenance/serving

// ─── Classes (Layer 1) ───────────────────────────────────────────────────────

type Class string

const (
	Unknown Class = ""

	Party   Class = "Party"
	Person  Class = "Person"
	Firm    Class = "Firm"
	Fund    Class = "Fund"
	Account Class = "Account"
	Broker  Class = "Broker"
	Client  Class = "Client"

	// Issue is the GENERAL spine node — a distinct proposition the deliverable must assess. The
	// "E" in BELO is epistemic, not enforcement-specific: Conduct (an alleged wrongful scheme) is
	// just the enforcement INSTANTIATION of an Issue. Other practice areas instantiate it as a
	// Requirement (compliance/compare), a Clause (transactional), or a RedFlag (diligence).
	Issue       Class = "Issue"
	Conduct     Class = "Conduct" // enforcement: an alleged wrongful scheme/violation/omission
	Scheme      Class = "Scheme"
	Violation   Class = "Violation"
	Omission    Class = "Omission"
	Requirement Class = "Requirement" // compliance/compare: a client instruction or standard to meet
	Clause      Class = "Clause"      // transactional: a contract clause or obligation
	RedFlag     Class = "RedFlag"     // diligence: a flagged finding

	Authority  Class = "Authority"
	Statute    Class = "Statute"
	Rule       Class = "Rule"
	Provision  Class = "Provision"
	Obligation Class = "Obligation"
	Standard   Class = "Standard"

	Instrument    Class = "Instrument"
	Filing        Class = "Filing"
	Exhibit       Class = "Exhibit"
	Agreement     Class = "Agreement"
	Communication Class = "Communication"

	Quantity   Class = "Quantity"
	Money      Class = "Money"
	Percentage Class = "Percentage"
	Count      Class = "Count"
	DateQ      Class = "Date"
	Identifier Class = "Identifier"

	Event        Class = "Event"
	Jurisdiction Class = "Jurisdiction"
	Matter       Class = "Matter"
)

// parent maps each class to its superclass (for subclass-aware domain/range checks).
var parent = map[Class]Class{
	Person: Party, Firm: Party, Fund: Party, Account: Party, Broker: Party, Client: Party,
	Conduct: Issue, Requirement: Issue, Clause: Issue, RedFlag: Issue, // all are kinds of Issue
	Scheme: Conduct, Violation: Conduct, Omission: Conduct,
	Statute: Authority, Rule: Authority, Provision: Authority, Obligation: Authority, Standard: Authority,
	Filing: Instrument, Exhibit: Instrument, Agreement: Instrument, Communication: Instrument,
	Money: Quantity, Percentage: Quantity, Count: Quantity, DateQ: Quantity, Identifier: Quantity,
}

// knownClass is the set of valid class identifiers (for parsing extractor output).
var knownClass = func() map[Class]bool {
	m := map[Class]bool{Party: true, Issue: true, Conduct: true, Authority: true, Instrument: true, Quantity: true, Event: true, Jurisdiction: true, Matter: true}
	for sub := range parent {
		m[sub] = true
	}
	return m
}()

// ParseClass maps an extractor-supplied class string to a Class, returning Unknown if it
// isn't a recognized BELO class (case-insensitive; tolerates the "belo:" prefix).
func ParseClass(s string) Class {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "belo:"))
	for c := range knownClass {
		if strings.ToLower(string(c)) == s {
			return c
		}
	}
	return Unknown
}

// IsA reports whether c is super or a subclass of super. Unknown matches anything
// (the extractor often leaves a node untyped; we don't reject on missing type).
func (c Class) IsA(super Class) bool {
	if super == Unknown || c == Unknown || c == super {
		return true
	}
	for p := parent[c]; p != ""; p = parent[p] {
		if p == super {
			return true
		}
	}
	return false
}

// ─── Epistemic status (Layer 2) ──────────────────────────────────────────────

type Status string

const (
	Hypothesized Status = "hypothesized"
	Alleged      Status = "alleged"
	Asserted     Status = "asserted"
	Grounded     Status = "grounded"
	Corroborated Status = "corroborated"
	Contested    Status = "contested"
	Verified     Status = "verified"
	Refuted      Status = "refuted"
	Unverified   Status = "unverified"
)

// ─── Predicates (controlled vocabulary with domain/range) ─────────────────────

// Predicate is one controlled relation. Empty Domain/Range means "any". Aliases are
// free-text verb phrases the extractor emits that canonicalize to this predicate.
type Predicate struct {
	Name    string
	Domain  []Class
	Range   []Class
	Aliases []string
}

var predicates = []Predicate{
	{Name: "committedBy", Domain: []Class{Conduct}, Range: []Class{Party},
		Aliases: []string{"committed by", "perpetrated by", "engaged in by", "responsible for", "responsibleparty"}},
	{Name: "violates", Domain: []Class{Conduct}, Range: []Class{Authority},
		Aliases: []string{"violates", "violation of", "in violation of", "breaches", "contravenes"}},
	{Name: "harmed", Domain: []Class{Conduct}, Range: []Class{Party},
		Aliases: []string{"harmed", "caused harm to", "injured", "damaged", "victimized"}},
	// Compliance / comparison / transactional issue predicates (subject is the Issue node — a
	// Requirement, Clause, etc.). These let the epistemic spine fire on non-enforcement matters:
	// a client instruction (Requirement) becomes a spine node via `requires`/`satisfiedBy`.
	{Name: "requires", Domain: []Class{Issue},
		Aliases: []string{"requires", "mandates", "instructs that", "directs that", "calls for", "specifies that", "must provide", "shall provide", "stipulates"}},
	{Name: "satisfiedBy", Domain: []Class{Issue}, Range: []Class{Instrument},
		Aliases: []string{"satisfied by", "met by", "conforms via", "provided in", "addressed in", "implemented in", "reflected in"}},
	{Name: "deviatesFrom", Domain: []Class{Issue},
		Aliases: []string{"deviates from", "departs from", "conflicts with", "inconsistent with", "not met in", "omitted from", "absent from", "missing from"}},
	{Name: "prohibits", Domain: []Class{Issue},
		Aliases: []string{"prohibits", "forbids", "bars", "restricts", "precludes"}},
	{Name: "quantifiedAs", Domain: []Class{Conduct, Party}, Range: []Class{Quantity},
		Aliases: []string{"quantified as", "amounts to", "estimated at", "totaling", "valued at", "has figure"}},
	{Name: "ownsStakeIn", Domain: []Class{Party}, Range: []Class{Party},
		Aliases: []string{"owns stake in", "holds interest in", "owns", "is owner of", "holds interest"}},
	{Name: "receivedFrom", Domain: []Class{Party}, Range: []Class{Party},
		Aliases: []string{"received from", "received compensation from", "received payment from", "paid by"}},
	{Name: "failedToDisclose", Domain: []Class{Party},
		Aliases: []string{"failed to disclose", "did not disclose", "omitted", "concealed", "failed to report"}},
	{Name: "directedTradesTo", Domain: []Class{Party}, Range: []Class{Broker},
		Aliases: []string{"directed trades to", "directed brokerage to", "routed trades to"}},
	{Name: "heldAccountAt", Domain: []Class{Party}, Range: []Class{Broker},
		Aliases: []string{"held account at", "maintained account at", "traded through"}},
	{Name: "alteredRecords", Domain: []Class{Party}, Range: []Class{Instrument},
		Aliases: []string{"altered records", "deleted records", "destroyed records", "deletedoraltered", "instructed deletion of"}},
	{Name: "occurredDuring", Domain: []Class{Conduct}, Range: []Class{Event},
		Aliases: []string{"occurred during", "during", "in the period"}},
	{Name: "requiresElement", Domain: []Class{Authority},
		Aliases: []string{"requires element", "requires", "requires proof of"}},
	{Name: "hasRole", Domain: []Class{Party},
		Aliases: []string{"has role", "serves as", "holds title", "role", "title"}},
	{Name: "citedIn", Range: []Class{Instrument},
		Aliases: []string{"cited in", "stated in", "appears in"}},
}

var byCanon = map[string]*Predicate{}
var byAlias = map[string]*Predicate{}

func init() {
	for i := range predicates {
		p := &predicates[i]
		byCanon[strings.ToLower(p.Name)] = p
		for _, a := range p.Aliases {
			byAlias[norm(a)] = p
		}
	}
}

// Lookup resolves a canonical name or a free-text alias to a controlled Predicate.
// A free-text relation that contains a known alias as a substring also resolves
// (the extractor rarely emits the exact alias).
func Lookup(rel string) (*Predicate, bool) {
	k := norm(rel)
	if p, ok := byCanon[k]; ok {
		return p, true
	}
	if p, ok := byAlias[k]; ok {
		return p, true
	}
	// Token-coverage fallback: an alias matches when ALL its tokens appear in the relation
	// (so "received undisclosed compensation from" resolves "received compensation from"),
	// preferring the most specific (longest) alias. Token-level, not substring, so short
	// aliases can't match inside words ("is" in "disclosed").
	relToks := tokenSet(k)
	var best *Predicate
	bestN := 0
	for i := range predicates {
		for _, a := range predicates[i].Aliases {
			at := tokenSet(a)
			if len(at) == 0 || len(at) <= bestN {
				continue
			}
			covered := true
			for tok := range at {
				if !relToks[tok] {
					covered = false
					break
				}
			}
			if covered {
				best, bestN = &predicates[i], len(at)
			}
		}
	}
	if best != nil {
		return best, true
	}
	return nil, false
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(s)) {
		out[t] = true
	}
	return out
}

// accepts reports whether (sClass, oClass) satisfies the predicate's domain/range.
func (p *Predicate) accepts(sClass, oClass Class) bool {
	return matchAny(sClass, p.Domain) && matchAny(oClass, p.Range)
}

func matchAny(c Class, set []Class) bool {
	if len(set) == 0 {
		return true // unconstrained
	}
	for _, s := range set {
		if c.IsA(s) {
			return true
		}
	}
	return false
}

// ─── Claim (Layer 2) — a reified, ontology-validated triple ───────────────────

type Claim struct {
	ID string

	// Proposition (domain triple)
	S, P, O string
	SClass  Class
	OClass  Class
	Value   string // attached quantity literal, if any

	// Provenance
	AssertedBy   string // Accuser | Respondent | Court | ThirdParty | ""
	ExtractedBy  string // BigLaw agent id
	PracticeArea string
	Round        int

	// Grounding
	Quote  string
	Source string

	// Epistemic
	Status     Status
	Modality   string
	Confidence float64

	// Dialectic (claim IDs)
	Contradicts []string
	Supports    []string
	Rebuts      []string

	VerifiedBy   string
	ChallengedBy string
}

// Normalize maps a raw (subject, relation, object) extraction onto the controlled
// vocabulary and validates domain/range. It returns the canonicalized predicate and,
// crucially, may SWAP subject/object when the relation was stated in reverse (the
// extractor emits "Section 206 violates <conduct>" — domain/range fixes it to
// "<conduct> violates Section 206"). ok=false means the triple isn't a recognized
// domain relation (e.g. neutral/policy text) and should be dropped.
func Normalize(s string, sClass Class, rel, o string, oClass Class) (subj string, sc Class, pred string, obj string, oc Class, ok bool) {
	p, found := Lookup(rel)
	if !found {
		return s, sClass, rel, o, oClass, false
	}
	if p.accepts(sClass, oClass) {
		return s, sClass, p.Name, o, oClass, true
	}
	if p.accepts(oClass, sClass) { // stated in reverse — swap to satisfy domain/range
		return o, oClass, p.Name, s, sClass, true
	}
	// classes unknown on one side → accept (don't over-reject); both-known-and-incompatible → drop
	if sClass == Unknown || oClass == Unknown {
		return s, sClass, p.Name, o, oClass, true
	}
	return s, sClass, rel, o, oClass, false
}

// ─── Analytic layer (Layer 3) — derived defense issues ────────────────────────

// IssueKind classifies a derived DefenseIssue (the belo:DefenseIssue subclasses in
// belo.ttl). Each kind is one analytic template: authority/conduct patterns in the
// grounded claim graph derive the defense angle a lawyer would raise.
type IssueKind string

const (
	DiscrepancyKind      IssueKind = "DiscrepancyIssue"        // cross-source contradiction (contradicts-pair)
	ElementGapKind       IssueKind = "ElementGapIssue"         // charged authority carries a contestable element
	LimitationsKind      IssueKind = "LimitationsIssue"        // limitations window joined to dated conduct
	CriminalExposureKind IssueKind = "CriminalExposureIssue"   // civil conduct with parallel criminal exposure
	MentalStateKind      IssueKind = "MentalStateMappingIssue" // conduct → required mental state mapping / ambiguity
	InnocentReadingKind  IssueKind = "InnocentReadingIssue"    // ambiguous communication admits an innocent reading
)

// DerivedIssue is a Layer-3 analytic conclusion. Unlike a Claim it is not itself
// grounded by a verbatim span — it is DERIVED from grounded claims and the charging
// documents' verbatim text by a curated template, so it carries the claim subject it
// attaches to and, when the derivation rests on a specific span, that span.
type DerivedIssue struct {
	Kind  IssueKind
	About string // the Conduct/Authority the issue attaches to ("" = matter-wide)
	Text  string // the rendered defense analysis
	Quote string // verbatim record span the derivation rests on, when there is one
}

// ─── Lightweight literal classification (fallback when extractor omits a type) ─

var (
	reMoney = regexp.MustCompile(`(?i)^\$|\bmillion\b|\bbillion\b|usd`)
	rePct   = regexp.MustCompile(`%|percent|basis point|bps`)
	reAuth  = regexp.MustCompile(`(?i)\bsection\b|\brule\b|\bitem\b|u\.s\.c|c\.f\.r|§|advisers act|\d+\([a-z0-9]+\)`)
	reDate  = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.? \d|\b(19|20)\d\d\b|\d{1,2}/\d{1,2}/\d{2,4}`)
	reFund  = regexp.MustCompile(`(?i)\b(fund|lp|l\.p\.|ltd|llc)\b`)
	reCount = regexp.MustCompile(`^\d[\d,]*$`)
)

// ClassifyLiteral guesses a node's class from its surface text — used only when the
// extractor leaves the node untyped, so domain/range checks still have something to
// work with. Conservative: returns Unknown rather than guessing wrong.
func ClassifyLiteral(s string) Class {
	t := strings.TrimSpace(s)
	switch {
	case reAuth.MatchString(t):
		return Authority
	case reMoney.MatchString(t):
		return Money
	case rePct.MatchString(t):
		return Percentage
	case reDate.MatchString(t):
		return DateQ
	case reFund.MatchString(t):
		return Fund
	case reCount.MatchString(t):
		return Count
	}
	return Unknown
}

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
