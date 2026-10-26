// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/discover-legal/BigLaw/internal/analysis"
	"github.com/discover-legal/BigLaw/internal/model"
)

// TestGoContractReproducesTheLists pins every list of what Go needs from a tool name as it was
// written by hand before the one table (goContract): membership, order where order is read, the
// figure reader's empty caller lists, and goDepends' user-visible reason for every built-in name.
// The lists are now queries on goContract (named, needs, goCallers, and its rows' sent inputs); the
// values are as they were. Order is read for two of them: MemoVisible and critics are goContract's
// rows in table order, so their exact slices below pin the table's row order too (moving a row in
// goContract fails this test).
func TestGoContractReproducesTheLists(t *testing.T) {
	eq := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", what, got, want)
		}
	}
	callers, inputs := map[string][]string{}, map[string][]string{}
	for _, c := range goContract {
		if by := goCallers(c.name); by != nil {
			callers[c.name] = by
		}
		if c.sent != "" {
			inputs[c.name] = []string{c.sent}
		}
	}
	if goCallers("no_such_tool") != nil || needs("no_such_tool", ^goNeeds(0)) {
		t.Error("a name not in the table has callers or needs")
	}
	eq("MemoVisible", MemoVisible, []string{"retrieve", "ledger", "sweep", "extract_facts", "analyze_issue", "check_deviation", "numbers", "join_discrepancies", "draft_section", "committee", "finish", "use_skill"})
	eq("goCallers", callers, map[string][]string{
		"verify_quote": {"Go: admit (every quote's paraphrase judge)"}, "read_figures": {"Go: admission and drafted sections (figures.go)"},
		"judge_figures": {"Go: join_discrepancies, finish (judge.go)"}, "extract_facts": {"sweep"}, "analyze_issue": {"cover"}, "draft_section": {"cover"},
		"prosecute": {"committee"}, "reproduce": {"committee"}, "audit_gaps": {"committee"}, "committee": {"Go: finish, a stopped run's review"},
		"cover": {"Go: finish, a stopped run"}, "finish": {"Go: a stopped run's forced finish"}, "meta": {"Go: finish (the process note)"},
		"section_check": {"Go: check_deviation's requirement check"}, "figure_context": {}, "exact_calc": {},
	})
	set := func(xs []string) []string { return slices.Sorted(slices.Values(xs)) }
	eq("goWritten", set(named(goWrites)), set([]string{"verify_quote", "read_figures", "judge_figures", "extract_facts", "analyze_issue", "draft_section", "prosecute", "reproduce", "audit_gaps", "committee", "cover", "finish"}))
	eq("goInputs", inputs, map[string][]string{"finish": {`{"op":"submit","limitations":null}`}, "committee": {`{}`}, "cover": {`{}`}})
	eq("passes", set(named(enginePass)), set([]string{"sweep", "committee", "cover", "finish", "ledger"}))
	eq("reservedNames", set(reservedNames), set([]string{"lead", "meta", "manage_test", "sweep", "committee", "cover", "finish", "ledger"}))
	eq("ledgerWriters", set(named(writesLedger)), set([]string{"ledger", "sweep", "committee", "cover", "finish", "numbers", "join_discrepancies", "use_skill", "read_figures", "judge_figures"}))
	eq("hookedTools", set(named(hookedSub)), set([]string{"extract_facts", "analyze_issue", "check_deviation", "draft_section"}))
	eq("forcedSteps", set(named(forcedStep)), set([]string{"committee", "cover"}))
	eq("subagentSafe", set(named(subagentOK)), []string{"ledger"})
	eq("critics", critics, []string{"prosecute", "reproduce", "audit_gaps"})
	eq("banditPinned", named(pinned), []string{"retrieve", "ledger", "committee", "finish"})
	eq("banditSkip", named(unhookable), []string{"verify_quote"})
	eq("checkTurn", named(checkTurn), []string{"check_deviation"})

	names := slices.Sorted(maps.Keys(builtinSpecs.specs))
	for _, n := range []string{"meta", "section_check", "no_such_tool"} {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	turns, unhook, depends := map[string]int{}, []string{}, map[string]string{}
	for _, n := range names {
		if k := goTurns(n); k != 0 {
			turns[n] = k
		}
		if unhooked(builtinSpecs.specs[n]) {
			unhook = append(unhook, n)
		}
		if why := goDepends(n); why != "" {
			depends[n] = why
		}
	}
	eq("goTurns", turns, map[string]int{"draft_section": 1, "extract_facts": 1, "analyze_issue": 1})
	eq("unhooked", unhook, []string{"finish", "verify_quote"})
	lead := "it is one of the memo lead's tools"
	eq("goDepends", depends, map[string]string{
		"retrieve": lead, "ledger": lead, "sweep": lead, "extract_facts": lead, "analyze_issue": lead, "check_deviation": lead, "numbers": lead,
		"join_discrepancies": lead, "draft_section": lead, "committee": lead, "finish": lead, "use_skill": lead,
		"verify_quote": "Go calls it (Go: admit (every quote's paraphrase judge))", "read_figures": "Go calls it (Go: admission and drafted sections (figures.go))",
		"judge_figures": "Go calls it (Go: join_discrepancies, finish (judge.go))", "prosecute": "Go calls it (committee)", "reproduce": "Go calls it (committee)",
		"audit_gaps": "Go calls it (committee)", "cover": "Go calls it (Go: finish, a stopped run)", "meta": "Go calls it (Go: finish (the process note))",
		"section_check": "Go calls it (Go: check_deviation's requirement check)", "figure_context": "the figure reader calls it", "exact_calc": "the figure reader calls it",
	})
	eq("leadVisible(builtin)", leadVisible(builtinSpecs), MemoVisible)
}

