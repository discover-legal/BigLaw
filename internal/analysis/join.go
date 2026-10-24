// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/discover-legal/BigLaw/internal/agent"
)

// The cross-document join (lab-r4 r7, owner decision: meaning comes from the agent): Go only
// proposes candidate pairs, two figures the figure reader read clearly as one kind in one currency,
// from two findings in two documents, whose values differ (Candidates); whether they are one quantity
// stated two ways is a judge's verdict per pair (the engine's judge_figures: yes, no or unclear, with
// a reason quoting the passages), and only "yes" asserts a conflict (Conflicts). No rule in Go
// decides whose figure it is, what it measures, for which period or who else is named: the reader's
// party, counterparty, measure and period labels are context the judge reads, never a key.

// Conflict is one cross-document discrepancy: two figures a judge read as one quantity, stated
// differently in two documents.
type Conflict struct {
	Entity string        `json:"entity"`
	Metric string        `json:"metric"`
	Kind   string        `json:"kind"` // numeric or date
	Values []ConflictVal `json:"values"`
	Reason string        `json:"reason"` // the judge's, quoting the passages
}

type ConflictVal struct {
	Value      string `json:"value"`
	Canon      string `json:"canon"`
	DocumentID string `json:"documentId"`
	FID        string `json:"fid"`
}

// Alias names one party and the other names the record uses for it (the lead supplies these): context
// the judge reads.
type Alias struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
}

// Side is one figure of a candidate pair: its value, where it is stated and how it was read.
type Side struct {
	Ref        string   `json:"ref"` // "F3.v1"
	FID        string   `json:"fid"`
	DocumentID string   `json:"documentId"`
	Value      string   `json:"value"`
	Canon      string   `json:"canon"`
	Entity     string   `json:"entity"`
	Metric     string   `json:"metric"`
	Claim      string   `json:"claim"`
	Quotes     []string `json:"quotes"`
	Fig        Reading  `json:"reading"`
}

// Candidate is one pair the judge decides.
type Candidate struct {
	ID   string `json:"id"`   // "F3.v1|F9.v2"
	Kind string `json:"kind"` // numeric or date
	A, B Side
}

// Verdict is the judge's answer for one pair.
type Verdict struct {
	Pair         string `json:"pair"`
	SameQuantity string `json:"sameQuantity"` // yes, no or unclear
	Reason       string `json:"reason"`
}

// Judge decides candidate pairs (the engine's judge_figures, out of the call's grant); a pair it
// could not judge has no verdict.
type Judge interface {
	Judge(ctx context.Context, c agent.Call, cands []Candidate) (map[string]Verdict, agent.Usage, error)
}

// JudgePairs is how many candidate pairs one judge call decides; a join judges at most JudgePairs
// for each call its grant funds (CandidateLimit).
const JudgePairs = 20

// CandidateLimit is how many pairs a grant of calls judges (lab-r4 r8 M1: the cap is the judge's
// budget, not a constant).
func CandidateLimit(calls int) int { return max(0, calls) * JudgePairs }

// nearest is how many counterparts of each value come first, closest first (rankPairs).
const nearest = 3

// Prior is a Judge that knows which pairs it has judged already (the engine's ledger).
type Prior interface {
	Judged(ctx context.Context, c agent.Call) (func(Candidate) bool, error)
}

// Candidates are the pairs of verified findings' values that could be one quantity stated two ways:
// both read clearly (Canon: the reader's kind, currency and exact amount; a date as written, its
// reading not unclear), of one kind and currency, from two findings in two documents, their values
// different (a date: not compatible at its precision). Two values in two documents are asked once,
// however many findings repeat them. Nothing else is filtered: whether a pair is one quantity is the
// judge's. Pairs judged already (judged; nil: none) come first, whatever the limit, so a verdict
// never falls out of the join as more findings arrive (r8 M1); then the rest in rankPairs' order, up
// to limit pairs in all; dropped is how many more there were.
func Candidates(findings []Finding, limit int, judged func(Candidate) bool) (cands []Candidate, dropped int) {
	ranked := rankPairs(findings)
	var rest []Candidate
	for _, c := range ranked {
		if judged != nil && judged(c) {
			cands = append(cands, c)
		} else {
			rest = append(rest, c)
		}
	}
	for _, c := range rest {
		if len(cands) >= limit {
			dropped++
			continue
		}
		cands = append(cands, c)
	}
	return cands, dropped
}

// side is one comparable value with what it is compared by.
type side struct {
	Side
	date Date
	ctx  map[string]float64 // its context's words, tf-idf weighted (norm 1)
}

