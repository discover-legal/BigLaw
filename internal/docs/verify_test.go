// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package docs

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

// citeDoc is the source of the carried v1 tabcite ladder vectors.
const citeDoc = `EMPLOYMENT AGREEMENT. Payment is due within 30 days of invoice. ` +
	`The Executive's base salary is $250,000 per annum, payable monthly in arrears. ` +
	`This agreement is governed by the laws of the State of Delaware.`

// scriptedJudge answers from a queue (the last answer repeats) and records each call.
type scriptedJudge struct {
	answers []verdict
	calls   []bool // vote flag per call
	passage string
}

type verdict struct {
	ok   bool
	conf float64
	err  error
}

func (j *scriptedJudge) judge(_ context.Context, _, passage string, vote bool) (bool, float64, error) {
	j.calls, j.passage = append(j.calls, vote), passage
	v := j.answers[min(len(j.calls), len(j.answers))-1]
	return v.ok, v.conf, v.err
}

func judgeOf(answers ...verdict) *scriptedJudge { return &scriptedJudge{answers: answers} }

const paraphrase = "the Executive receives base compensation of two hundred fifty thousand dollars each year"

func TestCitationExactAndTolerantNoJudge(t *testing.T) {
	j := judgeOf(verdict{true, 1, nil})
	v, err := Verify(t.Context(), citeDoc, "base salary is $250,000 per annum", j.judge)
	if err != nil || !v.Verified || v.Method != "exact" || v.Confidence != 1.0 || citeDoc[v.Start:v.End] != "base salary is $250,000 per annum" {
		t.Errorf("exact: %+v %v", v, err)
	}
	q := "The Executive’s base salary is  $250,000 per annum, payable monthly" // curly apostrophe, doubled space
	v, err = Verify(t.Context(), citeDoc, q, j.judge)
	if err != nil || !v.Verified || v.Method != "tolerant" || v.Confidence != 0.95 || citeDoc[v.Start:v.End] != "The Executive's base salary is $250,000 per annum, payable monthly" {
		t.Errorf("tolerant: %+v %q %v", v, citeDoc[v.Start:v.End], err)
	}
	if len(j.calls) != 0 {
		t.Errorf("string rungs called the judge %d times", len(j.calls))
	}
}

// TestVerbatimContains (v1 protocols vector): line wraps and runs of spaces are tolerated,
// paraphrase is not.
func TestVerbatimContains(t *testing.T) {
	src := "The Commission\nalleges   that on March 3, 2024,  Hargrove purchased shares."
	if v, _ := Verify(t.Context(), src, "alleges that on March 3, 2024, Hargrove purchased", nil); !v.Verified || v.Method != "tolerant" {
		t.Errorf("white-space-tolerant match: %+v", v)
	}
	if v, _ := Verify(t.Context(), src, "Hargrove sold his shares in a panic", nil); v.Verified || v.Method != "none" || v.Note == "" {
		t.Errorf("a paraphrase without a judge: %+v", v)
	}
}

func TestCitationParaphraseJudgeOneCall(t *testing.T) {
	j := judgeOf(verdict{true, 0.9, nil})
	v, err := Verify(t.Context(), citeDoc, paraphrase, j.judge)
	if err != nil || !v.Verified || v.Method != "judge" || v.Confidence != JudgeCap || len(j.calls) != 1 || j.calls[0] {
		t.Fatalf("judge: %+v %v, calls %v", v, err, j.calls)
	}
	if !strings.Contains(j.passage, "$250,000 per annum") {
		t.Errorf("the judge was not shown the clause: %q", j.passage)
	}
}

func TestCitationUncertainJudgeTriggersEnsemble(t *testing.T) {
	j := judgeOf(verdict{true, 0.5, nil}, verdict{true, 0.9, nil}, verdict{false, 0.9, nil}, verdict{true, 0.9, nil})
	v, err := Verify(t.Context(), citeDoc, paraphrase, j.judge)
	if err != nil || !v.Verified || v.Method != "ensemble" || math.Abs(v.Confidence-0.75*2/3) > 1e-9 {
		t.Fatalf("ensemble 2/3: %+v %v", v, err)
	}
	if len(j.calls) != 4 || j.calls[0] || !j.calls[1] || !j.calls[2] || !j.calls[3] {
		t.Errorf("calls %v, want one judge call then three votes", j.calls)
	}
}

func TestCitationEnsembleMajorityUnsupported(t *testing.T) {
	j := judgeOf(verdict{true, 0.4, nil}, verdict{false, 0.9, nil}, verdict{true, 0.6, nil}, verdict{false, 0.8, nil})
	v, err := Verify(t.Context(), citeDoc, "the Company grants the Executive a private jet allowance", j.judge)
	if err != nil || v.Verified || v.Method != "none" || v.Confidence != 0 || !strings.Contains(v.Note, "1 of 3") || len(j.calls) != 4 {
		t.Fatalf("ensemble 1/3: %+v %v", v, err)
	}
}

