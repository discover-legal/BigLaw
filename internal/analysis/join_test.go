// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/agent"
	"github.com/discover-legal/BigLaw/internal/docs"
)

// Carried from v1 orchestrator/crossdoc_test.go (10 of 11 vectors; TestCrossDocFindingsRideDiscrepancySection
// is dropped with the writer's section routing). v2's join is model-free: the alias judge becomes the
// lead's alias map, the adjudicator and its significance notes are gone (conflicts are reported with
// both quotes, not adjudicated), and the inputs are typed values of verified findings.

var nfid int

// fh is one verified finding holding one value, quoted from doc (v1's figureHit).
func fh(value, quote, doc, entity, metric string) Finding {
	nfid++
	return Finding{FID: fmt.Sprintf("F%d", nfid), Verified: true, Quotes: []Quote{{DocumentID: doc, Quote: quote}},
		Values: []Value{{Entity: entity, Metric: metric, Value: value}}}
}

func has(cs []Conflict, a, b string) *Conflict {
	for i, c := range cs {
		var vals []string
		for _, v := range c.Values {
			vals = append(vals, v.Value)
		}
		if j := strings.Join(vals, "|"); strings.Contains(j, a) && strings.Contains(j, b) {
			return &cs[i]
		}
	}
	return nil
}

const (
	qTradesReferral = "The staff reviewed 4,217 omnibus equity trades allocated by Ostrowski during the review period."
	qTradesExhibit  = "Total Omnibus Equity Trades Analyzed | 4,312"
	qCompReferral   = "¶56. Ostrowski received undisclosed compensation totaling $92,600 from Bayshore Palms during the relevant period."
	qCompExhibit    = "Grand total of 22 payments, 05/2022 through 01/2024: $103,800"
	qRateReferral   = "Approximately 73% of the profitable omnibus trades were allocated to the Lakeshore proprietary account."
	qRateExhibit    = "Profitable omnibus trade allocation rate across the review population: 78%"
	qDateEmail      = "From: Ostrowski To: Chen Date: Mon, 15 Apr 2024 09:23:47 -0400 Subject: need you to clean up the shared drive before Friday"
	qDateReferral   = "On or about April 22, 2024, Ostrowski instructed Chen to \"clean up the shared drive\"."
)

// fourPlanted is the four planted discrepancies, each value read as the record has it: the exhibit
// rows name no party, the reader reads them as Ostrowski's, and it reads the e-mail's date and the
// instruction's as one event's, the instruction to clean up the shared drive (the right labels); the
// e-mail's typed entity, Chen, is its recipient, another party than the reader's.
func fourPlanted() []Finding {
	instruction := "instruction to clean up the shared drive"
	return readAs([]Finding{
		fh("4,217", qTradesReferral, "referral", "Ostrowski", "omnibus trade count"),
		fh("4,312", qTradesExhibit, "exhibit-c", "", "omnibus trade count"),
		fh("$92,600", qCompReferral, "referral", "Ostrowski", "undisclosed compensation total"),
		fh("$103,800", qCompExhibit, "exhibit-d", "Bayshore Palms LLC", "undisclosed compensation total"),
		fh("Mon, 15 Apr 2024 09:23:47 -0400", qDateEmail, "exhibit-e", "Chen", "email sent date"),
		fh("April 22, 2024", qDateReferral, "referral", "Ostrowski", "obstruction instruction date"),
		fh("73%", qRateReferral, "referral", "Ostrowski", "profitable omnibus allocation rate"),
		fh("78%", qRateExhibit, "exhibit-f", "", "profitable omnibus allocation rate"),
	}, map[string]relabel{"4,312": {party: "Ostrowski"}, "78%": {party: "Ostrowski"},
		"Mon, 15 Apr 2024 09:23:47 -0400": {party: "Ostrowski", measures: instruction, entity: "other"}, "April 22, 2024": {measures: instruction, entity: "party"}})
}

func TestParseCrossDocDate(t *testing.T) {
	for in, want := range map[string]string{
		"Mon, 15 Apr 2024 09:23:47 -0400": "2024-04-15", "April 22, 2024": "2024-04-22", "on or about April 22, 2024": "2024-04-22",
		"15 Apr 2024": "2024-04-15", "2024-04-15": "2024-04-15", "04/22/2024": "2024-04-22", "May 2022": "2022-05", "05/2022": "2022-05",
		"early 2022": "2022", "2021": "2021", "Nov. 12, 2026": "2026-11-12", "4,217": "", "not a date": "",
	} {
		d, ok := ParseDate(in)
		if ok != (want != "") || ok && d.Key() != want {
			t.Errorf("ParseDate(%q) = %s %v, want %q", in, d.Key(), ok, want)
		}
	}
}