// rankPairs are every candidate pair, in a fixed order that never depends on two labels being worded
// alike (r8 M1): for each value its nearest counterparts first (by how alike the two values' contexts
// are, then by how close their amounts or dates are), a pair placed by the better of its two values'
// rankings; within a rank, the most alike first. A context is the words of the value's typed entity
// and metric, the reader's party, counterparty and measure, and its finding's claim, weighted by how
// rare each word is among the values (tf-idf), compared by cosine: no word list, no exact match.
func rankPairs(findings []Finding) []Candidate {
	var sides []side
	for _, f := range findings {
		if !f.Verified {
			continue
		}
		var qs []string
		for _, q := range f.Quotes {
			qs = append(qs, q.Quote)
		}
		for j, v := range f.Values {
			_, canon, d, ok := Canon(v)
			if !ok || v.Fig == nil || v.Fig.Unclear || v.Fig.Kind == KindOther || v.Fig.Kind == KindDuration {
				continue
			}
			sides = append(sides, side{Side: Side{Ref: fmt.Sprintf("%s.v%d", f.FID, j+1), FID: f.FID, DocumentID: docOf(f, v.Value), Value: v.Value, Canon: canon,
				Entity: v.Entity, Metric: v.Metric, Claim: f.Claim, Quotes: qs, Fig: *v.Fig}, date: d})
		}
	}
	weigh(sides)
	type pair struct {
		c         Candidate
		sim, dist float64
		rank      int
		key       [4]string
		ia, ib    int
	}
	var all []pair
	seen := map[[4]string]int{} // one question per two values in two documents: the most alike pair stands for them
	for i, a := range sides {
		for k := i + 1; k < len(sides); k++ {
			b := sides[k]
			ka, _, _ := strings.Cut(a.Canon, ":")
			kb, _, _ := strings.Cut(b.Canon, ":")
			if ka != kb || a.FID == b.FID || a.DocumentID == b.DocumentID || a.DocumentID == "" || b.DocumentID == "" || a.Canon == b.Canon {
				continue
			}
			kind, dist := "numeric", 0.0
			if ka == "date" {
				if a.date.Compatible(b.date) {
					continue
				}
				kind = "date"
				dist = math.Abs(a.date.time().Sub(b.date.time()).Hours()/24) / 3650 // ten years apart is as far as twice the amount
			} else if _, x, okx := CanonAmount(a.Canon); okx {
				_, y, _ := CanonAmount(b.Canon)
				fx, _ := x.Float64()
				fy, _ := y.Float64()
				if hi := math.Max(math.Abs(fx), math.Abs(fy)); hi > 0 {
					dist = math.Abs(fx-fy) / hi
				}
			}
			x, y, ix, iy := a, b, i, k
			if y.Ref < x.Ref {
				x, y, ix, iy = y, x, iy, ix
			}
			key := [4]string{x.Canon, x.DocumentID, y.Canon, y.DocumentID}
			if x.Canon > y.Canon || x.Canon == y.Canon && x.DocumentID > y.DocumentID {
				key = [4]string{y.Canon, y.DocumentID, x.Canon, x.DocumentID}
			}
			p := pair{c: Candidate{ID: x.Ref + "|" + y.Ref, Kind: kind, A: x.Side, B: y.Side}, sim: cosine(a.ctx, b.ctx), dist: dist, key: key, ia: ix, ib: iy}
			if at, ok := seen[key]; ok {
				if p.sim > all[at].sim || p.sim == all[at].sim && p.c.ID < all[at].c.ID {
					all[at] = p
				}
				continue
			}
			seen[key] = len(all)
			all = append(all, p)
		}
	}
	score := func(p pair) float64 { return rankScore(p.sim, p.dist) }
	better := func(p, q pair) int { // the more alike and closer, then by id
		return cmp.Or(cmp.Compare(score(q), score(p)), strings.Compare(p.c.ID, q.c.ID))
	}
	by := make([][]int, len(sides)) // each value's pairs, best first
	for n, p := range all {
		by[p.ia], by[p.ib] = append(by[p.ia], n), append(by[p.ib], n)
	}
	for i := range all {
		all[i].rank = math.MaxInt
	}
	for _, ns := range by {
		slices.SortFunc(ns, func(m, n int) int { return better(all[m], all[n]) })
		for r, n := range ns[:min(nearest, len(ns))] {
			all[n].rank = min(all[n].rank, r)
		}
	}
	slices.SortFunc(all, func(p, q pair) int { return cmp.Or(cmp.Compare(p.rank, q.rank), better(p, q)) })
	out := make([]Candidate, len(all))
	for i, p := range all {
		out[i] = p.c
	}
	return out
}

var rankScore = func(sim, dist float64) float64 { return sim * (1 - math.Min(dist, 1)) }

// weigh gives each side its context's tf-idf weights (norm 1).
func weigh(sides []side) {
	df := map[string]int{}
	bags := make([]map[string]int, len(sides))
	for i, s := range sides {
		bags[i] = map[string]int{}
		for _, w := range strings.Fields(fold(strings.Join([]string{s.Entity, s.Metric, s.Fig.Party, s.Fig.Counterparty, s.Fig.Measures, s.Claim}, " "))) {
			if len([]rune(w)) < 3 || strings.IndexFunc(w, unicode.IsLetter) < 0 {
				continue
			}
			if bags[i][w] == 0 {
				df[w]++
			}
			bags[i][w]++
		}
	}
	for i := range sides {
		v, norm := map[string]float64{}, 0.0
		for w, n := range bags[i] {
			x := float64(n) * math.Log(float64(1+len(sides))/float64(df[w]))
			v[w], norm = x, norm+x*x
		}
		for w := range v {
			v[w] /= math.Sqrt(norm)
		}
		sides[i].ctx = v
	}
}

