// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/engine/figtest"
	"github.com/discover-legal/BigLaw/internal/model"
)

// lab-r4 r7 A: the join's conflicts are the figure judge's verdicts. Go proposes the pair; only the
// judge's "yes" makes it a conflict finish asks the memo to state, "no" says nothing, "unclear" is a
// low-confidence Limitations line, and a verdict for a pair Go did not propose is rejected.

const (
	feeLetter   = "1. Fees\nThe Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023.\n"
	feeSchedule = "Schedule\nThe Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023.\n"
)

// feeRun runs the two commissions findings to a finish check, the judge deciding as decide says.
func feeRun(t *testing.T, decide func(a, b figtest.JudgeSide) string, over map[string][]model.Reply) (*fixture, TaskView, *model.Fake) {
	t.Helper()
	f := newFixture(t, feeLetter, feeSchedule)
	f.judge = decide
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	scripts := f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
			calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
		"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023",
			val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
			facts(fd("The Adviser paid Lakeshore Trading LLC commissions of $310,000.", d2, "The Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023",
				val("Lakeshore Trading LLC", "commissions", "$310,000", "Money")))},
		"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
	})
	for k, v := range over {
		scripts[k] = v
	}
	_, fake := f.engine(scripts, Config{})
	task := f.submit(0)
	f.step(context.Background())
	return f, f.get(task.ID), fake
}

func judgeCallCount(fake *model.Fake) int {
	n := 0
	for _, c := range fake.Calls() {
		if c.Agent == "judge_figures" {
			n++
		}
	}
	return n
}

func TestJoinAssertsOnlyTheJudgesYes(t *testing.T) {
	t.Parallel()
	for answer, want := range map[string]struct{ lint, unclear bool }{"yes": {true, false}, "no": {false, false}, "unclear": {false, true}} {
		f, v, fake := feeRun(t, func(a, b figtest.JudgeSide) string { return answer }, nil)
		fin := strings.Join(f.outputs(v.ID, "finish"), "")
		if got := strings.Contains(fin, "the memo does not state both"); got != want.lint {
			t.Errorf("%s: finish asks for both figures: %v\n%s", answer, got, fin)
		}
		j, err := f.e.joined(context.Background(), v.ID)
		if err != nil || len(j.all) != map[bool]int{true: 1}[want.lint] || len(j.unclear) != map[bool]int{true: 1}[want.unclear] || j.unjudged != 0 {
			t.Errorf("%s: joined %+v, %v", answer, j, err)
		}
		lines := strings.Join(j.lines(), "\n")
		if got := strings.Contains(lines, "Figures (low confidence)") && strings.Contains(lines, "[F1] $291,400 vs [F2] $310,000"); got != want.unclear {
			t.Errorf("%s: the low-confidence line: %q", answer, lines)
		}
		if judgeCallCount(fake) != 1 {
			t.Errorf("%s: %d judge calls, want 1 (finish's check judged; the verdict is kept)", answer, judgeCallCount(fake))
		}
	}
}

// A judge's verdicts for pairs Go did not propose (a finding with itself, two refs that are no
// values, a pair id made up) are rejected; the proposed pair, unanswered, stays unjudged and is
// disclosed; a "yes" with no reason is unclear.
func TestJoinRejectsVerdictsGoDidNotPropose(t *testing.T) {
	t.Parallel()
	bogus := func(vs ...map[string]string) model.Reply {
		data, _ := json.Marshal(map[string]any{"verdicts": vs})
		return model.Reply{Text: string(data)}
	}
	yes := func(pair, reason string) map[string]string {
		return map[string]string{"pair": pair, "sameQuantity": "yes", "reason": reason}
	}
	f, v, _ := feeRun(t, nil, map[string][]model.Reply{"judge_figures": {bogus(yes("F1.v1|F1.v1", "same"), yes("F1.s1|F2.s1", "spans"), yes("F1.v1|F9.v1", "made up"))}})
	j, err := f.e.joined(context.Background(), v.ID)
	if err != nil || len(j.all) != 0 || j.unjudged != 1 || !strings.Contains(strings.Join(j.lines(), ""), "1 pair of figures that two documents state differently, among those the join puts to the figure judge first (each figure's most alike and closest counterparts), was not judged") {
		t.Fatalf("joined %+v (%q), %v", j, j.lines(), err)
	}
	f, v, _ = feeRun(t, nil, map[string][]model.Reply{"judge_figures": {bogus(yes("F1.v1|F2.v1", " "))}})
	if j, err := f.e.joined(context.Background(), v.ID); err != nil || len(j.all) != 0 || len(j.unclear) != 1 {
		t.Fatalf("a yes without a reason: %+v, %v", j, err)
	}
}