func bench5Findings(t *testing.T) []Finding {
	t.Helper()
	data, err := os.ReadFile("testdata/bench5-sec-conflicts.json")
	if err != nil {
		t.Fatal(err)
	}
	var in struct{ Findings []Finding }
	if err := json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	return in.Findings
}

// pairOf reports whether conflicts hold a conflict between values a and b.
func pairOf(cs []Conflict, a, b string) bool {
	return slices.ContainsFunc(cs, func(c Conflict) bool {
		vs := map[string]bool{}
		for _, v := range c.Values {
			vs[v.Value] = true
		}
		return vs[a] && vs[b]
	})
}

// bench5Labels are the bench5 pairs' values as the figure reader reads them (the right labels): the
// extractor typed them under other labels ("Office of Quantitative Research", "Omnibus Equity Trades",
// "Email": no party at all) and metrics; the record says whose figures they are and what they measure.
var bench5Labels = func() map[string]relabel {
	wca, bellini, whitmore := "Whitmore Capital Advisors LLC", "Marcus T. Bellini", "Gerald R. Whitmore"
	trades, comp, instr := "omnibus equity trades analysed", "undisclosed compensation paid for Bellini", "instruction to clean up the shared drive"
	return map[string]relabel{"F80/4,217": {wca, trades, "", "none"}, "F170/4,312": {wca, trades, "", "none"},
		"F752/$92,600": {bellini, comp, "", "party"}, "F388/$103,800.00": {bellini, comp, "", ""},
		"F96/April 22, 2024": {whitmore, instr, "", "party"}, "F805/15 Apr 2024": {whitmore, instr, "", "none"}, "F934/15 Apr 2024": {whitmore, instr, "", "party"}}
}()

// TestJoinDiscrepanciesFidsDoNotScopeTheJoin (bench5: per-issue fids split each pair across issues):
// naming one side's findings still finds the conflict with the other side's, and only conflicts
// holding a named finding are reported.
func TestJoinDiscrepanciesFidsDoNotScopeTheJoin(t *testing.T) {
	fs := readAs(bench5Findings(t), bench5Labels)
	rt := &agent.Runtime{Reg: agent.NewRegistry()}
	for _, tool := range Tools(judgingSource{memSource(fs), scripted{yes: [][2]string{{"4,217", "4,312"}}}}) {
		if err := rt.Reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	res, err := rt.Invoke(context.Background(), agent.Agent{Name: "lead", Visible: []string{"numbers", "join_discrepancies"}}, agent.Call{TaskID: "t"}, "join_discrepancies",
		json.RawMessage(`{"fids":["F80"],"aliases":null}`), agent.NewBudget(10, 1, 0), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Conflicts []Conflict }
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Conflicts) != 1 || !pairOf(out.Conflicts, "4,217", "4,312") {
		t.Fatalf("conflicts for F80: %s", res.Output)
	}
	if !strings.Contains(spec("join_discrepancies").Description, "fids only choose which conflicts are reported") {
		t.Error("the spec does not say fids do not scope the join")
	}
}

type memSource []Finding

// judgingSource is a memSource whose join a scripted judge decides.
type judgingSource struct {
	memSource
	scripted
}

func (m memSource) Findings(context.Context, string) ([]Finding, error) { return m, nil }

// lab-r4 r8 verifier repros (critique/biglaw-lab-r4-r8-verifier.md) for the join, the labels and the
// calc, kept as regression tests.

// M1: what real ledgers put to the judge at a few budgets, and how many pairs past them are dropped.
func TestJoinBenchCandidateCounts(t *testing.T) {
	for _, name := range []string{"sec", "trust", "gatea"} {
		ranked := rankPairs(read(bench6Ledger(t, name))) // ranked once: Candidates is coverFrom(rankPairs(fs))
		all := len(ranked)
		for _, calls := range []int{6, 16} {
			cands, dropped, _ := coverFrom(ranked, CandidateLimit(calls), nil)
			if len(cands)+dropped != all || len(cands) != min(all, CandidateLimit(calls)) {
				t.Errorf("bench6 %s, %d calls: %d candidates + %d dropped, of %d pairs", name, calls, len(cands), dropped, all)
			}
			t.Logf("bench6 %s, %d judge calls: %d candidates, %d dropped (of %d pairs)", name, calls, len(cands), dropped, all)
		}
	}
}