func cosine(a, b map[string]float64) float64 {
	if len(b) < len(a) {
		a, b = b, a
	}
	s := 0.0
	for w, x := range a {
		s += x * b[w]
	}
	return s
}

// CheckVerdicts keeps the judge's verdicts for pairs it was given: one for an id it was not given is
// rejected (a note), a second for one pair makes it unclear, an answer other than yes, no or unclear
// is unclear, and so is a yes or no whose reason does not quote both passages (quoted).
func CheckVerdicts(cands []Candidate, raw []Verdict) (map[string]Verdict, []string) {
	out := map[string]Verdict{}
	var notes []string
	for _, v := range raw {
		i := slices.IndexFunc(cands, func(c Candidate) bool { return c.ID == v.Pair })
		if i < 0 {
			notes = append(notes, fmt.Sprintf("a verdict for %q was rejected: no such pair was given", v.Pair))
			continue
		}
		if !slices.Contains([]string{"yes", "no", "unclear"}, v.SameQuantity) {
			v.SameQuantity = "unclear"
		}
		if v.SameQuantity != "unclear" && !quoted(v.Reason, cands[i]) {
			v.SameQuantity, v.Reason = "unclear", "the judge's reason quotes neither passage as written: "+v.Reason
		}
		if _, twice := out[v.Pair]; twice {
			v.SameQuantity, v.Reason = "unclear", "judged twice"
		}
		out[v.Pair] = v
	}
	return out, notes
}

// quoteRun is how many words in a row of a side's quotes or claim a reason must repeat to quote it
// (all of them, for a shorter text).
const quoteRun = 4

// quoted reports whether a reason quotes both sides (lab-r4 r8 L1): it repeats a run of quoteRun
// words of one of each side's quotes or its claim, as written (case, punctuation and white space
// aside). A reason that quotes neither passage, or only one, is no reading of both.
func quoted(reason string, c Candidate) bool {
	words := func(x string) []string {
		var out []string
		for _, w := range strings.Fields(strings.ToLower(x)) {
			if w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }); w != "" {
				out = append(out, w)
			}
		}
		return out
	}
	said := " " + strings.Join(words(reason), " ") + " "
	quotes := func(s Side) bool {
		for _, t := range append(slices.Clone(s.Quotes), s.Claim) {
			ws := words(t)
			n := min(quoteRun, len(ws))
			for i := 0; n > 0 && i+n <= len(ws); i++ {
				if strings.Contains(said, " "+strings.Join(ws[i:i+n], " ")+" ") {
					return true
				}
			}
		}
		return false
	}
	return quotes(c.A) && quotes(c.B)
}

// Conflicts are the candidates the judge said are one quantity ("yes"), each with its reason; a pair
// judged no, unclear or not at all asserts nothing.
func Conflicts(cands []Candidate, verdicts map[string]Verdict) []Conflict {
	var out []Conflict
	for _, c := range cands {
		v, ok := verdicts[c.ID]
		if !ok || v.SameQuantity != "yes" {
			continue
		}
		out = append(out, Conflict{Entity: cmp.Or(c.A.Entity, c.B.Entity, c.A.Fig.Party, c.B.Fig.Party), Metric: cmp.Or(c.A.Metric, c.B.Metric, c.A.Fig.Measures, c.B.Fig.Measures),
			Kind: c.Kind, Values: []ConflictVal{{c.A.Value, c.A.Canon, c.A.DocumentID, c.A.FID}, {c.B.Value, c.B.Canon, c.B.DocumentID, c.B.FID}}, Reason: v.Reason})
	}
	return out
}

// Join is Candidates judged by j (nil: none judged, so no conflict is asserted): the conflicts, the
// pairs judged unclear (a LOW note each) and the pairs a cap or the judge left out.
func Join(ctx context.Context, c agent.Call, findings []Finding, j Judge) (conflicts []Conflict, unclear []Candidate, unjudged int, u agent.Usage, err error) {
	var judged func(Candidate) bool
	if p, ok := j.(Prior); ok { // pairs judged already stay ahead of the cap
		if judged, err = p.Judged(ctx, c); err != nil {
			return nil, nil, 0, u, err
		}
	}
	cands, dropped := Candidates(findings, CandidateLimit(c.Budget.Calls), judged)
	verdicts := map[string]Verdict{}
	if j != nil && len(cands) > 0 {
		if verdicts, u, err = j.Judge(ctx, c, cands); err != nil {
			return nil, nil, 0, u, err
		}
	}
	for _, x := range cands {
		switch v, ok := verdicts[x.ID]; {
		case !ok:
			unjudged++
		case v.SameQuantity == "unclear":
			unclear = append(unclear, x)
		}
	}
	return Conflicts(cands, verdicts), unclear, unjudged + dropped, u, nil
}

// fold is a reader's label as candidates are ordered by it: lower case, words only, no leading article.
func fold(s string) string {
	s = strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '%' }), " ")
	for _, a := range []string{"the ", "a ", "an "} {
		s = strings.TrimPrefix(s, a)
	}
	return s
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