func TestCitationFabricatedQuoteUnverified(t *testing.T) {
	j := judgeOf(verdict{false, 0.95, nil})
	v, err := Verify(t.Context(), citeDoc, "the Company grants the Executive a private jet allowance", j.judge)
	if err != nil || v.Verified || v.Method != "none" || !strings.Contains(v.Note, "unsupported") || len(j.calls) != 1 {
		t.Fatalf("a confident rejection: %+v %v, calls %d", v, err, len(j.calls))
	}
}

// TestCitationJudgeFailureIsAnError: v1's TestCitationJudgeFailureDegradesCitationOnly, changed
// (G6): a judge failure is returned as the judge's error, never as an "unverified" verdict that
// would look like a fabrication; only this quote is affected.
func TestCitationJudgeFailureIsAnError(t *testing.T) {
	down := errors.New("model unavailable")
	for _, j := range []*scriptedJudge{judgeOf(verdict{err: down}), judgeOf(verdict{true, 0.5, nil}, verdict{err: down}, verdict{err: down}, verdict{true, 1, nil})} {
		v, err := Verify(t.Context(), citeDoc, paraphrase, j.judge)
		if !errors.Is(err, down) || v.Verified || v.Method != "none" {
			t.Errorf("judge failure: %+v %v", v, err)
		}
	}
	// Two of three votes cast is enough to decide.
	j := judgeOf(verdict{true, 0.5, nil}, verdict{err: down}, verdict{true, 1, nil}, verdict{true, 1, nil})
	if v, err := Verify(t.Context(), citeDoc, paraphrase, j.judge); err != nil || v.Method != "ensemble" || math.Abs(v.Confidence-0.5) > 1e-9 {
		t.Errorf("2 votes cast, both supporting: %+v %v", v, err)
	}
}

// TestCitationWindowLocatesRegion (v1 vector; the form-feed page variant is dropped: v2 citations
// carry no page number): the judge sees a bounded region around the cited clause, not the document.
func TestCitationWindowLocatesRegion(t *testing.T) {
	filler := strings.Repeat("General boilerplate provisions apply to this section as stated. ", 400)
	doc := filler + "The indemnification obligations survive termination for a period of seventy-two months. " + filler
	j := judgeOf(verdict{true, 0.9, nil})
	v, err := Verify(t.Context(), doc, "indemnification obligations survive for seventy-two months", j.judge)
	if err != nil || !strings.Contains(j.passage, "seventy-two months") || len(j.passage) > 2*windowRadius+200 {
		t.Errorf("window %d bytes (doc %d), found %v: %v", len(j.passage), len(doc), strings.Contains(j.passage, "seventy-two"), err)
	}
	if got := doc[v.Start:v.End]; got != j.passage {
		t.Errorf("under %v of the quote's words in any span: the located text is the judged window", repairOverlap)
	}
	v, _ = Verify(t.Context(), doc, "indemnification obligation survives termination for a period of seventy-two months", j.judge)
	if got := doc[v.Start:v.End]; got != "indemnification obligations survive termination for a period of seventy-two months" {
		t.Errorf("a close paraphrase is located at its source words: %q", got)
	}
	multi := "Le délai de préavis est de trente jours ouvrés. " + strings.Repeat("é", 2000)
	if _, err := Verify(t.Context(), multi, "le préavis est de trente jours", judgeOf(verdict{true, 0.9, nil}).judge); err != nil {
		t.Errorf("multibyte text: %v", err)
	}
}

// TestLadderMethodsAndCaps: exact = 1.0, tolerant = 0.95, judge ≤ 0.8, ensemble < the judge cap,
// none = 0 — whatever confidence a judge reports.
func TestLadderMethodsAndCaps(t *testing.T) {
	cases := []struct {
		quote string
		j     *scriptedJudge
		want  string
		max   float64
	}{
		{"governed by the laws of the State of Delaware", nil, "exact", 1.0},
		{"GOVERNED   by the laws of the state of delaware", nil, "tolerant", 0.95},
		{paraphrase, judgeOf(verdict{true, 1, nil}), "judge", JudgeCap},
		{paraphrase, judgeOf(verdict{true, 7, nil}), "judge", JudgeCap},
		{paraphrase, judgeOf(verdict{true, 0.1, nil}, verdict{true, 1, nil}), "ensemble", 0.75},
		{paraphrase, judgeOf(verdict{false, -3, nil}, verdict{false, 1, nil}), "none", 0},
		{"  ", judgeOf(verdict{true, 1, nil}), "none", 0},
	}
	for _, c := range cases {
		var judge Judge
		if c.j != nil {
			judge = c.j.judge
		}
		v, err := Verify(t.Context(), citeDoc, c.quote, judge)
		if err != nil || v.Method != c.want || v.Confidence > c.max || (c.want != "none") != v.Verified || v.Verified && v.Confidence <= 0 {
			t.Errorf("%q: %+v %v, want %s ≤ %v", c.quote, v, err, c.want, c.max)
		}
		if c.want == "ensemble" && v.Confidence >= JudgeCap {
			t.Errorf("an ensemble outranks a judge: %v", v.Confidence)
		}
	}
	if !(ExactConfidence > TolerantConfidence && TolerantConfidence > JudgeCap && JudgeCap > ensembleScale) {
		t.Error("rung confidences are not strictly ordered")
	}
}