// M1 (cap): a pair judged "yes" stays a conflict when later findings rank many closer pairs ahead of
// it: pairs judged already come first, whatever the cap.
func TestJoinYesStaysAheadOfTheCap(t *testing.T) {
	fs := []Finding{fh("$92,600", "Compensation was $92,600.", "referral", "Ostrowski", "compensation"),
		fh("$103,800", "Total paid: $103,800", "exhibit", "Ostrowski", "payments total")}
	cands, _ := Candidates(read(fs), testLimit, nil)
	if len(cands) != 1 {
		t.Fatalf("%d candidates", len(cands))
	}
	yes := cands[0]
	vs := map[string]Verdict{yes.ID: {Pair: yes.ID, SameQuantity: "yes", Reason: "r"}}
	for i := range 17 {
		fs = append(fs, fh(fmt.Sprintf("$%d,000", 300+i), fmt.Sprintf("A fee of $%d,000.", 300+i), fmt.Sprintf("doc%d", i), "Acme", "fee"))
	}
	judged := func(c Candidate) bool {
		return c.A.Canon == yes.A.Canon && c.B.Canon == yes.B.Canon && c.A.DocumentID == yes.A.DocumentID
	}
	again, dropped := Candidates(read(fs), testLimit, judged)
	if cs := Conflicts(again, vs); len(cs) != 1 {
		t.Errorf("the judged-yes pair %s is no longer a conflict (%d candidates, %d dropped)", yes.ID, len(again), dropped)
	}
	if cs, _, unjudged, _, _ := Join(context.Background(), testCall, read(fs), prior{judged}); len(cs) != 1 || unjudged != dropped+len(again)-1 {
		t.Errorf("%d conflicts, unjudged %d: the judged pair fell out or counted as not judged", len(cs), unjudged)
	}
}

// prior is a judge that has judged the pairs judged "yes" (and answers nothing new), as the engine's
// ledger does.
type prior struct{ judged func(Candidate) bool }

func (p prior) Judge(_ context.Context, _ agent.Call, cands []Candidate) (map[string]Verdict, agent.Usage, error) {
	out := map[string]Verdict{}
	for _, c := range cands {
		if p.judged(c) {
			out[c.ID] = Verdict{Pair: c.ID, SameQuantity: "yes", Reason: "earlier"}
		}
	}
	return out, agent.Usage{}, nil
}

func (p prior) Judged(context.Context, agent.Call) (func(Candidate) bool, error) {
	return p.judged, nil
}

// L1: a verdict's reason must quote both passages: a reason quoting neither, or one, is unclear.
func TestVerdictReasonQuotesBothPassages(t *testing.T) {
	fs := read([]Finding{
		fh("$92,600", "Ostrowski received undisclosed compensation payments totaling $92,600.", "referral", "Ostrowski", "compensation"),
		fh("$103,800", "Grand total of payments on behalf of Bellini: $103,800", "exhibit", "Ostrowski", "payments total"),
	})
	cands, _ := Candidates(fs, testLimit, nil)
	id := cands[0].ID
	for reason, want := range map[string]string{
		`A says "received undisclosed compensation payments totaling $92,600"; B says "Grand total of payments on behalf of Bellini": $92,600 against $103,800.`: "yes",
		`A says "received undisclosed compensation payments totaling $92,600"; B says "Grand total of payments on behalf of Bellini".`:                           "unclear", // bench7: neither figure named beside the quotations
		`A says "the parties agreed a $5,000,000 cap in the side letter"; B says "see the side letter".`:                                                         "unclear",
		`A says "received undisclosed compensation payments totaling $92,600"; B is the same figure.`:                                                            "unclear",
		"": "unclear",
	} {
		if vs, _ := CheckVerdicts(cands, []Verdict{{Pair: id, SameQuantity: "yes", Reason: reason}}); vs[id].SameQuantity != want {
			t.Errorf("%q: %s, want %s", reason, vs[id].SameQuantity, want)
		}
	}
}

// lab-r4 r9 verifier repros (critique/biglaw-lab-r4-r9-verifier.md M2, L1, L2, L3, residual a), kept
// as regression tests.

// M2: the ranking is a function of its input: bench6 SEC ranks the same way on 50 calls (5 under -race), and every
// cap's candidate set (what the judge is asked, and the cache keys it uses) with it.
func TestRankPairsDeterministic(t *testing.T) {
	fs := read(bench6Ledger(t, "sec"))
	ids := func() []string {
		var out []string
		for _, c := range rankPairs(fs) {
			out = append(out, c.ID)
		}
		return out
	}
	first := ids()
	repeats, again := 50, 10
	if docs.RaceEnabled { // each ranking of the bench6 SEC ledger takes about 2 s under -race (r2 follow-up 6)
		repeats, again = 5, 2
	}
	for i := range repeats {
		if got := ids(); !slices.Equal(got, first) {
			k := 0
			for k < len(got) && got[k] == first[k] {
				k++
			}
			t.Fatalf("run %d ranks differently from position %d (%s vs %s)", i, k, got[k], first[k])
		}
	}
	for _, name := range []string{"trust", "gatea"} {
		gs := read(bench6Ledger(t, name))
		a, _ := Candidates(gs, 120, nil)
		for range again {
			if b, _ := Candidates(gs, 120, nil); !slices.EqualFunc(a, b, func(x, y Candidate) bool { return x.ID == y.ID }) {
				t.Fatalf("bench6 %s: the candidates at a cap of 120 vary", name)
			}
		}
	}
}

