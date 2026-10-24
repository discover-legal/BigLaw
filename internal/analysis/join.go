// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"cmp"
	"context"
	"fmt"
	"math/big"
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

// MaxCandidates bounds the pairs one join puts to the judge; past it, the pairs left out are counted
// (a Limitations line says how many).
const MaxCandidates = 120

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

// Candidates are the pairs of verified findings' values that could be one quantity stated two ways:
// both read clearly (Canon: the reader's kind, currency and exact amount; a date as written, its
// reading not unclear), of one kind and currency, from two findings in two documents, their values
// different (a date: not compatible at its precision). Two values in two documents are asked once,
// however many findings repeat them. They come in a fixed order (the reader's measures alike first,
// then the closest values, then by ref) and at most MaxCandidates; dropped is how many more there were. Nothing else is filtered: whether a pair is one quantity is the judge's.
func Candidates(findings []Finding) (cands []Candidate, dropped int) {
	var sides []Side
	var dates []Date
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
			if !ok || v.Fig == nil || v.Fig.Unclear || v.Fig.Kind == KindOther {
				continue
			}
			sides = append(sides, Side{Ref: fmt.Sprintf("%s.v%d", f.FID, j+1), FID: f.FID, DocumentID: docOf(f, v.Value), Value: v.Value, Canon: canon,
				Entity: v.Entity, Metric: v.Metric, Claim: f.Claim, Quotes: qs, Fig: *v.Fig})
			dates = append(dates, d)
		}
	}
	type scored struct {
		c          Candidate
		same, dist *big.Rat
		key        [4]string
	}
	var all []scored
	for i, a := range sides {
		for k := i + 1; k < len(sides); k++ {
			b := sides[k]
			ka, _, _ := strings.Cut(a.Canon, ":")
			kb, _, _ := strings.Cut(b.Canon, ":")
			if ka != kb || a.FID == b.FID || a.DocumentID == b.DocumentID || a.DocumentID == "" || b.DocumentID == "" || a.Canon == b.Canon {
				continue
			}
			kind, dist := "numeric", big.NewRat(0, 1)
			if ka == "date" {
				if dates[i].Compatible(dates[k]) {
					continue
				}
				kind = "date"
				dist = big.NewRat(abs(int64(dates[i].time().Sub(dates[k].time()).Hours()/24)), 1)
			} else if _, x, okx := CanonAmount(a.Canon); okx {
				_, y, _ := CanonAmount(b.Canon)
				hi := new(big.Rat).Abs(x)
				if new(big.Rat).Abs(y).Cmp(hi) > 0 {
					hi = new(big.Rat).Abs(y)
				}
				if hi.Sign() > 0 {
					dist = new(big.Rat).Quo(new(big.Rat).Abs(new(big.Rat).Sub(x, y)), hi)
				}
			}
			same := big.NewRat(1, 1)
			if fold(a.Fig.Measures) != "" && fold(a.Fig.Measures) == fold(b.Fig.Measures) { // an order, never a filter
				same = big.NewRat(0, 1)
			}
			x, y := a, b
			if y.Ref < x.Ref {
				x, y = y, x
			}
			key := [4]string{x.Canon, x.DocumentID, y.Canon, y.DocumentID}
			if x.Canon > y.Canon || x.Canon == y.Canon && x.DocumentID > y.DocumentID {
				key = [4]string{y.Canon, y.DocumentID, x.Canon, x.DocumentID}
			}
			all = append(all, scored{Candidate{ID: x.Ref + "|" + y.Ref, Kind: kind, A: x, B: y}, same, dist, key})
		}
	}
	slices.SortStableFunc(all, func(p, q scored) int {
		return cmp.Or(p.same.Cmp(q.same), p.dist.Cmp(q.dist), strings.Compare(p.c.ID, q.c.ID))
	})
	asked := map[[4]string]bool{} // one question per two values in two documents, however many findings repeat them
	for _, s := range all {
		if asked[s.key] {
			continue
		}
		asked[s.key] = true
		if len(cands) == MaxCandidates {
			dropped++
			continue
		}
		cands = append(cands, s.c)
	}
	return cands, dropped
}

// CheckVerdicts keeps the judge's verdicts for pairs it was given: one for an id it was not given is
// rejected (a note), a second for one pair makes it unclear, an answer other than yes, no or unclear
// is unclear.
func CheckVerdicts(cands []Candidate, raw []Verdict) (map[string]Verdict, []string) {
	out := map[string]Verdict{}
	var notes []string
	for _, v := range raw {
		if !slices.ContainsFunc(cands, func(c Candidate) bool { return c.ID == v.Pair }) {
			notes = append(notes, fmt.Sprintf("a verdict for %q was rejected: no such pair was given", v.Pair))
			continue
		}
		if !slices.Contains([]string{"yes", "no", "unclear"}, v.SameQuantity) {
			v.SameQuantity = "unclear"
		}
		if _, twice := out[v.Pair]; twice {
			v.SameQuantity, v.Reason = "unclear", "judged twice"
		}
		out[v.Pair] = v
	}
	return out, notes
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
	cands, dropped := Candidates(findings)
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
