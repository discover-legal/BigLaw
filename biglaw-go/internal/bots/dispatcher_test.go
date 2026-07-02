// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package bots

import (
	"strings"
	"testing"

	"github.com/discover-legal/biglaw-go/internal/types"
)

// fakeFacade records the budget/docket calls the dispatcher makes.
type fakeFacade struct {
	budgetStatusOf string
	setBudget      [2]string
	watch          [3]string
	unwatch        string
	listedDockets  bool
}

func (f *fakeFacade) ListTasks() []*types.Task                       { return nil }
func (f *fakeFacade) GetTask(string) *types.Task                     { return nil }
func (f *fakeFacade) SubmitTask(string, string) (*types.Task, error) { return &types.Task{}, nil }
func (f *fakeFacade) ListTemplates() []types.TaskTemplate            { return nil }
func (f *fakeFacade) SubmitFromTemplate(string) (*types.Task, error) { return &types.Task{}, nil }
func (f *fakeFacade) Clients() ClientLookup                          { return nil }
func (f *fakeFacade) Knowledge() KnowledgeSearcher                   { return nil }
func (f *fakeFacade) Briefing() BriefingGenerator                    { return nil }
func (f *fakeFacade) ListTimeEntries() []types.TimeEntry             { return nil }
func (f *fakeFacade) LPMReport(string) (string, error)               { return "report", nil }
func (f *fakeFacade) LPMPortfolio() (string, error)                  { return "portfolio", nil }

func (f *fakeFacade) BudgetStatus(mn string) (string, error) {
	f.budgetStatusOf = mn
	return "burn for " + mn, nil
}
func (f *fakeFacade) SetMatterBudget(mn, amount string) (string, error) {
	f.setBudget = [2]string{mn, amount}
	return "budget set", nil
}
func (f *fakeFacade) WatchDocket(mn, docket, court string) (string, error) {
	f.watch = [3]string{mn, docket, court}
	return "watching", nil
}
func (f *fakeFacade) UnwatchDocket(mn string) (string, error) {
	f.unwatch = mn
	return "unwatched", nil
}
func (f *fakeFacade) ListDockets() (string, error) {
	f.listedDockets = true
	return "docket list", nil
}

func TestDispatchBudgetStatusVsSet(t *testing.T) {
	f := &fakeFacade{}
	// No amount → status (read).
	r := Dispatch(BotMessage{Text: "@BigMichael budget M-001"}, f)
	if f.budgetStatusOf != "M-001" || r.Immediate != "burn for M-001" {
		t.Fatalf("budget status: facade=%q resp=%q", f.budgetStatusOf, r.Immediate)
	}
	// With amount → set.
	r = Dispatch(BotMessage{Text: "@BigMichael budget M-002 50000"}, f)
	if f.setBudget != [2]string{"M-002", "50000"} || r.Immediate != "budget set" {
		t.Fatalf("budget set: facade=%v resp=%q", f.setBudget, r.Immediate)
	}
}

func TestDispatchWatchUnwatchDockets(t *testing.T) {
	f := &fakeFacade{}
	Dispatch(BotMessage{Text: "@BigMichael watch M-001 1:23-cv-456 cand"}, f)
	if f.watch != [3]string{"M-001", "1:23-cv-456", "cand"} {
		t.Errorf("watch parsed wrong: %v", f.watch)
	}
	Dispatch(BotMessage{Text: "@BigMichael unwatch M-001"}, f)
	if f.unwatch != "M-001" {
		t.Errorf("unwatch parsed wrong: %q", f.unwatch)
	}
	r := Dispatch(BotMessage{Text: "@BigMichael dockets"}, f)
	if !f.listedDockets || r.Immediate != "docket list" {
		t.Errorf("dockets command failed: %v %q", f.listedDockets, r.Immediate)
	}
}

func TestDispatchCommandUsageMessages(t *testing.T) {
	f := &fakeFacade{}
	if r := Dispatch(BotMessage{Text: "@BigMichael budget"}, f); !strings.Contains(r.Immediate, "Usage") {
		t.Error("empty budget args should show usage")
	}
	if r := Dispatch(BotMessage{Text: "@BigMichael watch M-001"}, f); !strings.Contains(r.Immediate, "Usage") {
		t.Error("incomplete watch args should show usage")
	}
}

func TestDispatchReportPortfolioAsync(t *testing.T) {
	f := &fakeFacade{}
	r := Dispatch(BotMessage{Text: "@BigMichael report M-001"}, f)
	if r.AsyncWork == nil {
		t.Fatal("report should run async")
	}
	out, _ := r.AsyncWork()
	if out != "report" {
		t.Errorf("report async result: %q", out)
	}
}