// TestWrappedToolsDeclareChecks keeps goContract's checks honest: every tool Go wraps (hooked's
// subagents, and the numbers and join_discrepancies tools runtime puts behind sheets and aliasJoin)
// declares at least one, each a non-empty line, and only finish (its lint) declares checks without
// being wrapped.
func TestWrappedToolsDeclareChecks(t *testing.T) {
	an := analysis.Tools(nil)
	wrapped := append(named(hookedSub), an[0].Spec().Name, an[1].Spec().Name) // runtime: sheets{an[0]}, aliasJoin{an[1]}
	if !slices.Contains(wrapped, "numbers") || !slices.Contains(wrapped, "join_discrepancies") {
		t.Fatalf("runtime's wrapped analysis tools moved: %v", wrapped)
	}
	for _, c := range goContract {
		if slices.Contains(wrapped, c.name) != (len(c.checks) > 0) && c.name != "finish" {
			t.Errorf("%s: wrapped %v, %d checks", c.name, slices.Contains(wrapped, c.name), len(c.checks))
		}
		for _, l := range c.checks {
			if strings.TrimSpace(l) == "" || !strings.HasSuffix(l, ".") {
				t.Errorf("%s: check %q is not a sentence", c.name, l)
			}
		}
	}
	if len(goChecks("finish")) == 0 {
		t.Error("finish declares no checks (its lint)")
	}
}