// L2: closeness orders a value's counterparts even when their contexts share no word: $100,000's
// first counterpart is $100,100 (0.1% apart), not $1,000.
func TestClosenessOrdersWithoutSharedWords(t *testing.T) {
	fs := read([]Finding{
		fh("$100,000", "Paid $100,000.", "d1", "Alpha", "xx"),
		fh("$1,000", "Owed $1,000.", "d2", "Beta", "yy"),
		fh("$100,100", "Owed $100,100.", "d3", "Gamma", "zz"),
	})
	for _, c := range rankPairs(fs) {
		if c.A.Value == "$100,000" || c.B.Value == "$100,000" {
			if c.A.Value != "$100,100" && c.B.Value != "$100,100" {
				t.Errorf("$100,000's first counterpart is %s~%s", c.A.Value, c.B.Value)
			}
			break
		}
	}
}

// L1: words both passages share ("in accordance with the Agreement") quote neither: a reason must
// quote words that are one side's alone, for each side.
func TestReasonBoilerplateQuotesNeither(t *testing.T) {
	fs := read([]Finding{
		fh("$291,400", "In accordance with the Agreement, the Adviser paid commissions of $291,400 to Lakeshore.", "letter", "Lakeshore", "commissions"),
		fh("$310,000", "In accordance with the Agreement, a fee of $310,000 was invoiced for audit services.", "schedule", "Lakeshore", "audit fee"),
	})
	cands, _ := Candidates(fs, testLimit, nil)
	id := cands[0].ID
	for reason, want := range map[string]string{
		`Both say "in accordance with the agreement", so they are the same.`:                                                                        "unclear",
		`A says "the Adviser paid commissions of $291,400"; B says "a fee of $310,000 was invoiced for audit services": $291,400 against $310,000.`: "yes",
	} {
		if vs, _ := CheckVerdicts(cands, []Verdict{{Pair: id, SameQuantity: "yes", Reason: reason}}); vs[id].SameQuantity != want {
			t.Errorf("%q: %s, want %s", reason, vs[id].SameQuantity, want)
		}
	}
}

// Residual a: the values a join this size leaves without any counterpart judged are counted (what the
// Limitations line leads with), and fall as the budget grows.
func TestJoinCountsUncoveredValues(t *testing.T) {
	ranked := rankPairs(read(bench6Ledger(t, "sec"))) // ranked once: CandidatesCover is coverFrom(rankPairs(fs))
	last := -1
	for _, calls := range []int{2, 6, 15, 16} {
		cands, dropped, uncovered := coverFrom(ranked, CandidateLimit(calls), nil)
		t.Logf("bench6 sec, %d judge calls: %d candidates, %d dropped, %d values with no counterpart judged", calls, len(cands), dropped, uncovered)
		if last >= 0 && uncovered > last || uncovered == 0 {
			t.Errorf("%d calls: %d uncovered values (before: %d)", calls, uncovered, last)
		}
		last = uncovered
	}
	if _, _, u := coverFrom(ranked, len(ranked), nil); u != 0 {
		t.Errorf("every pair asked, %d values uncovered", u)
	}
}

// r10 M2: a grant sized to the pending pairs asks every one of them, however many pairs are judged
// already: the limit counts the pairs to judge, not the judged ones kept ahead of it (bench6 SEC, as
// the coverage pass sizes its judge grant: pending among CandidateLimit(jcalls), then ceil(n/20)).
func TestPendingSizedGrantAsksAllPending(t *testing.T) {
	fs := read(bench6Ledger(t, "sec"))
	ranked := rankPairs(fs)
	for _, j := range []int{0, 10, 20, 40, 100} {
		done := map[string]bool{}
		for _, c := range ranked[:j] {
			done[c.ID] = true
		}
		judged := func(c Candidate) bool { return done[c.ID] }
		jcalls := 10
		cands, _, _ := coverFrom(ranked, CandidateLimit(jcalls), judged) // Candidates(fs, …), ranked once
		n := 0
		for _, c := range cands {
			if !judged(c) {
				n++
			}
		}
		grant := min(jcalls, (n+JudgePairs-1)/JudgePairs)
		got, _, _ := coverFrom(ranked, CandidateLimit(grant), judged)
		asked := 0
		for _, c := range got {
			if !judged(c) {
				asked++
			}
		}
		if asked < n {
			t.Errorf("judged %d: %d of %d pending pairs the grant was sized for are never asked", j, n-asked, n)
		}
	}
}