// The judge's yes and no are cached across runs of the matter (figure_reads, under its spec's hash):
// a second run over the same findings asks it nothing and gets the same conflict.
func TestJoinVerdictsCachedAcrossRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, feeLetter, feeSchedule)
	d1, d2 := f.docs[0].ID, f.docs[1].ID
	scripts := func() map[string][]model.Reply {
		return f.with(map[string][]model.Reply{
			"lead": {calls(plan(issue("I1", "What commissions were paid?", "RedFlag"))), calls(extract(d1, "commissions", "I1")), calls(extract(d2, "commissions", "I1")),
				calls(draft("I1", "F1")), calls(committee()), calls(finishCall("check"))},
			"extract_facts": {facts(fd("The Adviser paid commissions of $291,400 to Lakeshore Trading LLC.", d1, "The Adviser paid commissions of $291,400 to Lakeshore Trading LLC in 2023",
				val("Lakeshore Trading LLC", "commissions", "$291,400", "Money"))),
				facts(fd("The Adviser paid Lakeshore Trading LLC commissions of $310,000.", d2, "The Adviser paid Lakeshore Trading LLC commissions of $310,000 in 2023",
					val("Lakeshore Trading LLC", "commissions", "$310,000", "Money")))},
			"draft_section": {section("The Adviser paid $291,400 in commissions [F1].", "s")},
		})
	}
	var calls []int
	for range 2 {
		_, fake := f.engine(scripts(), Config{})
		task := f.submit(0)
		f.step(context.Background())
		j, err := f.e.joined(context.Background(), task.ID)
		if err != nil || len(j.all) != 1 {
			t.Fatalf("joined %+v, %v", j, err)
		}
		calls = append(calls, judgeCallCount(fake))
	}
	if calls[0] != 1 || calls[1] != 0 {
		t.Fatalf("judge calls per run %v, want [1 0]", calls)
	}
}

// r8 L4, L2: a verdict is keyed by the aliases that name its sides only (an alias of another party
// judges nothing again), and its cross-run key by the task's documents (a reason may quote a document
// only that set holds).
func TestVerdictKeysScopeAliasesAndDocuments(t *testing.T) {
	x := analysis.Candidate{ID: "F1.v1|F2.v1", Kind: "numeric",
		A: analysis.Side{Ref: "F1.v1", DocumentID: "d1", Value: "$291,400", Canon: "usd:291400", Entity: "Lakeshore Trading LLC", Fig: analysis.Reading{Party: "Lakeshore Trading LLC"}},
		B: analysis.Side{Ref: "F2.v1", DocumentID: "d2", Value: "$310,000", Canon: "usd:310000", Entity: "Ostrowski"}}
	other := []analysis.Alias{{Name: "Acme Corp", Aliases: []string{"Acme"}}}
	own := append(slices.Clone(other), analysis.Alias{Name: "Lakeshore Trading LLC", Aliases: []string{"Ostrowski"}})
	if ledgerKey(nil, x) != ledgerKey(other, x) || judgeKey("h", "m", "matter:M-1", []string{"d1", "d2"}, nil, x) != judgeKey("h", "m", "matter:M-1", []string{"d2", "d1"}, other, x) {
		t.Error("an alias naming neither side, or the documents' order, changes a verdict's key")
	}
	if ledgerKey(nil, x) == ledgerKey(own, x) || judgeKey("h", "m", "matter:M-1", []string{"d1", "d2"}, nil, x) == judgeKey("h", "m", "matter:M-1", []string{"d1", "d2", "d3"}, nil, x) {
		t.Error("an alias naming a side, or another document set, keeps a verdict's key")
	}
	y := x
	y.ID, y.A.Ref = "F7.v1|F2.v1", "F7.v1" // the same two values restated by another finding
	if ledgerKey(own, x) != ledgerKey(own, y) {
		t.Error("a pair restated by another finding loses its verdict")
	}
}

// r8 L3: finish's and the coverage pass's own judging takes a quarter of their calls (at least one,
// never the last).
func TestJudgeShareOfCalls(t *testing.T) {
	for calls, want := range map[int]int{0: 0, 1: 0, 2: 1, 13: 3, 36: 9, 61: 15} {
		if got := judgeCalls(calls); got != want {
			t.Errorf("judgeCalls(%d) = %d, want %d", calls, got, want)
		}
	}
}