// TestEveryToolOpMatchesItsOutputSchema (bench6: numbers op "handles" always failed its output
// schema, "truncated" missing, and the error named the output path "input.truncated"): the lead calls
// every op of every tool it sees, and no answer fails its tool's output schema.
func TestEveryToolOpMatchesItsOutputSchema(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	d1 := f.docs[0].ID
	out, err := f.d.Outline(context.Background(), f.ids(), d1)
	if err != nil || len(out) == 0 {
		t.Fatal(err, out)
	}
	sec := out[0].SectionID
	nulls := func(kv ...any) m {
		a := m{}
		for i := 0; i < len(kv); i += 2 {
			a[kv[i].(string)] = kv[i+1]
		}
		return a
	}
	num := func(op string, kv ...any) model.ToolCall {
		a := m{"op": op, "fn": nil, "operands": nil, "holidays": nil, "fids": nil, "salientTop": nil}
		for k, v := range nulls(kv...) {
			a[k] = v
		}
		return tc("numbers", a)
	}
	ret := func(op string, kv ...any) model.ToolCall {
		a := m{"op": op, "query": nil, "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil}
		for k, v := range nulls(kv...) {
			a[k] = v
		}
		return tc("retrieve", a)
	}
	lead := []model.ToolCall{
		plan(issue("I1", "What does Greystone claim?", "Conduct")),
		ret("search", "query", "damages"), ret("outline", "documentId", d1), ret("section", "documentId", d1, "sectionId", sec),
		extract(d1, "damages", "I1"),
		ledgerOp("findings"), ledgerOp("get", "fid", "F1", "detail", "full"), ledgerOp("relations"),
		num("calc", "fn", "sum", "operands", []m{{"ref": "F1.v1", "literal": nil, "unit": nil}, {"ref": nil, "literal": "10", "unit": "USD"}}),
		num("handles"), num("handles", "fids", []string{"F1"}, "salientTop", 1), num("table"),
		tc("join_discrepancies", m{"fids": nil, "aliases": nil}),
		draft("I1", "F1"), ledgerOp("draft_get", "issueId", "I1"),
		committee(), ledgerOp("critics"), ledgerOp("resolve", "criticId", "C1", "resolution", "fixed", "note", "redrafted"),
		tc("sweep", m{"over": "sections", "documentIds": nil}),
		finishCall("check"), finishCall("submit"),
	}
	var script []model.Reply
	for _, c := range lead {
		script = append(script, calls(c))
	}
	v := f.run(f.with(map[string][]model.Reply{"lead": script}), Config{})
	ss, _ := f.e.Steps(context.Background(), v.ID, 0)
	seen := map[string]bool{}
	for _, s := range ss {
		if s.Kind != "tool_call" {
			continue
		}
		seen[s.Tool] = true
		if b := string(s.Output) + " " + s.Summary; strings.Contains(b, "does not match its schema") || strings.Contains(b, "output.") {
			t.Errorf("step %d %s: %s", s.Step, s.Tool, b)
		}
	}
	for _, tool := range []string{"retrieve", "ledger", "extract_facts", "numbers", "join_discrepancies", "draft_section", "committee", "sweep", "finish"} {
		if !seen[tool] {
			t.Errorf("%s was never called: %s", tool, f.steps(v.ID))
		}
	}
}

// bench6SECTools is the bench6 SEC run's lead tool calls, in order, from its step log
// (biglaw-bench6.pgdump, task 68a266cb: committee 7 times by the lead, $4.16 over 9 cycles with the
// engine's two, while the Bellini block's verified findings were never drafted).
const bench6SECTools = `ledger sweep analyze_issue analyze_issue analyze_issue join_discrepancies ledger ledger ledger ledger ledger ledger draft_section draft_section draft_section draft_section ledger draft_section committee ledger ledger ledger ledger ledger ledger ledger ledger ledger ledger ledger draft_section draft_section draft_section draft_section retrieve retrieve retrieve extract_facts extract_facts extract_facts numbers extract_facts extract_facts numbers draft_section committee ledger ledger ledger ledger draft_section ledger draft_section draft_section extract_facts draft_section ledger committee ledger ledger ledger draft_section draft_section committee ledger ledger ledger ledger ledger ledger ledger ledger ledger ledger draft_section draft_section draft_section draft_section committee ledger ledger ledger ledger ledger draft_section draft_section draft_section draft_section committee ledger ledger ledger ledger ledger ledger ledger draft_section draft_section draft_section draft_section finish numbers ledger draft_section draft_section draft_section draft_section finish numbers numbers draft_section draft_section draft_section draft_section committee ledger ledger ledger ledger ledger ledger ledger ledger ledger ledger ledger ledger draft_section draft_section draft_section draft_section draft_section draft_section finish`

// TestBench6SECCommitteeCapped (bench6 SEC: nine committee cycles, $4.16, while drafting starved and
// verified findings stayed uncited): the lead's bench6 calls replayed over a SEC-shaped matter priced
// as bench5 measured. The lead runs at most maxCycles cycles (the rest are refused with what to do
// instead), finish accepts a draft changed after the capped committee (disclosed), and the coverage
// pass spends what the cycles left on placing left-out findings, in more than one round.
func TestBench6SECCommitteeCapped(t *testing.T) {
	t.Parallel()
	var docTexts []string
	n := 0
	for _, pages := range []int{60, 40, 42} {
		var b strings.Builder
		for i := 1; i <= pages; i++ {
			n++
			fmt.Fprintf(&b, "%d. Allegation %d\n%s\n%s\n\n", i, n, secItem(n, 0), secItem(n, 1))
		}
		docTexts = append(docTexts, b.String())
	}
	f := newFixture(t, docTexts...)
	lead := []model.Reply{calls(plan(issue("I1", "What are the specific allegations against the Firm?", "RedFlag"), issue("I2", "What evidence supports each allegation?", "RedFlag"),
		issue("I3", "What compliance violations are identified?", "Conduct"), issue("I4", "What are the key defense priorities?", "Conduct"),
		issue("I5", "What quantitative findings does the analysis reveal?", "RedFlag"), issue("I6", "What is the chronology of relevant events?", "Requirement")))}
	k, a, cycles, crit := 0, 0, 0, 0
	for i, tool := range strings.Fields(bench6SECTools) {
		var c model.ToolCall
		switch tool {
		case "ledger":
			if i == 0 {
				continue // the plan, above
			}
			c = ledgerOp("findings")
			if crit < cycles*3 { // the bench lead resolved the critics' lines between cycles
				crit++
				c = ledgerOp("resolve", "criticId", fmt.Sprintf("C%d", crit), "resolution", "disclosed", "note", "noted for the reviewing lawyer")
			}
		case "sweep":
			c = tc("sweep", m{"over": "sections", "documentIds": nil})
		case "analyze_issue":
			a++
			c = tc("analyze_issue", m{"issueId": fmt.Sprintf("I%d", []int{3, 4, 6}[(a-1)%3]), "question": "What does the law make of it?", "jurisdiction": "US", "packs": nil})
		case "join_discrepancies":
			c = tc("join_discrepancies", m{"fids": nil, "aliases": nil})
		case "draft_section":
			k++
			c = draft(fmt.Sprintf("I%d", (k-1)%6+1), fmt.Sprintf("F%d", (k-1)%6+1), fmt.Sprintf("F%d", (k-1)%6+7))
		case "committee":
			c, cycles = committee(), cycles+1
		case "retrieve":
			c = tc("retrieve", m{"op": "search", "query": "failed to disclose", "documentId": nil, "sectionId": nil, "k": nil, "offset": nil, "limit": nil})
		case "extract_facts":
			c = extract(f.docs[0].ID, "allocation", "I2")
		case "numbers":
			c = tc("numbers", m{"op": "handles", "fn": nil, "operands": nil, "holidays": nil, "fids": nil, "salientTop": nil})
		case "finish":
			c = finishCall("submit")
		}
		lead = append(lead, calls(c))
	}
	chat := &secShaped{lead: lead}
	prices := testPrices(t)
	f.e = New(f.st, f.d, chat, testRoles, prices, Config{BudgetCalls: 900, BudgetUSD: 11, CollapseThreshold: 0.5, CollapseAction: "strict"}, nil)
	task := f.submit(0)
	f.step(context.Background())
	v := f.get(task.ID)
	ss, _ := f.e.Steps(context.Background(), task.ID, 0)
	var leadCycles []string
	for _, s := range ss {
		if s.Tool == "committee" && s.Kind == "tool_call" {
			leadCycles = append(leadCycles, s.Status)
		}
	}
	cyc, _ := load[Cycle](context.Background(), f.e, task.ID, kCycle)
	c, _ := f.e.Cost(context.Background(), task.ID)
	cov, _ := f.e.coverRow(context.Background(), task.ID)
	placed := 0
	for _, r := range chat.reqs {
		placed += btoi(r.Agent == "draft_section" && strings.Contains(fmt.Sprint(r.Messages[1].Content), "These findings never reached the memo"))
	}
	committed := 0
	for _, s := range leadCycles {
		committed += btoi(s == "committed")
	}
	if committed != maxCycles || len(leadCycles) <= maxCycles || len(cyc) > maxCycles+1 {
		t.Errorf("lead committee steps %v (want %d committed, the rest refused), %d cycles in the ledger", leadCycles, maxCycles, len(cyc))
	}
	if c.Summary.USD == nil || *c.Summary.USD > 11 || cov == nil || placed <= 6 || v.Output == nil {
		cs, _ := json.Marshal(cov)
		fin := f.outputs(task.ID, "finish")
		t.Fatalf("%s: cost %+v, cover %s, %d placement redrafts\n%s\n%.3000s", stop(v), c.Summary, cs, placed, f.steps(task.ID), fin[len(fin)-1])
	}
	t.Logf("spent $%.2f in %d calls; %d cycles; %d placement redrafts; %d left out after cover", *c.Summary.USD, c.Summary.Calls, len(cyc), placed, len(cov.LeftOut))
}
