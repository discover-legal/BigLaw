// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/model"
)

const referralText = `1. Review Period
The Division reviewed trades executed from January 1, 2021 through December 31, 2023.

2. Allocations
In 2021 the Firm allocated profitable trades to Oceanic Fund I LP.
`

// analysed is analyze_issue's answer: a rule, its application and a conclusion, no new findings.
func analysed(rule, app, concl string) model.Reply {
	return text(m{"rule": rule, "application": app, "conclusion": concl, "findings": []m{}, "authorities": []m{{"cite": "28 U.S.C. § 2462", "inRecord": false}}})
}

const (
	limitsRule = "Civil penalties are subject to the five-year limitations period of 28 U.S.C. § 2462 (general knowledge, not the record)."
	limitsConc = "Penalties for conduct in 2021 may be time-barred."
)

// TestCoveragePassFromTheReserve (bench, SEC referral: the run finished from its reserve with
// limitation periods and supervision touched, never analysed): before the forced finish, Go
// analyses each issue that was only touched, from the share kept for it, and redrafts it with
// that analysis; the redraft is reviewed, and what is still missing is disclosed.
func TestCoveragePassFromTheReserve(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	bad := model.Reply{Text: "not json"}
	e, _ := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"), issue("I2", "Is the Firm liable for failure to supervise?", "Conduct"))),
			calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {analysed(limitsRule, "The 2021 allocations [F1] fall outside five years of a 2026 action.", limitsConc), bad, bad},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s"),
			section("In 2021 the Firm allocated profitable trades to Oceanic [F1]. Under 28 U.S.C. § 2462 penalties for that conduct may be time-barred.", "s")},
	}), Config{})
	task := f.submit(6) // the run stops at its sixth step and finishes from its reserve
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	steps := f.steps(v.ID)
	out := deref(v.Output)
	if stop(v) != "awaiting_gate/max_steps" {
		t.Fatalf("%s\n%s", stop(v), steps)
	}
	if !strings.Contains(steps, "tool_call:cover:committed") || strings.Index(steps, "tool_call:cover") > strings.Index(steps, "tool_call:committee") ||
		!strings.Contains(out, "penalties for that conduct may be time-barred") || !strings.Contains(out, "Coverage: I2 (Is the Firm liable for failure to supervise?) was not analysed") ||
		strings.Contains(out, "Coverage: I1") {
		t.Fatalf("%s\n%s", steps, out)
	}
}

// TestCoveragePassOnSubmit: a funded run that submits with an issue only touched (facts, no
// analysis) gets the pass inside its submit: analyze_issue, the redraft with the analysis in its
// request, and a committee on the changed draft; the submit is then accepted, once.
func TestCoveragePassOnSubmit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, referralText)
	d1 := f.docs[0].ID
	e, fake := f.engine(f.with(map[string][]model.Reply{
		"lead": {calls(plan(issue("I1", "Is the 2021 conduct time-barred?", "Requirement"))), calls(extract(d1, "2021", "I1")), calls(draft("I1", "F1")),
			calls(committee()), calls(finishCall("submit"))},
		"extract_facts": {facts(fd("The Firm allocated profitable trades to Oceanic in 2021.", d1, "In 2021 the Firm allocated profitable trades to Oceanic Fund I LP"))},
		"analyze_issue": {analysed(limitsRule, "The 2021 allocations [F1] fall outside five years.", limitsConc)},
		"draft_section": {section("In 2021 the Firm allocated profitable trades to Oceanic [F1].", "s"),
			section("In 2021 the Firm allocated profitable trades to Oceanic [F1]; penalties for it may be time-barred.", "s")},
		"prosecute": {supported, supported}, "reproduce": {noAnswers, noAnswers}, "audit_gaps": {noGaps, noGaps},
	}), Config{})
	task := f.submit(0)
	f.step(context.Background())
	v, _ := e.Get(context.Background(), task.ID)
	fin := f.outputs(v.ID, "finish")
	if stop(v) != "awaiting_gate/finished" || len(fin) != 1 || !strings.Contains(deref(v.Output), "may be time-barred") || strings.Contains(deref(v.Output), "Coverage: I1") {
		t.Fatalf("%s %q\n%s", stop(v), fin, deref(v.Output))
	}
	var reqs []string
	crit := 0
	for _, c := range fake.Calls() {
		switch c.Agent {
		case "draft_section":
			reqs = append(reqs, fmt.Sprint(c.Messages[1].Content))
		case "prosecute":
			crit++
		}
	}
	if len(reqs) != 2 || strings.Contains(reqs[0], `"analysis"`) || !strings.Contains(reqs[1], `"analysis":{"rule":"Civil penalties`) || !strings.Contains(reqs[1], "Coverage pass") || crit != 2 {
		t.Fatalf("%d critics; draft requests %q", crit, reqs)
	}
	if touched, _ := e.touched(context.Background(), task.ID); len(touched) != 0 {
		t.Fatalf("still touched: %+v", touched)
	}
}
