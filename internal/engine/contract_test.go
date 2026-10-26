// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package engine

import (
	"maps"
	"reflect"
	"slices"
	"testing"
)

// TestGoContractReproducesTheLists pins every list of what Go needs from a tool name, as it was
// written by hand before the one table (goContract): membership, order where order is read, the
// figure reader's empty caller lists, and goDepends' user-visible reason for every built-in name.
func TestGoContractReproducesTheLists(t *testing.T) {
	eq := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %#v\nwant %#v", what, got, want)
		}
	}
	eq("MemoVisible", MemoVisible, []string{"retrieve", "ledger", "sweep", "extract_facts", "analyze_issue", "check_deviation", "numbers", "join_discrepancies", "draft_section", "committee", "finish", "use_skill"})
	eq("goCallers", goCallers, map[string][]string{
		"verify_quote": {"Go: admit (every quote's paraphrase judge)"}, "read_figures": {"Go: admission and drafted sections (figures.go)"},
		"judge_figures": {"Go: join_discrepancies, finish (judge.go)"}, "extract_facts": {"sweep"}, "analyze_issue": {"cover"}, "draft_section": {"cover"},
		"prosecute": {"committee"}, "reproduce": {"committee"}, "audit_gaps": {"committee"}, "committee": {"Go: finish, a stopped run's review"},
		"cover": {"Go: finish, a stopped run"}, "finish": {"Go: a stopped run's forced finish"}, "meta": {"Go: finish (the process note)"},
		"section_check": {"Go: check_deviation's requirement check"}, "figure_context": {}, "exact_calc": {},
	})
	set := func(xs []string) []string { return slices.Sorted(slices.Values(xs)) }
	eq("goWritten", set(goWritten), set([]string{"verify_quote", "read_figures", "judge_figures", "extract_facts", "analyze_issue", "draft_section", "prosecute", "reproduce", "audit_gaps", "committee", "cover", "finish"}))
	eq("goInputs", goInputs, map[string][]string{"finish": {`{"op":"submit","limitations":null}`}, "committee": {`{}`}, "cover": {`{}`}})
	eq("passes", set(passes), set([]string{"sweep", "committee", "cover", "finish", "ledger"}))
	eq("reservedNames", set(reservedNames), set([]string{"lead", "meta", "manage_test", "sweep", "committee", "cover", "finish", "ledger"}))
	eq("ledgerWriters", set(ledgerWriters), set([]string{"ledger", "sweep", "committee", "cover", "finish", "numbers", "join_discrepancies", "use_skill", "read_figures", "judge_figures"}))
	eq("hookedTools", set(hookedTools), set([]string{"extract_facts", "analyze_issue", "check_deviation", "draft_section"}))
	eq("forcedSteps", set(forcedSteps), set([]string{"committee", "cover"}))
	eq("subagentSafe", set(subagentSafe), []string{"ledger"})
	eq("critics", critics, []string{"prosecute", "reproduce", "audit_gaps"})

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
